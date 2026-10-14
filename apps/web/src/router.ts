import { useEffect, useLayoutEffect, useState, useSyncExternalStore } from "react"

export type Route =
  | { readonly _tag: "List" }
  | { readonly _tag: "Artifact"; readonly id: string }
  | { readonly _tag: "Credits" }

const parse = (pathname: string): Route => {
  if (/^\/credits\/?$/.test(pathname)) return { _tag: "Credits" }
  const match = /^\/a\/([^/]+)\/?$/.exec(pathname)
  return match?.[1] ? { _tag: "Artifact", id: decodeURIComponent(match[1]) } : { _tag: "List" }
}

/** The way a navigation went, so pages can slide accordingly. */
export type Direction = "forward" | "back"

/** A path change; `direction` is null when nothing should animate (first render, or the browser animated it). */
export interface Navigation {
  readonly path: string
  readonly direction: Direction | null
}

/*
 * Every history entry carries a key and its depth: popstate compares depths to know the direction,
 * and scroll positions are kept per key since the browser can't restore them before React renders.
 */
type Entry = { readonly key: number; readonly depth: number }

const isEntry = (state: unknown): state is Entry =>
  typeof state === "object" && state !== null && "key" in state && "depth" in state

window.history.scrollRestoration = "manual"
if (!isEntry(window.history.state)) window.history.replaceState({ key: Date.now(), depth: 0 }, "")
let current = window.history.state as Entry
const scrolls = new Map<number, number>()

const pathListeners = new Set<(navigation: Navigation) => void>()
const beforeListeners = new Set<(navigation: Navigation) => void>()
const searchListeners = new Set<() => void>()
const notifySearch = () => {
  for (const listener of searchListeners) listener()
}

/** The list's filters as they were last seen, so "back to the list" keeps them. */
let lastListSearch = ""
const rememberListSearch = () => {
  if (parse(window.location.pathname)._tag === "List") lastListSearch = window.location.search
}
rememberListSearch()

let pathname = window.location.pathname

const leave = (next: Entry, direction: Direction, uaTransition = false) => {
  scrolls.set(current.key, window.scrollY)
  current = next
  rememberListSearch()
  // A new page mounts with the new query already, telling the old one would only flash it with the wrong filters.
  if (window.location.pathname === pathname) return notifySearch()
  pathname = window.location.pathname
  // A swipe back the browser already animated shouldn't animate twice.
  const navigation = { path: pathname, direction: uaTransition ? null : direction }
  for (const listener of beforeListeners) listener(navigation)
  for (const listener of pathListeners) listener(navigation)
}

window.addEventListener("popstate", (event) => {
  const next = isEntry(event.state) ? event.state : { key: Date.now(), depth: 0 }
  leave(next, next.depth < current.depth ? "back" : "forward", event.hasUAVisualTransition)
})

/**
 * Pages often render a loading state first, too short to scroll back into. Keep scrolling as the page
 * grows, until it fits, the user scrolls themselves, or a couple of seconds pass. Returns the cleanup.
 */
const restoreScroll = (top: number) => {
  window.scrollTo({ top })
  if (top === 0 || window.scrollY >= top - 1) return
  const observer = new ResizeObserver(() => {
    window.scrollTo({ top })
    if (window.scrollY >= top - 1) stop()
  })
  const interactions = ["wheel", "touchstart", "keydown", "pointerdown"] as const
  const timeout = setTimeout(() => stop(), 2000)
  const stop = () => {
    observer.disconnect()
    clearTimeout(timeout)
    for (const type of interactions) window.removeEventListener(type, stop)
  }
  observer.observe(document.body)
  for (const type of interactions) window.addEventListener(type, stop, { passive: true })
  return stop
}

/** The current route and the navigation that led to it. Use it once, at the top of the app. */
export const useRoute = (): { readonly route: Route; readonly navigation: Navigation } => {
  const [navigation, setNavigation] = useState<Navigation>({ path: window.location.pathname, direction: null })
  useEffect(() => {
    pathListeners.add(setNavigation)
    return () => void pathListeners.delete(setNavigation)
  }, [])
  useLayoutEffect(() => restoreScroll(scrolls.get(current.key) ?? 0), [navigation])
  return { route: parse(navigation.path), navigation }
}

/** Runs right before a path change renders, while the old page is still on screen. */
export const useBeforeNavigate = (listener: (navigation: Navigation) => void) =>
  useEffect(() => {
    beforeListeners.add(listener)
    return () => void beforeListeners.delete(listener)
  }, [listener])

export const navigate = (path: string) => {
  const next = { key: Date.now(), depth: current.depth + 1 }
  window.history.pushState(next, "", path)
  leave(next, "forward")
}

/** Plain anchors that stay in the SPA on a normal click but still open in new tabs. */
export const onLinkClick = (path: string) => (event: React.MouseEvent) => {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  navigate(path)
}

export const listPath = () => `/${lastListSearch}`

const subscribeSearch = (listener: () => void) => {
  searchListeners.add(listener)
  return () => void searchListeners.delete(listener)
}

/** A query-string backed value: filters survive reloads, sharing and back/forward. Updates are urgent, never animated. */
export const useQueryParam = (name: string): [string, (value: string) => void] => {
  const search = useSyncExternalStore(subscribeSearch, () => window.location.search)
  const value = new URLSearchParams(search).get(name) ?? ""
  const set = (next: string) => {
    const params = new URLSearchParams(window.location.search)
    if (next === "") params.delete(name)
    else params.set(name, next)
    const query = params.toString()
    window.history.replaceState(current, "", `${window.location.pathname}${query === "" ? "" : `?${query}`}`)
    rememberListSearch()
    notifySearch()
  }
  return [value, set]
}
