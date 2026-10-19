package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/phinnerhq/sloppa/internal/api"
)

// setupPrompt is the whole setup, out in the open for the user to read before pasting it. The
// agent installs the CLI, which registers its machine with the link and keeps the token: the agent
// never has to see it.
func setupPrompt(setupURL string) string {
	return fmt.Sprintf(`Install sloppa for PR screenshots, recordings and dev server previews in your app.

1. Install the sloppa CLI, unless `+"`sloppa --version`"+` already works:
   download the binary for this OS and architecture from https://github.com/phinnerhq/sloppa/releases onto the PATH,
   or, with Go installed: go install github.com/phinnerhq/sloppa/cmd/sloppa@latest

2. Register this machine with sloppa, which saves its token for the CLI without showing it:
     sloppa setup '%[1]s'
   The link works once. If this machine was set up before, it keeps its place under a new token.
   Then check it: sloppa whoami

Setup is complete when sloppa whoami names the user and this machine.
If the link was used already or expired (it lasts 30 minutes), ask me for a new one.

3. Read `+"`sloppa --help`"+`, and install the skill at skills/sloppa/SKILL.md in https://github.com/phinnerhq/sloppa
   the way your app takes skills, in user scope for all projects, so you know when and how to use sloppa.`, setupURL)
}

// setupText is plain text that mustn't be cached: it's about a secret.
func setupText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Cache-Control", "no-store")
	text(w, status, body)
}

func setupExpired(w http.ResponseWriter) {
	setupText(w, http.StatusGone, "This setup link was used already, expired or was revoked: make a new one from the dashboard.\n")
}

// clip trims a fact the CLI reported and bounds it, rather than refusing a machine with a long name.
func clip(s string) string {
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > api.MachineNameMax {
		s = strings.TrimSpace(string(runes[:api.MachineNameMax]))
	}
	return s
}

// registerSetup serves setup links: POST spends one on a machine, which `sloppa setup` does, and
// DELETE revokes it. The code in the path is the credential.
func (s *server) registerSetup(mux *http.ServeMux) {
	mux.HandleFunc("POST /setup/{code}", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var facts api.MachineFacts
		// No body at all is a machine nothing is known about.
		if err := json.NewDecoder(r.Body).Decode(&facts); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, r, api.BadRequest("Expected what's known of the machine as JSON: "+err.Error()))
			return
		}
		facts = api.MachineFacts{Hostname: clip(facts.Hostname), OS: clip(facts.OS), Arch: clip(facts.Arch), CLIVersion: clip(facts.CLIVersion)}
		// The machine's current token, when it has one, re-pairs it rather than adding another.
		current, _ := bearerToken(r)
		result, ok, err := s.accounts.RedeemSetup(r.Context(), r.PathValue("code"), current, facts)
		w.Header().Set("Cache-Control", "no-store")
		switch {
		case err != nil:
			writeError(w, r, err)
		case !ok:
			writeError(w, r, api.SetupLinkGone())
		default:
			writeJSON(w, http.StatusOK, result)
		}
	})

	// Opened in a browser (or unfurled in a chat): say what this is, but spend nothing.
	mux.HandleFunc("GET /setup/{code}", func(w http.ResponseWriter, r *http.Request) {
		expires, ok, err := s.accounts.SetupExpiry(r.Context(), r.PathValue("code"))
		switch {
		case err != nil:
			writeError(w, r, err)
		case !ok:
			setupExpired(w)
		default:
			minutes := max(1, int(math.Round(time.Until(expires).Minutes())))
			setupText(w, http.StatusOK, fmt.Sprintf("This sloppa setup link works once, for %d more minute(s). Opening it does nothing: "+
				"paste the setup prompt from the dashboard into an agent, and `sloppa setup` registers its machine with it.\n", minutes))
		}
	})

	mux.HandleFunc("DELETE /setup/{code}", func(w http.ResponseWriter, r *http.Request) {
		revoked, err := s.accounts.RevokeSetup(r.Context(), r.PathValue("code"))
		switch {
		case err != nil:
			writeError(w, r, err)
		case revoked:
			setupText(w, http.StatusOK, "Revoked: the setup link no longer works.\n")
		default:
			setupText(w, http.StatusNotFound, "This setup link was used already, expired or was revoked.\n")
		}
	})
}
