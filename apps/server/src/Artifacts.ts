import {
  Artifact,
  type ArtifactId,
  ArtifactGroup,
  ArtifactPage,
  CollectionSummary,
  GROUP_PREVIEW_SIZE,
  GroupPage,
  rawPath,
  Stats,
  thumbPath,
  viewerPath
} from "@sloppa/api"
import { Context, Effect, Layer, Option, Semaphore } from "effect"
import { AppConfig } from "./Config.ts"
import { resolveContentType } from "./ContentType.ts"
import { ArtifactIndex } from "./Index.ts"
import { ArtifactStorage } from "./Storage.ts"
import type { StoredArtifact, Upload } from "./StorageShared.ts"
import { hasThumbnail, Thumbnails } from "./Thumbnails.ts"

interface UploadInput {
  readonly id?: ArtifactId | undefined
  readonly name: string
  /** What the uploader claimed; the file extension wins when this is missing or generic. */
  readonly contentType: string | undefined
  readonly body: Upload
  readonly collection: string
}

const makeArtifacts = Effect.gen(function* () {
  const storage = yield* ArtifactStorage
  const index = yield* ArtifactIndex
  const thumbnails = yield* Thumbnails
  const { publicUrl } = yield* AppConfig
  /**
   * Moves and deletes rewrite storage, then the index. One at a time, and never cut off halfway, so a
   * move can't bring back a sidecar a delete just removed, nor leave the index naming the old collection.
   */
  const mutation = Semaphore.withPermits(yield* Semaphore.make(1), 1)
  const atomically = <A, E, R>(effect: Effect.Effect<A, E, R>) => mutation(Effect.uninterruptible(effect))

  const present = (stored: StoredArtifact) =>
    new Artifact({
      id: stored.id,
      name: stored.name,
      contentType: stored.contentType,
      size: stored.size,
      createdAt: stored.createdAt,
      collection: stored.collection,
      url: `${publicUrl}${viewerPath(stored.id)}`,
      rawUrl: `${publicUrl}${rawPath(stored.id, stored.name)}`,
      ...(hasThumbnail(stored.contentType) ? { thumbUrl: `${publicUrl}${thumbPath(stored.id)}` } : {})
    })

  const list = (query: {
    collection?: string | undefined
    q?: string | undefined
    cursor?: string | undefined
    limit: number
  }) =>
    index
      .list({
        collection: query.collection?.trim() || undefined,
        search: query.q?.trim() || undefined,
        cursor: query.cursor,
        limit: query.limit
      })
      .pipe(
        Effect.map(
          ({ items, nextCursor }) =>
            new ArtifactPage({ items: items.map(present), ...(nextCursor === undefined ? {} : { nextCursor }) })
        )
      )

  const groups = (query: { cursor?: string | undefined; limit: number }) =>
    index.groups({ cursor: query.cursor, limit: query.limit, previewSize: GROUP_PREVIEW_SIZE }).pipe(
      Effect.map(
        ({ items, nextCursor }) =>
          new GroupPage({
            items: items.map(({ preview, ...rest }) => new ArtifactGroup({ ...rest, preview: preview.map(present) })),
            ...(nextCursor === undefined ? {} : { nextCursor })
          })
      )
    )

  const get = (id: ArtifactId) =>
    index.get(id).pipe(
      Effect.flatMap(
        Option.match({
          onSome: Effect.succeed,
          // Not indexed (yet): storage is authoritative, so look there and backfill.
          onNone: () => storage.head(id).pipe(Effect.tap(index.insert))
        })
      ),
      Effect.map(present)
    )

  const upload = Effect.fn("Artifacts.upload")(function* (input: UploadInput) {
    const stored = yield* storage.put({
      id: input.id,
      name: input.name,
      contentType: resolveContentType(input.name, input.contentType),
      body: input.body,
      collection: input.collection.trim()
    })
    yield* index.insert(stored)
    yield* Effect.logInfo(`Stored ${stored.name} as ${stored.id}`)
    return present(stored)
  })

  /** Unindexed once it's gone from storage, or already was; a failed delete leaves it listed. */
  const remove = (id: ArtifactId) => {
    const forget = Effect.andThen(index.remove(id), thumbnails.remove(id))
    return atomically(
      storage.remove(id).pipe(
        Effect.catchTag("ArtifactNotFound", (missing) => Effect.andThen(forget, Effect.fail(missing))),
        Effect.andThen(forget)
      )
    )
  }

  const move = (id: ArtifactId, collection: string) =>
    atomically(storage.setCollection(id, collection.trim()).pipe(Effect.tap(index.insert), Effect.map(present)))

  const exists = (id: ArtifactId) =>
    storage.head(id).pipe(
      Effect.as(true),
      Effect.catchTag("ArtifactNotFound", () => Effect.succeed(false))
    )

  const stats = index.stats.pipe(
    Effect.map(
      ({ lastUploadAt, ...rest }) => new Stats({ ...rest, ...(lastUploadAt === undefined ? {} : { lastUploadAt }) })
    )
  )

  const collections = index.collections.pipe(
    Effect.map((collections) => collections.map((collection) => new CollectionSummary(collection)))
  )

  return { present, list, mosaic: groups, get, upload, move, remove, exists, stats, collections }
})

/** Everything you can do with artifacts, shared by the HTTP API and the MCP server. */
export class Artifacts extends Context.Service<Artifacts, Effect.Success<typeof makeArtifacts>>()(
  "sloppa/server/Artifacts"
) {
  /** Also hands out the storage and thumbnails, which the file routes serve from directly. */
  static readonly layer = Layer.effect(Artifacts, makeArtifacts).pipe(
    Layer.provideMerge(
      Layer.mergeAll(ArtifactIndex.layer, Thumbnails.layer).pipe(Layer.provideMerge(ArtifactStorage.layer))
    )
  )
}
