import { type ArtifactId, type ArtifactNotFound, StorageError } from "@sloppa/api"
import { Context, Data, Effect, FileSystem, Layer, Path, Semaphore } from "effect"
import sharp from "sharp"
import { AppConfig } from "./Config.ts"
import { ArtifactStorage, readAll } from "./Storage.ts"

/** What sharp's prebuilt libvips decodes. GIFs keep their first frame. */
const RASTER = new Set(["image/jpeg", "image/png", "image/webp", "image/gif", "image/avif", "image/tiff"])

export const hasThumbnail = (contentType: string) => RASTER.has(contentType.split(";")[0]!.trim().toLowerCase())

/** Bounding box, in pixels: a grid slot is under 200 CSS px wide, so this stays sharp up to 2.5x. */
const SIZE = 480
/** Decoding needs 4 bytes per pixel, this caps it around 200 MB. */
const MAX_PIXELS = 50_000_000

/** The image couldn't be decoded or encoded. */
export class ThumbnailError extends Data.TaggedError("ThumbnailError")<{ readonly cause: unknown }> {}

/**
 * Small WebP previews for the inventory grid, so a page of screenshots doesn't download every original.
 * Made on first request and kept under `DATA_DIR/thumbs`: like the index, they derive from storage and
 * are safe to delete.
 */
export class Thumbnails extends Context.Service<
  Thumbnails,
  {
    readonly get: (id: ArtifactId) => Effect.Effect<Uint8Array, ArtifactNotFound | StorageError | ThumbnailError>
    readonly remove: (id: ArtifactId) => Effect.Effect<void>
  }
>()("sloppa/server/Thumbnails") {
  static readonly layer = Layer.effect(
    Thumbnails,
    Effect.gen(function* () {
      const fs = yield* FileSystem.FileSystem
      const path = yield* Path.Path
      const storage = yield* ArtifactStorage
      const dir = path.join((yield* AppConfig).dataDir, "thumbs")
      yield* fs.makeDirectory(dir, { recursive: true }).pipe(Effect.orDie)
      const fileOf = (id: ArtifactId) => path.join(dir, `${id}.webp`)
      // Each decode holds the whole original in memory: a page of fresh uploads takes turns.
      const decoding = yield* Semaphore.make(2)

      const make = Effect.fn("Thumbnails.make")(
        function* (id: ArtifactId) {
          const { artifact, body } = yield* storage.read(id)
          if (!hasThumbnail(artifact.contentType)) {
            return yield* new ThumbnailError({ cause: `${artifact.contentType} has no thumbnail` })
          }
          const original = yield* readAll(body, artifact.size).pipe(
            Effect.mapError((cause) => new StorageError({ message: `read artifact: ${cause.message}` }))
          )
          const thumb = yield* Effect.tryPromise({
            try: () =>
              sharp(original, { limitInputPixels: MAX_PIXELS })
                .rotate()
                .resize(SIZE, SIZE, { fit: "inside", withoutEnlargement: true })
                .webp({ quality: 80 })
                .toBuffer(),
            catch: (cause) => new ThumbnailError({ cause })
          })
          // Written aside, then renamed: a request racing this one never reads half a file.
          const tmp = `${fileOf(id)}.${crypto.randomUUID()}.tmp`
          yield* fs.writeFile(tmp, thumb).pipe(
            Effect.andThen(fs.rename(tmp, fileOf(id))),
            Effect.catch((cause) => Effect.logWarning(`Couldn't cache the thumbnail of ${id}: ${cause.message}`))
          )
          return thumb
        },
        (effect) => decoding.withPermit(effect)
      )

      return Thumbnails.of({
        get: (id) =>
          fs.readFile(fileOf(id)).pipe(
            Effect.catchReason("PlatformError", "NotFound", () => make(id)),
            Effect.mapError((cause) =>
              cause._tag === "PlatformError" ? new StorageError({ message: `read thumbnail: ${cause.message}` }) : cause
            )
          ),
        remove: (id) => fs.remove(fileOf(id), { force: true }).pipe(Effect.ignore)
      })
    })
  )
}
