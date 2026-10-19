// Fails when gofmt would change a Go file, listing them: `gofmt -l` alone exits 0 either way.
import { execFileSync } from "node:child_process"

const unformatted = execFileSync("gofmt", ["-l", "cmd", "internal"], { encoding: "utf8" }).trim()
if (unformatted !== "") {
  console.error(`gofmt would change these, run pnpm fmt:\n${unformatted}`)
  process.exit(1)
}
