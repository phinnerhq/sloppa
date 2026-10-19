// How the dashboard labels an artifact: which viewer opens it, and its size.
import { describe, expect, it } from "vitest"
import { formatSize, kindOf, pullRequestUrl } from "../src/format.ts"

describe("kindOf", () => {
  it("goes by the media type, whatever its case or parameters", () => {
    expect(kindOf({ name: "a", contentType: "IMAGE/PNG" })).toBe("image")
    expect(kindOf({ name: "a", contentType: "video/mp4" })).toBe("video")
    expect(kindOf({ name: "a", contentType: "audio/ogg" })).toBe("audio")
    expect(kindOf({ name: "a", contentType: "application/pdf" })).toBe("pdf")
    expect(kindOf({ name: "a", contentType: "text/html; charset=utf-8" })).toBe("html")
  })

  it("shows text formats as text, even when they don't say text/", () => {
    expect(kindOf({ name: "a", contentType: "application/json; charset=utf-8" })).toBe("text")
    expect(kindOf({ name: "a", contentType: "text/csv" })).toBe("text")
  })

  it("falls back to the file name when the type is a generic download", () => {
    expect(kindOf({ name: "run.LOG", contentType: "application/octet-stream" })).toBe("text")
    expect(kindOf({ name: "patch.diff", contentType: "application/octet-stream" })).toBe("text")
    expect(kindOf({ name: "core.dump", contentType: "application/octet-stream" })).toBe("binary")
  })
})

describe("formatSize", () => {
  it("counts bytes under a kilobyte as they are", () => {
    expect(formatSize(0)).toBe("0 B")
    expect(formatSize(1023)).toBe("1023 B")
  })

  it("keeps one decimal below 10 of a unit, none above", () => {
    expect(formatSize(1024)).toBe("1.0 KB")
    expect(formatSize(1536)).toBe("1.5 KB")
    expect(formatSize(10 * 1024)).toBe("10 KB")
    expect(formatSize(5 * 1024 ** 2)).toBe("5.0 MB")
    expect(formatSize(300 * 1024 ** 3)).toBe("300 GB")
  })

  it("stops at gigabytes", () => {
    expect(formatSize(2 * 1024 ** 4)).toBe("2048 GB")
  })
})

describe("pullRequestUrl", () => {
  it("links a collection named owner/repo #42 to its PR on GitHub", () => {
    expect(pullRequestUrl("phinnerhq/sloppa #42")).toBe("https://github.com/phinnerhq/sloppa/pull/42")
    expect(pullRequestUrl("phinnerhq/phinner.dev#7")).toBe("https://github.com/phinnerhq/phinner.dev/pull/7")
  })

  it("keeps a URL as it is, and links nothing else", () => {
    expect(pullRequestUrl("https://example.com/review/3")).toBe("https://example.com/review/3")
    expect(pullRequestUrl("nightly screenshots")).toBeUndefined()
    expect(pullRequestUrl("sloppa #42")).toBeUndefined()
  })
})
