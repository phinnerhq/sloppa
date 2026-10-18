package preview

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

var escaper = strings.NewReplacer("&", "&#38;", "<", "&#60;", ">", "&#62;", `"`, "&#34;", "'", "&#39;")

// escapeHTML escapes like the TypeScript server did, so its pages read the same byte for byte.
func escapeHTML(text string) string { return escaper.Replace(text) }

// refusal is one of the proxy's own answers: a small HTML page. Body is HTML: escape anything that
// came from outside.
type refusal struct {
	status int
	title  string
	body   string
	header http.Header
}

func (f refusal) render() string {
	return fmt.Sprintf(`<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:34rem;margin:15vh auto;padding:0 1.5rem;color:#1f2937}h1{font-size:1.4rem}code{background:#f3f4f6;padding:.1em .35em;border-radius:.3em}small{color:#6b7280}</style>
<h1>%s</h1><p>%s</p><small>%d · sloppa preview</small>
`, escapeHTML(f.title), escapeHTML(f.title), f.body, f.status)
}

func (f refusal) write(w http.ResponseWriter) {
	page := f.render()
	h := w.Header()
	for name, values := range f.header {
		h[name] = values
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(page)))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(f.status)
	w.Write([]byte(page))
}

var (
	noPreview = refusal{
		status: http.StatusNotFound,
		title:  "No preview here",
		body:   "Nothing is running at this address. Previews stop when their <code>sloppa preview run</code> does.",
	}
	notFound = refusal{status: http.StatusNotFound, title: "Not found"}
	apexPage = refusal{
		status: http.StatusNotFound,
		title:  "sloppa preview",
		body:   "Previews live on their own subdomains. Start one with <code>sloppa preview run -- &lt;command&gt;</code>.",
	}
	disconnected = refusal{
		status: http.StatusBadGateway,
		title:  "Preview disconnected",
		body:   "This preview's <code>sloppa preview run</code> isn't connected right now. It reconnects by itself if it can: try again in a moment.",
	}
)

func passwordNeeded(label string) refusal {
	return refusal{
		status: http.StatusUnauthorized,
		title:  "Password needed",
		body:   "This preview has a password. Any user name works.",
		header: http.Header{"Www-Authenticate": {fmt.Sprintf(`Basic realm="%s", charset="UTF-8"`, label)}},
	}
}

func tooMany(retryAfter int) refusal {
	return refusal{
		status: http.StatusTooManyRequests,
		title:  "Slow down",
		body:   "Too many requests for previews that don't exist or wrong passwords. Try again shortly.",
		header: http.Header{"Retry-After": {strconv.Itoa(retryAfter)}},
	}
}

// checkingBusy is a password that couldn't be checked: too many are, server-wide.
var checkingBusy = refusal{
	status: http.StatusServiceUnavailable,
	title:  "Busy",
	body:   "Too many passwords are being checked right now. Try again in a moment.",
	header: http.Header{"Retry-After": {"1"}},
}

// busy is a preview with as many connections open as it may have.
var busy = refusal{
	status: http.StatusServiceUnavailable,
	title:  "Preview busy",
	body:   "This preview has too many connections open right now. Try again in a moment.",
	header: http.Header{"Retry-After": {"1"}},
}

func notListening(port int) refusal {
	return refusal{
		status: http.StatusBadGateway,
		title:  "Dev server not listening",
		body:   fmt.Sprintf("The preview is connected, but nothing listens on 127.0.0.1:%d yet. If it just started, give it a moment; otherwise make it use <code>$PORT</code>.", port),
	}
}

func unreachable(err error) refusal {
	return refusal{status: http.StatusBadGateway, title: "Preview unreachable", body: escapeHTML(err.Error())}
}
