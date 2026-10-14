import { ArrowLeftIcon, ArrowSquareOutIcon, CodeIcon, GlobeIcon } from "@phosphor-icons/react"
import type { ReactNode } from "react"
import { listPath, onLinkClick } from "../router.ts"
import { Floppa, type Mood } from "./ui.tsx"

const SITE = "https://phinner.dev"
const REPO = "https://github.com/phinnerhq/sloppa-artifacts"

const PHOTOS: ReadonlyArray<{
  mood: Mood
  title: string
  href: string
  author: string
  license: string
  licenseHref?: string
}> = [
  {
    mood: "hiss",
    title: "Caracal caracal at the Zoo",
    href: "https://commons.wikimedia.org/wiki/File:Caracal_caracal_at_the_Zoo.jpg",
    author: "ZhanserikKT",
    license: "CC BY 4.0",
    licenseHref: "https://creativecommons.org/licenses/by/4.0/"
  },
  {
    mood: "stare",
    title: "Caracal Portrait",
    href: "https://commons.wikimedia.org/wiki/File:Caracal_Portrait_(7476164046).jpg",
    author: "Charles Barilleaux",
    license: "CC BY 2.0",
    licenseHref: "https://creativecommons.org/licenses/by/2.0/"
  },
  {
    mood: "loaf",
    title: "Gfp-caracal",
    href: "https://commons.wikimedia.org/wiki/File:Gfp-caracal.jpg",
    author: "Yinan Chen",
    license: "Public domain"
  }
]

type Entry = readonly [name: string, href: string, license: string, note: string]

const LOOKS: ReadonlyArray<Entry> = [
  ["Phosphor Icons", "https://phosphoricons.com", "MIT", "every icon, and the background doodles"],
  ["Lilita One", "https://fonts.google.com/specimen/Lilita+One", "OFL 1.1", "titles"],
  ["Nunito", "https://fonts.google.com/specimen/Nunito", "OFL 1.1", "body text"],
  ["JetBrains Mono", "https://www.jetbrains.com/lp/mono/", "OFL 1.1", "code and file previews"],
  ["rembg", "https://github.com/danielgatis/rembg", "MIT", "cutting the floppas out"]
]

const STACK: ReadonlyArray<Entry> = [
  ["Effect", "https://effect.website", "MIT", "server, MCP, API and client state"],
  ["React", "https://react.dev", "MIT", "the app you're looking at"],
  ["Bun", "https://bun.sh", "MIT", "runtime, bundler and SQLite"],
  ["Vite", "https://vite.dev", "MIT", "web build"],
  ["Tailwind CSS", "https://tailwindcss.com", "MIT", "styling"],
  ["distilled", "https://github.com/alchemy-run/distilled", "Apache 2.0", "the S3 client"]
]

const External = ({ href, children, className = "" }: { href: string; children: ReactNode; className?: string }) => (
  <a href={href} target="_blank" rel="noreferrer" className={className}>
    {children}
  </a>
)

const Section = ({ title, children }: { title: string; children: ReactNode }) => (
  <section className="space-y-3">
    <h2 className="game-title text-xl">{title}</h2>
    {children}
  </section>
)

const Entries = ({ entries }: { entries: ReadonlyArray<Entry> }) => (
  <ul className="panel divide-y-2 divide-dashed divide-ink/15 px-4">
    {entries.map(([name, href, license, note]) => (
      <li key={name} className="flex flex-wrap items-baseline justify-between gap-x-4 py-2.5">
        <span>
          <External href={href} className="font-extrabold text-sky-deep hover:underline">
            {name}
          </External>
          <span className="ml-2 text-sm font-semibold text-ink/60">{note}</span>
        </span>
        <span className="rounded-md border-2 border-ink bg-belly px-2 font-game text-xs">{license}</span>
      </li>
    ))}
  </ul>
)

export const Credits = () => (
  <div className="mx-auto max-w-3xl space-y-8">
    <a href={listPath()} onClick={onLinkClick(listPath())} className="game-btn game-btn-cream !px-3 !py-1 !text-sm">
      <ArrowLeftIcon size={16} /> all artifacts
    </a>

    <div className="panel animate-pop-in flex flex-col items-center gap-4 px-6 py-8 text-center sm:flex-row sm:text-left">
      <Floppa mood="stare" className="h-32 w-auto shrink-0" />
      <div className="space-y-3">
        <p className="font-game text-3xl leading-tight">made by phinner</p>
        <p className="font-semibold text-ink/70">
          Part of <span className="font-extrabold text-ink">sloppa</span>, your line of personal slop software.
        </p>
        <div className="flex flex-wrap justify-center gap-2.5 sm:justify-start">
          <External href={SITE} className="game-btn game-btn-blue">
            <GlobeIcon size={18} /> phinner.dev
          </External>
          <External href={REPO} className="game-btn game-btn-gold">
            <CodeIcon size={18} /> source
          </External>
        </div>
      </div>
    </div>

    <Section title="the floppas">
      <div className="grid gap-3 sm:grid-cols-3">
        {PHOTOS.map((photo) => (
          <figure key={photo.mood} className="panel flex flex-col items-center gap-2 p-3 text-center">
            <div className="flex h-28 items-end">
              <Floppa mood={photo.mood} className="max-h-28 w-auto" />
            </div>
            <figcaption className="space-y-0.5 text-sm">
              <External
                href={photo.href}
                className="inline-flex items-center gap-1 font-extrabold text-sky-deep hover:underline"
              >
                {photo.title} <ArrowSquareOutIcon size={12} />
              </External>
              <p className="font-semibold text-ink/70">by {photo.author}</p>
              <p className="font-game text-xs">
                {photo.licenseHref === undefined ? (
                  photo.license
                ) : (
                  <External href={photo.licenseHref} className="hover:underline">
                    {photo.license}
                  </External>
                )}
              </p>
            </figcaption>
          </figure>
        ))}
      </div>
    </Section>

    <Section title="icons, fonts and tools">
      <Entries entries={LOOKS} />
    </Section>

    <Section title="built with">
      <Entries entries={STACK} />
    </Section>
  </div>
)
