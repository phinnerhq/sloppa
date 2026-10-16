import { type ArtifactId, ArtifactNotFound, StorageError } from "@sloppa/api"
import { DateTime, Effect, FileSystem, Option, Path, PlatformError, Schema, Stream } from "effect"
import {
  type ArtifactStore,
  legacyCollection,
  type PutInput,
  skipUnreadable,
  type StoredArtifact
} from "./StorageShared.ts"

/** Sidecar written next to each file: everything S3 would keep as object metadata. */
const Meta = Schema.Struct({
  name: Schema.String,
  contentType: Schema.String,
  size: Schema.Finite,
  createdMs: Schema.Finite,
  collection: Schema.optionalKey(Schema.String),
  /** Written before collections; read only. */
  repo: Schema.optionalKey(Schema.String),
  pr: Schema.optionalKey(Schema.String)
})
const decodeMeta = Schema.decodeUnknownEffect(Schema.fromJsonString(Meta))
const encodeMeta = Schema.encodeEffect(Schema.fromJsonString(Meta))

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/** `bytes=a-b`, `bytes=a-` or `bytes=-n`; anything else (or unsatisfiable) serves the whole file. */
const parseRange = (header: string | undefined, size: number) => {
  const match = header === undefined ? null : /^bytes=(\d*)-(\d*)$/.exec(header.trim())
  if (match === null || (match[1] === "" && match[2] === "")) return undefined
  const [start, end] =
    match[1] === ""
      ? [Math.max(0, size - Number(match[2])), size - 1]
      : [Number(match[1]), match[2] === "" ? size - 1 : Math.min(Number(match[2]), size - 1)]
  // The end is clamped to the last byte, so a start past it (or past an empty file) fails this too.
  return start <= end ? { start, end } : undefined
}

/** Platform failures surface as `StorageError`s, prefixed with what was being done. */
const storageError =
  (action: string) =>
  <A, E, R>(
    effect: Effect.Effect<A, E, R>
  ): Effect.Effect<A, Exclude<E, PlatformError.PlatformError> | StorageError, R> =>
    effect.pipe(
      Effect.mapError((cause) =>
        PlatformError.isPlatformError(cause)
          ? new StorageError({ message: `${action}: ${cause.message}` })
          : (cause as Exclude<E, PlatformError.PlatformError>)
      )
    )

/**
 * Artifacts as plain files under `dir`: `<uuid>` holds the bytes and
 * `<uuid>.json` the metadata. For development and small single-box setups;
 * production sets `S3_ENDPOINT`.
 */
export const makeFsStorage = (dir: string) =>
  Effect.gen(function* () {
    const fs = yield* FileSystem.FileSystem
    const path = yield* Path.Path
    yield* fs.makeDirectory(dir, { recursive: true }).pipe(Effect.orDie)
    const fileOf = (id: string) => path.join(dir, id)
    const metaOf = (id: string) => path.join(dir, `${id}.json`)

    const head = Effect.fn("FsStorage.head")(function* (id: ArtifactId) {
      const text = yield* fs.readFileString(metaOf(id)).pipe(
        Effect.catchReason("PlatformError", "NotFound", () => Effect.fail(new ArtifactNotFound({ id }))),
        storageError("read metadata")
      )
      const meta = yield* decodeMeta(text).pipe(
        Effect.mapError((cause) => new StorageError({ message: `decode metadata of ${id}: ${cause.message}` }))
      )
      const artifact: StoredArtifact = {
        id,
        name: meta.name,
        contentType: meta.contentType,
        size: meta.size,
        createdAt: DateTime.makeUnsafe(meta.createdMs),
        collection: meta.collection ?? legacyCollection(meta.repo, meta.pr)
      }
      return artifact
    })

    /**
     * Swaps a sidecar in whole, so a reader sees the old one or the new one, never half of one. Each
     * write gets its own temp file: two at once (moves racing) can't interleave in a shared one.
     */
    const writeMeta = (id: ArtifactId, meta: string) => {
      const tmp = `${metaOf(id)}.${crypto.randomUUID()}.tmp`
      return fs.writeFileString(tmp, meta).pipe(
        Effect.andThen(fs.rename(tmp, metaOf(id))),
        Effect.onError(() => fs.remove(tmp).pipe(Effect.ignore)),
        storageError("write metadata")
      )
    }

    const put = Effect.fn("FsStorage.put")(function* (input: PutInput) {
      const id = input.id ?? (crypto.randomUUID() as ArtifactId)
      const createdMs = DateTime.toEpochMillis(yield* DateTime.now)
      const meta = yield* encodeMeta({
        name: input.name,
        contentType: input.contentType,
        size: input.body.size,
        createdMs,
        collection: input.collection
      }).pipe(Effect.orDie)
      // Bytes first, metadata last: an artifact exists once its sidecar does. The body is streamed to disk,
      // never buffered whole.
      yield* input.body.stream.pipe(Stream.run(fs.sink(fileOf(id))), storageError("write artifact"))
      yield* writeMeta(id, meta)
      return yield* head(id).pipe(Effect.catchTag("ArtifactNotFound", Effect.die))
    })

    const read = Effect.fn("FsStorage.read")(function* (id: ArtifactId, range?: string) {
      const artifact = yield* head(id)
      const slice = parseRange(range, artifact.size)
      return {
        artifact,
        body: fs.stream(
          fileOf(id),
          slice === undefined ? {} : { offset: slice.start, bytesToRead: slice.end - slice.start + 1 }
        ),
        contentLength: slice === undefined ? artifact.size : slice.end - slice.start + 1,
        contentRange: slice === undefined ? undefined : `bytes ${slice.start}-${slice.end}/${artifact.size}`
      }
    })

    const scan = Stream.fromIterableEffect(fs.readDirectory(dir).pipe(storageError("list storage dir"))).pipe(
      Stream.filter((entry) => entry.endsWith(".json") && UUID.test(entry.slice(0, -5))),
      Stream.mapEffect((entry) => head(entry.slice(0, -5) as ArtifactId).pipe(skipUnreadable), { concurrency: 16 }),
      Stream.filter(Option.isSome),
      Stream.map((found) => found.value)
    )

    const remove = Effect.fn("FsStorage.remove")(function* (id: ArtifactId) {
      yield* head(id)
      yield* Effect.all([fs.remove(metaOf(id), { force: true }), fs.remove(fileOf(id), { force: true })], {
        concurrency: 2
      }).pipe(storageError("delete artifact"))
    })

    const setCollection = Effect.fn("FsStorage.setCollection")(function* (id: ArtifactId, collection: string) {
      const artifact = yield* head(id)
      const meta = yield* encodeMeta({
        name: artifact.name,
        contentType: artifact.contentType,
        size: artifact.size,
        createdMs: DateTime.toEpochMillis(artifact.createdAt),
        collection
      }).pipe(Effect.orDie)
      yield* writeMeta(id, meta)
      return { ...artifact, collection }
    })

    const service: ArtifactStore = { put, head, read, scan, remove, setCollection }
    return service
  })
