// What the storage backends share with each other and the app: the artifact records, the backend
// contract, and helpers. Apart from Storage.ts, which picks a backend, so neither imports the other.
import {
  type ArtifactId,
  type ArtifactNotFound,
  collectionOfPr,
  type StorageError,
  UNKNOWN_COLLECTION
} from "@sloppa/api"
import { type DateTime, Effect, Option, Stream } from "effect"

export interface StoredArtifact {
  readonly id: ArtifactId
  readonly name: string
  readonly contentType: string
  readonly size: number
  readonly createdAt: DateTime.Utc
  readonly collection: string
}

/**
 * Artifacts stored before collections were filed under a PR and its repo: they read back as the PR's
 * label, so they land with the ones uploaded since, or in `unknown` without either.
 */
export const legacyCollection = (repo: string | undefined, pr: string | undefined) =>
  pr === undefined ? (repo ?? UNKNOWN_COLLECTION) : collectionOfPr(pr)

/**
 * An upload's bytes, streamed, with the size known up front. The filesystem backend writes the stream
 * straight to disk; S3 needs it whole (see `S3Storage.put`).
 */
export interface Upload {
  readonly size: number
  readonly stream: Stream.Stream<Uint8Array, StorageError>
}

/** A stream gathered into one buffer of its known size. */
export const readAll = <E>(stream: Stream.Stream<Uint8Array, E>, size: number) =>
  Stream.runCollect(stream).pipe(
    Effect.map((chunks) => {
      const bytes = new Uint8Array(size)
      let offset = 0
      for (const chunk of chunks) {
        bytes.set(chunk, offset)
        offset += chunk.byteLength
      }
      return bytes
    })
  )

/** For bytes already in memory. */
export const uploadOf = (bytes: Uint8Array): Upload => ({ size: bytes.byteLength, stream: Stream.make(bytes) })

export interface PutInput {
  /** Picked up front for upload tickets; a fresh UUID otherwise. */
  readonly id?: ArtifactId | undefined
  readonly name: string
  readonly contentType: string
  readonly body: Upload
  readonly collection: string
}

interface ReadResult {
  readonly artifact: StoredArtifact
  readonly body: Stream.Stream<Uint8Array, Error>
  readonly contentLength: number
  /** Set when a byte range was served. */
  readonly contentRange: string | undefined
}

/** What a storage backend does: see `ArtifactStorage`, which picks one. */
export interface ArtifactStore {
  readonly put: (input: PutInput) => Effect.Effect<StoredArtifact, StorageError>
  readonly head: (id: ArtifactId) => Effect.Effect<StoredArtifact, ArtifactNotFound | StorageError>
  readonly read: (id: ArtifactId, range?: string) => Effect.Effect<ReadResult, ArtifactNotFound | StorageError>
  /** Every stored artifact: for rebuilding the index only. */
  readonly scan: Stream.Stream<StoredArtifact, StorageError>
  readonly remove: (id: ArtifactId) => Effect.Effect<void, ArtifactNotFound | StorageError>
  /** Files it under another collection; the bytes, id and links stay as they are. */
  readonly setCollection: (
    id: ArtifactId,
    collection: string
  ) => Effect.Effect<StoredArtifact, ArtifactNotFound | StorageError>
}

/**
 * For scans: an artifact deleted mid-scan is skipped quietly, one that can't be read is skipped with a
 * warning, so a single bad file doesn't keep the rest from being indexed.
 */
export const skipUnreadable = <E extends ArtifactNotFound | StorageError, R>(
  head: Effect.Effect<StoredArtifact, E, R>
): Effect.Effect<Option.Option<StoredArtifact>, never, R> =>
  head.pipe(
    Effect.asSome,
    Effect.catch((error) =>
      error._tag === "ArtifactNotFound"
        ? Effect.succeedNone
        : Effect.logWarning(`Skipped an unreadable artifact: ${error.message}`).pipe(Effect.as(Option.none()))
    )
  )
