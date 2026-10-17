import { Artifact, ArtifactId, ArtifactPage, CollectionName, CollectionSummary } from "@sloppa/api"
import { Console, DateTime, Effect, FileSystem, Option, Path, Schema } from "effect"
import { Argument, Command, Flag } from "effect/cli"
import { HttpClientRequest, HttpClientResponse } from "effect/http"
import { buffer } from "node:stream/consumers"
import { apiError, json, printJson, shellQuote, userError } from "./Shared.ts"
import { Sloppa } from "./Sloppa.ts"

/** One artifact per line, tab-separated: id, name, collection, link. */
const artifactLine = (artifact: Artifact) =>
  [artifact.id, artifact.name, artifact.collection, artifact.rawUrl].join("\t")

const artifactFields = (artifact: Artifact) =>
  [
    `id: ${artifact.id}`,
    `name: ${artifact.name}`,
    `collection: ${artifact.collection}`,
    `type: ${artifact.contentType}`,
    `size: ${artifact.size}`,
    `created_at: ${DateTime.formatIso(artifact.createdAt)}`,
    `url: ${artifact.url}`,
    `raw_url: ${artifact.rawUrl}`
  ].join("\n")

const id = Argument.String("id").pipe(Argument.withSchema(ArtifactId), Argument.withDescription("The artifact's id"))

const collection = Flag.String("collection").pipe(
  Flag.withAlias("c"),
  Flag.withSchema(CollectionName),
  Flag.withDescription("e.g. `owner/repo #42` for a PR; the exact same label adds to a collection")
)

/** The bytes to send for one argument: a file, or stdin for `-`. */
const readUpload = Effect.fn(function* (file: string, name: Option.Option<string>) {
  if (file === "-") {
    if (Option.isNone(name))
      return yield* userError("Name what stdin holds: … | sloppa artifact upload -c <collection> --name report.md -")
    if (process.stdin.isTTY)
      return yield* userError(
        `Nothing is piped in for -: … | sloppa artifact upload -c <collection> --name ${name.value} -`
      )
    return {
      name: name.value,
      bytes: new Uint8Array(yield* Effect.promise(() => buffer(process.stdin)))
    }
  }
  const fs = yield* FileSystem.FileSystem
  const path = yield* Path.Path
  const info = yield* fs.stat(file).pipe(Effect.option)
  if (Option.isNone(info) || info.value.type !== "File") return yield* userError(`Not a file: ${file}`)
  return { name: path.basename(file), bytes: yield* fs.readFile(file) }
})

const upload = Command.make(
  "upload",
  {
    files: Argument.String("files").pipe(
      Argument.withDescription("Files to upload; - reads stdin, named by --name"),
      Argument.variadic({ min: 1 })
    ),
    collection,
    name: Flag.String("name").pipe(Flag.withDescription("The file name for stdin, with extension"), Flag.optional),
    json
  },
  Effect.fn(function* ({ files, collection, name, json }) {
    if (files.filter((file) => file === "-").length > 1)
      return yield* userError("Stdin can be read once: pass - at most once")
    // Every file is read before anything is sent, so a typo uploads nothing.
    const uploads = yield* Effect.forEach(files, (file) => readUpload(file, name))
    const sloppa = yield* Sloppa
    const form = new FormData()
    form.set("collection", collection)
    for (const upload of uploads) form.append("files", new Blob([upload.bytes as Uint8Array<ArrayBuffer>]), upload.name)
    const artifacts = yield* sloppa.http
      .execute(
        HttpClientRequest.post(`${sloppa.url}/api/artifacts`).pipe(
          HttpClientRequest.bearerToken(sloppa.token),
          HttpClientRequest.bodyFormData(form)
        )
      )
      .pipe(
        Effect.flatMap(HttpClientResponse.filterStatusOk),
        Effect.flatMap(HttpClientResponse.schemaBodyJson(Schema.Array(Artifact))),
        Effect.mapError(apiError("Upload failed"))
      )
    if (json) return yield* printJson(Schema.Array(Artifact), artifacts)
    // Ready to paste into a PR: images embed, anything else links.
    for (const artifact of artifacts) {
      const image = /\.(png|jpe?g|gif|webp|avif|svg)$/i.test(artifact.name)
      yield* Console.log(`${image ? "!" : ""}[${artifact.name}](${artifact.rawUrl})`)
    }
  })
).pipe(
  Command.withDescription(
    "Upload files as artifacts and print a Markdown link to each, ready for a PR: images embed, anything else links."
  ),
  Command.withExamples([
    { command: 'sloppa artifact upload -c "owner/repo #42" checkout.png run.log', description: "Two files for a PR" },
    {
      command: 'pnpm test 2>&1 | sloppa artifact upload -c "owner/repo #42" --name test-run.log -',
      description: "Output piped in, under a name of your choosing"
    },
    { command: 'sloppa artifact upload -c "owner/repo #42" --json trace.zip', description: "Ids and links as JSON" }
  ])
)

const list = Command.make(
  "list",
  {
    collection: collection.pipe(Flag.withDescription("Only this collection, e.g. `owner/repo #42`"), Flag.optional),
    search: Flag.String("search").pipe(
      Flag.withAlias("q"),
      Flag.withDescription("Part of the name, id, collection or type, any case"),
      Flag.optional
    ),
    limit: Flag.Int("limit").pipe(Flag.withDescription("At most this many, 1 to 200 (default 60)"), Flag.optional),
    cursor: Flag.String("cursor").pipe(
      Flag.withDescription("The next page, as printed after the last one"),
      Flag.optional
    ),
    json
  },
  Effect.fn(function* ({ collection, search, limit, cursor, json }) {
    const sloppa = yield* Sloppa
    const page = yield* sloppa.api.artifacts
      .list({
        query: {
          collection: Option.getOrUndefined(collection),
          q: Option.getOrUndefined(search),
          limit: Option.getOrUndefined(limit),
          cursor: Option.getOrUndefined(cursor)
        }
      })
      .pipe(Effect.mapError(apiError("Couldn't list artifacts")))
    if (json) return yield* printJson(ArtifactPage, page)
    for (const artifact of page.items) yield* Console.log(artifactLine(artifact))
    if (page.nextCursor !== undefined) {
      const same = [
        ...Option.match(collection, { onNone: () => [], onSome: (c) => ["-c", shellQuote(c)] }),
        ...Option.match(search, { onNone: () => [], onSome: (q) => ["-q", shellQuote(q)] }),
        ...Option.match(limit, { onNone: () => [], onSome: (n) => ["--limit", String(n)] })
      ]
      yield* Console.error(`more: sloppa artifact list ${[...same, "--cursor", page.nextCursor].join(" ")}`)
    }
  })
).pipe(
  Command.withDescription(
    "List artifacts, newest first: one per line, tab-separated id, name, collection and link. " +
      "When there are more, the command for the next page goes to stderr."
  ),
  Command.withExamples([
    { command: 'sloppa artifact list -c "owner/repo #42"', description: "One PR's artifacts" },
    { command: "sloppa artifact list -q checkout --limit 10", description: "Search by name, id, collection or type" },
    { command: "sloppa artifact list --json | jq -r '.items[].rawUrl'", description: "Links only" }
  ])
)

const get = Command.make(
  "get",
  { id, json },
  Effect.fn(function* ({ id, json }) {
    const sloppa = yield* Sloppa
    const artifact = yield* sloppa.api.artifacts.get({ params: { id } }).pipe(
      Effect.catchTag("ArtifactNotFound", () => userError(`No artifact ${id}: find ids with sloppa artifact list`)),
      Effect.mapError(apiError("Couldn't look it up"))
    )
    yield* json ? printJson(Artifact, artifact) : Console.log(artifactFields(artifact))
  })
).pipe(
  Command.withDescription("Show one artifact: name, collection, type, size, upload time and links."),
  Command.withExamples([
    { command: "sloppa artifact get 3f2b8c1e-7d4a-4e0b-9c55-2a1f6e8d9b70", description: "" },
    { command: "sloppa artifact get 3f2b8c1e-7d4a-4e0b-9c55-2a1f6e8d9b70 --json", description: "" }
  ])
)

const move = Command.make(
  "move",
  { id, collection, json },
  Effect.fn(function* ({ id, collection, json }) {
    const sloppa = yield* Sloppa
    const artifact = yield* sloppa.api.artifacts.move({ params: { id }, payload: { collection } }).pipe(
      Effect.catchTag("ArtifactNotFound", () => userError(`No artifact ${id}: find ids with sloppa artifact list`)),
      Effect.mapError(apiError("Couldn't move it"))
    )
    yield* json ? printJson(Artifact, artifact) : Console.log(artifactFields(artifact))
  })
).pipe(
  Command.withDescription("File an artifact under another collection. Its links stay the same."),
  Command.withExamples([
    {
      command: 'sloppa artifact move 3f2b8c1e-7d4a-4e0b-9c55-2a1f6e8d9b70 -c "owner/repo #43"',
      description: "Fix a mistyped PR number"
    }
  ])
)

const remove = Command.make(
  "delete",
  {
    ids: Argument.String("ids").pipe(
      Argument.withSchema(ArtifactId),
      Argument.withDescription("The artifacts' ids"),
      Argument.variadic({ min: 1 })
    ),
    yes: Flag.Boolean("yes").pipe(
      Flag.withDescription("Delete them: their links stop working, in PRs too"),
      Flag.withDefault(false)
    ),
    dryRun: Flag.Boolean("dry-run").pipe(
      Flag.withDescription("Only show what would be deleted"),
      Flag.withDefault(false)
    )
  },
  Effect.fn(function* ({ ids, yes, dryRun }) {
    const sloppa = yield* Sloppa
    if (dryRun) {
      for (const id of ids) {
        const artifact = yield* sloppa.api.artifacts.get({ params: { id } }).pipe(
          Effect.asSome,
          Effect.catchTag("ArtifactNotFound", () => Effect.succeedNone),
          Effect.mapError(apiError("Couldn't look it up"))
        )
        yield* Console.log(
          Option.match(artifact, {
            onNone: () => `already gone: ${id}`,
            onSome: (artifact) => `would delete: ${artifactLine(artifact)}`
          })
        )
      }
      return
    }
    if (!yes) {
      return yield* userError(
        `Deleting breaks every link to these artifacts, in PRs too. Check with --dry-run, then confirm:\n  sloppa artifact delete ${ids.join(" ")} --yes`
      )
    }
    for (const id of ids) {
      // Already gone is what was asked for, so a retry succeeds too.
      const gone = yield* sloppa.api.artifacts.delete({ params: { id } }).pipe(
        Effect.as("deleted"),
        Effect.catchTag("ArtifactNotFound", () => Effect.succeed("already gone")),
        Effect.mapError(apiError(`Couldn't delete ${id}`))
      )
      yield* Console.log(`${gone}: ${id}`)
    }
  })
).pipe(
  Command.withDescription("Delete artifacts. Asks for --yes, since their links stop working everywhere."),
  Command.withExamples([
    {
      command: "sloppa artifact delete 3f2b8c1e-7d4a-4e0b-9c55-2a1f6e8d9b70 --dry-run",
      description: "See what would go"
    },
    { command: "sloppa artifact delete 3f2b8c1e-7d4a-4e0b-9c55-2a1f6e8d9b70 --yes", description: "" }
  ])
)

export const artifactCommand = Command.make("artifact").pipe(
  Command.withDescription("Upload, find, refile and delete artifacts"),
  Command.withSubcommands([upload, list, get, move, remove])
)

const listCollections = Command.make(
  "list",
  { json },
  Effect.fn(function* ({ json }) {
    const sloppa = yield* Sloppa
    const summaries = yield* sloppa.api.artifacts
      .collections()
      .pipe(Effect.mapError(apiError("Couldn't list collections")))
    if (json) return yield* printJson(Schema.Array(CollectionSummary), summaries)
    for (const summary of summaries) {
      yield* Console.log([summary.collection, summary.count, DateTime.formatIso(summary.lastUploadAt)].join("\t"))
    }
  })
).pipe(
  Command.withDescription(
    "List collections, most recently active first: one per line, tab-separated name, artifact count and last upload."
  ),
  Command.withExamples([
    { command: "sloppa collection list", description: "" },
    { command: "sloppa collection list --json", description: "" }
  ])
)

export const collectionCommand = Command.make("collection").pipe(
  Command.withDescription("List collections"),
  Command.withSubcommands([listCollections])
)
