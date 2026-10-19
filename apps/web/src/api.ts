import { keepPreviousData, mutationOptions, QueryClient, queryOptions } from "@tanstack/react-query"
import {
  agentsCreateMutation,
  agentsCreateSetupMutation,
  agentsListOptions,
  agentsRenameMutation,
  agentsRevokeMutation,
  agentsRevokeSetupMutation,
  artifactsCollectionsOptions,
  artifactsDeleteMutation,
  artifactsGetOptions,
  artifactsGetQueryKey,
  artifactsListInfiniteOptions,
  artifactsMosaicInfiniteOptions,
  artifactsStatsOptions,
  previewsListOptions,
  previewsStopMutation,
  sessionChangePasswordMutation,
  sessionCurrentOptions,
  sessionLoginMutation,
  sessionLogoutMutation
} from "./client/@tanstack/react-query.gen.ts"
import { GROUP_PAGE_SIZE, PAGE_SIZE } from "./constants.ts"

/** How long data survives nothing showing it, so coming back shows it at once (then revalidates). */
const CACHE = 5 * 60 * 1000

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      gcTime: CACHE,
      // Signed out or not found won't come right on a retry: say so at once.
      retry: false,
      refetchOnWindowFocus: false
    }
  }
})

/** Every query under one of the API document's tags. */
const invalidate = (tag: "Artifacts" | "Agents" | "Previews") =>
  queryClient.invalidateQueries({ queryKey: [{ tags: [tag] }] })

/** Who is signed in; fails with `unauthorized` when nobody is. Only signing in or out changes it. */
export const sessionQuery = { ...sessionCurrentOptions(), staleTime: Infinity }

/** Signing in changes what every query returns. */
export const loginMutation = mutationOptions({
  ...sessionLoginMutation(),
  onSuccess: () => queryClient.invalidateQueries()
})
/** Everything goes back to loading, so nothing signed in shows while the signed-out answers come. */
export const logoutMutation = mutationOptions({
  ...sessionLogoutMutation(),
  onSuccess: () => queryClient.resetQueries()
})
export const changePasswordMutation = sessionChangePasswordMutation()

export const agentsQuery = agentsListOptions()
export const createAgentMutation = mutationOptions({ ...agentsCreateMutation(), onSuccess: () => invalidate("Agents") })
export const revokeAgentMutation = mutationOptions({ ...agentsRevokeMutation(), onSuccess: () => invalidate("Agents") })
export const renameAgentMutation = mutationOptions({ ...agentsRenameMutation(), onSuccess: () => invalidate("Agents") })
export const createSetupMutation = agentsCreateSetupMutation()
export const revokeSetupMutation = agentsRevokeSetupMutation()

/** Dev servers shared with `sloppa preview run`, live for as long as that runs. */
export const previewsQuery = previewsListOptions()
export const stopPreviewMutation = mutationOptions({
  ...previewsStopMutation(),
  onSuccess: () => invalidate("Previews")
})

/** Collections that have artifacts, newest activity first. */
export const collectionsQuery = artifactsCollectionsOptions()

export const statsQuery = artifactsStatsOptions()

export interface ListFilter {
  /** "" for all of them. */
  readonly collection: string
  /** Searched on the server, so it reaches artifacts that aren't loaded yet. */
  readonly search: string
}

/** Pages (not items) are kept, so the last one's `nextCursor` says whether there is more. */
const paged = {
  initialPageParam: {},
  getNextPageParam: (page: { readonly nextCursor?: string }) => page.nextCursor
}

/** Newest first. */
export const artifactPagesQuery = ({ collection, search }: ListFilter) => {
  const q = search.trim()
  return {
    ...artifactsListInfiniteOptions({
      query: { limit: PAGE_SIZE, ...(collection === "" ? {} : { collection }), ...(q === "" ? {} : { q }) }
    }),
    ...paged,
    // A filter that's never been loaded keeps the previous one's results until its own land.
    placeholderData: keepPreviousData
  }
}

/** The mosaic: a tile per collection, most recently active first. */
export const groupPagesQuery = {
  ...artifactsMosaicInfiniteOptions({ query: { limit: GROUP_PAGE_SIZE } }),
  ...paged
}

/** Where the dashboard lists a collection's artifacts. */
export const collectionPath = (collection: string) => `/?${new URLSearchParams({ collection })}`

/** Uploads leave an artifact as it is, and moves and deletes invalidate it. */
export const artifactQuery = (id: string) => ({ ...artifactsGetOptions({ path: { id } }), staleTime: Infinity })

/** Its own copy goes rather than refetching under the page, so Back finds it gone. */
export const deleteArtifactMutation = mutationOptions({
  ...artifactsDeleteMutation(),
  onSuccess: (_, { path }) => {
    queryClient.removeQueries({ queryKey: artifactsGetQueryKey({ path }) })
    return invalidate("Artifacts")
  }
})

/** Larger text files get a download button instead of an inline preview. */
export const MAX_TEXT_PREVIEW = 1024 * 1024

/** The file itself, outside the API. */
export const textPreviewQuery = (url: string) =>
  queryOptions({
    queryKey: ["textPreview", url],
    queryFn: async ({ signal }) => {
      const response = await fetch(url, { signal })
      if (!response.ok) throw new Error(`${response.status} ${response.statusText}`)
      return response.text()
    },
    // An artifact's file never changes.
    staleTime: Infinity
  })
