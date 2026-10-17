// The type an upload is served with, which decides whether a browser renders it, plays it or
// downloads it.
import { describe, expect, it } from "vitest"
import { resolveContentType } from "../src/ContentType.ts"

describe("resolveContentType", () => {
  it("trusts the type the client declared", () => {
    expect(resolveContentType("notes.bin", "text/plain; charset=utf-8")).toBe("text/plain; charset=utf-8")
    expect(resolveContentType("shot.png", " image/webp ")).toBe("image/webp")
  })

  it("goes by the extension when the type is missing or the generic fallback", () => {
    expect(resolveContentType("shot.png", undefined)).toBe("image/png")
    expect(resolveContentType("run.log", "")).toBe("text/plain; charset=utf-8")
    expect(resolveContentType("trace.json", "application/octet-stream")).toBe("application/json; charset=utf-8")
  })

  it("reads extensions in any case, and only the last one", () => {
    expect(resolveContentType("SHOT.PNG", undefined)).toBe("image/png")
    expect(resolveContentType("logs.tar.gz", undefined)).toBe("application/gzip")
  })

  it("falls back to a download for unknown extensions", () => {
    expect(resolveContentType("data.parquet", undefined)).toBe("application/octet-stream")
  })

  it("falls back to a download for names without an extension, even one that spells one", () => {
    expect(resolveContentType("README", undefined)).toBe("application/octet-stream")
    expect(resolveContentType("json", undefined)).toBe("application/octet-stream")
    expect(resolveContentType("png", undefined)).toBe("application/octet-stream")
  })

  it("doesn't take a dotfile's name for an extension", () => {
    expect(resolveContentType(".env", undefined)).toBe("application/octet-stream")
  })
})
