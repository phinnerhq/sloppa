import { Effect, Redacted } from "effect"
import { argon2, randomBytes, timingSafeEqual } from "node:crypto"

/**
 * argon2id in the PHC string format, `$argon2id$v=19$m=65536,t=2,p=1$<salt>$<hash>`: the same
 * parameters and encoding as `Bun.password`, which hashed the passwords stored before, so those
 * still verify.
 */
const DEFAULTS = { memory: 65536, passes: 2, parallelism: 1 }
const SALT_BYTES = 32
const TAG_BYTES = 32

const PHC = /^\$(argon2id|argon2i|argon2d)\$v=19\$m=(\d+),t=(\d+),p=(\d+)\$([A-Za-z0-9+/]+)\$([A-Za-z0-9+/]+)$/

const derive = (
  algorithm: "argon2id" | "argon2i" | "argon2d",
  password: string,
  nonce: Buffer,
  params: { memory: number; passes: number; parallelism: number },
  tagLength: number
) =>
  Effect.callback<Buffer, Error>((resume) =>
    argon2(algorithm, { message: password, nonce, tagLength, ...params }, (error, tag) =>
      resume(error ? Effect.fail(error) : Effect.succeed(tag))
    )
  )

const unpadded = (bytes: Buffer) => bytes.toString("base64").replace(/=+$/, "")

export const hashPassword = (password: Redacted.Redacted<string>) =>
  Effect.gen(function* () {
    const salt = randomBytes(SALT_BYTES)
    const tag = yield* derive("argon2id", Redacted.value(password), salt, DEFAULTS, TAG_BYTES)
    const { memory, passes, parallelism } = DEFAULTS
    return `$argon2id$v=19$m=${memory},t=${passes},p=${parallelism}$${unpadded(salt)}$${unpadded(tag)}`
  }).pipe(Effect.orDie)

/** False for a wrong password and for a hash it can't read alike. */
export const verifyPassword = (password: string, hash: string) =>
  Effect.gen(function* () {
    const match = PHC.exec(hash)
    if (match === null) return false
    const [, algorithm, memory, passes, parallelism, salt, expected] = match
    const want = Buffer.from(expected!, "base64")
    const tag = yield* derive(
      algorithm as "argon2id",
      password,
      Buffer.from(salt!, "base64"),
      { memory: Number(memory), passes: Number(passes), parallelism: Number(parallelism) },
      want.length
    )
    return timingSafeEqual(tag, want)
  }).pipe(Effect.orElseSucceed(() => false))
