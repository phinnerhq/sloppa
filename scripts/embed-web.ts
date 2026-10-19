// Copies the built dashboard where `go build` embeds it (internal/webui), keeping its .gitkeep.
import { cpSync, readdirSync, rmSync } from "node:fs"
import { join } from "node:path"

const target = "internal/webui/dist"
for (const entry of readdirSync(target)) if (entry !== ".gitkeep") rmSync(join(target, entry), { recursive: true })
cpSync("apps/web/dist", target, { recursive: true })
