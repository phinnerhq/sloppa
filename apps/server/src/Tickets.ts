import { ArtifactId } from "@sloppa/api"
import { Context, DateTime, Duration, Effect, Layer, Option, Redacted, Result, Schema } from "effect"
import { Base64Url } from "effect/encoding"
import { createHmac, timingSafeEqual } from "node:crypto"
import { Accounts } from "./Accounts.ts"

/** What an upload ticket lets its holder store: one file, under an id picked up front. */
export const Ticket = Schema.Struct({
  id: ArtifactId,
  name: Schema.String,
  contentType: Schema.optionalKey(Schema.String),
  repo: Schema.optionalKey(Schema.String),
  pr: Schema.optionalKey(Schema.String),
  /** Expiry, epoch millis. */
  exp: Schema.Finite
})
export type Ticket = typeof Ticket.Type

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
  }
>()("sloppa/server/UploadTickets") {
  static readonly layer = Layer.effect(
    UploadTickets,
    Effect.gen(function* () {
      const { ticketKey } = yield* Accounts
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

      return UploadTickets.of({ issue, verify })
    })
  )
}
