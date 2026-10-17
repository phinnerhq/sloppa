// Artifacts end to end, over loopback: the `sloppa artifact` and `sloppa collection` commands, the
// one-time upload URLs agents get over MCP, listing, and byte ranges on shared links, against the real server.
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
  const { code, stdout, stderr } = await cliRun(sloppa, ["artifact", "upload", "--json", "-c", collection, ...paths])
  expect(code, stderr).toBe(0)
  return JSON.parse(stdout) as Array<Uploaded>
}

const list = async (query: Record<string, string>) => {
  const response = await fetch(`${sloppa.url}/api/artifacts?${new URLSearchParams(query)}`, {
    headers: { authorization: `Bearer ${sloppa.token}` }
  })
  return { status: response.status, page: (await response.json()) as { items: Array<Uploaded>; nextCursor?: string } }
}

describe("sloppa artifact upload", () => {
  it("prints a Markdown link per file, embedding images, each serving what was uploaded", async () => {
    const png = file("shot.png", new Uint8Array([0x89, 0x50, 0x4e, 0x47, 1, 2, 3]))
    const log = file("run.log", "line one\nline two\n")

    const { code, stdout, stderr } = await cliRun(sloppa, ["artifact", "upload", "-c", "o/r #1", png, log])

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
      "artifact",
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

    const { code, stderr } = await cliRun(nowhere, ["artifact", "upload", "-c", "o/r #4", file("lost.txt", "x")])

    expect(code).toBe(1)
    expect(stderr).toMatch(/Upload failed.*Transport error/)
  })

  it("uploads nothing when any file doesn't exist", async () => {
    const { code, stderr } = await cliRun(sloppa, [
      "artifact",
      "upload",
      "-c",
      "o/r #5",
      file("fine.txt", "x"),
      join(work, "missing.txt")
    ])

    expect(code).toBe(1)
    expect(stderr).toContain(`Not a file: ${join(work, "missing.txt")}`)
    expect((await list({ collection: "o/r #5" })).page.items).toEqual([])
  })

  it("uploads what's piped in, under --name, for -", async () => {
    const { code, stdout, stderr } = await cliRun(
      sloppa,
      ["artifact", "upload", "-c", "o/r #6", "--name", "piped.log", "-"],
      "from a pipe\n"
    )

    expect(code, stderr).toBe(0)
    const served = await fetch(/\((.+)\)/.exec(stdout)![1]!)
    expect(await served.text()).toBe("from a pipe\n")
  })

  it("refuses - without --name, sending nothing", async () => {
    const { code, stderr } = await cliRun(sloppa, ["artifact", "upload", "-c", "o/r #7", "-"], "x")

    expect(code).toBe(1)
    expect(stderr).toContain("--name report.md -")
    expect((await list({ collection: "o/r #7" })).page.items).toEqual([])
  })
})

describe("sloppa artifact list, get, move and delete", () => {
  /** The CLI's text output: one tab-separated line per item. */
  const rows = (stdout: string) =>
    stdout
      .trim()
      .split("\n")
      .filter(Boolean)
      .map((line) => line.split("\t"))

  it("lists one line per artifact, and prints the next page's command on stderr", async () => {
    const uploaded = await upload("o/r #40", file("one.txt", "1"), file("two.txt", "2"), file("three.txt", "3"))

    const first = await cliRun(sloppa, ["artifact", "list", "-c", "o/r #40", "--limit", "2"])
    const next = /^more: sloppa (.+)$/m.exec(first.stderr)![1]!
    // The printed command, as a shell would split it: the collection is quoted for its space and #.
    expect(next).toMatch(/^artifact list -c 'o\/r #40' --limit 2 --cursor \S+$/)
    const words = next.match(/'[^']*'|\S+/g)!.map((word) => (word.startsWith("'") ? word.slice(1, -1) : word))
    const second = await cliRun(sloppa, words)

    expect(first.code).toBe(0)
    expect(rows(first.stdout)).toHaveLength(2)
    expect(second.stderr).not.toContain("more:")
    const listed = [...rows(first.stdout), ...rows(second.stdout)]
    expect(listed.map((row) => row[0]!).toSorted()).toEqual(uploaded.map((artifact) => artifact.id).toSorted())
    expect(listed.find(([, name]) => name === "two.txt")).toEqual([
      uploaded.find((artifact) => artifact.name === "two.txt")!.id,
      "two.txt",
      "o/r #40",
      uploaded.find((artifact) => artifact.name === "two.txt")!.rawUrl
    ])
  })

  it("gets an artifact's fields, and points at list for an unknown id", async () => {
    const [artifact] = await upload("o/r #41", file("shown.txt", "abc"))

    const shown = await cliRun(sloppa, ["artifact", "get", artifact!.id])
    const missing = await cliRun(sloppa, ["artifact", "get", "00000000-0000-4000-8000-000000000000"])

    expect(shown.stdout).toContain(`name: shown.txt\ncollection: o/r #41\n`)
    expect(shown.stdout).toContain(`raw_url: ${artifact!.rawUrl}`)
    expect(missing.code).toBe(1)
    expect(missing.stderr).toContain("find ids with sloppa artifact list")
  })

  it("moves an artifact to another collection, keeping its links", async () => {
    const [artifact] = await upload("o/r #42", file("moved.txt", "m"))

    const { code } = await cliRun(sloppa, ["artifact", "move", artifact!.id, "-c", "o/r #43"])

    expect(code).toBe(0)
    expect((await list({ collection: "o/r #43" })).page.items.map((item) => item.id)).toEqual([artifact!.id])
    expect(await (await fetch(artifact!.rawUrl)).text()).toBe("m")
  })

  it("deletes only with --yes, shows what would go with --dry-run, and succeeds again once it's gone", async () => {
    const [artifact] = await upload("o/r #44", file("doomed.txt", "d"))

    const refused = await cliRun(sloppa, ["artifact", "delete", artifact!.id])
    const dry = await cliRun(sloppa, ["artifact", "delete", artifact!.id, "--dry-run"])
    const stillThere = (await fetch(artifact!.rawUrl)).status
    const deleted = await cliRun(sloppa, ["artifact", "delete", artifact!.id, "--yes"])
    const again = await cliRun(sloppa, ["artifact", "delete", artifact!.id, "--yes"])

    expect(refused.code).toBe(1)
    expect(refused.stderr).toContain(`sloppa artifact delete ${artifact!.id} --yes`)
    expect(dry.stdout).toMatch(new RegExp(`^would delete: ${artifact!.id}\tdoomed.txt`))
    expect(stillThere).toBe(200)
    expect(deleted.stdout.trim()).toBe(`deleted: ${artifact!.id}`)
    expect(again).toMatchObject({ code: 0, stdout: `already gone: ${artifact!.id}\n` })
    expect((await fetch(artifact!.rawUrl)).status).toBe(404)
  })

  it("lists collections with their counts", async () => {
    await upload("o/r #45", file("a.txt", "a"), file("b.txt", "b"))

    const { stdout } = await cliRun(sloppa, ["collection", "list"])

    expect(rows(stdout).find(([name]) => name === "o/r #45")?.[1]).toBe("2")
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
    // The signature's first character: its last one carries padding bits, so some edits there change nothing.
    const forged = ticket.uploadUrl.replace(/\.(.)([^.]*)$/, (_, first: string, rest: string) =>
      first === "A" ? `.B${rest}` : `.A${rest}`
    )

    const response = await put(forged, "x")

    expect(response.status).toBe(403)
    expect((await fetch(ticket.rawUrl)).status).toBe(404)
  })
})
