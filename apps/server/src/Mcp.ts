import {
  Artifact,
  ArtifactId,
  ArtifactNotFound,
  ArtifactPage,
  CollectionName,
  CollectionSummary,
  Preview,
  PreviewNotFound,
  PreviewsUnavailable,
  rawPath,
  StorageError,
  viewerPath
} from "@sloppa/api"
import { DateTime, Effect, Layer, Option, Schema } from "effect"
import { McpProtocol, McpServer, Tool, Toolkit } from "effect/ai"
import { HttpRouter, HttpServerRequest, HttpServerResponse } from "effect/http"
import { Accounts } from "./Accounts.ts"
import { Artifacts } from "./Artifacts.ts"
import { AppConfig } from "./Config.ts"
import { uploadOf } from "./StorageShared.ts"
import { Previews } from "./Previews.ts"
import { UploadTickets } from "./Tickets.ts"

const Collection = CollectionName.annotate({
  description:
    "Groups related files under one label, free-form. For a PR, use `owner/repo #42`; " +
    "reuse the exact same label to add to a collection."
})

const Failure = Schema.Union([ArtifactNotFound, StorageError])

// MCP clients assume tools are destructive and open-world unless told otherwise, hence the hints below.

const RequestUpload = Tool.make("request_upload", {
  description:
    "Get a one-time URL to upload a local file (screenshot, video, log, report...) as an artifact. " +
    "Then run the returned `command` from the directory holding the file. The URL expires in 30 minutes. " +
    "The links work as soon as the upload succeeds.",
  parameters: Schema.Struct({
    name: Schema.String.annotate({ description: "File name, with extension, e.g. checkout.png." }),
    collection: Collection,
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
    rawUrl: Schema.String
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
    collection: Collection,
    contentType: Schema.optionalKey(Schema.String)
  }),
  success: Artifact,
  failure: Failure
})
  .annotate(Tool.Title, "Upload text")
  .annotate(Tool.Destructive, false)
  .annotate(Tool.OpenWorld, false)

const ListArtifacts = Tool.make("list_artifacts", {
  description: "List artifacts, newest first, optionally in one collection, or matching a search.",
  parameters: Schema.Struct({
    collection: Schema.optionalKey(Collection),
    search: Schema.optionalKey(
      Schema.String.annotate({ description: "Part of the name, id, collection or content type; case-insensitive." })
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

const ListCollections = Tool.make("list_collections", {
  description: "Collections that have artifacts, with counts, most recently active first.",
  parameters: Tool.EmptyParams,
  success: Schema.Struct({ items: Schema.Array(CollectionSummary) }),
  failure: Failure
})
  .annotate(Tool.Title, "List collections")
  .annotate(Tool.Readonly, true)
  .annotate(Tool.Destructive, false)
  .annotate(Tool.OpenWorld, false)

const GetArtifact = Tool.make("get_artifact", {
  description: "Look up one artifact by id.",
  parameters: Schema.Struct({ id: ArtifactId }),
  success: Artifact,
  failure: Failure
})
  .annotate(Tool.Title, "Get artifact")
  .annotate(Tool.Readonly, true)
  .annotate(Tool.Destructive, false)
  .annotate(Tool.OpenWorld, false)

const MoveArtifact = Tool.make("move_artifact", {
  description: "File an artifact under another collection, e.g. to fix a mistyped label. Its links don't change.",
  parameters: Schema.Struct({ id: ArtifactId, collection: Collection }),
  success: Artifact,
  failure: Failure
})
  .annotate(Tool.Title, "Move artifact")
  .annotate(Tool.Idempotent, true)
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

const PreviewFailure = Schema.Union([PreviewNotFound, PreviewsUnavailable])

const PreviewList = Tool.make("preview_list", {
  description:
    "Dev servers this account is sharing with `sloppa preview run`, newest first: URL, command, " +
    "working directory and machine. A preview lives exactly as long as its command runs.",
  parameters: Tool.EmptyParams,
  success: Schema.Struct({ items: Schema.Array(Preview) }),
  failure: PreviewFailure
})
  .annotate(Tool.Title, "List previews")
  .annotate(Tool.Readonly, true)
  .annotate(Tool.Destructive, false)
  .annotate(Tool.OpenWorld, false)

const PreviewStop = Tool.make("preview_stop", {
  description:
    "Stop a preview by id: its URL stops working, and its `sloppa preview run` kills the dev server and exits.",
  parameters: Schema.Struct({ id: Schema.String }),
  success: Schema.Struct({ stopped: Schema.String }),
  failure: PreviewFailure
})
  .annotate(Tool.Title, "Stop preview")
  .annotate(Tool.Idempotent, true)
  .annotate(Tool.OpenWorld, false)

const ArtifactTools = Toolkit.make(
  RequestUpload,
  UploadText,
  ListArtifacts,
  ListCollections,
  GetArtifact,
  MoveArtifact,
  DeleteArtifact,
  PreviewList,
  PreviewStop
)

const ArtifactToolHandlers = ArtifactTools.toLayer(
  Effect.gen(function* () {
    const artifacts = yield* Artifacts
    const tickets = yield* UploadTickets
    const { publicUrl } = yield* AppConfig
    const previews = yield* Previews
    const accounts = yield* Accounts
    /** Previews belong to an account, so these tools ask who the agent token is. */
    const caller = Effect.gen(function* () {
      const request = yield* Effect.serviceOption(HttpServerRequest.HttpServerRequest)
      const token = Option.getOrUndefined(Option.flatMapNullishOr(request, bearerOf)) ?? ""
      return Option.getOrThrowWith(
        yield* accounts.authenticate("agent", token),
        () => new Error("MCP tool call without an agent token")
      )
    })

    return {
      request_upload: Effect.fn(function* ({ name, collection, contentType }) {
        const { token, ticket } = yield* tickets.issue({
          name,
          collection,
          ...(contentType === undefined ? {} : { contentType })
        })
        const uploadUrl = `${publicUrl}/api/uploads/${token}`
        return {
          id: ticket.id,
          command: `curl -fsS --upload-file '${name.replaceAll("'", `'\\''`)}' '${uploadUrl}'`,
          uploadUrl,
          expiresAt: DateTime.formatIso(DateTime.makeUnsafe(ticket.exp)),
          url: `${publicUrl}${viewerPath(ticket.id)}`,
          rawUrl: `${publicUrl}${rawPath(ticket.id, name)}`
        }
      }),
      upload_text: ({ name, content, collection, contentType }) =>
        artifacts.upload({
          name,
          contentType: contentType ?? undefined,
          body: uploadOf(new TextEncoder().encode(content)),
          collection
        }),
      list_artifacts: ({ collection, search, cursor, limit }) =>
        artifacts.list({ collection, q: search, cursor, limit: limit ?? 30 }),
      list_collections: () => artifacts.collections.pipe(Effect.map((items) => ({ items }))),
      get_artifact: ({ id }) => artifacts.get(id),
      move_artifact: ({ id, collection }) => artifacts.move(id, collection),
      delete_artifact: ({ id }) => artifacts.remove(id).pipe(Effect.as({ deleted: id })),
      preview_list: () =>
        caller.pipe(
          Effect.flatMap(previews.list),
          Effect.map((items) => ({ items }))
        ),
      preview_stop: ({ id }) =>
        caller.pipe(
          Effect.flatMap((account) => previews.stop(account, id)),
          Effect.as({ stopped: id })
        )
    }
  })
)

const INSTRUCTIONS = `Stores artifacts (screenshots, recordings, logs, reports) in collections and gives back stable links.
To attach a local file: call request_upload, run its command, then link its rawUrl from the PR description or a comment.
For text you already have in hand, upload_text does it in one call.
Every upload goes in a collection: for a PR, name it \`owner/repo #42\`. move_artifact refiles one.
Links are public to anyone holding them; listing needs this agent's token.
To share a running dev server instead, run \`sloppa preview run -- <dev command>\` in the background and give out the URL it prints; preview_list and preview_stop manage those.`

const bearerOf = (request: HttpServerRequest.HttpServerRequest) =>
  /^Bearer +(\S+)$/i.exec(request.headers["authorization"] ?? "")?.[1]

/** The agent token check. It lives outside the typed API's security, so it's attached to the MCP routes themselves. */
const AgentToken = HttpRouter.middleware(
  Effect.gen(function* () {
    const accounts = yield* Accounts
    return (httpApp) =>
      Effect.gen(function* () {
        const request = yield* HttpServerRequest.HttpServerRequest
        const token = bearerOf(request)
        if (token !== undefined && Option.isSome(yield* accounts.authenticate("agent", token))) return yield* httpApp
        return HttpServerResponse.text("Missing or invalid token: set this agent up from the dashboard\n", {
          status: 401,
          headers: { "www-authenticate": "Bearer" }
        })
      })
  })
).layer

/** Streamable HTTP MCP at `/mcp`, behind an agent token. */
export const McpRoutes = McpServer.toolkit(ArtifactTools).pipe(
  Layer.provide(ArtifactToolHandlers),
  Layer.provide(
    McpServer.layerHttp({
      name: "sloppa",
      version: "0.1.0",
      instructions: INSTRUCTIONS,
      path: "/mcp",
      protocols: [McpProtocol.v2025_11_25, McpProtocol.v2025_06_18, McpProtocol.v2025_03_26]
    }).pipe(Layer.provide(AgentToken))
  )
)
