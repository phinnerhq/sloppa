/**
 * Builds the background wallpaper tile from Phosphor icons (MIT, bold weight):
 * a staggered grid where each icon gets a seeded tilt, scale and nudge, so the
 * pattern reads as scattered doodles rather than a grid. Re-run after changing
 * the icon list: `bun run doodles`.
 */
import { readFileSync, writeFileSync } from "node:fs"
import { join } from "node:path"

const ICONS = [
  "cat",
  "paw-print",
  "fish",
  "yarn",
  "cloud-arrow-up",
  "sparkle",
  "file-image",
  "bowl-food",
  "package",
  "star",
  "file-video",
  "heart",
  "coin",
  "file-code",
  "game-controller",
  "crown"
]

const TILE = 520
const COLS = 5
const ROWS = 5
const CELL = TILE / COLS
const ICON = 30
const INK = "#1d2433"
const OPACITY = 0.09

const assets = join(import.meta.dir, "../node_modules/@phosphor-icons/core/assets/bold")
const inner = (name: string) =>
  readFileSync(join(assets, `${name}-bold.svg`), "utf8")
    .replace(/^<svg[^>]*>/, "")
    .replace(/<\/svg>\s*$/, "")

// Small deterministic PRNG so the tile is stable between runs.
let seed = 0x5107ba
const random = () => {
  seed = (seed * 1664525 + 1013904223) >>> 0
  return seed / 2 ** 32
}

// Every icon once, then random extras, shuffled: no fixed rows or columns of the same icon.
const picks = [...ICONS]
while (picks.length < COLS * ROWS) picks.push(ICONS[Math.floor(random() * ICONS.length)]!)
for (let i = picks.length - 1; i > 0; i--) {
  const j = Math.floor(random() * (i + 1))
  ;[picks[i], picks[j]] = [picks[j]!, picks[i]!]
}

const glyphs: Array<string> = []
for (let row = 0; row < ROWS; row++) {
  for (let col = 0; col < COLS; col++) {
    const name = picks[row * COLS + col]!
    const size = ICON * (0.85 + random() * 0.4)
    // Odd rows are shifted by half a cell; wrap so the tile stays seamless.
    const cx = ((col + 0.5 + (row % 2) * 0.5) * CELL + (random() - 0.5) * CELL * 0.3) % TILE
    const cy = (row + 0.5) * CELL + (random() - 0.5) * CELL * 0.3
    const angle = (random() - 0.5) * 50
    const glyph = (x: number) =>
      `<g transform="translate(${(x - size / 2).toFixed(1)} ${(cy - size / 2).toFixed(1)}) rotate(${angle.toFixed(
        1
      )} ${(size / 2).toFixed(1)} ${(size / 2).toFixed(1)}) scale(${(size / 256).toFixed(4)})">${inner(name)}</g>`
    glyphs.push(glyph(cx))
    // Icons near the edge are drawn again on the other side to tile seamlessly.
    if (cx + size > TILE) glyphs.push(glyph(cx - TILE))
    if (cx - size < 0) glyphs.push(glyph(cx + TILE))
  }
}

const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${TILE}" height="${TILE}" viewBox="0 0 ${TILE} ${TILE}">
<!-- Icons: Phosphor Icons (https://phosphoricons.com), MIT License, Copyright (c) 2020 Phosphor Icons -->
<g fill="${INK}" fill-opacity="${OPACITY}">${glyphs.join("")}</g>
</svg>
`

const out = join(import.meta.dir, "../apps/web/src/assets/doodles.svg")
writeFileSync(out, svg)
console.log(`wrote ${out} (${glyphs.length} glyphs, ${(svg.length / 1024).toFixed(1)} KB)`)
