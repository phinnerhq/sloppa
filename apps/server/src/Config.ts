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

/** A wildcard bind address (as in a container) isn't something a browser can open. */
const localHost = (host: string) => (host === "0.0.0.0" || host === "::" ? "localhost" : host)

export class AppConfig extends Context.Service<
  AppConfig,
  {
    readonly host: string
    readonly port: number
    readonly publicUrl: string
    readonly uploadToken: Redacted.Redacted<string>
    readonly staticDir: string | undefined
    /** Holds the SQLite index and, without S3, the files themselves. */
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
        uploadToken: yield* Config.Redacted("UPLOAD_TOKEN"),
        staticDir: yield* Config.String("STATIC_DIR").pipe(Config.withDefault(undefined)),
        dataDir: yield* Config.String("DATA_DIR").pipe(Config.withDefault(".data")),
        s3: yield* s3Config
      })
    })
  )
}
