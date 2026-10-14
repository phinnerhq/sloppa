import { useAtomSet, useAtomValue } from "@effect/atom-react"
import { Atom } from "effect/reactivity"
import { type ReactNode, useEffect, useState } from "react"

export type Mood = "stare" | "hiss" | "loaf"

/** The mascot: real caracals, cut out and stickered (see scripts/mascot). */
export const Floppa = ({
  mood = "stare",
  className = "",
  flop = "hover"
}: {
  mood?: Mood
  className?: string
  flop?: "hover" | "loop" | "none"
}) => {
  const [flopping, setFlopping] = useState(false)
  return (
    <img
      src={`/floppa/${mood}.webp`}
      alt=""
      draggable={false}
      onMouseEnter={() => flop === "hover" && setFlopping(true)}
      onAnimationEnd={() => setFlopping(false)}
      className={`select-none ${flop === "loop" ? "animate-flop-loop" : flopping ? "animate-flop" : ""} ${className}`}
    />
  )
}

const toastAtom = Atom.make<{ readonly id: number; readonly text: string } | null>(null).pipe(Atom.keepAlive)

export const useToast = () => {
  const set = useAtomSet(toastAtom)
  return (text: string) => set({ id: Date.now(), text })
}

export const Toaster = () => {
  const toast = useAtomValue(toastAtom)
  const set = useAtomSet(toastAtom)
  useEffect(() => {
    if (toast === null) return
    const timer = setTimeout(() => set(null), 1800)
    return () => clearTimeout(timer)
  }, [toast, set])
  if (toast === null) return null
  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-6 z-50 flex justify-center px-4">
      <div
        key={toast.id}
        className="panel animate-pop-in flex items-center gap-3 bg-gold px-4 py-2 font-game text-base"
      >
        <Floppa className="h-8 w-auto" flop="none" />
        {toast.text}
      </div>
    </div>
  )
}

export const CopyButton = ({
  value,
  label,
  toast,
  color = "cream"
}: {
  value: string
  label: string
  toast: string
  color?: "green" | "blue" | "cream"
}) => {
  const showToast = useToast()
  return (
    <button
      type="button"
      onClick={() => void navigator.clipboard.writeText(value).then(() => showToast(toast))}
      className={`game-btn game-btn-${color}`}
    >
      {label}
    </button>
  )
}

export const LinkButton = ({
  href,
  children,
  download,
  color = "cream"
}: {
  href: string
  children: ReactNode
  download?: boolean
  color?: "gold" | "cream"
}) => (
  <a href={href} target={download ? undefined : "_blank"} rel="noreferrer" className={`game-btn game-btn-${color}`}>
    {children}
  </a>
)

export const Loading = ({ label = "loading…" }: { label?: string }) => (
  <div className="flex flex-col items-center gap-3 py-16">
    <Floppa className="h-24 w-auto" flop="loop" />
    <p className="game-title text-xl">{label}</p>
  </div>
)

export const Notice = ({ title, mood = "stare", children }: { title: string; mood?: Mood; children?: ReactNode }) => (
  <div className="panel animate-pop-in mx-auto flex max-w-xl flex-col items-center gap-3 px-6 py-8 text-center">
    <Floppa mood={mood} className={mood === "loaf" ? "h-28 w-auto" : "h-32 w-auto"} />
    <p className="font-game text-2xl">{title}</p>
    {children && <div className="text-sm font-semibold text-ink/70">{children}</div>}
  </div>
)

export const extensionOf = (name: string) => {
  const dot = name.lastIndexOf(".")
  return dot > 0 && dot < name.length - 1
    ? name
        .slice(dot + 1)
        .toUpperCase()
        .slice(0, 5)
    : "FILE"
}
