import { Combobox } from "@base-ui/react/combobox"
import { useQuery } from "@tanstack/react-query"
import { useRef } from "react"
import { collectionsQuery } from "../api.ts"
import { overlayScrollbar, CollectionName } from "./ui.tsx"
import { CheckIcon, MagnifyingGlassIcon, XIcon } from "./Icon"

/**
 * One field for everything: what's typed searches artifacts, and suggests the collections it matches.
 * Picking one pins it as a chip, so further typing searches inside it; backspace on an empty field
 * (or the chip's ×) lets it go.
 *
 * Base UI's chips are a multiple-selection combobox. Only the newest pick is kept, and its habit of
 * clearing the typed text whenever the popup closes is ignored: here that text is the search.
 */
export const SearchBar = ({
  collection,
  onCollectionChange,
  search,
  onSearchChange
}: {
  /** "" for none. */
  collection: string
  onCollectionChange: (collection: string) => void
  search: string
  onSearchChange: (search: string) => void
}) => {
  const { data: collections = [] } = useQuery(collectionsQuery)
  const counts = new Map(collections.map((c) => [c.collection, c.count]))
  const items = collections.map((c) => c.collection)
  if (collection !== "" && !counts.has(collection)) items.push(collection)
  const field = useRef<HTMLDivElement>(null)

  return (
    <Combobox.Root
      items={items}
      multiple
      value={collection === "" ? [] : [collection]}
      onValueChange={(next) => {
        const picked = next.at(-1) ?? ""
        onCollectionChange(picked)
        // The text found the collection; inside it, start over.
        if (picked !== "") onSearchChange("")
      }}
      inputValue={search}
      onInputValueChange={(next, { reason }) => {
        if (reason !== "input-clear") onSearchChange(next)
      }}
      openOnInputClick
      limit={8}
    >
      <Combobox.Chips
        ref={field}
        className="panel game-field flex w-full cursor-text items-center gap-2 px-4 py-2"
        onClick={(event) => {
          if (event.target === event.currentTarget) event.currentTarget.querySelector("input")?.focus()
        }}
      >
        <MagnifyingGlassIcon size={18} className="shrink-0 text-ink/50" />
        <Combobox.Value>
          {(value: Array<string>) =>
            value.map((picked) => (
              <Combobox.Chip
                key={picked}
                className="flex max-w-[60%] min-w-0 shrink-0 items-center gap-1 rounded-lg border-2 border-ink bg-gold py-0.5 pr-1 pl-2 text-sm font-extrabold outline-none data-highlighted:bg-[#fff0b8]"
              >
                <CollectionName collection={picked} />
                <Combobox.ChipRemove
                  aria-label={`Leave ${picked}`}
                  className="shrink-0 cursor-pointer rounded p-0.5 transition-colors hover:bg-ink/10"
                >
                  <XIcon size={12} />
                </Combobox.ChipRemove>
              </Combobox.Chip>
            ))
          }
        </Combobox.Value>
        <Combobox.Input
          aria-label="Search artifacts and collections"
          placeholder={collection === "" ? "search files, or find a collection…" : "search this collection…"}
          className="min-w-24 flex-1 bg-transparent py-0.5 font-semibold outline-none placeholder:text-ink/40"
        />
      </Combobox.Chips>
      <Combobox.Portal>
        {/* Nothing to suggest is no reason to cover the results: the popup only shows with matches. */}
        <Combobox.Positioner
          anchor={field}
          align="start"
          sideOffset={10}
          className="z-40 outline-none data-empty:hidden"
        >
          <Combobox.Popup className="game-picker min-w-[min(22rem,var(--anchor-width))] p-1.5">
            <p className="px-2.5 pt-1 pb-1.5 text-xs font-extrabold tracking-wide text-ink/50 uppercase">collections</p>
            {/* The overlay scrollbar takes over the list's parent, so the list gets one of its own, under the label. */}
            <div>
              <Combobox.List
                ref={overlayScrollbar}
                className="max-h-[min(20rem,var(--available-height))] overflow-y-auto"
              >
                {(item: string) => (
                  <Combobox.Item key={item} value={item} className="game-option group">
                    <Combobox.ItemIndicator className="col-start-1">
                      <CheckIcon size={14} />
                    </Combobox.ItemIndicator>
                    <CollectionName collection={item} className="col-start-2" />
                    {counts.has(item) && (
                      <span className="col-start-3 rounded-md border-2 border-ink bg-gold px-1.5 font-game text-xs group-data-selected:bg-cream">
                        {counts.get(item)}
                      </span>
                    )}
                  </Combobox.Item>
                )}
              </Combobox.List>
            </div>
          </Combobox.Popup>
        </Combobox.Positioner>
      </Combobox.Portal>
    </Combobox.Root>
  )
}
