import { useAtomSet, useAtomValue } from "@effect/atom-react"
import { type Artifact, ArtifactId } from "@sloppa/api"
import { Exit, Option, Schema } from "effect"
import { AsyncResult } from "effect/reactivity"
import { Dialog } from "@base-ui/react/dialog"
import { type MouseEvent, type ReactNode, useLayoutEffect, useRef, useState } from "react"
import {
  artifactAtom,
  collectionPath,
  deleteArtifactAtom,
  invalidateArtifacts,
  MAX_TEXT_PREVIEW,
  textPreviewAtom
} from "../api.ts"
import { formatAgo, formatDate, formatSize, kindOf, pullRequestUrl } from "../format.ts"
import { listPath, navigate, onLinkClick, returnTo } from "../router.ts"
import { useSignedIn } from "./Login.tsx"
import {
  Button,
  ButtonLink,
  CollectionName,
  CopyButton,
  LinkButton,
  Loading,
  Notice,
  overlayScrollbarWith,
  scrollbars,
  useToast
} from "./ui.tsx"
import {
  ArrowLeftIcon,
  ArrowSquareOutIcon,
  BrowserIcon,
  CircleNotchIcon,
  DownloadSimpleIcon,
  LinkIcon,
  TrashIcon,
  XIcon
} from "./Icon"

const TextPreview = ({ url }: { url: string }) => {
  const result = useAtomValue(textPreviewAtom(url))
  return AsyncResult.match(result, {
    onInitial: () => <Loading label="loading file…" />,
    onFailure: () => <Notice title="couldn't load this file" mood="hiss" bare />,
    onSuccess: ({ value }) => (
      <pre className="max-h-[70vh] overflow-auto bg-[#1d2433] p-5 font-mono text-[13px] leading-relaxed break-words whitespace-pre-wrap text-[#e8edf6]">
        {value}
      </pre>
    )
  })
}

const TooBig = ({ artifact, title }: { artifact: Artifact; title: string }) => (
  <Notice title={title} mood="loaf" bare>
    <div className="mt-2">
      <LinkButton href={`${artifact.rawUrl}?download`} download tone="gold">
        download {formatSize(artifact.size)}
      </LinkButton>
    </div>
  </Notice>
)

/**
 * Images and videos play on a stage sized before any of their bytes arrive: nothing tells us their
 * dimensions up front, and a box that grows as they load would shove the page around on a slow link.
 */
const STAGE = "flex max-h-[70vh] w-full items-center justify-center"

/** Where a zooming click landed: as fractions of the image, and as a point on screen. */
interface Anchor {
  readonly fx: number
  readonly fy: number
  readonly x: number
  readonly y: number
}

/** Whether the image is drawn smaller than it is, so showing it at its own size would enlarge it. */
const shrunk = (img: HTMLImageElement) => img.naturalWidth > img.clientWidth || img.naturalHeight > img.clientHeight

/**
 * Fit to the screen at first. A click shows it at its own size with the clicked pixel kept under the
 * cursor, the lightbox scrolling to pan; another click fits it again. Mounted only while open, so
 * every opening starts fitted.
 */
const ZoomableImage = ({ artifact }: { artifact: Artifact }) => {
  const img = useRef<HTMLImageElement>(null)
  const [zoom, setZoom] = useState<Anchor | null>(null)
  const [zoomable, setZoomable] = useState(false)

  useLayoutEffect(() => {
    const box = img.current?.getBoundingClientRect()
    if (zoom === null || box === undefined) return
    img.current?.closest("[data-lightbox]")?.scrollBy({
      left: box.left + zoom.fx * box.width - zoom.x,
      top: box.top + zoom.fy * box.height - zoom.y,
      behavior: "instant"
    })
  }, [zoom])

  const toggle = (e: MouseEvent<HTMLImageElement>) => {
    if (zoom !== null) return setZoom(null)
    if (!shrunk(e.currentTarget)) return
    const box = e.currentTarget.getBoundingClientRect()
    setZoom({
      fx: (e.clientX - box.left) / box.width,
      fy: (e.clientY - box.top) / box.height,
      x: e.clientX,
      y: e.clientY
    })
  }

  return (
    <img
      ref={img}
      src={artifact.rawUrl}
      alt={artifact.name}
      onLoad={(e) => setZoomable(shrunk(e.currentTarget))}
      onPointerEnter={(e) => zoom === null && setZoomable(shrunk(e.currentTarget))}
      onClick={toggle}
      className={`checkerboard ${
        zoom !== null
          ? "max-w-none cursor-zoom-out"
          : `max-h-[calc(100dvh-3rem)] max-w-[calc(100vw-3rem)] ${zoomable ? "cursor-zoom-in" : ""}`
      }`}
    />
  )
}

/** The original over the whole page. Esc, the close button, or a click beside the image closes it. */
const lightboxScrollbar = overlayScrollbarWith(scrollbars("move", "os-theme-sloppa-cream"))

const Lightbox = ({ artifact }: { artifact: Artifact }) => (
  <Dialog.Portal>
    <Dialog.Backdrop className="fixed inset-0 z-50 bg-ink/85 transition-opacity duration-200 data-ending-style:opacity-0 data-starting-style:opacity-0" />
    <Dialog.Viewport className="fixed inset-0 z-50">
      {/* OverlayScrollbars makes its frame position: relative and owns the scroller's padding: hence the layers. */}
      <div className="size-full">
        <div ref={lightboxScrollbar} data-lightbox className="size-full overflow-auto">
          <div className="flex min-h-full w-max min-w-full p-6">
            {/* Opacity only: a transform here would pin the close button to the popup, not the screen. */}
            <Dialog.Popup className="m-auto outline-none transition-opacity duration-200 data-ending-style:opacity-0 data-starting-style:opacity-0">
              <Dialog.Title className="sr-only">{artifact.name}</Dialog.Title>
              <ZoomableImage artifact={artifact} />
              <Dialog.Close
                render={
                  <Button tone="cream" size="icon" aria-label="Close" className="fixed top-4 right-4">
                    <XIcon size={18} />
                  </Button>
                }
              />
            </Dialog.Popup>
          </div>
        </div>
      </div>
    </Dialog.Viewport>
  </Dialog.Portal>
)

/**
 * The thumbnail stands in until the original lands. The grid already fetched it, and it's cached for
 * good, so it's usually there at once; the original fades in over it. Clicking opens the lightbox.
 */
const ImagePreview = ({ artifact }: { artifact: Artifact }) => {
  const [loaded, setLoaded] = useState(false)
  return (
    <Dialog.Root>
      <Dialog.Trigger
        aria-label={`Enlarge ${artifact.name}`}
        className={`checkerboard relative aspect-4/3 cursor-zoom-in p-4 ${STAGE}`}
      >
        {artifact.thumbUrl !== undefined && !loaded && (
          <img
            src={artifact.thumbUrl}
            alt=""
            aria-hidden
            className="absolute inset-4 size-[calc(100%-2rem)] object-contain blur-[2px]"
          />
        )}
        <img
          // Already cached, the image may be complete before React would see its load event.
          ref={(img) => {
            if (img?.complete && img.naturalWidth > 0) setLoaded(true)
          }}
          src={artifact.rawUrl}
          alt={artifact.name}
          onLoad={() => setLoaded(true)}
          className={`relative max-h-full max-w-full object-contain transition-opacity duration-200 ${
            loaded ? "opacity-100" : "opacity-0"
          }`}
        />
      </Dialog.Trigger>
      <Lightbox artifact={artifact} />
    </Dialog.Root>
  )
}

const Preview = ({ artifact }: { artifact: Artifact }) => {
  switch (kindOf(artifact)) {
    case "image":
      return <ImagePreview key={artifact.id} artifact={artifact} />
    case "video":
      return <video src={artifact.rawUrl} controls className={`aspect-video bg-black ${STAGE}`} />
    case "audio":
      return (
        <div className="p-8">
          <audio src={artifact.rawUrl} controls className="w-full" />
        </div>
      )
    case "pdf":
      return <iframe src={artifact.rawUrl} title={artifact.name} className="h-[70vh] w-full bg-white" />
    case "html":
      // The raw route already serves HTML with a CSP sandbox; the iframe sandbox doubles down.
      return (
        <iframe
          src={artifact.rawUrl}
          title={artifact.name}
          sandbox="allow-scripts allow-popups"
          className="h-[70vh] w-full bg-white"
        />
      )
    case "text":
      return artifact.size <= MAX_TEXT_PREVIEW ? (
        <TextPreview url={artifact.rawUrl} />
      ) : (
        <TooBig artifact={artifact} title="too large to preview" />
      )
    case "binary":
      return <TooBig artifact={artifact} title="no preview for this file type" />
  }
}

const SellButton = ({ artifact }: { artifact: Artifact }) => {
  const remove = useAtomSet(deleteArtifactAtom, { mode: "promiseExit" })
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  return (
    <Button
      tone="red"
      disabled={busy}
      onClick={() => {
        if (!window.confirm(`Delete ${artifact.name}? Links to it will stop working.`)) return
        setBusy(true)
        void remove({ params: { id: artifact.id }, reactivityKeys: invalidateArtifacts }).then((exit) => {
          setBusy(false)
          if (Exit.isSuccess(exit)) {
            toast("deleted")
            navigate(listPath())
          } else {
            toast("delete failed")
          }
        })
      }}
    >
      {busy ? (
        <>
          <CircleNotchIcon size={18} className="animate-spin" /> deleting…
        </>
      ) : (
        <>
          <TrashIcon size={18} /> delete file
        </>
      )}
    </Button>
  )
}

const SignedIn = ({ children }: { children: ReactNode }) => (useSignedIn() ? children : null)

const Stat = ({ label, children }: { label: string; children: ReactNode }) => (
  <div className="flex items-baseline justify-between gap-4 border-b-2 border-dashed border-ink/15 py-1.5 last:border-0">
    <dt className="font-game text-sm text-ink/60">{label}</dt>
    <dd className="min-w-0 truncate text-right text-sm font-extrabold">{children}</dd>
  </div>
)

/**
 * Signed in, onto the rest of the collection; signed out (someone following a shared link), the
 * dashboard is closed to them, so only a collection named after a PR links anywhere: to the PR.
 */
const CollectionLink = ({ collection }: { collection: string }) => {
  const signedIn = useSignedIn()
  const name = <CollectionName collection={collection} />
  const link = "flex justify-end text-sky-deep hover:underline"
  if (signedIn) {
    const path = collectionPath(collection)
    return (
      <a href={path} onClick={onLinkClick(path)} className={link}>
        {name}
      </a>
    )
  }
  const pr = pullRequestUrl(collection)
  return pr === undefined ? (
    <span className="flex justify-end">{name}</span>
  ) : (
    <a href={pr} target="_blank" rel="noreferrer" className={link}>
      {name}
    </a>
  )
}

const Inspector = ({ artifact }: { artifact: Artifact }) => {
  // minmax(0, …): a long name in the sidebar truncates instead of widening the page.
  return (
    <div className="grid grid-cols-1 items-start gap-5 lg:grid-cols-[minmax(0,1fr)_300px]">
      <div className="panel animate-rise-in overflow-hidden">
        <Preview artifact={artifact} />
      </div>
      <aside className="panel animate-rise-in space-y-4 p-4">
        <h1 className="font-game text-2xl leading-tight break-all">{artifact.name}</h1>
        <dl>
          <Stat label="Collection">
            <CollectionLink collection={artifact.collection} />
          </Stat>
          <Stat label="Type">
            <code className="font-mono text-xs">{artifact.contentType}</code>
          </Stat>
          <Stat label="Size">{formatSize(artifact.size)}</Stat>
          <Stat label="Uploaded">
            <span title={formatDate(artifact.createdAt)}>{formatAgo(artifact.createdAt)}</span>
          </Stat>
        </dl>
        <div className="grid gap-2.5">
          <CopyButton
            label="copy file URL"
            icon={<LinkIcon size={18} />}
            value={artifact.rawUrl}
            toast="file URL copied"
            tone="green"
          />
          <CopyButton
            label="copy page URL"
            icon={<BrowserIcon size={18} />}
            value={artifact.url}
            toast="page URL copied"
            tone="blue"
          />
          <div className="grid grid-cols-2 gap-2.5">
            <ButtonLink href={artifact.rawUrl} target="_blank" rel="noreferrer">
              <ArrowSquareOutIcon size={18} /> open file
            </ButtonLink>
            <ButtonLink href={`${artifact.rawUrl}?download`} tone="gold">
              <DownloadSimpleIcon size={18} /> download
            </ButtonLink>
          </div>
          <SignedIn>
            <SellButton artifact={artifact} />
          </SignedIn>
        </div>
        <p className="text-center font-mono text-[11px] break-all text-ink/40">{artifact.id}</p>
      </aside>
    </div>
  )
}

const Found = ({ id }: { id: ArtifactId }) => {
  const result = useAtomValue(artifactAtom(id))
  return AsyncResult.matchWithError(result, {
    onInitial: () => <Loading label="loading artifact…" />,
    onError: (error) =>
      error._tag === "artifact_not_found" ? (
        <Notice title="artifact not found" mood="hiss">
          it may have been deleted.
        </Notice>
      ) : (
        <Notice title="storage error" mood="hiss">
          {error.message}
        </Notice>
      ),
    onDefect: () => <Notice title="couldn't reach the server" mood="hiss" />,
    onSuccess: ({ value }) => <Inspector artifact={value} />
  })
}

const decodeId = Schema.decodeUnknownOption(ArtifactId)

export const ArtifactView = ({ id }: { id: string }) => (
  <div className="space-y-5">
    {/* The list is behind sign-in: a shared link's visitor has nowhere to go back to. */}
    <SignedIn>
      <ButtonLink href={listPath()} onClick={onLinkClick(listPath(), returnTo)} size="sm">
        <ArrowLeftIcon size={16} /> all artifacts
      </ButtonLink>
    </SignedIn>
    {Option.match(decodeId(id), {
      onNone: () => (
        <Notice title="artifact not found" mood="hiss">
          that isn't a valid artifact id.
        </Notice>
      ),
      onSome: (id) => <Found id={id} />
    })}
  </div>
)
