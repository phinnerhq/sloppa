package preview

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/phinnerhq/sloppa/internal/api"
)

// devServer is a dev server, as the CLI's machine runs it: it reports what it was asked, streams,
// redirects to its own address, sets cookies, and takes WebSockets.
func devServer(t *testing.T, release <-chan struct{}) *httptest.Server {
	t.Helper()
	var dev *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"host":          r.Host,
			"path":          r.URL.RequestURI(),
			"forwardedFor":  r.Header.Get("X-Forwarded-For"),
			"forwardedHost": r.Header.Get("X-Forwarded-Host"),
			"proto":         r.Header.Get("X-Forwarded-Proto"),
			"authorization": r.Header.Get("Authorization"),
			"cookie":        r.Header.Get("Cookie"),
		})
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: second\n\n")
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dev.URL+"/landed?x=1#top", http.StatusFound)
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com/elsewhere", http.StatusFound)
	})
	mux.HandleFunc("/cookie", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "wide=1; Domain=.preview.test; Path=/")
		w.Header().Add("Set-Cookie", "own=2; Domain=demo.preview.test; HttpOnly")
	})
	mux.HandleFunc("/robots", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "all")
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			kind, msg, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			if err := ws.Write(r.Context(), kind, append([]byte("echo "), msg...)); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		n, _ := bufio.NewReader(r.Body).Discard(1 << 30)
		fmt.Fprint(w, n)
	})
	dev = httptest.NewServer(mux)
	t.Cleanup(dev.Close)
	return dev
}

func devPort(t *testing.T, dev *httptest.Server) int {
	t.Helper()
	_, p, _ := net.SplitHostPort(dev.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	return port
}

func echo(t *testing.T, e *env, rawURL string, header ...string) map[string]string {
	t.Helper()
	resp := e.get(rawURL, header...)
	if resp.StatusCode != 200 {
		t.Fatalf("%s: %d %s", rawURL, resp.StatusCode, body(t, resp))
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(body(t, resp)), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestProxiesRequests(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	port := devPort(t, dev)
	started := e.start(alice, api.StartPreview{Name: "demo", Port: port})
	if started.URL != "http://demo.preview.test/" {
		t.Fatalf("url %s", started.URL)
	}
	if want := "ws" + strings.TrimPrefix(e.app.URL, "http") + "/api/previews/" + started.ID + "/tunnel"; started.TunnelURL != want {
		t.Fatalf("tunnel url %s, want %s", started.TunnelURL, want)
	}
	e.serve(started, alice.CredentialID, dev)

	// The port in Host plays no part in routing, and the browser's forwarding headers go.
	got := echo(t, e, "http://demo.preview.test:4100/echo?q=1", "X-Forwarded-For", "6.6.6.6", "X-Forwarded-Host", "evil.test", "Cookie", "a=1")
	want := map[string]string{
		"host":          fmt.Sprintf("127.0.0.1:%d", port),
		"path":          "/echo?q=1",
		"forwardedFor":  "127.0.0.1",
		"forwardedHost": "demo.preview.test",
		"proto":         "http",
		"authorization": "",
		"cookie":        "a=1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}

	resp := e.get("http://demo.preview.test/echo")
	body(t, resp)
	if resp.Header.Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Errorf("X-Robots-Tag %q", resp.Header.Get("X-Robots-Tag"))
	}
	resp = e.get("http://demo.preview.test/robots")
	body(t, resp)
	if got := resp.Header.Values("X-Robots-Tag"); len(got) != 1 || got[0] != "all" {
		t.Errorf("the dev server's X-Robots-Tag should win, got %q", got)
	}

	// Uploads stream through.
	req, _ := http.NewRequest("POST", "http://demo.preview.test/upload", strings.NewReader(strings.Repeat("x", 3<<20)))
	resp, err := e.browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, resp); got != strconv.Itoa(3<<20) {
		t.Errorf("upload: dev server read %s bytes", got)
	}

	list, err := e.s.List(context.Background(), alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Connected || list[0].Requests != 4 || list[0].Name != "demo" || list[0].Port != port || list[0].Protected {
		t.Fatalf("list: %+v", list)
	}
}

func TestTrustProxyForwardsTheProxysEntry(t *testing.T) {
	e := newEnvWith(t, 20, true)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Port: devPort(t, dev)})
	e.serve(started, alice.CredentialID, dev)
	got := echo(t, e, previewURL(started, "/echo"), "X-Forwarded-For", "1.1.1.1, 198.51.100.7")
	if got["forwardedFor"] != "198.51.100.7" {
		t.Fatalf("X-Forwarded-For %q", got["forwardedFor"])
	}
}

func TestStreamsResponses(t *testing.T) {
	e := newEnv(t, 20)
	release := make(chan struct{})
	dev := devServer(t, release)
	started := e.start(alice, api.StartPreview{Port: devPort(t, dev)})
	e.serve(started, alice.CredentialID, dev)

	resp := e.get(previewURL(started, "/stream"))
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	line, err := r.ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("first event: %q %v", line, err)
	}
	// Only now does the dev server finish: the first event came through on its own.
	close(release)
	r.ReadString('\n')
	if line, _ := r.ReadString('\n'); line != "data: second\n" {
		t.Fatalf("second event: %q", line)
	}
}

// countedConn is a connection to the dev server, counted while open.
type countedConn struct {
	net.Conn
	once sync.Once
	open *atomic.Int32
}

func (c *countedConn) Close() error {
	c.once.Do(func() { c.open.Add(-1) })
	return c.Conn.Close()
}

// A preview holds so many connections to the dev server at once, a WebSocket among them: past that,
// requests are turned away at once, and those holding theirs carry on.
func TestPreviewConnectionsAreCapped(t *testing.T) {
	e := newEnv(t, 20)
	release := make(chan struct{})
	dev := devServer(t, release)
	started := e.start(alice, api.StartPreview{Name: "busy", Port: devPort(t, dev)})
	var open, peak atomic.Int32
	e.connect(started, alice.CredentialID, func() (net.Conn, error) {
		conn, err := dialTo(dev)()
		if err != nil {
			return nil, err
		}
		n := open.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		return &countedConn{Conn: conn, open: &open}, nil
	})
	e.waitConnected(started.ID)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://busy.preview.test/ws", &websocket.DialOptions{HTTPClient: e.browser})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	var streams []*http.Response
	for i := range maxStreams - 1 {
		resp := e.get(previewURL(started, "/stream"))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: %d %s", i, resp.StatusCode, body(t, resp))
		}
		if line, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil || line != "data: first\n" {
			t.Fatalf("stream %d: %q %v", i, line, err)
		}
		streams = append(streams, resp)
	}

	resp := e.get(previewURL(started, "/echo"))
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" ||
		!strings.Contains(body(t, resp), "Preview busy") {
		t.Errorf("one too many: %d %v", resp.StatusCode, resp.Header)
	}
	if n := peak.Load(); n != maxStreams {
		t.Errorf("%d connections to the dev server at once, want %d", n, maxStreams)
	}
	if err := ws.Write(ctx, websocket.MessageText, []byte("still here")); err != nil {
		t.Fatal(err)
	}
	if _, got, err := ws.Read(ctx); err != nil || string(got) != "echo still here" {
		t.Fatalf("WebSocket: %q %v", got, err)
	}

	close(release)
	for _, resp := range streams {
		body(t, resp)
	}
	echo(t, e, previewURL(started, "/echo"))
	if n := peak.Load(); n > maxStreams {
		t.Errorf("%d connections to the dev server at once, over %d", n, maxStreams)
	}
}

func TestUpgradesWebSockets(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Name: "hmr", Port: devPort(t, dev)})
	e.serve(started, alice.CredentialID, dev)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "ws://hmr.preview.test/ws", &websocket.DialOptions{HTTPClient: e.browser})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	if resp.Header.Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Errorf("101 without X-Robots-Tag: %v", resp.Header)
	}
	for _, msg := range []string{"hello", "again"} {
		if err := ws.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
			t.Fatal(err)
		}
		_, got, err := ws.Read(ctx)
		if err != nil || string(got) != "echo "+msg {
			t.Fatalf("got %q, %v", got, err)
		}
	}
	list, _ := e.s.List(ctx, alice)
	if list[0].Connections != 1 {
		t.Errorf("an open WebSocket counts as a connection: %+v", list[0])
	}

	// Stopping the preview drops the WebSocket too.
	if err := e.s.Stop(ctx, alice, started.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.Read(ctx); err == nil {
		t.Fatal("the WebSocket outlived its preview")
	}
}

func TestRewritesRedirectsAndCookies(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Name: "demo", Port: devPort(t, dev)})
	e.serve(started, alice.CredentialID, dev)

	resp := e.get("http://demo.preview.test/redirect")
	body(t, resp)
	if got := resp.Header.Get("Location"); got != "http://demo.preview.test/landed?x=1#top" {
		t.Errorf("Location %q", got)
	}
	resp = e.get("http://demo.preview.test/away")
	body(t, resp)
	if got := resp.Header.Get("Location"); got != "https://example.com/elsewhere" {
		t.Errorf("Location %q", got)
	}
	resp = e.get("http://demo.preview.test/cookie")
	body(t, resp)
	if got := resp.Header.Values("Set-Cookie"); len(got) != 2 || got[0] != "wide=1; Path=/" || got[1] != "own=2; Domain=demo.preview.test; HttpOnly" {
		t.Errorf("Set-Cookie %q", got)
	}
}

func basic(user, pw string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pw))
}

func TestPassword(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Name: "secret", Password: "hunter2", Port: devPort(t, dev)})
	e.serve(started, alice.CredentialID, dev)

	for _, header := range [][]string{nil, {"Authorization", basic("x", "wrong")}, {"Authorization", "Bearer hunter2"}} {
		resp := e.get("http://secret.preview.test/echo", header...)
		page := body(t, resp)
		if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") != `Basic realm="secret", charset="UTF-8"` || !strings.Contains(page, "Password needed") {
			t.Fatalf("%v: %d %v", header, resp.StatusCode, resp.Header)
		}
	}
	// Any user name, and the dev server never sees the password.
	for _, user := range []string{"", "me", "someone else"} {
		got := echo(t, e, "http://secret.preview.test/echo", "Authorization", basic(user, "hunter2"))
		if got["authorization"] != "" {
			t.Fatalf("the password reached the dev server: %q", got["authorization"])
		}
	}
	p, _ := e.s.lookup("secret")
	p.verifiedMu.Lock()
	n := len(p.verified)
	p.verifiedMu.Unlock()
	if n != 3 {
		t.Errorf("%d verified headers cached, want 3", n)
	}
	list, _ := e.s.List(context.Background(), alice)
	if !list[0].Protected {
		t.Error("not listed as protected")
	}
}

func TestUnprotectedPreviewForwardsAuthorization(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Port: devPort(t, dev)})
	e.serve(started, alice.CredentialID, dev)
	if got := echo(t, e, previewURL(started, "/echo"), "Authorization", basic("a", "b")); got["authorization"] != basic("a", "b") {
		t.Fatalf("authorization %q", got["authorization"])
	}
}

func TestUnknownHostsAreRateLimited(t *testing.T) {
	e := newEnvWith(t, 3, true)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Name: "real", Port: devPort(t, dev)})
	e.serve(started, alice.CredentialID, dev)

	for i := range 3 {
		resp := e.get(fmt.Sprintf("http://guess%d.preview.test/", i))
		page := body(t, resp)
		if resp.StatusCode != 404 {
			t.Fatalf("guess %d: %d", i, resp.StatusCode)
		}
		if !strings.Contains(page, "<title>No preview here</title>") || strings.Contains(page, "real") {
			t.Fatalf("guess %d: a 404 page that says too little, or names a real preview:\n%s", i, page)
		}
		if resp.Header.Get("Content-Security-Policy") != "default-src 'none'; style-src 'unsafe-inline'" || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("headers %v", resp.Header)
		}
	}
	// Out of guesses: even a real preview is refused, until the bucket refills.
	for _, host := range []string{"guess9", "real"} {
		resp := e.get("http://" + host + ".preview.test/echo")
		body(t, resp)
		if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != "3" {
			t.Fatalf("%s: %d, Retry-After %q", host, resp.StatusCode, resp.Header.Get("Retry-After"))
		}
	}
	// Someone else isn't.
	if got := echo(t, e, "http://real.preview.test/echo", "X-Forwarded-For", "203.0.113.5"); got["forwardedFor"] != "203.0.113.5" {
		t.Fatal(got)
	}
}

func TestWrongPasswordsAreRateLimited(t *testing.T) {
	e := newEnv(t, 2)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Name: "secret", Password: "hunter2", Port: devPort(t, dev)})
	e.serve(started, alice.CredentialID, dev)
	for _, want := range []int{401, 401, 429} {
		resp := e.get("http://secret.preview.test/echo", "Authorization", basic("", "guess"))
		body(t, resp)
		if resp.StatusCode != want {
			t.Fatalf("got %d, want %d", resp.StatusCode, want)
		}
	}
}

// A batch of guesses sent at once all find a miss to spare before any is checked: each takes its
// miss first, so the batch gets no more guesses than one at a time would.
func TestConcurrentWrongPasswordsAreRateLimited(t *testing.T) {
	e := newEnv(t, 2)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Name: "secret", Password: "hunter2", Port: devPort(t, dev)})
	e.serve(started, alice.CredentialID, dev)
	const n = 12
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			req, _ := http.NewRequest("GET", "http://secret.preview.test/echo", nil)
			req.Header.Set("Authorization", basic("", "guess"))
			resp, err := e.browser.Do(req)
			if err != nil {
				codes <- 0
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			codes <- resp.StatusCode
		})
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[401] != 2 || counts[429] != n-2 {
		t.Fatalf("statuses %v, want 2 checked and the rest refused", counts)
	}
}

func TestApexAndOtherHosts(t *testing.T) {
	e := newEnv(t, 20)
	resp := e.get("http://preview.test/healthz")
	if got := body(t, resp); resp.StatusCode != 200 || got != "ok\n" {
		t.Fatalf("healthz: %d %q", resp.StatusCode, got)
	}
	resp = e.get("http://preview.test/")
	if got := body(t, resp); resp.StatusCode != 404 || !strings.Contains(got, "<title>sloppa preview</title>") {
		t.Fatalf("apex: %d %q", resp.StatusCode, got)
	}
	for host, want := range map[string]bool{
		"preview.test": true, "demo.preview.test:4100": true, "DEMO.Preview.Test": true,
		"preview.test.evil": false, "notpreview.test": false, "127.0.0.1:4100": false, "": false,
	} {
		if got := e.s.IsPreviewHost(host); got != want {
			t.Errorf("IsPreviewHost(%q) = %v", host, got)
		}
	}
}

func TestRefusedPortIsA502(t *testing.T) {
	e := newEnv(t, 20)
	closed := httptest.NewServer(http.NotFoundHandler())
	port := devPort(t, closed)
	closed.Close()
	started := e.start(alice, api.StartPreview{Port: port})
	e.connect(started, alice.CredentialID, func() (net.Conn, error) {
		return net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	})
	e.waitConnected(started.ID)

	resp := e.get(previewURL(started, "/"))
	page := body(t, resp)
	if resp.StatusCode != 502 || !strings.Contains(page, "Dev server not listening") || !strings.Contains(page, fmt.Sprintf("127.0.0.1:%d", port)) {
		t.Fatalf("%d %s", resp.StatusCode, page)
	}
}

func TestDisconnectedPreviewIsA502(t *testing.T) {
	e := newEnv(t, 1)
	started := e.start(alice, api.StartPreview{Port: 3000})
	for range 3 {
		resp := e.get(previewURL(started, "/"))
		page := body(t, resp)
		// Not a miss: the preview exists, it's waiting for its CLI.
		if resp.StatusCode != 502 || !strings.Contains(page, "Preview disconnected") {
			t.Fatalf("%d %s", resp.StatusCode, page)
		}
	}
}

func TestRewriteLocation(t *testing.T) {
	public, _ := url.Parse("https://demo.preview.test/")
	for location, want := range map[string]string{
		"http://127.0.0.1:5173/landed?x=1": "https://demo.preview.test/landed?x=1",
		"http://localhost:5173/":           "https://demo.preview.test/",
		"http://localhost:5173":            "https://demo.preview.test/",
		"http://[::1]:5173/a":              "https://demo.preview.test/a",
		"http://0.0.0.0:5173/a#b":          "https://demo.preview.test/a#b",
		"/relative":                        "/relative",
		"http://127.0.0.1:9999/":           "http://127.0.0.1:9999/",
		"https://example.com:5173/":        "https://example.com:5173/",
		"http://127.0.0.1/":                "http://127.0.0.1/",
	} {
		if got := rewriteLocation(location, 5173, public); got != want {
			t.Errorf("rewriteLocation(%q) = %q, want %q", location, got, want)
		}
	}
}

func TestScopeCookie(t *testing.T) {
	for cookie, want := range map[string]string{
		"a=1; Domain=.preview.test; Path=/":    "a=1; Path=/",
		"b=2; domain=localhost; HttpOnly":      "b=2; HttpOnly",
		"c=3; Domain=Demo.Preview.Test":        "c=3; Domain=Demo.Preview.Test",
		"d=4; Path=/; Secure":                  "d=4; Path=/; Secure",
		"e=5":                                  "e=5",
		"f=6;Domain=.demo.preview.test;Path=/": "f=6;Domain=.demo.preview.test;Path=/",
	} {
		if got := scopeCookie(cookie, "demo.preview.test"); got != want {
			t.Errorf("scopeCookie(%q) = %q, want %q", cookie, got, want)
		}
	}
}

func TestBasicPassword(t *testing.T) {
	for header, want := range map[string]string{
		basic("", "pw"):      "pw",
		basic("user", "p:w"): "p:w",
		"basic " + base64.RawStdEncoding.EncodeToString([]byte("u:pw")): "pw",
	} {
		if got, ok := basicPassword(header); !ok || got != want {
			t.Errorf("basicPassword(%q) = %q, %v", header, got, ok)
		}
	}
	for _, header := range []string{"", "Bearer x", "Basic !!!", "Basic " + base64.StdEncoding.EncodeToString([]byte("nocolon"))} {
		if _, ok := basicPassword(header); ok {
			t.Errorf("basicPassword(%q) passed", header)
		}
	}
}
