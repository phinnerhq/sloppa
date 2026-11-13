import { useMutation, useQuery } from "@tanstack/react-query"
import { type ReactNode, useEffect, useRef, useState } from "react"
import {
  agentsQuery,
  configureSkillMutation,
  deleteSkillMutation,
  importSkillsMutation,
  listSourceMutation,
  putSkillMutation,
  setInstructionsMutation,
  setMachineInstructionsMutation,
  skillContentQuery,
  updateSkillsMutation
} from "../api.ts"
import type { MachineAgents, Skill, SkillFile, SourceListing } from "../client/types.gen.ts"
import { apiError } from "../errors.ts"
import { formatAgo, formatSize } from "../format.ts"
import { navigate, onLinkClick } from "../router.ts"
import { Button, ButtonLink, CopyButton, Loading, Notice, useNow, useToast } from "./ui.tsx"
import {
  ArrowsClockwiseIcon,
  CaretDownIcon,
  CheckIcon,
  FloppyDiskIcon,
  GithubLogoIcon,
  SparkleIcon,
  TrashIcon,
  UploadSimpleIcon,
  WarningIcon
} from "./Icon"

const SYNC = "sloppa agents sync"

const FIELD = "panel game-field w-full bg-white px-2.5 py-1.5 text-sm font-bold placeholder:text-ink/40"

/** What went wrong, from the server's message when it sent one. */
const messageOf = (error: unknown, fallback: string) => {
  const known = apiError(error as { code: string; message?: string } | null)
  return known !== undefined && "message" in known && typeof known.message === "string" ? known.message : fallback
}

/** The header's way in. */
export const AgentsButton = () => (
  <ButtonLink
    href="/agents"
    onClick={onLinkClick("/agents")}
    tone="blue"
    size="icon"
    aria-label="Agents"
    title="Agent instructions and skills"
    className="[&>.pressable-face]:size-12.5"
  >
    <SparkleIcon size={22} />
  </ButtonLink>
)

const Section = ({ title, aside, children }: { title: string; aside?: ReactNode; children: ReactNode }) => (
  <section className="space-y-3">
    <div className="flex flex-wrap items-end justify-between gap-2">
      <h2 className="game-title text-2xl">{title}</h2>
      {aside}
    </div>
    {children}
  </section>
)

// --- Machines ----------------------------------------------------------------------------------------

type SyncState = "synced" | "problems" | "behind" | "never"

const syncState = (m: MachineAgents): SyncState =>
  m.lastSync === undefined
    ? "never"
    : m.lastSync.fingerprint !== m.fingerprint
      ? "behind"
      : m.lastSync.problems.length > 0
        ? "problems"
        : "synced"

const STATE_LOOK: Record<SyncState, { readonly label: string; readonly dot: string }> = {
  synced: { label: "in sync", dot: "bg-cash" },
  problems: { label: "synced, with problems", dot: "bg-danger" },
  behind: { label: "needs a sync", dot: "bg-gold" },
  never: { label: "never synced", dot: "bg-ink/25" }
}

const MachineCard = ({ machine, now }: { machine: MachineAgents; now: number }) => {
  const state = syncState(machine)
  const look = STATE_LOOK[state]
  return (
    <li className="panel space-y-1 px-4 py-3">
      <div className="flex items-center gap-2">
        <span className={`size-3 shrink-0 rounded-full border-2 border-ink ${look.dot}`} aria-hidden />
        <p className="min-w-0 flex-1 truncate font-game text-lg">{machine.name}</p>
      </div>
      <p className="text-xs font-bold text-ink/65">
        {look.label}
        {machine.lastSync !== undefined && ` · last ${formatAgo(machine.lastSync.at, now)}`}
      </p>
      {state === "problems" && (
        <ul className="space-y-0.5 pt-1 text-xs font-semibold text-danger">
          {machine.lastSync?.problems.map((problem) => (
            <li key={problem} className="flex gap-1">
              <WarningIcon size={12} className="mt-0.5 shrink-0" />
              <span className="break-words">{problem}</span>
            </li>
          ))}
        </ul>
      )}
    </li>
  )
}

const Machines = ({ machines }: { machines: ReadonlyArray<MachineAgents> }) => {
  const now = useNow(30_000)
  return (
    <Section
      title="machines"
      aside={
        <div className="flex items-center gap-2 rounded-lg border-2 border-dashed border-ink/40 bg-cream/70 p-1.5 pl-2.5">
          <code className="font-mono text-xs">{SYNC}</code>
          <CopyButton value={SYNC} label="copy" toast="copied" tone="blue" size="sm" />
        </div>
      }
    >
      {machines.length === 0 ? (
        <p className="panel px-4 py-3 text-sm font-semibold text-ink/70">
          no machines yet: set one up with the robot button, then run <code className="font-mono">{SYNC}</code> on it.
        </p>
      ) : (
        <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {machines.map((machine) => (
            <MachineCard key={machine.machineId} machine={machine} now={now} />
          ))}
        </ul>
      )}
    </Section>
  )
}

// --- Instructions ------------------------------------------------------------------------------------

const SHARED = "shared"

const InstructionsEditor = ({
  tab,
  saved,
  machines
}: {
  tab: string
  saved: string
  machines: ReadonlyArray<MachineAgents>
}) => {
  const [draft, setDraft] = useState(saved)
  const setShared = useMutation(setInstructionsMutation)
  const setOwn = useMutation(setMachineInstructionsMutation)
  const toast = useToast()
  const dirty = draft !== saved
  const saving = setShared.isPending || setOwn.isPending
  const machine = machines.find((m) => m.machineId === tab)

  // Unsaved edits are worth a warning before the tab closes.
  useEffect(() => {
    if (!dirty) return
    const warn = (event: BeforeUnloadEvent) => event.preventDefault()
    window.addEventListener("beforeunload", warn)
    return () => window.removeEventListener("beforeunload", warn)
  }, [dirty])

  const save = () => {
    const done = {
      onSuccess: () => toast("saved: machines get it on their next sync"),
      onError: (error: unknown) => toast(messageOf(error, "couldn't save"))
    }
    if (tab === SHARED) setShared.mutate({ body: { content: draft } }, done)
    else setOwn.mutate({ path: { machineId: tab }, body: { content: draft } }, done)
  }

  return (
    <div className="space-y-2">
      <p className="text-sm font-semibold text-ink/70">
        {machine === undefined
          ? "every machine gets these in ~/.agents/AGENTS.md, linked as ~/.claude/CLAUDE.md, ~/.codex/AGENTS.md and ~/.config/opencode/AGENTS.md."
          : `only ${machine.name} gets these, after the shared ones.`}
      </p>
      <textarea
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if ((e.ctrlKey || e.metaKey) && e.key === "s") {
            e.preventDefault()
            if (dirty && !saving) save()
          }
        }}
        spellCheck={false}
        aria-label={machine === undefined ? "Shared instructions" : `Instructions for ${machine.name}`}
        placeholder={machine === undefined ? "## Agent instructions\n\n- …" : "nothing extra for this machine"}
        className={`${FIELD} min-h-72 resize-y font-mono text-[13px] leading-relaxed font-medium`}
      />
      <div className="flex items-center justify-end gap-3">
        <span className="text-xs font-bold text-ink/60">{dirty ? "unsaved changes · ctrl+s saves" : "saved"}</span>
        <Button tone="green" disabled={!dirty || saving} onClick={save}>
          <FloppyDiskIcon size={16} />
          save
        </Button>
      </div>
    </div>
  )
}

const Instructions = ({ shared, machines }: { shared: string; machines: ReadonlyArray<MachineAgents> }) => {
  const [tab, setTab] = useState(SHARED)
  const tabs = [{ id: SHARED, label: "shared" }, ...machines.map((m) => ({ id: m.machineId, label: m.name }))]
  const saved = tab === SHARED ? shared : (machines.find((m) => m.machineId === tab)?.instructions ?? "")
  return (
    <Section title="instructions">
      <div className="panel space-y-3 p-4">
        <div role="tablist" className="flex flex-wrap gap-2">
          {tabs.map((t) => (
            <Button
              key={t.id}
              role="tab"
              aria-selected={t.id === tab}
              size="sm"
              tone={t.id === tab ? "gold" : "cream"}
              onClick={() => setTab(t.id)}
            >
              {t.label}
              {t.id !== SHARED && machines.find((m) => m.machineId === t.id)?.instructions.trim() && (
                <span className="size-1.5 rounded-full bg-ink" aria-label="has its own" />
              )}
            </Button>
          ))}
        </div>
        {/* Keyed by tab and saved text: switching tabs or saving starts the draft over. */}
        <InstructionsEditor key={`${tab}:${saved}`} tab={tab} saved={saved} machines={machines} />
      </div>
    </Section>
  )
}

// --- Skills ------------------------------------------------------------------------------------------

/** Bytes as base64, in chunks: spreading a large array into one call overflows the stack. */
const toBase64 = (bytes: Uint8Array) => {
  let binary = ""
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return btoa(binary)
}

const fromBase64 = (content: string) => Uint8Array.from(atob(content), (c) => c.charCodeAt(0))

const SKILL_NAME = /^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$/

/** A skill's name from its SKILL.md frontmatter if it gives a valid one, its folder's otherwise. */
const skillNameOf = (skillMd: string, folder: string) => {
  const name = /^---\r?\n[\s\S]*?^name:\s*["']?([^"'\r\n]+?)["']?\s*$/m.exec(skillMd)?.[1]
  if (name !== undefined && SKILL_NAME.test(name)) return name
  return folder
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, "-")
    .replace(/^[-._]+|[-._]+$/g, "")
    .slice(0, 64)
}

interface FolderSkill {
  readonly name: string
  readonly files: ReadonlyArray<SkillFile>
}

const IGNORED = new Set([".git", "node_modules", ".DS_Store"])

/**
 * The skills in a picked folder: the folder itself when it has a SKILL.md, otherwise each of its folders
 * that has one, like ~/.agents/skills.
 */
const skillsInFolder = async (picked: ReadonlyArray<File>): Promise<FolderSkill[]> => {
  const entries = picked
    .map((file) => ({ file, parts: file.webkitRelativePath.split("/").slice(1) }))
    .filter(({ parts }) => parts.length > 0 && !parts.some((part) => IGNORED.has(part)))
  const root = picked[0]?.webkitRelativePath.split("/")[0] ?? ""
  const groups = new Map<string, Array<{ file: File; path: string }>>()
  if (entries.some(({ parts }) => parts.join("/") === "SKILL.md")) {
    groups.set(
      root,
      entries.map(({ file, parts }) => ({ file, path: parts.join("/") }))
    )
  } else {
    for (const { file, parts } of entries) {
      if (parts.length < 2) continue
      const [folder, ...rest] = parts
      groups.set(folder!, [...(groups.get(folder!) ?? []), { file, path: rest.join("/") }])
    }
  }
  const skills: FolderSkill[] = []
  for (const [folder, files] of groups) {
    const skillMd = files.find((f) => f.path === "SKILL.md")
    if (skillMd === undefined) continue
    const read = await Promise.all(
      files.map(async ({ file, path }) => ({ path, content: toBase64(new Uint8Array(await file.arrayBuffer())) }))
    )
    skills.push({ name: skillNameOf(await skillMd.file.text(), folder), files: read })
  }
  return skills.sort((a, b) => a.name.localeCompare(b.name))
}

const UploadFolder = () => {
  const input = useRef<HTMLInputElement>(null)
  const put = useMutation(putSkillMutation)
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    // Not in React's typings as a prop: set on the element.
    input.current?.setAttribute("webkitdirectory", "")
  }, [])
  const upload = async (files: ReadonlyArray<File>) => {
    setBusy(true)
    try {
      const skills = await skillsInFolder(files)
      if (skills.length === 0) {
        toast("no SKILL.md in that folder")
        return
      }
      const failed: string[] = []
      for (const skill of skills) {
        await put.mutateAsync({ path: { name: skill.name }, body: { files: [...skill.files] } }).catch(() => {
          failed.push(skill.name)
        })
      }
      toast(
        failed.length === 0
          ? `uploaded ${skills.map((s) => s.name).join(", ")}`
          : `couldn't upload ${failed.join(", ")}`
      )
    } finally {
      setBusy(false)
      if (input.current !== null) input.current.value = ""
    }
  }
  return (
    <>
      <input ref={input} type="file" multiple hidden onChange={(e) => void upload(Array.from(e.target.files ?? []))} />
      <Button
        tone="cream"
        size="sm"
        disabled={busy}
        title="A skill's folder, or a folder of skills like ~/.agents/skills"
        onClick={() => input.current?.click()}
      >
        <UploadSimpleIcon size={14} />
        {busy ? "uploading…" : "upload folder"}
      </Button>
    </>
  )
}

const ImportFromGitHub = ({ onClose }: { onClose: () => void }) => {
  const [source, setSource] = useState("")
  const [listing, setListing] = useState<SourceListing | null>(null)
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set())
  const [filter, setFilter] = useState("")
  const list = useMutation(listSourceMutation)
  const imp = useMutation(importSkillsMutation)
  const toast = useToast()
  const shown = (listing?.skills ?? []).filter(
    (s) => filter === "" || `${s.name} ${s.description ?? ""}`.toLowerCase().includes(filter.toLowerCase())
  )
  return (
    <div className="panel animate-pop-in space-y-3 p-4">
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault()
          if (source.trim() === "") return
          list.mutate(
            { body: { source } },
            {
              onSuccess: (l) => {
                setListing(l)
                setPicked(new Set())
                setFilter("")
              },
              onError: () => setListing(null)
            }
          )
        }}
      >
        <input
          value={source}
          onChange={(e) => setSource(e.target.value)}
          placeholder="owner/repo, or its GitHub URL"
          aria-label="GitHub repository"
          autoFocus
          className={FIELD}
        />
        <Button type="submit" tone="blue" disabled={list.isPending || source.trim() === ""}>
          {list.isPending ? "reading…" : "list skills"}
        </Button>
      </form>
      {list.isError && <p className="text-sm font-bold text-danger">{messageOf(list.error, "couldn't read it")}</p>}
      {listing !== null && (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <p className="text-sm font-bold">
              {listing.skills.length} skills in {listing.source}
              {listing.ref !== "" && (
                <span className="font-mono text-xs text-ink/55"> @ {listing.ref.slice(0, 7)}</span>
              )}
            </p>
            {listing.skills.length > 6 && (
              <input
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder="filter…"
                aria-label="Filter skills"
                className={`${FIELD} max-w-56`}
              />
            )}
          </div>
          <ul className="max-h-80 space-y-1 overflow-y-auto pr-1">
            {shown.map((s) => (
              <li key={s.name}>
                <label className="flex cursor-pointer items-start gap-2 rounded-lg px-2 py-1.5 hover:bg-ink/5">
                  <input
                    type="checkbox"
                    className="mt-1 size-4 shrink-0 accent-ink"
                    checked={picked.has(s.name)}
                    onChange={(e) => {
                      const next = new Set(picked)
                      if (e.target.checked) next.add(s.name)
                      else next.delete(s.name)
                      setPicked(next)
                    }}
                  />
                  <span className="min-w-0">
                    <span className="font-extrabold">{s.name}</span>
                    {s.imported && <span className="ml-2 text-xs font-bold text-ink/55">(have it: replaces it)</span>}
                    {s.description !== undefined && (
                      <span className="block truncate text-xs font-semibold text-ink/65">{s.description}</span>
                    )}
                  </span>
                </label>
              </li>
            ))}
          </ul>
        </div>
      )}
      <div className="flex justify-end gap-2">
        <Button size="sm" onClick={onClose}>
          close
        </Button>
        {listing !== null && (
          <Button
            tone="green"
            size="sm"
            disabled={picked.size === 0 || imp.isPending}
            onClick={() =>
              imp.mutate(
                { body: { source: listing.source, names: [...picked] } },
                {
                  onSuccess: (imported) => {
                    toast(`imported ${imported.map((s) => s.name).join(", ")}`)
                    onClose()
                  },
                  onError: (error) => toast(messageOf(error, "couldn't import"))
                }
              )
            }
          >
            {imp.isPending ? "importing…" : `import ${picked.size || ""}`}
          </Button>
        )}
      </div>
    </div>
  )
}

const decoder = new TextDecoder()

const SkillFiles = ({ name }: { name: string }) => {
  const content = useQuery(skillContentQuery(name))
  const [open, setOpen] = useState("SKILL.md")
  if (content.isPending) return <p className="text-xs font-bold text-ink/55">loading files…</p>
  if (content.isError) return <p className="text-xs font-bold text-danger">couldn't load its files.</p>
  const file = content.data.files.find((f) => f.path === open)
  return (
    <div className="grid gap-3 md:grid-cols-[14rem_1fr]">
      <ul className="space-y-0.5 text-xs">
        {content.data.files.map((f) => (
          <li key={f.path}>
            <button
              type="button"
              onClick={() => setOpen(f.path)}
              className={`w-full truncate rounded px-2 py-1 text-left font-mono ${f.path === open ? "bg-gold font-bold" : "hover:bg-ink/5"}`}
              title={f.path}
            >
              {f.path}
              {f.executable && " *"}
            </button>
          </li>
        ))}
      </ul>
      <pre className="max-h-96 overflow-auto rounded-lg border-2 border-ink/20 bg-white p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap">
        {file === undefined ? "" : decoder.decode(fromBase64(file.content))}
      </pre>
    </div>
  )
}

/** A machine's switch for a skill: on, or off on that machine (or everywhere, when the skill is). */
const MachineSwitch = ({ skill, machine }: { skill: Skill; machine: MachineAgents }) => {
  const configure = useMutation(configureSkillMutation)
  const off = skill.disabledOn.includes(machine.machineId)
  const on = skill.enabled && !off
  return (
    <button
      type="button"
      disabled={!skill.enabled || configure.isPending}
      aria-pressed={on}
      title={
        !skill.enabled ? "off everywhere" : on ? `on ${machine.name}: click to turn off` : `off on ${machine.name}`
      }
      onClick={() =>
        configure.mutate({
          path: { name: skill.name },
          body: {
            disabledOn: off
              ? skill.disabledOn.filter((id) => id !== machine.machineId)
              : [...skill.disabledOn, machine.machineId]
          }
        })
      }
      className={`flex items-center gap-1 rounded-full border-2 border-ink px-2 py-0.5 text-[11px] font-extrabold transition-colors disabled:cursor-not-allowed ${
        on ? "bg-cash text-white" : "bg-cream text-ink/45 line-through"
      } ${!skill.enabled ? "opacity-40" : ""}`}
    >
      {on && <CheckIcon size={10} />}
      {machine.name}
    </button>
  )
}

const SkillRow = ({ skill, machines }: { skill: Skill; machines: ReadonlyArray<MachineAgents> }) => {
  const [expanded, setExpanded] = useState(false)
  const configure = useMutation(configureSkillMutation)
  const update = useMutation(updateSkillsMutation)
  const del = useMutation(deleteSkillMutation)
  const toast = useToast()
  return (
    <li className={`py-3 ${skill.enabled ? "" : "opacity-70"}`}>
      <div className="flex flex-wrap items-start gap-3">
        <button
          type="button"
          onClick={() => setExpanded(!expanded)}
          aria-expanded={expanded}
          className="mt-0.5 grid size-6 shrink-0 place-items-center rounded-md hover:bg-ink/10"
          aria-label={expanded ? `Hide ${skill.name}'s files` : `Show ${skill.name}'s files`}
        >
          <CaretDownIcon size={14} className={`transition-transform ${expanded ? "" : "-rotate-90"}`} />
        </button>
        <div className="min-w-0 flex-1 basis-64 space-y-1">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className="font-game text-lg leading-tight">{skill.name}</span>
            {skill.source === undefined ? (
              <span className="text-xs font-bold text-ink/55">uploaded</span>
            ) : (
              <a
                href={`https://github.com/${skill.source}${skill.sourcePath ? `/tree/${skill.sourceRef ?? "HEAD"}/${skill.sourcePath}` : ""}`}
                target="_blank"
                rel="noreferrer"
                className="flex items-center gap-1 text-xs font-bold text-ink/65 hover:text-sky-deep"
              >
                <GithubLogoIcon size={12} />
                {skill.source}
              </a>
            )}
            <span className="font-mono text-[11px] text-ink/45" title={`version ${skill.hash}`}>
              {skill.hash.slice(0, 8)} · {skill.files === 1 ? "1 file" : `${skill.files} files`} ·{" "}
              {formatSize(skill.size)}
            </span>
          </div>
          {skill.description !== undefined && (
            <p className="line-clamp-2 text-sm font-semibold text-ink/70">{skill.description}</p>
          )}
          {machines.length > 0 && (
            <div className="flex flex-wrap gap-1.5 pt-1">
              {machines.map((m) => (
                <MachineSwitch key={m.machineId} skill={skill} machine={m} />
              ))}
            </div>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <label className="flex cursor-pointer items-center gap-1.5 text-xs font-extrabold">
            <input
              type="checkbox"
              role="switch"
              className="size-4 accent-cash"
              checked={skill.enabled}
              disabled={configure.isPending}
              onChange={(e) => configure.mutate({ path: { name: skill.name }, body: { enabled: e.target.checked } })}
            />
            {skill.enabled ? "on" : "off"}
          </label>
          {skill.source !== undefined && (
            <Button
              size="icon"
              aria-label={`Update ${skill.name}`}
              title={`Update from ${skill.source}`}
              className="[&>.pressable-face]:size-8"
              disabled={update.isPending}
              onClick={() =>
                update.mutate(
                  { body: { names: [skill.name] } },
                  {
                    onSuccess: (u) =>
                      toast(
                        u.failed.length > 0
                          ? u.failed[0]!.message
                          : u.updated.length > 0
                            ? `updated ${skill.name}`
                            : `${skill.name} is up to date`
                      ),
                    onError: (error) => toast(messageOf(error, "couldn't update"))
                  }
                )
              }
            >
              <ArrowsClockwiseIcon size={14} className={update.isPending ? "animate-spin" : ""} />
            </Button>
          )}
          <Button
            size="icon"
            tone="red"
            aria-label={`Delete ${skill.name}`}
            title="Delete: machines remove it on their next sync"
            className="[&>.pressable-face]:size-8"
            disabled={del.isPending}
            onClick={() => {
              if (!window.confirm(`Delete ${skill.name}? Machines remove it on their next sync.`)) return
              del.mutate({ path: { name: skill.name } }, { onSuccess: () => toast(`deleted ${skill.name}`) })
            }}
          >
            <TrashIcon size={14} />
          </Button>
        </div>
      </div>
      {expanded && (
        <div className="mt-3 pl-9">
          <SkillFiles name={skill.name} />
        </div>
      )}
    </li>
  )
}

const Skills = ({ skills, machines }: { skills: ReadonlyArray<Skill>; machines: ReadonlyArray<MachineAgents> }) => {
  const [importing, setImporting] = useState(false)
  const [search, setSearch] = useState("")
  const updateAll = useMutation(updateSkillsMutation)
  const toast = useToast()
  const imported = skills.some((s) => s.source !== undefined)
  const shown = skills.filter(
    (s) =>
      search === "" || `${s.name} ${s.description ?? ""} ${s.source ?? ""}`.toLowerCase().includes(search.toLowerCase())
  )
  return (
    <Section
      title="skills"
      aside={
        <div className="flex flex-wrap items-center gap-2">
          <Button tone="blue" size="sm" onClick={() => setImporting(!importing)}>
            <GithubLogoIcon size={14} />
            import from github
          </Button>
          <UploadFolder />
          {imported && (
            <Button
              size="sm"
              disabled={updateAll.isPending}
              onClick={() =>
                updateAll.mutate(
                  { body: {} },
                  {
                    onSuccess: (u) =>
                      toast(
                        [
                          u.updated.length > 0 ? `updated ${u.updated.join(", ")}` : "all up to date",
                          u.failed.length > 0 ? `${u.failed.length} failed` : ""
                        ]
                          .filter(Boolean)
                          .join(" · ")
                      ),
                    onError: (error) => toast(messageOf(error, "couldn't update"))
                  }
                )
              }
            >
              <ArrowsClockwiseIcon size={14} className={updateAll.isPending ? "animate-spin" : ""} />
              {updateAll.isPending ? "updating…" : "update all"}
            </Button>
          )}
        </div>
      }
    >
      {importing && <ImportFromGitHub onClose={() => setImporting(false)} />}
      <div className="panel px-4 py-1">
        {skills.length === 0 ? (
          <p className="py-3 text-sm font-semibold text-ink/70">
            no skills yet: import some from a GitHub repository, or upload a skill's folder (or a whole
            ~/.agents/skills).
          </p>
        ) : (
          <>
            {skills.length > 8 && (
              <input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={`search ${skills.length} skills…`}
                aria-label="Search skills"
                className={`${FIELD} my-2`}
              />
            )}
            <ul className="divide-y-2 divide-dashed divide-ink/15">
              {shown.map((skill) => (
                <SkillRow key={skill.name} skill={skill} machines={machines} />
              ))}
            </ul>
          </>
        )}
      </div>
    </Section>
  )
}

// --- Page --------------------------------------------------------------------------------------------

export const Agents = () => {
  const overview = useQuery(agentsQuery)
  if (overview.isPending) return <Loading />
  if (overview.isError) {
    const signedOut = apiError(overview.error)?.code === "unauthorized"
    return (
      <Notice title={signedOut ? "sign in first" : "couldn't load agents"} mood="hiss">
        {signedOut ? (
          <Button tone="blue" onClick={() => navigate("/")}>
            go sign in
          </Button>
        ) : (
          "try again in a moment."
        )}
      </Notice>
    )
  }
  const { instructions, skills, machines } = overview.data
  return (
    <div className="space-y-10">
      <div className="space-y-1">
        <h1 className="game-title text-4xl">agents</h1>
        <p className="max-w-2xl text-sm font-semibold text-ink">
          instructions and skills for every machine&apos;s agents. edit them here, then run{" "}
          <code className="rounded bg-cream/80 px-1 font-mono">{SYNC}</code> on a machine to bring them over.
        </p>
      </div>
      <Machines machines={machines} />
      <Instructions shared={instructions} machines={machines} />
      <Skills skills={skills} machines={machines} />
    </div>
  )
}
