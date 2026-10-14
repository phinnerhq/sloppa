const byExtension: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  avif: "image/avif",
  svg: "image/svg+xml",
  mp4: "video/mp4",
  webm: "video/webm",
  mov: "video/quicktime",
  mp3: "audio/mpeg",
  wav: "audio/wav",
  pdf: "application/pdf",
  html: "text/html; charset=utf-8",
  htm: "text/html; charset=utf-8",
  json: "application/json; charset=utf-8",
  ndjson: "application/x-ndjson; charset=utf-8",
  xml: "application/xml; charset=utf-8",
  yaml: "text/yaml; charset=utf-8",
  yml: "text/yaml; charset=utf-8",
  csv: "text/csv; charset=utf-8",
  md: "text/markdown; charset=utf-8",
  txt: "text/plain; charset=utf-8",
  log: "text/plain; charset=utf-8",
  diff: "text/x-diff; charset=utf-8",
  patch: "text/x-diff; charset=utf-8",
  zip: "application/zip",
  gz: "application/gzip",
  har: "application/json; charset=utf-8",
  trace: "application/octet-stream"
}

/** Trust the client's type unless it's missing or the generic fallback; then go by extension. */
export const resolveContentType = (name: string, declared: string | undefined) => {
  const type = (declared ?? "").trim()
  if (type !== "" && type !== "application/octet-stream") return type
  const extension = name.slice(name.lastIndexOf(".") + 1).toLowerCase()
  return byExtension[extension] ?? "application/octet-stream"
}
