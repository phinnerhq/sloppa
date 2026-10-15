import { Api, type ArtifactId, GROUP_PAGE_SIZE, PAGE_SIZE } from "@sloppa/api"
import { Effect, Option, Stream } from "effect"
import { FetchHttpClient, HttpClient, HttpClientResponse } from "effect/http"
import { Atom, AtomHttpApi } from "effect/reactivity"

export class ApiClient extends AtomHttpApi.Service<ApiClient>()("sloppa/web/ApiClient", {
  api: Api,
  // Signed in through an HttpOnly cookie, which same-origin fetches send by themselves.
  httpClient: FetchHttpClient.layer,
  baseUrl: window.location.origin
}) {}

/** Signing in or out changes what every authorized query returns. */
const SESSION_KEY = ["session"]

/** Who is signed in; fails with `Unauthorized` when nobody is. */
export const sessionAtom = ApiClient.query("session", "current", { reactivityKeys: SESSION_KEY })

export const loginAtom = ApiClient.mutation("session", "login")
export const logoutAtom = ApiClient.mutation("session", "logout")
/** Pass as `reactivityKeys` when signing in or out. */
export const invalidateSession = ["session", "artifacts", "agents"]

const AGENTS_KEY = ["agents"]

export const agentsAtom = ApiClient.query("agents", "list", { reactivityKeys: [...SESSION_KEY, ...AGENTS_KEY] })
export const createAgentAtom = ApiClient.mutation("agents", "create")
export const revokeAgentAtom = ApiClient.mutation("agents", "revoke")
export const renameAgentAtom = ApiClient.mutation("agents", "rename")
export const createSetupAtom = ApiClient.mutation("agents", "createSetup")
export const revokeSetupAtom = ApiClient.mutation("agents", "revokeSetup")
export const invalidateAgents = AGENTS_KEY

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

/** The `pr` filter for the artifacts filed under none. */
export const NO_PR = "none"

export interface ListFilter {
  readonly repo: string
  /** A PR, `NO_PR`, or "" for all of them. */
  readonly pr: string
  /** Searched on the server, so it reaches artifacts that aren't loaded yet. */
  readonly search: string
}

/**
 * Write to the atom to pull the next page. Pages (not items) are accumulated so the last one's
 * `nextCursor` says whether there is more.
 */
const paged = <P extends { readonly nextCursor?: string }, E>(
  fetch: (client: ApiClient["Service"], cursor: string | undefined) => Effect.Effect<P, E>
) =>
  ApiClient.runtime
    .pull(
      Stream.paginate(undefined as string | undefined, (cursor) =>
        ApiClient.use((client) => fetch(client, cursor)).pipe(
          Effect.map((page) => [[page], Option.fromNullishOr(page.nextCursor)] as const)
        )
      )
    )
    .pipe(Atom.withReactivity(ARTIFACTS_KEY), Atom.setIdleTTL(LIST_CACHE))

const cursorQuery = (cursor: string | undefined) => (cursor === undefined ? {} : { cursor })

/** Newest first. Keyed by a string so equal filters share one atom. */
const pagesFamily = Atom.family((key: string) => {
  const { repo, pr, search } = JSON.parse(key) as ListFilter
  return paged((client, cursor) =>
    client.artifacts.list({
      query: {
        limit: PAGE_SIZE,
        ...(repo === "" ? {} : { repo }),
        ...(pr === "" ? {} : pr === NO_PR ? { noPr: "true" as const } : { pr }),
        ...(search === "" ? {} : { q: search }),
        ...cursorQuery(cursor)
      }
    })
  )
})

export const artifactPagesAtom = (filter: ListFilter) =>
  pagesFamily(JSON.stringify({ repo: filter.repo, pr: filter.pr, search: filter.search.trim() }))

/** The mosaic: a tile per PR, most recently active first; "" for every repo. */
export const groupPagesAtom = Atom.family((repo: string) =>
  paged((client, cursor) =>
    client.artifacts.groups({
      query: { limit: GROUP_PAGE_SIZE, ...(repo === "" ? {} : { repo }), ...cursorQuery(cursor) }
    })
  )
)

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
