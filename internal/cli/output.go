package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
)

// jsonFlag is made anew for each command: a flag keeps what it parsed.
func jsonFlag() cli.Flag { return &cli.BoolFlag{Name: "json", Usage: "Print JSON instead of text"} }

// printJSON prints the API's JSON as it came, indented, so it reads the same as `curl` would.
func printJSON(w io.Writer, raw json.RawMessage) error {
	var b bytes.Buffer
	if err := json.Indent(&b, bytes.TrimSpace(raw), "", "  "); err != nil {
		return err
	}
	b.WriteByte('\n')
	_, err := w.Write(b.Bytes())
	return err
}

// isoTime is an instant as JavaScript's toISOString writes it, as the TypeScript CLI printed them.
func isoTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

var shellSafe = regexp.MustCompile(`^[\w@%+=:,./-]+$`)

// shellQuote is a word as a shell would need it typed, for the commands printed back to the caller.
func shellQuote(word string) string {
	if shellSafe.MatchString(word) {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}
