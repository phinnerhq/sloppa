import { DateTime, Effect, Layer, Option } from "effect"
import { HttpRouter, HttpServerRequest, HttpServerResponse } from "effect/http"
import { AGENT_NAME_MAX } from "@sloppa/api"
import { Accounts } from "./Accounts.ts"

const NAME = "sloppa-artifacts"

/**
 * The whole setup, out in the open for the user to read before pasting it. The agent knows its own
 * app's MCP setup best; the link only answers with a token, which it never has to see.
 */
export const setupPrompt = (publicUrl: string, setupUrl: string) =>
  `Install Sloppa Artifacts for PR screenshots and recordings in your app.

1. Find your app's supported MCP configuration method.
   Use user scope for all projects, replacing any server named ${NAME}.
   Configure streamable HTTP at ${publicUrl}/mcp with "Authorization: Bearer <token>".

2. Keep tool output and chat free of the token, including configuration readbacks.
   With shell tracing disabled, run these three steps in one shell command, chained with &&:
   - capture the token:
     TOKEN=$(curl -fsS '${setupUrl}' --data-urlencode "agent=<app> on $(hostname)")
   - persist it for your app
   - check it:
     curl -fsS '${publicUrl}/api/session' -H "Authorization: Bearer $TOKEN"
   Replace <app> with only your app's name, such as Codex or Claude Code.
   For Codex, use --bearer-token-env-var and save that variable in $CODEX_HOME/.env (default ~/.codex/.env) for startup.

Setup is complete when the user-scope configuration is saved and the session check returns {"username":...}.
A 410 means the link expired or was revoked; ask me for a new one.

Then tell me how to reload your MCP servers, and ask whether to revoke this setup link, giving me:
curl -fsS -X DELETE '${setupUrl}'
Mention that it expires 30 minutes after creation and I can paste this prompt into other agents until then.`

const text = (body: string, status = 200) =>
  HttpServerResponse.text(body, { status, headers: { "cache-control": "no-store" } })

const expired = text("This setup link expired or was revoked: make a new one from the dashboard.\n", 410)

/**
 * `POST /setup/:code` trades a setup link for a fresh agent token, as many times as the link lives;
 * `DELETE` revokes it. Plain text, for curl.
 */
export const SetupRoute = Layer.effectDiscard(
  Effect.gen(function* () {
    const router = yield* HttpRouter.HttpRouter
    const accounts = yield* Accounts

    const code = Effect.map(HttpRouter.params, (params) => params["code"] ?? "")

    yield* router.add(
      "POST",
      "/setup/:code",
      Effect.gen(function* () {
        const request = yield* HttpServerRequest.HttpServerRequest
        const body = yield* request.urlParamsBody.pipe(Effect.option)
        const given = Option.getOrUndefined(body)
          ?.params.find(([key]) => key === "agent")?.[1]
          ?.trim()
          .slice(0, AGENT_NAME_MAX)
        const redeemed = yield* accounts.redeemSetup(
          yield* code,
          given === undefined || given === "" ? "an agent" : given
        )
        return Option.match(redeemed, { onNone: () => expired, onSome: ({ token }) => text(`${token}\n`) })
      })
    )

    // Opened in a browser (or unfurled in a chat): say what this is, but hand out nothing.
    yield* router.add(
      "GET",
      "/setup/:code",
      Effect.gen(function* () {
        const expiry = yield* accounts.setupExpiry(yield* code)
        if (Option.isNone(expiry)) return expired
        const minutes = Math.max(
          1,
          Math.round((DateTime.toEpochMillis(expiry.value) - DateTime.toEpochMillis(yield* DateTime.now)) / 60_000)
        )
        return text(
          `This Sloppa Artifacts setup link works for ${minutes} more minute(s). Opening it does nothing: paste the setup prompt from the dashboard into an agent, and its command trades this link for a token.\n`
        )
      })
    )

    yield* router.add(
      "DELETE",
      "/setup/:code",
      Effect.flatMap(code, accounts.revokeSetup).pipe(
        Effect.map((revoked) =>
          revoked
            ? text("Revoked: the setup link no longer works. Agents set up with it keep their tokens.\n")
            : text("This setup link was already expired or revoked.\n", 404)
        )
      )
    )
  })
)
