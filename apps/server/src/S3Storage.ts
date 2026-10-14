import { Credentials, Endpoint, Region } from "@distilled.cloud/aws"
import * as S3 from "@distilled.cloud/aws/s3"
import { type ArtifactId, ArtifactNotFound, StorageError } from "@sloppa/api"
import { DateTime, Effect, Layer, Option, Predicate, Stream } from "effect"
import { FetchHttpClient, type HttpClient } from "effect/http"
import type { S3Config } from "./Config.ts"
import type { ArtifactStorage, PutInput, StoredArtifact } from "./Storage.ts"

const PREFIX = "artifacts/"
const keyOf = (id: string) => `${PREFIX}${id}`

// S3 user metadata must be ASCII, so free-form values are URI-encoded.
const encodeMeta = (value: string) => encodeURIComponent(value)
const decodeMeta = (value: string | undefined) => {
  if (value === undefined) return undefined
  try {
    return decodeURIComponent(value)
  } catch {
    return value
  }
}

const toStorageError = (cause: { readonly _tag: string; readonly message?: string | undefined }) =>
  new StorageError({ message: cause.message ? `${cause._tag}: ${cause.message}` : cause._tag })

/** Artifacts as plain objects under `artifacts/<uuid>`, metadata as object metadata. */
export const makeS3Storage = (s3: S3Config) =>
  Effect.gen(function* () {
    const Bucket = s3.bucket
    const services = yield* Effect.context<S3Services>()
    const run = <A, E>(effect: Effect.Effect<A, E, S3Services>) => Effect.provideContext(effect, services)

    yield* S3.headBucket({ Bucket }).pipe(
      Effect.catchTag("NotFound", () =>
        S3.createBucket({ Bucket }).pipe(
          Effect.catchTag("BucketAlreadyOwnedByYou", () => Effect.void),
          Effect.tap(() => Effect.logInfo(`Created bucket ${Bucket}`))
        )
      ),
      Effect.orDie
    )

    const fromHead = (id: ArtifactId, head: S3.HeadObjectOutput): StoredArtifact => ({
      id,
      name: decodeMeta(head.Metadata?.["name"]) ?? id,
      contentType: head.ContentType ?? "application/octet-stream",
      size: head.ContentLength ?? 0,
      // S3's LastModified only has second precision; keep upload order exact.
      createdAt: DateTime.makeUnsafe(Number(head.Metadata?.["uploaded-at"]) || head.LastModified?.getTime() || 0),
      repo: decodeMeta(head.Metadata?.["repo"]),
      pr: decodeMeta(head.Metadata?.["pr"])
    })

    const head = Effect.fn("ArtifactStorage.head")(function* (id: ArtifactId) {
      const output = yield* run(S3.headObject({ Bucket, Key: keyOf(id) })).pipe(
        Effect.catchTag("NotFound", () => Effect.fail(new ArtifactNotFound({ id }))),
        Effect.mapError((e) => (e._tag === "ArtifactNotFound" ? e : toStorageError(e)))
      )
      return fromHead(id, output)
    })

    const put = Effect.fn("ArtifactStorage.put")(function* (input: PutInput) {
      const id = input.id ?? (crypto.randomUUID() as ArtifactId)
      const uploadedAt = yield* DateTime.now
      yield* run(
        S3.putObject({
          Bucket,
          Key: keyOf(id),
          Body: input.body,
          ContentType: input.contentType,
          Metadata: {
            name: encodeMeta(input.name),
            "uploaded-at": String(DateTime.toEpochMillis(uploadedAt)),
            ...(input.repo === undefined ? {} : { repo: encodeMeta(input.repo) }),
            ...(input.pr === undefined ? {} : { pr: encodeMeta(input.pr) })
          }
        })
      ).pipe(Effect.mapError(toStorageError))
      return yield* head(id).pipe(Effect.catchTag("ArtifactNotFound", Effect.die))
    })

    const getObject = (id: ArtifactId, Range: string | undefined) =>
      run(S3.getObject({ Bucket, Key: keyOf(id), ...(Range === undefined ? {} : { Range }) })).pipe(
        Effect.catchTag("NoSuchKey", () => Effect.fail(new ArtifactNotFound({ id }))),
        Effect.mapError((e) => (e._tag === "ArtifactNotFound" ? e : toStorageError(e)))
      )

    const read = Effect.fn("ArtifactStorage.read")(function* (id: ArtifactId, range?: string) {
      // An unsatisfiable or malformed range just gets the whole file.
      const output = yield* getObject(id, range).pipe(
        Effect.catchTag("StorageError", (e) => (range === undefined ? Effect.fail(e) : getObject(id, undefined)))
      )
      const contentLength = output.ContentLength ?? 0
      const total = Number(output.ContentRange?.split("/")[1])
      return {
        artifact: { ...fromHead(id, output), size: Number.isFinite(total) ? total : contentLength },
        body: output.Body ?? Stream.empty,
        contentLength,
        contentRange: output.ContentRange
      }
    })

    const scan = S3.listObjectsV2.pages({ Bucket, Prefix: PREFIX }).pipe(
      Stream.provideContext(services),
      Stream.mapError(toStorageError),
      Stream.flatMap((page) => Stream.fromIterable(page.Contents ?? [])),
      Stream.map((object) => object.Key),
      Stream.filter(Predicate.isNotUndefined),
      Stream.mapEffect((key) => head(key.slice(PREFIX.length) as ArtifactId).pipe(Effect.option), {
        concurrency: 16
      }),
      Stream.filter(Option.isSome),
      Stream.map((found) => found.value)
    )

    const remove = Effect.fn("ArtifactStorage.remove")(function* (id: ArtifactId) {
      yield* head(id)
      yield* run(S3.deleteObject({ Bucket, Key: keyOf(id) })).pipe(Effect.mapError(toStorageError))
    })

    const service: ArtifactStorage["Service"] = { put, head, read, scan, remove }
    return service
  }).pipe(
    Effect.provide(
      Layer.mergeAll(
        FetchHttpClient.layer,
        Region.of(s3.region),
        Endpoint.of(s3.endpoint),
        Credentials.fromCredentials({ accessKeyId: s3.accessKeyId, secretAccessKey: s3.secretAccessKey }, s3.region)
      )
    )
  )

type S3Services = Credentials.Credentials | HttpClient.HttpClient
