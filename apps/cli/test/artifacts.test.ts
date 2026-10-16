// Artifacts end to end, over loopback: `sloppa upload`, the one-time upload URLs agents get over
// MCP, listing, and byte ranges on shared links, against the real server.
import { writeFileSync } from "node:fs"
import { join } from "node:path"
import { afterAll, beforeAll, describe, expect, it } from "vitest"
import { cliRun, freePort, mcpTool, removeWork, startSloppa, stopServers, work } from "./harness.ts"

let sloppa: Awaited<ReturnType<typeof startSloppa>>

beforeAll(async () => {
  sloppa = await startSloppa()
})

afterAll(async () => {
  await stopServers()
  removeWork()
})

/** A file in the test's own folder, holding `content`. */
const file = (name: string, content: string | Uint8Array) => {
  const path = join(work, name)
  writeFileSync(path, content)
  return path
}

interface Uploaded {
  readonly id: string
  readonly name: string
  readonly url: string
  readonly rawUrl: string
}

const upload = async (collection: string, ...paths: Array<string>) => {
  const { code, stdout, stderr } = await cliRun(sloppa, ["upload", "--json", "-c", collection, ...paths])
  expect(code, stderr).toBe(0)
  return JSON.parse(stdout) as Array<Uploaded>
}

const list = async (query: Record<string, string>) => {
  const response = await fetch(`${sloppa.url}/api/artifacts?${new URLSearchParams(query)}`, {
    headers: { authorization: `Bearer ${sloppa.token}` }
  })
  return { status: response.status, page: (await response.json()) as { items: Array<Uploaded>; nextCursor?: string } }
}

describe("sloppa upload", () => {
  it("prints a Markdown link per file, embedding images, each serving what was uploaded", async () => {
    const png = file("shot.png", new Uint8Array([0x89, 0x50, 0x4e, 0x47, 1, 2, 3]))
    const log = file("run.log", "line one\nline two\n")

    const { code, stdout, stderr } = await cliRun(sloppa, ["upload", "-c", "o/r #1", png, log])

    expect(code, stderr).toBe(0)
    const [image, link] = stdout.trim().split("\n")
    expect(image).toMatch(/^!\[shot\.png\]\(http:\/\/127\.0\.0\.1:\d+\/f\/[0-9a-f-]+\/shot\.png\)$/)
    expect(link).toMatch(/^\[run\.log\]\(http:\/\/127\.0\.0\.1:\d+\/f\/[0-9a-f-]+\/run\.log\)$/)

    const served = await fetch(/\((.+)\)/.exec(link!)![1]!)
    expect(await served.text()).toBe("line one\nline two\n")
    expect(served.headers.get("content-type")).toBe("text/plain; charset=utf-8")
  })

  it("prints the artifacts as JSON with --json", async () => {
    const [artifact] = await upload("o/r #2", file("notes.md", "# notes"))

    expect(artifact).toMatchObject({ name: "notes.md" })
    expect(artifact!.rawUrl).toBe(`${sloppa.url}/f/${artifact!.id}/notes.md`)
  })

  it("fails, storing nothing, with a token the server doesn't know", async () => {
    const { code, stderr } = await cliRun({ ...sloppa, token: "not-a-token" }, [
      "upload",
      "-c",
      "o/r #3",
      file("refused.txt", "x")
    ])

    expect(code).toBe(1)
    expect(stderr).toMatch(/Upload failed.*401/)
    expect((await list({ collection: "o/r #3" })).page.items).toEqual([])
  })

  it("fails when the server can't be reached", async () => {
    const nowhere = { url: `http://127.0.0.1:${await freePort()}`, token: sloppa.token }

    const { code, stderr } = await cliRun(nowhere, ["upload", "-c", "o/r #4", file("lost.txt", "x")])

    expect(code).toBe(1)
    expect(stderr).toMatch(/Upload failed.*Transport error/)
  })

  it("refuses a file that doesn't exist, before sending anything", async () => {
    const { code } = await cliRun(sloppa, ["upload", "-c", "o/r #5", join(work, "missing.txt")])

    expect(code).not.toBe(0)
    expect((await list({ collection: "o/r #5" })).page.items).toEqual([])
  })
})

describe("listing", () => {
  it("pages newest first, every artifact exactly once, until there's no next cursor", async () => {
    // One command: several artifacts can share a millisecond, and paging must still hold.
    const uploaded = await upload("o/r #10", ...["a", "b", "c", "d", "e"].map((name) => file(`${name}.txt`, name)))

    const seen: Array<string> = []
    let cursor: string | undefined
    do {
      const { page } = await list({ collection: "o/r #10", limit: "2", ...(cursor ? { cursor } : {}) })
      expect(page.items.length).toBeLessThanOrEqual(2)
      seen.push(...page.items.map((item) => item.id))
      cursor = page.nextCursor
    } while (cursor !== undefined)

    expect(seen.toSorted()).toEqual(uploaded.map((artifact) => artifact.id).toSorted())
    expect(new Set(seen).size).toBe(5)
  })

  it("lists the newest upload first", async () => {
    const [older] = await upload("o/r #11", file("older.txt", "1"))
    const [newer] = await upload("o/r #11", file("newer.txt", "2"))

    const { page } = await list({ collection: "o/r #11" })

    expect(page.items.map((item) => item.id)).toEqual([newer!.id, older!.id])
  })

  it("keeps to one collection, and finds by name whatever the case", async () => {
    await upload("o/r #12", file("Report-Final.PDF", "%PDF"))
    await upload("o/r #13", file("elsewhere.txt", "x"))

    const inCollection = await list({ collection: "o/r #12" })
    const found = await list({ q: "report-final" })

    expect(inCollection.page.items.map((item) => item.name)).toEqual(["Report-Final.PDF"])
    expect(found.page.items.map((item) => item.name)).toEqual(["Report-Final.PDF"])
  })

  it("answers a cursor it never issued with a page, not an error", async () => {
    const { status, page } = await list({ collection: "o/r #10", cursor: "not-a-cursor" })

    expect(status).toBe(200)
    expect(page.items.length).toBeGreaterThan(0)
  })
})

describe("byte ranges on shared links", () => {
  const alphabet = "abcdefghijklmnopqrstuvwxyz"
  let rawUrl: string

  beforeAll(async () => {
    const [artifact] = await upload("o/r #20", file("alphabet.txt", alphabet))
    rawUrl = artifact!.rawUrl
  })

  const fetchRange = async (range: string) => {
    const response = await fetch(rawUrl, { headers: { range } })
    return { status: response.status, range: response.headers.get("content-range"), body: await response.text() }
  }

  it("serves the bytes asked for, as RFC 9110 reads each form", async () => {
    expect(await fetchRange("bytes=0-4")).toEqual({ status: 206, range: "bytes 0-4/26", body: "abcde" })
    expect(await fetchRange("bytes=20-")).toEqual({ status: 206, range: "bytes 20-25/26", body: "uvwxyz" })
    expect(await fetchRange("bytes=-3")).toEqual({ status: 206, range: "bytes 23-25/26", body: "xyz" })
  })

  it("stops a range at the end of the file", async () => {
    expect(await fetchRange("bytes=24-100")).toEqual({ status: 206, range: "bytes 24-25/26", body: "yz" })
  })

  it("serves the whole file for a range it can't satisfy or doesn't support", async () => {
    for (const range of ["bytes=30-40", "bytes=5-2", "bytes=0-1,4-5", "items=0-4", "bytes=-0", "bytes=-"]) {
      expect(await fetchRange(range), range).toEqual({ status: 200, range: null, body: alphabet })
    }
  })

  it("says it takes ranges", async () => {
    expect((await fetch(rawUrl)).headers.get("accept-ranges")).toBe("bytes")
  })
})

describe("upload URLs from MCP", () => {
  const requestUpload = async (args: Record<string, unknown>) => {
    const { result } = await mcpTool(sloppa, "request_upload", args)
    expect(result?.isError ?? false).toBe(false)
    return result!.structuredContent as { id: string; uploadUrl: string; rawUrl: string }
  }

  const put = (url: string, body: string, contentType?: string) =>
    fetch(url, { method: "PUT", body, ...(contentType ? { headers: { "content-type": contentType } } : {}) })

  it("store the one file they were issued for, under the name and type given", async () => {
    const ticket = await requestUpload({ name: "trace.json", collection: "o/r #30", contentType: "application/json" })

    const response = await put(ticket.uploadUrl, '{"ok":true}')
    const served = await fetch(ticket.rawUrl)

    expect(response.status).toBe(200)
    expect(((await response.json()) as { id: string }).id).toBe(ticket.id)
    expect(await served.text()).toBe('{"ok":true}')
    expect(served.headers.get("content-type")).toBe("application/json")
  })

  it("take the request's type when the ticket names none", async () => {
    const ticket = await requestUpload({ name: "page", collection: "o/r #31" })

    await put(ticket.uploadUrl, "<p>hi</p>", "text/html")

    expect((await fetch(ticket.rawUrl)).headers.get("content-type")).toBe("text/html")
  })

  it("work once: a second upload is refused, and the first stays", async () => {
    const ticket = await requestUpload({ name: "once.txt", collection: "o/r #32" })

    const first = await put(ticket.uploadUrl, "first")
    const second = await put(ticket.uploadUrl, "second")

    expect(first.status).toBe(200)
    expect(second.status).toBe(409)
    expect(await (await fetch(ticket.rawUrl)).text()).toBe("first")
  })

  it("refuse a URL that was altered, storing nothing", async () => {
    const ticket = await requestUpload({ name: "forged.txt", collection: "o/r #33" })
    const forged = ticket.uploadUrl.replace(/.$/, (last) => (last === "A" ? "B" : "A"))

    const response = await put(forged, "x")

    expect(response.status).toBe(403)
    expect((await fetch(ticket.rawUrl)).status).toBe(404)
  })
})
