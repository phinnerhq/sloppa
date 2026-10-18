package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/artifacts"
	"github.com/phinnerhq/sloppa/internal/storage"
)

// immutable is for artifacts, which never change once uploaded and whose ids are unguessable:
// anyone may cache them forever.
func immutable(h http.Header) {
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Access-Control-Allow-Origin", "*")
}

func notFound(w http.ResponseWriter) {
	text(w, http.StatusNotFound, "Not found")
}

func text(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	io.WriteString(w, body)
}

// registerFiles serves raw files and thumbnails, outside the typed API so each response carries
// the artifact's own headers.
func (s *server) registerFiles(mux *http.ServeMux) {
	mux.HandleFunc("GET /f/{id}", s.raw)
	mux.HandleFunc("GET /f/{id}/{name}", s.raw)
	mux.HandleFunc("GET /t/{id}", s.thumbnail)
}

// raw is a file as uploaded. The name in its path is for whoever reads the link: the id is enough.
// Uploaded HTML and SVG get an opaque, sandboxed origin, so they can't act as the app. Only the
// dashboard's viewer may frame them: anyone else could dress them up as something they aren't.
func (s *server) raw(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !artifactID.MatchString(id) {
		notFound(w)
		return
	}
	obj, err := s.artifacts.Open(r.Context(), id, r.Header.Get("Range"))
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	defer obj.Body.Close()

	disposition := "inline"
	if r.URL.Query().Has("download") {
		disposition = "attachment"
	}
	h := w.Header()
	h.Set("Content-Type", obj.Artifact.ContentType)
	h.Set("Content-Length", strconv.FormatInt(obj.ContentLength, 10))
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Disposition", contentDisposition(disposition, obj.Artifact.Name))
	h.Set("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-forms allow-downloads; frame-ancestors 'self'")
	immutable(h)
	status := http.StatusOK
	if rg := obj.Range; rg != nil {
		h.Set("Content-Range", "bytes "+strconv.FormatInt(rg.Start, 10)+"-"+strconv.FormatInt(rg.End, 10)+"/"+strconv.FormatInt(obj.Artifact.Size, 10))
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, obj.Body); err != nil {
		slog.DebugContext(r.Context(), "Stopped sending a file", "id", id, "error", err)
	}
}

// thumbnail is a small WebP of an image, for the dashboard's grid; an image that can't be shrunk
// sends the browser to the original.
func (s *server) thumbnail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !artifactID.MatchString(id) {
		notFound(w)
		return
	}
	webp, err := s.artifacts.Thumbnail(r.Context(), id)
	if errors.Is(err, artifacts.ErrNoThumbnail) {
		slog.WarnContext(r.Context(), "No thumbnail for "+id, "error", err)
		http.Redirect(w, r, "/f/"+id, http.StatusFound)
		return
	}
	if err != nil {
		s.fileError(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/webp")
	h.Set("Content-Length", strconv.Itoa(len(webp)))
	immutable(h)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		w.Write(webp)
	}
}

// fileError answers in plain text, as these routes are opened by browsers rather than the client.
// An API error keeps its status and headers: a thumbnail that's too busy to be made says when to
// come back.
func (s *server) fileError(w http.ResponseWriter, r *http.Request, err error) {
	var e *api.Error
	errors.As(err, &e)
	switch {
	case errors.Is(err, storage.ErrNotFound) || e != nil && e.Status == http.StatusNotFound:
		notFound(w)
	case e != nil && (e.Message != "" || e.RetryAfter > 0):
		if e.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfter))
		}
		text(w, e.Status, e.Error())
	default:
		slog.ErrorContext(r.Context(), "Couldn't read a file", "path", logPath(r.URL.Path), "error", err)
		text(w, http.StatusBadGateway, "Couldn't read the file from storage")
	}
}

// contentDisposition names the file twice: plainly for old clients, with what isn't printable
// ASCII (or is a quote) replaced, and exactly in RFC 5987's filename*.
func contentDisposition(disposition, name string) string {
	var plain strings.Builder
	for _, r := range name {
		switch {
		case r > 0xFFFF:
			// JavaScript, which this matches, replaced each half of a surrogate pair.
			plain.WriteString("__")
		case r < 0x20 || r > 0x7e || r == '"':
			plain.WriteByte('_')
		default:
			plain.WriteRune(r)
		}
	}
	return disposition + `; filename="` + plain.String() + `"; filename*=UTF-8''` + api.EncodeURIComponent(name)
}
