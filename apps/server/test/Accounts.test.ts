// The bootstrap password seeds the first user and resets it when changed, but one left set in the
// environment mustn't undo a password changed from the app since.
import { NodeServices } from "@effect/platform-node"
import { describe, expect, it } from "@effect/vitest"
import { Effect, Layer, Option, Redacted } from "effect"
import { mkdtempSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { Accounts } from "../src/Accounts.ts"
import { AppConfig } from "../src/Config.ts"

/** A boot of the server on `dataDir`, with `password` as BOOTSTRAP_PASSWORD. */
const boot = (dataDir: string, password: string) =>
  Accounts.layer.pipe(
    Layer.provide(
      Layer.succeed(
        AppConfig,
        AppConfig.of({
          host: "127.0.0.1",
          port: 0,
          publicUrl: "http://sloppa.test",
          bootstrapUser: { username: "admin", password: Redacted.make(password) },
          trustProxy: false,
          staticDir: undefined,
          dataDir,
          s3: undefined,
          preview: undefined
        })
      )
    ),
    Layer.provide(NodeServices.layer)
  )

const freshDir = () => mkdtempSync(join(tmpdir(), "sloppa-accounts-test-"))

const signIn = (password: string) => Accounts.use((accounts) => accounts.login("admin", password, undefined))

const signsIn = (password: string) =>
  signIn(password).pipe(Effect.match({ onFailure: () => false, onSuccess: () => true }))

const isSignedIn = (session: string) =>
  Accounts.use((accounts) => accounts.authenticate("session", session)).pipe(Effect.map(Option.isSome))

const change = (session: string, current: string, next: string) =>
  Effect.gen(function* () {
    const { account } = yield* signIn(current)
    yield* (yield* Accounts).changePassword(account, session, current, next)
  })

describe("accounts", () => {
  it.effect("keep a password changed in the app when the bootstrap one stays set", () =>
    Effect.gen(function* () {
      const dataDir = freshDir()
      yield* Effect.gen(function* () {
        const { session } = yield* signIn("first-pass")
        yield* change(session, "first-pass", "second-pass")
      }).pipe(Effect.provide(boot(dataDir, "first-pass")))

      yield* Effect.gen(function* () {
        expect(yield* signsIn("second-pass")).toBe(true)
        expect(yield* signsIn("first-pass")).toBe(false)
      }).pipe(Effect.provide(boot(dataDir, "first-pass")))
    })
  )

  it.effect("reset the password, signing browsers out, when the bootstrap one changes", () =>
    Effect.gen(function* () {
      const dataDir = freshDir()
      const session = yield* Effect.gen(function* () {
        const { session } = yield* signIn("first-pass")
        yield* change(session, "first-pass", "second-pass")
        return session
      }).pipe(Effect.provide(boot(dataDir, "first-pass")))

      yield* Effect.gen(function* () {
        expect(yield* signsIn("third-pass")).toBe(true)
        expect(yield* signsIn("second-pass")).toBe(false)
        expect(yield* isSignedIn(session)).toBe(false)
      }).pipe(Effect.provide(boot(dataDir, "third-pass")))
    })
  )

  it.effect("sign out every other browser on a change, and refuse a wrong current password", () =>
    Effect.gen(function* () {
      const here = yield* signIn("first-pass")
      const there = yield* signIn("first-pass")
      const accounts = yield* Accounts

      const wrong = yield* Effect.flip(accounts.changePassword(here.account, here.session, "not-it", "second-pass"))
      expect(wrong._tag).toBe("InvalidCredentials")
      expect(yield* isSignedIn(there.session)).toBe(true)

      yield* accounts.changePassword(here.account, here.session, "first-pass", "second-pass")
      expect(yield* isSignedIn(here.session)).toBe(true)
      expect(yield* isSignedIn(there.session)).toBe(false)
      expect(yield* signsIn("second-pass")).toBe(true)
    }).pipe(Effect.provide(boot(freshDir(), "first-pass")))
  )
})
