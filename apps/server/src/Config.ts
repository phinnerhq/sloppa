import { Config, Context, Duration, Effect, Layer, Redacted } from "effect"

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

/** `sloppa preview run`: tickets, the tunnels, and the previews themselves, on the app's port. */
export interface PreviewConfig {
  /** Like https://preview.example.com: previews are its subdomains, and the apex takes the tunnels. */
  readonly origin: URL
  /** Signs preview tickets, which the tunnel endpoint checks offline. */
  readonly secret: Redacted.Redacted<string>
  /** Failed guesses (unknown hosts, wrong passwords, bad tickets) a client gets, refilled one per `missEvery`. */
  readonly missBurst: number
  readonly missEvery: Duration.Duration
}

/** Throws, as a defect: the server can't start with a broken one. */
const parseOrigin = (raw: string) => {
  const url = URL.parse(raw)
  const invalid = (why: string) => new Error(`PREVIEW_ORIGIN: expected ${why}, got ${raw}`)
  if (url === null || (url.protocol !== "http:" && url.protocol !== "https:")) throw invalid("http:// or https://")
  if (/^[\d.]+$|^\[/.test(url.hostname)) throw invalid("a DNS name, whose subdomains are the previews")
  if (url.pathname.replace(/\//g, "") !== "" || url.search !== "" || url.username !== "") {
    throw invalid("only a scheme, host and port")
  }
  return new URL(url.origin)
}

const previewConfig = (publicUrl: string) =>
  Effect.gen(function* () {
    const origin = yield* Config.String("PREVIEW_ORIGIN").pipe(Config.withDefault(""))
    const secret = yield* Config.Redacted("PREVIEW_SECRET").pipe(Config.withDefault(undefined))
    if (origin === "" || secret === undefined) return undefined
    if (Redacted.value(secret).length < 32) {
      return yield* Effect.die(new Error("PREVIEW_SECRET needs at least 32 characters (openssl rand -base64 32)"))
    }
    const parsed = yield* Effect.sync(() => parseOrigin(origin))
    // The app and the previews share a port, told apart by Host: the app's can't be a preview's.
    const app = new URL(publicUrl).hostname
    if (app === parsed.hostname || app.endsWith(`.${parsed.hostname}`)) {
      return yield* Effect.die(new Error(`PUBLIC_URL: ${app} is the preview domain's, give the app a host of its own`))
    }
    return {
      origin: parsed,
      secret,
      missBurst: yield* Config.Int("PREVIEW_MISS_BURST").pipe(Config.withDefault(20)),
      missEvery: yield* Config.Duration("PREVIEW_MISS_EVERY").pipe(Config.withDefault(Duration.seconds(3)))
    } satisfies PreviewConfig
  })

/** The first user, created on boot, and given this password again whenever it changes. */
interface BootstrapUser {
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
    /** Set when `PREVIEW_ORIGIN` and `PREVIEW_SECRET` are. */
    readonly preview: PreviewConfig | undefined
  }
>()("sloppa/server/AppConfig") {
  static readonly layer = Layer.effect(
    AppConfig,
    Effect.gen(function* () {
      const host = yield* Config.String("HOST").pipe(Config.withDefault("127.0.0.1"))
      const port = yield* Config.Int("PORT").pipe(Config.withDefault(4100))
      const publicUrl = (yield* Config.String("PUBLIC_URL").pipe(
        Config.withDefault(`http://${localHost(host)}:${port}`)
      )).replace(/\/+$/, "")
      return AppConfig.of({
        host,
        port,
        publicUrl,
        bootstrapUser: yield* bootstrapUser,
        trustProxy: yield* Config.Boolean("TRUST_PROXY").pipe(Config.withDefault(false)),
        staticDir: yield* Config.String("STATIC_DIR").pipe(Config.withDefault(undefined)),
        dataDir: yield* Config.String("DATA_DIR").pipe(Config.withDefault(".data")),
        s3: yield* s3Config,
        preview: yield* previewConfig(publicUrl)
      })
    })
  )
}
