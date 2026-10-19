// Telling the server's error bodies apart from requests that never got one.
import { describe, expect, it } from "vitest"
import { apiError } from "../src/errors.ts"

describe("apiError", () => {
  it("reads a body the server sent", () => {
    const error = { code: "too_many_attempts", message: "slow down", retryAfter: 3 } as const
    expect(apiError(error)).toBe(error)
  })

  it("has nothing for a failed fetch, an abort, or a body that wasn't JSON", () => {
    expect(apiError<{ code: string }>(new TypeError("Failed to fetch") as never)).toBeUndefined()
    expect(apiError<{ code: string }>(new DOMException("aborted", "AbortError") as never)).toBeUndefined()
    expect(apiError<{ code: string }>("Bad Gateway" as never)).toBeUndefined()
    expect(apiError<{ code: string }>({} as never)).toBeUndefined()
    expect(apiError(null)).toBeUndefined()
  })
})
