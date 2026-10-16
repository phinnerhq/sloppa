import { BunHttpServer, BunRuntime } from "@effect/platform-bun"
import { MAX_UPLOAD_BYTES } from "@sloppa/api"
import { Effect, Layer } from "effect"
import { HttpRouter } from "effect/http"
import { AppConfig } from "./Config.ts"
import { Routes } from "./Http.ts"

const Server = Layer.unwrap(
  Effect.gen(function* () {
    const { host, port, publicUrl } = yield* AppConfig
    yield* Effect.logInfo(`Serving artifacts on http://${host}:${port} (public URL ${publicUrl})`)
    return HttpRouter.serve(Routes).pipe(
      // Headroom over the largest file for multipart framing.
      Layer.provide(
        BunHttpServer.layer({ hostname: host, port, maxRequestBodySize: MAX_UPLOAD_BYTES + 10 * 1024 * 1024 })
      )
    )
  })
)

Layer.launch(Server.pipe(Layer.provide(AppConfig.layer))).pipe(BunRuntime.runMain)
