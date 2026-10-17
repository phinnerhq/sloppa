// Upload tickets: the one-time `curl -T` URLs the MCP server hands agents. Their signature and
// expiry are the only credential on that route, so what they refuse matters as much as what they let through.
import { describe, expect, it } from "@effect/vitest"
import { Effect, Option, Redacted } from "effect"
import { TestClock } from "effect/testing"
import { makeUploadTickets } from "../src/Tickets.ts"

const key = (seed: string) => Redacted.make(Buffer.from(seed.padEnd(32, "-")).toString("base64url"))
const tickets = makeUploadTickets(key("this server"))
const wanted = { name: "trace.zip", contentType: "application/zip", collection: "o/r #1" }

/** Swaps one character of the part of a token at `index` (0 body, 1 signature) for another. */
const tamper = (token: string, index: 0 | 1) => {
  const parts = token.split(".")
  const part = parts[index]!
  parts[index] = `${part.slice(0, 5)}${part[5] === "A" ? "B" : "A"}${part.slice(6)}`
  return parts.join(".")
}

describe("upload tickets", () => {
  it.effect("give back exactly what was issued", () =>
    Effect.gen(function* () {
      const { token, ticket } = yield* tickets.issue(wanted)

      expect(Option.getOrThrow(yield* tickets.verify(token))).toEqual(ticket)
      expect(ticket).toMatchObject(wanted)
    })
  )

  it.effect("last 30 minutes", () =>
    Effect.gen(function* () {
      const { token } = yield* tickets.issue(wanted)

      yield* TestClock.adjust("29 minutes")
      expect(Option.isSome(yield* tickets.verify(token))).toBe(true)

      yield* TestClock.adjust("1 minute")
      expect(Option.isNone(yield* tickets.verify(token))).toBe(true)
    })
  )

  it.effect("refuse a token whose body or signature was changed", () =>
    Effect.gen(function* () {
      const { token } = yield* tickets.issue(wanted)

      expect(Option.isNone(yield* tickets.verify(tamper(token, 0)))).toBe(true)
      expect(Option.isNone(yield* tickets.verify(tamper(token, 1)))).toBe(true)
    })
  )

  it.effect("refuse a ticket another server signed", () =>
    Effect.gen(function* () {
      const { token } = yield* makeUploadTickets(key("another server")).issue(wanted)

      expect(Option.isNone(yield* tickets.verify(token))).toBe(true)
    })
  )

  it.effect("refuse anything that isn't a token at all", () =>
    Effect.gen(function* () {
      const { token } = yield* tickets.issue(wanted)

      for (const garbage of ["", "nodot", `${token}.extra`, ".", "%%%.%%%"]) {
        expect(Option.isNone(yield* tickets.verify(garbage)), garbage).toBe(true)
      }
    })
  )

  it.effect("serve one upload each, until a failed one hands its ticket back", () =>
    Effect.gen(function* () {
      const { ticket } = yield* tickets.issue(wanted)

      expect(yield* tickets.claim(ticket)).toBe(true)
      expect(yield* tickets.claim(ticket)).toBe(false)

      yield* tickets.release(ticket)
      expect(yield* tickets.claim(ticket)).toBe(true)
    })
  )
})
