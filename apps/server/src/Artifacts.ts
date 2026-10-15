import {
  Artifact,
  type ArtifactId,
  ArtifactGroup,
  ArtifactPage,
  GROUP_PREVIEW_SIZE,
  GroupPage,
  normalizePr,
  PrSummary,
  rawPath,
  RepoSummary,
  repoOfPr,
  Stats,
  thumbPath,
  viewerPath
} from "@sloppa/api"
import { Context, Effect, Layer, Option } from "effect"
import { AppConfig } from "./Config.ts"
import { resolveContentType } from "./ContentType.ts"
import { ArtifactIndex } from "./Index.ts"
import { ArtifactStorage, type StoredArtifact, type Upload } from "./Storage.ts"
import { hasThumbnail, Thumbnails } from "./Thumbnails.ts"

export interface UploadInput {
  readonly id?: ArtifactId | undefined
  readonly name: string
  /** What the uploader claimed; the file extension wins when this is missing or generic. */
  readonly contentType: string | undefined
  readonly body: Upload
  readonly repo: string | undefined
  readonly pr: string | undefined
}

const makeArtifacts = Effect.gen(function* () {
  const storage = yield* ArtifactStorage
  const index = yield* ArtifactIndex
  const thumbnails = yield* Thumbnails
  const { publicUrl } = yield* AppConfig

  const present = (stored: StoredArtifact) =>
    new Artifact({
      id: stored.id,
      name: stored.name,
      contentType: stored.contentType,
      size: stored.size,
      createdAt: stored.createdAt,
      ...(stored.repo === undefined ? {} : { repo: stored.repo }),
      ...(stored.pr === undefined ? {} : { pr: stored.pr }),
      url: `${publicUrl}${viewerPath(stored.id)}`,
      rawUrl: `${publicUrl}${rawPath(stored.id, stored.name)}`,
      ...(hasThumbnail(stored.contentType) ? { thumbUrl: `${publicUrl}${thumbPath(stored.id)}` } : {})
    })

  const list = (query: {
    repo?: string | undefined
    pr?: string | undefined
    noPr?: boolean | undefined
    q?: string | undefined
    cursor?: string | undefined
    limit: number
  }) =>
    index
      .list({
        repo: query.repo,
        pr: query.pr === undefined ? undefined : normalizePr(query.pr),
        noPr: query.noPr ?? false,
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

  const groups = (query: { repo?: string | undefined; cursor?: string | undefined; limit: number }) =>
    index.groups({ repo: query.repo, cursor: query.cursor, limit: query.limit, previewSize: GROUP_PREVIEW_SIZE }).pipe(
      Effect.map(
        ({ items, nextCursor }) =>
          new GroupPage({
            items: items.map(
              ({ pr, repo, preview, ...rest }) =>
                new ArtifactGroup({
                  ...rest,
                  ...(pr === undefined ? {} : { pr }),
                  ...(repo === undefined ? {} : { repo }),
                  preview: preview.map(present)
                })
            ),
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
    const pr = input.pr === undefined ? undefined : normalizePr(input.pr)
    const stored = yield* storage.put({
      id: input.id,
      name: input.name,
      contentType: resolveContentType(input.name, input.contentType),
      body: input.body,
      repo: input.repo ?? (pr === undefined ? undefined : repoOfPr(pr)),
      pr
    })
    yield* index.insert(stored)
    yield* Effect.logInfo(`Stored ${stored.name} as ${stored.id}`)
    return present(stored)
  })

  const remove = (id: ArtifactId) =>
    storage.remove(id).pipe(Effect.ensuring(Effect.andThen(index.remove(id), thumbnails.remove(id))))

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

  const prs = (repo: string | undefined) =>
    index
      .prs(repo)
      .pipe(
        Effect.map((prs) =>
          prs.map(({ repo, ...rest }) => new PrSummary({ ...rest, ...(repo === undefined ? {} : { repo }) }))
        )
      )

  const repos = index.repos.pipe(Effect.map((repos) => repos.map((repo) => new RepoSummary(repo))))

  return { present, list, groups, get, upload, remove, exists, stats, prs, repos }
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
