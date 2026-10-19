package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/phinnerhq/sloppa/internal/api"
)

// splitSetupLink is the server a setup link is on, and its code: the link is the server's public
// URL, which may have a path of its own, then /setup/<code>.
func splitSetupLink(link string) (server, code string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", false
	}
	i := strings.LastIndex(u.Path, api.SetupPath(""))
	if i < 0 {
		return "", "", false
	}
	code = u.Path[i+len(api.SetupPath("")):]
	if code == "" || strings.Contains(code, "/") {
		return "", "", false
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment = u.Path[:i], "", "", ""
	return strings.TrimRight(u.String(), "/"), code, true
}

func (a *app) setupCommand() *cli.Command {
	return &cli.Command{
		Name:      "setup",
		Usage:     "Register this machine with a setup link from the dashboard, and save its token for the other commands",
		ArgsUsage: "<setup-link>",
		Description: "Each link works once, within 30 minutes of being made. Run on a machine already signed in to the same " +
			"server, it re-pairs that machine under a new token rather than adding another.",
		Metadata: helpSections(
			[]argument{{"setup-link", "The link in the setup prompt, e.g. https://sloppa.example.com/setup/…"}},
			example{"sloppa setup 'https://sloppa.example.com/setup/…'", "Registers this machine and signs the CLI in"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return userErrorf("Pass the setup link from the dashboard:\n  sloppa setup '<setup-link>'")
			}
			server, code, ok := splitSetupLink(cmd.Args().First())
			if !ok {
				return userErrorf("Expected a setup link, like https://sloppa.example.com/setup/…: %s", cmd.Args().First())
			}
			// The saved token, not SLOPPA_TOKEN: re-pairing rotates it, which would leave the
			// environment holding a dead one.
			current := ""
			if saved, ok := readCredentials(); ok && strings.TrimRight(saved.URL, "/") == server {
				current = saved.Token
			}
			hostname, _ := os.Hostname()
			facts := api.MachineFacts{Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, CLIVersion: a.version}
			var result api.SetupResult
			if _, err := newClient(credentials{URL: server, Token: current}, a.version).
				call(ctx, http.MethodPost, api.SetupPath(url.PathEscape(code)), nil, facts, &result); err != nil {
				return apiFailure("Couldn't set this machine up", err)
			}
			c := credentials{URL: server, Token: result.Token}
			path, err := saveCredentials(c)
			if err != nil {
				return userErrorf("Set this machine up, but couldn't save its token: %v", err)
			}
			user, _, err := newClient(c, a.version).session(ctx)
			if err != nil {
				return apiFailure("Saved the token to "+path+", but couldn't check it", err)
			}
			if result.Repaired {
				fmt.Fprintf(stdout(cmd), "Re-paired machine %q with %s as %s, under a new token; saved to %s\n",
					result.Machine.Name, server, user.Username, path)
			} else {
				fmt.Fprintf(stdout(cmd), "Registered this machine as %q with %s as %s; saved its token to %s\n",
					result.Machine.Name, server, user.Username, path)
			}
			return nil
		},
	}
}
