import { useAtomSet, useAtomValue } from "@effect/atom-react"
import { Popover } from "@base-ui/react/popover"
import { PASSWORD_MIN } from "@sloppa/api"
import { Exit, Option } from "effect"
import { AsyncResult } from "effect/reactivity"
import { useState } from "react"
import { changePasswordAtom, invalidateSession, logoutAtom, sessionAtom } from "../api.ts"
import { Button, useToast } from "./ui.tsx"
import { SignOutIcon, UserCircleIcon } from "./Icon"

const FIELD = "panel game-field w-full bg-white px-2.5 py-1.5 text-sm font-bold placeholder:text-ink/40"

/** Why a change didn't go through, in the form's own words. */
const problemOf = (error: { readonly _tag: string; readonly retryAfter?: number } | undefined) => {
  if (error?._tag === "too_many_attempts") return `too many tries, wait ${error.retryAfter ?? 60}s`
  return error?._tag === "invalid_credentials" ? "that's not the current password" : "couldn't reach the server"
}

const PasswordForm = ({ username }: { username: string }) => {
  const change = useAtomSet(changePasswordAtom, { mode: "promiseExit" })
  const toast = useToast()
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const [again, setAgain] = useState("")
  const [busy, setBusy] = useState(false)
  const [problem, setProblem] = useState<string | null>(null)
  const ready = current !== "" && next.length >= PASSWORD_MIN && next === again
  return (
    <form
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault()
        if (!ready) return
        setBusy(true)
        void change({ payload: { current, next } }).then((exit) => {
          setBusy(false)
          setCurrent("")
          if (Exit.isFailure(exit)) return setProblem(problemOf(Option.getOrUndefined(Exit.findErrorOption(exit))))
          setProblem(null)
          setNext("")
          setAgain("")
          toast("password changed, other browsers are signed out")
        })
      }}
    >
      <p className="font-game text-lg">change password</p>
      {/* For password managers, which save the new password under this name. */}
      <input type="text" autoComplete="username" value={username} readOnly hidden />
      <input
        type="password"
        autoComplete="current-password"
        value={current}
        onChange={(e) => setCurrent(e.target.value)}
        placeholder="current password"
        aria-label="Current password"
        className={FIELD}
      />
      <input
        type="password"
        autoComplete="new-password"
        value={next}
        onChange={(e) => setNext(e.target.value)}
        placeholder={`new password, ${PASSWORD_MIN}+ characters`}
        aria-label="New password"
        className={FIELD}
      />
      <input
        type="password"
        autoComplete="new-password"
        value={again}
        onChange={(e) => setAgain(e.target.value)}
        placeholder="new password again"
        aria-label="New password again"
        className={FIELD}
      />
      <div className="flex items-center justify-between gap-3">
        <span className="text-xs font-bold text-ink/60">
          {problem ?? (again !== "" && next !== again ? "the new passwords differ" : "")}
        </span>
        <Button type="submit" tone="green" size="sm" disabled={busy || !ready}>
          {busy ? "changing…" : "change"}
        </Button>
      </div>
    </form>
  )
}

/** Who is signed in, their password, and signing out. */
export const Account = () => {
  const session = useAtomValue(sessionAtom)
  const logout = useAtomSet(logoutAtom)
  const username = AsyncResult.isSuccess(session) ? session.value.username : ""
  return (
    <Popover.Root>
      <Popover.Trigger
        render={
          <Button size="icon" aria-label="Account" title="Account" className="[&>.pressable-face]:size-12.5">
            <UserCircleIcon size={22} />
          </Button>
        }
      />
      <Popover.Portal>
        <Popover.Positioner align="end" sideOffset={10} collisionPadding={12} className="z-40 outline-none">
          <Popover.Popup className="game-picker w-[min(20rem,calc(100vw-1.5rem))] space-y-4 p-4">
            <div className="flex items-center justify-between gap-3">
              <Popover.Title className="min-w-0 truncate font-game text-xl" title={username}>
                {username}
              </Popover.Title>
              <Button tone="red" size="sm" onClick={() => logout({ reactivityKeys: invalidateSession })}>
                <span className="flex items-center gap-1.5">
                  <SignOutIcon size={14} />
                  sign out
                </span>
              </Button>
            </div>
            <PasswordForm username={username} />
          </Popover.Popup>
        </Popover.Positioner>
      </Popover.Portal>
    </Popover.Root>
  )
}
