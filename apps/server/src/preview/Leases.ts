import { Close, type Hello, label, type TicketBody, TunnelStream } from "@sloppa/preview"
import { Clock, Context, Deferred, Duration, Effect, Exit, Layer, Option } from "effect"
import { randomBytes } from "node:crypto"
import { Agent } from "node:http"
import type { Duplex } from "node:stream"
import type { WebSocket } from "ws"

/** The CLI answered an `open` with `refuse`: nothing listens on the dev server's port. */
export class RefusedError extends Error {
  override readonly name = "RefusedError"
}

/** How a lease ends, as the CLI hears it in the control socket's close frame. */
export interface Ending {
  readonly code: number
  readonly reason: string
}

/**
 * A connection to the dev server as node:http's client sees it: the tunnel, plus the few socket
 * methods `http.Agent` calls. The idle timeout is real, so pooled connections don't linger.
 */
class TunnelSocket extends TunnelStream {
  timeout = 0
  #timer: NodeJS.Timeout | undefined

  protected override activity() {
    clearTimeout(this.#timer)
    if (this.timeout > 0) this.#timer = setTimeout(() => this.emit("timeout"), this.timeout).unref()
  }

  setTimeout(ms: number, callback?: () => void) {
    this.timeout = ms
    if (callback !== undefined) this.once("timeout", callback)
    this.activity()
    return this
  }

  setKeepAlive() {
    return this
  }

  setNoDelay() {
    return this
  }

  ref() {
    return this
  }

  unref() {
    return this
  }

  override _destroy(error: Error | null, callback: (error?: Error | null) => void) {
    clearTimeout(this.#timer)
    super._destroy(error, callback)
  }
}

/** Every connection it opens is a tunnel to one lease's dev server, so pooling them works as for any upstream. */
class TunnelAgent extends Agent {
  readonly #dial: () => Promise<TunnelSocket>

  constructor(dial: () => Promise<TunnelSocket>) {
    super({ keepAlive: true, maxFreeSockets: 32, timeout: 30_000 })
    this.#dial = dial
  }

  override createConnection(
    _options: unknown,
    callback?: (error: Error | null, stream: Duplex) => void
  ): Duplex | null | undefined {
    this.#dial().then(
      (socket) => callback?.(null, socket),
      (error: Error) => callback?.(error, undefined as never)
    )
    return undefined
  }
}

interface Dial {
  readonly resolve: (socket: TunnelSocket) => void
  readonly reject: (error: Error) => void
}

/** How long a browser connection waits for the CLI to open its data socket. */
const DIAL_TIMEOUT = 10_000

/**
 * One connected `sloppa preview run`, routed for exactly as long as its control socket is open.
 * The proxy uses it from Node callbacks, so what it does per connection is plain and synchronous;
 * its lifetime is `Leases.serve`'s.
 */
export class Lease {
  readonly ticket: TicketBody
  readonly hello: Hello
  readonly url: string
  readonly control: WebSocket
  readonly connectedAt = new Date()
  requests = 0
  /** Completed to end the lease: stopped from sloppa, replaced by a reconnect, or a protocol error. */
  readonly ending = Deferred.makeUnsafe<Ending>()
  readonly agent: TunnelAgent

  readonly #dials = new Map<string, Dial>()
  /** Every lease's pending dials, by connection id: how a data socket finds its lease. */
  readonly #pending: Map<string, Lease>
  readonly #open = new Set<Duplex>()
  /** Whether the CLI said anything since the last ping went out. */
  #heard = true
  #disconnected = false

  constructor(options: {
    readonly ticket: TicketBody
    readonly hello: Hello
    readonly url: string
    readonly control: WebSocket
    readonly pending: Map<string, Lease>
  }) {
    this.ticket = options.ticket
    this.hello = options.hello
    this.url = options.url
    this.control = options.control
    this.#pending = options.pending
    this.agent = new TunnelAgent(() => this.dial())
  }

  get label() {
    return label(this.ticket)
  }

  get id() {
    return this.ticket.id
  }

  get connections() {
    return this.#open.size
  }

  /** Whether the CLI said anything since the last call, which starts the next interval. */
  heardSinceLastPing() {
    const heard = this.#heard
    this.#heard = false
    return heard
  }

  end(ending: Ending) {
    Deferred.doneUnsafe(this.ending, Exit.succeed(ending))
  }

  /** Any message from the CLI says it's still there. */
  // fallow-ignore-next-line unused-class-member -- called from the preview listener, on a lease it looked up
  seen() {
    this.#heard = true
  }

  send(message: object) {
    if (this.control.readyState === this.control.OPEN) this.control.send(JSON.stringify(message))
  }

  /** Asks the CLI for a connection to its dev server, and waits for the data socket it opens. */
  dial() {
    return new Promise<TunnelSocket>((resolve, reject) => {
      if (this.#disconnected) return reject(new Error("preview disconnected"))
      const conn = randomBytes(16).toString("base64url")
      const timer = setTimeout(() => {
        this.#forget(conn)
        reject(new Error("the CLI didn't open a connection in time"))
      }, DIAL_TIMEOUT)
      const settle =
        <A>(f: (a: A) => void) =>
        (a: A) => {
          clearTimeout(timer)
          f(a)
        }
      this.#dials.set(conn, { resolve: settle(resolve), reject: settle(reject) })
      this.#pending.set(conn, this)
      this.send({ type: "open", conn })
    })
  }

  #forget(conn: string) {
    const dial = this.#dials.get(conn)
    this.#dials.delete(conn)
    this.#pending.delete(conn)
    return dial
  }

  /** The CLI opened the data socket for `conn`; false when nobody waits for it any more. */
  // fallow-ignore-next-line unused-class-member -- called from the preview listener, on a lease it looked up
  accept(conn: string, ws: WebSocket) {
    const dial = this.#forget(conn)
    if (dial === undefined) return false
    const socket = new TunnelSocket(ws)
    this.track(socket)
    dial.resolve(socket)
    return true
  }

  // fallow-ignore-next-line unused-class-member -- called from the preview listener, on a lease it looked up
  refuse(conn: string, reason: string) {
    this.#forget(conn)?.reject(new RefusedError(`nothing accepts connections on port ${this.hello.port}: ${reason}`))
  }

  /** Counted, and ended with the lease: tunneled connections, and the browser's side of upgrades. */
  track(socket: Duplex) {
    this.#open.add(socket)
    socket.once("close", () => this.#open.delete(socket))
  }

  /** Drops every connection through the lease, open WebSockets included. */
  disconnect() {
    this.#disconnected = true
    for (const conn of this.#dials.keys()) this.#forget(conn)?.reject(new Error("preview disconnected"))
    for (const socket of this.#open) socket.destroy()
    this.agent.destroy()
  }
}

/** The control socket closing, from either side. */
const closed = (ws: WebSocket) =>
  Effect.callback<Ending>((resume) => {
    const onClose = () => resume(Effect.succeed({ code: 1000, reason: "control socket closed" }))
    if (ws.readyState === ws.CLOSED) return onClose()
    ws.once("close", onClose)
    return Effect.sync(() => ws.off("close", onClose))
  })

const PING_EVERY = Duration.seconds(20)
/** Pings in a row without a word back before the CLI counts as gone: a minute of silence. */
const MISSED_PINGS = 3

/** A ping every 20 seconds; a CLI silent for a minute is gone. */
const keepalive = (lease: Lease) =>
  Effect.gen(function* () {
    let missed = 0
    while (true) {
      missed = lease.heardSinceLastPing() ? 0 : missed + 1
      if (missed >= MISSED_PINGS) return { code: 1008, reason: "no pong" } satisfies Ending
      lease.send({ type: "ping" })
      yield* Effect.sleep(PING_EVERY)
    }
  })

/**
 * The live leases, by label. Only in memory: a restart drops every route, and the CLIs reconnect
 * to the same URLs with the same tickets.
 */
export class Leases extends Context.Service<
  Leases,
  {
    /**
     * Holds the route for one control socket's life: pings it, ends it at the ticket's expiry or
     * when asked to, and closes the socket with the reason. Interrupting it (a shutdown) closes
     * with 1001, which the CLI answers by reconnecting.
     */
    readonly serve: (options: {
      readonly ticket: TicketBody
      readonly hello: Hello
      readonly url: string
      readonly control: WebSocket
    }) => Effect.Effect<void>
    readonly lookup: (label: string) => Option.Option<Lease>
    /** The lease waiting for the data socket `conn`. */
    readonly pending: (conn: string) => Option.Option<Lease>
    readonly list: Effect.Effect<ReadonlyArray<Lease>>
  }
>()("sloppa/server/preview/Leases") {
  static readonly layer = Layer.sync(Leases, () => {
    const byLabel = new Map<string, Lease>()
    const pending = new Map<string, Lease>()

    const claim = (lease: Lease) =>
      Effect.sync(() => {
        const existing = byLabel.get(lease.label)
        // A reconnect with the same ticket replaces the old connection, which may not have noticed
        // it's dead yet; any other ticket has to wait for the name.
        if (existing !== undefined && existing.id !== lease.id) return false
        existing?.end({ code: Close.Replaced, reason: "replaced by a reconnect" })
        byLabel.set(lease.label, lease)
        return true
      })

    const release = (lease: Lease, exit: Exit.Exit<Ending>) =>
      Effect.gen(function* () {
        if (byLabel.get(lease.label) === lease) byLabel.delete(lease.label)
        lease.disconnect()
        const ending = Exit.isSuccess(exit) ? exit.value : { code: 1001, reason: "sloppa is restarting" }
        if (lease.control.readyState === lease.control.OPEN) lease.control.close(ending.code, ending.reason)
        else lease.control.terminate()
        yield* Effect.logInfo("Preview closed").pipe(Effect.annotateLogs({ reason: ending.reason }))
      })

    return Leases.of({
      serve: (options) =>
        Effect.gen(function* () {
          const lease = new Lease({ ...options, pending })
          if (!(yield* claim(lease))) {
            return options.control.close(Close.Taken, `"${lease.label}" is taken by another preview right now`)
          }
          yield* Effect.logInfo("Preview connected").pipe(Effect.annotateLogs({ port: lease.hello.port }))
          lease.send({
            type: "ready",
            id: lease.id,
            label: lease.label,
            url: lease.url,
            expiresAt: new Date(lease.ticket.exp).toISOString()
          })
          const expiry = Clock.currentTimeMillis.pipe(
            Effect.flatMap((now) => Effect.sleep(Duration.millis(Math.max(0, lease.ticket.exp - now)))),
            Effect.as<Ending>({ code: Close.Expired, reason: "the preview's time is up" })
          )
          yield* Effect.raceAll([Deferred.await(lease.ending), closed(lease.control), expiry, keepalive(lease)]).pipe(
            Effect.onExit((exit) => release(lease, exit))
          )
        }).pipe(
          Effect.annotateLogs({
            preview: label(options.ticket),
            id: options.ticket.id,
            account: options.ticket.account
          })
        ),
      lookup: (label) => Option.fromNullishOr(byLabel.get(label)),
      pending: (conn) => Option.fromNullishOr(pending.get(conn)),
      list: Effect.sync(() => [...byLabel.values()])
    })
  })
}
