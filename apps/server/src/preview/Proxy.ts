import { splice } from "@sloppa/preview"
import { Effect } from "effect"
import { type IncomingMessage, request as httpRequest, type ServerResponse, STATUS_CODES } from "node:http"
import type { Duplex } from "node:stream"
import { type Lease, RefusedError } from "./Leases.ts"
import { escapeHtml, page, rawPage } from "./Page.ts"

/** Headers that describe one hop rather than the request, never forwarded as they are. */
const HOP_BY_HOP = new Set([
  "connection",
  "proxy-connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade"
])

/** Replaced with the proxy's own, so the dev server can trust what it reads in them. */
const REPLACED = new Set(["host", "forwarded", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto", "expect"])

type Headers = Array<[string, string]>

/** `rawHeaders` as pairs, minus the hop-by-hop ones and whatever `Connection` lists. */
const endToEnd = (raw: ReadonlyArray<string>): Headers => {
  const pairs: Headers = []
  for (let i = 0; i < raw.length; i += 2) pairs.push([raw[i]!, raw[i + 1]!])
  const listed = new Set(
    pairs
      .filter(([name]) => name.toLowerCase() === "connection")
      .flatMap(([, value]) => value.split(",").map((token) => token.trim().toLowerCase()))
  )
  return pairs.filter(([name]) => !HOP_BY_HOP.has(name.toLowerCase()) && !listed.has(name.toLowerCase()))
}

/** What the dev server thinks it's called: a loopback address passes Vite's `allowedHosts` and the like. */
const localHost = (lease: Lease) => `127.0.0.1:${lease.hello.port}`

const requestHeaders = (request: IncomingMessage, lease: Lease, client: string | undefined) => {
  const publicUrl = new URL(lease.url)
  const headers = endToEnd(request.rawHeaders).filter(([name, value]) => {
    const lower = name.toLowerCase()
    // The preview's password is the proxy's business, not the dev server's.
    if (lower === "authorization" && lease.ticket.passwordMac !== undefined) return !/^basic /i.test(value)
    return !REPLACED.has(lower)
  })
  headers.push(["host", localHost(lease)])
  if (client !== undefined) headers.push(["x-forwarded-for", client])
  headers.push(["x-forwarded-host", publicUrl.host], ["x-forwarded-proto", publicUrl.protocol.slice(0, -1)])
  if (/\btrailers\b/i.test(String(request.headers.te ?? ""))) headers.push(["te", "trailers"])
  if (request.headers.upgrade !== undefined) {
    headers.push(["connection", "Upgrade"], ["upgrade", request.headers.upgrade])
  }
  return headers
}

/** Redirects to the dev server's own address point back at the preview. */
export const rewriteLocation = (location: string, port: number, publicUrl: URL) => {
  let url: URL
  try {
    url = new URL(location)
  } catch {
    return location
  }
  if (url.port !== String(port) || !["127.0.0.1", "localhost", "[::1]", "0.0.0.0"].includes(url.hostname)) {
    return location
  }
  url.protocol = publicUrl.protocol
  url.host = publicUrl.host
  return url.toString()
}

/**
 * Drops `Domain` from a cookie unless it's this preview's own host: a cookie set for a parent
 * domain would reach every other preview, and anything else under it.
 */
export const scopeCookie = (cookie: string, host: string) => {
  const [value = "", ...attributes] = cookie.split(";")
  const kept = attributes.filter((attribute) => {
    const [name = "", domain = ""] = attribute.trim().split("=")
    return name.toLowerCase() !== "domain" || domain.replace(/^\./, "").toLowerCase() === host.toLowerCase()
  })
  return [value, ...kept].join(";")
}

const responseHeaders = (upstream: IncomingMessage, lease: Lease) => {
  const publicUrl = new URL(lease.url)
  const headers: Headers = endToEnd(upstream.rawHeaders).map(([name, value]) => {
    switch (name.toLowerCase()) {
      case "location":
        return [name, rewriteLocation(value, lease.hello.port, publicUrl)]
      case "set-cookie":
        return [name, scopeCookie(value, publicUrl.hostname)]
      default:
        return [name, value]
    }
  })
  if (upstream.headers["x-robots-tag"] === undefined) headers.push(["x-robots-tag", "noindex, nofollow"])
  if (upstream.statusCode === 101 && upstream.headers.upgrade !== undefined) {
    headers.push(["connection", "Upgrade"], ["upgrade", upstream.headers.upgrade])
  }
  return headers
}

const statusLine = (upstream: IncomingMessage) =>
  `HTTP/1.1 ${upstream.statusCode} ${upstream.statusMessage || STATUS_CODES[upstream.statusCode ?? 502] || ""}`

const head = (upstream: IncomingMessage, headers: Headers) =>
  `${statusLine(upstream)}\r\n${headers.map(([name, value]) => `${name}: ${value}\r\n`).join("")}\r\n`

const failure = (lease: Lease, error: Error): [number, string, string] =>
  error instanceof RefusedError
    ? [
        502,
        "Dev server not listening",
        `The preview is connected, but nothing listens on 127.0.0.1:${lease.hello.port} yet. If it just started, give it a moment; otherwise make it use <code>$PORT</code>.`
      ]
    : [502, "Preview unreachable", escapeHtml(error.message)]

/** Logs what went wrong in the tunnel, once the exchange is over; a dev server not listening is just a 502. */
const report = (error: Error | undefined) =>
  error === undefined || error instanceof RefusedError
    ? Effect.void
    : Effect.logWarning(`Preview proxy error: ${error.message}`)

const outgoing = (lease: Lease, request: IncomingMessage, client: string | undefined) =>
  httpRequest({
    agent: lease.agent,
    // Only for the agent's pool: every connection is a tunnel to this lease's dev server.
    host: "127.0.0.1",
    port: lease.hello.port,
    method: request.method,
    path: request.url,
    headers: requestHeaders(request, lease, client).flat(),
    setHost: false
  })

/**
 * Passes a request through the tunnel, until the browser's response is done. Bodies stream both
 * ways with backpressure, responses are flushed as they come (server-sent events), and whatever
 * encoding the dev server picked goes through untouched. Interrupting it drops the exchange.
 */
export const forward = (lease: Lease, request: IncomingMessage, response: ServerResponse, client?: string) =>
  Effect.callback<Error | undefined>((resume) => {
    lease.requests += 1
    let failed: Error | undefined
    const out = outgoing(lease, request, client)
    out.on("response", (upstream) => {
      response.writeHead(upstream.statusCode ?? 502, upstream.statusMessage, responseHeaders(upstream, lease).flat())
      response.flushHeaders()
      upstream.on("error", () => response.destroy())
      upstream.pipe(response)
    })
    out.on("error", (error) => {
      failed = error
      if (response.headersSent) return void response.destroy()
      if (!response.destroyed) page(response, ...failure(lease, error))
    })
    request.on("error", () => out.destroy())
    response.on("close", () => {
      // The browser went away: so does the request to the dev server.
      if (!response.writableFinished) out.destroy()
      resume(Effect.succeed(failed))
    })
    request.pipe(out)
    return Effect.sync(() => response.destroy())
  }).pipe(Effect.flatMap(report))

/**
 * Passes an upgrade (Vite's HMR WebSocket, say) through the tunnel, then splices the two
 * connections until either closes.
 */
export const forwardUpgrade = (
  lease: Lease,
  request: IncomingMessage,
  socket: Duplex,
  early: Buffer,
  client?: string
) =>
  Effect.callback<Error | undefined>((resume) => {
    lease.requests += 1
    lease.track(socket)
    let failed: Error | undefined
    const out = outgoing(lease, request, client)
    out.on("upgrade", (upstream, upstreamSocket, upstreamEarly) => {
      upstreamSocket.setTimeout(0)
      lease.track(upstreamSocket)
      socket.write(head(upstream, responseHeaders(upstream, lease)))
      if (upstreamEarly.length > 0) socket.write(upstreamEarly)
      if (early.length > 0) upstreamSocket.write(early)
      void splice(socket, upstreamSocket)
    })
    // The dev server declined the upgrade: its answer goes back as is, then the connection closes.
    out.on("response", (upstream) => {
      socket.write(head(upstream, [...responseHeaders(upstream, lease), ["connection", "close"]]))
      upstream.on("error", () => socket.destroy())
      upstream.pipe(socket)
    })
    out.on("error", (error) => {
      failed = error
      rawPage(socket, ...failure(lease, error))
    })
    socket.on("error", () => out.destroy())
    socket.once("close", () => resume(Effect.succeed(failed)))
    out.end()
    return Effect.sync(() => socket.destroy())
  }).pipe(Effect.flatMap(report))
