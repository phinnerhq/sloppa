import { Context, Effect, Layer, Path } from "effect"
import { AppConfig } from "./Config.ts"
import { makeFsStorage } from "./FsStorage.ts"
import { makeS3Storage } from "./S3Storage.ts"
import type { ArtifactStore } from "./StorageShared.ts"

/**
 * Where the files live: an S3 bucket in production, plain files under
 * `DATA_DIR` otherwise. Each artifact is keyed by its UUID alone, with name,
 * collection and upload time stored next to it, so the storage stays the source
 * of truth and the SQLite index can always be rebuilt from it (see `ArtifactIndex`).
 */
export class ArtifactStorage extends Context.Service<ArtifactStorage, ArtifactStore>()(
  "sloppa/server/ArtifactStorage"
) {
  /** S3 when `S3_ENDPOINT` is set, the local filesystem otherwise. */
  static readonly layer = Layer.unwrap(
    Effect.gen(function* () {
      const { s3, dataDir } = yield* AppConfig
      if (s3 !== undefined) {
        yield* Effect.logInfo(`Storing artifacts in s3://${s3.bucket} at ${s3.endpoint}`)
        return Layer.effect(ArtifactStorage, makeS3Storage(s3))
      }
      const dir = (yield* Path.Path).join(dataDir, "files")
      yield* Effect.logInfo(`S3_ENDPOINT not set, storing artifacts on disk in ${dir}`)
      return Layer.effect(ArtifactStorage, makeFsStorage(dir))
    })
  )
}
