import type { PreviewTicket } from "@sloppa/api"
import { Close, FINAL, ServerMessage, SUBPROTOCOL } from "@sloppa/preview"
import { Console, Data, DateTime, Duration, Effect, FiberSet, Layer, Option, Schedule, Schema } from "effect"
import { CliError } from "effect/cli"
import { ChildProcess, ChildProcessSpawner } from "effect/process"
import * as Socket from "effect/socket/Socket"
import { createServer } from "node:net"
import { hostname } from "node:os"
import { WebSocket } from "ws"
import { lookupLocalhost } from "./Localhost.ts"
import { connectLocal, pipe } from "./Pipe.ts"
import { resolveSpawnCommand } from "./Spawn.ts"

/**
 * `ws` sockets for Effect's Socket, each with an error listener of its own. Effect removes its
 * listeners before closing, and `ws` reports closing a socket that's still connecting as an
 * "error" event: with nobody listening, Node would crash the CLI on it. Effect still hears every
 * error that matters through its own listener while it has one.
 */
const WebSocketConstructor = Layer.succeed(Socket.WebSocketConstructor, (url, options) => {
  const ws =
    options === undefined || typeof options === "string" || Array.isArray(options)
      ? new WebSocket(url, options, { lookup: lookupLocalhost })
      : new WebSocket(url, options.protocols, { headers: options.headers, lookup: lookupLocalhost })
  ws.on("error", () => {})
  return ws as unknown as globalThis.WebSocket
})

/** Why the preview ended, when the dev server didn't end it itself. */
class TunnelClosed extends Data.TaggedError("TunnelClosed")<{
  readonly code: number
  readonly reason: string
}> {
  override get message() {
    return `${FINAL[this.code] ?? `closed (${this.code})`}${this.reason ? `: ${this.reason}` : ""}`
  }
}

class Silent extends Data.TaggedError("Silent")<{}> {}

const decodeMessage = Schema.decodeUnknownOption(Schema.fromJsonString(ServerMessage))

const EXPIRY_GRACE = Duration.seconds(5)

/** The server pings every 20 seconds: twice that much silence means the connection is gone. */
const SILENCE = Duration.seconds(45)

/** A free loopback port for the dev server. Freed before it binds again, so it's a guess, but a good one. */
export const freePort = Effect.callback<number, Error>((resume) => {
  const server = createServer()
  server.once("error", (error) => resume(Effect.fail(error)))
  server.listen(0, "127.0.0.1", () => {
    const address = server.address()
    const port = typeof address === "object" && address !== null ? address.port : 0
    server.close(() => resume(Effect.succeed(port)))
  })
})

/** `{port}`, `{host}`, `{url}` and `{id}`, for dev servers that only take flags. */
export const expand = (args: ReadonlyArray<string>, values: { port: number; url: string; id: string }) =>
  args.map((arg) =>
    arg
      .replaceAll("{port}", String(values.port))
      .replaceAll("{host}", "127.0.0.1")
      .replaceAll("{url}", values.url)
      .replaceAll("{id}", values.id)
  )

/**
 * One control connection, for as long as it lasts: it says hello, opens a data socket for each
 * browser connection the server asks for, and answers pings. Fails with TunnelClosed when the server
 * ends the lease for good; any other failure is a reason to reconnect.
 */
const session = Effect.fnUntraced(function* (
  ticket: PreviewTicket,
  port: number,
  command: ReadonlyArray<string>,
  onReady: (url: string) => Effect.Effect<void>
) {
  const ws = yield* Socket.makeWebSocket(ticket.connectUrl, {
    protocols: SUBPROTOCOL,
    headers: { authorization: `Bearer ${ticket.ticket}` },
    openTimeout: "10 seconds"
  }).pipe(Effect.provide(WebSocketConstructor))
  const reader = yield* ws.reader
  const writer = yield* ws.writer
  const send = (message: object) => writer.write(JSON.stringify(message))
  const pull = reader.pull.pipe(
    Effect.timeoutOrElse({ duration: SILENCE, orElse: () => Effect.fail(new Silent()) }),
    Effect.mapError((error) =>
      Socket.isSocketError(error) && error.reason._tag === "SocketCloseError" && FINAL[error.reason.code] !== undefined
        ? new TunnelClosed({ code: error.reason.code, reason: error.reason.closeReason ?? "" })
        : error
    )
  )

  yield* send({
    type: "hello",
    port,
    command: command.join(" "),
    cwd: process.cwd(),
    hostname: hostname()
  })

  const connections = yield* FiberSet.make()
  const dataUrl = (conn: string) => `${ticket.connectUrl.replace(/\/_connect$/, "")}/_data/${encodeURIComponent(conn)}`
  const open = (conn: string) =>
    Effect.gen(function* () {
      const local = yield* connectLocal(port).pipe(Effect.option)
      if (Option.isNone(local)) {
        return yield* send({ type: "refuse", conn, reason: `connection refused on 127.0.0.1:${port}` })
      }
      yield* pipe(local.value, dataUrl(conn))
    }).pipe(
      Effect.scoped,
      Effect.catchCause((cause) => Effect.logDebug("connection ended", cause))
    )

  while (true) {
    const frames = yield* pull
    for (const frame of frames) {
      const message = decodeMessage(typeof frame === "string" ? frame : new TextDecoder().decode(frame))
      if (Option.isNone(message)) continue
      switch (message.value.type) {
        case "ready":
          yield* onReady(message.value.url)
          break
        case "open":
          yield* FiberSet.run(connections, open(message.value.conn))
          break
        case "ping":
          yield* send({ type: "pong" })
          break
      }
    }
  }
}, Effect.scoped)

/**
 * Keeps the tunnel up until the server ends the lease: through network blips and server restarts,
 * the same ticket reconnects to the same URL. Only ever fails, with why the preview is over. The
 * ticket's expiry ends it here too, so a preview whose server is gone doesn't retry forever.
 */
export const tunnel = (
  ticket: PreviewTicket,
  port: number,
  command: ReadonlyArray<string>,
  onReady: (url: string) => Effect.Effect<void>
) => {
  let ready = false
  const connected = session(ticket, port, command, (url) =>
    Effect.suspend(() => {
      const first = !ready
      ready = true
      return first ? onReady(url) : Console.error(`sloppa: reconnected to ${url}`)
    })
  ).pipe(
    Effect.tapError((error) =>
      error._tag === "TunnelClosed"
        ? Effect.void
        : Console.error(`sloppa: tunnel lost (${error._tag === "Silent" ? "no ping" : error.message}), reconnecting`)
    ),
    Effect.retry({
      while: (error) => error._tag !== "TunnelClosed",
      // Doubling from half a second, then every 10 seconds at most.
      schedule: Schedule.min([Schedule.exponential("500 millis"), Schedule.spaced("10 seconds")])
    }),
    // Retrying forever, only a TunnelClosed gets out; this tells the types as much.
    Effect.mapError((error) =>
      error._tag === "TunnelClosed" ? error : new TunnelClosed({ code: 0, reason: error.message })
    ),
    Effect.andThen(Effect.never)
  )
  // A backstop, a little late: when the server is reachable, its own verdict comes first. A tunnel
  // that never came up didn't run its time: its ticket was dead on arrival, as the server would say.
  const expiry = DateTime.now.pipe(
    Effect.flatMap((now) => Effect.sleep(Duration.sum(DateTime.distance(now, ticket.expiresAt), EXPIRY_GRACE))),
    Effect.andThen(
      Effect.suspend(() =>
        Effect.fail(
          ready
            ? new TunnelClosed({ code: Close.Expired, reason: "the preview's time is up" })
            : new TunnelClosed({ code: Close.Ticket, reason: "expired preview ticket, ask for a new one" })
        )
      )
    )
  )
  return Effect.raceFirst(connected, expiry)
}

/**
 * Runs the dev server so leaving the scope stops it with everything it started: Effect puts it in
 * its own process group on macOS and Linux, and kills its tree with `taskkill /T` on Windows.
 */
export const devServer = (command: ReadonlyArray<string>, env: Record<string, string>) =>
  Effect.gen(function* () {
    const spawner = yield* ChildProcessSpawner.ChildProcessSpawner
    const [executable = "", ...args] = command
    const spawn = resolveSpawnCommand(executable, args, { env: { ...process.env, ...env } })
    return yield* spawner.spawn(
      ChildProcess.make(spawn.command, spawn.args, {
        env,
        extendEnv: true,
        shell: spawn.shell,
        stdin: "inherit",
        stdout: "inherit",
        stderr: "inherit",
        killSignal: "SIGTERM",
        forceKillAfter: "3 seconds"
      })
    )
  }).pipe(
    Effect.mapError(
      (error) => new CliError.UserError({ cause: error, userMessage: `Couldn't start ${command[0]}: ${error.message}` })
    )
  )
