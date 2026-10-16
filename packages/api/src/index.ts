import { Context, Schema } from "effect"
import { Multipart } from "effect/http"
import {
  HttpApi,
  HttpApiEndpoint,
  HttpApiGroup,
  HttpApiMiddleware,
  HttpApiSchema,
  HttpApiSecurity,
  OpenApi
} from "effect/http-api"

export const ArtifactId = Schema.String.pipe(Schema.check(Schema.isUUID()), Schema.brand("ArtifactId"))
export type ArtifactId = typeof ArtifactId.Type

export const COLLECTION_MAX = 200
/**
 * A free-form label grouping related artifacts, picked by whoever uploads them. A PR's reads best as
 * `owner/repo #42`, which the dashboard links to GitHub.
 */
export const CollectionName = Schema.Trimmed.check(Schema.isNonEmpty(), Schema.isMaxLength(COLLECTION_MAX))

/** Where artifacts stored before collections, with neither a PR nor a repo, read back as. */
export const UNKNOWN_COLLECTION = "unknown"

export class Artifact extends Schema.Class<Artifact, { readonly brand: unique symbol }>("Artifact")({
  id: ArtifactId,
  name: Schema.String,
  contentType: Schema.String,
  size: Schema.Finite,
  createdAt: Schema.DateTimeUtcFromString,
  collection: Schema.String,
  /** Viewer page in the SPA. */
  url: Schema.String,
  /** The file itself, served inline. */
  rawUrl: Schema.String,
  /** A small WebP preview, for images that have one. */
  thumbUrl: Schema.optionalKey(Schema.String)
}) {}

export class ArtifactPage extends Schema.Class<ArtifactPage, { readonly brand: unique symbol }>("ArtifactPage")({
  items: Schema.Array(Artifact),
  /** Pass back as `cursor` for the next page; absent on the last one. */
  nextCursor: Schema.optionalKey(Schema.String)
}) {}

/** A collection's artifacts, as one tile of the dashboard's mosaic. */
export class ArtifactGroup extends Schema.Class<ArtifactGroup, { readonly brand: unique symbol }>("ArtifactGroup")({
  collection: Schema.String,
  count: Schema.Int,
  lastUploadAt: Schema.DateTimeUtcFromString,
  /** The newest few, to show what's inside. */
  preview: Schema.Array(Artifact)
}) {}

export class GroupPage extends Schema.Class<GroupPage, { readonly brand: unique symbol }>("GroupPage")({
  items: Schema.Array(ArtifactGroup),
  /** Pass back as `cursor` for the next page; absent on the last one. */
  nextCursor: Schema.optionalKey(Schema.String)
}) {}

export class CollectionSummary extends Schema.Class<CollectionSummary, { readonly brand: unique symbol }>(
  "CollectionSummary"
)({
  collection: Schema.String,
  count: Schema.Int,
  lastUploadAt: Schema.DateTimeUtcFromString
}) {}

export class Stats extends Schema.Class<Stats, { readonly brand: unique symbol }>("Stats")({
  total: Schema.Int,
  totalBytes: Schema.Finite,
  last24h: Schema.Int,
  lastUploadAt: Schema.optionalKey(Schema.DateTimeUtcFromString)
}) {}

export class ArtifactNotFound extends Schema.TaggedError<ArtifactNotFound, { readonly brand: unique symbol }>()(
  "ArtifactNotFound",
  { id: Schema.String },
  { httpApiStatus: 404 }
) {}

export class Unauthorized extends Schema.TaggedError<Unauthorized, { readonly brand: unique symbol }>()(
  "Unauthorized",
  { message: Schema.String },
  { httpApiStatus: 401 }
) {}

export class StorageError extends Schema.TaggedError<StorageError, { readonly brand: unique symbol }>()(
  "StorageError",
  { message: Schema.String },
  { httpApiStatus: 502 }
) {}

export class InvalidCredentials extends Schema.TaggedError<InvalidCredentials, { readonly brand: unique symbol }>()(
  "InvalidCredentials",
  {},
  { httpApiStatus: 401 }
) {}

export class TooManyAttempts extends Schema.TaggedError<TooManyAttempts, { readonly brand: unique symbol }>()(
  "TooManyAttempts",
  { /** Seconds until the next attempt counts. */ retryAfter: Schema.Int },
  { httpApiStatus: 429 }
) {}

export class User extends Schema.Class<User, { readonly brand: unique symbol }>("User")({
  username: Schema.String
}) {}

export const AGENT_NAME_MAX = 100
export const AgentName = Schema.Trimmed.check(Schema.isNonEmpty(), Schema.isMaxLength(AGENT_NAME_MAX))

/** A token handed to one agent through a setup link. */
export class Agent extends Schema.Class<Agent, { readonly brand: unique symbol }>("Agent")({
  id: Schema.String,
  /** What the agent called itself, e.g. "Claude Code on my laptop". */
  name: Schema.String,
  createdAt: Schema.DateTimeUtcFromString,
  lastUsedAt: Schema.optionalKey(Schema.DateTimeUtcFromString)
}) {}

/** Shown once: only a hash of the token is kept. */
export class CreatedAgent extends Schema.Class<CreatedAgent, { readonly brand: unique symbol }>("CreatedAgent")({
  agent: Agent,
  token: Schema.String
}) {}

export class SetupLink extends Schema.Class<SetupLink, { readonly brand: unique symbol }>("SetupLink")({
  id: Schema.String,
  url: Schema.String,
  /** Paste this into an agent: it sets itself up from the link. */
  prompt: Schema.String,
  expiresAt: Schema.DateTimeUtcFromString
}) {}

export class CurrentUser extends Context.Service<CurrentUser, { readonly id: number; readonly username: string }>()(
  "sloppa/api/CurrentUser"
) {}

export const SESSION_COOKIE = "sloppa_session"
export const sessionCookie = HttpApiSecurity.apiKey({ in: "cookie", key: SESSION_COOKIE })

/**
 * A signed-in browser's cookie, or an agent's bearer token. Everything needs one except fetching a
 * single artifact by id: the random UUID is what makes a shared link unguessable, so listing (which
 * would hand out every id) stays behind sign-in.
 *
 * Reading the cookie takes the request, hence the requirements it leaks.
 * @effect-expect-leaking HttpServerRequest | ParsedSearchParams | RouteContext
 */
export class Authorization extends HttpApiMiddleware.Service<
  Authorization,
  {
    provides: CurrentUser
    requires: never
  }
>()("sloppa/api/Authorization", {
  security: { session: sessionCookie, bearer: HttpApiSecurity.bearer },
  error: Unauthorized
}) {}

/**
 * A signed-in browser's cookie only. Managing agents takes a person: a leaked agent token mustn't be
 * able to mint itself replacements before it's revoked.
 *
 * @effect-expect-leaking HttpServerRequest | ParsedSearchParams | RouteContext
 */
export class SessionAuthorization extends HttpApiMiddleware.Service<
  SessionAuthorization,
  {
    provides: CurrentUser
    requires: never
  }
>()("sloppa/api/SessionAuthorization", {
  security: { session: sessionCookie },
  error: Unauthorized
}) {}

export const MAX_UPLOAD_BYTES = 100 * 1024 * 1024

export const PAGE_SIZE = 60
export const GROUP_PAGE_SIZE = 24
export const GROUP_PREVIEW_SIZE = 4

export class ArtifactsGroup extends HttpApiGroup.make("artifacts")
  .add(
    HttpApiEndpoint.get("list", "/api/artifacts", {
      query: {
        collection: Schema.optional(Schema.String),
        /** Matches name, id, collection or content type, case-insensitively. */
        q: Schema.optional(Schema.String),
        cursor: Schema.optional(Schema.String),
        limit: Schema.optional(Schema.Int.check(Schema.isBetween({ minimum: 1, maximum: 200 })))
      },
      success: ArtifactPage
    }).middleware(Authorization),
    HttpApiEndpoint.get("mosaic", "/api/collections/mosaic", {
      query: {
        cursor: Schema.optional(Schema.String),
        limit: Schema.optional(Schema.Int.check(Schema.isBetween({ minimum: 1, maximum: 100 })))
      },
      success: GroupPage
    }).middleware(Authorization),
    HttpApiEndpoint.get("collections", "/api/collections", {
      success: Schema.Array(CollectionSummary)
    }).middleware(Authorization),
    HttpApiEndpoint.get("stats", "/api/stats", {
      success: Stats
    }).middleware(Authorization),
    HttpApiEndpoint.get("get", "/api/artifacts/:id", {
      params: { id: ArtifactId },
      success: Artifact,
      error: [ArtifactNotFound, StorageError]
    }),
    HttpApiEndpoint.post("upload", "/api/artifacts", {
      payload: Schema.Struct({
        files: Multipart.FilesSchema,
        /** Filed under this collection, e.g. `owner/repo #42`. */
        collection: CollectionName
      }).pipe(
        HttpApiSchema.asMultipart({
          maxFileSize: MAX_UPLOAD_BYTES,
          // By default JSON parts are parsed as fields; every part with a filename is a file here.
          fieldMimeTypes: []
        })
      ),
      success: Schema.Array(Artifact),
      error: StorageError
    }).middleware(Authorization),
    HttpApiEndpoint.patch("move", "/api/artifacts/:id", {
      params: { id: ArtifactId },
      payload: Schema.Struct({ collection: CollectionName }),
      success: Artifact,
      error: [ArtifactNotFound, StorageError]
    }).middleware(Authorization),
    HttpApiEndpoint.delete("delete", "/api/artifacts/:id", {
      params: { id: ArtifactId },
      success: HttpApiSchema.NoContent,
      error: [ArtifactNotFound, StorageError]
    }).middleware(Authorization)
  )
  .annotateMerge(OpenApi.annotations({ title: "Artifacts" })) {}

export class SessionGroup extends HttpApiGroup.make("session")
  .add(
    HttpApiEndpoint.post("login", "/api/session", {
      // Bounded, so a login can't make the throttle hold on to a huge username.
      payload: Schema.Struct({
        username: Schema.String.check(Schema.isMaxLength(200)),
        password: Schema.String.check(Schema.isMaxLength(1024))
      }),
      success: User,
      error: [InvalidCredentials, TooManyAttempts]
    }),
    HttpApiEndpoint.get("current", "/api/session", { success: User }).middleware(Authorization),
    HttpApiEndpoint.delete("logout", "/api/session", { success: HttpApiSchema.NoContent }).middleware(Authorization)
  )
  .annotateMerge(OpenApi.annotations({ title: "Session" })) {}

export class AgentsGroup extends HttpApiGroup.make("agents")
  .add(
    HttpApiEndpoint.get("list", "/api/agents", { success: Schema.Array(Agent) }).middleware(SessionAuthorization),
    HttpApiEndpoint.post("create", "/api/agents", {
      payload: Schema.Struct({ name: AgentName }),
      success: CreatedAgent
    }).middleware(SessionAuthorization),
    HttpApiEndpoint.delete("revoke", "/api/agents/:id", {
      params: { id: Schema.String },
      success: HttpApiSchema.NoContent
    }).middleware(SessionAuthorization),
    HttpApiEndpoint.patch("rename", "/api/agents/:id", {
      params: { id: Schema.String },
      payload: Schema.Struct({ name: AgentName }),
      success: HttpApiSchema.NoContent
    }).middleware(SessionAuthorization),
    HttpApiEndpoint.post("createSetup", "/api/setup-links", { success: SetupLink }).middleware(SessionAuthorization),
    HttpApiEndpoint.delete("revokeSetup", "/api/setup-links/:id", {
      params: { id: Schema.String },
      success: HttpApiSchema.NoContent
    }).middleware(SessionAuthorization)
  )
  .annotateMerge(OpenApi.annotations({ title: "Agents" })) {}

/** `owner/repo #42`, the label for a GitHub PR, from its URL; anything else is kept as it is. */
export const collectionOfPr = (pr: string): string => {
  const match = /^https?:\/\/github\.com\/([\w.-]+\/[\w.-]+)\/pull\/(\d+)/.exec(pr.trim())
  return match ? `${match[1]} #${match[2]}` : pr.trim()
}

/** Where a collection named after a GitHub PR (`owner/repo #42`, or its URL) points, if it is one. */
export const pullRequestUrl = (collection: string): string | undefined => {
  const match = /^([\w.-]+\/[\w.-]+) ?#(\d+)$/.exec(collection)
  if (match) return `https://github.com/${match[1]}/pull/${match[2]}`
  return /^https?:\/\//.test(collection) ? collection : undefined
}

export class Api extends HttpApi.make("sloppa-artifacts")
  .add(ArtifactsGroup)
  .add(SessionGroup)
  .add(AgentsGroup)
  .annotateMerge(OpenApi.annotations({ title: "Sloppa Artifacts" })) {}

/** Where agents redeem a setup link, outside the typed API since they read it as plain text. */
export const setupPath = (code: string) => `/setup/${code}`

/** Path of the raw file route, served outside the typed API so headers can follow the file. */
export const rawPath = (id: string, name: string) => `/f/${id}/${encodeURIComponent(name)}`
export const thumbPath = (id: string) => `/t/${id}`
export const viewerPath = (id: string) => `/a/${id}`
