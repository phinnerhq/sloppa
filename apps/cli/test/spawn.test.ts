// How the dev server's command is spawned: as is on macOS and Linux, and on Windows through
// cmd.exe for the .cmd shims most dev tools are. The last test runs on Windows alone.
import { spawnSync } from "node:child_process"
import { mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { delimiter, join } from "node:path"
import { describe, expect, it } from "vitest"
import { escapeWindowsShellArg, resolveSpawnCommand } from "../src/Spawn.ts"

/** Arguments a dev command can plausibly get, and every character cmd.exe would otherwise act on. */
const awkward = [
  "plain",
  "two words",
  "--base=/some path/",
  'quote"inside',
  "amp & pipe | less < more >",
  "caret ^ and percent %PATH% %",
  "(parens) [brackets] ; , * ?",
  "!bang!",
  "trailing\\",
  'C:\\Program Files\\x\\"',
  ""
]

const windows = {
  platform: "win32" as const,
  env: { PATH: "C:\\tools;C:\\Program Files\\nodejs", PATHEXT: ".COM;.EXE;.BAT;.CMD" }
}

describe("resolveSpawnCommand", () => {
  it("leaves commands alone outside Windows", () => {
    expect(resolveSpawnCommand("pnpm", ["dev"], { platform: "linux" })).toEqual({
      command: "pnpm",
      args: ["dev"],
      shell: false
    })
  })

  it("spawns a Windows .exe directly, found on PATH", () => {
    const exists = (path: string) => path === "C:\\Program Files\\nodejs\\node.exe"
    expect(resolveSpawnCommand("node", ["server.js"], { ...windows, exists })).toEqual({
      command: "C:\\Program Files\\nodejs\\node.exe",
      args: ["server.js"],
      shell: false
    })
  })

  it("runs a Windows .cmd shim through cmd.exe as one escaped line", () => {
    const exists = (path: string) => path === "C:\\Program Files\\nodejs\\pnpm.CMD"
    expect(resolveSpawnCommand("pnpm", ["dev", "--port", "3000"], { ...windows, exists })).toEqual({
      command: '"C:\\Program^ Files\\nodejs\\pnpm.CMD" "dev" "--port" "3000"'.replace(/"/g, '^"'),
      args: [],
      shell: true
    })
  })

  it("escapes cmd.exe's metacharacters so none of them act", () => {
    expect(escapeWindowsShellArg("a & b")).toBe('^"a^ ^&^ b^"')
    expect(escapeWindowsShellArg('say "hi"')).toBe('^"say^ \\^"hi\\^"^"')
    expect(escapeWindowsShellArg("dir\\")).toBe('^"dir\\\\^"')
  })

  // Windows alone has .cmd shims to run.
  it.runIf(process.platform === "win32")("gets awkward arguments through a real .cmd shim intact", () => {
    const dir = mkdtempSync(join(tmpdir(), "sloppa spawn "))
    try {
      writeFileSync(join(dir, "echo-args.js"), "console.log(JSON.stringify(process.argv.slice(2)))\n")
      // Like the shims npm and pnpm install: a batch file passing %* on to node.
      writeFileSync(join(dir, "echo-args.cmd"), '@node "%~dp0echo-args.js" %*\r\n')
      const env = { ...process.env, PATH: `${dir}${delimiter}${process.env["PATH"] ?? ""}` }
      const spawn = resolveSpawnCommand("echo-args", awkward, { env })
      expect(spawn.shell).toBe(true)
      const result = spawnSync(spawn.command, spawn.args, { shell: spawn.shell, env, encoding: "utf8" })
      expect(result.status, result.stderr).toBe(0)
      expect(JSON.parse(result.stdout)).toEqual(awkward)
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})
