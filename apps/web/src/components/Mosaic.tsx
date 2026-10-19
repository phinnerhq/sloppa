import { useInfiniteQuery } from "@tanstack/react-query"
import { AnimatePresence, motion } from "motion/react"
import { memo, type ReactNode } from "react"
import { collectionPath, groupPagesQuery } from "../api.ts"
import type { Artifact, ArtifactGroup } from "../client/types.gen.ts"
import { formatAgoShort } from "../format.ts"
import { onLinkClick } from "../router.ts"
import { ItemCount, Paged, pop, Preview, skipOnceRendered } from "./Inventory.tsx"
import { CollectionName } from "./ui.tsx"

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

/** `owner/repo #42` keeps its number when it truncates; any other name gets the title type whole. */
const ChestName = ({ collection }: { collection: string }) =>
  /#\d+$/.test(collection) ? (
    <CollectionName collection={collection} className="text-sm font-extrabold" />
  ) : (
    <span className={`truncate ${TITLE}`}>{collection}</span>
  )

/** A collection as a chest: a peek at its newest artifacts, opening onto all of them. */
const Chest = memo(function Chest({ group }: { group: ArtifactGroup }) {
  const path = collectionPath(group.collection)
  const ago = formatAgoShort(group.lastUploadAt)
  return (
    <a href={path} onClick={onLinkClick(path)} className="pressable pressable-card block">
      <div className="pressable-face p-2.5">
        <Collage preview={group.preview} count={group.count} />
        <p className="mt-2 flex px-0.5" title={group.collection}>
          <ChestName collection={group.collection} />
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

/** Every collection at a glance, most recently active first; a filter or search swaps in the flat list. */
export const Mosaic = ({ empty }: { empty: ReactNode }) => {
  const query = useInfiniteQuery(groupPagesQuery)
  return (
    <Paged query={query} skeleton={<ChestsSkeleton count={8} />} more={<ChestsSkeleton count={4} />} empty={empty}>
      {(pages) => (
        <div className={`${MOSAIC} relative`}>
          <AnimatePresence mode="popLayout" initial={false}>
            {pages
              .flatMap((page) => page.items)
              .map((group) => (
                <motion.div
                  key={group.collection}
                  ref={skipOnceRendered}
                  {...pop}
                  className="offscreen-skip [--estimate:22rem]"
                >
                  <Chest group={group} />
                </motion.div>
              ))}
          </AnimatePresence>
        </div>
      )}
    </Paged>
  )
}
