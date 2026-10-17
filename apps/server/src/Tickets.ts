import { ArtifactId } from "@sloppa/api"
import { Context, DateTime, Duration, Effect, Layer, Option, Redacted, Result, Schema } from "effect"
import { Base64Url } from "effect/encoding"
import { createHmac, timingSafeEqual } from "node:crypto"
import { Accounts } from "./Accounts.ts"

/** What an upload ticket lets its holder store: one file, under an id picked up front. */
const Ticket = Schema.Struct({
  id: ArtifactId,
  name: Schema.String,
  contentType: Schema.optionalKey(Schema.String),
  collection: Schema.String,
  /** Expiry, epoch millis. */
  exp: Schema.Finite
})
type Ticket = typeof Ticket.Type

const TTL = Duration.minutes(30)

const decodeTicket = Schema.decodeUnknownOption(Schema.fromJsonString(Ticket))

/**
 * Stateless, HMAC-signed upload URLs. An agent gets one from the MCP server
 * and `curl`s its file to it, so the bytes never pass through the model and
 * curl needs no credentials.
 */
export class UploadTickets extends Context.Service<
  UploadTickets,
  {
    readonly issue: (
      ticket: Omit<Ticket, "id" | "exp">
    ) => Effect.Effect<{ readonly token: string; readonly ticket: Ticket }>
    readonly verify: (token: string) => Effect.Effect<Option.Option<Ticket>>
    /**
     * Takes a ticket for one upload. False if it's already taken, by an upload that finished or one
     * still running, so two PUTs can't race to write the same artifact, and deleting it doesn't free
     * the ticket again. `release` hands it back after a failed upload.
     */
    readonly claim: (ticket: Ticket) => Effect.Effect<boolean>
    readonly release: (ticket: Ticket) => Effect.Effect<void>
  }
>()("sloppa/server/UploadTickets") {
  static readonly layer = Layer.effect(
    UploadTickets,
    Effect.map(Accounts, ({ ticketKey }) => makeUploadTickets(ticketKey))
  )
}

/** Upload tickets signed with `ticketKey`, base64url: the account store keeps it across restarts. */
export const makeUploadTickets = (ticketKey: Redacted.Redacted<string>) => {
  const key = Buffer.from(Redacted.value(ticketKey), "base64url")
  const sign = (body: string) => createHmac("sha256", key).update(body).digest()

  const issue = Effect.fn("UploadTickets.issue")(function* (input: Omit<Ticket, "id" | "exp">) {
    const now = yield* DateTime.now
    const ticket: Ticket = {
      ...input,
      id: crypto.randomUUID() as ArtifactId,
      exp: DateTime.toEpochMillis(DateTime.addDuration(now, TTL))
    }
    const body = Base64Url.encode(JSON.stringify(ticket))
    return { token: `${body}.${Base64Url.encode(sign(body))}`, ticket }
  })

  const verify = Effect.fn("UploadTickets.verify")(function* (token: string) {
    const [body, signature, ...rest] = token.split(".")
    if (body === undefined || signature === undefined || rest.length > 0) return Option.none()
    const given = Result.getOrUndefined(Base64Url.decode(signature))
    const expected = sign(body)
    if (given === undefined || given.length !== expected.length || !timingSafeEqual(given, expected)) {
      return Option.none()
    }
    const now = DateTime.toEpochMillis(yield* DateTime.now)
    return Result.getSuccess(Base64Url.decodeString(body)).pipe(
      Option.flatMap(decodeTicket),
      Option.filter((ticket) => ticket.exp > now)
    )
  })

  // Ids by expiry. A ticket past it fails verification anyway, so it can be forgotten then.
  const claimed = new Map<string, number>()
  const claim = (ticket: Ticket) =>
    Effect.map(DateTime.now, (now) => {
      const ms = DateTime.toEpochMillis(now)
      for (const [id, exp] of claimed) if (exp <= ms) claimed.delete(id)
      if (claimed.has(ticket.id)) return false
      claimed.set(ticket.id, ticket.exp)
      return true
    })
  const release = (ticket: Ticket) => Effect.sync(() => void claimed.delete(ticket.id))

  return UploadTickets.of({ issue, verify, claim, release })
}
