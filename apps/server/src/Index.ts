import { SqliteClient, SqliteMigrator } from "@effect/sql-sqlite-bun"
import { ArtifactId } from "@sloppa/api"
import { Context, DateTime, Effect, FileSystem, Layer, Option, Path, Result, Schema, Stream } from "effect"
import { Base64Url } from "effect/encoding"
import { SqlClient, SqlSchema } from "effect/sql"
import { AppConfig } from "./Config.ts"
import { ArtifactStorage, type StoredArtifact } from "./Storage.ts"

const Row = Schema.Struct({
  id: ArtifactId,
  name: Schema.String,
  content_type: Schema.String,
  size: Schema.Finite,
  repo: Schema.NullOr(Schema.String),
  pr: Schema.NullOr(Schema.String),
  created_ms: Schema.Finite
})
type Row = typeof Row.Type

const fromRow = (row: Row): StoredArtifact => ({
  id: row.id,
  name: row.name,
  contentType: row.content_type,
  size: row.size,
  createdAt: DateTime.makeUnsafe(row.created_ms),
  repo: row.repo ?? undefined,
  pr: row.pr ?? undefined
})

const toRow = (artifact: StoredArtifact): Row => ({
  id: artifact.id,
  name: artifact.name,
  content_type: artifact.contentType,
  size: artifact.size,
  repo: artifact.repo ?? null,
  pr: artifact.pr ?? null,
  created_ms: DateTime.toEpochMillis(artifact.createdAt)
})

/** Opaque "created_ms:id" keyset cursor, newest first. */
const Cursor = Schema.Struct({ ms: Schema.Finite, id: Schema.String })
const encodeCursor = (row: Row) => Base64Url.encode(JSON.stringify({ ms: row.created_ms, id: row.id }))
const decodeCursor = (cursor: string) =>
  Result.getSuccess(Base64Url.decodeString(cursor)).pipe(
    Option.flatMap(Schema.decodeUnknownOption(Schema.fromJsonString(Cursor)))
  )

export interface ListQuery {
  readonly repo: string | undefined
  readonly pr: string | undefined
  readonly cursor: string | undefined
  readonly limit: number
}

export interface IndexStats {
  readonly total: number
  readonly totalBytes: number
  readonly last24h: number
  readonly lastUploadAt: DateTime.Utc | undefined
}

export interface PrStats {
  readonly pr: string
  readonly repo: string | undefined
  readonly count: number
  readonly lastUploadAt: DateTime.Utc
}

export interface RepoStats {
  readonly repo: string
  readonly count: number
  readonly lastUploadAt: DateTime.Utc
}

const Migrations = SqliteMigrator.layer({
  loader: SqliteMigrator.fromRecord({
    "0001_create_artifacts": Effect.gen(function* () {
      const sql = yield* SqlClient.SqlClient
      yield* sql`
        CREATE TABLE artifacts (
          id TEXT PRIMARY KEY,
          name TEXT NOT NULL,
          content_type TEXT NOT NULL,
          size INTEGER NOT NULL,
          repo TEXT,
          pr TEXT,
          created_ms INTEGER NOT NULL
        )
      `
      yield* sql`CREATE INDEX artifacts_recent ON artifacts (created_ms DESC, id DESC)`
      yield* sql`CREATE INDEX artifacts_repo ON artifacts (repo, created_ms DESC, id DESC)`
      yield* sql`CREATE INDEX artifacts_pr ON artifacts (pr, created_ms DESC, id DESC)`
    })
  })
})

const Sqlite = Layer.unwrap(
  Effect.gen(function* () {
    const { dataDir } = yield* AppConfig
    const fs = yield* FileSystem.FileSystem
    const path = yield* Path.Path
    yield* fs.makeDirectory(dataDir, { recursive: true }).pipe(Effect.orDie)
    return Migrations.pipe(Layer.provideMerge(SqliteClient.layer({ filename: path.join(dataDir, "index.sqlite") })))
  })
)

/**
 * Queryable copy of the stored metadata. It is only ever derived from storage: if
 * the database is missing or empty on boot it is rebuilt with a full scan, so
 * deleting the file is always safe.
 */
export class ArtifactIndex extends Context.Service<
  ArtifactIndex,
  {
    readonly insert: (artifact: StoredArtifact) => Effect.Effect<void>
    readonly remove: (id: ArtifactId) => Effect.Effect<void>
    readonly get: (id: ArtifactId) => Effect.Effect<Option.Option<StoredArtifact>>
    readonly list: (query: ListQuery) => Effect.Effect<{
      readonly items: ReadonlyArray<StoredArtifact>
      readonly nextCursor: string | undefined
    }>
    readonly repos: Effect.Effect<ReadonlyArray<RepoStats>>
    readonly prs: (repo: string | undefined) => Effect.Effect<ReadonlyArray<PrStats>>
    readonly stats: Effect.Effect<IndexStats>
  }
>()("sloppa/server/ArtifactIndex") {
  static readonly layerNoDeps = Layer.effect(
    ArtifactIndex,
    Effect.gen(function* () {
      const sql = yield* SqlClient.SqlClient
      const storage = yield* ArtifactStorage

      const upsert = (row: Row) =>
        sql`INSERT INTO artifacts ${sql.insert(row)} ON CONFLICT (id) DO UPDATE SET
          name = excluded.name, content_type = excluded.content_type, size = excluded.size,
          repo = excluded.repo, pr = excluded.pr, created_ms = excluded.created_ms`

      const findById = SqlSchema.findOneOption({
        Request: ArtifactId,
        Result: Row,
        execute: (id) => sql`SELECT * FROM artifacts WHERE id = ${id}`
      })

      const findPage = SqlSchema.findAll({
        Request: Schema.Struct({
          repo: Schema.NullOr(Schema.String),
          pr: Schema.NullOr(Schema.String),
          after: Schema.NullOr(Cursor),
          limit: Schema.Finite
        }),
        Result: Row,
        execute: ({ repo, pr, after, limit }) =>
          sql`SELECT * FROM artifacts WHERE ${sql.and([
            repo === null ? sql`1 = 1` : sql`repo = ${repo}`,
            pr === null ? sql`1 = 1` : sql`pr = ${pr}`,
            after === null
              ? sql`1 = 1`
              : sql`(created_ms < ${after.ms} OR (created_ms = ${after.ms} AND id < ${after.id}))`
          ])} ORDER BY created_ms DESC, id DESC LIMIT ${limit}`
      })

      const RepoRow = Schema.Struct({ repo: Schema.String, count: Schema.Finite, last_ms: Schema.Finite })
      const findRepos = SqlSchema.findAll({
        Request: Schema.Void,
        Result: RepoRow,
        execute: () =>
          sql`SELECT repo, COUNT(*) AS count, MAX(created_ms) AS last_ms FROM artifacts
            WHERE repo IS NOT NULL GROUP BY repo ORDER BY last_ms DESC`
      })

      const PrRow = Schema.Struct({
        pr: Schema.String,
        repo: Schema.NullOr(Schema.String),
        count: Schema.Finite,
        last_ms: Schema.Finite
      })
      const findPrs = SqlSchema.findAll({
        Request: Schema.NullOr(Schema.String),
        Result: PrRow,
        execute: (repo) =>
          sql`SELECT pr, MAX(repo) AS repo, COUNT(*) AS count, MAX(created_ms) AS last_ms FROM artifacts
            WHERE ${sql.and([sql`pr IS NOT NULL`, repo === null ? sql`1 = 1` : sql`repo = ${repo}`])}
            GROUP BY pr ORDER BY last_ms DESC`
      })

      const StatsRow = Schema.Struct({
        total: Schema.Finite,
        total_bytes: Schema.NullOr(Schema.Finite),
        last_24h: Schema.NullOr(Schema.Finite),
        last_ms: Schema.NullOr(Schema.Finite)
      })
      const findStats = SqlSchema.findOne({
        Request: Schema.Finite,
        Result: StatsRow,
        execute: (since) =>
          sql`SELECT COUNT(*) AS total, SUM(size) AS total_bytes,
            SUM(CASE WHEN created_ms >= ${since} THEN 1 ELSE 0 END) AS last_24h,
            MAX(created_ms) AS last_ms FROM artifacts`
      })

      const [counted] = yield* sql<{ count: number }>`SELECT COUNT(*) AS count FROM artifacts`.pipe(Effect.orDie)
      if (counted?.count === 0) {
        yield* Effect.logInfo("Index is empty, rebuilding it from storage")
        const indexed = yield* storage.scan.pipe(
          Stream.mapEffect((artifact) => upsert(toRow(artifact))),
          Stream.runCount,
          Effect.orDie
        )
        yield* Effect.logInfo(`Indexed ${indexed} artifact(s)`)
      }

      return ArtifactIndex.of({
        insert: (artifact) => upsert(toRow(artifact)).pipe(Effect.orDie, Effect.asVoid),
        remove: (id) => sql`DELETE FROM artifacts WHERE id = ${id}`.pipe(Effect.orDie, Effect.asVoid),
        get: (id) => findById(id).pipe(Effect.map(Option.map(fromRow)), Effect.orDie),
        list: Effect.fn("ArtifactIndex.list")(function* (query: ListQuery) {
          const after = query.cursor === undefined ? Option.none() : decodeCursor(query.cursor)
          // One extra row tells us whether there is a next page.
          const rows = yield* findPage({
            repo: query.repo ?? null,
            pr: query.pr ?? null,
            after: Option.getOrNull(after),
            limit: query.limit + 1
          }).pipe(Effect.orDie)
          const page = rows.slice(0, query.limit)
          const last = page.at(-1)
          return {
            items: page.map(fromRow),
            nextCursor: rows.length > query.limit && last !== undefined ? encodeCursor(last) : undefined
          }
        }),
        repos: findRepos(undefined).pipe(
          Effect.map((rows) =>
            rows.map((row) => ({ repo: row.repo, count: row.count, lastUploadAt: DateTime.makeUnsafe(row.last_ms) }))
          ),
          Effect.orDie
        ),
        prs: (repo) =>
          findPrs(repo ?? null).pipe(
            Effect.map((rows) =>
              rows.map((row) => ({
                pr: row.pr,
                repo: row.repo ?? undefined,
                count: row.count,
                lastUploadAt: DateTime.makeUnsafe(row.last_ms)
              }))
            ),
            Effect.orDie
          ),
        stats: Effect.gen(function* () {
          const now = DateTime.toEpochMillis(yield* DateTime.now)
          const row = yield* findStats(now - 24 * 60 * 60 * 1000).pipe(Effect.orDie)
          return {
            total: row.total,
            totalBytes: row.total_bytes ?? 0,
            last24h: row.last_24h ?? 0,
            lastUploadAt: row.last_ms === null ? undefined : DateTime.makeUnsafe(row.last_ms)
          }
        })
      })
    })
  )

  static readonly layer = this.layerNoDeps.pipe(Layer.provide(Sqlite))
}
