import { useAtomSet, useAtomValue } from "@effect/atom-react"
import { CaretDownIcon } from "@phosphor-icons/react"
import type { Artifact, Stats } from "@sloppa/api"
import { Predicate } from "effect"
import { AsyncResult } from "effect/reactivity"
import { type ReactNode, useMemo, useState } from "react"
import { artifactPagesAtom, type ListFilter, prsAtom, reposAtom, statsAtom, tokenAtom } from "../api.ts"
import { colorFor, formatAgo, formatAgoShort, formatSize, kindOf, prLabel } from "../format.ts"
import { onLinkClick, useQueryParam } from "../router.ts"
import { CopyButton, extensionOf, Floppa, Loading, Notice } from "./ui.tsx"

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
        className="w-full rounded-xl border-[3px] border-ink bg-white px-4 py-2.5 font-semibold placeholder:text-ink/40 focus:outline-none focus:ring-4 focus:ring-gold"
      />
      <button type="submit" className="game-btn game-btn-green w-full text-lg">
        sign in
      </button>
    </form>
  )
}

/** Two wave layers drifting against each other, offset per card so they never slosh in sync. */
const Liquid = ({ color, delay }: { color: string; delay: number }) => (
  <div aria-hidden className="absolute inset-x-0 bottom-0 h-1/2" style={{ color }}>
    <div
      className="wave opacity-35"
      style={{ animationDuration: "9s", animationDirection: "reverse", animationDelay: `${delay * 2}s` }}
    />
    <div className="wave opacity-60" style={{ animationDelay: `${delay}s` }} />
  </div>
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
      <p className="truncate font-game text-2xl leading-tight">{children}</p>
    </div>
  </div>
)

const StatsRow = ({ stats }: { stats: Stats }) => (
  <div className="flex items-end gap-3">
    <Floppa mood="loaf" className="hidden h-20 w-auto shrink-0 sm:block" />
    <div className="grid flex-1 grid-cols-2 gap-3 lg:grid-cols-4">
      <StatCard label="Artifacts" color="#5ab8ff" delay={0}>
        {stats.total.toLocaleString("en")}
      </StatCard>
      <StatCard label="Storage" color="#4cd964" delay={-1.7}>
        {formatSize(stats.totalBytes)}
      </StatCard>
      <StatCard label="Uploaded today" color="#ffb340" delay={-3.1}>
        {stats.last24h.toLocaleString("en")}
      </StatCard>
      <StatCard label="Last upload" color="#f78fb6" delay={-4.4}>
        {stats.lastUploadAt === undefined ? "never" : formatAgo(stats.lastUploadAt)}
      </StatCard>
    </div>
  </div>
)

const Select = ({
  label,
  value,
  onChange,
  children
}: {
  label: string
  value: string
  onChange: (value: string) => void
  children: ReactNode
}) => (
  <div className="relative">
    <select aria-label={label} value={value} onChange={(e) => onChange(e.target.value)} className="game-select w-full">
      {children}
    </select>
    <CaretDownIcon size={14} className="pointer-events-none absolute top-1/2 right-4 -translate-y-1/2" />
  </div>
)

const RepoSelect = ({ repo, onChange }: { repo: string; onChange: (repo: string) => void }) => {
  const repos = AsyncResult.getOrElse(useAtomValue(reposAtom), () => [])
  return (
    <Select label="Repository" value={repo} onChange={onChange}>
      <option value="">All repos</option>
      {repos.map((r) => (
        <option key={r.repo} value={r.repo}>
          {r.repo} ({r.count})
        </option>
      ))}
      {repo !== "" && !repos.some((r) => r.repo === repo) && <option value={repo}>{repo}</option>}
    </Select>
  )
}

const PrSelect = ({ repo, pr, onChange }: { repo: string; pr: string; onChange: (pr: string) => void }) => {
  const prs = AsyncResult.getOrElse(useAtomValue(prsAtom(repo)), () => [])
  // Within one repo "#42" is enough; across repos keep the owner/repo prefix.
  const label = (url: string) => (repo === "" ? prLabel(url) : prLabel(url).replace(/^.*(#\d+)$/, "$1"))
  return (
    <Select label="Pull request" value={pr} onChange={onChange}>
      <option value="">All PRs</option>
      {prs.map((p) => (
        <option key={p.pr} value={p.pr}>
          {label(p.pr)} ({p.count})
        </option>
      ))}
      {pr !== "" && !prs.some((p) => p.pr === pr) && <option value={pr}>{label(pr)}</option>}
    </Select>
  )
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
    <a
      href={path}
      onClick={onLinkClick(path)}
      className="panel group block p-2 transition-transform duration-100 hover:-translate-y-1 hover:rotate-[-1deg]"
    >
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
    </a>
  )
}

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
  <div className="space-y-8">
    {groupByPr(artifacts).map(([pr, items]) => (
      <section key={pr ?? "none"} className="space-y-3">
        <h2 className="flex flex-wrap items-center gap-2">
          {pr === undefined ? (
            <span className="game-title text-xl">no PR</span>
          ) : (
            <a href={pr} target="_blank" rel="noreferrer" className="game-title text-xl hover:underline">
              {prLabel(pr)}
            </a>
          )}
          <span className="rounded-md border-2 border-ink bg-gold px-2 font-game text-xs">
            {items.length} item{items.length === 1 ? "" : "s"}
          </span>
        </h2>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4 lg:grid-cols-6">
          {items.map((artifact) => (
            <Slot key={artifact.id} artifact={artifact} />
          ))}
        </div>
      </section>
    ))}
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
        <CopyButton value={command} label="copy" toast="copied" color="green" />
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

const Pages = ({ filter, search }: { filter: ListFilter; search: string }) => {
  const result = useAtomValue(artifactPagesAtom(filter))
  const loadMore = useAtomSet(artifactPagesAtom(filter))
  const filtered = filter.repo !== "" || filter.pr !== "" || search.trim() !== ""
  const keep = useMemo(() => matches(search), [search])
  return AsyncResult.matchWithError(result, {
    onInitial: () => <Loading />,
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
        <div className="space-y-10">
          {visible.length === 0 ? (
            <Notice title="nothing matches these filters" mood="loaf" />
          ) : (
            <Inventory artifacts={visible} />
          )}
          {hasMore && (
            <div className="flex justify-center">
              <button type="button" disabled={waiting} onClick={() => loadMore()} className="game-btn game-btn-blue">
                {waiting ? "loading…" : "load more"}
              </button>
            </div>
          )}
        </div>
      )
    }
  })
}

const Dashboard = () => {
  const [repo, setRepo] = useQueryParam("repo")
  const [pr, setPr] = useQueryParam("pr")
  const [search, setSearch] = useQueryParam("q")
  const stats = useAtomValue(statsAtom)
  return (
    <div className="space-y-8">
      {AsyncResult.isSuccess(stats) && <StatsRow stats={stats.value} />}
      <div className="grid gap-3 md:grid-cols-[1fr_auto_auto]">
        <input
          type="search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="search by name, type or id…"
          aria-label="Search artifacts"
          className="panel w-full px-4 py-2.5 font-semibold placeholder:text-ink/40 focus:outline-none focus:ring-4 focus:ring-gold"
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
      <Pages key={`${repo}|${pr}`} filter={{ repo, pr }} search={search} />
    </div>
  )
}

export const ArtifactList = () => (useAtomValue(tokenAtom) === null ? <Login invalid={false} /> : <Dashboard />)
