import { useAtom, useAtomRefresh, useAtomSet, useAtomValue } from "@effect/atom-react"
import { Popover } from "@base-ui/react/popover"
import { KeyIcon, PencilSimpleIcon, RobotIcon, XIcon } from "@phosphor-icons/react"
import { type Agent, AGENT_NAME_MAX, type SetupLink } from "@sloppa/api"
import { DateTime, Exit } from "effect"
import { AsyncResult, Atom } from "effect/reactivity"
import { useEffect, useState } from "react"
import {
  agentsAtom,
  createAgentAtom,
  createSetupAtom,
  invalidateAgents,
  renameAgentAtom,
  revokeAgentAtom,
  revokeSetupAtom
} from "../api.ts"
import { formatAgo } from "../format.ts"
import { Button, CopyButton, overlayScrollbar, useToast } from "./ui.tsx"

/** The link made last, kept while the tab lives so closing the menu doesn't lose it. */
const setupLinkAtom = Atom.make<SetupLink | null>(null).pipe(Atom.keepAlive)

/** Re-renders every `ms`, for countdowns and "used 2 minutes ago". */
const useNow = (ms: number) => {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), ms)
    return () => clearInterval(timer)
  }, [ms])
  return now
}

/** `text` with `secret` blurred until the secret itself is hovered, so the panel is safe to screen-share. */
const Redacted = ({ text, secret }: { text: string; secret: string }) =>
  text.split(secret).flatMap((part, i) =>
    i === 0
      ? [part]
      : [
          <span key={i} className="blur-[4px] transition-[filter] duration-200 select-all hover:blur-none">
            {secret}
          </span>,
          part
        ]
  )

const msLeft = (link: SetupLink, now: number) => DateTime.toEpochMillis(link.expiresAt) - now

const SetupPrompt = () => {
  const [link, setLink] = useAtom(setupLinkAtom)
  const create = useAtomSet(createSetupAtom, { mode: "promiseExit" })
  const revoke = useAtomSet(revokeSetupAtom, { mode: "promiseExit" })
  const refreshAgents = useAtomRefresh(agentsAtom)
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const now = useNow(5_000)
  const live = link !== null && msLeft(link, now) > 0
  const minutes = link === null ? 0 : Math.max(1, Math.round(msLeft(link, now) / 60_000))

  // Agents show up below as they redeem the link.
  useEffect(() => {
    if (!live) return
    const timer = setInterval(refreshAgents, 4_000)
    return () => clearInterval(timer)
  }, [live, refreshAgents])

  const make = () => {
    setBusy(true)
    void create({}).then((exit) => {
      setBusy(false)
      if (Exit.isSuccess(exit)) setLink(exit.value)
      else toast("couldn't make a link")
    })
  }

  if (!live) {
    return (
      <div className="space-y-2">
        {link !== null && <p className="text-xs font-bold text-ink/60">that setup link expired.</p>}
        <Button tone="green" className="w-full" disabled={busy} onClick={make}>
          {busy ? "making one…" : "make a setup prompt"}
        </Button>
      </div>
    )
  }
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-3">
        <span className="font-game text-lg">setup prompt</span>
        <CopyButton value={link.prompt} label="copy" toast="copied, paste it into an agent" tone="blue" size="sm" />
      </div>
      {/* All of it is there to read before pasting, scrolled within a few lines' height. */}
      <div className="rounded-lg border-2 border-ink bg-white px-2.5 py-1.5">
        <pre
          ref={overlayScrollbar}
          className="max-h-[min(16rem,40vh)] overflow-y-auto font-mono text-xs wrap-break-word whitespace-pre-wrap"
        >
          <Redacted text={link.prompt} secret={link.url.slice(link.url.lastIndexOf("/") + 1)} />
        </pre>
      </div>
      <div className="flex items-center justify-between gap-3 text-xs font-bold text-ink/60">
        <span>
          works for {minutes} more minute{minutes === 1 ? "" : "s"}, for as many agents as you paste it into
        </span>
        <Button
          tone="red"
          size="sm"
          onClick={() =>
            void revoke({ params: { id: link.id } }).then((exit) => {
              if (Exit.isSuccess(exit)) {
                setLink(null)
                toast("setup link revoked")
              } else {
                toast("couldn't revoke it")
              }
            })
          }
        >
          revoke
        </Button>
      </div>
    </div>
  )
}

const SMALL_ICON = "[&>.pressable-face]:size-8"

/** The name and its rename field share one box, pulled out by its border and padding so the text stays put. */
const NAME_BOX = "-mx-2 block w-[calc(100%+1rem)] rounded-md border-2 px-1.5 py-0 text-sm leading-5 font-extrabold"

/** Saves on Enter or leaving the field; Escape (or an empty name) keeps the old one. */
const RenameField = ({ agent, onDone }: { agent: Agent; onDone: () => void }) => {
  const rename = useAtomSet(renameAgentAtom, { mode: "promiseExit" })
  const toast = useToast()
  const [name, setName] = useState(agent.name)
  const save = () => {
    onDone()
    const next = name.trim()
    if (next === "" || next === agent.name) return
    void rename({ params: { id: agent.id }, payload: { name: next }, reactivityKeys: invalidateAgents }).then(
      (exit) => Exit.isFailure(exit) && toast("couldn't rename it")
    )
  }
  return (
    <input
      autoFocus
      value={name}
      maxLength={AGENT_NAME_MAX}
      aria-label={`Rename ${agent.name}`}
      onChange={(e) => setName(e.target.value)}
      onFocus={(e) => e.target.select()}
      onBlur={save}
      onKeyDown={(e) => {
        if (e.key === "Enter") e.currentTarget.blur()
        if (e.key === "Escape") {
          // Don't close the menu along with the field.
          e.stopPropagation()
          setName(agent.name)
          onDone()
        }
      }}
      className={`${NAME_BOX} border-ink bg-white outline-none`}
    />
  )
}

const AgentRow = ({ agent, now }: { agent: Agent; now: number }) => {
  const revoke = useAtomSet(revokeAgentAtom, { mode: "promiseExit" })
  const toast = useToast()
  const [editing, setEditing] = useState(false)
  return (
    <li className="flex items-center gap-3 py-1.5">
      <RobotIcon size={18} className="shrink-0 text-ink/50" />
      <div className="min-w-0 flex-1">
        {editing ? (
          <RenameField agent={agent} onDone={() => setEditing(false)} />
        ) : (
          <p className={`${NAME_BOX} truncate border-transparent`} title={agent.name}>
            {agent.name}
          </p>
        )}
        <p className="text-[11px] font-bold text-ink/55">
          {agent.lastUsedAt === undefined ? "not used yet" : `used ${formatAgo(agent.lastUsedAt, now)}`}
        </p>
      </div>
      <Button
        tone="cream"
        size="icon"
        aria-label={`Rename ${agent.name}`}
        title="Rename"
        className={SMALL_ICON}
        onClick={() => setEditing(true)}
      >
        <PencilSimpleIcon size={14} weight="bold" />
      </Button>
      <Button
        tone="cream"
        size="icon"
        aria-label={`Revoke ${agent.name}`}
        title="Revoke its token"
        className={SMALL_ICON}
        onClick={() => {
          if (!window.confirm(`Revoke ${agent.name}? It won't reach this server until it's set up again.`)) return
          void revoke({ params: { id: agent.id }, reactivityKeys: invalidateAgents }).then((exit) =>
            toast(Exit.isSuccess(exit) ? "revoked" : "couldn't revoke it")
          )
        }}
      >
        <XIcon size={14} weight="bold" />
      </Button>
    </li>
  )
}

/** A key made last, kept until dismissed: it is shown this once, so closing the menu mustn't lose it. */
const createdKeyAtom = Atom.make<{ readonly name: string; readonly token: string } | null>(null).pipe(Atom.keepAlive)

const NewKey = ({ onDone }: { onDone: () => void }) => {
  const create = useAtomSet(createAgentAtom, { mode: "promiseExit" })
  const setCreated = useAtomSet(createdKeyAtom)
  const toast = useToast()
  const [name, setName] = useState("")
  const [busy, setBusy] = useState(false)
  return (
    <form
      className="flex items-center gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        if (name.trim() === "") return
        setBusy(true)
        void create({ payload: { name: name.trim() }, reactivityKeys: invalidateAgents }).then((exit) => {
          setBusy(false)
          if (Exit.isFailure(exit)) return toast("couldn't make a key")
          setCreated({ name: exit.value.agent.name, token: exit.value.token })
          onDone()
        })
      }}
    >
      <input
        autoFocus
        value={name}
        maxLength={AGENT_NAME_MAX}
        placeholder="label, e.g. CI on GitHub"
        aria-label="API key label"
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => {
          if (e.key !== "Escape") return
          // Don't close the menu along with the field.
          e.stopPropagation()
          onDone()
        }}
        className="panel game-field min-w-0 flex-1 bg-white px-2.5 py-1 text-sm font-bold placeholder:text-ink/40"
      />
      <Button type="submit" tone="green" size="sm" disabled={busy || name.trim() === ""}>
        {busy ? "making…" : "create"}
      </Button>
    </form>
  )
}

const CreatedKey = ({ name, token }: { name: string; token: string }) => {
  const setCreated = useAtomSet(createdKeyAtom)
  return (
    <div className="space-y-2 rounded-lg border-2 border-dashed border-ink/40 p-2.5">
      <div className="flex items-center justify-between gap-3">
        <span className="min-w-0 truncate text-sm font-extrabold" title={name}>
          {name}
        </span>
        <CopyButton value={token} label="copy" toast="key copied" tone="blue" size="sm" />
      </div>
      <code className="block rounded-lg border-2 border-ink bg-white px-2.5 py-1.5 font-mono text-xs break-all">
        <Redacted text={token} secret={token} />
      </code>
      <div className="flex items-center justify-between gap-3 text-xs font-bold text-ink/60">
        <span>shown this once: send it as Authorization: Bearer &lt;key&gt;</span>
        <Button size="sm" onClick={() => setCreated(null)}>
          done
        </Button>
      </div>
    </div>
  )
}

const Agents = () => {
  const agents = useAtomValue(agentsAtom)
  const created = useAtomValue(createdKeyAtom)
  const [adding, setAdding] = useState(false)
  const now = useNow(30_000)
  const list = AsyncResult.isSuccess(agents) ? agents.value : []
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-3">
        <p className="font-game text-lg">agents & keys</p>
        {!adding && (
          <Button tone="cream" size="sm" onClick={() => setAdding(true)}>
            <span className="flex items-center gap-1.5">
              <KeyIcon size={14} weight="bold" />
              API key
            </span>
          </Button>
        )}
      </div>
      {adding && <NewKey onDone={() => setAdding(false)} />}
      {created !== null && <CreatedKey name={created.name} token={created.token} />}
      {list.length === 0 ? (
        <p className="text-xs font-bold text-ink/55">none yet: they show up here as they connect.</p>
      ) : (
        <ul className="divide-y-2 divide-dashed divide-ink/15">
          {list.map((agent) => (
            <AgentRow key={agent.id} agent={agent} now={now} />
          ))}
        </ul>
      )}
    </div>
  )
}

/** A setup prompt to paste into agents, keys made by hand, and everything already connected. */
export const AgentSetup = () => (
  <div className="space-y-4 text-left">
    <SetupPrompt />
    <Agents />
  </div>
)

/** The header's way to hook an agent up to this server. */
export const ConnectAgent = () => {
  const refreshAgents = useAtomRefresh(agentsAtom)
  return (
    <Popover.Root onOpenChange={(open) => open && refreshAgents()}>
      <Popover.Trigger
        render={
          <Button
            tone="gold"
            size="icon"
            aria-label="Connect an agent"
            title="Connect an agent"
            className="[&>.pressable-face]:size-12.5"
          >
            <RobotIcon size={22} />
          </Button>
        }
      />
      <Popover.Portal>
        <Popover.Positioner align="end" sideOffset={10} collisionPadding={12} className="z-40 outline-none">
          <Popover.Popup className="game-picker w-[min(26rem,calc(100vw-1.5rem))] space-y-3 p-4">
            <div>
              <Popover.Title className="font-game text-xl">connect an agent</Popover.Title>
              <Popover.Description className="text-sm font-semibold text-ink/70">
                paste the prompt into Claude Code, Codex or any agent with a shell: it sets itself up.
              </Popover.Description>
            </div>
            <AgentSetup />
          </Popover.Popup>
        </Popover.Positioner>
      </Popover.Portal>
    </Popover.Root>
  )
}
