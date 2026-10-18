package httpapi

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/phinnerhq/sloppa/internal/api"
)

// setupPrompt is the whole setup, out in the open for the user to read before pasting it. The
// agent installs the CLI and signs it in with a token from the link, which it never has to see.
func setupPrompt(publicURL, setupURL string) string {
	return fmt.Sprintf(`Install sloppa for PR screenshots, recordings and dev server previews in your app.

1. Install the sloppa CLI, unless `+"`sloppa --version`"+` already works:
   download the binary for this OS and architecture from https://github.com/phinnerhq/sloppa/releases onto the PATH,
   or, with Go installed: go install github.com/phinnerhq/sloppa/cmd/sloppa@latest

2. Keep tool output and chat free of the token: nothing needs to show it.
   With shell tracing disabled, trade this link for a token and sign the CLI in with it, in one shell command:
     curl -fsS '%[2]s' --data-urlencode "agent=<app> on $(hostname)" | sloppa login '%[1]s'
   Replace <app> with only your app's name, such as Codex or Claude Code.
   Then check it: sloppa whoami

Setup is complete when sloppa whoami names the user.
A 410 means the link expired or was revoked; ask me for a new one.

3. Read `+"`sloppa --help`"+`, and install the skill at skills/sloppa/SKILL.md in https://github.com/phinnerhq/sloppa
   the way your app takes skills, in user scope for all projects, so you know when and how to use sloppa.

Then ask whether to revoke this setup link, giving me:
curl -fsS -X DELETE '%[2]s'
Mention that it expires 30 minutes after creation and I can paste this prompt into other agents until then.`, publicURL, setupURL)
}

// setupText is plain text that mustn't be cached: it's about a secret.
func setupText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Cache-Control", "no-store")
	text(w, status, body)
}

func setupExpired(w http.ResponseWriter) {
	setupText(w, http.StatusGone, "This setup link expired or was revoked: make a new one from the dashboard.\n")
}

// registerSetup serves setup links: POST trades one for a fresh agent token, as many times as the
// link lives, and DELETE revokes it. Plain text, for curl. The code in the path is the credential.
func (s *server) registerSetup(mux *http.ServeMux) {
	mux.HandleFunc("POST /setup/{code}", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		name := ""
		if err := r.ParseForm(); err == nil {
			name = strings.TrimSpace(r.PostForm.Get("agent"))
		}
		if runes := []rune(name); len(runes) > api.AgentNameMax {
			name = strings.TrimSpace(string(runes[:api.AgentNameMax]))
		}
		if name == "" {
			name = "an agent"
		}
		token, ok, err := s.accounts.RedeemSetup(r.Context(), r.PathValue("code"), name)
		switch {
		case err != nil:
			writeError(w, r, err)
		case !ok:
			setupExpired(w)
		default:
			setupText(w, http.StatusOK, token+"\n")
		}
	})

	// Opened in a browser (or unfurled in a chat): say what this is, but hand out nothing.
	mux.HandleFunc("GET /setup/{code}", func(w http.ResponseWriter, r *http.Request) {
		expires, ok, err := s.accounts.SetupExpiry(r.Context(), r.PathValue("code"))
		switch {
		case err != nil:
			writeError(w, r, err)
		case !ok:
			setupExpired(w)
		default:
			minutes := max(1, int(math.Round(time.Until(expires).Minutes())))
			setupText(w, http.StatusOK, fmt.Sprintf("This sloppa setup link works for %d more minute(s). Opening it does nothing: "+
				"paste the setup prompt from the dashboard into an agent, and its command trades this link for a token.\n", minutes))
		}
	})

	mux.HandleFunc("DELETE /setup/{code}", func(w http.ResponseWriter, r *http.Request) {
		revoked, err := s.accounts.RevokeSetup(r.Context(), r.PathValue("code"))
		switch {
		case err != nil:
			writeError(w, r, err)
		case revoked:
			setupText(w, http.StatusOK, "Revoked: the setup link no longer works. Agents set up with it keep their tokens.\n")
		default:
			setupText(w, http.StatusNotFound, "This setup link was already expired or revoked.\n")
		}
	})
}
