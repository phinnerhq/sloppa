import { type ArtifactId, ArtifactNotFound, StorageError } from "@sloppa/api"
import { DateTime, Effect, Option, Schema, Stream } from "effect"
import { mkdir, readdir, rename, rm } from "node:fs/promises"
import { join } from "node:path"
import type { ArtifactStorage, PutInput, StoredArtifact } from "./Storage.ts"

/** Sidecar written next to each file: everything S3 would keep as object metadata. */
const Meta = Schema.Struct({
  name: Schema.String,
  contentType: Schema.String,
  size: Schema.Finite,
  createdMs: Schema.Finite,
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
  return start <= end && start < size ? { start, end } : undefined
}

const fail = (action: string) => (cause: unknown) =>
  new StorageError({ message: `${action}: ${cause instanceof Error ? cause.message : String(cause)}` })

/**
 * Artifacts as plain files under `dir`: `<uuid>` holds the bytes and
 * `<uuid>.json` the metadata. For development and small single-box setups;
 * production sets `S3_ENDPOINT`.
 */
export const makeFsStorage = (dir: string) =>
  Effect.gen(function* () {
    yield* Effect.tryPromise({ try: () => mkdir(dir, { recursive: true }), catch: fail("create storage dir") }).pipe(
      Effect.orDie
    )
    const fileOf = (id: string) => join(dir, id)
    const metaOf = (id: string) => join(dir, `${id}.json`)

    const head = Effect.fn("FsStorage.head")(function* (id: ArtifactId) {
      const file = Bun.file(metaOf(id))
      if (!(yield* Effect.promise(() => file.exists()))) return yield* new ArtifactNotFound({ id })
      const text = yield* Effect.tryPromise({ try: () => file.text(), catch: fail("read metadata") })
      const meta = yield* decodeMeta(text).pipe(Effect.mapError(fail(`decode metadata of ${id}`)))
      const artifact: StoredArtifact = {
        id,
        name: meta.name,
        contentType: meta.contentType,
        size: meta.size,
        createdAt: DateTime.makeUnsafe(meta.createdMs),
        repo: meta.repo,
        pr: meta.pr
      }
      return artifact
    })

    const put = Effect.fn("FsStorage.put")(function* (input: PutInput) {
      const id = input.id ?? (crypto.randomUUID() as ArtifactId)
      const createdMs = DateTime.toEpochMillis(yield* DateTime.now)
      const meta = yield* encodeMeta({
        name: input.name,
        contentType: input.contentType,
        size: input.body.size,
        createdMs,
        ...(input.repo === undefined ? {} : { repo: input.repo }),
        ...(input.pr === undefined ? {} : { pr: input.pr })
      }).pipe(Effect.orDie)
      // Bytes first, metadata last: an artifact exists once its sidecar does.
      yield* Effect.tryPromise({
        try: async () => {
          await Bun.write(fileOf(id), input.body)
          await Bun.write(`${metaOf(id)}.tmp`, meta)
          await rename(`${metaOf(id)}.tmp`, metaOf(id))
        },
        catch: fail("write artifact")
      })
      return yield* head(id).pipe(Effect.catchTag("ArtifactNotFound", Effect.die))
    })

    const read = Effect.fn("FsStorage.read")(function* (id: ArtifactId, range?: string) {
      const artifact = yield* head(id)
      const file = Bun.file(fileOf(id))
      const slice = parseRange(range, artifact.size)
      const blob = slice === undefined ? file : file.slice(slice.start, slice.end + 1)
      return {
        artifact,
        body: Stream.fromReadableStream({ evaluate: () => blob.stream(), onError: fail("read artifact") }),
        contentLength: slice === undefined ? artifact.size : slice.end - slice.start + 1,
        contentRange: slice === undefined ? undefined : `bytes ${slice.start}-${slice.end}/${artifact.size}`
      }
    })

    const scan = Stream.fromIterableEffect(
      Effect.tryPromise({ try: () => readdir(dir), catch: fail("list storage dir") })
    ).pipe(
      Stream.filter((entry) => entry.endsWith(".json") && UUID.test(entry.slice(0, -5))),
      Stream.mapEffect((entry) => head(entry.slice(0, -5) as ArtifactId).pipe(Effect.option), { concurrency: 16 }),
      Stream.filter(Option.isSome),
      Stream.map((found) => found.value)
    )

    const remove = Effect.fn("FsStorage.remove")(function* (id: ArtifactId) {
      yield* head(id)
      yield* Effect.tryPromise({
        try: () => Promise.all([rm(metaOf(id), { force: true }), rm(fileOf(id), { force: true })]),
        catch: fail("delete artifact")
      })
    })

    const service: ArtifactStorage["Service"] = { put, head, read, scan, remove }
    return service
  })
