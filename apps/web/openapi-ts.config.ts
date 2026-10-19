// The API client in src/client, generated from the server's OpenAPI document: `pnpm gen` at the root
// rewrites the document from the Go API, then this.
import { defineConfig } from "@hey-api/openapi-ts"

export default defineConfig({
  input: "../../openapi.json",
  output: {
    path: "src/client",
    // The bundled fetch runtime doesn't pass exactOptionalPropertyTypes; its types still reach callers.
    header: (ctx) => ["// @ts-nocheck", ...ctx.defaultValue]
  },
  parser: {
    // A WebSocket upgrade for `sloppa preview run`, nothing a browser queries.
    filters: { operations: { exclude: ["GET /api/previews/{id}/tunnel"] } }
  },
  plugins: [
    { name: "@hey-api/client-fetch", runtimeConfigPath: "./src/hey-api.ts" },
    // Dates arrive as Dates. int64s stay numbers: sizes and counts are nowhere near 2^53.
    { name: "@hey-api/transformers", dates: true, bigInt: false },
    "valibot",
    // Responses only: valibot reads `format: binary` as a string, and would turn uploaded Files away.
    { name: "@hey-api/sdk", validator: { response: "valibot" }, transformer: true },
    // Query keys carry their operation's tag, like Artifacts: api.ts invalidates by them.
    { name: "@tanstack/react-query", queryKeys: { tags: true }, infiniteQueryKeys: { tags: true } }
  ]
})
