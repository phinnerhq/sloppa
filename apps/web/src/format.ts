import type { Artifact } from "@sloppa/api"
import { DateTime } from "effect"

export type Kind = "image" | "video" | "audio" | "pdf" | "html" | "text" | "binary"

const TEXT_TYPES = /^(text\/|application\/(json|xml|yaml|x-yaml|javascript|x-ndjson|toml|x-sh|sql|graphql))/
const TEXT_EXTENSIONS =
  /\.(txt|log|md|json|ya?ml|toml|csv|tsv|diff|patch|ts|tsx|js|jsx|py|rs|go|java|kt|sh|sql|xml|ini|env)$/i

export const kindOf = (artifact: Pick<Artifact, "name" | "contentType">): Kind => {
  const type = artifact.contentType.split(";")[0]!.trim().toLowerCase()
  if (type.startsWith("image/")) return "image"
  if (type.startsWith("video/")) return "video"
  if (type.startsWith("audio/")) return "audio"
  if (type === "application/pdf") return "pdf"
  if (type === "text/html") return "html"
  if (TEXT_TYPES.test(type) || TEXT_EXTENSIONS.test(artifact.name)) return "text"
  return "binary"
}

export const formatSize = (bytes: number) => {
  if (bytes < 1024) return `${bytes} B`
  const units = ["KB", "MB", "GB"]
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  return `${value.toFixed(value < 10 ? 1 : 0)} ${units[unit]}`
}

const relative = new Intl.RelativeTimeFormat("en", { numeric: "auto" })

export const formatAgo = (date: DateTime.Utc, now = Date.now()) => {
  const seconds = Math.round((DateTime.toEpochMillis(date) - now) / 1000)
  const steps: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ["year", 31536000],
    ["month", 2592000],
    ["day", 86400],
    ["hour", 3600],
    ["minute", 60]
  ]
  for (const [unit, size] of steps) {
    if (Math.abs(seconds) >= size) return relative.format(Math.round(seconds / size), unit)
  }
  return "just now"
}

export const formatDate = (date: DateTime.Utc) =>
  DateTime.toDateUtc(date).toLocaleString("en", { dateStyle: "medium", timeStyle: "short" })

export { pullRequestUrl } from "@sloppa/api"

/** "now", "24m", "3h", "2d", "5mo", "1y" */
export const formatAgoShort = (date: DateTime.Utc, now = Date.now()) => {
  const seconds = Math.max(0, Math.round((now - DateTime.toEpochMillis(date)) / 1000))
  const steps: Array<[string, number]> = [
    ["y", 31536000],
    ["mo", 2592000],
    ["d", 86400],
    ["h", 3600],
    ["m", 60]
  ]
  for (const [unit, size] of steps) {
    if (seconds >= size) return `${Math.floor(seconds / size)}${unit}`
  }
  return "now"
}

/** A stable pastel per extension: random-looking, but every `.json` gets the same one. */
export const colorFor = (extension: string) => {
  let hash = 0x811c9dc5
  for (const char of extension) hash = Math.imul(hash ^ char.charCodeAt(0), 0x01000193)
  return `hsl(${(hash >>> 0) % 360} 85% 78%)`
}
