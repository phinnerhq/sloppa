package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/tunnel"
)

// devPreview is `sloppa preview run`, in-process, with the test binary as its dev server.
type devPreview struct {
	*running
	fake    *fakeAPI
	preview *fakePreview
	pids    string
}

func startPreview(t *testing.T, ctx context.Context, fake *fakeAPI, flags []string, devArgs ...string) *devPreview {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pids := filepath.Join(t.TempDir(), "pids")
	t.Setenv(roleEnv, "devserver")
	t.Setenv("FIXTURE_PIDS", pids)
	args := append(append([]string{"preview", "run"}, flags...), "--", self)
	r := start(ctx, "", append(args, devArgs...)...)
	d := &devPreview{running: r, fake: fake, pids: pids}
	t.Cleanup(func() {
		// Whatever the test did, nothing it started outlives it.
		if server, grandchild := d.processes(); server != 0 {
			kill(server)
			kill(grandchild)
		}
	})
	d.preview = fake.preview(t)
	return d
}

// processes are the dev server's pid and its grandchild's, once it wrote them.
func (d *devPreview) processes() (int, int) {
	data, _ := os.ReadFile(d.pids)
	var server, grandchild int
	fmt.Sscanf(string(data), "%d %d", &server, &grandchild)
	return server, grandchild
}

func (d *devPreview) said(t *testing.T, what string) {
	t.Helper()
	eventually(t, fmt.Sprintf("%q on stderr; it said:\n%s", what, d.stderr.String()), func() bool {
		return strings.Contains(d.stderr.String(), what)
	})
}

// ready waits for the dev server to answer, and returns its pids.
func (d *devPreview) ready(t *testing.T) (int, int) {
	t.Helper()
	d.said(t, "preview_ready: ")
	eventually(t, "the dev server's pids", func() bool { server, _ := d.processes(); return server != 0 })
	return d.processes()
}

// gone waits for the dev server and its grandchild to be gone, as stopping the preview must leave them.
func gone(t *testing.T, pids ...int) {
	t.Helper()
	eventually(t, fmt.Sprintf("processes %v to stop", pids), func() bool {
		for _, pid := range pids {
			if alive(pid) {
				return false
			}
		}
		return true
	})
}

type devResponse struct {
	Path       string   `json:"path"`
	Body       int      `json:"body"`
	PreviewURL string   `json:"previewUrl"`
	PreviewID  string   `json:"previewId"`
	Args       []string `json:"args"`
}

func getDev(t *testing.T, srv *tunnel.Server, path string) devResponse {
	t.Helper()
	resp, body := get(t, srv, path)
	var got devResponse
	if resp.StatusCode != 200 || json.Unmarshal(body, &got) != nil {
		t.Fatalf("GET %s: %s %s", path, resp.Status, body)
	}
	return got
}

func TestPreviewRunSharesTheDevServerUntilStopped(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	d := startPreview(t, t.Context(), fake, []string{"--name", "demo", "--password", "hunter2", "--ttl", "2h"}, "two words", "{port}", "{url}")
	srv := d.preview.next(t)
	server, grandchild := d.ready(t)

	start := d.preview.start
	port := strconv.Itoa(start.Port)
	self, _ := os.Executable()
	cwd, _ := os.Getwd()
	hostname, _ := os.Hostname()
	if start.Name != "demo" || start.Password != "hunter2" || start.TTL != 7200 || start.Port == 0 ||
		start.Command != self+" two words "+port+" {url}" || start.Cwd != cwd || start.Hostname != hostname {
		t.Errorf("started with %+v", start)
	}
	want := fmt.Sprintf("preview_url: http://demo.preview.localhost/\npreview_id: prev0001\npreview_port: %s\npreview_expires_at: %s\n", port, isoTime(d.preview.ExpiresAt))
	if !strings.HasPrefix(d.stderr.String(), want) && !strings.Contains(d.stderr.String(), "\n"+want) {
		t.Errorf("stderr %q, want %q", d.stderr.String(), want)
	}
	got := getDev(t, srv, "/some/path")
	if got.Path != "/some/path" || got.PreviewURL != "http://demo.preview.localhost/" || got.PreviewID != "prev0001" ||
		strings.Join(got.Args, "|") != "two words|"+port+"|http://demo.preview.localhost/" {
		t.Errorf("the dev server got %+v", got)
	}

	srv.Close(tunnel.CloseStopped, "stopped from sloppa")
	res := d.wait(t)

	if res.code != 0 || !strings.Contains(res.stderr, "sloppa: preview stopped: stopped from sloppa; stopped the dev server\n") {
		t.Errorf("exit %d: %s", res.code, res.stderr)
	}
	gone(t, server, grandchild)
	if !fake.requested("DELETE /api/previews/prev0001") {
		t.Error("didn't stop the preview on the server")
	}
}

func TestPreviewRunReconnectsUntilAFinalClose(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	d := startPreview(t, t.Context(), fake, nil)
	first := d.preview.next(t)
	d.ready(t)

	// A restart, as the server says when it shuts down.
	first.Close(websocket.StatusGoingAway, "restarting")
	second := d.preview.next(t)
	d.said(t, "sloppa: reconnected to http://prev0001.preview.localhost/")
	if !strings.Contains(d.stderr.String(), "sloppa: tunnel lost (") {
		t.Errorf("stderr %s", d.stderr.String())
	}
	if got := getDev(t, second, "/back"); got.Path != "/back" {
		t.Errorf("%+v", got)
	}

	// The server down for a while, behind a proxy that answers for it.
	fake.mu.Lock()
	fake.refuseTunnels = http.StatusBadGateway
	fake.mu.Unlock()
	second.Close(websocket.StatusGoingAway, "restarting")
	eventually(t, "two retries", func() bool { return strings.Count(d.stderr.String(), "tunnel lost") >= 3 })
	fake.mu.Lock()
	fake.refuseTunnels = 0
	fake.mu.Unlock()
	third := d.preview.next(t)
	if got := getDev(t, third, "/again"); got.Path != "/again" {
		t.Errorf("%+v", got)
	}

	third.Close(tunnel.CloseExpired, "the preview's time is up")
	if res := d.wait(t); res.code != 0 || !strings.Contains(res.stderr, "sloppa: preview expired: the preview's time is up; stopped the dev server") {
		t.Errorf("exit %d: %s", res.code, res.stderr)
	}
}

func TestPreviewRunFailsOnARefusal(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	d := startPreview(t, t.Context(), fake, nil)
	srv := d.preview.next(t)
	server, grandchild := d.ready(t)

	srv.Close(tunnel.CloseUnauthorized, "the agent token was revoked")

	if res := d.wait(t); res.code != 1 || !strings.Contains(res.stderr, "sloppa: preview unauthorized: the agent token was revoked; stopped the dev server") {
		t.Errorf("exit %d: %s", res.code, res.stderr)
	}
	gone(t, server, grandchild)
}

func TestPreviewRunEndsWithTheDevServersExitCode(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	d := startPreview(t, t.Context(), fake, nil)
	srv := d.preview.next(t)
	_, grandchild := d.ready(t)

	resp, _ := get(t, srv, "/exit?code=3")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status)
	}

	if res := d.wait(t); res.code != 3 || !strings.Contains(res.stderr, "sloppa: the dev server exited (3); the preview is gone") {
		t.Errorf("exit %d: %s", res.code, res.stderr)
	}
	// What it started goes with it.
	gone(t, grandchild)
	if !fake.requested("DELETE /api/previews/prev0001") {
		t.Error("didn't stop the preview on the server")
	}
}

func TestPreviewRunInterrupted(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	ctx, cancel := context.WithCancelCause(t.Context())
	d := startPreview(t, ctx, fake, nil)
	d.preview.next(t)
	server, grandchild := d.ready(t)

	cancel(&interrupted{signal: os.Interrupt})

	if res := d.wait(t); res.code != 130 || !strings.Contains(res.stderr, "sloppa: interrupted; stopped the dev server") {
		t.Errorf("exit %d: %s", res.code, res.stderr)
	}
	gone(t, server, grandchild)
	if !fake.requested("DELETE /api/previews/prev0001") {
		t.Error("didn't stop the preview on the server")
	}
}

// shortGrace makes the expiry backstop quick to reach.
func shortGrace(t *testing.T) {
	grace := expiryGrace
	expiryGrace = 100 * time.Millisecond
	t.Cleanup(func() { expiryGrace = grace })
}

func TestPreviewRunStopsAtItsExpiryWhenTheServerIsGone(t *testing.T) {
	shortGrace(t)
	fake := newFakeAPI(t)
	fake.signIn(t)
	fake.expiresIn = 2 * time.Second
	d := startPreview(t, t.Context(), fake, nil)
	srv := d.preview.next(t)
	server, _ := d.ready(t)

	fake.mu.Lock()
	fake.refuseTunnels = http.StatusServiceUnavailable
	fake.mu.Unlock()
	srv.Close(websocket.StatusGoingAway, "")

	res := d.wait(t)
	if res.code != 0 || !strings.Contains(res.stderr, "reconnecting") || !strings.Contains(res.stderr, "sloppa: preview expired: the preview's time is up") {
		t.Errorf("exit %d: %s", res.code, res.stderr)
	}
	gone(t, server)
}

func TestPreviewRunCallsAPreviewDeadOnArrivalAFailure(t *testing.T) {
	shortGrace(t)
	fake := newFakeAPI(t)
	fake.signIn(t)
	fake.expiresIn = time.Second
	fake.refuseTunnels = http.StatusServiceUnavailable

	d := startPreview(t, t.Context(), fake, nil)

	if res := d.wait(t); res.code != 1 || !strings.Contains(res.stderr, "sloppa: preview expired before its tunnel ever connected") {
		t.Errorf("exit %d: %s", res.code, res.stderr)
	}
}

func TestPreviewRunEndsWhenThePreviewIsGoneFromTheServer(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	d := startPreview(t, t.Context(), fake, nil)
	srv := d.preview.next(t)
	d.ready(t)

	// Stopped while the tunnel was down: there's nothing to reconnect to.
	fake.mu.Lock()
	delete(fake.previews, "prev0001")
	fake.mu.Unlock()
	srv.Close(websocket.StatusGoingAway, "")

	if res := d.wait(t); res.code != 0 || !strings.Contains(res.stderr, "sloppa: preview stopped: the server has no such preview anymore") {
		t.Errorf("exit %d: %s", res.code, res.stderr)
	}
}

func TestPreviewRunSaysWhenTheDevServerIsntListening(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	elsewhere, _ := freePort()
	t.Setenv("FIXTURE_PORT", strconv.Itoa(elsewhere))
	ctx, cancel := context.WithCancel(t.Context())
	d := startPreview(t, ctx, fake, nil)
	srv := d.preview.next(t)
	d.said(t, "preview_url: ")

	_, err := srv.Dial(t.Context())

	refused, ok := err.(*tunnel.RefusedError)
	if !ok || refused.Message != fmt.Sprintf("connection refused on 127.0.0.1:%d", d.preview.start.Port) {
		t.Errorf("%v", err)
	}
	if strings.Contains(d.stderr.String(), "preview_ready") {
		t.Error("ready, though nothing listens")
	}
	cancel()
	d.wait(t)
}

func TestPreviewRunRefusesBeforeStarting(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	fake.previews["taken001"] = &fakePreview{Preview: api.Preview{ID: "taken001", Name: "demo"}}

	for _, tc := range []struct {
		args []string
		says string
	}{
		{[]string{"--ttl", "30s", "--", "dev"}, "--ttl must be between 1m and 24h"},
		{[]string{"--ttl", "25h", "--", "dev"}, "--ttl must be between 1m and 24h"},
		{[]string{"--ttl", "2w", "--", "dev"}, "Expected a duration like 90s, 30m, 2h or 1d"},
		{[]string{"--name", "Not_A_Name", "--", "dev"}, "lowercase letters, digits and dashes"},
		{[]string{"--port", "70000", "--", "dev"}, "--port must be between 1 and 65535"},
		{nil, "Name the dev server's command"},
		{[]string{"--name", "demo", "--", "dev"}, `Couldn't start a preview: "demo" is taken by another preview right now`},
	} {
		res := run(t, "", append([]string{"preview", "run"}, tc.args...)...)
		if res.code != 1 || !strings.Contains(res.stderr, tc.says) {
			t.Errorf("%v: %+v", tc.args, res)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.previews) != 1 {
		t.Errorf("started %d previews", len(fake.previews)-1)
	}
}

func TestPreviewRunStopsThePreviewWhenTheCommandWontStart(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	missing := filepath.Join(t.TempDir(), "no-such-dev-server")

	res := run(t, "", "preview", "run", "--", missing, "--port", "{port}")

	if res.code != 1 || !strings.Contains(res.stderr, "Couldn't start "+missing+": ") {
		t.Errorf("%+v", res)
	}
	if !fake.requested("DELETE /api/previews/prev0001") {
		t.Error("left the preview running on the server")
	}
}

func TestPreviewListAndStop(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	expires := api.Time(time.Now().Add(time.Hour))
	fake.previews["k3j9x2ab"] = &fakePreview{Preview: api.Preview{ID: "k3j9x2ab", URL: "http://k3j9x2ab.preview.localhost/", Command: "pnpm dev", ExpiresAt: expires}}

	listed := run(t, "", "preview", "list")
	asJSON := run(t, "", "preview", "list", "--json")
	stopped := run(t, "", "preview", "stop", "k3j9x2ab")
	again := run(t, "", "preview", "stop", "k3j9x2ab")

	if listed.stdout != "k3j9x2ab\thttp://k3j9x2ab.preview.localhost/\t"+isoTime(expires)+"\tpnpm dev\n" {
		t.Errorf("list: %q", listed.stdout)
	}
	var previews []api.Preview
	if json.Unmarshal([]byte(asJSON.stdout), &previews); len(previews) != 1 || previews[0].ID != "k3j9x2ab" {
		t.Errorf("--json: %s", asJSON.stdout)
	}
	if stopped.code != 0 || stopped.stdout != "stopped: k3j9x2ab\n" {
		t.Errorf("stop: %+v", stopped)
	}
	if again.code != 0 || again.stdout != "not running: k3j9x2ab\n" {
		t.Errorf("stop again: %+v", again)
	}
}

func TestParseTTL(t *testing.T) {
	for ttl, want := range map[string]int{"90s": 90, "30m": 1800, "2h": 7200, "1d": 86400, "60s": 60, "24h": 86400} {
		if got, err := parseTTL(ttl); err != nil || got != want {
			t.Errorf("parseTTL(%s) = %d, %v", ttl, got, err)
		}
	}
	for _, ttl := range []string{"59s", "2d", "1440m1", "h", "99999999999999999999d", "-1h", "1.5h"} {
		if _, err := parseTTL(ttl); err == nil {
			t.Errorf("parseTTL(%s) took it", ttl)
		}
	}
}

func TestExpand(t *testing.T) {
	got := expand([]string{"vite", "--host", "{host}", "--port={port}", "{url}#{id}"}, 5173, "http://x.preview.localhost/", "x1")
	if strings.Join(got, " ") != "vite --host 127.0.0.1 --port=5173 http://x.preview.localhost/#x1" {
		t.Errorf("%q", got)
	}
}
