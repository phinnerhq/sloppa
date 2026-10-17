#!/usr/bin/env node
import { NodeRuntime, NodeServices } from "@effect/platform-node"
import { Cause, Console, Effect, Exit, FileSystem, Layer, Path } from "effect"
import { Argument, CliError, Command } from "effect/cli"
import { FetchHttpClient } from "effect/http"
import { text } from "node:stream/consumers"
import { artifactCommand, collectionCommand } from "./ArtifactCommands.ts"
import { exitCode, previewCommand } from "./PreviewCommands.ts"
import { apiError } from "./Shared.ts"
import { Sloppa, clientFor, credentialsPath } from "./Sloppa.ts"

const VERSION = "0.1.0"

const PIPE_TOKEN = 'printf %s "$TOKEN" | sloppa login <url>'

const login = Command.make(
  "login",
  {
    url: Argument.String("url").pipe(Argument.withDescription("The sloppa server, e.g. https://sloppa.example.com"))
  },
  Effect.fn(function* ({ url }) {
    // A terminal on stdin would wait for typing: the token comes from a pipe, never the keyboard or argv.
    const stdin = process.stdin.isTTY ? "" : (yield* Effect.promise(() => text(process.stdin))).trim()
    if (stdin === "") {
      return yield* new CliError.UserError({
        cause: new Error("no token"),
        userMessage: `Pipe an agent token in, so it stays out of your shell history:\n  ${PIPE_TOKEN}`
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
).pipe(
  Command.withDescription("Save a server and an agent token, piped in, for the other commands"),
  Command.withExamples([
    { command: PIPE_TOKEN, description: "The token from the dashboard's robot menu" },
    {
      command: "SLOPPA_URL=https://sloppa.example.com SLOPPA_TOKEN=… sloppa artifact list",
      description: "Or both in the environment, which wins over what's saved"
    }
  ])
)

const sloppa = Command.make("sloppa").pipe(
  Command.withDescription(
    "Agent tools for a sloppa server: artifacts (screenshots, recordings, logs) filed in collections like " +
      "`owner/repo #42`, and dev servers shared at public URLs. Run `sloppa <command> --help` for each one's flags."
  ),
  Command.withExamples([
    { command: 'sloppa artifact upload -c "owner/repo #42" checkout.png', description: "Prints a Markdown link" },
    { command: 'sloppa artifact list -c "owner/repo #42"', description: "" },
    { command: "sloppa preview run -- pnpm dev", description: "Shares the dev server while it runs" }
  ]),
  Command.withSubcommands([
    login,
    artifactCommand.pipe(Command.provide(Sloppa.layer)),
    collectionCommand.pipe(Command.provide(Sloppa.layer)),
    previewCommand.pipe(Command.provide(Sloppa.layer))
  ])
)

sloppa.pipe(
  Command.run({ version: VERSION }),
  Effect.provide(Layer.mergeAll(NodeServices.layer, FetchHttpClient.layer)),
  NodeRuntime.runMain({
    teardown: (exit, onExit) => onExit(Exit.isSuccess(exit) ? exitCode : Cause.hasInterruptsOnly(exit.cause) ? 130 : 1)
  })
)
