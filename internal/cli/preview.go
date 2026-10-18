package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/phinnerhq/sloppa/internal/api"
)

func (a *app) previewCommand() *cli.Command {
	return &cli.Command{
		Name:     "preview",
		Usage:    "Share local dev servers, list them, and stop them",
		Commands: []*cli.Command{a.runCommand(), a.previewListCommand(), a.previewStopCommand()},
	}
}

var ttlPattern = regexp.MustCompile(`^(\d+)([smhd])$`)

var ttlUnits = map[string]int{"s": 1, "m": 60, "h": 3600, "d": 86400}

// parseTTL reads `90s`, `30m`, `2h` or `1d` as seconds.
func parseTTL(ttl string) (int, error) {
	m := ttlPattern.FindStringSubmatch(ttl)
	if m == nil {
		return 0, userErrorf("--ttl: Expected a duration like 90s, 30m, 2h or 1d")
	}
	n, err := strconv.Atoi(m[1])
	seconds := n * ttlUnits[m[2]]
	if err != nil || n > int(api.PreviewTTLMax.Seconds()) || seconds < int(api.PreviewTTLMin.Seconds()) || seconds > int(api.PreviewTTLMax.Seconds()) {
		return 0, userErrorf("--ttl must be between 1m and 24h")
	}
	return seconds, nil
}

func (a *app) runCommand() *cli.Command {
	return &cli.Command{
		Name: "run",
		Usage: "Run a dev server and share it at a public URL, for exactly as long as this command runs. " +
			"The command gets PORT, HOST=127.0.0.1, PREVIEW_URL and PREVIEW_ID; stopping either side stops both. " +
			"Prints preview_url: once the tunnel is up, then preview_ready: once the dev server answers, on stderr.",
		ArgsUsage: "-- <command>...",
		// The first argument starts the dev server's command: everything after it is the command's own,
		// flags included, with or without a `--` before it.
		StopOnNthArg: new(1),
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "name", Usage: "A readable, stable subdomain instead of a random one"},
			&cli.StringFlag{Name: "password", Usage: "Ask for this password (any user name) before showing the preview"},
			&cli.StringFlag{Name: "ttl", Usage: "Stop by itself after this long (default 8h, at most 24h)"},
			&cli.IntFlag{Name: "port", Usage: "Use this local port instead of a free one", HideDefault: true},
		},
		Metadata: helpSections(
			[]argument{{"command", "The dev server, after --; {port}, {host}, {url} and {id} are filled in"}},
			example{"sloppa preview run -- pnpm dev", "A dev server that reads PORT and HOST"},
			example{"sloppa preview run --name checkout -- vite --host {host} --port {port} --strictPort", "Vite, which only takes flags, under a stable name"},
			example{"sloppa preview run --password hunter2 --ttl 2h -- python3 -m http.server {port} --bind {host}", "Behind a password, for two hours"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			opts := previewOptions{
				name:     cmd.String("name"),
				password: cmd.String("password"),
				port:     cmd.Int("port"),
				command:  cmd.Args().Slice(),
			}
			if len(opts.command) == 0 {
				return userErrorf("Name the dev server's command, after --:\n  sloppa preview run -- pnpm dev")
			}
			if cmd.IsSet("name") && !api.IsPreviewName(opts.name) {
				return userErrorf("--name: Expected lowercase letters, digits and dashes, at most 40, not starting or ending with a dash")
			}
			if cmd.IsSet("password") && (opts.password == "" || len(opts.password) > 200) {
				return userErrorf("--password must be 1 to 200 characters")
			}
			if cmd.IsSet("port") && (opts.port < 1 || opts.port > 65535) {
				return userErrorf("--port must be between 1 and 65535")
			}
			if cmd.IsSet("ttl") {
				ttl, err := parseTTL(cmd.String("ttl"))
				if err != nil {
					return err
				}
				opts.ttl = ttl
			}
			return a.runPreview(ctx, cmd, opts)
		},
	}
}

func (a *app) previewListCommand() *cli.Command {
	return &cli.Command{
		Name:  "list",
		Usage: "List the previews this account is sharing, newest first: one per line, tab-separated id, URL, expiry and command.",
		Flags: []cli.Flag{jsonFlag()},
		Metadata: helpSections(nil,
			example{"sloppa preview list", ""},
			example{"sloppa preview list --json", "With hostname, directory, port and traffic"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var previews []api.Preview
			raw, err := c.call(ctx, http.MethodGet, "/api/previews", nil, nil, &previews)
			if err != nil {
				return apiFailure("Couldn't list previews", err)
			}
			if cmd.Bool("json") {
				return printJSON(stdout(cmd), raw)
			}
			for _, p := range previews {
				fmt.Fprintln(stdout(cmd), strings.Join([]string{p.ID, p.URL, isoTime(p.ExpiresAt), p.Command}, "\t"))
			}
			return nil
		},
	}
}

func (a *app) previewStopCommand() *cli.Command {
	return &cli.Command{
		Name:      "stop",
		Usage:     "Stop previews: their URLs stop working, and each `sloppa preview run` kills its dev server and exits.",
		ArgsUsage: "<ids>...",
		Metadata: helpSections(
			[]argument{{"ids", "The previews' ids"}},
			example{"sloppa preview stop k3j9x2ab", "An id from sloppa preview list"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			ids := cmd.Args().Slice()
			if len(ids) == 0 {
				return userErrorf("Name the previews to stop by their ids: find them with sloppa preview list")
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			for _, id := range ids {
				// Not running is what was asked for, so a retry succeeds too.
				_, err := c.call(ctx, http.MethodDelete, "/api/previews/"+url.PathEscape(id), nil, nil, nil)
				switch {
				case isTag(err, "PreviewNotFound"):
					fmt.Fprintf(stdout(cmd), "not running: %s\n", id)
				case err != nil:
					return apiFailure("Couldn't stop "+id, err)
				default:
					fmt.Fprintf(stdout(cmd), "stopped: %s\n", id)
				}
			}
			return nil
		},
	}
}
