import { RegistryProvider, useAtom } from "@effect/atom-react"
import { IconContext, SignOutIcon } from "@phosphor-icons/react"
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { tokenAtom } from "./api.ts"
import { ArtifactList } from "./components/ArtifactList.tsx"
import { ArtifactView } from "./components/ArtifactView.tsx"
import { Credits } from "./components/Credits.tsx"
import { Floppa, Toaster } from "./components/ui.tsx"
import "./index.css"
import { onLinkClick, useRoute } from "./router.ts"

const Session = () => {
  const [token, setToken] = useAtom(tokenAtom)
  if (token === null) return null
  return (
    <button
      type="button"
      onClick={() => setToken(null)}
      aria-label="Sign out"
      title="Sign out"
      className="game-btn game-btn-cream !p-2"
    >
      <SignOutIcon size={20} />
    </button>
  )
}

const Footer = () => (
  <footer className="mt-16 pb-4 text-center text-xs font-semibold text-ink/50">
    made by{" "}
    <a className="underline" href="https://phinner.dev" target="_blank" rel="noreferrer">
      phinner
    </a>{" "}
    ·{" "}
    <a className="underline" href="/credits" onClick={onLinkClick("/credits")}>
      credits
    </a>{" "}
    ·{" "}
    <a className="underline" href="/api/openapi.json" target="_blank" rel="noreferrer">
      openapi.json
    </a>
  </footer>
)

const App = () => {
  const route = useRoute()
  return (
    <div className="mx-auto max-w-6xl px-4 py-6 sm:px-6">
      <header className="mb-8 flex flex-wrap items-center justify-between gap-3">
        <a href="/" onClick={onLinkClick("/")} className="panel mt-4 flex items-center gap-3 py-1.5 pr-4 pl-2">
          <Floppa mood="hiss" className="-mt-9 -mb-3 h-20 w-auto" />
          <span className="font-game text-2xl leading-none">sloppa artifacts</span>
        </a>
        <div className="flex items-center gap-2.5">
          <Session />
        </div>
      </header>
      <main>
        {route._tag === "List" ? (
          <ArtifactList />
        ) : route._tag === "Credits" ? (
          <Credits />
        ) : (
          <ArtifactView key={route.id} id={route.id} />
        )}
      </main>
      <Footer />
      <Toaster />
    </div>
  )
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <RegistryProvider>
      <IconContext.Provider value={{ weight: "bold" }}>
        <App />
      </IconContext.Provider>
    </RegistryProvider>
  </StrictMode>
)
