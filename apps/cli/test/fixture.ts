// A dev server for the end-to-end tests: everything a real one does that a proxy can get wrong.
// Reads PORT and HOST like a well-behaved one, and starts a grandchild so tests can check that
// stopping a preview stops its whole process group.
import { spawn } from "node:child_process"
import { createHash } from "node:crypto"
import { writeFileSync } from "node:fs"
import { createServer } from "node:http"
import { setTimeout as sleep } from "node:timers/promises"
import { gzipSync } from "node:zlib"
import { WebSocketServer } from "ws"

// FIXTURE_PORT makes it listen somewhere other than where it was told, like a misconfigured one.
const port = Number(process.env["FIXTURE_PORT"] ?? process.env["PORT"])
// A process of its own that lingers, like a dev server's watcher: stopping the preview has to reach it too.
const grandchild = spawn(process.execPath, ["-e", "setTimeout(() => {}, 600_000)"], { stdio: "ignore" })
if (process.env["FIXTURE_PIDS"]) writeFileSync(process.env["FIXTURE_PIDS"], `${process.pid} ${grandchild.pid}`)

const json = (value: unknown) => [{ "content-type": "application/json" }, JSON.stringify(value)] as const

const server = createServer({ requestTimeout: 0 }, async (request, response) => {
  const url = new URL(request.url ?? "/", "http://fixture")
  switch (url.pathname) {
    case "/events": {
      response.writeHead(200, { "content-type": "text/event-stream" })
      response.write("data: first\n\n")
      await sleep(1500)
      return void response.end("data: second\n\n")
    }
    case "/upload": {
      // Slower than the network on purpose: the tunnel has to hold the rest back, not buffer it.
      const hash = createHash("sha256")
      let size = 0
      for await (const chunk of request as AsyncIterable<Buffer>) {
        hash.update(chunk)
        size += chunk.length
        await sleep(1)
      }
      const [headers, body] = json({ size, sha256: hash.digest("hex") })
      response.writeHead(200, headers)
      return void response.end(body)
    }
    case "/cookies":
      response.setHeader("set-cookie", ["a=1; Path=/; HttpOnly", "b=2; Domain=localhost; Path=/"])
      return void response.end("cookies")
    case "/gzip":
      response.writeHead(200, { "content-encoding": "gzip", "content-type": "text/plain" })
      return void response.end(gzipSync("squeezed ".repeat(100)))
    case "/redirect":
      response.writeHead(302, { location: `http://127.0.0.1:${port}/landed` })
      return void response.end()
    case "/half-close": {
      // Answers only once the whole body arrived: only works if the browser side's end gets through.
      let size = 0
      for await (const chunk of request as AsyncIterable<Buffer>) size += chunk.length
      return void response.end(`got ${size}`)
    }
    default: {
      const [headers, body] = json({
        path: url.pathname,
        host: request.headers.host ?? null,
        forwardedHost: request.headers["x-forwarded-host"] ?? null,
        forwardedProto: request.headers["x-forwarded-proto"] ?? null,
        authorization: request.headers.authorization ?? null,
        previewUrl: process.env["PREVIEW_URL"],
        // As it was started: what reached it through whatever launched it.
        args: process.argv.slice(2)
      })
      response.writeHead(200, headers)
      return void response.end(body)
    }
  }
})

const websockets = new WebSocketServer({
  server,
  path: "/ws",
  handleProtocols: (protocols) => (protocols.has("vite-hmr") ? "vite-hmr" : false)
})
websockets.on("connection", (ws) =>
  ws.on("message", (message) => ws.send(`echo:${(message as Buffer).toString("utf8")}`))
)

server.listen(port, process.env["HOST"] ?? "127.0.0.1", () => console.log(`fixture listening on ${port}`))
