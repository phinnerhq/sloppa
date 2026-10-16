// End to end, over loopback: the real CLI, the real sloppa server with its preview listener, and
// a dev server. A stub hands out the tickets the real server never would: forged, expired, or
// pointing at a server the test then kills. POSIX only: it signals process groups, and the memory
// test reads /proc, so Linux alone runs that one.
import assert from "node:assert/strict"
import { createHash, randomBytes } from "node:crypto"
import { readFileSync } from "node:fs"
import { join } from "node:path"
import { after, afterEach, before, describe, test } from "node:test"
import { gunzipSync } from "node:zlib"
import { WebSocket } from "ws"
import {
  alive,
  api,
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
  stopStarted,
  waitFor,
  work
} from "./harness.ts"

const posix = { skip: process.platform === "win32" ? "POSIX only" : false }
const linux = { skip: process.platform === "linux" ? false : "reads /proc" }

afterEach(stopStarted)

let sloppa: Awaited<ReturnType<typeof startSloppa>>

before(async () => {
  sloppa = await startSloppa()
})

after(() => {
  sloppa?.process.kill()
  removeWork()
})

describe("sloppa preview run", posix, () => {
  test("proxies with the dev server's own host, and tells it the public one", async () => {
    const preview = await run(sloppa)
    const port = /preview_port: (\d+)/.exec(preview.stderr())?.[1]
    const response = await fetch(`${preview.url}some/path`)
    assert.deepEqual(await response.json(), {
      path: "/some/path",
      host: `127.0.0.1:${port}`,
      forwardedHost: new URL(preview.url).host,
      forwardedProto: "http",
      authorization: null,
      previewUrl: preview.url,
      args: []
    })
    assert.equal(response.headers.get("x-robots-tag"), "noindex, nofollow")

    const redirect = await get(`${preview.url}redirect`)
    assert.equal(redirect.headers.location, `${preview.url}landed`)

    const cookies = await get(`${preview.url}cookies`)
    assert.deepEqual(cookies.headers["set-cookie"], ["a=1; Path=/; HttpOnly", "b=2; Path=/"])

    // Untouched: still gzip, and the same bytes the dev server sent.
    const gzip = await get(`${preview.url}gzip`, { "accept-encoding": "gzip" })
    assert.equal(gzip.headers["content-encoding"], "gzip")
    assert.equal(gunzipSync(gzip.body).toString(), "squeezed ".repeat(100))
    await stop(preview)
  })

  test("reuses connections to the dev server", async () => {
    const preview = await run(sloppa)
    for (let i = 0; i < 5; i++) assert.equal(await status(`${preview.url}again`), 200)
    const listed = (await api(sloppa, "GET", "/api/previews")).json as Array<{
      id: string
      connections: number
      requests: number
    }>
    const lease = listed.find((lease) => lease.id === preview.id)
    assert.equal(lease?.requests, 5)
    assert.equal(lease?.connections, 1)
    await stop(preview)
  })

  test("streams server-sent events as they come", async () => {
    const preview = await run(sloppa)
    const started = Date.now()
    const reader = (await fetch(`${preview.url}events`)).body!.pipeThrough(new TextDecoderStream()).getReader()
    assert.equal((await reader.read()).value, "data: first\n\n")
    assert.ok(Date.now() - started < 1000)
    assert.equal((await reader.read()).value, "data: second\n\n")
    await stop(preview)
  })

  test("upgrades WebSockets with their subprotocol, like Vite's HMR", async () => {
    const preview = await run(sloppa)
    const ws = new WebSocket(`${preview.url.replace("http:", "ws:")}ws`, ["vite-hmr"])
    await new Promise((done, fail) => ws.once("open", done).once("error", fail))
    assert.equal(ws.protocol, "vite-hmr")
    const echoed = new Promise<string>((done) => ws.once("message", (data) => done((data as Buffer).toString("utf8"))))
    ws.send("hello")
    assert.equal(await echoed, "echo:hello")

    // Ending the preview takes its open sockets along.
    const closed = new Promise((done) => ws.once("close", done))
    preview.process.kill("SIGTERM")
    await closed
    await preview.exited
  })

  test("passes a request body's end through, for servers that wait for it", async () => {
    const preview = await run(sloppa)
    const response = await fetch(`${preview.url}half-close`, { method: "POST", body: "x".repeat(100_000) })
    assert.equal(await response.text(), "got 100000")
    await stop(preview)
  })

  test("carries a large upload to a slow dev server without buffering it", linux, async () => {
    const preview = await run(sloppa)
    const payload = randomBytes(256 << 20)
    const rss = (pid: number) => Number(/VmRSS:\s+(\d+)/.exec(readFileSync(`/proc/${pid}/status`, "utf8"))?.[1]) * 1024
    const watched = [preview.process.pid!, sloppa.process.pid!]
    const before = watched.map(rss)
    const peak = [...before]
    const sampler = setInterval(() => watched.forEach((pid, i) => (peak[i] = Math.max(peak[i]!, rss(pid)))), 20)
    const response = await fetch(`${preview.url}upload`, { method: "POST", body: payload })
    clearInterval(sampler)
    assert.deepEqual(await response.json(), {
      size: payload.length,
      sha256: createHash("sha256").update(payload).digest("hex")
    })
    // Measured: 50-65 MiB of allocator churn on each end whatever the size (the same for 384 MiB),
    // against the whole upload when a full buffer didn't pause the WebSocket. Bounded is the point.
    watched.forEach((_, i) => {
      const growth = peak[i]! - before[i]!
      assert.ok(growth < 128 << 20, `${["the CLI", "the server"][i]} grew ${growth >> 20} MiB`)
    })
    await stop(preview)
  })

  test("asks for the password, with any user name", async () => {
    const preview = await run(sloppa, ["--password", "hunter2"])
    assert.equal(await status(preview.url), 401)
    const basic = (credentials: string) => ({ authorization: `Basic ${Buffer.from(credentials).toString("base64")}` })
    assert.equal((await get(preview.url, basic("me:hunter3"))).status, 401)
    const right = await get(preview.url, basic("me:hunter2"))
    assert.equal(right.status, 200)
    // The dev server never sees the preview's password.
    assert.equal(JSON.parse(right.body.toString()).authorization, null)
    await stop(preview)
  })

  test("uses a name as the subdomain, one preview at a time", async () => {
    const preview = await run(sloppa, ["--name", "checkout-demo"])
    assert.equal(preview.url, `http://checkout-demo.${new URL(sloppa.origin).host}/`)
    assert.equal(await status(preview.url), 200)
    const second = spawnCli(sloppa, ["--name", "checkout-demo"])
    assert.equal(await second.exited, 1)
    assert.match(second.stderr(), /name taken/)
    assert.equal(await status(preview.url), 200)
    await stop(preview)
  })

  test("a dev server that isn't listening yet is a 502 that says so", async () => {
    const pidFile = join(work, `pids-${randomBytes(4).toString("hex")}`)
    // Listens on a port other than the one it was given.
    const { stderr, child } = spawnCli(sloppa, [], { FIXTURE_PIDS: pidFile, FIXTURE_PORT: String(await freePort()) })
    await waitFor(() => stderr().includes("preview_url:"), "the tunnel")
    const url = /preview_url: (\S+)/.exec(stderr())![1]!
    const response = await get(url)
    assert.equal(response.status, 502)
    assert.match(response.body.toString(), /Dev server not listening/)
    child.kill("SIGTERM")
  })

  test("stopping the CLI stops the dev server's whole process group, and the route", async () => {
    const preview = await run(sloppa)
    const [server, grandchild] = preview.pids()
    preview.process.kill("SIGTERM")
    assert.equal(await preview.exited, 130)
    await waitFor(() => !alive(server) && !alive(grandchild), "the process group to stop")
    assert.equal(await status(preview.url), 404)
  })

  test("a CLI that crashes takes the route with it", async () => {
    const preview = await run(sloppa)
    const [server, grandchild] = preview.pids()
    preview.process.kill("SIGKILL")
    await preview.exited
    await waitFor(async () => (await status(preview.url)) === 404, "the route to go")
    // No cleanup is possible after SIGKILL: the dev server is orphaned, as documented.
    assert.ok(alive(server) && alive(grandchild))
    process.kill(-server, "SIGKILL")
  })

  test("the dev server exiting ends the preview with its exit code", async () => {
    const preview = await run(sloppa)
    const [server, grandchild] = preview.pids()
    process.kill(server, "SIGKILL")
    assert.equal(await preview.exited, 1)
    assert.match(preview.stderr(), /the dev server exited/)
    await waitFor(() => !alive(grandchild), "the grandchild to be stopped too")
    assert.equal(await status(preview.url), 404)
  })

  test("stopping from sloppa ends the CLI and its dev server", async () => {
    const preview = await run(sloppa)
    const [server] = preview.pids()
    assert.equal((await api(sloppa, "DELETE", `/api/previews/${preview.id}`)).status, 204)
    assert.equal(await preview.exited, 0)
    assert.match(preview.stderr(), /stopped from sloppa/)
    await waitFor(() => !alive(server), "the dev server to stop")
  })

  test("reconnects to the same URL when the server restarts", async () => {
    const preview = await run(sloppa)
    sloppa.process.kill("SIGTERM")
    await exited(sloppa.process)
    sloppa = await startSloppa({ ports: sloppa.ports, dataDir: sloppa.dataDir })
    await waitFor(() => preview.stderr().includes("reconnected"), "the reconnect")
    assert.equal(await status(`${preview.url}back`), 200)
    await stop(preview)
  })

  test("still stops at its ticket's expiry when the server is gone", async () => {
    const lonely = await startSloppa()
    const stub = await startStub(lonely, () => ({ secret, exp: Date.now() + 3000 }))
    try {
      const preview = await run(stub)
      const [server] = preview.pids()
      lonely.process.kill("SIGKILL")
      assert.equal(await preview.exited, 0)
      assert.match(preview.stderr(), /reconnecting/)
      assert.match(preview.stderr(), /time is up/)
      await waitFor(() => !alive(server), "the dev server to stop")
    } finally {
      stub.server.close()
    }
  })

  test("refuses forged and expired tickets", async () => {
    for (const [ticket, reason] of [
      [{ secret: "not-the-secret-not-the-secret-12", exp: Date.now() + 60_000 }, "invalid preview ticket"],
      [{ secret, exp: Date.now() - 1000 }, "expired preview ticket"]
    ] as const) {
      const stub = await startStub(sloppa, () => ticket)
      try {
        const { exited, stderr } = spawnCli(stub, [])
        assert.equal(await exited, 1)
        assert.match(stderr(), new RegExp(reason))
      } finally {
        stub.server.close()
      }
    }
  })
})

describe("the preview listener", posix, () => {
  test("rate-limits guesses at unknown hosts", async () => {
    const strict = await startSloppa({ env: { PREVIEW_MISS_BURST: "3", PREVIEW_MISS_EVERY: "1 minute" } })
    try {
      const host = new URL(strict.origin).host
      const statuses = []
      for (const label of ["aaaaaaaa", "bbbbbbbb", "cccccccc", "dddddddd"]) {
        statuses.push(await status(`http://${label}.${host}/`))
      }
      assert.deepEqual(statuses, [404, 404, 404, 429])
    } finally {
      strict.process.kill()
    }
  })
})
