import { useAtomSet, useAtomValue } from "@effect/atom-react"
import { Exit, Option } from "effect"
import { AsyncResult } from "effect/reactivity"
import { useState } from "react"
import { invalidateSession, loginAtom, sessionAtom } from "../api.ts"
import { Button, Floppa } from "./ui.tsx"

/** True once the server confirms a session, and still while it re-checks one. */
export const useSignedIn = () => AsyncResult.isSuccess(useAtomValue(sessionAtom))

const FIELD = "panel game-field w-full bg-white px-4 py-2.5 font-semibold placeholder:text-ink/40"

type Status = "idle" | "busy" | "rejected" | "failed" | { readonly retryAfter: number }

const waitLabel = (seconds: number) => (seconds < 60 ? `${seconds}s` : `${Math.ceil(seconds / 60)} min`)

/** What a failed sign-in comes to: too many tries, a wrong password, or no answer at all. */
const failureOf = (error: { readonly _tag: string; readonly retryAfter?: number } | undefined): Status => {
  if (error?._tag === "TooManyAttempts" && error.retryAfter !== undefined) return { retryAfter: error.retryAfter }
  return error?._tag === "InvalidCredentials" ? "rejected" : "failed"
}

const HEADLINES = { rejected: "wrong username or password", failed: "couldn't reach the server" } as const

const headline = (status: Status, expired: boolean) => {
  if (typeof status === "object") return "too many tries"
  if (status === "rejected" || status === "failed") return HEADLINES[status]
  return expired ? "signed out" : "sign in"
}

/** `expired`: a session that was signed in until a moment ago. */
export const Login = ({ expired = false }: { expired?: boolean }) => {
  const login = useAtomSet(loginAtom, { mode: "promiseExit" })
  const [username, setUsername] = useState("")
  const [password, setPassword] = useState("")
  const [status, setStatus] = useState<Status>("idle")
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        if (username.trim() === "" || password === "") return
        setStatus("busy")
        void login({ payload: { username, password }, reactivityKeys: invalidateSession }).then((exit) => {
          if (Exit.isSuccess(exit)) return
          setStatus(failureOf(Option.getOrUndefined(Exit.findErrorOption(exit))))
          setPassword("")
        })
      }}
      className="panel animate-pop-in mx-auto flex max-w-md flex-col items-center gap-4 px-6 py-8 text-center"
    >
      {/* One portrait throughout: swapping moods on a wrong password flashed while the other loaded. */}
      <Floppa mood="hiss" className="h-32 w-auto" />
      <div className="space-y-1">
        <p className="font-game text-2xl">{headline(status, expired)}</p>
        <p className="text-sm font-semibold text-ink/70">
          {typeof status === "object"
            ? `wait ${waitLabel(status.retryAfter)} before trying again.`
            : "Shared links work without an account."}
        </p>
      </div>
      <input
        autoFocus
        autoComplete="username"
        value={username}
        onChange={(e) => setUsername(e.target.value)}
        placeholder="username"
        aria-label="Username"
        className={FIELD}
      />
      <input
        type="password"
        autoComplete="current-password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        placeholder="password"
        aria-label="Password"
        className={FIELD}
      />
      <Button type="submit" tone="green" size="lg" className="w-full" disabled={status === "busy"}>
        {status === "busy" ? "signing in…" : "sign in"}
      </Button>
    </form>
  )
}
