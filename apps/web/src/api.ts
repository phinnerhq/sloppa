import { BrowserKeyValueStore } from "@effect/platform-browser"
import { Api, type ArtifactId, Authorization, PAGE_SIZE } from "@sloppa/api"
import { Effect, Layer, Option, Schema, Stream } from "effect"
import { FetchHttpClient, HttpClient, HttpClientRequest, HttpClientResponse } from "effect/http"
import { HttpApiMiddleware } from "effect/http-api"
import { Atom, AtomHttpApi } from "effect/reactivity"

/** The server's UPLOAD_TOKEN, as used by MCP clients; it gates listing, single artifacts are public. */
export const tokenAtom = Atom.kvs({
  runtime: Atom.runtime(BrowserKeyValueStore.layerLocalStorage),
  key: "sloppa-artifacts-token",
  schema: Schema.NullOr(Schema.String),
  defaultValue: () => null
})

const authorization = (token: string | null) =>
  HttpApiMiddleware.layerClient(Authorization, ({ next, request }) =>
    next(token === null ? request : HttpClientRequest.bearerToken(request, token))
  )

export class ApiClient extends AtomHttpApi.Service<ApiClient>()("sloppa/web/ApiClient", {
  api: Api,
  httpClient: (get) => Layer.mergeAll(FetchHttpClient.layer, authorization(get(tokenAtom))),
  baseUrl: window.location.origin
}) {}

/** Invalidated by uploads/deletes made from this tab. */
const ARTIFACTS_KEY = ["artifacts"]

/** How long the list's data survives leaving the page, so coming back shows it at once (then revalidates). */
const LIST_CACHE = "5 minutes"

export const reposAtom = ApiClient.query("artifacts", "repos", {
  reactivityKeys: ARTIFACTS_KEY,
  timeToLive: LIST_CACHE
})

export const statsAtom = ApiClient.query("artifacts", "stats", {
  reactivityKeys: ARTIFACTS_KEY,
  timeToLive: LIST_CACHE
})

/** PRs that have artifacts, newest activity first; "" for every repo. */
export const prsAtom = Atom.family((repo: string) =>
  ApiClient.query("artifacts", "prs", {
    query: repo === "" ? {} : { repo },
    reactivityKeys: ARTIFACTS_KEY,
    timeToLive: LIST_CACHE
  })
)

export interface ListFilter {
  readonly repo: string
  readonly pr: string
}

/**
 * Newest first; write to the atom to pull the next page. Pages (not items) are
 * accumulated so the last one's `nextCursor` says whether there is more.
 * Keyed by a string so equal filters share one atom.
 */
const pagesFamily = Atom.family((key: string) => {
  const { repo, pr } = JSON.parse(key) as ListFilter
  return ApiClient.runtime
    .pull(
      Stream.paginate(undefined as string | undefined, (cursor) =>
        Effect.gen(function* () {
          const client = yield* ApiClient
          const page = yield* client.artifacts.list({
            query: {
              limit: PAGE_SIZE,
              ...(repo === "" ? {} : { repo }),
              ...(pr === "" ? {} : { pr }),
              ...(cursor === undefined ? {} : { cursor })
            }
          })
          return [[page], Option.fromNullishOr(page.nextCursor)] as const
        })
      )
    )
    .pipe(Atom.withReactivity(ARTIFACTS_KEY), Atom.setIdleTTL(LIST_CACHE))
})

export const artifactPagesAtom = (filter: ListFilter) =>
  pagesFamily(JSON.stringify({ repo: filter.repo, pr: filter.pr }))

export const artifactAtom = Atom.family((id: ArtifactId) =>
  ApiClient.query("artifacts", "get", { params: { id }, timeToLive: "5 minutes" })
)

export const deleteArtifactAtom = ApiClient.mutation("artifacts", "delete")
export const invalidateArtifacts = ARTIFACTS_KEY

/** Larger text files get a download button instead of an inline preview. */
export const MAX_TEXT_PREVIEW = 1024 * 1024

export const textPreviewAtom = Atom.family((url: string) =>
  Atom.make(
    HttpClient.get(url).pipe(
      Effect.flatMap(HttpClientResponse.filterStatusOk),
      Effect.flatMap((response) => response.text),
      Effect.provide(FetchHttpClient.layer)
    )
  )
)
