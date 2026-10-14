import { useAtomSet, useAtomValue } from "@effect/atom-react"
import { Atom } from "effect/reactivity"
import {
  type AnchorHTMLAttributes,
  type ButtonHTMLAttributes,
  type ReactNode,
  useCallback,
  useEffect,
  useState
} from "react"

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

const Toast = ({ text, onDone }: { text: string; onDone: () => void }) => {
  const [leaving, setLeaving] = useState(false)
  useEffect(() => {
    const leave = setTimeout(() => setLeaving(true), 1800)
    // Timed rather than on animationend, which never fires under reduced motion.
    const done = setTimeout(onDone, 1800 + 220)
    return () => {
      clearTimeout(leave)
      clearTimeout(done)
    }
  }, [onDone])
  return (
    <div
      className={`panel flex items-center gap-3 bg-gold px-4 py-2 font-game text-base ${
        leaving ? "animate-pop-out" : "animate-pop-in"
      }`}
    >
      <Floppa className="h-8 w-auto" flop="none" />
      {text}
    </div>
  )
}

export const Toaster = () => {
  const toast = useAtomValue(toastAtom)
  const set = useAtomSet(toastAtom)
  const clear = useCallback(() => set(null), [set])
  if (toast === null) return null
  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-6 z-50 flex justify-center px-4">
      <Toast key={toast.id} text={toast.text} onDone={clear} />
    </div>
  )
}

const TILE = 520
const viewport = () => ({ width: window.innerWidth, height: window.innerHeight })

/**
 * The background doodles, drifting one tile diagonally per loop.
 * Firefox only runs transform animations off the main thread for frames about the viewport's size,
 * so instead of one oversized layer, every tile is its own small layer sliding in lockstep.
 */
export const Doodles = () => {
  const [{ width, height }, setSize] = useState(viewport)
  useEffect(() => {
    const resize = () => setSize(viewport())
    window.addEventListener("resize", resize)
    return () => window.removeEventListener("resize", resize)
  }, [])
  // Quarter tiles on small screens, where a whole one would already be too large.
  const size = Math.min(width, height) >= TILE * 1.125 ? TILE : TILE / 2
  const columns = Math.ceil(width / size) + TILE / size
  const rows = Math.ceil(height / size) + TILE / size
  return (
    <div aria-hidden className="doodles">
      <div style={{ gridTemplateColumns: `repeat(${columns}, ${size}px)`, gridAutoRows: `${size}px` }}>
        {Array.from({ length: columns * rows }, (_, i) => (
          <div
            key={i}
            style={{
              backgroundPosition: `${-((i % columns) % 2) * size}px ${-(Math.floor(i / columns) % 2) * size}px`
            }}
          />
        ))}
      </div>
    </div>
  )
}

export type Tone = "green" | "blue" | "gold" | "red" | "cream"

type PressableProps = {
  tone?: Tone | undefined
  size?: "sm" | "md" | "lg" | "icon" | undefined
  className?: string | undefined
  children: ReactNode
}

const pressable = ({ tone = "cream", size = "md", className = "" }: Omit<PressableProps, "children">) =>
  `pressable game-btn tone-${tone} game-btn-${size} ${className}`

/** A game button: an ink wall with a face that lifts on hover and slams down on press. */
export const Button = ({
  tone,
  size,
  className,
  children,
  type = "button",
  ...props
}: PressableProps & Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className" | "children">) => (
  <button type={type} className={pressable({ tone, size, className })} {...props}>
    <span className="pressable-face">{children}</span>
  </button>
)

export const ButtonLink = ({
  tone,
  size,
  className,
  children,
  ...props
}: PressableProps & Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "className" | "children">) => (
  <a className={pressable({ tone, size, className })} {...props}>
    <span className="pressable-face">{children}</span>
  </a>
)

export const CopyButton = ({
  value,
  label,
  toast,
  tone
}: {
  value: string
  label: string
  toast: string
  tone?: Tone
}) => {
  const showToast = useToast()
  return (
    <Button tone={tone} onClick={() => void navigator.clipboard.writeText(value).then(() => showToast(toast))}>
      {label}
    </Button>
  )
}

export const LinkButton = ({
  href,
  children,
  download,
  tone
}: {
  href: string
  children: ReactNode
  download?: boolean
  tone?: Tone
}) => (
  <ButtonLink href={href} target={download ? undefined : "_blank"} rel="noreferrer" tone={tone}>
    {children}
  </ButtonLink>
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
