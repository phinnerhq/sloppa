import { chmodSync, writeFileSync } from "node:fs"
import { defineConfig } from "rolldown"

// One script for whatever Node is installed. `.mjs` is ESM wherever it lands: an extensionless file
// takes its format from the nearest package.json, and one saying "commonjs" breaks it. Symlinked
// onto the PATH it keeps working, since Node goes by the link's target.
export default defineConfig({
  input: "src/main.ts",
  platform: "node",
  // ws's optional native helpers: it falls back to plain JavaScript without them.
  external: ["bufferutil", "utf-8-validate"],
  output: { file: "dist/sloppa.mjs", format: "esm", minify: true, codeSplitting: false },
  plugins: [
    {
      name: "launchers",
      // Here rather than in the package script, so the build doesn't need a POSIX shell.
      writeBundle() {
        chmodSync("dist/sloppa.mjs", 0o755)
        // Windows ignores the shebang: this is what `sloppa` runs there, from the same folder.
        writeFileSync("dist/sloppa.cmd", '@node "%~dp0sloppa.mjs" %*\r\n')
      }
    }
  ]
})
