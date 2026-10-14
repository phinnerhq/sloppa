import type { ArtifactId, ArtifactNotFound, StorageError } from "@sloppa/api"
import { Context, type DateTime, Effect, Layer, Path, type Stream } from "effect"
import { AppConfig } from "./Config.ts"
import { makeFsStorage } from "./FsStorage.ts"
import { makeS3Storage } from "./S3Storage.ts"

export interface StoredArtifact {
  readonly id: ArtifactId
  readonly name: string
  readonly contentType: string
  readonly size: number
  readonly createdAt: DateTime.Utc
  readonly repo: string | undefined
  readonly pr: string | undefined
}

export interface PutInput {
  /** Picked up front for upload tickets; a fresh UUID otherwise. */
  readonly id?: ArtifactId | undefined
  readonly name: string
  readonly contentType: string
  /**
   * A sized body, so fetch sends a Content-Length (some S3 servers, RustFS included, reject chunked PutObject).
   * Don't also pass `ContentLength` to S3: distilled then drops the header.
   */
  readonly body: Blob
  readonly repo: string | undefined
  readonly pr: string | undefined
}

export interface ReadResult {
  readonly artifact: StoredArtifact
  readonly body: Stream.Stream<Uint8Array, Error>
  readonly contentLength: number
  /** Set when a byte range was served. */
  readonly contentRange: string | undefined
}

/**
 * Where the files live: an S3 bucket in production, plain files under
 * `DATA_DIR` otherwise. Each artifact is keyed by its UUID alone, with name,
 * repo, PR and upload time stored next to it, so the storage stays the source
 * of truth and the SQLite index can always be rebuilt from it (see `ArtifactIndex`).
 */
export class ArtifactStorage extends Context.Service<
  ArtifactStorage,
  {
    readonly put: (input: PutInput) => Effect.Effect<StoredArtifact, StorageError>
    readonly head: (id: ArtifactId) => Effect.Effect<StoredArtifact, ArtifactNotFound | StorageError>
    readonly read: (id: ArtifactId, range?: string) => Effect.Effect<ReadResult, ArtifactNotFound | StorageError>
    /** Every stored artifact: for rebuilding the index only. */
    readonly scan: Stream.Stream<StoredArtifact, StorageError>
    readonly remove: (id: ArtifactId) => Effect.Effect<void, ArtifactNotFound | StorageError>
  }
>()("sloppa/server/ArtifactStorage") {
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
