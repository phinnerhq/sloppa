import type { Artifact, ArtifactGroup } from "@sloppa/api"
import { AnimatePresence, motion } from "motion/react"
import { memo, type ReactNode } from "react"
import { groupPagesAtom, NO_PR } from "../api.ts"
import { formatAgoShort, prLabel } from "../format.ts"
import { onLinkClick } from "../router.ts"
import { ItemCount, Paged, pop, Preview, skipOnceRendered } from "./Inventory.tsx"
import { PrName } from "./ui.tsx"

const MOSAIC = "grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-4"

/** Cell layouts for one to four previews; three puts the newest in a tall cell on the left. */
const LAYOUTS = ["", "grid-cols-1", "grid-cols-2", "grid-cols-2 grid-rows-2", "grid-cols-2 grid-rows-2"]

/** The newest few artifacts; past four, the last cell counts the rest instead. */
const Collage = ({ preview, count }: { preview: ReadonlyArray<Artifact>; count: number }) => {
  const shown = count > preview.length ? preview.slice(0, 3) : preview
  const cells = shown.length + (shown.length < count ? 1 : 0)
  return (
    <div className={`grid aspect-square gap-1.5 ${LAYOUTS[cells]}`}>
      {shown.map((artifact, i) => (
        <Preview
          key={artifact.id}
          artifact={artifact}
          className={`min-h-0 ${cells === 3 && i === 0 ? "row-span-2" : ""}`}
        />
      ))}
      {shown.length < count && (
        <span className="flex items-center justify-center rounded-lg border-[3px] border-dashed border-ink/40 font-game text-2xl text-ink/60">
          +{count - shown.length}
        </span>
      )}
    </div>
  )
}

const TITLE = "font-game text-lg leading-tight"

/** `owner/repo #42`; narrowed to one repo, every tile would repeat it, so just the number. */
const ChestName = ({ group, repo }: { group: ArtifactGroup; repo: string }) => {
  if (group.pr === undefined) return <span className={TITLE}>no PR</span>
  if (repo === "")
    return <PrName pr={group.pr} className="text-sm font-extrabold" numberClassName={`ml-1.5 ${TITLE}`} />
  return <span className={`truncate ${TITLE}`}>{/#\d+$/.exec(prLabel(group.pr))?.[0] ?? prLabel(group.pr)}</span>
}

/** A PR as a chest: a peek at its newest artifacts, opening onto all of them. */
const Chest = memo(function Chest({ group, repo }: { group: ArtifactGroup; repo: string }) {
  const params = new URLSearchParams(repo === "" ? {} : { repo })
  params.set("pr", group.pr ?? NO_PR)
  const path = `/?${params}`
  const ago = formatAgoShort(group.lastUploadAt)
  return (
    <a href={path} onClick={onLinkClick(path)} className="pressable pressable-card block">
      <div className="pressable-face p-2.5">
        <Collage preview={group.preview} count={group.count} />
        <p className="mt-2 flex px-0.5" title={group.pr}>
          <ChestName group={group} repo={repo} />
        </p>
        <div className="mt-1 flex items-center gap-2 px-0.5">
          <ItemCount count={group.count} />
          <span className="truncate text-[11px] font-bold text-ink/55">
            {ago === "now" ? "just now" : `${ago} ago`}
          </span>
        </div>
      </div>
    </a>
  )
})

const ChestsSkeleton = ({ count }: { count: number }) => (
  <div className={MOSAIC}>
    {Array.from({ length: count }, (_, i) => (
      <div key={i} className="panel mt-1 p-2.5">
        <div className="grid aspect-square grid-cols-2 gap-1.5">
          {Array.from({ length: 4 }, (_, j) => (
            <div key={j} className="skeleton rounded-lg" />
          ))}
        </div>
        <div className="skeleton mt-3 h-4.5 w-3/4" />
        <div className="skeleton mt-2 mb-0.5 h-4 w-1/2" />
      </div>
    ))}
  </div>
)

/** Every PR at a glance, most recently active first; a filter or search swaps in the flat list. */
export const Mosaic = ({ repo, empty }: { repo: string; empty: ReactNode }) => (
  <Paged
    atom={groupPagesAtom(repo)}
    skeleton={<ChestsSkeleton count={8} />}
    more={<ChestsSkeleton count={4} />}
    empty={empty}
  >
    {(pages) => (
      <div className={`${MOSAIC} relative`}>
        <AnimatePresence mode="popLayout" initial={false}>
          {pages
            .flatMap((page) => page.items)
            .map((group) => (
              <motion.div
                key={group.pr ?? NO_PR}
                ref={skipOnceRendered}
                {...pop}
                className="offscreen-skip [--estimate:22rem]"
              >
                <Chest group={group} repo={repo} />
              </motion.div>
            ))}
        </AnimatePresence>
      </div>
    )}
  </Paged>
)
