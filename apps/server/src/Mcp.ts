import {
  Artifact,
  ArtifactId,
  ArtifactNotFound,
  ArtifactPage,
  markdownFor,
  normalizePr,
  PrSummary,
  rawPath,
  RepoName,
  StorageError,
  viewerPath
} from "@sloppa/api"
import { DateTime, Effect, Layer, Schema } from "effect"
import { McpProtocol, McpServer, Tool, Toolkit } from "effect/ai"
import { Artifacts } from "./Artifacts.ts"
import { AppConfig } from "./Config.ts"
import { resolveContentType } from "./ContentType.ts"
import { uploadOf } from "./Storage.ts"
import { UploadTickets } from "./Tickets.ts"

const Pr = Schema.String.annotate({
  description: "The pull request: its URL, or owner/repo#42 for GitHub. The repo is inferred from it."
})
const Repo = RepoName.annotate({ description: "owner/repo, when there is no PR to infer it from." })

const ArtifactResult = Schema.Struct({
  ...Artifact.fields,
  markdown: Schema.String.annotate({ description: "Paste this into the PR: images embed, other files link." })
})

// A plain copy is the point: the tool result is a struct, not an Artifact instance.
// oxlint-disable-next-line typescript/no-misused-spread
const withMarkdown = (artifact: Artifact) => ({ ...artifact, markdown: markdownFor(artifact) })

const Failure = Schema.Union([ArtifactNotFound, StorageError])

// MCP clients assume tools are destructive and open-world unless told otherwise, hence the hints below.

const RequestUpload = Tool.make("request_upload", {
  description:
    "Get a one-time URL to upload a local file (screenshot, video, log, report...) as a PR artifact. " +
    "Then run the returned `command` from the directory holding the file. The URL expires in 30 minutes. " +
    "The links and markdown work as soon as the upload succeeds.",
  parameters: Schema.Struct({
    name: Schema.String.annotate({ description: "File name, with extension, e.g. checkout.png." }),
    pr: Schema.optionalKey(Pr),
    repo: Schema.optionalKey(Repo),
    contentType: Schema.optionalKey(
      Schema.String.annotate({ description: "MIME type; guessed from the extension when omitted." })
    )
  }),
  success: Schema.Struct({
    id: ArtifactId,
    command: Schema.String,
    uploadUrl: Schema.String,
    expiresAt: Schema.String,
    url: Schema.String,
    rawUrl: Schema.String,
    markdown: Schema.String
  })
})
  .annotate(Tool.Title, "Request an upload URL")
  .annotate(Tool.Destructive, false)
  .annotate(Tool.OpenWorld, false)

const UploadText = Tool.make("upload_text", {
  description:
    "Store text you already have (a log excerpt, markdown report, JSON...) as an artifact, " +
    "without writing it to a file first. Prefer request_upload for anything large or binary.",
  parameters: Schema.Struct({
    name: Schema.String.annotate({ description: "File name, with extension, e.g. report.md." }),
    content: Schema.String,
    pr: Schema.optionalKey(Pr),
    repo: Schema.optionalKey(Repo),
    contentType: Schema.optionalKey(Schema.String)
  }),
  success: ArtifactResult,
  failure: Failure
})
  .annotate(Tool.Title, "Upload text")
  .annotate(Tool.Destructive, false)
  .annotate(Tool.OpenWorld, false)

const ListArtifacts = Tool.make("list_artifacts", {
  description: "List artifacts, newest first, optionally for one repo or PR, or matching a search.",
  parameters: Schema.Struct({
    pr: Schema.optionalKey(Pr),
    repo: Schema.optionalKey(Repo),
    search: Schema.optionalKey(
      Schema.String.annotate({ description: "Part of the name, id, PR, repo or content type; case-insensitive." })
    ),
    cursor: Schema.optionalKey(Schema.String.annotate({ description: "nextCursor from the previous page." })),
    limit: Schema.optionalKey(Schema.Int.check(Schema.isBetween({ minimum: 1, maximum: 100 })))
  }),
  success: ArtifactPage,
  failure: Failure
})
  .annotate(Tool.Title, "List artifacts")
  .annotate(Tool.Readonly, true)
  .annotate(Tool.Destructive, false)
  .annotate(Tool.OpenWorld, false)

const ListPullRequests = Tool.make("list_pull_requests", {
  description: "Pull requests that have artifacts, with counts, most recently active first.",
  parameters: Schema.Struct({ repo: Schema.optionalKey(Repo) }),
  success: Schema.Struct({ items: Schema.Array(PrSummary) }),
  failure: Failure
})
  .annotate(Tool.Title, "List pull requests")
  .annotate(Tool.Readonly, true)
  .annotate(Tool.Destructive, false)
  .annotate(Tool.OpenWorld, false)

const GetArtifact = Tool.make("get_artifact", {
  description: "Look up one artifact by id.",
  parameters: Schema.Struct({ id: ArtifactId }),
  success: ArtifactResult,
  failure: Failure
})
  .annotate(Tool.Title, "Get artifact")
  .annotate(Tool.Readonly, true)
  .annotate(Tool.Destructive, false)
  .annotate(Tool.OpenWorld, false)

const DeleteArtifact = Tool.make("delete_artifact", {
  description: "Delete an artifact. Its links stop working, including in PRs that embed it.",
  parameters: Schema.Struct({ id: ArtifactId }),
  success: Schema.Struct({ deleted: ArtifactId }),
  failure: Failure
})
  .annotate(Tool.Title, "Delete artifact")
  .annotate(Tool.Idempotent, true)
  .annotate(Tool.OpenWorld, false)

const ArtifactTools = Toolkit.make(
  RequestUpload,
  UploadText,
  ListArtifacts,
  ListPullRequests,
  GetArtifact,
  DeleteArtifact
)

const ArtifactToolHandlers = ArtifactTools.toLayer(
  Effect.gen(function* () {
    const artifacts = yield* Artifacts
    const tickets = yield* UploadTickets
    const { publicUrl } = yield* AppConfig

    return {
      request_upload: Effect.fn(function* ({ name, pr, repo, contentType }) {
        const { token, ticket } = yield* tickets.issue({
          name,
          ...(contentType === undefined ? {} : { contentType }),
          ...(repo === undefined ? {} : { repo }),
          ...(pr === undefined ? {} : { pr: normalizePr(pr) })
        })
        const uploadUrl = `${publicUrl}/api/uploads/${token}`
        const rawUrl = `${publicUrl}${rawPath(ticket.id, name)}`
        return {
          id: ticket.id,
          command: `curl -fsS --upload-file '${name.replaceAll("'", `'\\''`)}' '${uploadUrl}'`,
          uploadUrl,
          expiresAt: DateTime.formatIso(DateTime.makeUnsafe(ticket.exp)),
          url: `${publicUrl}${viewerPath(ticket.id)}`,
          rawUrl,
          markdown: markdownFor({ name, rawUrl, contentType: resolveContentType(name, contentType) })
        }
      }),
      upload_text: ({ name, content, pr, repo, contentType }) =>
        artifacts
          .upload({
            name,
            contentType: contentType ?? undefined,
            body: uploadOf(new TextEncoder().encode(content)),
            repo,
            pr
          })
          .pipe(Effect.map(withMarkdown)),
      list_artifacts: ({ pr, repo, search, cursor, limit }) =>
        artifacts.list({ pr, repo, q: search, cursor, limit: limit ?? 30 }),
      list_pull_requests: ({ repo }) => artifacts.prs(repo).pipe(Effect.map((items) => ({ items }))),
      get_artifact: ({ id }) => artifacts.get(id).pipe(Effect.map(withMarkdown)),
      delete_artifact: ({ id }) => artifacts.remove(id).pipe(Effect.as({ deleted: id }))
    }
  })
)

const INSTRUCTIONS = `Stores PR artifacts (screenshots, recordings, logs, reports) and gives back stable links.
To attach a local file: call request_upload, run its command, then put its markdown in the PR description or a comment.
For text you already have in hand, upload_text does it in one call.
Links are public to anyone holding them; listing needs this agent's token.`

/** Streamable HTTP MCP at `/mcp`; agent token auth is installed by the router (see `Http.ts`). */
export const McpRoutes = McpServer.toolkit(ArtifactTools).pipe(
  Layer.provide(ArtifactToolHandlers),
  Layer.provide(
    McpServer.layerHttp({
      name: "sloppa-artifacts",
      version: "0.1.0",
      instructions: INSTRUCTIONS,
      path: "/mcp",
      protocols: [McpProtocol.v2025_11_25, McpProtocol.v2025_06_18, McpProtocol.v2025_03_26]
    })
  )
)
