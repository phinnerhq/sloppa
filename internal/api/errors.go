package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Error is an API failure as sent: an HTTP status and a JSON body with a code saying what went
// wrong and a message saying it to people, like
// {"code":"artifact_not_found","message":"No artifact 1f…","id":"1f…"}.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// ID and RetryAfter are set by the errors that carry them.
	ID string `json:"id,omitempty"`
	// RetryAfter is in seconds, for too_many_attempts; the Retry-After header says it too.
	RetryAfter int `json:"retryAfter,omitempty"`
}

func (e *Error) Error() string { return e.Message }

func (e *Error) GetStatus() int { return e.Status }

// GetHeaders is the headers sent with the error, for Huma and writeError.
func (e *Error) GetHeaders() http.Header {
	if e.RetryAfter == 0 {
		return nil
	}
	return http.Header{"Retry-After": {strconv.Itoa(e.RetryAfter)}}
}

// ErrorKind is one of the errors the API answers with, as the OpenAPI document lists it: a code, a
// status, and the field it carries besides the code and message, if any.
type ErrorKind struct {
	Code   string
	Status int
	// Field is "id" or "retryAfter", or "" for none.
	Field string
}

// Is tells whether err is an error of this kind.
func (k ErrorKind) Is(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == k.Code
}

// Name is the kind's name in the OpenAPI document, like ArtifactNotFound for artifact_not_found.
func (k ErrorKind) Name() string {
	var b strings.Builder
	for word := range strings.SplitSeq(k.Code, "_") {
		b.WriteString(strings.ToUpper(word[:1]) + word[1:])
	}
	return b.String()
}

func (k ErrorKind) New(message string) *Error {
	return &Error{Status: k.Status, Code: k.Code, Message: message}
}

var (
	KindBadRequest          = ErrorKind{"bad_request", http.StatusBadRequest, ""}
	KindUnauthorized        = ErrorKind{"unauthorized", http.StatusUnauthorized, ""}
	KindInvalidCredentials  = ErrorKind{"invalid_credentials", http.StatusUnauthorized, ""}
	KindForbidden           = ErrorKind{"forbidden", http.StatusForbidden, ""}
	KindNotFound            = ErrorKind{"not_found", http.StatusNotFound, ""}
	KindArtifactNotFound    = ErrorKind{"artifact_not_found", http.StatusNotFound, "id"}
	KindPreviewNotFound     = ErrorKind{"preview_not_found", http.StatusNotFound, "id"}
	KindPreviewNameTaken    = ErrorKind{"preview_name_taken", http.StatusConflict, ""}
	KindPayloadTooLarge     = ErrorKind{"payload_too_large", http.StatusRequestEntityTooLarge, ""}
	KindTooManyAttempts     = ErrorKind{"too_many_attempts", http.StatusTooManyRequests, "retryAfter"}
	KindInternalError       = ErrorKind{"internal_error", http.StatusInternalServerError, ""}
	KindStorageError        = ErrorKind{"storage_error", http.StatusBadGateway, ""}
	KindPreviewsUnavailable = ErrorKind{"previews_unavailable", http.StatusServiceUnavailable, ""}
	KindSetupLinkGone       = ErrorKind{"setup_link_gone", http.StatusGone, ""}
)

// ErrorKinds is every kind of error the API's operations document. KindNotFound isn't among them: it
// answers paths that aren't operations; nor is KindSetupLinkGone, from setup links outside the API.
var ErrorKinds = []ErrorKind{
	KindBadRequest, KindUnauthorized, KindInvalidCredentials, KindForbidden, KindArtifactNotFound,
	KindPreviewNotFound, KindPreviewNameTaken, KindPayloadTooLarge, KindTooManyAttempts, KindInternalError,
	KindStorageError, KindPreviewsUnavailable,
}

// BadRequest is input the API refuses, with what's wrong with it.
func BadRequest(message string) *Error   { return KindBadRequest.New(message) }
func Unauthorized(message string) *Error { return KindUnauthorized.New(message) }
func InvalidCredentials() *Error         { return KindInvalidCredentials.New("Wrong username or password") }
func Forbidden(message string) *Error    { return KindForbidden.New(message) }

// NotFound is a route the API doesn't have.
func NotFound(message string) *Error { return KindNotFound.New(message) }

func ArtifactNotFound(id string) *Error {
	e := KindArtifactNotFound.New("No artifact " + id)
	e.ID = id
	return e
}

func PreviewNotFound(id string) *Error {
	e := KindPreviewNotFound.New("No preview " + id)
	e.ID = id
	return e
}

// PreviewNameTaken means another live preview holds the name.
func PreviewNameTaken(name string) *Error {
	return KindPreviewNameTaken.New(fmt.Sprintf("%q is taken by another preview right now", name))
}

func PayloadTooLarge(message string) *Error { return KindPayloadTooLarge.New(message) }

// TooManyAttempts says to wait retryAfter seconds, at least one.
func TooManyAttempts(retryAfter int) *Error {
	retryAfter = max(retryAfter, 1)
	e := KindTooManyAttempts.New(fmt.Sprintf("Too many attempts: try again in %ds", retryAfter))
	e.RetryAfter = retryAfter
	return e
}

// InternalError is what clients see of a failure that isn't theirs; the cause goes to the log.
func InternalError() *Error { return KindInternalError.New("Something went wrong on the server") }

func StorageError(message string) *Error { return KindStorageError.New(message) }

// PreviewsUnavailable means previews aren't configured on this server.
func PreviewsUnavailable(message string) *Error { return KindPreviewsUnavailable.New(message) }

// SetupLinkGone answers a setup link that was used already, expired or was revoked, or never was:
// they're all told apart by nobody.
func SetupLinkGone() *Error {
	return KindSetupLinkGone.New("This setup link was used already, expired or was revoked: make a new one from the dashboard")
}
