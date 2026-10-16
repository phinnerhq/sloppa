// A lease's life, on the test clock: what ends it, and what the CLI is told when it does. The
// end-to-end suites in apps/cli/test run the same over real sockets, but can't wait out a minute
// of silence or a ticket's expiry.
import { describe, expect, it } from "@effect/vitest"
import { Close, type TicketBody } from "@sloppa/preview"
import { Effect, Fiber, Option } from "effect"
import { TestClock } from "effect/testing"
import { EventEmitter } from "node:events"
import type { WebSocket } from "ws"
import { Leases } from "../src/preview/Leases.ts"

/** The control socket, as far as a lease uses it: records what it's sent and how it was closed. */
class FakeSocket extends EventEmitter {
  readonly OPEN = 1
  readonly CLOSED = 3
  readyState = 1
  readonly sent: Array<{ readonly type: string }> = []
  closed: { readonly code: number; readonly reason: string } | undefined

  send(data: string) {
    this.sent.push(JSON.parse(data))
  }

  close(code: number, reason: string) {
    this.closed = { code, reason }
    this.readyState = this.CLOSED
    this.emit("close", code, reason)
  }

  terminate() {
    this.close(1006, "")
  }

  get pings() {
    return this.sent.filter((message) => message.type === "ping").length
  }
}

const ticket = (overrides: Partial<TicketBody> = {}): TicketBody => ({
  id: "abcd1234",
  account: 1,
  exp: 3_600_000,
  ...overrides
})

/** Serves a lease on `ws` in the background, as the preview listener does for each control socket. */
const serve = (ws: FakeSocket, body: TicketBody = ticket()) =>
  Effect.gen(function* () {
    const leases = yield* Leases
    const fiber = yield* Effect.forkChild(
      leases.serve({
        ticket: body,
        hello: { type: "hello", port: 5173 },
        url: "http://preview.test/",
        control: ws as unknown as WebSocket
      })
    )
    yield* Effect.yieldNow
    return fiber
  })

const lookup = (label: string) => Effect.map(Leases, (leases) => leases.lookup(label))

describe("Leases", () => {
  it.effect("routes the label and tells the CLI where", () =>
    Effect.gen(function* () {
      const ws = new FakeSocket()
      yield* serve(ws)
      expect(ws.sent[0]).toMatchObject({ type: "ready", id: "abcd1234", url: "http://preview.test/" })
      expect(Option.isSome(yield* lookup("abcd1234"))).toBe(true)
      expect(ws.closed).toBeUndefined()
    }).pipe(Effect.provide(Leases.layer))
  )

  it.effect("ends at the ticket's expiry", () =>
    Effect.gen(function* () {
      const ws = new FakeSocket()
      yield* serve(ws, ticket({ exp: 50_000 }))
      yield* TestClock.adjust("49 seconds")
      expect(ws.closed).toBeUndefined()
      yield* TestClock.adjust("1 second")
      expect(ws.closed).toEqual({ code: Close.Expired, reason: "the preview's time is up" })
      expect(Option.isNone(yield* lookup("abcd1234"))).toBe(true)
    }).pipe(Effect.provide(Leases.layer))
  )

  it.effect("pings every 20 seconds and drops a CLI silent for a minute", () =>
    Effect.gen(function* () {
      const ws = new FakeSocket()
      yield* serve(ws)
      yield* TestClock.adjust("59 seconds")
      expect(ws.pings).toBe(3)
      expect(ws.closed).toBeUndefined()
      yield* TestClock.adjust("1 second")
      expect(ws.closed).toEqual({ code: 1008, reason: "no pong" })
      expect(Option.isNone(yield* lookup("abcd1234"))).toBe(true)
    }).pipe(Effect.provide(Leases.layer))
  )

  it.effect("keeps a CLI that answers", () =>
    Effect.gen(function* () {
      const ws = new FakeSocket()
      yield* serve(ws)
      const lease = Option.getOrThrow(yield* lookup("abcd1234"))
      for (let round = 0; round < 10; round++) {
        lease.seen()
        yield* TestClock.adjust("20 seconds")
      }
      expect(ws.closed).toBeUndefined()
      expect(ws.pings).toBe(11)
    }).pipe(Effect.provide(Leases.layer))
  )

  it.effect("stops when asked, and says so", () =>
    Effect.gen(function* () {
      const ws = new FakeSocket()
      yield* serve(ws)
      Option.getOrThrow(yield* lookup("abcd1234")).end({ code: Close.Stopped, reason: "stopped from sloppa" })
      yield* Effect.yieldNow
      expect(ws.closed).toEqual({ code: Close.Stopped, reason: "stopped from sloppa" })
      expect(Option.isNone(yield* lookup("abcd1234"))).toBe(true)
    }).pipe(Effect.provide(Leases.layer))
  )

  it.effect("hands the route to a reconnect with the same ticket", () =>
    Effect.gen(function* () {
      const first = new FakeSocket()
      const second = new FakeSocket()
      yield* serve(first)
      yield* serve(second)
      expect(first.closed?.code).toBe(Close.Replaced)
      expect(second.closed).toBeUndefined()
      expect(Option.getOrThrow(yield* lookup("abcd1234")).control).toBe(second)
    }).pipe(Effect.provide(Leases.layer))
  )

  it.effect("keeps a name for the ticket holding it", () =>
    Effect.gen(function* () {
      const holder = new FakeSocket()
      const other = new FakeSocket()
      yield* serve(holder, ticket({ name: "demo" }))
      yield* serve(other, ticket({ id: "zzzz9999", name: "demo" }))
      expect(other.closed?.code).toBe(Close.Taken)
      expect(holder.closed).toBeUndefined()
      expect(Option.getOrThrow(yield* lookup("demo")).id).toBe("abcd1234")
    }).pipe(Effect.provide(Leases.layer))
  )

  it.effect("drops the route when the CLI hangs up", () =>
    Effect.gen(function* () {
      const ws = new FakeSocket()
      yield* serve(ws)
      ws.readyState = ws.CLOSED
      ws.emit("close", 1000, "")
      yield* Effect.yieldNow
      expect(Option.isNone(yield* lookup("abcd1234"))).toBe(true)
    }).pipe(Effect.provide(Leases.layer))
  )

  it.effect("closes with going-away when interrupted, so the CLI reconnects", () =>
    Effect.gen(function* () {
      const ws = new FakeSocket()
      const fiber = yield* serve(ws)
      yield* Fiber.interrupt(fiber)
      expect(ws.closed).toEqual({ code: 1001, reason: "sloppa is restarting" })
      expect(Option.isNone(yield* lookup("abcd1234"))).toBe(true)
    }).pipe(Effect.provide(Leases.layer))
  )
})
