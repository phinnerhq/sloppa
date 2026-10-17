import { Option, Schema } from "effect"
import { createHmac, randomBytes, timingSafeEqual } from "node:crypto"

/**
 * What a preview ticket lets `sloppa preview run` do: serve a dev server under `name ?? id` until
 * `exp`, reconnecting as often as it needs to. The sloppa server signs it and checks it offline
 * when the CLI connects, with `PREVIEW_SECRET`.
 */
export const TicketBody = Schema.Struct({
  /** 8 random base36 characters: the label unless `name` is set, and the lease's identity either way. */
  id: Schema.String,
  account: Schema.Int,
  name: Schema.optionalKey(Schema.String),
  /** See `passwordMac`: what Basic auth is checked against. */
  passwordMac: Schema.optionalKey(Schema.String),
  /** Expiry, epoch millis. */
  exp: Schema.Finite
})
export type TicketBody = typeof TicketBody.Type

const decodeBody = Schema.decodeUnknownOption(Schema.fromJsonString(TicketBody))

export const label = (ticket: TicketBody) => ticket.name ?? ticket.id

const BASE36 = "0123456789abcdefghijklmnopqrstuvwxyz"

/** 8 base36 characters, about 41 bits: unguessable at the proxy's rate limit, short enough to read out. */
export const previewId = () => {
  let id = ""
  while (id.length < 8) {
    // 252 is the largest multiple of 36 below 256: rejecting the rest keeps every character equally likely.
    for (const byte of randomBytes(16)) if (byte < 252 && id.length < 8) id += BASE36[byte % 36]
  }
  return id
}

export const isPreviewId = (id: string) => /^[a-z0-9]{8}$/.test(id)

/** One lowercase DNS label, the same rule as `PreviewName` in @sloppa/api. */
export const isPreviewName = (name: string) => /^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$/.test(name)

const mac = (secret: string, data: string) => createHmac("sha256", secret).update(data).digest()

/**
 * A preview's password, keyed with `PREVIEW_SECRET` and the preview's id: the server recomputes it
 * from Basic auth on each request, and a leaked ticket gives nothing to guess against offline.
 * The NUL separators keep it apart from ticket signatures, whose input is base64url.
 */
export const passwordMac = (secret: string, id: string, password: string) =>
  mac(secret, `sloppa-preview password\0${id}\0${password}`).toString("base64url")

export const checkPassword = (secret: string, ticket: TicketBody, password: string) => {
  if (ticket.passwordMac === undefined) return true
  const want = Buffer.from(ticket.passwordMac, "base64url")
  const got = Buffer.from(passwordMac(secret, ticket.id, password), "base64url")
  return want.length === got.length && timingSafeEqual(want, got)
}

/** `base64url(json).base64url(HMAC-SHA256)`. */
export const signTicket = (secret: string, body: TicketBody) => {
  const encoded = Buffer.from(JSON.stringify(body)).toString("base64url")
  return `${encoded}.${mac(secret, encoded).toString("base64url")}`
}

export type Verdict =
  | { readonly _tag: "Valid"; readonly ticket: TicketBody }
  | { readonly _tag: "Invalid"; readonly reason: string }

const invalid: Verdict = { _tag: "Invalid", reason: "invalid preview ticket" }

export const verifyTicket = (secret: string, token: string, now: number): Verdict => {
  const [encoded, signature, ...rest] = token.split(".")
  if (encoded === undefined || signature === undefined || rest.length > 0) return invalid
  const given = Buffer.from(signature, "base64url")
  const want = mac(secret, encoded)
  if (given.length !== want.length || !timingSafeEqual(given, want)) return invalid
  const body = decodeBody(Buffer.from(encoded, "base64url").toString("utf8"))
  if (Option.isNone(body)) return invalid
  const ticket = body.value
  // Signed by the server, but checked anyway: the label ends up in a hostname.
  if (!isPreviewId(ticket.id) || (ticket.name !== undefined && !isPreviewName(ticket.name))) return invalid
  if (now >= ticket.exp) return { _tag: "Invalid", reason: "expired preview ticket, ask for a new one" }
  return { _tag: "Valid", ticket }
}
