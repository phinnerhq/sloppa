package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/phinnerhq/sloppa/internal/api"
)

type previewOptions struct {
	name, password string
	// ttl is in seconds, 0 for the server's default.
	ttl int
	// port is the dev server's, 0 for a free one.
	port    int
	command []string
}

const (
	// stopGrace is how long the dev server has to exit once asked, before it's killed.
	stopGrace = 3 * time.Second
	// readyPoll is how often the dev server is tried until it first answers.
	readyPoll = 300 * time.Millisecond
	// cleanupTimeout bounds stopping the preview on the server, on the way out.
	cleanupTimeout = 5 * time.Second
)

// expand fills in `{port}`, `{host}`, `{url}` and `{id}`, for dev servers that only take flags.
func expand(args []string, port int, previewURL, id string) []string {
	r := strings.NewReplacer("{port}", strconv.Itoa(port), "{host}", "127.0.0.1", "{url}", previewURL, "{id}", id)
	expanded := make([]string, len(args))
	for i, arg := range args {
		expanded[i] = r.Replace(arg)
	}
	return expanded
}

// runPreview runs the dev server for as long as the preview lasts, and the other way around: the
// dev server exiting, the server ending the preview, a hang-up or Ctrl-C stops both.
func (a *app) runPreview(ctx context.Context, cmd *cli.Command, opts previewOptions) error {
	log := stderr(cmd)
	c, err := a.client()
	if err != nil {
		return err
	}
	port := opts.port
	if port == 0 {
		if port, err = freePort(); err != nil {
			return userErrorf("Couldn't find a free port for the dev server: %v", err)
		}
	}
	cwd, _ := os.Getwd()
	hostname, _ := os.Hostname()
	var started api.StartedPreview
	_, err = c.call(ctx, http.MethodPost, "/api/previews", nil, api.StartPreview{
		Name:     opts.name,
		Password: opts.password,
		TTL:      opts.ttl,
		Port:     port,
		// The URL and id aren't known yet: listings show where they go.
		Command:  strings.Join(expand(opts.command, port, "{url}", "{id}"), " "),
		Cwd:      cwd,
		Hostname: hostname,
	}, &started)
	if err != nil {
		return apiFailure("Couldn't start a preview", err)
	}

	args := expand(opts.command, port, started.URL, started.ID)
	env := append(os.Environ(),
		"PORT="+strconv.Itoa(port),
		"HOST=127.0.0.1",
		"PREVIEW_URL="+started.URL,
		"PREVIEW_ID="+started.ID,
	)
	dev, err := startDevServer(args, env, cmd.Root().Reader, stdout(cmd), log)
	if err != nil {
		stopPreview(c, started.ID)
		return userErrorf("Couldn't start %s: %v", opts.command[0], err)
	}

	tunnelCtx, hangUp := context.WithCancel(context.WithoutCancel(ctx))
	defer hangUp()
	var announcing sync.WaitGroup
	defer announcing.Wait()
	t := &previewTunnel{
		client:  c,
		preview: started,
		port:    port,
		log:     log,
		onConnect: func(first bool) {
			if !first {
				fmt.Fprintf(log, "sloppa: reconnected to %s\n", started.URL)
				return
			}
			fmt.Fprintf(log, "preview_url: %s\npreview_id: %s\npreview_port: %d\npreview_expires_at: %s\n",
				started.URL, started.ID, port, isoTime(started.ExpiresAt))
			announcing.Go(func() { announceReady(tunnelCtx, port, started.URL, log) })
		},
	}
	ended := make(chan tunnelEnd, 1)
	go func() { ended <- t.run(tunnelCtx) }()
	hangups := make(chan os.Signal, 1)
	signal.Notify(hangups, syscall.SIGHUP)
	defer signal.Stop(hangups)

	var code int
	var stopSignal os.Signal = syscall.SIGTERM
	tunnelDone := false
	select {
	case <-dev.Done():
		code = dev.exitCode()
		fmt.Fprintf(log, "sloppa: the dev server exited (%d); the preview is gone\n", code)
	case end := <-ended:
		tunnelDone = true
		code = end.exit
		fmt.Fprintf(log, "sloppa: preview %s; stopped the dev server\n", end.message)
	case <-hangups:
		code, stopSignal = 129, syscall.SIGHUP
		fmt.Fprintln(log, "sloppa: hung up; stopped the dev server")
	case <-ctx.Done():
		code = 130
		if sig, ok := errors.AsType[*interrupted](context.Cause(ctx)); ok {
			stopSignal = sig.signal
		}
		fmt.Fprintln(log, "sloppa: interrupted; stopped the dev server")
	}

	// The URL stops working first, then the dev server stops with everything it started.
	hangUp()
	var wg sync.WaitGroup
	wg.Go(func() { stopPreview(c, started.ID) })
	wg.Go(func() { dev.stop(stopSignal, stopGrace) })
	if !tunnelDone {
		wg.Go(func() { <-ended })
	}
	wg.Wait()
	if code != 0 {
		return &exitError{code: code}
	}
	return nil
}

// stopPreview stops the preview on the server, if it can: it may have stopped it already.
func stopPreview(c *client, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	c.call(ctx, http.MethodDelete, "/api/previews/"+url.PathEscape(id), nil, nil, nil)
}

// announceReady says when the dev server first accepts connections, so whoever waits on this
// output knows when to look.
func announceReady(ctx context.Context, port int, previewURL string, log io.Writer) {
	for {
		if conn, err := connectLocal(ctx, port); err == nil {
			conn.Close()
			fmt.Fprintf(log, "preview_ready: %s\n", previewURL)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(readyPoll):
		}
	}
}
