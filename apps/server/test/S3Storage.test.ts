// How an S3 object's metadata reads back as an artifact: what `put` writes today, and what older
// versions left in the bucket. No bucket needed, this is the mapping alone.
import type { ArtifactId } from "@sloppa/api"
import { DateTime } from "effect"
import { describe, expect, it } from "vitest"
import { fromHead } from "../src/S3Storage.ts"

const id = "0a1b2c3d-0000-4000-8000-000000000001" as ArtifactId

describe("fromHead", () => {
  it("reads back what put writes, free-form text included", () => {
    const artifact = fromHead(id, {
      ContentType: "image/png",
      ContentLength: 2048,
      LastModified: new Date("2026-10-16T10:00:00Z"),
      Metadata: {
        name: encodeURIComponent("écran final ✓.png"),
        "uploaded-at": "1792144800123",
        collection: encodeURIComponent("acme/web.app #42")
      }
    })

    expect(artifact).toEqual({
      id,
      name: "écran final ✓.png",
      contentType: "image/png",
      size: 2048,
      createdAt: DateTime.makeUnsafe(1792144800123),
      collection: "acme/web.app #42"
    })
  })

  it("keeps the upload's exact millisecond, which LastModified rounds to the second", () => {
    const artifact = fromHead(id, {
      LastModified: new Date("2026-10-16T10:00:00Z"),
      Metadata: { "uploaded-at": "1792144800999" }
    })

    expect(DateTime.toEpochMillis(artifact.createdAt)).toBe(1792144800999)
  })

  it("files objects from before collections under their PR, then their repo, then unknown", () => {
    const underPr = fromHead(id, { Metadata: { repo: "o/r", pr: "https://github.com/o/r/pull/7" } })
    const underRepo = fromHead(id, { Metadata: { repo: "o/r" } })
    const underNothing = fromHead(id, { Metadata: {} })

    expect(underPr.collection).toBe("o/r #7")
    expect(underRepo.collection).toBe("o/r")
    expect(underNothing.collection).toBe("unknown")
  })

  it("survives an object with no metadata at all", () => {
    const artifact = fromHead(id, { LastModified: new Date("2026-10-16T10:00:00Z") })

    expect(artifact).toEqual({
      id,
      name: id,
      contentType: "application/octet-stream",
      size: 0,
      createdAt: DateTime.makeUnsafe(Date.parse("2026-10-16T10:00:00Z")),
      collection: "unknown"
    })
  })

  it("keeps metadata that isn't valid URI encoding as it is, rather than failing the read", () => {
    const artifact = fromHead(id, { Metadata: { name: "100% done.txt", "uploaded-at": "not a number" } })

    expect(artifact.name).toBe("100% done.txt")
    expect(DateTime.toEpochMillis(artifact.createdAt)).toBe(0)
  })
})
