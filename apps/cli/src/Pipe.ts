import { splice, SUBPROTOCOL, TunnelStream } from "@sloppa/preview"
import { Data, Effect } from "effect"
import { connect, type Socket } from "node:net"
import { WebSocket } from "ws"

/** Nothing accepted a connection on the dev server's port. */
export class DevServerUnreachable extends Data.TaggedError("DevServerUnreachable")<{ readonly cause: unknown }> {}

/**
 * A connection to the dev server: 127.0.0.1 first, then ::1 for servers that bound `localhost` on
 * IPv6 only. Nothing else, ever: the port is local to this machine.
 */
export const connectLocal = (port: number) => {
  const open = (host: string) =>
    Effect.acquireRelease(
      Effect.callback<Socket, DevServerUnreachable>((resume) => {
        const socket = connect({ host, port, allowHalfOpen: true, timeout: 3000 })
        socket.once("connect", () => {
          socket.setTimeout(0)
          resume(Effect.succeed(socket))
        })
        socket.once("timeout", () => socket.destroy(new Error(`timed out connecting to ${host}:${port}`)))
        socket.once("error", (cause) => resume(Effect.fail(new DevServerUnreachable({ cause }))))
        return Effect.sync(() => socket.destroy())
      }),
      (socket) => Effect.sync(() => socket.destroy())
    )
  return open("127.0.0.1").pipe(Effect.catch(() => open("::1")))
}

/**
 * Pipes bytes between the dev server and one data socket, until both are closed. Never looks
 * inside them: HTTP, upgrades and everything else are the server's business. See TunnelStream for
 * the framing, and why memory stays bounded however slow either side is.
 */
export const pipe = (local: Socket, dataUrl: string) =>
  Effect.callback<void>((resume) => {
    const ws = new WebSocket(dataUrl, SUBPROTOCOL, { perMessageDeflate: false, handshakeTimeout: 10_000 })
    const tunnel = new TunnelStream(ws)
    void splice(local, tunnel).then(() => resume(Effect.void))
    return Effect.sync(() => tunnel.destroy())
  })
