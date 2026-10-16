import { PREVIEW_TTL_DEFAULT_SECONDS, Preview, PreviewNotFound, PreviewsUnavailable, PreviewTicket } from "@sloppa/api"
import { Close, passwordMac, previewId, signTicket } from "@sloppa/preview"
import { Context, DateTime, Duration, Effect, Layer, Redacted } from "effect"
import type { Account } from "./Accounts.ts"
import { AppConfig } from "./Config.ts"
import { type Lease, Leases } from "./preview/Leases.ts"
import { PreviewServerLive } from "./preview/Server.ts"

const unavailable = new PreviewsUnavailable({
  message: "Previews aren't set up on this server: set PREVIEW_ORIGIN and PREVIEW_SECRET"
})

const toPreview = (lease: Lease) =>
  new Preview({
    id: lease.id,
    url: lease.url,
    ...(lease.ticket.name === undefined ? {} : { name: lease.ticket.name }),
    protected: lease.ticket.passwordMac !== undefined,
    port: lease.hello.port,
    ...(lease.hello.command === undefined ? {} : { command: lease.hello.command }),
    ...(lease.hello.cwd === undefined ? {} : { cwd: lease.hello.cwd }),
    ...(lease.hello.hostname === undefined ? {} : { hostname: lease.hello.hostname }),
    connectedAt: DateTime.fromDateUnsafe(lease.connectedAt),
    expiresAt: DateTime.makeUnsafe(lease.ticket.exp),
    connections: lease.connections,
    requests: lease.requests
  })

/**
 * `sloppa preview run`, from the app's side: it signs the tickets the CLI connects with, and
 * lists and stops what's connected. The leases themselves live in `Leases`, in memory, for
 * exactly as long as each CLI stays connected to the preview listener.
 */
export class Previews extends Context.Service<
  Previews,
  {
    readonly start: (
      account: Account,
      options: { readonly name?: string; readonly password?: string; readonly ttl?: number }
    ) => Effect.Effect<PreviewTicket, PreviewsUnavailable>
    readonly list: (account: Account) => Effect.Effect<ReadonlyArray<Preview>, PreviewsUnavailable>
    readonly stop: (account: Account, id: string) => Effect.Effect<void, PreviewNotFound | PreviewsUnavailable>
  }
>()("sloppa/server/Previews") {
  static readonly layer = Layer.effect(
    Previews,
    Effect.gen(function* () {
      const { preview } = yield* AppConfig
      if (preview === undefined) {
        return Previews.of({
          start: () => Effect.fail(unavailable),
          list: () => Effect.fail(unavailable),
          stop: () => Effect.fail(unavailable)
        })
      }
      const leases = yield* Leases
      const secret = Redacted.value(preview.secret)
      const { origin } = preview
      const connectUrl = `${origin.protocol === "https:" ? "wss:" : "ws:"}//${origin.host}/_connect`
      const own = (account: Account) =>
        Effect.map(leases.list, (all) => all.filter((lease) => lease.ticket.account === account.id))

      return Previews.of({
        start: Effect.fn("Previews.start")(function* (account, { name, password, ttl }) {
          const expiresAt = DateTime.addDuration(
            yield* DateTime.now,
            Duration.seconds(ttl ?? PREVIEW_TTL_DEFAULT_SECONDS)
          )
          const id = previewId()
          const ticket = signTicket(secret, {
            id,
            account: account.id,
            ...(name === undefined ? {} : { name }),
            ...(password === undefined ? {} : { passwordMac: passwordMac(secret, id, password) }),
            exp: DateTime.toEpochMillis(expiresAt)
          })
          const url = `${origin.protocol}//${name ?? id}.${origin.host}/`
          return new PreviewTicket({ ticket, id, url, connectUrl, expiresAt })
        }),
        list: (account) =>
          Effect.map(own(account), (leases) =>
            leases.toSorted((a, b) => b.connectedAt.getTime() - a.connectedAt.getTime()).map(toPreview)
          ),
        stop: Effect.fn("Previews.stop")(function* (account, id) {
          // Only one's own.
          const lease = (yield* own(account)).find((lease) => lease.id === id)
          if (lease === undefined) return yield* new PreviewNotFound({ id })
          lease.end({ code: Close.Stopped, reason: "stopped from sloppa" })
        })
      })
    })
  ).pipe(Layer.provide(PreviewServerLive))
}
