import { SqliteClient, SqliteMigrator } from "@effect/sql-sqlite-node"
import { InvalidCredentials, TooManyAttempts } from "@sloppa/api"
import { Context, DateTime, Duration, Effect, FileSystem, Layer, Option, Path, Redacted } from "effect"
import { SqlClient } from "effect/sql"
import { createHash, randomBytes } from "node:crypto"
import { AppConfig } from "./Config.ts"
import { hashPassword, verifyPassword } from "./Password.ts"

export interface Account {
  readonly id: number
  readonly username: string
}

interface AgentCredential {
  readonly id: string
  readonly name: string
  readonly createdAt: DateTime.Utc
  readonly lastUsedAt: DateTime.Utc | undefined
}

/**
 * A signed-in browser, an agent's MCP token, or a setup link waiting for agents to redeem it. Only
 * a hash of each is stored: the database alone doesn't let anyone in.
 */
type Kind = "session" | "agent" | "setup"

const SESSION_TTL = Duration.days(30)
const SETUP_TTL = Duration.minutes(30)

/** Sign-in attempts allowed before each failure makes the next one wait, doubling up to `MAX_WAIT`. */
const FREE_ATTEMPTS = 5
const MAX_WAIT_MS = 15 * 60_000
/** Past this many keys the stalest are forgotten, so a flood of usernames can't grow memory forever. */
const MAX_KEYS = 10_000

/**
 * Failed sign-ins per username and per client address, in memory: a restart forgets them. Per
 * address alone, many addresses get unlimited guesses at one account; per username alone, anyone
 * could keep its owner out for long, so a key on its own waits at most `MAX_WAIT_MS`.
 */
class LoginThrottle {
  private readonly failures = new Map<string, { readonly count: number; readonly lastMs: number }>()

  private waitMs(key: string, now: number) {
    const entry = this.failures.get(key)
    if (entry === undefined || entry.count < FREE_ATTEMPTS) return 0
    const wait = Math.min(1000 * 2 ** (entry.count - FREE_ATTEMPTS), MAX_WAIT_MS)
    return Math.max(0, entry.lastMs + wait - now)
  }

  /** How long until these keys may try again; 0 counts this attempt as a failure until `succeeded`. */
  attempt(keys: ReadonlyArray<string>, now: number) {
    const wait = Math.max(...keys.map((key) => this.waitMs(key, now)))
    if (wait > 0) return wait
    // Counted up front, so parallel guesses can't all slip in before the first one fails.
    for (const key of keys) {
      const entry = this.failures.get(key)
      const stale = entry === undefined || now - entry.lastMs > MAX_WAIT_MS * 2
      this.failures.delete(key)
      this.failures.set(key, { count: stale ? 1 : entry.count + 1, lastMs: now })
    }
    while (this.failures.size > MAX_KEYS) this.failures.delete(this.failures.keys().next().value!)
    return 0
  }

  succeeded(keys: ReadonlyArray<string>) {
    for (const key of keys) this.failures.delete(key)
  }
}

/** Prefixed so secret scanners (and people) can tell what leaked. */
const AGENT_PREFIX = "sloppa_"

const secret = (bytes: number) => randomBytes(bytes).toString("base64url")
const hashOf = (token: string) => createHash("sha256").update(token).digest("hex")

const Migrations = SqliteMigrator.layer({
  loader: SqliteMigrator.fromRecord({
    "0001_create_accounts": Effect.gen(function* () {
      const sql = yield* SqlClient.SqlClient
      yield* sql`
        CREATE TABLE users (
          id INTEGER PRIMARY KEY,
          username TEXT NOT NULL UNIQUE COLLATE NOCASE,
          password_hash TEXT NOT NULL,
          created_ms INTEGER NOT NULL
        )
      `
      yield* sql`
        CREATE TABLE credentials (
          id TEXT PRIMARY KEY,
          hash TEXT NOT NULL UNIQUE,
          kind TEXT NOT NULL,
          user_id INTEGER NOT NULL,
          name TEXT,
          created_ms INTEGER NOT NULL,
          expires_ms INTEGER,
          last_used_ms INTEGER
        )
      `
      yield* sql`CREATE INDEX credentials_user ON credentials (user_id, kind, created_ms DESC)`
      yield* sql`CREATE TABLE secrets (name TEXT PRIMARY KEY, value TEXT NOT NULL)`
    })
  })
})

const Sqlite = Layer.unwrap(
  Effect.gen(function* () {
    const { dataDir } = yield* AppConfig
    const fs = yield* FileSystem.FileSystem
    const path = yield* Path.Path
    yield* fs.makeDirectory(dataDir, { recursive: true }).pipe(Effect.orDie)
    return Migrations.pipe(Layer.provideMerge(SqliteClient.layer({ filename: path.join(dataDir, "accounts.sqlite") })))
  })
)

/**
 * Users, and everything that signs in as one. Unlike the index this isn't derived from storage, so
 * `DATA_DIR/accounts.sqlite` is the one file to keep. The first user comes from `BOOTSTRAP_USERNAME`
 * and `BOOTSTRAP_PASSWORD`.
 */
export class Accounts extends Context.Service<
  Accounts,
  {
    /** `client` is the caller's address, when known. */
    readonly login: (
      username: string,
      password: string,
      client: string | undefined
    ) => Effect.Effect<
      { readonly account: Account; readonly session: string; readonly expiresAt: DateTime.Utc },
      InvalidCredentials | TooManyAttempts
    >
    readonly logout: (session: string) => Effect.Effect<void>
    /** Who a session cookie or an agent token belongs to. */
    readonly authenticate: (kind: "session" | "agent", token: string) => Effect.Effect<Option.Option<Account>>
    readonly agents: (account: Account) => Effect.Effect<ReadonlyArray<AgentCredential>>
    readonly revokeAgent: (account: Account, id: string) => Effect.Effect<void>
    readonly renameAgent: (account: Account, id: string, name: string) => Effect.Effect<void>
    /** A key made by hand, for a script or an app the setup prompt doesn't fit. */
    readonly createAgent: (
      account: Account,
      name: string
    ) => Effect.Effect<{ readonly agent: AgentCredential; readonly token: string }>
    /**
     * A link any number of agents can trade for a token of their own, until it expires or is revoked.
     * Redeeming replaces the token of an agent with the same name.
     */
    readonly createSetup: (
      account: Account
    ) => Effect.Effect<{ readonly id: string; readonly code: string; readonly expiresAt: DateTime.Utc }>
    readonly redeemSetup: (
      code: string,
      name: string
    ) => Effect.Effect<Option.Option<{ readonly token: string; readonly expiresAt: DateTime.Utc }>>
    /** When a setup link stops working, if it still does; redeems nothing. */
    readonly setupExpiry: (code: string) => Effect.Effect<Option.Option<DateTime.Utc>>
    /** False when there was no such link left. */
    readonly revokeSetup: (code: string) => Effect.Effect<boolean>
    readonly revokeSetupById: (account: Account, id: string) => Effect.Effect<void>
    /** Made once and kept: what upload tickets are signed with. */
    readonly ticketKey: Redacted.Redacted<string>
  }
>()("sloppa/server/Accounts") {
  static readonly layerNoDeps = Layer.effect(
    Accounts,
    Effect.gen(function* () {
      const sql = yield* SqlClient.SqlClient
      const { bootstrapUser } = yield* AppConfig
      const nowMs = Effect.map(DateTime.now, DateTime.toEpochMillis)

      const findUser = (username: string) =>
        sql<{ id: number; username: string; password_hash: string }>`
          SELECT id, username, password_hash FROM users WHERE username = ${username}
        `.pipe(
          Effect.map((rows) => Option.fromNullishOr(rows[0])),
          Effect.orDie
        )

      if (bootstrapUser !== undefined) {
        const existing = yield* findUser(bootstrapUser.username)
        if (Option.isNone(existing)) {
          yield* sql`INSERT INTO users ${sql.insert({
            username: bootstrapUser.username,
            password_hash: yield* hashPassword(bootstrapUser.password),
            created_ms: yield* nowMs
          })}`.pipe(Effect.orDie)
          yield* Effect.logInfo(`Created the user ${bootstrapUser.username}`)
        } else if (!(yield* verifyPassword(Redacted.value(bootstrapUser.password), existing.value.password_hash))) {
          // A new bootstrap password is how one gets reset: whoever knew the old one is signed out.
          yield* sql`UPDATE users SET password_hash = ${yield* hashPassword(bootstrapUser.password)}
            WHERE id = ${existing.value.id}`.pipe(Effect.orDie)
          yield* sql`DELETE FROM credentials WHERE user_id = ${existing.value.id} AND kind = 'session'`.pipe(
            Effect.orDie
          )
          yield* Effect.logInfo(`Reset the password of ${bootstrapUser.username}`)
        }
      }
      const [users] = yield* sql<{ count: number }>`SELECT COUNT(*) AS count FROM users`.pipe(Effect.orDie)
      if (users?.count === 0) {
        return yield* Effect.die(
          new Error("No users yet: set BOOTSTRAP_USERNAME and BOOTSTRAP_PASSWORD to create the first one")
        )
      }

      const throttle = new LoginThrottle()

      // Unknown usernames still pay for a hash check, so timing doesn't tell which ones exist.
      const decoy = yield* hashPassword(Redacted.make(secret(16)))

      const ticketKey = yield* Effect.gen(function* () {
        const [stored] = yield* sql<{ value: string }>`SELECT value FROM secrets WHERE name = 'tickets'`
        if (stored !== undefined) return stored.value
        const value = secret(32)
        yield* sql`INSERT INTO secrets ${sql.insert({ name: "tickets", value })}`
        return value
      }).pipe(Effect.orDie)

      const issue = Effect.fn("Accounts.issue")(function* (
        kind: Kind,
        userId: number,
        options: { readonly token: string; readonly name?: string; readonly ttl?: Duration.Duration }
      ) {
        const now = yield* DateTime.now
        const expiresAt = options.ttl === undefined ? undefined : DateTime.addDuration(now, options.ttl)
        const id = crypto.randomUUID()
        yield* sql`DELETE FROM credentials WHERE expires_ms <= ${DateTime.toEpochMillis(now)}`
        yield* sql`INSERT INTO credentials ${sql.insert({
          id,
          hash: hashOf(options.token),
          kind,
          user_id: userId,
          name: options.name ?? null,
          created_ms: DateTime.toEpochMillis(now),
          expires_ms: expiresAt === undefined ? null : DateTime.toEpochMillis(expiresAt),
          last_used_ms: null
        })}`
        return { id, expiresAt }
      }, Effect.orDie)

      const holder = (kind: Kind, token: string) =>
        Effect.flatMap(nowMs, (now) =>
          sql<{ id: string; user_id: number; username: string; expires_ms: number | null }>`
            SELECT credentials.id, user_id, username, expires_ms FROM credentials
            JOIN users ON users.id = credentials.user_id
            WHERE hash = ${hashOf(token)} AND kind = ${kind} AND (expires_ms IS NULL OR expires_ms > ${now})
          `.pipe(
            Effect.map((rows) => Option.fromNullishOr(rows[0])),
            Effect.orDie
          )
        )

      return Accounts.of({
        login: Effect.fn("Accounts.login")(function* (username, password, client) {
          const keys = [`user:${username.trim().toLowerCase()}`, ...(client === undefined ? [] : [`client:${client}`])]
          const wait = throttle.attempt(keys, yield* nowMs)
          if (wait > 0) return yield* new TooManyAttempts({ retryAfter: Math.ceil(wait / 1000) })
          const user = yield* findUser(username.trim())
          const valid = yield* verifyPassword(
            password,
            Option.match(user, { onNone: () => decoy, onSome: (u) => u.password_hash })
          )
          if (Option.isNone(user) || !valid) return yield* new InvalidCredentials()
          throttle.succeeded(keys)
          const session = secret(32)
          const { expiresAt } = yield* issue("session", user.value.id, { token: session, ttl: SESSION_TTL })
          return { account: { id: user.value.id, username: user.value.username }, session, expiresAt: expiresAt! }
        }),
        logout: (session) =>
          sql`DELETE FROM credentials WHERE hash = ${hashOf(session)} AND kind = 'session'`.pipe(
            Effect.orDie,
            Effect.asVoid
          ),
        authenticate: Effect.fn("Accounts.authenticate")(function* (kind, token) {
          const found = yield* holder(kind, token)
          if (Option.isNone(found)) return Option.none()
          if (kind === "agent") {
            yield* sql`UPDATE credentials SET last_used_ms = ${yield* nowMs} WHERE id = ${found.value.id}`.pipe(
              Effect.orDie
            )
          }
          return Option.some({ id: found.value.user_id, username: found.value.username })
        }),
        agents: (account) =>
          sql<{ id: string; name: string | null; created_ms: number; last_used_ms: number | null }>`
            SELECT id, name, created_ms, last_used_ms FROM credentials
            WHERE user_id = ${account.id} AND kind = 'agent' ORDER BY created_ms DESC
          `.pipe(
            Effect.map((rows) =>
              rows.map((row) => ({
                id: row.id,
                name: row.name ?? "agent",
                createdAt: DateTime.makeUnsafe(row.created_ms),
                lastUsedAt: row.last_used_ms === null ? undefined : DateTime.makeUnsafe(row.last_used_ms)
              }))
            ),
            Effect.orDie
          ),
        revokeAgent: (account, id) =>
          sql`DELETE FROM credentials WHERE id = ${id} AND user_id = ${account.id} AND kind = 'agent'`.pipe(
            Effect.orDie,
            Effect.asVoid
          ),
        createAgent: Effect.fn("Accounts.createAgent")(function* (account, name) {
          const token = AGENT_PREFIX + secret(32)
          const { id } = yield* issue("agent", account.id, { token, name })
          return { agent: { id, name, createdAt: yield* DateTime.now, lastUsedAt: undefined }, token }
        }),
        renameAgent: (account, id, name) =>
          sql`UPDATE credentials SET name = ${name} WHERE id = ${id} AND user_id = ${account.id} AND kind = 'agent'`.pipe(
            Effect.orDie,
            Effect.asVoid
          ),
        createSetup: Effect.fn("Accounts.createSetup")(function* (account) {
          const code = secret(24)
          const { id, expiresAt } = yield* issue("setup", account.id, { token: code, ttl: SETUP_TTL })
          return { id, code, expiresAt: expiresAt! }
        }),
        redeemSetup: Effect.fn("Accounts.redeemSetup")(function* (code, name) {
          const link = yield* holder("setup", code)
          if (Option.isNone(link)) return Option.none()
          const token = AGENT_PREFIX + secret(32)
          // Setting the same app up again on the same machine overwrote its config: its old token is dead weight.
          yield* sql`DELETE FROM credentials WHERE user_id = ${link.value.user_id} AND kind = 'agent' AND name = ${name}`.pipe(
            Effect.orDie
          )
          yield* issue("agent", link.value.user_id, { token, name })
          return Option.some({ token, expiresAt: DateTime.makeUnsafe(link.value.expires_ms!) })
        }),
        setupExpiry: (code) =>
          holder("setup", code).pipe(Effect.map(Option.map((link) => DateTime.makeUnsafe(link.expires_ms!)))),
        revokeSetup: (code) =>
          sql<{
            id: string
          }>`DELETE FROM credentials WHERE hash = ${hashOf(code)} AND kind = 'setup' RETURNING id`.pipe(
            Effect.map((rows) => rows.length > 0),
            Effect.orDie
          ),
        revokeSetupById: (account, id) =>
          sql`DELETE FROM credentials WHERE id = ${id} AND user_id = ${account.id} AND kind = 'setup'`.pipe(
            Effect.orDie,
            Effect.asVoid
          ),
        ticketKey: Redacted.make(ticketKey)
      })
    })
  )

  static readonly layer = this.layerNoDeps.pipe(Layer.provide(Sqlite))
}
