// Package cli is the `sloppa` command: agent tools for a sloppa server (artifacts, collections and
// previews of dev servers), and `sloppa serve`, the server itself. Results go to stdout, everything
// said along the way to stderr.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mattn/go-isatty"
	"github.com/urfave/cli/v3"
)

const pipeToken = signInAgain

// App is the `sloppa` command line. serve runs the server, for `sloppa serve`. Errors are printed
// and exit the process with their code, so Run only returns once a command succeeded.
func App(serve cli.ActionFunc, version string) *cli.Command {
	root := newApp(serve, version)
	root.ExitErrHandler = func(_ context.Context, cmd *cli.Command, err error) {
		if err != nil {
			os.Exit(report(cmd.Root().ErrWriter, err))
		}
	}
	return root
}

// app is what every command shares.
type app struct{ version string }

// client is the API signed in as the credentials say, or an error saying how to sign in.
func (a *app) client() (*client, error) {
	c, err := loadCredentials()
	if err != nil {
		return nil, err
	}
	return newClient(c, a.version), nil
}

// newApp is App without exiting, for tests to run in-process. Its errors are left to report.
func newApp(serve cli.ActionFunc, version string) *cli.Command {
	a := &app{version: version}
	if serve == nil {
		serve = func(context.Context, *cli.Command) error { return errors.New("this build of sloppa has no server") }
	}
	stop := func() {}
	root := &cli.Command{
		Name:  "sloppa",
		Usage: "Agent tools for a sloppa server",
		Description: "Agent tools for a sloppa server: artifacts (screenshots, recordings, logs) filed in collections like " +
			"`owner/repo #42`, and dev servers shared at public URLs. Run `sloppa <command> --help` for each one's flags.",
		Version:         version,
		HideHelpCommand: true,
		Metadata: helpSections(nil,
			example{`sloppa artifact upload -c "owner/repo #42" checkout.png`, "Prints a Markdown link"},
			example{`sloppa artifact list -c "owner/repo #42"`, ""},
			example{"sloppa preview run -- pnpm dev", "Shares the dev server while it runs"},
		),
		// Actions get a context that a signal cancels, with the signal as its cause, for commands that
		// run a process to hand on.
		Before: func(ctx context.Context, _ *cli.Command) (context.Context, error) {
			ctx, stop = withSignals(ctx)
			return ctx, nil
		},
		After: func(context.Context, *cli.Command) error {
			stop()
			return nil
		},
		Commands: []*cli.Command{
			a.setupCommand(),
			a.loginCommand(),
			a.whoamiCommand(),
			a.artifactCommand(),
			a.collectionCommand(),
			a.previewCommand(),
			{
				Name:   "serve",
				Usage:  "Run the sloppa server; configured by environment variables, see .env.example",
				Action: serve,
			},
		},
	}
	finish(root)
	return root
}

// finish makes a mistyped command line an error like any other, pointing at the help, all the way
// down the tree.
func finish(cmd *cli.Command) {
	cmd.OnUsageError = func(_ context.Context, cmd *cli.Command, err error, _ bool) error {
		return userErrorf("%v\nRun `%s --help` for its flags and examples.", err, cmd.FullName())
	}
	if len(cmd.Commands) > 0 && cmd.Action == nil {
		cmd.Action = groupAction
	}
	for _, sub := range cmd.Commands {
		finish(sub)
	}
}

// groupAction is the action of a command that only has subcommands: it shows them, and refuses one
// that isn't among them.
func groupAction(_ context.Context, cmd *cli.Command) error {
	if cmd.Args().Present() {
		return userErrorf("No command %q in %s: run `%s --help` for its commands.", cmd.Args().First(), cmd.FullName(), cmd.FullName())
	}
	if cmd.Root() == cmd {
		return cli.ShowRootCommandHelp(cmd)
	}
	return cli.ShowSubcommandHelp(cmd)
}

// interrupted is how a signal cancels the actions' context.
type interrupted struct{ signal os.Signal }

func (e *interrupted) Error() string { return e.signal.String() + " received" }

// withSignals is a context that SIGINT or SIGTERM cancels, with an *interrupted as its cause. Later
// signals are caught too, so the dev server of a preview gets its grace period, however impatient
// the Ctrl-C.
func withSignals(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case s := <-signals:
			cancel(&interrupted{signal: s})
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(signals)
		cancel(nil)
	}
}

func stdout(cmd *cli.Command) io.Writer { return cmd.Root().Writer }
func stderr(cmd *cli.Command) io.Writer { return cmd.Root().ErrWriter }

// isTerminal tells whether a stream is a terminal, rather than a pipe or a file.
func isTerminal(stream any) bool {
	f, ok := stream.(*os.File)
	return ok && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
}

// readStdin reads what's piped in, up to limit bytes, unless ctx is done first: the signals that
// cancel it are caught, so they don't end a read that a producer holding its end of the pipe open
// would block for good. A read given up on is left to the process's exit.
func readStdin(ctx context.Context, in io.Reader, limit int64) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	read := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(in, limit))
		read <- result{data, err}
	}()
	select {
	case r := <-read:
		return r.data, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// tokenMax bounds what's read from stdin as a token.
const tokenMax = 64 << 10

func (a *app) loginCommand() *cli.Command {
	return &cli.Command{
		Name:      "login",
		Usage:     "Save a server and a machine token made in the dashboard, piped in, for the other commands",
		ArgsUsage: "<url>",
		Metadata: helpSections(
			[]argument{{"url", "The sloppa server, e.g. https://sloppa.example.com"}},
			example{pipeToken, "A token from the dashboard's machines menu; `sloppa setup` makes one for you"},
			example{"SLOPPA_URL=https://sloppa.example.com SLOPPA_TOKEN=… sloppa artifact list", "Or both in the environment, which wins over what's saved"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 1 {
				return userErrorf("Name the server to sign in to:\n  %s", pipeToken)
			}
			server := strings.TrimRight(cmd.Args().First(), "/")
			if u, err := url.Parse(server); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return userErrorf("Expected the server's URL, like https://sloppa.example.com: %s", cmd.Args().First())
			}
			// A terminal on stdin would wait for typing: the token comes from a pipe, never the
			// keyboard or argv.
			token := ""
			if in := cmd.Root().Reader; !isTerminal(in) {
				read, err := readStdin(ctx, in, tokenMax)
				if err != nil {
					return fmt.Errorf("reading the token from stdin: %w", err)
				}
				token = strings.TrimSpace(string(read))
			}
			if token == "" {
				return userErrorf("Pipe a machine token in, so it stays out of your shell history:\n  %s", pipeToken)
			}
			c := credentials{URL: server, Token: token}
			user, _, err := newClient(c, a.version).session(ctx)
			if err != nil {
				return apiFailure(server+" didn't take that token", err)
			}
			path, err := saveCredentials(c)
			if err != nil {
				return userErrorf("Signed in, but couldn't save the token: %v", err)
			}
			fmt.Fprintf(stdout(cmd), "Signed in to %s as %s; saved to %s\n", server, user.Username, path)
			return nil
		},
	}
}

func (a *app) whoamiCommand() *cli.Command {
	return &cli.Command{
		Name:  "whoami",
		Usage: "Print who the CLI signs in as, and on which machine, which also checks it's set up",
		Flags: []cli.Flag{jsonFlag()},
		Metadata: helpSections(nil,
			example{"sloppa whoami", "The user and machine, like `admin on laptop`, or an error saying how to sign in"},
		),
		Action: func(ctx context.Context, cmd *cli.Command) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			user, raw, err := c.session(ctx)
			if err != nil {
				return apiFailure("Couldn't check who's signed in", err)
			}
			if cmd.Bool("json") {
				return printJSON(stdout(cmd), raw)
			}
			if user.Machine != "" {
				fmt.Fprintf(stdout(cmd), "%s on %s\n", user.Username, user.Machine)
				return nil
			}
			fmt.Fprintln(stdout(cmd), user.Username)
			return nil
		},
	}
}
