import { Popover } from "@base-ui/react/popover"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useState } from "react"
import { changePasswordMutation, logoutMutation, sessionQuery } from "../api.ts"
import type { SessionChangePasswordError } from "../client/types.gen.ts"
import { PASSWORD_MIN } from "../constants.ts"
import { apiError } from "../errors.ts"
import { Button, useToast } from "./ui.tsx"
import { SignOutIcon, UserCircleIcon } from "./Icon"

const FIELD = "panel game-field w-full bg-white px-2.5 py-1.5 text-sm font-bold placeholder:text-ink/40"

/** Why a change didn't go through, in the form's own words. */
const problemOf = (error: SessionChangePasswordError) => {
  const known = apiError(error)
  if (known?.code === "too_many_attempts") return `too many tries, wait ${known.retryAfter}s`
  return known?.code === "invalid_credentials" ? "that's not the current password" : "couldn't reach the server"
}

const PasswordForm = ({ username }: { username: string }) => {
  const change = useMutation(changePasswordMutation)
  const toast = useToast()
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const [again, setAgain] = useState("")
  const [problem, setProblem] = useState<string | null>(null)
  const ready = current !== "" && next.length >= PASSWORD_MIN && next === again
  return (
    <form
      className="space-y-2"
      onSubmit={(e) => {
        e.preventDefault()
        if (!ready) return
        change.mutate(
          { body: { current, next } },
          {
            onSettled: () => setCurrent(""),
            onError: (error) => setProblem(problemOf(error)),
            onSuccess: () => {
              setProblem(null)
              setNext("")
              setAgain("")
              toast("password changed, other browsers are signed out")
            }
          }
        )
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
        <Button type="submit" tone="green" size="sm" disabled={change.isPending || !ready}>
          {change.isPending ? "changing…" : "change"}
        </Button>
      </div>
    </form>
  )
}

/** Who is signed in, their password, and signing out. */
export const Account = () => {
  const session = useQuery(sessionQuery)
  const logout = useMutation(logoutMutation)
  const username = session.isSuccess ? session.data.username : ""
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
              <Button tone="red" size="sm" onClick={() => logout.mutate({})}>
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
