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
  collection: Schema.String,
  created_ms: Schema.Finite
})
type Row = typeof Row.Type

const fromRow = (row: Row): StoredArtifact => ({
  id: row.id,
  name: row.name,
  contentType: row.content_type,
  size: row.size,
  createdAt: DateTime.makeUnsafe(row.created_ms),
  collection: row.collection
})

const toRow = (artifact: StoredArtifact): Row => ({
  id: artifact.id,
  name: artifact.name,
  content_type: artifact.contentType,
  size: artifact.size,
  collection: artifact.collection,
  created_ms: DateTime.toEpochMillis(artifact.createdAt)
})

/** Opaque keyset cursors: where the last page ended, in its sort order. */
const encodeCursor = (position: object) => Base64Url.encode(JSON.stringify(position))
const decodeCursor = <A>(schema: Schema.Codec<A>, cursor: string | undefined) =>
  cursor === undefined
    ? Option.none()
    : Result.getSuccess(Base64Url.decodeString(cursor)).pipe(
        Option.flatMap(Schema.decodeUnknownOption(Schema.fromJsonString(schema)))
      )

/** Artifacts, newest first. */
const Cursor = Schema.Struct({ ms: Schema.Finite, id: Schema.String })
/** Groups, most recently active first. */
const GroupCursor = Schema.Struct({ ms: Schema.Finite, collection: Schema.String })

export interface ListQuery {
  readonly collection: string | undefined
  /** A substring of the name, id, collection or content type. */
  readonly search: string | undefined
  readonly cursor: string | undefined
  readonly limit: number
}

export interface GroupQuery {
  readonly cursor: string | undefined
  readonly limit: number
  readonly previewSize: number
}

export interface IndexGroup {
  readonly collection: string
  readonly count: number
  readonly lastUploadAt: DateTime.Utc
  readonly preview: ReadonlyArray<StoredArtifact>
}

export interface IndexStats {
  readonly total: number
  readonly totalBytes: number
  readonly last24h: number
  readonly lastUploadAt: DateTime.Utc | undefined
}

export interface CollectionStats {
  readonly collection: string
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
    }),
    // Repos and PRs gave way to collections. The index is only a copy: start it over, and the next boot
    // fills it back from storage, which reads the old fields as collections.
    "0002_collections": Effect.gen(function* () {
      const sql = yield* SqlClient.SqlClient
      yield* sql`DROP TABLE artifacts`
      yield* sql`
        CREATE TABLE artifacts (
          id TEXT PRIMARY KEY,
          name TEXT NOT NULL,
          content_type TEXT NOT NULL,
          size INTEGER NOT NULL,
          collection TEXT NOT NULL,
          created_ms INTEGER NOT NULL
        )
      `
      yield* sql`CREATE INDEX artifacts_recent ON artifacts (created_ms DESC, id DESC)`
      yield* sql`CREATE INDEX artifacts_collection ON artifacts (collection, created_ms DESC, id DESC)`
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
    readonly groups: (query: GroupQuery) => Effect.Effect<{
      readonly items: ReadonlyArray<IndexGroup>
      readonly nextCursor: string | undefined
    }>
    readonly collections: Effect.Effect<ReadonlyArray<CollectionStats>>
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
          collection = excluded.collection, created_ms = excluded.created_ms`

      const findById = SqlSchema.findOneOption({
        Request: ArtifactId,
        Result: Row,
        execute: (id) => sql`SELECT * FROM artifacts WHERE id = ${id}`
      })

      /**
       * A plain scan, walking the newest-first index and stopping at a page's worth of matches: plenty for
       * tens of thousands of artifacts. LIKE ignores ASCII case; `%` and `_` in the search are literal.
       */
      const matching = (search: string) => {
        const pattern = `%${search.replace(/[\\%_]/g, "\\$&")}%`
        return sql.or(
          ["name", "id", "collection", "content_type"].map((column) => sql`${sql(column)} LIKE ${pattern} ESCAPE '\\'`)
        )
      }

      const findPage = SqlSchema.findAll({
        Request: Schema.Struct({
          collection: Schema.NullOr(Schema.String),
          search: Schema.NullOr(Schema.String),
          after: Schema.NullOr(Cursor),
          limit: Schema.Finite
        }),
        Result: Row,
        execute: ({ collection, search, after, limit }) =>
          sql`SELECT * FROM artifacts WHERE ${sql.and([
            collection === null ? sql`1 = 1` : sql`collection = ${collection}`,
            search === null ? sql`1 = 1` : matching(search),
            after === null
              ? sql`1 = 1`
              : sql`(created_ms < ${after.ms} OR (created_ms = ${after.ms} AND id < ${after.id}))`
          ])} ORDER BY created_ms DESC, id DESC LIMIT ${limit}`
      })

      const GroupRow = Schema.Struct({
        collection: Schema.String,
        count: Schema.Finite,
        last_ms: Schema.Finite
      })
      const findGroups = SqlSchema.findAll({
        Request: Schema.Struct({ after: Schema.NullOr(GroupCursor), limit: Schema.Finite }),
        Result: GroupRow,
        execute: ({ after, limit }) =>
          sql`SELECT collection, COUNT(*) AS count, MAX(created_ms) AS last_ms FROM artifacts
            GROUP BY collection
            HAVING ${
              after === null
                ? sql`1 = 1`
                : sql`last_ms < ${after.ms} OR (last_ms = ${after.ms} AND collection < ${after.collection})`
            }
            ORDER BY last_ms DESC, collection DESC LIMIT ${limit}`
      })

      // One small lookup per group, each walking the (collection, created_ms) index: cheaper than ranking the
      // whole table.
      const findPreview = SqlSchema.findAll({
        Request: Schema.Struct({ collection: Schema.String, limit: Schema.Finite }),
        Result: Row,
        execute: ({ collection, limit }) =>
          sql`SELECT * FROM artifacts WHERE ${sql`collection = ${collection}`} ORDER BY created_ms DESC, id DESC LIMIT ${limit}`
      })

      const CollectionRow = Schema.Struct({ collection: Schema.String, count: Schema.Finite, last_ms: Schema.Finite })
      const findCollections = SqlSchema.findAll({
        Request: Schema.Void,
        Result: CollectionRow,
        execute: () =>
          sql`SELECT collection, COUNT(*) AS count, MAX(created_ms) AS last_ms FROM artifacts
            GROUP BY collection ORDER BY last_ms DESC`
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
        // All or nothing: a rebuild cut short would leave a nonempty index, never rebuilt again.
        const indexed = yield* storage.scan.pipe(
          Stream.mapEffect((artifact) => upsert(toRow(artifact))),
          Stream.runCount,
          sql.withTransaction,
          Effect.orDie
        )
        yield* Effect.logInfo(`Indexed ${indexed} artifact(s)`)
      }

      return ArtifactIndex.of({
        insert: (artifact) => upsert(toRow(artifact)).pipe(Effect.orDie, Effect.asVoid),
        remove: (id) => sql`DELETE FROM artifacts WHERE id = ${id}`.pipe(Effect.orDie, Effect.asVoid),
        get: (id) => findById(id).pipe(Effect.map(Option.map(fromRow)), Effect.orDie),
        list: Effect.fn("ArtifactIndex.list")(function* (query: ListQuery) {
          const after = decodeCursor(Cursor, query.cursor)
          // One extra row tells us whether there is a next page.
          const rows = yield* findPage({
            collection: query.collection ?? null,
            search: query.search ?? null,
            after: Option.getOrNull(after),
            limit: query.limit + 1
          }).pipe(Effect.orDie)
          const page = rows.slice(0, query.limit)
          const last = page.at(-1)
          return {
            items: page.map(fromRow),
            nextCursor:
              rows.length > query.limit && last !== undefined
                ? encodeCursor({ ms: last.created_ms, id: last.id })
                : undefined
          }
        }),
        groups: Effect.fn("ArtifactIndex.groups")(function* (query: GroupQuery) {
          const rows = yield* findGroups({
            after: Option.getOrNull(decodeCursor(GroupCursor, query.cursor)),
            limit: query.limit + 1
          }).pipe(Effect.orDie)
          const page = rows.slice(0, query.limit)
          const items = yield* Effect.forEach(page, (row) =>
            findPreview({ collection: row.collection, limit: query.previewSize }).pipe(
              Effect.orDie,
              Effect.map((preview): IndexGroup => ({
                collection: row.collection,
                count: row.count,
                lastUploadAt: DateTime.makeUnsafe(row.last_ms),
                preview: preview.map(fromRow)
              }))
            )
          )
          const last = page.at(-1)
          return {
            items,
            nextCursor:
              rows.length > query.limit && last !== undefined
                ? encodeCursor({ ms: last.last_ms, collection: last.collection })
                : undefined
          }
        }),
        collections: findCollections(undefined).pipe(
          Effect.map((rows) =>
            rows.map((row) => ({
              collection: row.collection,
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
