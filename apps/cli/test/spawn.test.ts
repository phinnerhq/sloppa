// How the dev server's command is spawned: as is on macOS and Linux, and on Windows through
// cmd.exe for the .cmd shims most dev tools are. The last test runs on Windows alone.
import assert from "node:assert/strict"
import { spawnSync } from "node:child_process"
import { mkdtempSync, rmSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { delimiter, join } from "node:path"
import { describe, test } from "node:test"
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
  test("leaves commands alone outside Windows", () => {
    assert.deepEqual(resolveSpawnCommand("pnpm", ["dev"], { platform: "linux" }), {
      command: "pnpm",
      args: ["dev"],
      shell: false
    })
  })

  test("spawns a Windows .exe directly, found on PATH", () => {
    const exists = (path: string) => path === "C:\\Program Files\\nodejs\\node.exe"
    assert.deepEqual(resolveSpawnCommand("node", ["server.js"], { ...windows, exists }), {
      command: "C:\\Program Files\\nodejs\\node.exe",
      args: ["server.js"],
      shell: false
    })
  })

  test("runs a Windows .cmd shim through cmd.exe as one escaped line", () => {
    const exists = (path: string) => path === "C:\\Program Files\\nodejs\\pnpm.CMD"
    assert.deepEqual(resolveSpawnCommand("pnpm", ["dev", "--port", "3000"], { ...windows, exists }), {
      command: '"C:\\Program^ Files\\nodejs\\pnpm.CMD" "dev" "--port" "3000"'.replace(/"/g, '^"'),
      args: [],
      shell: true
    })
  })

  test("escapes cmd.exe's metacharacters so none of them act", () => {
    assert.equal(escapeWindowsShellArg("a & b"), '^"a^ ^&^ b^"')
    assert.equal(escapeWindowsShellArg('say "hi"'), '^"say^ \\^"hi\\^"^"')
    assert.equal(escapeWindowsShellArg("dir\\"), '^"dir\\\\^"')
  })

  test(
    "gets awkward arguments through a real .cmd shim intact",
    { skip: process.platform === "win32" ? false : "Windows only" },
    () => {
      const dir = mkdtempSync(join(tmpdir(), "sloppa spawn "))
      try {
        writeFileSync(join(dir, "echo-args.js"), "console.log(JSON.stringify(process.argv.slice(2)))\n")
        // Like the shims npm and pnpm install: a batch file passing %* on to node.
        writeFileSync(join(dir, "echo-args.cmd"), '@node "%~dp0echo-args.js" %*\r\n')
        const env = { ...process.env, PATH: `${dir}${delimiter}${process.env["PATH"] ?? ""}` }
        const spawn = resolveSpawnCommand("echo-args", awkward, { env })
        assert.equal(spawn.shell, true)
        const result = spawnSync(spawn.command, spawn.args, { shell: spawn.shell, env, encoding: "utf8" })
        assert.equal(result.status, 0, result.stderr)
        assert.deepEqual(JSON.parse(result.stdout), awkward)
      } finally {
        rmSync(dir, { recursive: true, force: true })
      }
    }
  )
})
