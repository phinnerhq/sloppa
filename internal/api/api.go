// Package api is the HTTP API's wire format, shared by the server and the CLI. The JSON matches what
// the dashboard's client expects: optional fields are left out rather than null, times are UTC with
// millisecond precision, and errors carry a `_tag` naming them.
package api

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	CollectionMax = 200
	// UnknownCollection is where artifacts stored before collections, with neither a PR nor a repo,
	// read back as.
	UnknownCollection = "unknown"

	AgentNameMax = 100
	PasswordMin  = 8

	MaxUploadBytes = 100 << 20

	PageSize         = 60
	GroupPageSize    = 24
	GroupPreviewSize = 4

	PreviewTTLDefault = 8 * time.Hour
	PreviewTTLMax     = 24 * time.Hour
	PreviewTTLMin     = time.Minute

	SessionCookie = "sloppa_session"
	// AgentTokenPrefix lets secret scanners (and people) tell what leaked.
	AgentTokenPrefix = "sloppa_"
)

// Time is how the API writes instants: RFC 3339 in UTC, to the millisecond.
func Time(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }

// Millis is an instant from epoch milliseconds, as SQLite and the storage sidecars keep them.
func Millis(ms int64) time.Time { return Time(time.UnixMilli(ms)) }

type Artifact struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	ContentType string    `json:"contentType"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"createdAt"`
	Collection  string    `json:"collection"`
	// URL is the viewer page in the dashboard.
	URL string `json:"url"`
	// RawURL is the file itself, served inline.
	RawURL string `json:"rawUrl"`
	// ThumbURL is a small WebP preview, for images that have one.
	ThumbURL string `json:"thumbUrl,omitempty"`
}

type ArtifactPage struct {
	Items []Artifact `json:"items"`
	// NextCursor is passed back as `cursor` for the next page; absent on the last one.
	NextCursor string `json:"nextCursor,omitempty"`
}

// ArtifactGroup is a collection's artifacts, as one tile of the dashboard's mosaic.
type ArtifactGroup struct {
	Collection   string    `json:"collection"`
	Count        int       `json:"count"`
	LastUploadAt time.Time `json:"lastUploadAt"`
	// Preview holds the newest few, to show what's inside.
	Preview []Artifact `json:"preview"`
}

type GroupPage struct {
	Items      []ArtifactGroup `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

type CollectionSummary struct {
	Collection   string    `json:"collection"`
	Count        int       `json:"count"`
	LastUploadAt time.Time `json:"lastUploadAt"`
}

type Stats struct {
	Total        int        `json:"total"`
	TotalBytes   int64      `json:"totalBytes"`
	Last24h      int        `json:"last24h"`
	LastUploadAt *time.Time `json:"lastUploadAt,omitempty"`
}

type MoveArtifact struct {
	Collection string `json:"collection"`
}

type User struct {
	Username string `json:"username"`
}

type Login struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type ChangePassword struct {
	Current string `json:"current"`
	Next    string `json:"next"`
}

// Agent is a token handed to one agent, through a setup link or made by hand.
type Agent struct {
	ID string `json:"id"`
	// Name is what the agent called itself, e.g. "Claude Code on my laptop".
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

type AgentName struct {
	Name string `json:"name"`
}

// CreatedAgent is shown once: only a hash of the token is kept.
type CreatedAgent struct {
	Agent Agent  `json:"agent"`
	Token string `json:"token"`
}

type SetupLink struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	// Prompt is pasted into an agent, which sets itself up from the link.
	Prompt    string    `json:"prompt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Preview is a dev server shared by `sloppa preview run`, live until it expires or is stopped.
type Preview struct {
	// ID is 8 random base36 characters; the subdomain too, unless the preview has a name.
	ID   string `json:"id"`
	URL  string `json:"url"`
	Name string `json:"name,omitempty"`
	// Protected previews ask for a password, checked by the preview proxy.
	Protected bool `json:"protected"`
	// Port is the dev server's, on the machine running the command.
	Port     int    `json:"port"`
	Command  string `json:"command,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	// ConnectedAt is when the tunnel last connected, or when the preview was started if it never has.
	ConnectedAt time.Time `json:"connectedAt"`
	// ExpiresAt is when the preview stops on its own, killing its dev server.
	ExpiresAt time.Time `json:"expiresAt"`
	// Connected tells whether a tunnel is up right now.
	Connected bool `json:"connected"`
	// Connections counts browser connections open right now.
	Connections int `json:"connections"`
	Requests    int `json:"requests"`
}

// StartPreview is what `sloppa preview run` asks for. The preview is a row on the server from then
// on: the tunnel connects to it with the same agent token, as often as it needs to.
type StartPreview struct {
	Name string `json:"name,omitempty"`
	// Password is asked for by the proxy as Basic auth, any user name.
	Password string `json:"password,omitempty"`
	// TTL is in seconds; PreviewTTLDefault when zero.
	TTL      int    `json:"ttl,omitempty"`
	Port     int    `json:"port"`
	Command  string `json:"command,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Hostname string `json:"hostname,omitempty"`
}

type StartedPreview struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	// TunnelURL is the WebSocket the CLI connects to, with its agent token as a bearer.
	TunnelURL string    `json:"tunnelUrl"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Error is an API failure as sent: an HTTP status and a JSON body tagged with what went wrong, like
// {"_tag":"ArtifactNotFound","id":"…"}.
type Error struct {
	Status int    `json:"-"`
	Tag    string `json:"_tag"`
	// Message, ID and RetryAfter are set by the errors that carry them.
	Message string `json:"message,omitempty"`
	ID      string `json:"id,omitempty"`
	// RetryAfter is in seconds, for TooManyAttempts.
	RetryAfter int `json:"retryAfter,omitempty"`
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.ID != "" {
		return fmt.Sprintf("%s: %s", e.Tag, e.ID)
	}
	return e.Tag
}

func (e *Error) GetStatus() int { return e.Status }

func ArtifactNotFound(id string) *Error { return &Error{Status: 404, Tag: "ArtifactNotFound", ID: id} }
func PreviewNotFound(id string) *Error  { return &Error{Status: 404, Tag: "PreviewNotFound", ID: id} }
func Unauthorized(message string) *Error {
	return &Error{Status: 401, Tag: "Unauthorized", Message: message}
}
func InvalidCredentials() *Error { return &Error{Status: 401, Tag: "InvalidCredentials"} }
func TooManyAttempts(retryAfter int) *Error {
	return &Error{Status: 429, Tag: "TooManyAttempts", RetryAfter: retryAfter}
}
func StorageError(message string) *Error {
	return &Error{Status: 502, Tag: "StorageError", Message: message}
}

// PreviewsUnavailable means previews aren't configured on this server.
func PreviewsUnavailable(message string) *Error {
	return &Error{Status: 503, Tag: "PreviewsUnavailable", Message: message}
}

// PreviewNameTaken means another live preview holds the name.
func PreviewNameTaken(name string) *Error {
	return &Error{Status: 409, Tag: "PreviewNameTaken", Message: fmt.Sprintf("%q is taken by another preview right now", name)}
}

// BadRequest is input the API refuses, with what's wrong with it.
func BadRequest(message string) *Error {
	return &Error{Status: 400, Tag: "BadRequest", Message: message}
}

var previewName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

// IsPreviewName tells whether name works as a preview's subdomain: one lowercase DNS label, like
// `checkout-demo`.
func IsPreviewName(name string) bool { return previewName.MatchString(name) }

var previewID = regexp.MustCompile(`^[a-z0-9]{8}$`)

func IsPreviewID(id string) bool { return previewID.MatchString(id) }

// SetupPath is where agents redeem a setup link, outside the typed API since they read it as text.
func SetupPath(code string) string { return "/setup/" + code }

// RawPath is the raw file route, served outside the typed API so headers can follow the file.
func RawPath(id, name string) string { return "/f/" + id + "/" + EncodeURIComponent(name) }
func ThumbPath(id string) string     { return "/t/" + id }
func ViewerPath(id string) string    { return "/a/" + id }

// EncodeURIComponent escapes like JavaScript's, so links read the same as the ones handed out before.
func EncodeURIComponent(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
