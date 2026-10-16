import { Console, Schema } from "effect"
import { CliError, Flag } from "effect/cli"

/** The server's own message for an API error, which says what to do about it. A message of ours stays as it is. */
export const apiError = (what: string) => (error: { readonly message: string }) =>
  error instanceof CliError.UserError
    ? error
    : new CliError.UserError({
        cause: error,
        userMessage:
          "_tag" in error && error._tag === "Unauthorized"
            ? `${what}: the server refused this agent token. Sign in again:\n  printf %s "$TOKEN" | sloppa login <url>`
            : `${what}: ${error.message}`
      })

export const userError = (userMessage: string) => new CliError.UserError({ cause: new Error(userMessage), userMessage })

export const json = Flag.Boolean("json").pipe(
  Flag.withDescription("Print JSON instead of text"),
  Flag.withDefault(false)
)

/** JSON as the API sends it, so it reads the same as `curl` would. */
export const printJson = <A, I>(schema: Schema.Codec<A, I>, value: A) =>
  Console.log(JSON.stringify(Schema.encodeSync(schema)(value), null, 2))

/** A word as a shell would need it typed, for the commands printed back to the caller. */
export const shellQuote = (word: string) =>
  /^[\w@%+=:,./-]+$/.test(word) ? word : `'${word.replaceAll("'", "'\\''")}'`
