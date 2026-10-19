package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/db"
)

const testPassword = "dev-password"

type harness struct {
	t         *testing.T
	handler   http.Handler
	cfg       *config.Config
	accounts  *accounts.Service
	artifacts *fakeArtifacts
	previews  *fakePreviews
}

type option func(*config.Config, *Deps)

func withStatic(files fs.FS) option {
	return func(_ *config.Config, d *Deps) { d.Static = files }
}

func withConfig(f func(*config.Config)) option {
	return func(c *config.Config, _ *Deps) { f(c) }
}

// newHarness is the app on the real accounts service, over a fresh in-memory state database with
// the user admin, and fakes for the rest. Inside a synctest bubble, it must be made there too.
func newHarness(t *testing.T, opts ...option) *harness {
	t.Helper()
	state, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	if err := db.Migrate(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	accts, err := accounts.New(t.Context(), state, "admin", config.Secret(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{PublicURL: "http://sloppa.test"}
	h := &harness{t: t, cfg: cfg, accounts: accts, artifacts: newFakeArtifacts(), previews: &fakePreviews{}}
	d := Deps{Config: cfg, Accounts: accts, Artifacts: h.artifacts, Previews: h.previews}
	for _, opt := range opts {
		opt(cfg, &d)
	}
	h.handler = New(d)
	return h
}

// request is one to send, built up with its methods.
type request struct {
	*http.Request
	cookieName string
}

// cookieName is the session cookie's name for the app's PUBLIC_URL.
func (h *harness) cookieName() string { return sessionCookieName(h.cfg.PublicURL) }

func (h *harness) req(method, target string, body any) request {
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	case io.Reader:
		r = b
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			h.t.Fatal(err)
		}
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, r)
	req.Host = "sloppa.test"
	if _, ok := body.(string); !ok && body != nil {
		if _, ok := body.(io.Reader); !ok {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	return request{req, h.cookieName()}
}

func (r request) cookie(session string) request {
	r.AddCookie(&http.Cookie{Name: r.cookieName, Value: session})
	return r
}

func (r request) bearer(token string) request {
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func (r request) header(name, value string) request {
	r.Header.Set(name, value)
	return r
}

func (h *harness) send(r request) *httptest.ResponseRecorder {
	h.t.Helper()
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, r.Request)
	return rec
}

func (h *harness) do(method, target string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.send(h.req(method, target, body))
}

// login signs admin in and is the session cookie's value.
func (h *harness) login() string {
	h.t.Helper()
	res := h.do("POST", "/api/session", map[string]string{"username": "admin", "password": testPassword})
	if res.Code != http.StatusOK {
		h.t.Fatalf("login: %d %s", res.Code, res.Body)
	}
	for _, c := range res.Result().Cookies() {
		if c.Name == h.cookieName() {
			return c.Value
		}
	}
	h.t.Fatal("login set no session cookie")
	return ""
}

// agentToken is a fresh agent token of admin's.
func (h *harness) agentToken() string {
	h.t.Helper()
	res := h.send(h.req("POST", "/api/agents", map[string]string{"name": "test agent"}).cookie(h.login()))
	if res.Code != http.StatusOK {
		h.t.Fatalf("create agent: %d %s", res.Code, res.Body)
	}
	var created struct{ Token string }
	json.Unmarshal(res.Body.Bytes(), &created)
	return created.Token
}

// fixture is a golden response, from testdata/responses.
type fixture struct {
	status int
	header http.Header
	body   []byte
}

func loadFixture(t *testing.T, name string) fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "responses", name+".http"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return fixture{status: res.StatusCode, header: res.Header, body: bytes.TrimSpace(body)}
}

// Headers that differ for reasons that don't matter: framing, and cookies, compared on their own.
var unchecked = map[string]bool{"Content-Length": true, "Transfer-Encoding": true, "Set-Cookie": true, "Date": true}

// like checks res against the fixture: the same status and headers, and a JSON body of the same
// shape, or the same JSON when exact. A fixture with no body only checks the status.
func like(t *testing.T, res *httptest.ResponseRecorder, name string, exact bool) {
	t.Helper()
	f := loadFixture(t, name)
	if res.Code != f.status {
		t.Fatalf("%s: status %d, want %d; body %s", name, res.Code, f.status, res.Body)
	}
	if len(f.body) == 0 {
		return
	}
	for key, values := range f.header {
		if unchecked[key] {
			continue
		}
		if got := res.Header().Values(key); !reflect.DeepEqual(got, values) {
			t.Errorf("%s: header %s = %q, want %q", name, key, got, values)
		}
	}
	var got, want any
	if err := json.Unmarshal(f.body, &want); err != nil {
		if !bytes.Equal(bytes.TrimSpace(res.Body.Bytes()), f.body) {
			t.Errorf("%s: body %q, want %q", name, res.Body, f.body)
		}
		return
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("%s: body isn't JSON: %v: %s", name, err, res.Body)
	}
	if exact {
		if !reflect.DeepEqual(normalize(got), normalize(want)) {
			t.Errorf("%s: body\n%s\nwant\n%s", name, res.Body, f.body)
		}
		return
	}
	if diff := sameShape("$", got, want); diff != "" {
		t.Errorf("%s: %s\ngot  %s\nwant %s", name, diff, res.Body, f.body)
	}
}

// normalize reads timestamps as instants, since "…28.480Z" and "…28.48Z" are the same.
func normalize(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			out[k] = normalize(x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = normalize(x)
		}
		return out
	case string:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t.UTC()
		}
	}
	return v
}

// sameShape describes where got's keys or types first differ from want's, or is "".
func sameShape(at string, got, want any) string {
	if reflect.TypeOf(got) != reflect.TypeOf(want) {
		return at + ": " + typeName(got) + ", want " + typeName(want)
	}
	switch w := want.(type) {
	case map[string]any:
		g := got.(map[string]any)
		for k := range w {
			if _, ok := g[k]; !ok {
				return at + "." + k + ": missing"
			}
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				return at + "." + k + ": unexpected"
			}
		}
		for k := range w {
			if d := sameShape(at+"."+k, g[k], w[k]); d != "" {
				return d
			}
		}
	case []any:
		g := got.([]any)
		if (len(g) == 0) != (len(w) == 0) {
			return at + ": " + typeName(got) + " of a different length"
		}
		if len(g) > 0 {
			return sameShape(at+"[0]", g[0], w[0])
		}
	}
	return ""
}

func typeName(v any) string {
	if v == nil {
		return "null"
	}
	return reflect.TypeOf(v).String()
}

func decode[T any](t *testing.T, res *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(res.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T: %v: %s", v, err, res.Body)
	}
	return v
}

// fixtureBody decodes a fixture's JSON body as T.
func fixtureBody[T any](t *testing.T, name string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(loadFixture(t, name).body, &v); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return v
}

func errorCode(t *testing.T, res *httptest.ResponseRecorder) (code, message string) {
	t.Helper()
	e := decode[struct{ Code, Message string }](t, res)
	return e.Code, e.Message
}

// TestMain keeps the request log out of test output.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}
