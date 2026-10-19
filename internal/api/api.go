// Package api is the HTTP API's wire format, shared by the server and the CLI. Optional fields are
// left out rather than null, times are UTC with millisecond precision, and errors carry a code
// naming them (see Error).
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

	MachineNameMax = 100
	PasswordMin    = 8

	MaxUploadBytes = 100 << 20

	PageSize         = 60
	GroupPageSize    = 24
	GroupPreviewSize = 4

	PreviewTTLDefault = 8 * time.Hour
	PreviewTTLMax     = 24 * time.Hour
	PreviewTTLMin     = time.Minute

	SessionCookie = "sloppa_session"
	// TokenPrefix starts machine tokens, so secret scanners (and people) can tell what leaked.
	TokenPrefix = "sloppa_"
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
	// Machine is the name of the machine whose token signed in, when one did.
	Machine string `json:"machine,omitempty"`
}

type Login struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type ChangePassword struct {
	Current string `json:"current"`
	Next    string `json:"next"`
}

// Machine is a computer the CLI is set up on, signed in with a token of its own. Hostname, OS, Arch
// and CLIVersion are what the CLI said when it was last set up there; machines made by hand don't
// have them.
type Machine struct {
	ID string `json:"id"`
	// Name is the hostname, unless someone renamed it.
	Name       string    `json:"name"`
	Hostname   string    `json:"hostname,omitempty"`
	OS         string    `json:"os,omitempty"`
	Arch       string    `json:"arch,omitempty"`
	CLIVersion string    `json:"cliVersion,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	// LastSeenAt is when its token was last used.
	LastSeenAt *time.Time `json:"lastSeenAt,omitempty"`
}

type MachineName struct {
	Name string `json:"name"`
}

// CreatedMachine is shown once: only a hash of the token is kept.
type CreatedMachine struct {
	Machine Machine `json:"machine"`
	Token   string  `json:"token"`
}

// MachineFacts is what `sloppa setup` says about the machine it runs on, posted to a setup link.
type MachineFacts struct {
	Hostname   string `json:"hostname,omitempty"`
	OS         string `json:"os,omitempty"`
	Arch       string `json:"arch,omitempty"`
	CLIVersion string `json:"cliVersion,omitempty"`
}

// SetupResult is a setup link redeemed: a new machine, or with Repaired, the one whose token came
// with the request, under a new token.
type SetupResult struct {
	CreatedMachine
	Repaired bool `json:"repaired"`
}

type SetupLink struct {
	ID  string `json:"id"`
	URL string `json:"url"`
	// Prompt is pasted into an agent, which sets its machine up from the link.
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
// on: the tunnel connects to it with the same token, as often as it needs to.
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
	// TunnelURL is the WebSocket the CLI connects to, with its token as a bearer.
	TunnelURL string    `json:"tunnelUrl"`
	ExpiresAt time.Time `json:"expiresAt"`
}

var previewName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?$`)

// IsPreviewName tells whether name works as a preview's subdomain: one lowercase DNS label, like
// `checkout-demo`.
func IsPreviewName(name string) bool { return previewName.MatchString(name) }

var previewID = regexp.MustCompile(`^[a-z0-9]{8}$`)

func IsPreviewID(id string) bool { return previewID.MatchString(id) }

// SetupPath is where `sloppa setup` redeems a setup link, outside the typed API: the code in the path
// is the credential, kept out of the logs.
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
