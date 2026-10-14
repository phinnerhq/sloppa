import { useSyncExternalStore } from "react"

export type Route =
  | { readonly _tag: "List" }
  | { readonly _tag: "Artifact"; readonly id: string }
  | { readonly _tag: "Credits" }

const parse = (pathname: string): Route => {
  if (/^\/credits\/?$/.test(pathname)) return { _tag: "Credits" }
  const match = /^\/a\/([^/]+)\/?$/.exec(pathname)
  return match?.[1] ? { _tag: "Artifact", id: decodeURIComponent(match[1]) } : { _tag: "List" }
}

const subscribe = (notify: () => void) => {
  window.addEventListener("popstate", notify)
  return () => window.removeEventListener("popstate", notify)
}

const notify = () => window.dispatchEvent(new PopStateEvent("popstate"))

export const useRoute = (): Route => parse(useSyncExternalStore(subscribe, () => window.location.pathname))

export const navigate = (path: string) => {
  window.history.pushState(null, "", path)
  notify()
  window.scrollTo({ top: 0 })
}

/** Plain anchors that stay in the SPA on a normal click but still open in new tabs. */
export const onLinkClick = (path: string) => (event: React.MouseEvent) => {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  navigate(path)
}

/** The list's filters as they were last seen, so "back to the list" keeps them. Tracked outside React. */
let lastListSearch = ""
const rememberListSearch = () => {
  if (parse(window.location.pathname)._tag === "List") lastListSearch = window.location.search
}
rememberListSearch()
window.addEventListener("popstate", rememberListSearch)

export const listPath = () => `/${lastListSearch}`

/** A query-string backed value: filters survive reloads, sharing and back/forward. */
export const useQueryParam = (name: string): [string, (value: string) => void] => {
  const search = useSyncExternalStore(subscribe, () => window.location.search)
  const value = new URLSearchParams(search).get(name) ?? ""
  const set = (next: string) => {
    const params = new URLSearchParams(window.location.search)
    if (next === "") params.delete(name)
    else params.set(name, next)
    const query = params.toString()
    window.history.replaceState(null, "", `${window.location.pathname}${query === "" ? "" : `?${query}`}`)
    notify()
  }
  return [value, set]
}
