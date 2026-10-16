import { Schema } from "effect"

/** Versions the tunnel, so an old CLI gets a clear refusal instead of garbage. */
export const SUBPROTOCOL = "sloppa-preview.v1"

/**
 * Close codes on the control socket. 4000-4099 are final: the CLI stops its dev server. Anything
 * else (a network blip, the server restarting with 1001) is worth a reconnect.
 */
export const Close = {
  Protocol: 4000,
  Ticket: 4001,
  Taken: 4002,
  Stopped: 4003,
  Expired: 4004,
  Replaced: 4005
} as const

export const FINAL: Readonly<Record<number, string>> = {
  [Close.Protocol]: "protocol error",
  [Close.Ticket]: "ticket rejected",
  [Close.Taken]: "name taken",
  [Close.Stopped]: "stopped",
  [Close.Expired]: "expired",
  [Close.Replaced]: "replaced by another connection"
}

/** The CLI's first message: where its dev server listens, and what it is, for listings. */
export const Hello = Schema.Struct({
  type: Schema.Literal("hello"),
  port: Schema.Int.check(Schema.isBetween({ minimum: 1, maximum: 65535 })),
  command: Schema.optionalKey(Schema.String),
  cwd: Schema.optionalKey(Schema.String),
  hostname: Schema.optionalKey(Schema.String)
})
export type Hello = typeof Hello.Type

/** The server to the CLI. `open` asks for a data socket at `/_data/<conn>`; `ping` wants a `pong`. */
export const ServerMessage = Schema.Union([
  Schema.Struct({ type: Schema.Literal("ready"), id: Schema.String, url: Schema.String, expiresAt: Schema.String }),
  Schema.Struct({ type: Schema.Literal("open"), conn: Schema.String }),
  Schema.Struct({ type: Schema.Literals(["ping", "pong"]) })
])
export type ServerMessage = typeof ServerMessage.Type

/** The CLI to the server, after its hello. `refuse` is the answer to an `open` it can't serve. */
export const ClientMessage = Schema.Union([
  Schema.Struct({ type: Schema.Literal("refuse"), conn: Schema.String, reason: Schema.optionalKey(Schema.String) }),
  Schema.Struct({ type: Schema.Literals(["ping", "pong"]) })
])
export type ClientMessage = typeof ClientMessage.Type
