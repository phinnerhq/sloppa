import { Config, Context, Effect, Layer, Redacted } from "effect"

export interface S3Config {
  readonly endpoint: string
  readonly region: string
  readonly bucket: string
  readonly accessKeyId: Redacted.Redacted<string>
  readonly secretAccessKey: Redacted.Redacted<string>
}

const s3Config = Effect.gen(function* () {
  const endpoint = yield* Config.String("S3_ENDPOINT").pipe(Config.withDefault(undefined))
  if (endpoint === undefined || endpoint === "") return undefined
  return {
    endpoint,
    region: yield* Config.String("S3_REGION").pipe(Config.withDefault("us-east-1")),
    bucket: yield* Config.String("S3_BUCKET").pipe(Config.withDefault("artifacts")),
    accessKeyId: yield* Config.Redacted("S3_ACCESS_KEY_ID"),
    secretAccessKey: yield* Config.Redacted("S3_SECRET_ACCESS_KEY")
  } satisfies S3Config
})

/** The first user, created (or given this password again) on boot. */
export interface BootstrapUser {
  readonly username: string
  readonly password: Redacted.Redacted<string>
}

const bootstrapUser = Effect.gen(function* () {
  const username = yield* Config.String("BOOTSTRAP_USERNAME").pipe(Config.withDefault(""))
  const password = yield* Config.Redacted("BOOTSTRAP_PASSWORD").pipe(Config.withDefault(undefined))
  if (username.trim() === "" || password === undefined || Redacted.value(password) === "") return undefined
  return { username: username.trim(), password } satisfies BootstrapUser
})

/** A wildcard bind address (as in a container) isn't something a browser can open. */
const localHost = (host: string) => (host === "0.0.0.0" || host === "::" ? "localhost" : host)

export class AppConfig extends Context.Service<
  AppConfig,
  {
    readonly host: string
    readonly port: number
    readonly publicUrl: string
    /** Unset once users exist: accounts live in `dataDir`. */
    readonly bootstrapUser: BootstrapUser | undefined
    /**
     * Behind a reverse proxy, the client is the last `X-Forwarded-For` entry (the one the proxy added)
     * rather than the socket's address, which is the proxy's for everyone.
     */
    readonly trustProxy: boolean
    readonly staticDir: string | undefined
    /** Holds the accounts, the SQLite index and, without S3, the files themselves. */
    readonly dataDir: string
    /** Set when `S3_ENDPOINT` is: artifacts then live in the bucket instead of `dataDir`. */
    readonly s3: S3Config | undefined
  }
>()("sloppa/server/AppConfig") {
  static readonly layer = Layer.effect(
    AppConfig,
    Effect.gen(function* () {
      const host = yield* Config.String("HOST").pipe(Config.withDefault("127.0.0.1"))
      const port = yield* Config.Int("PORT").pipe(Config.withDefault(4100))
      return AppConfig.of({
        host,
        port,
        publicUrl: (yield* Config.String("PUBLIC_URL").pipe(
          Config.withDefault(`http://${localHost(host)}:${port}`)
        )).replace(/\/+$/, ""),
        bootstrapUser: yield* bootstrapUser,
        trustProxy: yield* Config.Boolean("TRUST_PROXY").pipe(Config.withDefault(false)),
        staticDir: yield* Config.String("STATIC_DIR").pipe(Config.withDefault(undefined)),
        dataDir: yield* Config.String("DATA_DIR").pipe(Config.withDefault(".data")),
        s3: yield* s3Config
      })
    })
  )
}
