package preview

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/db"
	"github.com/phinnerhq/sloppa/internal/tunnel"
)

var (
	alice  = accounts.Account{ID: 1, Username: "alice", CredentialID: "alice-agent", Kind: accounts.KindAgent}
	alice2 = accounts.Account{ID: 1, Username: "alice", CredentialID: "alice-laptop", Kind: accounts.KindAgent}
	bob    = accounts.Account{ID: 2, Username: "bob", CredentialID: "bob-agent", Kind: accounts.KindAgent}
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// stateDB is a migrated state database with alice (two agent tokens) and bob (one).
func stateDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, username, password_hash, created_ms) VALUES (1, 'alice', 'x', 0), (2, 'bob', 'x', 0)`,
		`INSERT INTO credentials (id, hash, kind, user_id, created_ms) VALUES
			('alice-agent', 'h1', 'agent', 1, 0), ('alice-laptop', 'h2', 'agent', 1, 0), ('bob-agent', 'h3', 'agent', 2, 0)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func previewConfig(burst int) *config.Preview {
	return &config.Preview{
		Origin:    &url.URL{Scheme: "http", Host: "preview.test"},
		MissBurst: burst,
		MissEvery: config.Duration(3 * time.Second),
	}
}

// env is the app with previews on, served by httptest the way the HTTP layer serves it: preview
// hosts go to the proxy, the tunnel route is authenticated with a bearer that is the credential's id.
type env struct {
	t   *testing.T
	db  *sql.DB
	s   *Service
	app *httptest.Server
	// browser reaches any preview host through app.
	browser *http.Client
}

func newEnv(t *testing.T, burst int) *env { return newEnvWith(t, burst, false) }

// newEnvWith is newEnv with TRUST_PROXY set as given.
func newEnvWith(t *testing.T, burst int, trustProxy bool) *env {
	t.Helper()
	e := &env{t: t, db: stateDB(t)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/previews/{id}/tunnel", func(w http.ResponseWriter, r *http.Request) {
		a, ok := e.authenticate(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		e.s.Tunnel(w, r.WithContext(accounts.WithAccount(r.Context(), a)), r.PathValue("id"))
	})
	e.app = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e.s.IsPreviewHost(r.Host) {
			e.s.ServeProxy(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	e.app.Start()
	t.Cleanup(e.app.Close)

	s, err := New(context.Background(), Options{DB: e.db, Config: previewConfig(burst), PublicURL: e.app.URL, TrustProxy: trustProxy})
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	t.Cleanup(s.Close)

	addr := e.app.Listener.Addr().String()
	e.browser = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, addr)
			},
			DisableCompression: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	t.Cleanup(e.browser.CloseIdleConnections)
	return e
}

func (e *env) authenticate(r *http.Request) (accounts.Account, bool) {
	id, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return accounts.Account{}, false
	}
	var a accounts.Account
	err := e.db.QueryRow(`SELECT users.id, username, credentials.id, kind FROM credentials JOIN users ON users.id = user_id WHERE credentials.id = ?`, id).
		Scan(&a.ID, &a.Username, &a.CredentialID, &a.Kind)
	return a, err == nil
}

func (e *env) start(a accounts.Account, in api.StartPreview) api.StartedPreview {
	e.t.Helper()
	started, err := e.s.Start(context.Background(), a, in)
	if err != nil {
		e.t.Fatal(err)
	}
	return started
}

func (e *env) get(rawURL string, header ...string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := e.browser.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

// body reads and closes resp's body.
func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// cli is `sloppa preview run`'s end of a tunnel.
type cli struct {
	t    *testing.T
	done chan error
}

// connect opens the tunnel as the CLI does, with token as the bearer, serving connections with dial.
func (e *env) connect(started api.StartedPreview, token string, dial func() (net.Conn, error)) *cli {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, started.TunnelURL, &websocket.DialOptions{
		Subprotocols: []string{tunnel.Subprotocol},
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		e.t.Fatal(err)
	}
	c, err := tunnel.NewClient(ctx, ws)
	if err != nil {
		e.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Serve(dial) }()
	e.t.Cleanup(c.Close)
	return &cli{t: e.t, done: done}
}

// serve connects a CLI for started that serves dev, and waits until the proxy routes through it.
func (e *env) serve(started api.StartedPreview, token string, dev *httptest.Server) *cli {
	e.t.Helper()
	c := e.connect(started, token, dialTo(dev))
	e.waitConnected(started.ID)
	return c
}

func dialTo(dev *httptest.Server) func() (net.Conn, error) {
	return func() (net.Conn, error) { return net.Dial("tcp", dev.Listener.Addr().String()) }
}

func (e *env) waitConnected(id string) *lease {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.s.mu.Lock()
		p := e.s.byID[id]
		var l *lease
		if p != nil {
			l = p.lease
		}
		e.s.mu.Unlock()
		if l != nil {
			return l
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatalf("preview %s never connected", id)
	return nil
}

// closed waits for how the server ended the tunnel.
func (c *cli) closed() *tunnel.ClosedError {
	c.t.Helper()
	select {
	case err := <-c.done:
		var closed *tunnel.ClosedError
		if !errors.As(err, &closed) {
			c.t.Fatalf("expected a ClosedError, got %v", err)
		}
		return closed
	case <-time.After(10 * time.Second):
		c.t.Fatal("the tunnel never closed")
		return nil
	}
}

func (c *cli) expectClose(code websocket.StatusCode) *tunnel.ClosedError {
	c.t.Helper()
	closed := c.closed()
	if closed.Code != code {
		c.t.Fatalf("expected close code %d, got %d (%s): %v", code, closed.Code, closed.Reason, closed.Err)
	}
	return closed
}

func (c *cli) expectOpen() {
	c.t.Helper()
	select {
	case err := <-c.done:
		c.t.Fatalf("the tunnel closed: %v", err)
	default:
	}
}

// previewURL is path on started's preview.
func previewURL(started api.StartedPreview, path string) string {
	return strings.TrimSuffix(started.URL, "/") + path
}
