import { Api } from "@sloppa/api"
import { Context, Effect, FileSystem, Layer, Option, Path, Redacted, Schema } from "effect"
import { FetchHttpClient, HttpClient, HttpClientRequest } from "effect/http"
import { HttpApiClient } from "effect/http-api"
import { CliError } from "effect/cli"
import { homedir } from "node:os"

/**
 * Where `sloppa login` keeps the server and token, readable by this user alone:
 * `$XDG_CONFIG_HOME` or `~/.config`, and `%APPDATA%` on Windows.
 */
export const credentialsPath = Effect.gen(function* () {
  const path = yield* Path.Path
  const base =
    process.env["XDG_CONFIG_HOME"] ||
    (process.platform === "win32" ? process.env["APPDATA"] : undefined) ||
    path.join(homedir(), ".config")
  return path.join(base, "sloppa", "credentials.json")
})

const Credentials = Schema.Struct({ url: Schema.String, token: Schema.String })
type Credentials = typeof Credentials.Type

const decodeCredentials = Schema.decodeUnknownOption(Schema.fromJsonString(Credentials))

const notLoggedIn = new CliError.UserError({
  cause: new Error(
    "Not signed in to sloppa: set SLOPPA_URL and SLOPPA_TOKEN, or pipe an agent token into `sloppa login <url>`"
  )
})

/** SLOPPA_URL and SLOPPA_TOKEN win over the file, so one machine can serve several agents. */
const loadCredentials = Effect.gen(function* () {
  const url = process.env["SLOPPA_URL"]
  const token = process.env["SLOPPA_TOKEN"]
  if (url && token) return { url, token }
  const fs = yield* FileSystem.FileSystem
  const stored = yield* fs.readFileString(yield* credentialsPath).pipe(Effect.option)
  const credentials = Option.flatMap(stored, decodeCredentials)
  if (Option.isNone(credentials)) return yield* notLoggedIn
  return { url: url || credentials.value.url, token: token || credentials.value.token }
})

export const clientFor = (credentials: Credentials) =>
  HttpApiClient.make(Api, {
    baseUrl: credentials.url.replace(/\/+$/, ""),
    transformClient: HttpClient.mapRequest(HttpClientRequest.bearerToken(Redacted.make(credentials.token)))
  })

/** The sloppa server's typed API, signed in with the agent's token. */
export class Sloppa extends Context.Service<
  Sloppa,
  {
    readonly url: string
    readonly api: HttpApiClient.ForApi<typeof Api>
    readonly http: HttpClient.HttpClient
    readonly token: Redacted.Redacted<string>
  }
>()("sloppa/cli/Sloppa") {
  static readonly layer = Layer.effect(
    Sloppa,
    Effect.gen(function* () {
      const credentials = yield* loadCredentials
      const http = yield* HttpClient.HttpClient
      return Sloppa.of({
        url: credentials.url.replace(/\/+$/, ""),
        api: yield* clientFor(credentials),
        http,
        token: Redacted.make(credentials.token)
      })
    })
  ).pipe(Layer.provide(FetchHttpClient.layer))
}
