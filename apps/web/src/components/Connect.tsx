import { Popover } from "@base-ui/react/popover"
import { useMutation, useQuery } from "@tanstack/react-query"
import { useState } from "react"
import {
  createMachineMutation,
  createSetupMutation,
  machinesQuery,
  renameMachineMutation,
  revokeMachineMutation,
  revokeSetupMutation
} from "../api.ts"
import type { Machine, SetupLink } from "../client/types.gen.ts"
import { MACHINE_NAME_MAX } from "../constants.ts"
import { formatAgo } from "../format.ts"
import { shared, useShared } from "../store.ts"
import { Button, CopyButton, overlayScrollbar, useNow, useToast } from "./ui.tsx"
import { KeyIcon, PencilSimpleIcon, RobotIcon, XIcon } from "./Icon"

/** The link made last, kept while the tab lives so closing the menu doesn't lose it. */
const setupLink = shared<SetupLink | null>(null)

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

const msLeft = (link: SetupLink, now: number) => link.expiresAt.getTime() - now

const SetupPrompt = () => {
  const link = useShared(setupLink)
  const create = useMutation(createSetupMutation)
  const revoke = useMutation(revokeSetupMutation)
  const toast = useToast()
  const now = useNow(5_000)
  const live = link !== null && msLeft(link, now) > 0
  const minutes = link === null ? 0 : Math.max(1, Math.round(msLeft(link, now) / 60_000))

  // The machine shows up below as it redeems the link.
  useQuery({ ...machinesQuery, refetchInterval: live && 4_000 })

  const make = () =>
    void create.mutateAsync({}).then(
      (created) => setupLink.set(created),
      () => toast("couldn't make a link")
    )

  if (!live) {
    return (
      <div className="space-y-2">
        {link !== null && <p className="text-xs font-bold text-ink/60">that setup link expired.</p>}
        <Button tone="green" className="w-full" disabled={create.isPending} onClick={make}>
          {create.isPending ? "making one…" : "make a setup prompt"}
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
          works once, for {minutes} more minute{minutes === 1 ? "" : "s"}: paste it into one agent
        </span>
        <Button
          tone="red"
          size="sm"
          onClick={() =>
            void revoke.mutateAsync({ path: { id: link.id } }).then(
              () => {
                setupLink.set(null)
                toast("setup link revoked")
              },
              () => toast("couldn't revoke it")
            )
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
const RenameField = ({ machine, onDone }: { machine: Machine; onDone: () => void }) => {
  const rename = useMutation(renameMachineMutation)
  const toast = useToast()
  const [name, setName] = useState(machine.name)
  const save = () => {
    onDone()
    const next = name.trim()
    if (next === "" || next === machine.name) return
    // A promise rather than mutate's callbacks, which stop once the field is gone, as it is by now.
    void rename.mutateAsync({ path: { id: machine.id }, body: { name: next } }).catch(() => toast("couldn't rename it"))
  }
  return (
    <input
      autoFocus
      value={name}
      maxLength={MACHINE_NAME_MAX}
      aria-label={`Rename ${machine.name}`}
      onChange={(e) => setName(e.target.value)}
      onFocus={(e) => e.target.select()}
      onBlur={save}
      onKeyDown={(e) => {
        if (e.key === "Enter") e.currentTarget.blur()
        if (e.key === "Escape") {
          // Don't close the menu along with the field.
          e.stopPropagation()
          setName(machine.name)
          onDone()
        }
      }}
      className={`${NAME_BOX} border-ink bg-white outline-none`}
    />
  )
}

/** Where it runs, as far as `sloppa setup` said: nothing for machines made by hand. */
const facts = (machine: Machine) =>
  [
    machine.hostname !== machine.name && machine.hostname,
    machine.os !== undefined && [machine.os, machine.arch].filter(Boolean).join("/")
  ].filter(Boolean)

const MachineRow = ({ machine, now }: { machine: Machine; now: number }) => {
  const revoke = useMutation(revokeMachineMutation)
  const toast = useToast()
  const [editing, setEditing] = useState(false)
  return (
    <li className="flex items-center gap-3 py-1.5">
      <RobotIcon size={18} className="shrink-0 text-ink/50" />
      <div className="min-w-0 flex-1">
        {editing ? (
          <RenameField machine={machine} onDone={() => setEditing(false)} />
        ) : (
          <p className={`${NAME_BOX} truncate border-transparent`} title={machine.name}>
            {machine.name}
          </p>
        )}
        <p className="truncate text-[11px] font-bold text-ink/55">
          {[
            ...facts(machine),
            machine.lastSeenAt === undefined ? "not seen yet" : `seen ${formatAgo(machine.lastSeenAt, now)}`
          ].join(" · ")}
        </p>
      </div>
      <Button
        tone="cream"
        size="icon"
        aria-label={`Rename ${machine.name}`}
        title="Rename"
        className={SMALL_ICON}
        onClick={() => setEditing(true)}
      >
        <PencilSimpleIcon size={14} />
      </Button>
      <Button
        tone="cream"
        size="icon"
        aria-label={`Revoke ${machine.name}`}
        title="Revoke its token"
        className={SMALL_ICON}
        onClick={() => {
          if (
            !window.confirm(
              `Revoke ${machine.name}? Its token stops working and its previews end, until it's set up again.`
            )
          )
            return
          // The row goes as the list refetches, before mutate's callbacks would run.
          void revoke.mutateAsync({ path: { id: machine.id } }).then(
            () => toast("revoked"),
            () => toast("couldn't revoke it")
          )
        }}
      >
        <XIcon size={14} />
      </Button>
    </li>
  )
}

/** A key made last, kept until dismissed: it is shown this once, so closing the menu mustn't lose it. */
const createdKey = shared<{ readonly name: string; readonly token: string } | null>(null)

const NewKey = ({ onDone }: { onDone: () => void }) => {
  const create = useMutation(createMachineMutation)
  const toast = useToast()
  const [name, setName] = useState("")
  return (
    <form
      className="flex items-center gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        if (name.trim() === "") return
        // A promise, not mutate's callbacks: those are dropped if the menu closes first, and the
        // token would be lost with them.
        void create.mutateAsync({ body: { name: name.trim() } }).then(
          (created) => {
            createdKey.set({ name: created.machine.name, token: created.token })
            onDone()
          },
          () => toast("couldn't make a token")
        )
      }}
    >
      <input
        autoFocus
        value={name}
        maxLength={MACHINE_NAME_MAX}
        placeholder="name, e.g. CI on GitHub"
        aria-label="Machine name"
        onChange={(e) => setName(e.target.value)}
        onKeyDown={(e) => {
          if (e.key !== "Escape") return
          // Don't close the menu along with the field.
          e.stopPropagation()
          onDone()
        }}
        className="panel game-field min-w-0 flex-1 bg-white px-2.5 py-1 text-sm font-bold placeholder:text-ink/40"
      />
      <Button type="submit" tone="green" size="sm" disabled={create.isPending || name.trim() === ""}>
        {create.isPending ? "making…" : "create"}
      </Button>
    </form>
  )
}

const CreatedKey = ({ name, token }: { name: string; token: string }) => {
  return (
    <div className="space-y-2 rounded-lg border-2 border-dashed border-ink/40 p-2.5">
      <div className="flex items-center justify-between gap-3">
        <span className="min-w-0 truncate text-sm font-extrabold" title={name}>
          {name}
        </span>
        <CopyButton value={token} label="copy" toast="token copied" tone="blue" size="sm" />
      </div>
      <code className="block rounded-lg border-2 border-ink bg-white px-2.5 py-1.5 font-mono text-xs break-all">
        <Redacted text={token} secret={token} />
      </code>
      <div className="flex items-center justify-between gap-3 text-xs font-bold text-ink/60">
        <span>shown this once: pipe it into sloppa login, or send it as Authorization: Bearer &lt;token&gt;</span>
        <Button size="sm" onClick={() => createdKey.set(null)}>
          done
        </Button>
      </div>
    </div>
  )
}

const Machines = () => {
  const { data: list = [] } = useQuery(machinesQuery)
  const created = useShared(createdKey)
  const [adding, setAdding] = useState(false)
  const now = useNow(30_000)
  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between gap-3">
        <p className="font-game text-lg">machines</p>
        {!adding && (
          <Button tone="cream" size="sm" onClick={() => setAdding(true)}>
            <span className="flex items-center gap-1.5">
              <KeyIcon size={14} />
              by hand
            </span>
          </Button>
        )}
      </div>
      {adding && <NewKey onDone={() => setAdding(false)} />}
      {created !== null && <CreatedKey name={created.name} token={created.token} />}
      {list.length === 0 ? (
        <p className="text-xs font-bold text-ink/55">none yet: they show up here as they're set up.</p>
      ) : (
        <ul className="divide-y-2 divide-dashed divide-ink/15">
          {list.map((machine) => (
            <MachineRow key={machine.id} machine={machine} now={now} />
          ))}
        </ul>
      )}
    </div>
  )
}

/** A setup prompt to paste into an agent, machines made by hand, and every machine already set up. */
export const MachineSetup = () => (
  <div className="space-y-4 text-left">
    <SetupPrompt />
    <Machines />
  </div>
)

/** The header's way to hook a machine, and the agents on it, up to this server. */
export const ConnectMachine = () => (
  <Popover.Root>
    <Popover.Trigger
      render={
        <Button
          tone="gold"
          size="icon"
          aria-label="Connect a machine"
          title="Connect a machine"
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
            <Popover.Title className="font-game text-xl">connect a machine</Popover.Title>
            <Popover.Description className="text-sm font-semibold text-ink/70">
              paste the prompt into Claude Code, Codex or any agent with a shell: it sets its machine up.
            </Popover.Description>
          </div>
          <MachineSetup />
        </Popover.Popup>
      </Popover.Positioner>
    </Popover.Portal>
  </Popover.Root>
)
