package httpapi

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/phinnerhq/sloppa/internal/api"
)

// static serves the dashboard from files: what exists as is, and index.html for any other page,
// since the dashboard routes in the browser. A missing file (a stale chunk, say) is a 404 rather
// than a page of HTML its script tag would choke on.
func static(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, r, &api.Error{Status: http.StatusNotFound, Tag: "NotFound", Message: "No such API route"})
			return
		}
		if files == nil {
			notFound(w)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if info, err := fs.Stat(files, name); err == nil && !info.IsDir() {
			serveFile(w, r, files, name)
			return
		}
		if path.Ext(name) != "" {
			notFound(w)
			return
		}
		serveFile(w, r, files, "index.html")
	})
}

func serveFile(w http.ResponseWriter, r *http.Request, files fs.FS, name string) {
	f, err := files.Open(name)
	if err != nil {
		notFound(w)
		return
	}
	defer f.Close()
	var modTime time.Time
	if info, err := f.Stat(); err == nil {
		modTime = info.ModTime()
	}
	content, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			text(w, http.StatusInternalServerError, "Couldn't read "+name)
			return
		}
		content = bytes.NewReader(b)
	}
	// No page may frame the dashboard, where a click it hid could act as the signed-in user: least of
	// all a preview, which can live under the same site. X-Frame-Options is for browsers without CSP.
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Frame-Options", "DENY")
	// Vite names what's under assets/ by its content's hash, so it can be cached forever; anything
	// else, index.html first, is checked for a new version every time.
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, modTime, content)
}
