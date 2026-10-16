import { useAtomRefresh, useAtomSet, useAtomValue } from "@effect/atom-react"
import { Popover } from "@base-ui/react/popover"
import { ArrowSquareOutIcon, BroadcastIcon, LockSimpleIcon, StopIcon } from "@phosphor-icons/react"
import type { Preview } from "@sloppa/api"
import { DateTime, Exit } from "effect"
import { AsyncResult } from "effect/reactivity"
import { useEffect, useState } from "react"
import { invalidatePreviews, previewsAtom, stopPreviewAtom } from "../api.ts"
import { formatAgoShort } from "../format.ts"
import { Button, CopyButton, overlayScrollbar, useNow, useToast } from "./ui.tsx"

const START = "sloppa preview run -- pnpm dev"

/** "2h left", "12m left": how long until the preview stops by itself. */
const timeLeft = (preview: Preview, now: number) => {
  const minutes = Math.max(0, Math.round((DateTime.toEpochMillis(preview.expiresAt) - now) / 60_000))
  return minutes >= 90 ? `${Math.round(minutes / 60)}h left` : `${minutes}m left`
}

/** The last directory or two, which say which checkout it runs from; the whole path on hover. */
const shortPath = (path: string) => path.split("/").filter(Boolean).slice(-2).join("/")

const PreviewRow = ({ preview, now }: { preview: Preview; now: number }) => {
  const stop = useAtomSet(stopPreviewAtom, { mode: "promiseExit" })
  const toast = useToast()
  const host = new URL(preview.url).host
  return (
    <li className="flex items-start gap-3 py-2">
      <span className="relative mt-1.5 flex size-2.5 shrink-0" aria-hidden>
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-cash opacity-50" />
        <span className="relative inline-flex size-2.5 rounded-full border border-ink bg-cash" />
      </span>
      <div className="min-w-0 flex-1 space-y-0.5">
        <a
          href={preview.url}
          target="_blank"
          rel="noreferrer"
          title={preview.url}
          className="flex min-w-0 items-center gap-1 text-sm font-extrabold hover:text-sky-deep"
        >
          {preview.protected && <LockSimpleIcon size={13} className="shrink-0" aria-label="password" />}
          <span className="truncate">{host}</span>
          <ArrowSquareOutIcon size={13} className="shrink-0 opacity-60" />
        </a>
        {preview.command !== undefined && (
          <p className="truncate font-mono text-[11px] text-ink/75" title={preview.command}>
            {preview.command}
          </p>
        )}
        <p className="truncate text-[11px] font-bold text-ink/55" title={preview.cwd}>
          {[
            preview.cwd === undefined ? undefined : shortPath(preview.cwd),
            preview.hostname,
            `up ${formatAgoShort(preview.connectedAt, now)}`,
            timeLeft(preview, now),
            preview.connections > 0 ? `${preview.connections} open` : undefined
          ]
            .filter((part) => part !== undefined)
            .join(" · ")}
        </p>
      </div>
      <Button
        tone="cream"
        size="icon"
        aria-label={`Stop ${host}`}
        title="Stop it, and its dev server"
        className="[&>.pressable-face]:size-8"
        onClick={() =>
          void stop({ params: { id: preview.id }, reactivityKeys: invalidatePreviews }).then((exit) =>
            toast(Exit.isSuccess(exit) ? "stopped" : "couldn't stop it")
          )
        }
      >
        <StopIcon size={14} weight="fill" />
      </Button>
    </li>
  )
}

const PreviewList = () => {
  const previews = useAtomValue(previewsAtom)
  const refresh = useAtomRefresh(previewsAtom)
  const now = useNow(15_000)
  // Previews come and go with their commands: keep up while the menu is open.
  useEffect(() => {
    const timer = setInterval(refresh, 4_000)
    return () => clearInterval(timer)
  }, [refresh])

  if (AsyncResult.isFailure(previews)) {
    return <p className="text-xs font-bold text-ink/60">previews aren't set up on this server.</p>
  }
  const list = AsyncResult.isSuccess(previews) ? previews.value : []
  return (
    <div className="space-y-2">
      {list.length === 0 ? (
        <p className="text-xs font-bold text-ink/55">
          nothing running. an agent shares a dev server with the command below, until it stops.
        </p>
      ) : (
        // Its own box: the overlay scrollbar takes over the list's parent.
        <div>
          <ul
            ref={overlayScrollbar}
            className="max-h-[min(22rem,50vh)] divide-y-2 divide-dashed divide-ink/15 overflow-y-auto"
          >
            {list.map((preview) => (
              <PreviewRow key={preview.id} preview={preview} now={now} />
            ))}
          </ul>
        </div>
      )}
      <div className="flex items-center gap-2 rounded-lg border-2 border-dashed border-ink/40 p-2">
        <code className="min-w-0 flex-1 truncate font-mono text-xs">{START}</code>
        <CopyButton value={START} label="copy" toast="copied" tone="blue" size="sm" />
      </div>
    </div>
  )
}

/** The header's list of live previews: one dot per running `sloppa preview run`. */
export const Previews = () => {
  const previews = useAtomValue(previewsAtom)
  const refresh = useAtomRefresh(previewsAtom)
  const [open, setOpen] = useState(false)
  const count = AsyncResult.isSuccess(previews) ? previews.value.length : 0
  return (
    <Popover.Root
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (next) refresh()
      }}
    >
      <Popover.Trigger
        render={
          <Button
            tone="green"
            size="icon"
            aria-label={`Previews (${count} live)`}
            title="Previews"
            className="relative [&>.pressable-face]:size-12.5"
          >
            <BroadcastIcon size={22} />
            {count > 0 && (
              <span className="absolute -top-1.5 -right-1.5 z-10 grid min-w-5 place-items-center rounded-full border-2 border-ink bg-gold px-1 font-game text-xs leading-4">
                {count}
              </span>
            )}
          </Button>
        }
      />
      <Popover.Portal>
        <Popover.Positioner align="end" sideOffset={10} collisionPadding={12} className="z-40 outline-none">
          <Popover.Popup className="game-picker w-[min(28rem,calc(100vw-1.5rem))] space-y-3 p-4">
            <div>
              <Popover.Title className="font-game text-xl">live previews</Popover.Title>
              <Popover.Description className="text-sm font-semibold text-ink/70">
                dev servers your agents are sharing. each lives exactly as long as its command.
              </Popover.Description>
            </div>
            {open && <PreviewList />}
          </Popover.Popup>
        </Popover.Positioner>
      </Popover.Portal>
    </Popover.Root>
  )
}
