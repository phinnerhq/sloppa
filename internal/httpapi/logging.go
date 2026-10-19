package httpapi

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/phinnerhq/sloppa/internal/api"
)

// logPath is a request's path as logged: setup links carry their secret in theirs. It may be spelled
// any way that cleans to /setup/… (//setup/…, /a/../setup/…), which ServeMux redirects, or not even
// that: whatever follows a setup segment anywhere goes. Escapes are decoded in the path already.
func logPath(p string) string {
	segments := strings.Split(p, "/")
	for i, segment := range segments[:len(segments)-1] {
		if strings.EqualFold(segment, "setup") {
			return strings.Join(segments[:i+1], "/") + "/<code>"
		}
	}
	return p
}

// logRequests logs each request once it's answered.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &recorder{ResponseWriter: w}
		defer func() {
			slog.InfoContext(r.Context(), "Sent HTTP response",
				"method", r.Method,
				"host", r.Host,
				"path", logPath(r.URL.Path),
				"status", rec.status(),
				"duration", time.Since(start).Round(time.Microsecond))
		}()
		next.ServeHTTP(rec, r)
	})
}

// recoverPanics answers a handler's panic with a 500, if nothing was sent yet, instead of the
// connection dropping without a word.
func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			// The way to abort a response on purpose: net/http handles it.
			if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				panic(v)
			}
			slog.ErrorContext(r.Context(), "Handler panicked", "method", r.Method, "path", logPath(r.URL.Path),
				"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
			if rec.code == 0 {
				writeError(rec, r, api.InternalError())
				return
			}
			panic(http.ErrAbortHandler)
		}()
		next.ServeHTTP(rec, r)
	})
}

// recorder notes the status a handler sent. Flushing, hijacking (for WebSockets) and the rest of
// http.ResponseController reach the writer underneath.
type recorder struct {
	http.ResponseWriter
	code int
}

func (r *recorder) WriteHeader(code int) {
	// 1xx responses are informational: the real one comes after.
	if r.code == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		r.code = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *recorder) Flush() {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(r.ResponseWriter).Hijack()
	if err == nil && r.code == 0 {
		r.code = http.StatusSwitchingProtocols
	}
	return conn, rw, err
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) status() int {
	if r.code == 0 {
		// Nothing written: net/http sends a 200.
		return http.StatusOK
	}
	return r.code
}
