// Paging through the index: what the end-to-end tests can't arrange, artifacts uploaded within the
// same millisecond, which a page boundary must neither skip nor repeat.
import { NodeServices } from "@effect/platform-node"
import { describe, expect, it } from "@effect/vitest"
import type { ArtifactId } from "@sloppa/api"
import { DateTime, Effect, Layer, Stream } from "effect"
import { mkdtempSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { AppConfig } from "../src/Config.ts"
import { ArtifactIndex } from "../src/Index.ts"
import { ArtifactStorage } from "../src/Storage.ts"
import type { StoredArtifact } from "../src/StorageShared.ts"

const unused = Effect.die("not used by the index")

const TestIndex = ArtifactIndex.layer.pipe(
  Layer.provide(
    Layer.succeed(
      ArtifactStorage,
      // Empty, so the index starts empty and holds only what the tests insert.
      ArtifactStorage.of({
        scan: Stream.empty,
        put: () => unused,
        head: () => unused,
        read: () => unused,
        remove: () => unused,
        setCollection: () => unused
      })
    )
  ),
  Layer.provide(
    Layer.succeed(
      AppConfig,
      AppConfig.of({
        host: "127.0.0.1",
        port: 0,
        publicUrl: "http://sloppa.test",
        bootstrapUser: undefined,
        trustProxy: false,
        staticDir: undefined,
        dataDir: mkdtempSync(join(tmpdir(), "sloppa-index-test-")),
        s3: undefined,
        preview: undefined
      })
    )
  ),
  Layer.provide(NodeServices.layer)
)

const artifact = (n: number, ms: number, collection = "o/r #1"): StoredArtifact => ({
  id: `00000000-0000-4000-8000-${String(n).padStart(12, "0")}` as ArtifactId,
  name: `${n}.txt`,
  contentType: "text/plain",
  size: 1,
  createdAt: DateTime.makeUnsafe(ms),
  collection
})

/** Every page of `collection`, `limit` at a time, as a client would walk them. */
const walk = (collection: string, limit: number) =>
  Effect.gen(function* () {
    const index = yield* ArtifactIndex
    const pages: Array<ReadonlyArray<string>> = []
    let cursor: string | undefined
    do {
      const page = yield* index.list({ collection, search: undefined, cursor, limit })
      pages.push(page.items.map((item) => item.name))
      cursor = page.nextCursor
    } while (cursor !== undefined)
    return pages
  })

describe("ArtifactIndex.list", () => {
  it.effect("pages through artifacts from the same millisecond once each, newest id first", () =>
    Effect.gen(function* () {
      const index = yield* ArtifactIndex
      for (const n of [1, 2, 3, 4, 5]) yield* index.insert(artifact(n, 1_000, "same ms"))

      const pages = yield* walk("same ms", 2)

      expect(pages).toEqual([["5.txt", "4.txt"], ["3.txt", "2.txt"], ["1.txt"]])
    }).pipe(Effect.provide(TestIndex))
  )

  it.effect("orders by time first, newest on top", () =>
    Effect.gen(function* () {
      const index = yield* ArtifactIndex
      yield* index.insert(artifact(9, 1_000, "by time"))
      yield* index.insert(artifact(1, 3_000, "by time"))
      yield* index.insert(artifact(5, 2_000, "by time"))

      const pages = yield* walk("by time", 10)

      expect(pages).toEqual([["1.txt", "5.txt", "9.txt"]])
    }).pipe(Effect.provide(TestIndex))
  )

  it.effect("gives no next cursor on a page that ends exactly at the last artifact", () =>
    Effect.gen(function* () {
      const index = yield* ArtifactIndex
      for (const n of [1, 2]) yield* index.insert(artifact(n, n * 1_000, "exact"))

      const pages = yield* walk("exact", 2)

      expect(pages).toEqual([["2.txt", "1.txt"]])
    }).pipe(Effect.provide(TestIndex))
  )
})
