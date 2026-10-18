import { useAtomValue } from "@effect/atom-react"
import type { Stats } from "@sloppa/api"
import { Predicate } from "effect"
import { AsyncResult } from "effect/reactivity"
import { type CSSProperties, type ReactNode, useEffect, useState } from "react"
import { artifactPagesAtom, collectionsAtom, type ListFilter, sessionAtom, statsAtom } from "../api.ts"
import { formatAgo, formatSize } from "../format.ts"
import { onLinkClick, returnTo, useQueryParam } from "../router.ts"
import {
  CollectionTitle,
  Inventory,
  InventorySkeleton,
  ItemCount,
  Paged,
  SlotsSkeleton,
  useRevalidate
} from "./Inventory.tsx"
import { Login } from "./Login.tsx"
import { AgentSetup } from "./Connect.tsx"
import { Mosaic } from "./Mosaic.tsx"
import { SearchBar } from "./SearchBar.tsx"
import { ButtonLink, Floppa, Notice } from "./ui.tsx"
import { ArrowLeftIcon } from "./Icon"

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

const NoArtifacts = () => (
  <Notice title="no artifacts yet" mood="loaf">
    <p>set an agent up with the sloppa CLI, then ask it to attach screenshots to its PRs:</p>
    <div className="mx-auto mt-4 max-w-md">
      <AgentSetup />
    </div>
  </Notice>
)

/** Every artifact of a collection, or the ones matching a search, as one long grid. */
const Pages = ({ filter }: { filter: ListFilter }) => (
  <Paged
    atom={artifactPagesAtom(filter)}
    skeleton={<InventorySkeleton />}
    more={<SlotsSkeleton />}
    empty={<Notice title="nothing matches these filters" mood="loaf" />}
  >
    {(pages) => <Inventory artifacts={pages.flatMap((page) => page.items)} headings={filter.collection === ""} />}
  </Paged>
)

/** Atop one collection's artifacts: the way back to the mosaic, and the collection with its full count. */
const CollectionHeading = ({ collection }: { collection: string }) => {
  const collections = AsyncResult.getOrElse(useAtomValue(collectionsAtom), () => [])
  const count = collections.find((c) => c.collection === collection)?.count
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
      <ButtonLink href="/" onClick={onLinkClick("/", returnTo)} size="sm">
        <ArrowLeftIcon size={16} /> all collections
      </ButtonLink>
      <h2 className="flex min-w-0 flex-wrap items-center gap-2">
        <CollectionTitle collection={collection} />
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
  const [collection, setCollection] = useQueryParam("collection")
  const [search, setSearch] = useQueryParam("q")
  // Every settled search is a request: wait for a pause in the typing.
  const query = useSettled(search, 250)
  return (
    <div className="space-y-8">
      {stats !== undefined && <StatsRow stats={stats} />}
      <SearchBar
        collection={collection}
        onCollectionChange={setCollection}
        search={search}
        onSearchChange={setSearch}
      />
      {collection === "" && query.trim() === "" ? (
        <div key="mosaic" className="animate-rise-in">
          <Mosaic empty={<NoArtifacts />} />
        </div>
      ) : (
        <div key="list" className="animate-rise-in space-y-5">
          {collection !== "" && <CollectionHeading collection={collection} />}
          <Pages filter={{ collection, search: query }} />
        </div>
      )}
    </div>
  )
}

/** Stats are the cheapest authorized call: the dashboard skeleton waits on them. */
const SignedIn = () => {
  const stats = useAtomValue(statsAtom)
  useRevalidate(statsAtom)
  useRevalidate(collectionsAtom)
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
