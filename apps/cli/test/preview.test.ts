// End to end, over loopback: the real CLI, the real sloppa server with its preview listener, and
// a dev server. A stub hands out the tickets the real server never would: forged, expired, or
// pointing at a server the test then kills. POSIX only: it signals process groups, and the memory
// test reads /proc, so Linux alone runs that one.
import { createHash, randomBytes } from "node:crypto"
import { readFileSync } from "node:fs"
import { join } from "node:path"
import { gunzipSync } from "node:zlib"
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest"
import { WebSocket } from "ws"
import {
  alive,
  api,
  cliRun,
  exited,
  freePort,
  get,
  removeWork,
  run,
  secret,
  spawnCli,
  startSloppa,
  startStub,
  status,
  stop,
  stopServers,
  stopStarted,
  waitFor,
  work
} from "./harness.ts"

afterEach(stopStarted)

let sloppa: Awaited<ReturnType<typeof startSloppa>>

beforeAll(async () => {
  sloppa = await startSloppa()
})

afterAll(async () => {
  await stopServers()
  removeWork()
})

describe.skipIf(process.platform === "win32")("sloppa preview run", () => {
  it("proxies with the dev server's own host, and tells it the public one", async () => {
    const preview = await run(sloppa)
    const port = /preview_port: (\d+)/.exec(preview.stderr())?.[1]
    const response = await fetch(`${preview.url}some/path`)
    expect(await response.json()).toEqual({
      path: "/some/path",
      host: `127.0.0.1:${port}`,
      forwardedHost: new URL(preview.url).host,
      forwardedProto: "http",
      authorization: null,
      previewUrl: preview.url,
      args: []
    })
    expect(response.headers.get("x-robots-tag")).toBe("noindex, nofollow")

    const redirect = await get(`${preview.url}redirect`)
    expect(redirect.headers.location).toBe(`${preview.url}landed`)

    const cookies = await get(`${preview.url}cookies`)
    expect(cookies.headers["set-cookie"]).toEqual(["a=1; Path=/; HttpOnly", "b=2; Path=/"])

    // Untouched: still gzip, and the same bytes the dev server sent.
    const gzip = await get(`${preview.url}gzip`, { "accept-encoding": "gzip" })
    expect(gzip.headers["content-encoding"]).toBe("gzip")
    expect(gunzipSync(gzip.body).toString()).toBe("squeezed ".repeat(100))
    await stop(preview)
  })

  it("reuses connections to the dev server", async () => {
    const preview = await run(sloppa)
    for (let i = 0; i < 5; i++) expect(await status(`${preview.url}again`)).toBe(200)
    const listed = (await api(sloppa, "GET", "/api/previews")).json as Array<{
      id: string
      connections: number
      requests: number
    }>
    const lease = listed.find((lease) => lease.id === preview.id)
    expect(lease?.requests).toBe(5)
    expect(lease?.connections).toBe(1)
    await stop(preview)
  })

  it("streams server-sent events as they come", async () => {
    const preview = await run(sloppa)
    const started = Date.now()
    const reader = (await fetch(`${preview.url}events`)).body!.pipeThrough(new TextDecoderStream()).getReader()
    expect((await reader.read()).value).toBe("data: first\n\n")
    expect(Date.now() - started < 1000).toBe(true)
    expect((await reader.read()).value).toBe("data: second\n\n")
    await stop(preview)
  })

  it("upgrades WebSockets with their subprotocol, like Vite's HMR", async () => {
    const preview = await run(sloppa)
    const ws = new WebSocket(`${preview.url.replace("http:", "ws:")}ws`, ["vite-hmr"])
    await new Promise((done, fail) => ws.once("open", done).once("error", fail))
    expect(ws.protocol).toBe("vite-hmr")
    const echoed = new Promise<string>((done) => ws.once("message", (data) => done((data as Buffer).toString("utf8"))))
    ws.send("hello")
    expect(await echoed).toBe("echo:hello")

    // Ending the preview takes its open sockets along.
    const closed = new Promise((done) => ws.once("close", done))
    preview.process.kill("SIGTERM")
    await closed
    await preview.exited
  })

  it("passes a request body's end through, for servers that wait for it", async () => {
    const preview = await run(sloppa)
    const response = await fetch(`${preview.url}half-close`, { method: "POST", body: "x".repeat(100_000) })
    expect(await response.text()).toBe("got 100000")
    await stop(preview)
  })

  // Linux alone: it reads /proc.
  it.runIf(process.platform === "linux")(
    "carries a large upload to a slow dev server without buffering it",
    async () => {
      const preview = await run(sloppa)
      const payload = randomBytes(256 << 20)
      const rss = (pid: number) =>
        Number(/VmRSS:\s+(\d+)/.exec(readFileSync(`/proc/${pid}/status`, "utf8"))?.[1]) * 1024
      const watched = [preview.process.pid!, sloppa.process.pid!]
      const before = watched.map(rss)
      const peak = [...before]
      const sampler = setInterval(() => watched.forEach((pid, i) => (peak[i] = Math.max(peak[i]!, rss(pid)))), 20)
      const response = await fetch(`${preview.url}upload`, { method: "POST", body: payload })
      clearInterval(sampler)
      expect(await response.json()).toEqual({
        size: payload.length,
        sha256: createHash("sha256").update(payload).digest("hex")
      })
      // Measured: 50-65 MiB of allocator churn on each end whatever the size (the same for 384 MiB),
      // against the whole upload when a full buffer didn't pause the WebSocket. Bounded is the point.
      watched.forEach((_, i) => {
        const growth = peak[i]! - before[i]!
        expect(growth < 128 << 20, `${["the CLI", "the server"][i]} grew ${growth >> 20} MiB`).toBe(true)
      })
      await stop(preview)
    }
  )

  it("asks for the password, with any user name", async () => {
    const preview = await run(sloppa, ["--password", "hunter2"])
    expect(await status(preview.url)).toBe(401)
    const basic = (credentials: string) => ({ authorization: `Basic ${Buffer.from(credentials).toString("base64")}` })
    expect((await get(preview.url, basic("me:hunter3"))).status).toBe(401)
    const right = await get(preview.url, basic("me:hunter2"))
    expect(right.status).toBe(200)
    // The dev server never sees the preview's password.
    expect(JSON.parse(right.body.toString()).authorization).toBe(null)
    await stop(preview)
  })

  it("uses a name as the subdomain, one preview at a time", async () => {
    const preview = await run(sloppa, ["--name", "checkout-demo"])
    expect(preview.url).toBe(`http://checkout-demo.${new URL(sloppa.origin).host}/`)
    expect(await status(preview.url)).toBe(200)
    const second = spawnCli(sloppa, ["--name", "checkout-demo"])
    expect(await second.exited, second.stderr()).toBe(1)
    expect(second.stderr()).toMatch(/name taken/)
    expect(await status(preview.url)).toBe(200)
    await stop(preview)
  })

  it("a dev server that isn't listening yet is a 502 that says so", async () => {
    const pidFile = join(work, `pids-${randomBytes(4).toString("hex")}`)
    // Listens on a port other than the one it was given.
    const { stderr, child } = spawnCli(sloppa, [], { FIXTURE_PIDS: pidFile, FIXTURE_PORT: String(await freePort()) })
    await waitFor(() => stderr().includes("preview_url:"), "the tunnel")
    const url = /preview_url: (\S+)/.exec(stderr())![1]!
    const response = await get(url)
    expect(response.status).toBe(502)
    expect(response.body.toString()).toMatch(/Dev server not listening/)
    child.kill("SIGTERM")
  })

  it("stopping the CLI stops the dev server's whole process group, and the route", async () => {
    const preview = await run(sloppa)
    const [server, grandchild] = preview.pids()
    preview.process.kill("SIGTERM")
    expect(await preview.exited, preview.stderr()).toBe(130)
    await waitFor(() => !alive(server) && !alive(grandchild), "the process group to stop")
    expect(await status(preview.url)).toBe(404)
  })

  it("a CLI that crashes takes the route with it", async () => {
    const preview = await run(sloppa)
    const [server, grandchild] = preview.pids()
    preview.process.kill("SIGKILL")
    await preview.exited
    await waitFor(async () => (await status(preview.url)) === 404, "the route to go")
    // No cleanup is possible after SIGKILL: the dev server is orphaned, as documented.
    expect(alive(server) && alive(grandchild)).toBe(true)
    process.kill(-server, "SIGKILL")
  })

  it("the dev server exiting ends the preview with its exit code", async () => {
    const preview = await run(sloppa)
    const [server, grandchild] = preview.pids()
    process.kill(server, "SIGKILL")
    expect(await preview.exited, preview.stderr()).toBe(1)
    expect(preview.stderr()).toMatch(/the dev server exited/)
    await waitFor(() => !alive(grandchild), "the grandchild to be stopped too")
    expect(await status(preview.url)).toBe(404)
  })

  it("sloppa preview list shows it, and stop ends the CLI and its dev server, even twice", async () => {
    const preview = await run(sloppa)
    const [server] = preview.pids()
    const listed = await cliRun(sloppa, ["preview", "list"])
    expect(
      listed.stdout
        .split("\n")
        .find((line) => line.startsWith(`${preview.id}\t`))
        ?.split("\t")[1]
    ).toBe(preview.url)

    const stopped = await cliRun(sloppa, ["preview", "stop", preview.id])
    expect(stopped).toMatchObject({ code: 0, stdout: `stopped: ${preview.id}\n` })
    expect(await preview.exited, preview.stderr()).toBe(0)
    expect(await cliRun(sloppa, ["preview", "stop", preview.id])).toMatchObject({
      code: 0,
      stdout: `not running: ${preview.id}\n`
    })
    expect(preview.stderr()).toMatch(/stopped from sloppa/)
    await waitFor(() => !alive(server), "the dev server to stop")
  })

  it("reconnects to the same URL when the server restarts", async () => {
    const preview = await run(sloppa)
    sloppa.process.kill("SIGTERM")
    await exited(sloppa.process)
    sloppa = await startSloppa({ ports: sloppa.ports, dataDir: sloppa.dataDir })
    await waitFor(() => preview.stderr().includes("reconnected"), "the reconnect")
    expect(await status(`${preview.url}back`)).toBe(200)
    await stop(preview)
  })

  it("still stops at its ticket's expiry when the server is gone", async () => {
    const lonely = await startSloppa()
    const stub = await startStub(lonely, () => ({ secret, exp: Date.now() + 3000 }))
    try {
      const preview = await run(stub)
      const [server] = preview.pids()
      lonely.process.kill("SIGKILL")
      expect(await preview.exited, preview.stderr()).toBe(0)
      expect(preview.stderr()).toMatch(/reconnecting/)
      expect(preview.stderr()).toMatch(/time is up/)
      await waitFor(() => !alive(server), "the dev server to stop")
    } finally {
      stub.server.close()
      lonely.process.kill("SIGKILL")
    }
  })

  it("calls a ticket dead on arrival a failure, even with the server out of reach", async () => {
    // Nothing listens at this origin, so only the CLI's own expiry backstop can decide.
    const nowhere = { ...sloppa, origin: `http://preview.localhost:${await freePort()}` }
    const stub = await startStub(nowhere, () => ({ secret, exp: Date.now() - 1000 }))
    try {
      const { exited, stderr } = spawnCli(stub, [])
      expect(await exited, stderr()).toBe(1)
      expect(stderr()).toMatch(/expired preview ticket/)
    } finally {
      stub.server.close()
    }
  })

  it("refuses forged and expired tickets", async () => {
    for (const [ticket, reason] of [
      [{ secret: "not-the-secret-not-the-secret-12", exp: Date.now() + 60_000 }, "invalid preview ticket"],
      [{ secret, exp: Date.now() - 1000 }, "expired preview ticket"]
    ] as const) {
      const stub = await startStub(sloppa, () => ticket)
      try {
        const { exited, stderr } = spawnCli(stub, [])
        expect(await exited, stderr()).toBe(1)
        expect(stderr()).toMatch(new RegExp(reason))
      } finally {
        stub.server.close()
      }
    }
  })
})

describe.skipIf(process.platform === "win32")("the preview listener", () => {
  it("rate-limits guesses at unknown hosts", async () => {
    const strict = await startSloppa({ env: { PREVIEW_MISS_BURST: "3", PREVIEW_MISS_EVERY: "1 minute" } })
    try {
      const host = new URL(strict.origin).host
      const statuses = []
      for (const label of ["aaaaaaaa", "bbbbbbbb", "cccccccc", "dddddddd"]) {
        statuses.push(await status(`http://${label}.${host}/`))
      }
      expect(statuses).toEqual([404, 404, 404, 429])
    } finally {
      strict.process.kill()
    }
  })
})
