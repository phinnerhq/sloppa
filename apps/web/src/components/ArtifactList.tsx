import { useAtomValue } from "@effect/atom-react"
import { Select as BaseSelect } from "@base-ui/react/select"
import { ArrowLeftIcon, CaretDownIcon, CheckIcon } from "@phosphor-icons/react"
import type { Stats } from "@sloppa/api"
import { Predicate } from "effect"
import { AsyncResult } from "effect/reactivity"
import { type CSSProperties, type ReactNode, useEffect, useState } from "react"
import { artifactPagesAtom, type ListFilter, NO_PR, prsAtom, reposAtom, sessionAtom, statsAtom } from "../api.ts"
import { formatAgo, formatSize, prLabel } from "../format.ts"
import { onLinkClick, returnTo, useQueryParam } from "../router.ts"
import { Inventory, InventorySkeleton, ItemCount, Paged, PrTitle, SlotsSkeleton, useRevalidate } from "./Inventory.tsx"
import { Login } from "./Login.tsx"
import { AgentSetup } from "./Connect.tsx"
import { Mosaic } from "./Mosaic.tsx"
import { ButtonLink, Floppa, Notice, overlayScrollbar, PrName } from "./ui.tsx"

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
  /** Also what type-ahead matches. */
  readonly label: string
  /** Shown instead of the label, in the list and the trigger. */
  readonly display?: ReactNode
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
      <BaseSelect.Value className="min-w-0 truncate">
        {(selected: string) => {
          const option = options.find((o) => o.value === selected)
          return option?.display ?? option?.label
        }}
      </BaseSelect.Value>
      <BaseSelect.Icon className="shrink-0 transition-transform duration-150 in-data-popup-open:rotate-180">
        <CaretDownIcon size={14} />
      </BaseSelect.Icon>
    </BaseSelect.Trigger>
    <BaseSelect.Portal>
      <BaseSelect.Positioner alignItemWithTrigger={false} align="start" sideOffset={10} className="z-40 outline-none">
        {/* The padding goes on the popup: OverlayScrollbars hands its target's padding to the list it scrolls. */}
        <BaseSelect.Popup className="game-picker p-1.5">
          <BaseSelect.List
            ref={overlayScrollbar}
            className="max-h-[min(22rem,var(--available-height))] overflow-y-auto"
          >
            {options.map((option) => (
              <BaseSelect.Item key={option.value} value={option.value} className="game-option group">
                <BaseSelect.ItemIndicator className="col-start-1">
                  <CheckIcon size={14} />
                </BaseSelect.ItemIndicator>
                <BaseSelect.ItemText className="col-start-2 min-w-0 truncate">
                  {option.display ?? option.label}
                </BaseSelect.ItemText>
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
  // Within one repo "#42" is enough; across repos keep the owner/repo prefix, without losing the number.
  const option = (url: string): Option =>
    repo === ""
      ? { value: url, label: prLabel(url), display: <PrName pr={url} /> }
      : { value: url, label: prLabel(url).replace(/^.*(#\d+)$/, "$1") }
  const options: Array<Option> = [
    { value: "", label: "All PRs" },
    ...prs.map((p) => ({ ...option(p.pr), count: p.count }))
  ]
  if (pr === NO_PR) options.push({ value: pr, label: "No PR" })
  else if (pr !== "" && !prs.some((p) => p.pr === pr)) options.push(option(pr))
  return <Select label="Pull request" value={pr} onChange={onChange} options={options} />
}

const NoArtifacts = () => (
  <Notice title="no artifacts yet" mood="loaf">
    <p>connect an agent over MCP, then ask it to attach screenshots to its PRs:</p>
    <div className="mx-auto mt-4 max-w-md">
      <AgentSetup />
    </div>
  </Notice>
)

/** Every artifact of a PR, or the ones matching a search, as one long grid. */
const Pages = ({ filter }: { filter: ListFilter }) => (
  <Paged
    atom={artifactPagesAtom(filter)}
    skeleton={<InventorySkeleton />}
    more={<SlotsSkeleton />}
    empty={<Notice title="nothing matches these filters" mood="loaf" />}
  >
    {(pages) => <Inventory artifacts={pages.flatMap((page) => page.items)} headings={filter.pr === ""} />}
  </Paged>
)

/** Atop one PR's artifacts: the way back to the mosaic, and the PR with its full count. */
const PrHeading = ({ repo, pr }: { repo: string; pr: string }) => {
  const prs = AsyncResult.getOrElse(useAtomValue(prsAtom(repo)), () => [])
  const count = prs.find((p) => p.pr === pr)?.count
  const mosaic = repo === "" ? "/" : `/?${new URLSearchParams({ repo })}`
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
      <ButtonLink href={mosaic} onClick={onLinkClick(mosaic, returnTo)} size="sm">
        <ArrowLeftIcon size={16} /> all PRs
      </ButtonLink>
      <h2 className="flex min-w-0 flex-wrap items-center gap-2">
        <PrTitle pr={pr === NO_PR ? undefined : pr} />
        {count !== undefined && <ItemCount count={count} />}
      </h2>
    </div>
  )
}

/** `value` once it has stood still for `ms`; clearing it is immediate. */
const useSettled = (value: string, ms: number) => {
  const [settled, setSettled] = useState(value)
  useEffect(() => {
    const timeout = setTimeout(() => setSettled(value), ms)
    return () => clearTimeout(timeout)
  }, [value, ms])
  return value.trim() === "" ? value : settled
}

const Dashboard = ({ stats }: { stats: Stats | null | undefined }) => {
  const [repo, setRepo] = useQueryParam("repo")
  const [pr, setPr] = useQueryParam("pr")
  const [search, setSearch] = useQueryParam("q")
  // Every settled search is a request: wait for a pause in the typing.
  const query = useSettled(search, 250)
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
      {pr === "" && query.trim() === "" ? (
        <div key="mosaic" className="animate-rise-in">
          <Mosaic
            repo={repo}
            empty={repo === "" ? <NoArtifacts /> : <Notice title="nothing in this repo yet" mood="loaf" />}
          />
        </div>
      ) : (
        <div key="list" className="animate-rise-in space-y-5">
          {pr !== "" && <PrHeading repo={repo} pr={pr} />}
          <Pages filter={{ repo, pr, search: query }} />
        </div>
      )}
    </div>
  )
}

/** Stats are the cheapest authorized call: the dashboard skeleton waits on them. */
const SignedIn = () => {
  const stats = useAtomValue(statsAtom)
  useRevalidate(statsAtom)
  useRevalidate(reposAtom)
  return AsyncResult.matchWithError(stats, {
    onInitial: () => <Dashboard stats={null} />,
    onError: (error) =>
      Predicate.isTagged(error, "Unauthorized") ? <Login expired /> : <Dashboard stats={undefined} />,
    onDefect: () => <Dashboard stats={undefined} />,
    onSuccess: ({ value }) => <Dashboard stats={value} />
  })
}

export const ArtifactList = () =>
  AsyncResult.matchWithError(useAtomValue(sessionAtom), {
    onInitial: () => <Dashboard stats={null} />,
    onError: (error) => (Predicate.isTagged(error, "Unauthorized") ? <Login /> : <Dashboard stats={undefined} />),
    onDefect: () => <Dashboard stats={undefined} />,
    onSuccess: () => <SignedIn />
  })
