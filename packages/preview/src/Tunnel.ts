import { Duplex } from "node:stream"
import type { WebSocket } from "ws"

/** Bytes per binary frame: big writes are split, so neither side needs a large `maxPayload`. */
export const MAX_FRAME = 64 << 10

/**
 * One TCP connection carried over a data socket. Binary frames are the bytes; the text frame
 * `eof` ends this direction, like a TCP half-close, while the other goes on.
 *
 * Backpressure is TCP's own: a full read buffer pauses the WebSocket, which stops reading its
 * socket, and writes complete only once the socket took them. So a slow dev server holds an upload
 * back all the way to the browser instead of either side buffering it.
 */
export class TunnelStream extends Duplex {
  readonly #ws: WebSocket

  constructor(ws: WebSocket) {
    super({ allowHalfOpen: true, readableHighWaterMark: 4 * MAX_FRAME, writableHighWaterMark: 4 * MAX_FRAME })
    this.#ws = ws
    ws.binaryType = "nodebuffer"
    ws.on("message", (raw, isBinary) => {
      const data = raw as Buffer
      this.activity()
      if (isBinary) {
        if (!this.push(data)) ws.pause()
      } else if (data.toString() === "eof") {
        this.push(null)
      } else {
        this.destroy(new Error(`unknown control frame ${JSON.stringify(data.toString().slice(0, 20))}`))
      }
    })
    ws.on("error", (error) => this.destroy(error))
    ws.once("close", () => this.destroy())
  }

  /** Called on every frame each way; the server's pooled connections time out idle ones with it. */
  protected activity() {}

  // fallow-ignore-next-line unused-class-member -- Duplex hooks, called by Node's stream machinery
  override _read() {
    this.#ws.resume()
  }

  // fallow-ignore-next-line unused-class-member -- Duplex hooks, called by Node's stream machinery
  override _write(chunk: Buffer, _encoding: BufferEncoding, callback: (error?: Error | null) => void) {
    const ws = this.#ws
    this.activity()
    if (chunk.length === 0) return callback()
    for (let offset = 0; offset < chunk.length; offset += MAX_FRAME) {
      const last = offset + MAX_FRAME >= chunk.length
      ws.send(chunk.subarray(offset, offset + MAX_FRAME), { binary: true }, last ? callback : undefined)
    }
  }

  // fallow-ignore-next-line unused-class-member -- Duplex hooks, called by Node's stream machinery
  override _final(callback: (error?: Error | null) => void) {
    this.#ws.send("eof", callback)
  }

  override _destroy(error: Error | null, callback: (error?: Error | null) => void) {
    const ws = this.#ws
    if (ws.readyState === ws.OPEN) ws.close(error === null ? 1000 : 1011)
    else ws.terminate()
    callback(error)
  }
}

/**
 * Pipes two connections into each other, half-closes included, and destroys both once either
 * fails or both directions are through. Resolves when they're closed.
 */
export const splice = (a: Duplex, b: Duplex) =>
  new Promise<void>((resolve) => {
    const done = () => {
      a.destroy()
      b.destroy()
    }
    let finished = 0
    let closed = 0
    for (const [from, to] of [
      [a, b],
      [b, a]
    ] as const) {
      from.pipe(to)
      // A direction is through once what it read is all written and ended on the other side.
      to.once("finish", () => {
        finished += 1
        if (finished === 2) done()
      })
      from.on("error", done)
      from.once("close", () => {
        // Closed before its end, a reset: the other side can't go on either.
        if (!from.readableEnded) done()
        closed += 1
        if (closed === 2) resolve()
      })
    }
  })
