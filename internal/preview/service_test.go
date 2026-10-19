package preview

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/tunnel"
)

func apiError(t *testing.T, err error, kind api.ErrorKind) *api.Error {
	t.Helper()
	var e *api.Error
	if !errors.As(err, &e) || e.Code != kind.Code {
		t.Fatalf("expected %s, got %v", kind.Code, err)
	}
	return e
}

func TestStartValidates(t *testing.T) {
	e := newEnv(t, 20)
	for _, in := range []api.StartPreview{
		{Name: "Demo", Port: 3000},
		{Name: "-demo", Port: 3000},
		{Name: strings.Repeat("a", 41), Port: 3000},
		{Name: "a.b", Port: 3000},
		{Password: strings.Repeat("p", 201), Port: 3000},
		{TTL: 59, Port: 3000},
		{TTL: 24*60*60 + 1, Port: 3000},
		{TTL: -1, Port: 3000},
		{Port: 0},
		{Port: 65536},
	} {
		_, err := e.s.Start(context.Background(), alice, in)
		if e := apiError(t, err, api.KindBadRequest); e.Status != 400 || e.Message == "" {
			t.Errorf("%+v: %+v", in, e)
		}
	}
	// 200 characters as JavaScript counts them: an emoji is two.
	if _, err := e.s.Start(context.Background(), alice, api.StartPreview{Password: strings.Repeat("😀", 101), Port: 3000}); err == nil {
		t.Error("a 202-unit password passed")
	}

	before := time.Now()
	started := e.start(alice, api.StartPreview{Port: 3000, TTL: 60, Command: strings.Repeat("c", 400), Hostname: strings.Repeat("h", 120)})
	if !api.IsPreviewID(started.ID) || started.URL != "http://"+started.ID+".preview.test/" {
		t.Fatalf("%+v", started)
	}
	if d := started.ExpiresAt.Sub(before); d < 59*time.Second || d > 61*time.Second {
		t.Fatalf("expires in %v", d)
	}
	list, _ := e.s.List(context.Background(), alice)
	if len([]rune(list[0].Command)) != 301 || !strings.HasSuffix(list[0].Command, "…") || len([]rune(list[0].Hostname)) != 101 {
		t.Fatalf("not truncated: %+v", list[0])
	}
	if list[0].Connected || !list[0].ConnectedAt.Equal(api.Time(list[0].ConnectedAt)) {
		t.Fatalf("%+v", list[0])
	}

	started = e.start(alice, api.StartPreview{Port: 3000})
	var expires int64
	e.db.QueryRow(`SELECT expires_ms - created_ms FROM previews WHERE id = ?`, started.ID).Scan(&expires)
	if time.Duration(expires)*time.Millisecond != api.PreviewTTLDefault {
		t.Fatalf("default ttl %v", time.Duration(expires)*time.Millisecond)
	}
}

func TestStoresTheRow(t *testing.T) {
	e := newEnv(t, 20)
	started := e.start(alice, api.StartPreview{Name: "demo", Password: "pw", Port: 5173, Command: "pnpm dev", Cwd: "/src", Hostname: "laptop"})
	var label, credential, hash, command string
	var port int
	if err := e.db.QueryRow(`SELECT label, credential_id, password_hash, port, command FROM previews WHERE id = ?`, started.ID).
		Scan(&label, &credential, &hash, &port, &command); err != nil {
		t.Fatal(err)
	}
	if label != "demo" || credential != alice.CredentialID || !strings.HasPrefix(hash, "$argon2id$") || port != 5173 || command != "pnpm dev" {
		t.Fatalf("row: %s %s %s %d %s", label, credential, hash, port, command)
	}
	var name *string
	other := e.start(alice, api.StartPreview{Port: 5173})
	e.db.QueryRow(`SELECT name FROM previews WHERE id = ?`, other.ID).Scan(&name)
	if name != nil {
		t.Fatalf("name %q, want NULL", *name)
	}
}

func TestDisabled(t *testing.T) {
	s, err := New(context.Background(), Options{DB: stateDB(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.List(context.Background(), alice)
	apiError(t, err, api.KindPreviewsUnavailable)
	_, err = s.Start(context.Background(), alice, api.StartPreview{Port: 3000})
	apiError(t, err, api.KindPreviewsUnavailable)
	apiError(t, s.Stop(context.Background(), alice, "abcd1234"), api.KindPreviewsUnavailable)
	if s.IsPreviewHost("demo.preview.test") {
		t.Fatal("claims a host")
	}
}

func TestListsOnlyOwnNewestFirst(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	first := e.start(alice, api.StartPreview{Name: "first", Port: devPort(t, dev)})
	time.Sleep(2 * time.Millisecond)
	second := e.start(alice, api.StartPreview{Name: "second", Port: 3000})
	e.start(bob, api.StartPreview{Name: "bobs", Port: 3000})
	time.Sleep(2 * time.Millisecond)
	e.serve(first, alice.CredentialID, dev)

	list, err := e.s.List(context.Background(), alice)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != first.ID || !list[0].Connected || list[1].ID != second.ID || list[1].Connected {
		t.Fatalf("%+v", list)
	}
	list, _ = e.s.List(context.Background(), bob)
	if len(list) != 1 || list[0].Name != "bobs" {
		t.Fatalf("%+v", list)
	}
}

func TestStopEndsTheTunnel(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Name: "demo", Port: devPort(t, dev)})
	c := e.serve(started, alice.CredentialID, dev)

	// Only one's own.
	apiError(t, e.s.Stop(context.Background(), bob, started.ID), api.KindPreviewNotFound)
	c.expectOpen()

	if err := e.s.Stop(context.Background(), alice, started.ID); err != nil {
		t.Fatal(err)
	}
	if closed := c.expectClose(tunnel.CloseStopped); !closed.Final() || closed.Reason != "stopped from sloppa" {
		t.Fatalf("%+v", closed)
	}
	apiError(t, e.s.Stop(context.Background(), alice, started.ID), api.KindPreviewNotFound)
	resp := e.get("http://demo.preview.test/")
	if body(t, resp); resp.StatusCode != 404 {
		t.Fatalf("stopped preview still served: %d", resp.StatusCode)
	}
	// A CLI that reconnects anyway is told it's over.
	e.connect(started, alice.CredentialID, dialTo(dev)).expectClose(tunnel.CloseUnauthorized)
}

// expire moves a preview's expiry to the past, as if its time was up.
func (e *env) expire(id string) {
	e.t.Helper()
	past := time.Now().Add(-time.Second)
	if _, err := e.db.Exec(`UPDATE previews SET expires_ms = ? WHERE id = ?`, past.UnixMilli(), id); err != nil {
		e.t.Fatal(err)
	}
	e.s.mu.Lock()
	e.s.byID[id].expires = past
	e.s.mu.Unlock()
}

func TestExpiryEndsTheTunnel(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Port: devPort(t, dev)})
	c := e.serve(started, alice.CredentialID, dev)

	e.expire(started.ID)
	// Routing stops right away, before the sweep.
	resp := e.get(previewURL(started, "/echo"))
	if body(t, resp); resp.StatusCode != 404 {
		t.Fatalf("expired preview served: %d", resp.StatusCode)
	}
	if err := e.s.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if closed := c.expectClose(tunnel.CloseExpired); closed.Reason != "the preview's time is up" {
		t.Fatal(closed.Reason)
	}
	var n int
	e.db.QueryRow(`SELECT count(*) FROM previews`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows left", n)
	}
	list, _ := e.s.List(context.Background(), alice)
	if len(list) != 0 {
		t.Fatalf("%+v", list)
	}
}

func TestExpiredPreviewRefusesTheTunnel(t *testing.T) {
	e := newEnv(t, 20)
	started := e.start(alice, api.StartPreview{Port: 3000})
	e.expire(started.ID)
	e.connect(started, alice.CredentialID, nil).expectClose(tunnel.CloseExpired)
}

func TestRevokingTheTokenEndsThePreview(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Port: devPort(t, dev)})
	c := e.serve(started, alice.CredentialID, dev)

	if _, err := e.db.Exec(`DELETE FROM credentials WHERE id = ?`, alice.CredentialID); err != nil {
		t.Fatal(err)
	}
	if err := e.s.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.expectClose(tunnel.CloseUnauthorized)
	list, _ := e.s.List(context.Background(), alice)
	if len(list) != 0 {
		t.Fatalf("%+v", list)
	}
}

func TestRevokingTheTunnelsTokenEndsTheTunnel(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Port: devPort(t, dev)})
	// Another of alice's tokens will do for the tunnel.
	c := e.serve(started, alice2.CredentialID, dev)
	echo(t, e, previewURL(started, "/echo"))

	if _, err := e.db.Exec(`DELETE FROM credentials WHERE id = ?`, alice2.CredentialID); err != nil {
		t.Fatal(err)
	}
	if err := e.s.sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.expectClose(tunnel.CloseUnauthorized)
	// The preview itself lasts, for a tunnel with a token that still works.
	list, _ := e.s.List(context.Background(), alice)
	if len(list) != 1 || list[0].Connected {
		t.Fatalf("%+v", list)
	}
	e.serve(started, alice.CredentialID, dev)
	echo(t, e, previewURL(started, "/echo"))
}

func TestTheSweepRunsOnItsOwn(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Port: devPort(t, dev)})
	c := e.serve(started, alice.CredentialID, dev)
	e.db.Exec(`DELETE FROM credentials WHERE id = ?`, alice.CredentialID)
	// Within sweepEvery, and some.
	c.expectClose(tunnel.CloseUnauthorized)
}

func TestReconnectReplacesTheTunnel(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Port: devPort(t, dev)})
	first := e.serve(started, alice.CredentialID, dev)
	old := e.waitConnected(started.ID)

	second := e.connect(started, alice2.CredentialID, dialTo(dev))
	if closed := first.expectClose(tunnel.CloseReplaced); !closed.Final() {
		t.Fatal(closed)
	}
	for e.waitConnected(started.ID) == old {
		time.Sleep(5 * time.Millisecond)
	}
	second.expectOpen()
	echo(t, e, previewURL(started, "/echo"))
}

func TestTunnelRefusals(t *testing.T) {
	e := newEnv(t, 20)
	started := e.start(alice, api.StartPreview{Port: 3000})

	// Someone else's preview, or none at all: final, so the CLI stops.
	e.connect(started, bob.CredentialID, nil).expectClose(tunnel.CloseUnauthorized)
	missing := started
	missing.TunnelURL = strings.Replace(started.TunnelURL, started.ID, "zzzzzzzz", 1)
	e.connect(missing, alice.CredentialID, nil).expectClose(tunnel.CloseUnauthorized)

	// A CLI speaking another protocol.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, started.TunnelURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + alice.CredentialID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.Read(ctx); websocket.CloseStatus(err) != tunnel.CloseProtocol {
		t.Fatalf("got %v", err)
	}
}

func TestTunnelWithPreviewsOff(t *testing.T) {
	s, _ := New(context.Background(), Options{DB: stateDB(t)})
	defer s.Close()
	rec := httptest.NewRecorder()
	s.Tunnel(rec, httptest.NewRequest("GET", "/api/previews/abcd1234/tunnel", nil), "abcd1234")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"code":"previews_unavailable"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestCloseSendsCLIsToReconnect(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	started := e.start(alice, api.StartPreview{Name: "demo", Port: devPort(t, dev)})
	c := e.serve(started, alice.CredentialID, dev)
	e.s.Close()
	if closed := c.expectClose(websocket.StatusServiceRestart); closed.Final() || closed.Reason != "sloppa is restarting" {
		t.Fatalf("%+v", closed)
	}

	// The next process has the preview, and takes the CLI back.
	s, err := New(context.Background(), Options{DB: e.db, Config: previewConfig(20), PublicURL: e.app.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	e.s = s
	e.serve(started, alice.CredentialID, dev)
	echo(t, e, "http://demo.preview.test/echo")
}

func TestNames(t *testing.T) {
	e := newEnv(t, 20)
	dev := devServer(t, nil)
	held := e.start(alice, api.StartPreview{Name: "demo", Port: devPort(t, dev)})
	c := e.serve(held, alice.CredentialID, dev)

	// Someone else waits for the name.
	_, err := e.s.Start(context.Background(), bob, api.StartPreview{Name: "demo", Port: 3000})
	if e := apiError(t, err, api.KindPreviewNameTaken); e.Status != 409 {
		t.Fatal(e)
	}
	c.expectOpen()

	// Its holder takes it over: the next run of the same command, say.
	again := e.start(alice2, api.StartPreview{Name: "demo", Port: devPort(t, dev)})
	if again.ID == held.ID || again.URL != held.URL {
		t.Fatalf("%+v", again)
	}
	c.expectClose(tunnel.CloseReplaced)
	list, _ := e.s.List(context.Background(), alice)
	if len(list) != 1 || list[0].ID != again.ID {
		t.Fatalf("%+v", list)
	}
	// The old preview is gone for good.
	e.connect(held, alice.CredentialID, nil).expectClose(tunnel.CloseUnauthorized)

	// A name that looks like an id is just a name.
	e.start(bob, api.StartPreview{Name: "abcd1234", Port: 3000})
	_, err = e.s.Start(context.Background(), alice, api.StartPreview{Name: "abcd1234", Port: 3000})
	apiError(t, err, api.KindPreviewNameTaken)
}

// The name passes to someone else once its preview has been without a tunnel for reconnectGrace.
func TestNamesFreeUpAfterTheGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, err := New(context.Background(), Options{DB: stateDB(t), Config: previewConfig(20), PublicURL: "https://sloppa.test"})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		ctx := context.Background()
		held, err := s.Start(ctx, alice, api.StartPreview{Name: "demo", Port: 3000})
		if err != nil {
			t.Fatal(err)
		}
		if held.TunnelURL != "wss://sloppa.test/api/previews/"+held.ID+"/tunnel" {
			t.Fatal(held.TunnelURL)
		}

		time.Sleep(reconnectGrace - time.Second)
		_, err = s.Start(ctx, bob, api.StartPreview{Name: "demo", Port: 3000})
		apiError(t, err, api.KindPreviewNameTaken)

		time.Sleep(time.Second)
		taken, err := s.Start(ctx, bob, api.StartPreview{Name: "demo", Port: 3000})
		if err != nil {
			t.Fatal(err)
		}
		if list, _ := s.List(ctx, alice); len(list) != 0 {
			t.Fatalf("alice's preview lives on: %+v", list)
		}
		if list, _ := s.List(ctx, bob); len(list) != 1 || list[0].ID != taken.ID {
			t.Fatalf("%+v", list)
		}
	})
}

// Expired previews go from the database on the sweep's schedule, with nobody asking.
func TestTheSweepDeletesExpiredRows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := stateDB(t)
		s, err := New(context.Background(), Options{DB: d, Config: previewConfig(20), PublicURL: "http://sloppa.test"})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		started, err := s.Start(context.Background(), alice, api.StartPreview{Port: 3000, TTL: 60})
		if err != nil {
			t.Fatal(err)
		}
		count := func() (n int) {
			d.QueryRow(`SELECT count(*) FROM previews`).Scan(&n)
			return n
		}
		time.Sleep(time.Minute - time.Millisecond)
		synctest.Wait()
		if count() != 1 {
			t.Fatal("expired early")
		}
		if p, _ := s.lookup(started.ID); p == nil {
			t.Fatal("not routed before its expiry")
		}
		time.Sleep(time.Millisecond)
		if p, _ := s.lookup(started.ID); p != nil {
			t.Fatal("routed past its expiry")
		}
		time.Sleep(sweepEvery)
		synctest.Wait()
		if count() != 0 {
			t.Fatal("the sweep left the row")
		}
	})
}
