import {
  checkPassword,
  ClientMessage,
  Close,
  Hello,
  label,
  SUBPROTOCOL,
  type TicketBody,
  verifyTicket
} from "@sloppa/preview"
import { NodeHttpServer } from "@effect/platform-node"
import { Duration, Effect, FiberSet, Layer, Option, Redacted, Schema } from "effect"
import { HttpServer } from "effect/http"
import { createServer, type IncomingMessage, type OutgoingHttpHeaders } from "node:http"
import type { Duplex } from "node:stream"
import { type WebSocket, WebSocketServer } from "ws"
import { AppConfig, type PreviewConfig } from "../Config.ts"
import { clientAddr, hostOf, Misses } from "./Guard.ts"
import { type Lease, Leases } from "./Leases.ts"
import { page, rawPage } from "./Page.ts"
import { forward, forwardUpgrade } from "./Proxy.ts"

const decodeHello = Schema.decodeUnknownOption(Schema.fromJsonString(Hello))
const decodeMessage = Schema.decodeUnknownOption(Schema.fromJsonString(ClientMessage))

const truncate = (value: string, max: number) => (value.length <= max ? value : `${value.slice(0, max)}…`)

/** The password from Basic auth; any user name goes, the password is the secret. */
const basicPassword = (header: string | undefined) => {
  const match = /^basic\s+(\S+)$/i.exec(header ?? "")
  if (match === null) return undefined
  const decoded = Buffer.from(match[1]!, "base64").toString("utf8")
  const colon = decoded.indexOf(":")
  return colon === -1 ? undefined : decoded.slice(colon + 1)
}

type Refusal = readonly [status: number, title: string, body: string, headers?: OutgoingHttpHeaders]

const NO_PREVIEW: Refusal = [
  404,
  "No preview here",
  "Nothing is running at this address. Previews stop when their <code>sloppa preview run</code> does."
]
const NOT_FOUND: Refusal = [404, "Not found", ""]

type Route = { readonly _tag: "Apex"; readonly path: string } | { readonly _tag: "Preview"; readonly label: string }

/**
 * Previews, routed by Host alone: the apex takes the CLI's tunnels, `<label>.<apex>` is a preview,
 * and no header ever picks where a request goes beyond the label.
 */
const make = Effect.fnUntraced(function* (config: PreviewConfig, trustProxy: boolean) {
  const leases = yield* Leases
  const secret = Redacted.value(config.secret)
  const apex = config.origin.hostname
  const misses = new Misses(config.missBurst, Duration.toMillis(config.missEvery))
  const tooMany: Refusal = [
    429,
    "Slow down",
    "Too many requests for previews that don't exist or wrong passwords. Try again shortly.",
    { "retry-after": misses.retryAfter }
  ]
  const websockets = (maxPayload: number) =>
    new WebSocketServer({
      noServer: true,
      perMessageDeflate: false,
      maxPayload,
      handleProtocols: (protocols) => (protocols.has(SUBPROTOCOL) ? SUBPROTOCOL : false)
    })
  const controls = websockets(16 << 10)
  const data = websockets(1 << 20)
  const upgradeTo = (server: WebSocketServer, request: IncomingMessage, socket: Duplex, early: Buffer) =>
    Effect.callback<WebSocket>((resume) =>
      server.handleUpgrade(request, socket, early, (ws) => resume(Effect.succeed(ws)))
    )

  /** Where a request for the preview domain goes; anything else is the app's. */
  const routeOf = (request: IncomingMessage): Route | undefined => {
    const host = hostOf(request.headers.host)
    if (host === apex) return { _tag: "Apex", path: new URL(request.url ?? "/", "http://x").pathname }
    if (host.endsWith(`.${apex}`)) return { _tag: "Preview", label: host.slice(0, -apex.length - 1) }
    return undefined
  }

  /** The lease a preview request may go through, or why not. Unknown names and wrong passwords cost a miss. */
  const admit = (
    request: IncomingMessage,
    label: string,
    client: string | undefined
  ): { readonly lease: Lease } | { readonly refusal: Refusal } => {
    if (misses.blocked(client)) return { refusal: tooMany }
    const lease = Option.getOrUndefined(leases.lookup(label))
    if (lease === undefined) {
      misses.miss(client)
      return { refusal: NO_PREVIEW }
    }
    const password = basicPassword(request.headers.authorization)
    if (lease.ticket.passwordMac !== undefined && !checkPassword(secret, lease.ticket, password ?? "")) {
      misses.miss(client)
      return {
        refusal: [
          401,
          "Password needed",
          "This preview has a password. Any user name works.",
          { "www-authenticate": `Basic realm="${label}", charset="UTF-8"` }
        ]
      }
    }
    return { lease }
  }

  const onRequest = (route: Route, request: IncomingMessage, response: Parameters<typeof page>[0]) =>
    Effect.suspend(() => {
      switch (route._tag) {
        case "Apex":
          if (route.path === "/healthz") {
            response.writeHead(200, { "content-type": "text/plain; charset=utf-8" })
            return Effect.sync(() => response.end("ok\n"))
          }
          return Effect.sync(() =>
            page(
              response,
              404,
              "sloppa preview",
              "Previews live on their own subdomains. Start one with <code>sloppa preview run -- &lt;command&gt;</code>."
            )
          )
        case "Preview": {
          const client = clientAddr(request, trustProxy)
          const admitted = admit(request, route.label, client)
          if ("refusal" in admitted) return Effect.sync(() => page(response, ...admitted.refusal))
          return forward(admitted.lease, request, response, client)
        }
      }
    })

  const onUpgrade = (route: Route, request: IncomingMessage, socket: Duplex, early: Buffer) =>
    Effect.suspend(() => {
      socket.on("error", () => socket.destroy())
      const client = clientAddr(request, trustProxy)
      const refuse = (refusal: Refusal) => Effect.sync(() => rawPage(socket, ...refusal))
      switch (route._tag) {
        case "Preview": {
          const admitted = admit(request, route.label, client)
          if ("refusal" in admitted) return refuse(admitted.refusal)
          return forwardUpgrade(admitted.lease, request, socket, early, client)
        }
        case "Apex":
          if (misses.blocked(client)) return refuse(tooMany)
          if (route.path === "/_connect") return connect(request, socket, early, client)
          if (route.path.startsWith("/_data/")) {
            const conn = route.path.slice("/_data/".length)
            const lease = Option.getOrUndefined(leases.pending(conn))
            if (lease === undefined) {
              misses.miss(client)
              return refuse(NOT_FOUND)
            }
            return upgradeTo(data, request, socket, early).pipe(
              Effect.map((ws) => lease.accept(conn, ws) || ws.terminate())
            )
          }
          return refuse(NOT_FOUND)
      }
    })

  /** A CLI's control socket: its lease lives exactly as long as it does. */
  const connect = (request: IncomingMessage, socket: Duplex, early: Buffer, client: string | undefined) =>
    Effect.gen(function* () {
      const token = /^bearer\s+(\S+)$/i.exec(request.headers.authorization ?? "")?.[1] ?? ""
      const verdict = verifyTicket(secret, token, Date.now())
      if (verdict._tag === "Invalid") misses.miss(client)
      // Accepted even for a bad ticket, so the CLI reads why in the close reason.
      const ws = yield* upgradeTo(controls, request, socket, early)
      ws.on("error", () => ws.terminate())
      if (ws.protocol !== SUBPROTOCOL) {
        return ws.close(Close.Protocol, "this CLI is too old or too new for this server")
      }
      if (verdict._tag === "Invalid") return ws.close(Close.Ticket, verdict.reason)
      const hello = yield* firstMessage(ws).pipe(
        Effect.map(decodeHello),
        Effect.timeoutOption("10 seconds"),
        Effect.map(Option.flatten)
      )
      if (Option.isNone(hello)) return ws.close(Close.Protocol, "expected a hello with the dev server's port")
      yield* serve(ws, verdict.ticket, hello.value, client)
    })

  const serve = (ws: WebSocket, ticket: TicketBody, hello: Hello, client: string | undefined) =>
    Effect.suspend(() => {
      const trimmed: Hello = {
        type: "hello",
        port: hello.port,
        ...(hello.command === undefined ? {} : { command: truncate(hello.command, 300) }),
        ...(hello.cwd === undefined ? {} : { cwd: truncate(hello.cwd, 300) }),
        ...(hello.hostname === undefined ? {} : { hostname: truncate(hello.hostname, 100) })
      }
      const url = `${config.origin.protocol}//${label(ticket)}.${config.origin.host}/`
      // The CLI's messages are small and answered on the spot; the lease's lifetime is `serve`'s.
      ws.on("message", (raw) => {
        const lease = Option.getOrUndefined(leases.lookup(label(ticket)))
        if (lease === undefined || lease.control !== ws) return
        lease.seen()
        const message = decodeMessage((raw as Buffer).toString("utf8"))
        if (Option.isNone(message)) return lease.end({ code: Close.Protocol, reason: "unreadable message" })
        switch (message.value.type) {
          case "ping":
            return lease.send({ type: "pong" })
          case "refuse":
            return lease.refuse(message.value.conn, truncate(message.value.reason ?? "refused", 300))
        }
      })
      return leases.serve({ ticket, hello: trimmed, url, control: ws })
    }).pipe(Effect.annotateLogs({ client }))

  return { routeOf, onRequest, onUpgrade }
})

const firstMessage = (ws: WebSocket) =>
  Effect.callback<string>((resume) => {
    const onMessage = (raw: unknown) => resume(Effect.succeed(String(raw)))
    ws.once("message", onMessage)
    return Effect.sync(() => ws.off("message", onMessage))
  })

/**
 * The one HTTP server, for the app and the previews: a request for the preview domain, by Host,
 * goes to the previews, and everything else to the app.
 */
export const layerHttpServer = (options: { readonly host: string; readonly port: number }) =>
  Layer.effect(
    HttpServer.HttpServer,
    Effect.gen(function* () {
      const { preview, trustProxy } = yield* AppConfig
      const previews = preview === undefined ? undefined : yield* make(preview, trustProxy)
      const fibers = yield* FiberSet.make()
      const run = yield* FiberSet.runtime(fibers)()
      // Long uploads and long-lived responses, a dev server's streams included, aren't a timeout's business.
      const server = createServer({ requestTimeout: 0, headersTimeout: 10_000 })
      const emit = server.emit.bind(server)
      // The port opens before the app's routes are up: requests for them wait for their listener.
      const waiting: Array<readonly [event: string, args: Array<any>]> = []
      server.on("newListener", (event) => {
        if (event === "request")
          queueMicrotask(() => waiting.splice(0).forEach(([event, args]) => emit(event, ...args)))
      })
      // Node only emits upgrades once something listens for them, and the app's listener comes later.
      server.on("upgrade", () => {})
      server.emit = ((event: string | symbol, ...args: Array<any>) => {
        if (event !== "request" && event !== "upgrade") return emit(event, ...args)
        const route = previews?.routeOf(args[0])
        if (route !== undefined) {
          run(
            event === "request"
              ? previews!.onRequest(route, args[0], args[1])
              : previews!.onUpgrade(route, args[0], args[1], args[2])
          )
        } else if (server.listenerCount("request") === 0) {
          waiting.push([event, args])
        } else {
          emit(event, ...args)
        }
        return true
      }) as typeof server.emit
      // A CLI's tunnel would hold the close open: the leases end first, and their going-away close
      // sends every CLI to reconnect to the next process.
      const close = server.close.bind(server)
      const runPromise = Effect.runPromiseWith(yield* Effect.context<never>())
      server.close = ((callback?: (error?: Error) => void) => {
        void runPromise(FiberSet.clear(fibers)).then(() => close(callback))
        return server
      }) as typeof server.close
      if (preview !== undefined) yield* Effect.logInfo(`Serving previews for ${preview.origin.origin}`)
      return yield* NodeHttpServer.make(() => server, options)
    })
  ).pipe(Layer.provide(Leases.layer))
