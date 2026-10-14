import { RegistryProvider, useAtom } from "@effect/atom-react"
import { IconContext, SignOutIcon } from "@phosphor-icons/react"
import { OverlayScrollbars } from "overlayscrollbars"
import { type ReactElement, type Ref, StrictMode, useCallback, useLayoutEffect, useRef, useState } from "react"
import { createRoot } from "react-dom/client"
import { tokenAtom } from "./api.ts"
import { ArtifactList } from "./components/ArtifactList.tsx"
import { ArtifactView } from "./components/ArtifactView.tsx"
import { Credits } from "./components/Credits.tsx"
import { Button, Doodles, Floppa, Toaster } from "./components/ui.tsx"
import "./index.css"
import { type Direction, type Navigation, onLinkClick, type Route, useBeforeNavigate, useRoute } from "./router.ts"

const Session = () => {
  const [token, setToken] = useAtom(tokenAtom)
  if (token === null) return null
  return (
    <Button onClick={() => setToken(null)} aria-label="Sign out" title="Sign out" size="icon">
      <SignOutIcon size={20} />
    </Button>
  )
}

const FooterLink = ({ href, children, external }: { href: string; children: string; external?: boolean }) => (
  <a
    href={href}
    {...(external ? { target: "_blank", rel: "noreferrer" } : { onClick: onLinkClick(href) })}
    className="underline decoration-sky-deep decoration-2 underline-offset-4 transition-colors hover:text-sky-deep"
  >
    {children}
  </a>
)

const Footer = ({ ref }: { ref: Ref<HTMLElement> }) => (
  <footer ref={ref} className="panel mt-16 flex flex-col items-center gap-3 px-5 py-3 sm:flex-row sm:justify-between">
    <p className="flex items-center gap-2.5 font-game text-base">
      <Floppa mood="loaf" className="h-9 w-auto" />
      <span>
        made by{" "}
        <FooterLink href="https://phinner.dev" external>
          phinner
        </FooterLink>
      </span>
    </p>
    <nav className="flex items-center gap-5 text-sm font-extrabold">
      <FooterLink href="/credits">credits</FooterLink>
      <FooterLink href="/api/openapi.json" external>
        openapi.json
      </FooterLink>
      <FooterLink href="https://github.com/phinnerhq/sloppa-artifacts" external>
        source
      </FooterLink>
    </nav>
  </footer>
)

const Page = ({ route }: { route: Route }) =>
  route._tag === "List" ? (
    <ArtifactList />
  ) : route._tag === "Credits" ? (
    <Credits />
  ) : (
    <ArtifactView key={route.id} id={route.id} />
  )

const EASE_OUT = "cubic-bezier(0.22, 1, 0.36, 1)"
const EASE_IN = "cubic-bezier(0.5, 0, 0.75, 0)"
const pageTop = (element: HTMLElement) => element.getBoundingClientRect().top + window.scrollY
const reducedMotion = () => matchMedia("(prefers-reduced-motion: reduce)").matches

const pageKey = (route: Route) => (route._tag === "Artifact" ? `artifact/${route.id}` : route._tag)

const riseFrom = (direction: Direction) => `translateY(${direction === "forward" ? 16 : -12}px)`
const sinkTo = (direction: Direction) => `translateY(${direction === "forward" ? -10 : 14}px)`

/**
 * The header isn't part of the page, but a scroll jump moves it on screen all the same. It rises in with the
 * page when it scrolls into view, glides when it only shifts a little, and when it scrolls out of view a
 * copy fades out in its place. Returns the cleanup.
 */
const moveHeader = (header: HTMLElement, from: DOMRect, direction: Direction) => {
  const to = header.getBoundingClientRect()
  const shift = from.top - to.top
  if (Math.abs(shift) < 1) return () => {}
  const visibleBefore = from.bottom > 0 && from.top < window.innerHeight
  const visibleAfter = to.bottom > 0 && to.top < window.innerHeight
  if (visibleAfter && (!visibleBefore || Math.abs(shift) > to.height)) {
    const rise = header.animate([{ opacity: 0, transform: riseFrom(direction) }, { opacity: 1 }], {
      duration: 300,
      delay: 70,
      easing: EASE_OUT,
      fill: "backwards"
    })
    return () => rise.cancel()
  }
  if (visibleAfter) {
    const glide = header.animate([{ transform: `translateY(${shift}px)` }, { transform: "none" }], {
      duration: 380,
      easing: EASE_OUT
    })
    return () => glide.cancel()
  }
  if (!visibleBefore) return () => {}
  const ghost = header.cloneNode(true) as HTMLElement
  ghost.inert = true
  Object.assign(ghost.style, {
    position: "fixed",
    top: `${from.top}px`,
    left: `${from.left}px`,
    width: `${from.width}px`,
    margin: "0",
    pointerEvents: "none"
  })
  header.after(ghost)
  const fade = ghost.animate([{ opacity: 1 }, { opacity: 0, transform: sinkTo(direction) }], {
    duration: 150,
    easing: EASE_IN,
    fill: "forwards"
  })
  fade.onfinish = () => ghost.remove()
  return () => {
    fade.cancel()
    ghost.remove()
  }
}

interface PageEntry {
  readonly key: string
  readonly page: ReactElement
}

/**
 * Page transitions, done by hand: the old page stays mounted a moment to fade out where it was, the new one
 * rises in from the side it's coming from, and the footer glides from where the old page ended.
 * No View Transition API: Gecko still flashes blank frames around them.
 */
const usePageTransitions = (route: Route, navigation: Navigation) => {
  const current: PageEntry = { key: pageKey(route), page: <Page route={route} /> }
  const latest = useRef(current)
  useLayoutEffect(() => {
    latest.current = current
  })
  const [leaving, setLeaving] = useState<PageEntry | null>(null)
  const leavingScroll = useRef(0)
  const footerFrom = useRef<number | null>(null)
  const entering = useRef<HTMLDivElement>(null)
  const exiting = useRef<HTMLDivElement>(null)
  const footer = useRef<HTMLElement>(null)
  const header = useRef<HTMLElement>(null)
  const headerFrom = useRef<DOMRect | null>(null)

  useBeforeNavigate(
    useCallback(({ direction }: Navigation) => {
      footerFrom.current = footer.current === null ? null : pageTop(footer.current)
      headerFrom.current = header.current?.getBoundingClientRect() ?? null
      leavingScroll.current = window.scrollY
      // The same element object, so React keeps the old page mounted without re-rendering it.
      setLeaving(direction === null || reducedMotion() ? null : latest.current)
    }, [])
  )

  useLayoutEffect(() => {
    const { direction } = navigation
    const from = footerFrom.current
    const headerRect = headerFrom.current
    footerFrom.current = null
    headerFrom.current = null
    if (direction === null || reducedMotion()) return
    const out = exiting.current
    let exit: Animation | undefined
    if (out !== null) {
      // Pinned where it was on screen, whatever the new page scrolled to, and cut off where main begins
      // so it never covers the header.
      const offset = window.scrollY - leavingScroll.current
      out.style.top = `${offset}px`
      out.style.clipPath = `inset(${Math.max(0, -offset)}px 0 0 0)`
      exit = out.animate([{ opacity: 1 }, { opacity: 0, transform: sinkTo(direction) }], {
        duration: 150,
        easing: EASE_IN,
        fill: "forwards"
      })
      exit.onfinish = () => setLeaving(null)
    }
    const stopHeader =
      header.current === null || headerRect === null ? () => {} : moveHeader(header.current, headerRect, direction)
    const enter = entering.current?.animate([{ opacity: 0, transform: riseFrom(direction) }, { opacity: 1 }], {
      duration: 300,
      delay: exit ? 70 : 0,
      easing: EASE_OUT,
      fill: "backwards"
    })
    const shift = from === null || footer.current === null ? 0 : from - pageTop(footer.current)
    const glide =
      shift === 0
        ? undefined
        : footer.current?.animate([{ transform: `translateY(${shift}px)` }, { transform: "none" }], {
            duration: 380,
            easing: EASE_OUT
          })
    return () => {
      exit?.cancel()
      enter?.cancel()
      glide?.cancel()
      stopHeader()
    }
  }, [navigation])

  const pages = (
    <>
      {leaving !== null && leaving.key !== current.key && (
        <div key={leaving.key} ref={exiting} inert className="pointer-events-none absolute inset-x-0 top-0">
          {leaving.page}
        </div>
      )}
      <div key={current.key} ref={entering}>
        {current.page}
      </div>
    </>
  )
  return { pages, header, footer }
}

const App = () => {
  const { route, navigation } = useRoute()
  const { pages, header, footer } = usePageTransitions(route, navigation)
  return (
    <div className="mx-auto max-w-6xl px-4 py-6 sm:px-6">
      <Doodles />
      <header ref={header} className="mb-8 flex flex-wrap items-center justify-between gap-3">
        <a href="/" onClick={onLinkClick("/")} className="panel mt-4 flex items-center gap-3 py-1.5 pr-4 pl-2">
          <Floppa mood="hiss" className="-mt-9 -mb-3 h-20 w-auto" />
          <span className="font-game text-2xl leading-none">sloppa artifacts</span>
        </a>
        <div className="flex items-center gap-2.5">
          <Session />
        </div>
      </header>
      <main className="relative">{pages}</main>
      <Footer ref={footer} />
      <Toaster />
    </div>
  )
}

// An overlay scrollbar for the page, so content never shifts when it appears or goes.
OverlayScrollbars(document.body, {
  scrollbars: { theme: "os-theme-sloppa", autoHide: "move", autoHideDelay: 900, clickScroll: true }
})

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <RegistryProvider>
      <IconContext.Provider value={{ weight: "bold" }}>
        <App />
      </IconContext.Provider>
    </RegistryProvider>
  </StrictMode>
)
