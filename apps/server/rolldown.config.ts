import { defineConfig } from "rolldown"

// The server in one file, as the image runs it: only what's imported, of the S3 client especially.
export default defineConfig({
  input: "src/main.ts",
  platform: "node",
  // sharp loads its libvips binary at runtime, so the image installs it beside the bundle. ws's
  // optional native helpers fall back to plain JavaScript without them.
  external: ["sharp", "bufferutil", "utf-8-validate"],
  output: { file: "dist/main.mjs", format: "esm", sourcemap: true, minify: "dce-only", codeSplitting: false }
})
