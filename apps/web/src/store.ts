import { useSyncExternalStore } from "react"

/** A value kept while the tab lives, outside any component: closing the menu showing it doesn't lose it. */
export interface Shared<A> {
  readonly get: () => A
  readonly set: (value: A) => void
  readonly subscribe: (listener: () => void) => () => void
}

export const shared = <A>(initial: A): Shared<A> => {
  let value = initial
  const listeners = new Set<() => void>()
  return {
    get: () => value,
    set: (next) => {
      value = next
      for (const listener of listeners) listener()
    },
    subscribe: (listener) => {
      listeners.add(listener)
      return () => void listeners.delete(listener)
    }
  }
}

export const useShared = <A>(state: Shared<A>) => useSyncExternalStore(state.subscribe, state.get)
