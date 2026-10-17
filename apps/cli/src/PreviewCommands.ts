import { PREVIEW_TTL_MAX_SECONDS, Preview, PreviewName } from "@sloppa/api"
import { Close } from "@sloppa/preview"
import { Console, DateTime, Effect, Option, Schedule, Schema } from "effect"
import { Argument, CliError, Command, Flag } from "effect/cli"
import { apiError, json, printJson } from "./Shared.ts"
import { connectLocal } from "./Pipe.ts"
import { devServer, expand, freePort, tunnel } from "./Preview.ts"
import { Sloppa } from "./Sloppa.ts"

/** What the process exits with once everything is cleaned up: the dev server's code, when it ended things. */
export let exitCode = 0

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
      "The command gets PORT, HOST=127.0.0.1, PREVIEW_URL and PREVIEW_ID; stopping either side stops both. " +
      "Prints preview_url: once the tunnel is up, then preview_ready: once the dev server answers, on stderr."
  ),
  Command.withExamples([
    { command: "sloppa preview run -- pnpm dev", description: "A dev server that reads PORT and HOST" },
    {
      command: "sloppa preview run --name checkout -- vite --host {host} --port {port} --strictPort",
      description: "Vite, which only takes flags, under a stable name"
    },
    {
      command: "sloppa preview run --password hunter2 --ttl 2h -- python3 -m http.server {port} --bind {host}",
      description: "Behind a password, for two hours"
    }
  ])
)

const list = Command.make(
  "list",
  { json },
  Effect.fn(function* ({ json }) {
    const sloppa = yield* Sloppa
    const previews = yield* sloppa.api.previews.list().pipe(Effect.mapError(apiError("Couldn't list previews")))
    if (json) return yield* printJson(Schema.Array(Preview), previews)
    for (const preview of previews) {
      yield* Console.log(
        [preview.id, preview.url, DateTime.formatIso(preview.expiresAt), preview.command ?? ""].join("\t")
      )
    }
  })
).pipe(
  Command.withDescription(
    "List the previews this account is sharing, newest first: one per line, tab-separated id, URL, expiry and command."
  ),
  Command.withExamples([
    { command: "sloppa preview list", description: "" },
    { command: "sloppa preview list --json", description: "With hostname, directory, port and traffic" }
  ])
)

const stop = Command.make(
  "stop",
  {
    ids: Argument.String("ids").pipe(Argument.withDescription("The previews' ids"), Argument.variadic({ min: 1 }))
  },
  Effect.fn(function* ({ ids }) {
    const sloppa = yield* Sloppa
    for (const id of ids) {
      // Not running is what was asked for, so a retry succeeds too.
      const outcome = yield* sloppa.api.previews.stop({ params: { id } }).pipe(
        Effect.as("stopped"),
        Effect.catchTag("PreviewNotFound", () => Effect.succeed("not running")),
        Effect.mapError(apiError(`Couldn't stop ${id}`))
      )
      yield* Console.log(`${outcome}: ${id}`)
    }
  })
).pipe(
  Command.withDescription(
    "Stop previews: their URLs stop working, and each `sloppa preview run` kills its dev server and exits."
  ),
  Command.withExamples([{ command: "sloppa preview stop k3j9x2ab", description: "An id from sloppa preview list" }])
)

export const previewCommand = Command.make("preview").pipe(
  Command.withDescription("Share local dev servers, list them, and stop them"),
  Command.withSubcommands([run, list, stop])
)
