// What the preview proxy changes on the way through, and who it counts guesses against. The
// end-to-end suites in apps/cli/test exercise both over real sockets; these pin the edge cases.
import { describe, expect, it } from "vitest"
import type { IncomingMessage } from "node:http"
import { clientAddr, Misses, missKey } from "../src/preview/Guard.ts"
import { rewriteLocation, scopeCookie } from "../src/preview/Proxy.ts"

const preview = new URL("https://demo.preview.test/")

describe("rewriteLocation", () => {
  it("points the dev server's own address back at the preview", () => {
    expect(rewriteLocation("http://127.0.0.1:5173/landed?x=1", 5173, preview)).toBe(
      "https://demo.preview.test/landed?x=1"
    )
    expect(rewriteLocation("http://localhost:5173/", 5173, preview)).toBe("https://demo.preview.test/")
    expect(rewriteLocation("http://[::1]:5173/a", 5173, preview)).toBe("https://demo.preview.test/a")
  })

  it("leaves everything else alone", () => {
    expect(rewriteLocation("/relative", 5173, preview)).toBe("/relative")
    expect(rewriteLocation("http://127.0.0.1:9999/", 5173, preview)).toBe("http://127.0.0.1:9999/")
    expect(rewriteLocation("https://example.com:5173/", 5173, preview)).toBe("https://example.com:5173/")
  })
})

describe("scopeCookie", () => {
  it("drops a Domain wider than the preview, whatever its case or dot", () => {
    expect(scopeCookie("a=1; Domain=.preview.test; Path=/", "demo.preview.test")).toBe("a=1; Path=/")
    expect(scopeCookie("b=2; domain=localhost; HttpOnly", "demo.preview.test")).toBe("b=2; HttpOnly")
  })

  it("keeps a Domain that is the preview's own host, and cookies without one", () => {
    expect(scopeCookie("c=3; Domain=Demo.Preview.Test", "demo.preview.test")).toBe("c=3; Domain=Demo.Preview.Test")
    expect(scopeCookie("d=4; Path=/; Secure", "demo.preview.test")).toBe("d=4; Path=/; Secure")
  })
})

/** Just what clientAddr reads off a request. */
const request = (remoteAddress: string, forwarded?: string) =>
  ({
    socket: { remoteAddress },
    headers: forwarded === undefined ? {} : { "x-forwarded-for": forwarded }
  }) as unknown as IncomingMessage

describe("clientAddr", () => {
  it("is the socket's peer, unless behind a trusted proxy", () => {
    expect(clientAddr(request("203.0.113.9", "198.51.100.1"), false)).toBe("203.0.113.9")
    expect(clientAddr(request("::ffff:203.0.113.9"), false)).toBe("203.0.113.9")
  })

  it("believes only the entry the proxy added, the last one", () => {
    expect(clientAddr(request("10.0.0.2", "1.1.1.1, 198.51.100.7"), true)).toBe("198.51.100.7")
    expect(clientAddr(request("10.0.0.2", "not an address"), true)).toBe("10.0.0.2")
  })
})

describe("Misses", () => {
  it("groups IPv6 clients by /64, written out in full", () => {
    expect(missKey("2001:db8::1")).toBe("2001:0db8:0000:0000::/64")
    expect(missKey("2001:db8:0:0:ffff::2")).toBe(missKey("2001:db8::9"))
    expect(missKey("203.0.113.9")).toBe("203.0.113.9")
  })

  it("blocks a client that spent its burst, and refills it over time", () => {
    let now = 0
    const misses = new Misses(2, 1000, () => now)
    misses.miss("2001:db8::1")
    misses.miss("2001:db8::2")
    expect(misses.blocked("2001:db8::3")).toBe(true)
    expect(misses.blocked("2001:db9::1")).toBe(false)
    now = 1000
    expect(misses.blocked("2001:db8::3")).toBe(false)
  })
})
