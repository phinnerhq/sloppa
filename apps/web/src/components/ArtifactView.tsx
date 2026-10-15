import { useAtomSet, useAtomValue } from "@effect/atom-react"
import { ArrowLeftIcon } from "@phosphor-icons/react"
import { type Artifact, ArtifactId } from "@sloppa/api"
import { Exit, Option, Schema } from "effect"
import { AsyncResult } from "effect/reactivity"
import { type ReactNode, useState } from "react"
import {
  artifactAtom,
  deleteArtifactAtom,
  invalidateArtifacts,
  MAX_TEXT_PREVIEW,
  textPreviewAtom,
  tokenAtom
} from "../api.ts"
import { formatAgo, formatDate, formatSize, kindOf, markdownFor } from "../format.ts"
import { listPath, navigate, onLinkClick, returnTo } from "../router.ts"
import { Button, ButtonLink, CopyButton, LinkButton, Loading, Notice, PrName, useToast } from "./ui.tsx"

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

const Preview = ({ artifact }: { artifact: Artifact }) => {
  switch (kindOf(artifact)) {
    case "image":
      return (
        <a href={artifact.rawUrl} target="_blank" rel="noreferrer" className="checkerboard flex justify-center p-4">
          <img src={artifact.rawUrl} alt={artifact.name} className="max-h-[70vh] max-w-full object-contain" />
        </a>
      )
    case "video":
      return <video src={artifact.rawUrl} controls className="max-h-[70vh] w-full bg-black" />
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
      {busy ? "deleting…" : "delete"}
    </Button>
  )
}

const SignedIn = ({ children }: { children: ReactNode }) => (useAtomValue(tokenAtom) === null ? null : children)

const Stat = ({ label, children }: { label: string; children: ReactNode }) => (
  <div className="flex items-baseline justify-between gap-4 border-b-2 border-dashed border-ink/15 py-1.5 last:border-0">
    <dt className="font-game text-sm text-ink/60">{label}</dt>
    <dd className="min-w-0 truncate text-right text-sm font-extrabold">{children}</dd>
  </div>
)

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
          {artifact.pr !== undefined && (
            <Stat label="PR">
              <a
                href={artifact.pr}
                target="_blank"
                rel="noreferrer"
                className="flex justify-end text-sky-deep hover:underline"
              >
                <PrName pr={artifact.pr} />
              </a>
            </Stat>
          )}
          {artifact.repo !== undefined && artifact.pr === undefined && <Stat label="Repo">{artifact.repo}</Stat>}
          <Stat label="Type">
            <code className="font-mono text-xs">{artifact.contentType}</code>
          </Stat>
          <Stat label="Size">{formatSize(artifact.size)}</Stat>
          <Stat label="Uploaded">
            <span title={formatDate(artifact.createdAt)}>{formatAgo(artifact.createdAt)}</span>
          </Stat>
        </dl>
        <div className="grid gap-2.5">
          <CopyButton label="copy link" value={artifact.rawUrl} toast="link copied" tone="green" />
          <CopyButton label="copy markdown" value={markdownFor(artifact)} toast="markdown copied" tone="blue" />
          <div className="grid grid-cols-2 gap-2.5">
            <LinkButton href={artifact.rawUrl}>open raw</LinkButton>
            <LinkButton href={`${artifact.rawUrl}?download`} download tone="gold">
              download
            </LinkButton>
          </div>
          <SignedIn>
            <SellButton artifact={artifact} />
          </SignedIn>
        </div>
        <p className="font-mono text-[11px] break-all text-ink/40">{artifact.id}</p>
      </aside>
    </div>
  )
}

const Found = ({ id }: { id: ArtifactId }) => {
  const result = useAtomValue(artifactAtom(id))
  return AsyncResult.matchWithError(result, {
    onInitial: () => <Loading label="loading artifact…" />,
    onError: (error) =>
      error._tag === "ArtifactNotFound" ? (
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
    <ButtonLink href={listPath()} onClick={onLinkClick(listPath(), returnTo)} size="sm">
      <ArrowLeftIcon size={16} /> all artifacts
    </ButtonLink>
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
