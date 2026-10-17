// The end-to-end harness: the real server with its previews, the real CLI from source,
// and the fixture dev server. Shared by the suites, which each stop what they started.
import { passwordMac, previewId, signTicket } from "@sloppa/preview"
import { type ChildProcess, spawn } from "node:child_process"
import { randomBytes } from "node:crypto"
import { mkdtempSync, readFileSync, rmSync } from "node:fs"
import {
  createServer as createHttpServer,
  request as httpRequest,
  type IncomingHttpHeaders,
  type Server
} from "node:http"
import { createServer } from "node:net"
import { tmpdir } from "node:os"
import { dirname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"
import { setTimeout as sleep } from "node:timers/promises"

const here = dirname(fileURLToPath(import.meta.url))
const root = resolve(here, "../../..")
const cli = join(root, "apps/cli/src/main.ts")
const serverMain = join(root, "apps/server/src/main.ts")
export const fixture = join(here, "fixture.ts")
export const work = mkdtempSync(join(tmpdir(), "sloppa-preview-test-"))
export const secret = randomBytes(32).toString("base64url")

export const freePort = () =>
  new Promise<number>((done) => {
    const server = createServer().listen(0, "127.0.0.1", () => {
      const address = server.address()
      server.close(() => done(typeof address === "object" && address !== null ? address.port : 0))
    })
  })

export const alive = (pid: number) => {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

export const waitFor = async (check: () => boolean | Promise<boolean>, what: string, ms = 15_000) => {
  const deadline = Date.now() + ms
  while (!(await check())) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`)
    await sleep(25)
  }
}

/** Resolves with the exit code, or 128 + the signal's number like a shell would say. */
export const exited = (child: ChildProcess) =>
  new Promise<number>((done) => {
    const settle = (code: number | null, signal: NodeJS.Signals | null) =>
      done(code ?? 128 + ({ SIGTERM: 15, SIGKILL: 9, SIGINT: 2 } as Record<string, number>)[signal ?? ""]!)
    // Already gone, by exiting or by a signal: "exit" won't come again.
    if (child.exitCode !== null || child.signalCode !== null) return settle(child.exitCode, child.signalCode)
    child.once("exit", settle)
  })

/** Raw HTTP, so nothing is decompressed or followed on the way. */
export const get = (url: string, headers: Record<string, string> = {}) =>
  new Promise<{ status: number; headers: IncomingHttpHeaders; body: Buffer }>((done, fail) => {
    httpRequest(url, { headers, signal: AbortSignal.timeout(10_000) }, (response) => {
      const chunks: Array<Buffer> = []
      response.on("data", (chunk: Buffer) => chunks.push(chunk))
      response.on("end", () =>
        done({ status: response.statusCode ?? 0, headers: response.headers, body: Buffer.concat(chunks) })
      )
    })
      .on("error", (cause) => fail(new Error(`GET ${url}`, { cause })))
      .end()
  })

export const status = async (url: string) => (await get(url)).status

export interface Sloppa {
  /** The app, where the CLI asks for tickets. */
  readonly url: string
  /** The preview domain's apex. */
  readonly origin: string
  readonly token: string
  readonly process: ChildProcess
}

/**
 * The environment, minus anything sloppa reads and the test runner's own: Windows needs the rest
 * in any process (SystemRoot and the like), and neither a developer's settings nor vitest's
 * (NODE_ENV=test among them) should reach the server under test.
 */
const inherited = Object.fromEntries(
  Object.entries(process.env).filter(
    ([name]) =>
      !/^(HOST|PORT|PUBLIC_URL|DATA_DIR|STATIC_DIR|TRUST_PROXY|BOOTSTRAP_.*|S3_.*|PREVIEW_.*|NODE_ENV|VITEST.*)$/i.test(
        name
      )
  )
)

/** A request that fails, saying which, instead of waiting forever. */
const request = async (what: string, url: string, init: RequestInit = {}) => {
  try {
    return await fetch(url, { ...init, signal: AbortSignal.timeout(10_000) })
  } catch (cause) {
    throw new Error(`${what}: ${url}`, { cause })
  }
}

/**
 * Every server started, so a test that fails halfway can't leave one running. Stopped like a
 * deployment would stop it, so it shuts down cleanly and writes its coverage; killed if it won't.
 */
const servers = new Set<ChildProcess>()
export const stopServers = async () => {
  for (const child of servers) {
    child.kill("SIGTERM")
    const timer = setTimeout(() => child.kill("SIGKILL"), 10_000)
    await exited(child)
    clearTimeout(timer)
  }
  servers.clear()
}

/** The real server, on a fresh port unless given one, signed in with a fresh agent token. */
export const startSloppa = async (
  options: { env?: Record<string, string>; port?: number; dataDir?: string } = {}
): Promise<Sloppa & { readonly port: number; readonly dataDir: string }> => {
  const port = options.port ?? (await freePort())
  const dataDir = options.dataDir ?? mkdtempSync(join(work, "data-"))
  const url = `http://127.0.0.1:${port}`
  const origin = `http://preview.localhost:${port}`
  const child = spawn(process.execPath, [serverMain], {
    env: {
      ...inherited,
      HOST: "127.0.0.1",
      PORT: String(port),
      DATA_DIR: dataDir,
      BOOTSTRAP_USERNAME: "test",
      BOOTSTRAP_PASSWORD: "test-password",
      PREVIEW_ORIGIN: origin,
      PREVIEW_SECRET: secret,
      PREVIEW_MISS_BURST: "1000",
      ...options.env
    },
    stdio: "ignore"
  })
  servers.add(child)
  await waitFor(async () => (await get(`${url}/api/openapi.json`).catch(() => undefined))?.status === 200, "the app")
  const login = await request("sign in", `${url}/api/session`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ username: "test", password: "test-password" })
  })
  const cookie = login.headers
    .getSetCookie()
    .map((cookie) => cookie.split(";")[0])
    .join("; ")
  const created = await request("create an agent", `${url}/api/agents`, {
    method: "POST",
    headers: { "content-type": "application/json", cookie },
    body: JSON.stringify({ name: `test-${randomBytes(3).toString("hex")}` })
  })
  const { token } = (await created.json()) as { token: string }
  return { url, origin, token, process: child, port, dataDir }
}

/** The app's preview API, as the dashboard and the MCP tools use it. */
export const api = async (sloppa: Sloppa, method: string, path: string) => {
  const response = await request(`${method} ${path}`, `${sloppa.url}${path}`, {
    method,
    headers: { authorization: `Bearer ${sloppa.token}` }
  })
  return { status: response.status, json: response.status === 204 ? null : ((await response.json()) as unknown) }
}

/** Runs any CLI command against `sloppa`, to its end, with `input` piped to its stdin. */
export const cliRun = (sloppa: { url: string; token: string }, args: ReadonlyArray<string>, input?: string) =>
  new Promise<{ code: number; stdout: string; stderr: string }>((done) => {
    const child = spawn(process.execPath, [cli, ...args], {
      cwd: work,
      env: { ...process.env, SLOPPA_URL: sloppa.url, SLOPPA_TOKEN: sloppa.token, XDG_CONFIG_HOME: work },
      stdio: ["pipe", "pipe", "pipe"]
    })
    child.stdin.end(input ?? "")
    let stdout = ""
    let stderr = ""
    child.stdout.setEncoding("utf8").on("data", (chunk: string) => (stdout += chunk))
    child.stderr.setEncoding("utf8").on("data", (chunk: string) => (stderr += chunk))
    void exited(child).then((code) => done({ code, stdout, stderr }))
  })

/** Calls one of the MCP server's tools as an agent would: a session, then the call. */
export const mcpTool = async (sloppa: Sloppa, tool: string, args: Record<string, unknown>) => {
  const rpc = (body: object, session?: string) =>
    request(`MCP ${"method" in body ? String(body.method) : ""}`, `${sloppa.url}/mcp`, {
      method: "POST",
      headers: {
        authorization: `Bearer ${sloppa.token}`,
        "content-type": "application/json",
        accept: "application/json, text/event-stream",
        ...(session === undefined ? {} : { "mcp-session-id": session })
      },
      body: JSON.stringify({ jsonrpc: "2.0", ...body })
    })

  const initialized = await rpc({
    id: 1,
    method: "initialize",
    params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "test", version: "1" } }
  })
  const session = initialized.headers.get("mcp-session-id") ?? undefined
  await rpc({ method: "notifications/initialized" }, session)

  const called = await rpc({ id: 2, method: "tools/call", params: { name: tool, arguments: args } }, session)
  return (await called.json()) as { result?: { structuredContent?: unknown; isError?: boolean } }
}

/** Hands out tickets for `target` the way the real server does, but with whatever `mint` says. */
export const startStub = (target: Sloppa, mint: () => { secret: string; exp: number }) =>
  new Promise<{ url: string; token: string; server: Server }>((done) => {
    const server = createHttpServer(async (request, response) => {
      let raw = ""
      for await (const chunk of request) raw += chunk
      const body = JSON.parse(raw || "{}") as { name?: string; password?: string }
      const { secret: key, exp } = mint()
      const id = previewId()
      const origin = new URL(target.origin)
      response.writeHead(200, { "content-type": "application/json" })
      response.end(
        JSON.stringify({
          ticket: signTicket(key, {
            id,
            account: 1,
            exp,
            ...(body.name ? { name: body.name } : {}),
            ...(body.password ? { passwordMac: passwordMac(key, id, body.password) } : {})
          }),
          id,
          url: `http://${body.name ?? id}.${origin.host}/`,
          connectUrl: `ws://${origin.host}/_connect`,
          expiresAt: new Date(exp).toISOString()
        })
      )
    })
    server.listen(0, "127.0.0.1", () => {
      const address = server.address()
      const port = typeof address === "object" && address !== null ? address.port : 0
      done({ url: `http://127.0.0.1:${port}`, token: "stub", server })
    })
  })

export interface Running {
  readonly url: string
  readonly id: string
  readonly process: ChildProcess
  readonly exited: Promise<number>
  readonly pids: () => [number, number]
  readonly stderr: () => string
}

/** Every CLI a test starts, stopped after it whatever its assertions did, so none outlive the suite. */
const started = new Set<ChildProcess>()
export const stopStarted = async () => {
  for (const child of started) {
    child.kill("SIGTERM")
    await exited(child)
  }
  started.clear()
}

// Windows holds on to a directory for a moment after the last process in it exits.
export const removeWork = () => rmSync(work, { recursive: true, force: true, maxRetries: 10 })

/** The fixture, as the dev server a preview runs unless a test gives another command. */
const fixtureCommand = [process.execPath, fixture]

export const spawnCli = (
  sloppa: { url: string; token: string },
  flags: Array<string>,
  env: Record<string, string> = {},
  command: ReadonlyArray<string> = fixtureCommand,
  cwd = root
) => {
  const child = spawn(process.execPath, [cli, "preview", "run", ...flags, "--", ...command], {
    cwd,
    env: { ...process.env, SLOPPA_URL: sloppa.url, SLOPPA_TOKEN: sloppa.token, XDG_CONFIG_HOME: work, ...env },
    stdio: ["ignore", "ignore", "pipe"]
  })
  started.add(child)
  let stderr = ""
  child.stderr!.setEncoding("utf8").on("data", (chunk: string) => (stderr += chunk))
  return { child, stderr: () => stderr, exited: exited(child) }
}

export const run = async (
  sloppa: { url: string; token: string },
  flags: Array<string> = [],
  command: ReadonlyArray<string> = fixtureCommand,
  cwd = root
): Promise<Running> => {
  const pidFile = join(work, `pids-${randomBytes(4).toString("hex")}`)
  const { child, stderr, exited } = spawnCli(sloppa, flags, { FIXTURE_PIDS: pidFile }, command, cwd)
  await waitFor(() => stderr().includes("preview_ready:") || child.exitCode !== null, "the preview")
  const url = /preview_url: (\S+)/.exec(stderr())?.[1]
  const id = /preview_id: (\S+)/.exec(stderr())?.[1]
  if (url === undefined || id === undefined) throw new Error(`no preview:\n${stderr()}`)
  await waitFor(() => readFileSafe(pidFile) !== "", "the fixture's pids")
  return {
    url,
    id,
    process: child,
    exited,
    pids: () => readFileSafe(pidFile).split(" ").map(Number) as [number, number],
    stderr
  }
}

export const stop = async (preview: Running) => {
  preview.process.kill("SIGTERM")
  await preview.exited
}

const readFileSafe = (path: string) => {
  try {
    return readFileSync(path, "utf8")
  } catch {
    return ""
  }
}
