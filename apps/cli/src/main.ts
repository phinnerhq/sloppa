#!/usr/bin/env node
import { NodeRuntime, NodeServices } from "@effect/platform-node"
import { CollectionName, PREVIEW_TTL_MAX_SECONDS, PreviewName } from "@sloppa/api"
import { Close } from "@sloppa/preview"
import { Cause, Console, DateTime, Effect, Exit, FileSystem, Layer, Option, Path, Schedule, Schema } from "effect"
import { Argument, CliError, Command, Flag } from "effect/cli"
import { FetchHttpClient, HttpClientRequest, HttpClientResponse } from "effect/http"
import { Sloppa, clientFor, credentialsPath } from "./Sloppa.ts"
import { devServer, expand, freePort, tunnel } from "./Preview.ts"
import { connectLocal } from "./Pipe.ts"
import { text } from "node:stream/consumers"

const VERSION = "0.1.0"

/** What the process exits with once everything is cleaned up: the dev server's code, when it ended things. */
let exitCode = 0

const userError = (message: string) => (cause: unknown) => new CliError.UserError({ cause, userMessage: message })

/** The server's own message for an API error, which says what to do about it. */
const apiError = (what: string) => (error: { readonly message: string }) =>
  new CliError.UserError({ cause: error, userMessage: `${what}: ${error.message}` })

const login = Command.make(
  "login",
  {
    url: Argument.String("url").pipe(Argument.withDescription("The sloppa server, e.g. https://artifacts.example.com"))
  },
  Effect.fn(function* ({ url }) {
    const stdin = (yield* Effect.promise(() => text(process.stdin))).trim()
    if (stdin === "") {
      return yield* new CliError.UserError({
        cause: new Error("no token"),
        userMessage:
          'Pipe an agent token in, so it stays out of your shell history: printf %s "$TOKEN" | sloppa login <url>'
      })
    }
    const credentials = { url: url.replace(/\/+$/, ""), token: stdin }
    const api = yield* clientFor(credentials).pipe(Effect.provide(FetchHttpClient.layer))
    const user = yield* api.session
      .current()
      .pipe(Effect.mapError(apiError(`${credentials.url} didn't take that token`)))
    const fs = yield* FileSystem.FileSystem
    const path = yield* Path.Path
    const file = yield* credentialsPath
    yield* fs.makeDirectory(path.dirname(file), { recursive: true, mode: 0o700 })
    yield* fs.writeFileString(file, JSON.stringify(credentials, null, 2) + "\n", { mode: 0o600 })
    yield* fs.chmod(file, 0o600)
    yield* Console.log(`Signed in to ${credentials.url} as ${user.username}; saved to ${file}`)
  })
).pipe(Command.withDescription("Save a sloppa server and agent token (read from stdin) for the other commands"))

const upload = Command.make(
  "upload",
  {
    files: Argument.Path("files", { pathType: "file", mustExist: true }).pipe(Argument.variadic({ min: 1 })),
    collection: Flag.String("collection").pipe(
      Flag.withAlias("c"),
      Flag.withSchema(CollectionName),
      Flag.withDescription("Where they go, e.g. `owner/repo #42` for a PR")
    ),
    json: Flag.Boolean("json").pipe(Flag.withDescription("Print the artifacts as JSON"), Flag.withDefault(false))
  },
  Effect.fn(function* ({ files, collection, json }) {
    const sloppa = yield* Sloppa
    const fs = yield* FileSystem.FileSystem
    const path = yield* Path.Path
    const form = new FormData()
    form.set("collection", collection)
    for (const file of files)
      form.append("files", new Blob([(yield* fs.readFile(file)) as Uint8Array<ArrayBuffer>]), path.basename(file))
    const artifacts = yield* sloppa.http
      .execute(
        HttpClientRequest.post(`${sloppa.url}/api/artifacts`).pipe(
          HttpClientRequest.bearerToken(sloppa.token),
          HttpClientRequest.bodyFormData(form)
        )
      )
      .pipe(
        Effect.flatMap(HttpClientResponse.filterStatusOk),
        Effect.flatMap(
          HttpClientResponse.schemaBodyJson(
            Schema.Array(
              Schema.Struct({ id: Schema.String, name: Schema.String, url: Schema.String, rawUrl: Schema.String })
            )
          )
        ),
        Effect.mapError(userError("Upload failed"))
      )
    if (json) return yield* Console.log(JSON.stringify(artifacts, null, 2))
    // Ready to paste into a PR: images embed, anything else links.
    for (const artifact of artifacts) {
      const image = /\.(png|jpe?g|gif|webp|avif|svg)$/i.test(artifact.name)
      yield* Console.log(`${image ? "!" : ""}[${artifact.name}](${artifact.rawUrl})`)
    }
  })
).pipe(Command.withDescription("Upload files as artifacts and print Markdown links to them"))

/** `90s`, `30m`, `2h`, `1d`. */
const Ttl = Schema.String.pipe(
  Schema.check(Schema.isPattern(/^\d+[smhd]$/, { message: "Expected a duration like 90s, 30m, 2h or 1d" }))
)
const ttlSeconds = (ttl: string) => Number(ttl.slice(0, -1)) * { s: 1, m: 60, h: 3600, d: 86400 }[ttl.at(-1) as "s"]

/** Says when the dev server first accepts connections, so whoever waits on this output knows when to look. */
const announceListening = (port: number, url: string) =>
  connectLocal(port).pipe(
    Effect.scoped,
    Effect.retry(Schedule.spaced("300 millis")),
    Effect.andThen(Console.error(`preview_ready: ${url}`))
  )

const run = Command.make(
  "run",
  {
    name: Flag.String("name").pipe(
      Flag.withSchema(PreviewName),
      Flag.withDescription("A readable, stable subdomain instead of a random one"),
      Flag.optional
    ),
    password: Flag.String("password").pipe(
      Flag.withDescription("Ask for this password (any user name) before showing the preview"),
      Flag.optional
    ),
    ttl: Flag.String("ttl").pipe(
      Flag.withSchema(Ttl),
      Flag.withDescription("Stop by itself after this long (default 8h, at most 24h)"),
      Flag.optional
    ),
    port: Flag.Int("port").pipe(Flag.withDescription("Use this local port instead of a free one"), Flag.optional),
    command: Argument.String("command").pipe(
      Argument.withDescription("The dev server, after --; {port}, {host}, {url} and {id} are filled in"),
      Argument.variadic({ min: 1 })
    )
  },
  Effect.fn(function* ({ name, password, ttl, port, command }) {
    const sloppa = yield* Sloppa
    const seconds = Option.map(ttl, ttlSeconds)
    if (Option.isSome(seconds) && (seconds.value < 60 || seconds.value > PREVIEW_TTL_MAX_SECONDS)) {
      return yield* new CliError.UserError({ cause: seconds.value, userMessage: "--ttl must be between 1m and 24h" })
    }
    const ticket = yield* sloppa.api.previews
      .start({
        payload: {
          ...Option.match(name, { onNone: () => ({}), onSome: (name) => ({ name }) }),
          ...Option.match(password, { onNone: () => ({}), onSome: (password) => ({ password }) }),
          ...Option.match(seconds, { onNone: () => ({}), onSome: (ttl) => ({ ttl }) })
        }
      })
      .pipe(Effect.mapError(apiError("Couldn't start a preview")))
    const localPort = Option.isSome(port) ? port.value : yield* freePort
    const args = expand(command, { port: localPort, url: ticket.url, id: ticket.id })

    const outcome = yield* Effect.scoped(
      Effect.gen(function* () {
        const scope = yield* Effect.scope
        const child = yield* devServer(args, {
          PORT: String(localPort),
          HOST: "127.0.0.1",
          PREVIEW_URL: ticket.url,
          PREVIEW_ID: ticket.id
        })
        const exited = child.exitCode.pipe(
          Effect.map((code) => ({ _tag: "Exited", code: Number(code) }) as const),
          Effect.orElseSucceed(() => ({ _tag: "Exited", code: 1 }) as const)
        )
        const closed = tunnel(ticket, localPort, args, (url) =>
          Console.error(
            [
              `preview_url: ${url}`,
              `preview_id: ${ticket.id}`,
              `preview_port: ${localPort}`,
              `preview_expires_at: ${DateTime.formatIso(ticket.expiresAt)}`
            ].join("\n")
          ).pipe(Effect.andThen(Effect.forkIn(announceListening(localPort, url), scope)), Effect.asVoid)
        ).pipe(Effect.catch((error) => Effect.succeed({ _tag: "Closed", error } as const)))
        const hangup = Effect.callback<{ readonly _tag: "Hangup" }>((resume) => {
          const onHangup = () => resume(Effect.succeed({ _tag: "Hangup" }))
          process.once("SIGHUP", onHangup)
          return Effect.sync(() => process.off("SIGHUP", onHangup))
        })
        return yield* Effect.raceAll([exited, closed, hangup])
      })
    )
    switch (outcome._tag) {
      case "Exited":
        yield* Console.error(`sloppa: the dev server exited (${outcome.code}); the preview is gone`)
        exitCode = outcome.code
        break
      case "Closed":
        yield* Console.error(`sloppa: preview ${outcome.error.message}; stopped the dev server`)
        // Stopped or out of time is the preview ending as asked; anything else is a failure.
        exitCode = outcome.error.code === Close.Stopped || outcome.error.code === Close.Expired ? 0 : 1
        break
      case "Hangup":
        yield* Console.error("sloppa: hung up; stopped the dev server")
        exitCode = 129
        break
    }
  })
).pipe(
  Command.withDescription(
    "Run a dev server and share it at a public URL, for exactly as long as this command runs. " +
      "The command gets PORT, HOST=127.0.0.1, PREVIEW_URL and PREVIEW_ID; stopping either side stops both."
  ),
  Command.withExamples([
    { command: "sloppa preview run -- pnpm dev", description: "A dev server that reads PORT and HOST" },
    {
      command: "sloppa preview run --name checkout -- vite --host {host} --port {port} --strictPort",
      description: "Vite, which only takes flags, under a stable name"
    },
    { command: "sloppa preview run --password hunter2 -- python3 -m http.server {port} --bind {host}", description: "" }
  ])
)

const preview = Command.make("preview").pipe(
  Command.withDescription("Share local dev servers"),
  Command.withSubcommands([run])
)

const sloppa = Command.make("sloppa").pipe(
  Command.withDescription("Agent tools for a sloppa server: artifacts and previews"),
  Command.withSubcommands([
    login,
    upload.pipe(Command.provide(Sloppa.layer)),
    preview.pipe(Command.provide(Sloppa.layer))
  ])
)

sloppa.pipe(
  Command.run({ version: VERSION }),
  Effect.provide(Layer.mergeAll(NodeServices.layer, FetchHttpClient.layer)),
  NodeRuntime.runMain({
    teardown: (exit, onExit) => onExit(Exit.isSuccess(exit) ? exitCode : Cause.hasInterruptsOnly(exit.cause) ? 130 : 1)
  })
)
