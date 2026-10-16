import { NodeHttpServer, NodeRuntime } from "@effect/platform-node"
import { MAX_UPLOAD_BYTES } from "@sloppa/api"
import { ByteSize, Effect, Layer } from "effect"
import { HttpIncomingMessage, HttpRouter } from "effect/http"
import { createServer } from "node:http"
import { AppConfig } from "./Config.ts"
import { Routes } from "./Http.ts"

const Server = Layer.unwrap(
  Effect.gen(function* () {
    const { host, port, publicUrl } = yield* AppConfig
    yield* Effect.logInfo(`Serving artifacts on http://${host}:${port} (public URL ${publicUrl})`)
    return HttpRouter.serve(Routes).pipe(
      Layer.provide(NodeHttpServer.layer(createServer, { host, port })),
      // Any one request body, with headroom over the largest file for multipart framing.
      Layer.provide(Layer.succeed(HttpIncomingMessage.MaxBodySize, ByteSize.bytes(MAX_UPLOAD_BYTES + 10 * 1024 * 1024)))
    )
  })
)

Layer.launch(Server.pipe(Layer.provide(AppConfig.layer))).pipe(NodeRuntime.runMain)
