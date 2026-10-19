import { type AnchorHTMLAttributes, type ButtonHTMLAttributes, type ReactNode, useEffect, useState } from "react"
import { OverlayScrollbars, type PartialOptions } from "overlayscrollbars"
import { shared, useShared } from "../store.ts"

/** Overlay scrollbars, styled as `os-theme-sloppa`: they float over content, so nothing shifts when one appears. */
export const scrollbars = (
  autoHide: "move" | "never",
  theme: "os-theme-sloppa" | "os-theme-sloppa-cream" = "os-theme-sloppa"
): PartialOptions => ({
  scrollbars: { theme, autoHide, autoHideDelay: 900, clickScroll: true }
})

/**
 * Ref for an element that scrolls inside a frame, its parent: the frame carries an overlay scrollbar,
 * and the element stays the scroller, so code that scrolls it keeps working, and just loses its native bar.
 */
export const overlayScrollbarWith = (options: PartialOptions) => (scroller: HTMLElement | null) => {
  const frame = scroller?.parentElement
  if (scroller == null || frame == null) return
  const instance = OverlayScrollbars({ target: frame, elements: { viewport: scroller } }, options)
  return () => instance.destroy()
}

/** For a scrolling list inside a popup, which Base UI scrolls to keep the highlighted option in view. */
export const overlayScrollbar = overlayScrollbarWith(scrollbars("never"))

export type Mood = "stare" | "hiss" | "loaf"

/** The mascot: real caracals, cut out and stickered (see scripts/mascot). */
/** The hover tricks: a sideways flop, a squash-and-stretch hop, and a wound-up spin that bounces to rest. */
const TRICKS = ["animate-flop", "animate-squish", "animate-spin-flop"] as const
type Trick = (typeof TRICKS)[number]

/** A random trick, never the same one twice in a row. */
const nextTrick = (last: Trick | null): Trick => {
  const pool = TRICKS.filter((trick) => trick !== last)
  return pool[Math.floor(Math.random() * pool.length)]!
}

export const Floppa = ({
  mood = "stare",
  className = "",
  flop = "hover"
}: {
  mood?: Mood
  className?: string
  flop?: "hover" | "loop" | "none"
}) => {
  const [trick, setTrick] = useState<{ readonly current: Trick | null; readonly last: Trick | null }>({
    current: null,
    last: null
  })
  return (
    <img
      src={`/floppa/${mood}.webp`}
      alt=""
      draggable={false}
      onMouseEnter={() =>
        flop === "hover" && trick.current === null && setTrick({ current: nextTrick(trick.last), last: trick.last })
      }
      onAnimationEnd={() => setTrick({ current: null, last: trick.current })}
      className={`select-none ${flop === "loop" ? "animate-flop-loop" : (trick.current ?? "")} ${className}`}
    />
  )
}

/** Re-renders every `ms`, for countdowns and "used 2 minutes ago". */
export const useNow = (ms: number) => {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), ms)
    return () => clearInterval(timer)
  }, [ms])
  return now
}

const toastShown = shared<{ readonly id: number; readonly text: string } | null>(null)
const clearToast = () => toastShown.set(null)

export const useToast = () => (text: string) => toastShown.set({ id: Date.now(), text })

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
  const toast = useShared(toastShown)
  if (toast === null) return null
  return (
    <div className="pointer-events-none fixed inset-x-0 bottom-6 z-50 flex justify-center px-4">
      <Toast key={toast.id} text={toast.text} onDone={clearToast} />
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
  icon,
  toast,
  tone,
  size
}: {
  value: string
  label: string
  /** Shown before the label. */
  icon?: ReactNode
  toast: string
  tone?: Tone
  size?: PressableProps["size"]
}) => {
  const showToast = useToast()
  return (
    <Button
      tone={tone}
      size={size}
      onClick={() => void navigator.clipboard.writeText(value).then(() => showToast(toast))}
    >
      {icon}
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

/** `bare` drops the card, for notices shown inside a panel that's already one. */
export const Notice = ({
  title,
  mood = "stare",
  bare = false,
  children
}: {
  title: string
  mood?: Mood
  bare?: boolean
  children?: ReactNode
}) => (
  <div
    className={`${bare ? "" : "panel animate-pop-in"} mx-auto flex max-w-xl flex-col items-center gap-3 px-6 py-8 text-center`}
  >
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

/**
 * A collection's name on one line. One ending in a number, like `owner/repo #42`, never loses it: the
 * rest truncates from its start ("…hq/phinner.dev"), so the owner goes first and the end of the repo
 * name, the most telling part, stays longest. Other names truncate as they are.
 */
export const CollectionName = ({
  collection,
  className = "",
  numberClassName = "ml-1"
}: {
  collection: string
  className?: string
  numberClassName?: string
}) => {
  const match = /^(.+?) ?(#\d+)$/.exec(collection)
  if (match === null) return <span className={`truncate ${className}`}>{collection}</span>
  const [, repo, number] = match
  return (
    <span className={`flex min-w-0 items-baseline ${className}`}>
      {/* Right-to-left puts the ellipsis at the start; the left-to-right isolate keeps the text in order. */}
      <span dir="rtl" className="min-w-0 truncate text-left">
        <bdi dir="ltr">{repo}</bdi>
      </span>
      <span className={`shrink-0 ${numberClassName}`}>{number}</span>
    </span>
  )
}
