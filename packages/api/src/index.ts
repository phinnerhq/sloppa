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

/** `owner/name`, as on GitHub. */
export const RepoName = Schema.String.pipe(
  Schema.check(Schema.isPattern(/^[\w.-]+\/[\w.-]+$/, { message: "Expected owner/repo" }))
)

export class Artifact extends Schema.Class<Artifact, { readonly brand: unique symbol }>("Artifact")({
  id: ArtifactId,
  name: Schema.String,
  contentType: Schema.String,
  size: Schema.Finite,
  createdAt: Schema.DateTimeUtcFromString,
  repo: Schema.optionalKey(RepoName),
  pr: Schema.optionalKey(Schema.String),
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

/** A PR's artifacts, or the ones filed under no PR, as one tile of the dashboard's mosaic. */
export class ArtifactGroup extends Schema.Class<ArtifactGroup, { readonly brand: unique symbol }>("ArtifactGroup")({
  /** Absent for the artifacts that have none. */
  pr: Schema.optionalKey(Schema.String),
  repo: Schema.optionalKey(RepoName),
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

export class RepoSummary extends Schema.Class<RepoSummary, { readonly brand: unique symbol }>("RepoSummary")({
  repo: RepoName,
  count: Schema.Int,
  lastUploadAt: Schema.DateTimeUtcFromString
}) {}

export class PrSummary extends Schema.Class<PrSummary, { readonly brand: unique symbol }>("PrSummary")({
  pr: Schema.String,
  repo: Schema.optionalKey(RepoName),
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

export const MAX_UPLOAD_BYTES = 100 * 1024 * 1024

export const PAGE_SIZE = 60
export const GROUP_PAGE_SIZE = 24
export const GROUP_PREVIEW_SIZE = 4

export class ArtifactsGroup extends HttpApiGroup.make("artifacts")
  .add(
    HttpApiEndpoint.get("list", "/api/artifacts", {
      query: {
        repo: Schema.optional(RepoName),
        pr: Schema.optional(Schema.String),
        /** Only the artifacts filed under no PR. */
        noPr: Schema.optional(Schema.Literal("true")),
        /** Matches name, id, PR, repo or content type, case-insensitively. */
        q: Schema.optional(Schema.String),
        cursor: Schema.optional(Schema.String),
        limit: Schema.optional(Schema.Int.check(Schema.isBetween({ minimum: 1, maximum: 200 })))
      },
      success: ArtifactPage
    }).middleware(Authorization),
    HttpApiEndpoint.get("groups", "/api/groups", {
      query: {
        repo: Schema.optional(RepoName),
        cursor: Schema.optional(Schema.String),
        limit: Schema.optional(Schema.Int.check(Schema.isBetween({ minimum: 1, maximum: 100 })))
      },
      success: GroupPage
    }).middleware(Authorization),
    HttpApiEndpoint.get("repos", "/api/repos", {
      success: Schema.Array(RepoSummary)
    }).middleware(Authorization),
    HttpApiEndpoint.get("prs", "/api/prs", {
      query: { repo: Schema.optional(RepoName) },
      success: Schema.Array(PrSummary)
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
        /** Inferred from a GitHub `pr` when omitted. */
        repo: Schema.optional(RepoName),
        /** A pull request URL, or `owner/repo#42` for GitHub. */
        pr: Schema.optional(Schema.String)
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
      payload: Schema.Struct({ username: Schema.String, password: Schema.String }),
      success: User,
      error: [InvalidCredentials, TooManyAttempts]
    }),
    HttpApiEndpoint.get("current", "/api/session", { success: User }).middleware(Authorization),
    HttpApiEndpoint.delete("logout", "/api/session", { success: HttpApiSchema.NoContent }).middleware(Authorization)
  )
  .annotateMerge(OpenApi.annotations({ title: "Session" })) {}

export class AgentsGroup extends HttpApiGroup.make("agents")
  .add(
    HttpApiEndpoint.get("list", "/api/agents", { success: Schema.Array(Agent) }).middleware(Authorization),
    HttpApiEndpoint.post("create", "/api/agents", {
      payload: Schema.Struct({ name: AgentName }),
      success: CreatedAgent
    }).middleware(Authorization),
    HttpApiEndpoint.delete("revoke", "/api/agents/:id", {
      params: { id: Schema.String },
      success: HttpApiSchema.NoContent
    }).middleware(Authorization),
    HttpApiEndpoint.patch("rename", "/api/agents/:id", {
      params: { id: Schema.String },
      payload: Schema.Struct({ name: AgentName }),
      success: HttpApiSchema.NoContent
    }).middleware(Authorization),
    HttpApiEndpoint.post("createSetup", "/api/setup-links", { success: SetupLink }).middleware(Authorization),
    HttpApiEndpoint.delete("revokeSetup", "/api/setup-links/:id", {
      params: { id: Schema.String },
      success: HttpApiSchema.NoContent
    }).middleware(Authorization)
  )
  .annotateMerge(OpenApi.annotations({ title: "Agents" })) {}

/** `owner/repo` of a GitHub pull request URL. */
export const repoOfPr = (pr: string): string | undefined => {
  const match = /^https?:\/\/github\.com\/([\w.-]+\/[\w.-]+)\/pull\/\d+/.exec(pr)
  return match?.[1]
}

/** `owner/repo#42` becomes the PR's GitHub URL; URLs and anything else pass through. */
export const normalizePr = (pr: string): string => {
  const match = /^([\w.-]+\/[\w.-]+)#(\d+)$/.exec(pr.trim())
  return match ? `https://github.com/${match[1]}/pull/${match[2]}` : pr.trim()
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

/** Ready to paste into a PR: images embed, everything else links. */
export const markdownFor = (artifact: {
  readonly name: string
  readonly contentType: string
  readonly rawUrl: string
}) =>
  artifact.contentType.startsWith("image/")
    ? `![${artifact.name}](${artifact.rawUrl})`
    : `[${artifact.name}](${artifact.rawUrl})`
