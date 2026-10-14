import { useAtomRefresh, useAtomSet, useAtomValue } from "@effect/atom-react"
import { Select as BaseSelect } from "@base-ui/react/select"
import { AnimatePresence, motion } from "motion/react"
import { CaretDownIcon, CheckIcon } from "@phosphor-icons/react"
import type { Artifact, Stats } from "@sloppa/api"
import { Predicate } from "effect"
import { AsyncResult, type Atom } from "effect/reactivity"
import { type CSSProperties, type ReactNode, useEffect, useMemo, useRef, useState } from "react"
import { artifactPagesAtom, type ListFilter, prsAtom, reposAtom, statsAtom, tokenAtom } from "../api.ts"
import { colorFor, formatAgo, formatAgoShort, formatSize, kindOf, prLabel } from "../format.ts"
import { onLinkClick, useQueryParam } from "../router.ts"
import { Button, CopyButton, extensionOf, Floppa, Notice } from "./ui.tsx"

const Login = ({ invalid }: { invalid: boolean }) => {
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

/** Two wave layers drifting against each other, offset per card so they never slosh in sync. */
const Liquid = ({ color, delay }: { color: string; delay: number }) => (
  <div aria-hidden className="absolute inset-x-0 bottom-0 h-1/2" style={{ color }}>
    <Wave
      className="opacity-35"
      style={{ animationDuration: "9s", animationDirection: "reverse", animationDelay: `${delay * 2}s` }}
    />
    <Wave className="opacity-60" style={{ animationDelay: `${delay}s` }} />
  </div>
)

/**
 * Two wave periods drawn as one path, twice the card's width, sliding by one period per loop.
 * A real path rather than a repeated CSS mask: Gecko stops repeating masks on moving layers.
 */
const Wave = ({ className, style }: { className: string; style: CSSProperties }) => (
  <svg viewBox="0 0 400 40" preserveAspectRatio="none" className={`wave ${className}`} style={style}>
    <path d="M0 12 Q50 0 100 12 T200 12 T300 12 T400 12 V40 H0Z" fill="currentColor" />
  </svg>
)

const StatCard = ({
  label,
  color,
  delay,
  children
}: {
  label: string
  color: string
  delay: number
  children: ReactNode
}) => (
  <div className="panel relative overflow-hidden px-4 py-2.5">
    <Liquid color={color} delay={delay} />
    <div className="relative">
      <p className="text-xs font-extrabold tracking-wide text-ink/55 uppercase">{label}</p>
      <p className="truncate font-game text-2xl leading-tight">
        {children ?? <span className="skeleton inline-block h-6 w-16 align-middle" />}
      </p>
    </div>
  </div>
)

/** `null` while loading: the same cards, with placeholders, so nothing shifts when the numbers land. */
const StatsRow = ({ stats }: { stats: Stats | null }) => (
  <div className="flex items-end gap-3">
    <Floppa mood="loaf" className="hidden h-20 w-auto shrink-0 sm:block" />
    <div className="grid flex-1 grid-cols-2 gap-3 lg:grid-cols-4">
      <StatCard label="Artifacts" color="#5ab8ff" delay={0}>
        {stats?.total.toLocaleString("en")}
      </StatCard>
      <StatCard label="Storage" color="#4cd964" delay={-1.7}>
        {stats && formatSize(stats.totalBytes)}
      </StatCard>
      <StatCard label="Uploaded today" color="#ffb340" delay={-3.1}>
        {stats?.last24h.toLocaleString("en")}
      </StatCard>
      <StatCard label="Last upload" color="#f78fb6" delay={-4.4}>
        {stats && (stats.lastUploadAt === undefined ? "never" : formatAgo(stats.lastUploadAt))}
      </StatCard>
    </div>
  </div>
)

interface Option {
  readonly value: string
  readonly label: string
  readonly count?: number
}

/**
 * A dropdown dressed like the rest of the game: Base UI handles the listbox semantics and keyboard,
 * the popup is a panel. Native pickers can't be styled in Gecko yet (`appearance: base-select` is flagged off).
 */
const Select = ({
  label,
  value,
  onChange,
  options
}: {
  label: string
  value: string
  onChange: (value: string) => void
  options: ReadonlyArray<Option>
}) => (
  <BaseSelect.Root items={options} value={value} onValueChange={(next) => onChange(next ?? "")} modal={false}>
    <BaseSelect.Trigger aria-label={label} className="game-select game-field">
      <BaseSelect.Value className="min-w-0 truncate" />
      <BaseSelect.Icon className="shrink-0 transition-transform duration-150 in-data-popup-open:rotate-180">
        <CaretDownIcon size={14} />
      </BaseSelect.Icon>
    </BaseSelect.Trigger>
    <BaseSelect.Portal>
      <BaseSelect.Positioner alignItemWithTrigger={false} align="start" sideOffset={10} className="z-40 outline-none">
        <BaseSelect.Popup className="game-picker">
          <BaseSelect.List className="max-h-[min(22rem,var(--available-height))] overflow-y-auto p-1.5">
            {options.map((option) => (
              <BaseSelect.Item key={option.value} value={option.value} className="game-option group">
                <BaseSelect.ItemIndicator className="col-start-1">
                  <CheckIcon size={14} />
                </BaseSelect.ItemIndicator>
                <BaseSelect.ItemText className="col-start-2 min-w-0 truncate">{option.label}</BaseSelect.ItemText>
                {option.count !== undefined && (
                  <span className="col-start-3 rounded-md border-2 border-ink bg-gold px-1.5 font-game text-xs group-data-selected:bg-cream">
                    {option.count}
                  </span>
                )}
              </BaseSelect.Item>
            ))}
          </BaseSelect.List>
        </BaseSelect.Popup>
      </BaseSelect.Positioner>
    </BaseSelect.Portal>
  </BaseSelect.Root>
)

const RepoSelect = ({ repo, onChange }: { repo: string; onChange: (repo: string) => void }) => {
  const repos = AsyncResult.getOrElse(useAtomValue(reposAtom), () => [])
  const options: Array<Option> = [
    { value: "", label: "All repos" },
    ...repos.map((r) => ({ value: r.repo, label: r.repo, count: r.count }))
  ]
  if (repo !== "" && !repos.some((r) => r.repo === repo)) options.push({ value: repo, label: repo })
  return <Select label="Repository" value={repo} onChange={onChange} options={options} />
}

const PrSelect = ({ repo, pr, onChange }: { repo: string; pr: string; onChange: (pr: string) => void }) => {
  const prs = AsyncResult.getOrElse(useAtomValue(prsAtom(repo)), () => [])
  useRevalidate(prsAtom(repo))
  // Within one repo "#42" is enough; across repos keep the owner/repo prefix.
  const label = (url: string) => (repo === "" ? prLabel(url) : prLabel(url).replace(/^.*(#\d+)$/, "$1"))
  const options: Array<Option> = [
    { value: "", label: "All PRs" },
    ...prs.map((p) => ({ value: p.pr, label: label(p.pr), count: p.count }))
  ]
  if (pr !== "" && !prs.some((p) => p.pr === pr)) options.push({ value: pr, label: label(pr) })
  return <Select label="Pull request" value={pr} onChange={onChange} options={options} />
}

const Thumbnail = ({ artifact }: { artifact: Artifact }) => {
  switch (kindOf(artifact)) {
    case "image":
      return <img src={artifact.rawUrl} alt="" loading="lazy" className="size-full object-contain" />
    case "video":
      return <video src={`${artifact.rawUrl}#t=0.1`} preload="metadata" muted className="size-full object-contain" />
    default:
      return <span className="font-game text-xl text-ink/80">{extensionOf(artifact.name)}</span>
  }
}

const Slot = ({ artifact }: { artifact: Artifact }) => {
  const path = `/a/${artifact.id}`
  const media = kindOf(artifact) === "image" || kindOf(artifact) === "video"
  const ago = formatAgoShort(artifact.createdAt)
  return (
    <a href={path} onClick={onLinkClick(path)} className="pressable pressable-card block">
      <div className="pressable-face p-2">
        <div
          className={`flex aspect-square items-center justify-center overflow-hidden rounded-lg border-[3px] border-ink ${
            media ? "checkerboard" : ""
          }`}
          style={media ? undefined : { backgroundColor: colorFor(extensionOf(artifact.name)) }}
        >
          <Thumbnail artifact={artifact} />
        </div>
        <p className="mt-1.5 truncate px-0.5 text-sm font-extrabold" title={artifact.name}>
          {artifact.name}
        </p>
        <p className="truncate px-0.5 text-[11px] font-bold text-ink/55">
          {formatSize(artifact.size)} · {ago === "now" ? "just now" : `${ago} ago`}
        </p>
      </div>
    </a>
  )
}

const GRID = "grid grid-cols-2 gap-3 sm:grid-cols-4 lg:grid-cols-6"

/** A first load: one group's worth of empty slots, sized like the real ones. */
const InventorySkeleton = () => (
  <section aria-busy aria-label="Loading artifacts" className="space-y-3">
    <div className="skeleton h-7 w-56 rounded-lg" />
    <div className={GRID}>
      {Array.from({ length: 6 }, (_, i) => (
        <div key={i} className="panel mt-1 p-2">
          <div className="skeleton aspect-square rounded-lg" />
          <div className="skeleton mt-2.5 h-3.5 w-3/4" />
          <div className="skeleton mt-2 mb-0.5 h-2.5 w-1/2" />
        </div>
      ))}
    </div>
  </section>
)

/** Cached data shows instantly when an atom is picked back up; refetch it in the background, once per pick-up. */
const useRevalidate = (atom: Atom.Atom<AsyncResult.AsyncResult<unknown, unknown>>) => {
  const result = useAtomValue(atom)
  const refresh = useAtomRefresh(atom)
  const checked = useRef<unknown>(null)
  useEffect(() => {
    if (checked.current === atom) return
    checked.current = atom
    if (AsyncResult.isSuccess(result) && !result.waiting) refresh()
  }, [atom, result, refresh])
}

/** Cards and groups pop in with a little bounce and shrink away; whatever stays glides to its new spot. */
const pop = {
  layout: "position",
  initial: { opacity: 0, scale: 0.85 },
  animate: { opacity: 1, scale: 1 },
  exit: { opacity: 0, scale: 0.85, transition: { duration: 0.15, ease: "easeIn" } },
  transition: { type: "spring", bounce: 0.35, visualDuration: 0.35 }
} as const

const groupByPr = (artifacts: ReadonlyArray<Artifact>) => {
  const groups = new Map<string | undefined, Array<Artifact>>()
  for (const artifact of artifacts) {
    const group = groups.get(artifact.pr) ?? []
    group.push(artifact)
    groups.set(artifact.pr, group)
  }
  return [...groups.entries()]
}

const Inventory = ({ artifacts }: { artifacts: ReadonlyArray<Artifact> }) => (
  <div className="relative flex flex-col gap-8">
    <AnimatePresence mode="popLayout" initial={false}>
      {groupByPr(artifacts).map(([pr, items]) => (
        <motion.section key={pr ?? "none"} {...pop} className="space-y-3">
          <h2 className="flex flex-wrap items-center gap-2">
            {pr === undefined ? (
              <span className="game-title text-xl">no PR</span>
            ) : (
              <a
                href={pr}
                target="_blank"
                rel="noreferrer"
                className="game-title text-xl transition-colors hover:text-sky"
              >
                {prLabel(pr)}
              </a>
            )}
            <span className="rounded-md border-2 border-ink bg-gold px-2 font-game text-xs">
              {items.length} item{items.length === 1 ? "" : "s"}
            </span>
          </h2>
          <div className={`${GRID} relative`}>
            <AnimatePresence mode="popLayout" initial={false}>
              {items.map((artifact) => (
                <motion.div key={artifact.id} {...pop}>
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

/** Hooks an agent up to this server: Claude Code syntax, other clients take the same URL and header. */
const mcpCommand = (token: string) =>
  `claude mcp add --transport http sloppa-artifacts ${window.location.origin}/mcp --header "Authorization: Bearer ${token}"`

const NoArtifacts = () => {
  const command = mcpCommand(useAtomValue(tokenAtom) ?? "<token>")
  return (
    <Notice title="no artifacts yet" mood="loaf">
      <p>connect an agent over MCP, then ask it to attach screenshots to its PR:</p>
      <div className="mt-3 flex flex-col items-center gap-2">
        <code className="max-w-full rounded-lg border-2 border-ink bg-white px-3 py-1.5 text-left font-mono text-xs break-all text-ink">
          {command}
        </code>
        <CopyButton value={command} label="copy" toast="copied" tone="green" />
      </div>
    </Notice>
  )
}

const matches = (search: string) => {
  const needle = search.trim().toLowerCase()
  return (a: Artifact) =>
    needle === "" ||
    [a.name, a.id, a.pr ?? "", a.repo ?? "", a.contentType].some((f) => f.toLowerCase().includes(needle))
}

type PagesValue = AsyncResult.AsyncResult.Success<Atom.Type<ReturnType<typeof artifactPagesAtom>>>

const Pages = ({ filter, search }: { filter: ListFilter; search: string }) => {
  const result = useAtomValue(artifactPagesAtom(filter))
  const loadMore = useAtomSet(artifactPagesAtom(filter))
  const filtered = filter.repo !== "" || filter.pr !== "" || search.trim() !== ""
  const keep = useMemo(() => matches(search), [search])
  useRevalidate(artifactPagesAtom(filter))
  // A filter that's never been loaded keeps the previous results on screen, dimmed, until its own land:
  // the cards then animate from one set to the other instead of flashing through a skeleton.
  const [last, setLast] = useState<PagesValue | null>(null)
  if (AsyncResult.isSuccess(result) && result.value !== last) setLast(result.value)
  const shown = AsyncResult.isInitial(result) && last !== null ? AsyncResult.success(last, { waiting: true }) : result
  const stale = shown !== result
  return AsyncResult.matchWithError(shown, {
    onInitial: () => <InventorySkeleton />,
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
    onSuccess: ({ value, waiting }) => {
      const all = value.items.flatMap((page) => page.items)
      const visible = all.filter(keep)
      const hasMore = value.items.at(-1)?.nextCursor !== undefined
      if (all.length === 0 && !filtered) return <NoArtifacts />
      return (
        <div aria-busy={stale} className={`space-y-10 transition-opacity duration-200 ${stale ? "opacity-60" : ""}`}>
          {visible.length === 0 ? (
            <Notice title="nothing matches these filters" mood="loaf" />
          ) : (
            <Inventory artifacts={visible} />
          )}
          {hasMore && (
            <div className="flex justify-center">
              <Button tone="blue" disabled={waiting || stale} onClick={() => loadMore()}>
                {waiting ? "loading…" : "load more"}
              </Button>
            </div>
          )}
        </div>
      )
    }
  })
}

const Dashboard = ({ stats }: { stats: Stats | null | undefined }) => {
  const [repo, setRepo] = useQueryParam("repo")
  const [pr, setPr] = useQueryParam("pr")
  const [search, setSearch] = useQueryParam("q")
  return (
    <div className="space-y-8">
      {stats !== undefined && <StatsRow stats={stats} />}
      <div className="grid gap-3 md:grid-cols-[1fr_15rem_15rem]">
        <input
          type="search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="search by name, type or id…"
          aria-label="Search artifacts"
          className="panel game-field w-full px-4 py-2.5 font-semibold placeholder:text-ink/40"
        />
        <RepoSelect
          repo={repo}
          onChange={(next) => {
            setRepo(next)
            setPr("")
          }}
        />
        <PrSelect repo={repo} pr={pr} onChange={setPr} />
      </div>
      <Pages filter={{ repo, pr }} search={search} />
    </div>
  )
}

/** Stats are the cheapest authorized call: they decide between the dashboard (skeleton until then) and a rejected token. */
const SignedIn = () => {
  const stats = useAtomValue(statsAtom)
  useRevalidate(statsAtom)
  useRevalidate(reposAtom)
  return AsyncResult.matchWithError(stats, {
    onInitial: () => <Dashboard stats={null} />,
    onError: (error) =>
      Predicate.isTagged(error, "Unauthorized") ? <Login invalid /> : <Dashboard stats={undefined} />,
    onDefect: () => <Dashboard stats={undefined} />,
    onSuccess: ({ value }) => <Dashboard stats={value} />
  })
}

export const ArtifactList = () => (useAtomValue(tokenAtom) === null ? <Login invalid={false} /> : <SignedIn />)
