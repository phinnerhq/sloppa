import type { IncomingMessage } from "node:http"
import { isIP } from "node:net"

/** `::ffff:1.2.3.4`, as dual-stack sockets report IPv4 peers, is just 1.2.3.4. */
const unmap = (address: string) => {
  const mapped = /^::ffff:(\d+\.\d+\.\d+\.\d+)$/i.exec(address)
  return mapped === null ? address.toLowerCase() : mapped[1]!
}

/**
 * Who sent a request, as the app's own routes tell it: behind a reverse proxy (`TRUST_PROXY`), the
 * last `X-Forwarded-For` entry, the one the proxy added; the socket's peer otherwise. Undefined
 * when there's no telling.
 */
export const clientAddr = (request: IncomingMessage, trustProxy: boolean): string | undefined => {
  const header = request.headers["x-forwarded-for"]
  const forwarded = trustProxy
    ? (Array.isArray(header) ? header.join(",") : header)?.split(",").at(-1)?.trim()
    : undefined
  const address = forwarded !== undefined && isIP(unmap(forwarded)) !== 0 ? forwarded : request.socket.remoteAddress
  return address === undefined ? undefined : unmap(address)
}

/** The first four groups of an IPv6 address, written out in full. */
const prefix64 = (address: string) => {
  const [head = "", tail] = address.split("::")
  const left = head === "" ? [] : head.split(":")
  const right = tail === undefined || tail === "" ? [] : tail.split(":")
  const groups = [...left, ...Array<string>(8 - left.length - right.length).fill("0"), ...right]
  return `${groups
    .slice(0, 4)
    .map((group) => group.padStart(4, "0"))
    .join(":")}::/64`
}

/** IPv6 clients go by /64, since anyone with one has the whole block to rotate through. */
export const missKey = (address: string) => (isIP(address) === 6 ? prefix64(address) : address)

interface Bucket {
  tokens: number
  at: number
}

/**
 * Rate-limits failures per client: unknown hosts, wrong passwords and bad tickets each spend a
 * token, so guessing preview names or passwords gets slow. Successes cost nothing.
 */
export class Misses {
  readonly #buckets = new Map<string, Bucket>()
  #swept = 0
  readonly burst: number
  readonly everyMs: number
  readonly now: () => number

  constructor(burst: number, everyMs: number, now: () => number = Date.now) {
    this.burst = burst
    this.everyMs = everyMs
    this.now = now
  }

  #bucket(address: string) {
    const now = this.now()
    // A bucket refilled to its burst is the same as none: forget it, so memory follows recent misses.
    if (now - this.#swept > 60_000) {
      for (const [key, bucket] of this.#buckets) {
        if (now - bucket.at > this.everyMs * this.burst) this.#buckets.delete(key)
      }
      this.#swept = now
    }
    const key = missKey(address)
    const bucket = this.#buckets.get(key) ?? { tokens: this.burst, at: now }
    bucket.tokens = Math.min(this.burst, bucket.tokens + (now - bucket.at) / this.everyMs)
    bucket.at = now
    this.#buckets.set(key, bucket)
    return bucket
  }

  /** Whether the client spent its misses; checking doesn't spend one. */
  blocked(address: string | undefined) {
    return address !== undefined && this.#bucket(address).tokens < 1
  }

  miss(address: string | undefined) {
    if (address === undefined) return
    const bucket = this.#bucket(address)
    if (bucket.tokens >= 1) bucket.tokens -= 1
  }

  /** How long until a blocked client may miss again, in whole seconds. */
  get retryAfter() {
    return String(Math.max(1, Math.round(this.everyMs / 1000)))
  }
}

/** The Host header without its port, which plays no part in routing. */
export const hostOf = (host: string | undefined) =>
  (host ?? "")
    .replace(/:\d+$/, "")
    .replace(/^\[(.*)\]$/, "$1")
    .toLowerCase()
