import { useAtomRefresh, useAtomSet, useAtomValue } from "@effect/atom-react"
import { PlayIcon } from "@phosphor-icons/react"
import type { Artifact } from "@sloppa/api"
import { Predicate } from "effect"
import { AsyncResult, type Atom } from "effect/reactivity"
import { AnimatePresence, motion } from "motion/react"
import { memo, type ReactNode, useEffect, useEffectEvent, useRef, useState } from "react"
import { colorFor, formatAgoShort, formatSize, kindOf, prLabel } from "../format.ts"
import { onLinkClick } from "../router.ts"
import { Login } from "./Login.tsx"
import { extensionOf, Notice } from "./ui.tsx"

/**
 * Ref for an `.offscreen-skip` card: lets it skip rendering while out of sight, two frames after it
 * mounts, once it has rendered for real and the browser remembers its size.
 */
export const skipOnceRendered = (element: HTMLElement | null) => {
  if (element === null || "skip" in element.dataset) return
  requestAnimationFrame(() => requestAnimationFrame(() => (element.dataset.skip = "")))
}

export const ItemCount = ({ count }: { count: number }) => (
  <span className="rounded-md border-2 border-ink bg-gold px-2 font-game text-xs">
    {count.toLocaleString("en")} item{count === 1 ? "" : "s"}
  </span>
)

/** False until the element comes within a screen of the viewport, true from then on. */
const useNearViewport = () => {
  const ref = useRef<HTMLDivElement>(null)
  const [near, setNear] = useState(false)
  useEffect(() => {
    const element = ref.current
    if (element === null || near) return
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) setNear(true)
      },
      { rootMargin: "100% 0px" }
    )
    observer.observe(element)
    return () => observer.disconnect()
  }, [near])
  return [ref, near] as const
}

/** Even `preload="metadata"` fetches part of every video, so they only mount once they're close. */
const VideoThumbnail = ({ artifact }: { artifact: Artifact }) => {
  const [ref, near] = useNearViewport()
  return (
    <div ref={ref} className="flex size-full items-center justify-center">
      {near ? (
        <video src={`${artifact.rawUrl}#t=0.1`} preload="metadata" muted className="size-full object-contain" />
      ) : (
        <PlayIcon size={32} weight="fill" className="text-ink/60" />
      )}
    </div>
  )
}

export const Thumbnail = ({ artifact }: { artifact: Artifact }) => {
  switch (kindOf(artifact)) {
    case "image":
      return (
        <img
          src={artifact.thumbUrl ?? artifact.rawUrl}
          alt=""
          loading="lazy"
          decoding="async"
          className="size-full object-contain"
        />
      )
    case "video":
      return <VideoThumbnail artifact={artifact} />
    default:
      return <span className="font-game text-xl text-ink/80">{extensionOf(artifact.name)}</span>
  }
}

/** An artifact's framed picture: its thumbnail, or its extension on a colour of its own. */
export const Preview = ({ artifact, className = "" }: { artifact: Artifact; className?: string }) => {
  const media = kindOf(artifact) === "image" || kindOf(artifact) === "video"
  return (
    <div
      className={`flex items-center justify-center overflow-hidden rounded-lg border-[3px] border-ink ${
        media ? "checkerboard" : ""
      } ${className}`}
      style={media ? undefined : { backgroundColor: colorFor(extensionOf(artifact.name)) }}
    >
      <Thumbnail artifact={artifact} />
    </div>
  )
}

/** Memoized: loading a page re-renders the inventory, not every card already in it. */
const Slot = memo(function Slot({ artifact }: { artifact: Artifact }) {
  const path = `/a/${artifact.id}`
  const ago = formatAgoShort(artifact.createdAt)
  return (
    <a href={path} onClick={onLinkClick(path)} className="pressable pressable-card block">
      <div className="pressable-face p-2">
        <Preview artifact={artifact} className="aspect-square" />
        <p className="mt-1.5 truncate px-0.5 text-sm font-extrabold" title={artifact.name}>
          {artifact.name}
        </p>
        <p className="truncate px-0.5 text-[11px] font-bold text-ink/55">
          {formatSize(artifact.size)} · {ago === "now" ? "just now" : `${ago} ago`}
        </p>
      </div>
    </a>
  )
})

const GRID = "grid grid-cols-2 gap-3 sm:grid-cols-4 lg:grid-cols-6"

/** A row of empty slots, sized like the real ones. */
export const SlotsSkeleton = () => (
  <div className={GRID}>
    {Array.from({ length: 6 }, (_, i) => (
      <div key={i} className="panel mt-1 p-2">
        <div className="skeleton aspect-square rounded-lg" />
        <div className="skeleton mt-2.5 h-3.5 w-3/4" />
        <div className="skeleton mt-2 mb-0.5 h-2.5 w-1/2" />
      </div>
    ))}
  </div>
)

/** A first load: one group's worth of empty slots. */
export const InventorySkeleton = () => (
  <section aria-busy aria-label="Loading artifacts" className="space-y-3">
    <div className="skeleton h-7 w-56 rounded-lg" />
    <SlotsSkeleton />
  </section>
)

/**
 * Infinite scroll: the next page loads as this placeholder row comes within a screen of the viewport.
 * The observer starts over after each page, so a page too short to push the row away pulls another.
 */
const NextPage = ({ paused, onLoad, children }: { paused: boolean; onLoad: () => void; children: ReactNode }) => {
  const ref = useRef<HTMLDivElement>(null)
  const load = useEffectEvent(onLoad)
  useEffect(() => {
    const element = ref.current
    if (element === null || paused) return
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) load()
      },
      { rootMargin: "0px 0px 100% 0px" }
    )
    observer.observe(element)
    return () => observer.disconnect()
  }, [paused])
  return (
    <div ref={ref} aria-busy aria-label="Loading more">
      {children}
    </div>
  )
}

/**
 * Cached data shows instantly when an atom is picked back up; refetch it in the background, once per
 * pick-up. `stale` can turn that down for a result that would lose something by refetching.
 */
export const useRevalidate = <A,>(
  atom: Atom.Atom<AsyncResult.AsyncResult<A, unknown>>,
  stale: (value: A) => boolean = () => true
) => {
  const result = useAtomValue(atom)
  const refresh = useAtomRefresh(atom)
  const checked = useRef<unknown>(null)
  useEffect(() => {
    if (checked.current === atom) return
    checked.current = atom
    if (AsyncResult.isSuccess(result) && !result.waiting && stale(result.value)) refresh()
  }, [atom, result, refresh, stale])
}

/**
 * Refetching starts a paged list over from its first page: one scrolled further keeps what it has
 * (at most `LIST_CACHE` old) rather than collapsing under the reader and their restored scroll.
 */
const onFirstPage = (value: { readonly items: ReadonlyArray<unknown> }) => value.items.length <= 1

/**
 * Cards and groups pop in with a little bounce and shrink away; whatever stays glides to its new spot.
 * The glide doesn't bounce: a spring's overshoot grows with the distance, and a group jumping from the
 * bottom of the list to the top would fly past its spot and over the filters.
 */
export const pop = {
  layout: "position",
  initial: { opacity: 0, scale: 0.85 },
  animate: { opacity: 1, scale: 1 },
  exit: { opacity: 0, scale: 0.85, transition: { duration: 0.15, ease: "easeIn" } },
  transition: {
    type: "spring",
    bounce: 0.35,
    visualDuration: 0.35,
    layout: { type: "spring", bounce: 0, visualDuration: 0.4 }
  }
} as const

interface Run {
  /** The run's first artifact: later pages only ever extend a run at its end, so this stays put. */
  readonly key: string
  readonly pr: string | undefined
  readonly items: Array<Artifact>
}

/**
 * Consecutive uploads to the same PR, newest first. Grouping across the whole list instead would pull
 * a PR's older uploads up into its first group whenever a page loads, reshuffling what's already on screen.
 */
const runsByPr = (artifacts: ReadonlyArray<Artifact>) => {
  const runs: Array<Run> = []
  for (const artifact of artifacts) {
    const last = runs.at(-1)
    if (last !== undefined && last.pr === artifact.pr) last.items.push(artifact)
    else runs.push({ key: artifact.id, pr: artifact.pr, items: [artifact] })
  }
  return runs
}

/** A PR's label, linking to it. */
export const PrTitle = ({ pr }: { pr: string | undefined }) =>
  pr === undefined ? (
    <span className="game-title text-xl">no PR</span>
  ) : (
    <a href={pr} target="_blank" rel="noreferrer" className="game-title text-xl transition-colors hover:text-sky">
      {prLabel(pr)}
    </a>
  )

/** Cards grouped by PR, each under its heading unless the page already has one. */
export const Inventory = ({
  artifacts,
  headings = true
}: {
  artifacts: ReadonlyArray<Artifact>
  headings?: boolean
}) => (
  <div className="relative flex flex-col gap-8">
    <AnimatePresence mode="popLayout" initial={false}>
      {runsByPr(artifacts).map(({ key, pr, items }) => (
        <motion.section key={key} {...pop} className="space-y-3">
          {headings && (
            <h2 className="flex flex-wrap items-center gap-2">
              <PrTitle pr={pr} />
              <ItemCount count={items.length} />
            </h2>
          )}
          <div className={`${GRID} relative`}>
            <AnimatePresence mode="popLayout" initial={false}>
              {items.map((artifact) => (
                <motion.div key={artifact.id} ref={skipOnceRendered} {...pop} className="offscreen-skip">
                  <Slot artifact={artifact} />
                </motion.div>
              ))}
            </AnimatePresence>
          </div>
        </motion.section>
      ))}
    </AnimatePresence>
  </div>
)

interface Page {
  readonly items: ReadonlyArray<unknown>
  readonly nextCursor?: string
}

/**
 * A paged list on screen: a skeleton on the first load, errors as notices, and the next page pulled as
 * `more` (a placeholder row) scrolls near. A filter that's never been loaded keeps the previous results
 * on screen, dimmed, until its own land: the cards then animate from one set to the other instead of
 * flashing through a skeleton.
 */
export const Paged = <P extends Page>({
  atom,
  skeleton,
  more,
  empty,
  children
}: {
  atom: Atom.Writable<Atom.PullResult<P, unknown>, void>
  skeleton: ReactNode
  more: ReactNode
  /** Shown when there's nothing at all. */
  empty: ReactNode
  children: (pages: ReadonlyArray<P>) => ReactNode
}) => {
  const result = useAtomValue(atom)
  const loadMore = useAtomSet(atom)
  useRevalidate(atom, onFirstPage)
  const [last, setLast] = useState<{ readonly items: ReadonlyArray<P> } | null>(null)
  if (AsyncResult.isSuccess(result) && result.value !== last) setLast(result.value)
  const shown = AsyncResult.isInitial(result) && last !== null ? AsyncResult.success(last, { waiting: true }) : result
  const stale = shown !== result
  return AsyncResult.matchWithError(shown, {
    onInitial: () => skeleton,
    onError: (error) =>
      Predicate.isTagged(error, "Unauthorized") ? (
        <Login invalid />
      ) : (
        <Notice title="couldn't load artifacts" mood="hiss">
          is the server and its bucket up?
        </Notice>
      ),
    onDefect: () => (
      <Notice title="couldn't load artifacts" mood="hiss">
        is the server and its bucket up?
      </Notice>
    ),
    onSuccess: ({ value, waiting }) => (
      <div aria-busy={stale} className={`space-y-8 transition-opacity duration-200 ${stale ? "opacity-60" : ""}`}>
        {value.items.every((page) => page.items.length === 0) ? empty : children(value.items)}
        {value.items.at(-1)?.nextCursor !== undefined && (
          <NextPage paused={waiting || stale} onLoad={() => loadMore()}>
            {more}
          </NextPage>
        )}
      </div>
    )
  })
}
