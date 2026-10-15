import { useAtomSet } from "@effect/atom-react"
import { useState } from "react"
import { tokenAtom } from "../api.ts"
import { Button, Floppa } from "./ui.tsx"

export const Login = ({ invalid }: { invalid: boolean }) => {
  const setToken = useAtomSet(tokenAtom)
  const [value, setValue] = useState("")
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault()
        if (value.trim() !== "") setToken(value.trim())
      }}
      className="panel animate-pop-in mx-auto flex max-w-md flex-col items-center gap-4 px-6 py-8 text-center"
    >
      <Floppa mood={invalid ? "hiss" : "stare"} className="h-32 w-auto" />
      <div className="space-y-1">
        <p className="font-game text-2xl">{invalid ? "that token was rejected" : "token required"}</p>
        <p className="text-sm font-semibold text-ink/70">
          Enter the server's <code className="font-mono">UPLOAD_TOKEN</code> to browse. Shared links work without it.
        </p>
      </div>
      <input
        type="password"
        autoFocus
        value={value}
        onChange={(e) => setValue(e.target.value)}
        placeholder="token"
        aria-label="Token"
        className="panel game-field w-full bg-white px-4 py-2.5 font-semibold placeholder:text-ink/40"
      />
      <Button type="submit" tone="green" size="lg" className="w-full">
        sign in
      </Button>
    </form>
  )
}
