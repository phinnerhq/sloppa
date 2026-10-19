package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/phinnerhq/sloppa/internal/api"
)

// Huma's own errors (validation, unreadable bodies, panics in handlers) are api.Errors too, so
// every error body the API sends has a code to tell it by.
func init() {
	huma.NewError = newError
	huma.NewErrorWithContext = func(_ huma.Context, status int, msg string, errs ...error) huma.StatusError {
		return newError(status, msg, errs...)
	}
}

func newError(status int, msg string, errs ...error) huma.StatusError {
	for _, err := range errs {
		var e *api.Error
		if errors.As(err, &e) {
			return e
		}
	}
	switch {
	case status >= 500:
		slog.Error("Request failed", "error", errors.Join(errs...), "status", status, "message", msg)
		return api.InternalError()
	case status == http.StatusUnprocessableEntity || status == http.StatusBadRequest:
		// Huma reports invalid input as 422: all input the API refuses is a 400, the bad_request it documents.
		return api.BadRequest(describe(msg, errs))
	case status == http.StatusRequestEntityTooLarge:
		return api.PayloadTooLarge(describe(msg, errs))
	}
	code := strings.ToLower(strings.ReplaceAll(http.StatusText(status), " ", "_"))
	return &api.Error{Status: status, Code: code, Message: describe(msg, errs)}
}

// describe is msg and what's wrong where, like `validation failed: body.name: expected length >= 1`.
func describe(msg string, errs []error) string {
	var details []string
	for _, err := range errs {
		if err == nil {
			continue
		}
		var detailer huma.ErrorDetailer
		if errors.As(err, &detailer) {
			d := detailer.ErrorDetail()
			if d.Location != "" {
				details = append(details, d.Location+": "+d.Message)
				continue
			}
			details = append(details, d.Message)
			continue
		}
		details = append(details, err.Error())
	}
	if len(details) == 0 {
		return msg
	}
	if msg == "" {
		return strings.Join(details, "; ")
	}
	return msg + ": " + strings.Join(details, "; ")
}

// writeError answers a request outside Huma's routes as the API would: an api.Error as its JSON,
// anything else as a logged 500.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var e *api.Error
	if !errors.As(err, &e) {
		slog.ErrorContext(r.Context(), "Request failed", "method", r.Method, "path", logPath(r.URL.Path), "error", err)
		e = api.InternalError()
	}
	for key, values := range e.GetHeaders() {
		w.Header()[key] = values
	}
	writeJSON(w, e.Status, e)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	if err := marshalJSON(&buf, v); err != nil {
		panic(fmt.Errorf("encode a response: %w", err))
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprint(buf.Len()))
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// marshalJSON leaves HTML characters as they are (the setup prompt is full of `<app>` and `&&`),
// and no trailing newline.
func marshalJSON(w io.Writer, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := w.Write(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return err
}

var jsonFormat = huma.Format{Marshal: marshalJSON, Unmarshal: json.Unmarshal}
