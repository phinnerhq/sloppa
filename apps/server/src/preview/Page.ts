import { STATUS_CODES, type OutgoingHttpHeaders, type ServerResponse } from "node:http"
import type { Duplex } from "node:stream"

export const escapeHtml = (text: string) => text.replace(/[&<>"']/g, (char) => `&#${char.charCodeAt(0)};`)

/** A small HTML page for the preview proxy's own answers. `body` is HTML: escape anything that came from outside. */
const render = (status: number, title: string, body: string) => `<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>${escapeHtml(title)}</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:34rem;margin:15vh auto;padding:0 1.5rem;color:#1f2937}h1{font-size:1.4rem}code{background:#f3f4f6;padding:.1em .35em;border-radius:.3em}small{color:#6b7280}</style>
<h1>${escapeHtml(title)}</h1><p>${body}</p><small>${status} · sloppa preview</small>
`

const headersFor = (html: string, extra: OutgoingHttpHeaders): OutgoingHttpHeaders => ({
  "content-type": "text/html; charset=utf-8",
  "content-length": Buffer.byteLength(html),
  "cache-control": "no-store",
  "x-robots-tag": "noindex, nofollow",
  "content-security-policy": "default-src 'none'; style-src 'unsafe-inline'",
  ...extra
})

export const page = (
  response: ServerResponse,
  status: number,
  title: string,
  body: string,
  extra: OutgoingHttpHeaders = {}
) => {
  const html = render(status, title, body)
  response.writeHead(status, headersFor(html, extra))
  response.end(html)
}

/** The same, on a socket that asked for an upgrade, where there's no ServerResponse to write to. */
export const rawPage = (
  socket: Duplex,
  status: number,
  title: string,
  body: string,
  extra: OutgoingHttpHeaders = {}
) => {
  if (!socket.writable) return void socket.destroy()
  const html = render(status, title, body)
  const headers = { ...headersFor(html, extra), connection: "close" }
  const lines = Object.entries(headers).map(([name, value]) => `${name}: ${String(value)}`)
  socket.end(`HTTP/1.1 ${status} ${STATUS_CODES[status] ?? ""}\r\n${lines.join("\r\n")}\r\n\r\n${html}`)
}
