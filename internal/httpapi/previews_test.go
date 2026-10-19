package httpapi

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/phinnerhq/sloppa/internal/api"
)

func TestPreviews(t *testing.T) {
	h := newHarness(t)
	token := h.agentToken()

	like(t, h.send(h.req("GET", "/api/previews", nil).bearer(token)), "23-previews", true)

	start := api.StartPreview{Name: "demo", Password: "pw", TTL: 600, Port: 5173, Command: "pnpm dev", Cwd: "/src", Hostname: "laptop"}
	res := h.send(h.req("POST", "/api/previews", start).bearer(token))
	if res.Code != http.StatusOK {
		t.Fatalf("start: %d %s", res.Code, res.Body)
	}
	// Deliberately unlike 24-preview-start: no ticket, the tunnel connects with the agent token.
	started := decode[map[string]any](t, res)
	for _, key := range []string{"id", "url", "tunnelUrl", "expiresAt"} {
		if _, ok := started[key]; !ok {
			t.Errorf("started preview lacks %s: %s", key, res.Body)
		}
	}
	if got := h.previews.started; len(got) != 1 || got[0] != start {
		t.Errorf("started %+v, want %+v", got, start)
	}

	res = h.send(h.req("POST", "/api/previews", map[string]any{"name": "taken", "port": 3000}).bearer(token))
	if code, _ := errorCode(t, res); res.Code != http.StatusConflict || code != "preview_name_taken" {
		t.Errorf("a taken name: %d %s", res.Code, res.Body)
	}

	if res := h.send(h.req("DELETE", "/api/previews/74m2w312", nil).bearer(token)); res.Code != http.StatusNoContent {
		t.Errorf("stop: %d %s", res.Code, res.Body)
	}
	res = h.send(h.req("DELETE", "/api/previews/zzzzzzzz", nil).bearer(token))
	if code, _ := errorCode(t, res); res.Code != http.StatusNotFound || code != "preview_not_found" {
		t.Errorf("stop an unknown preview: %d %s", res.Code, res.Body)
	}
}

func TestStartPreviewIsValidated(t *testing.T) {
	h := newHarness(t)
	token := h.agentToken()
	for _, body := range []map[string]any{
		{"name": "Bad Name!", "port": 3000},
		{"name": "-demo", "port": 3000},
		{"name": "", "port": 3000},
		{"password": "", "port": 3000},
		{"ttl": 59, "port": 3000},
		{"ttl": 86401, "port": 3000},
		{"port": 0},
		{"port": 65536},
		{},
	} {
		res := h.send(h.req("POST", "/api/previews", body).bearer(token))
		like(t, res, "25-preview-start-badname", false)
		if code, _ := errorCode(t, res); code != "bad_request" {
			t.Errorf("%v: %s", body, res.Body)
		}
	}
	if len(h.previews.started) != 0 {
		t.Errorf("started %+v", h.previews.started)
	}
}

func TestStartPreviewBoundsMatchTheAPI(t *testing.T) {
	typ := reflect.TypeFor[startPreviewBody]()
	ttl, _ := typ.FieldByName("TTL")
	if ttl.Tag.Get("minimum") != strconv.Itoa(int(api.PreviewTTLMin.Seconds())) ||
		ttl.Tag.Get("maximum") != strconv.Itoa(int(api.PreviewTTLMax.Seconds())) {
		t.Errorf("ttl bounds %q", ttl.Tag)
	}
	name, _ := typ.FieldByName("Name")
	for _, sample := range []string{"a", "checkout-demo", "x1", "-a", "a-", "A", "a_b", "abcdefghij0123456789abcdefghij0123456789", "abcdefghij0123456789abcdefghij0123456789x"} {
		if matched := matchesTag(t, name.Tag.Get("pattern"), sample); matched != api.IsPreviewName(sample) {
			t.Errorf("the name pattern says %v for %q, api.IsPreviewName %v", matched, sample, !matched)
		}
	}
	agent, _ := reflect.TypeFor[agentName]().FieldByName("Name")
	if agent.Tag.Get("maxLength") != strconv.Itoa(api.AgentNameMax) {
		t.Errorf("agent name bound %q", agent.Tag)
	}
	move, _ := reflect.TypeFor[moveInput]().FieldByName("Body")
	collection, _ := move.Type.FieldByName("Collection")
	if collection.Tag.Get("maxLength") != strconv.Itoa(api.CollectionMax) {
		t.Errorf("collection bound %q", collection.Tag)
	}
	id, _ := reflect.TypeFor[artifactIDInput]().FieldByName("ID")
	if id.Tag.Get("pattern") != uuidPattern {
		t.Errorf("artifact id pattern %q, want %q", id.Tag.Get("pattern"), uuidPattern)
	}
}

func TestTunnel(t *testing.T) {
	h := newHarness(t)
	like(t, h.do("GET", "/api/previews/74m2w312/tunnel", nil), "01-session-unauth", true)
	if res := h.send(h.req("GET", "/api/previews/74m2w312/tunnel", nil).bearer(h.agentToken())); res.Code != http.StatusSwitchingProtocols {
		t.Fatalf("tunnel: %d %s", res.Code, res.Body)
	}
	if res := h.send(h.req("GET", "/api/previews/74m2w312/tunnel", nil).cookie(h.login())); res.Code != http.StatusSwitchingProtocols {
		t.Fatalf("tunnel: %d %s", res.Code, res.Body)
	}
	want := []string{"74m2w312 as admin with agent", "74m2w312 as admin with session"}
	if !reflect.DeepEqual(h.previews.tunnels, want) {
		t.Errorf("tunnels %q, want %q", h.previews.tunnels, want)
	}
}

func TestPreviewHostsGoToTheProxy(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	for _, r := range []request{
		h.req("GET", "/", nil),
		h.req("GET", "/api/session", nil).cookie(session),
		h.req("POST", "/api/agents", map[string]string{"name": "x"}).cookie(session).header("Sec-Fetch-Site", "cross-site"),
		h.req("GET", "/setup/abc", nil),
	} {
		r.Host = "demo.preview.test"
		if res := h.send(r); res.Code != http.StatusNotFound || res.Body.String() != "<h1>No preview here</h1>" {
			t.Errorf("%s %s: %d %s", r.Method, r.URL.Path, res.Code, res.Body)
		}
	}
	if len(h.previews.proxied) != 4 {
		t.Errorf("proxied %q", h.previews.proxied)
	}
	if res := h.send(h.req("GET", "/api/session", nil).cookie(session)); res.Code != http.StatusOK {
		t.Errorf("the app's host: %d", res.Code)
	}
}

func matchesTag(t *testing.T, pattern, s string) bool {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return re.MatchString(s)
}

// wsPreviews accepts the tunnel as a WebSocket and echoes one message, so a test can tell the
// connection reaches the previews service through every wrapper around it.
type wsPreviews struct{ fakePreviews }

func (p *wsPreviews) Tunnel(w http.ResponseWriter, r *http.Request, id string) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"sloppa-tunnel.v1"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	typ, msg, err := conn.Read(r.Context())
	if err != nil {
		return
	}
	conn.Write(r.Context(), typ, append([]byte(id+": "), msg...))
	conn.Close(websocket.StatusNormalClosure, "")
}

func TestTunnelUpgradesThroughTheMiddleware(t *testing.T) {
	h := newHarness(t)
	token := h.agentToken()
	previews := &wsPreviews{}
	srv := httptest.NewServer(New(Deps{Config: h.cfg, Accounts: h.accounts, Artifacts: h.artifacts, Previews: previews}))
	defer srv.Close()

	ctx := t.Context()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/previews/74m2w312/tunnel", &websocket.DialOptions{
		HTTPHeader:   http.Header{"Authorization": {"Bearer " + token}},
		Subprotocols: []string{"sloppa-tunnel.v1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, msg, err := conn.Read(ctx)
	if err != nil || string(msg) != "74m2w312: hello" {
		t.Fatalf("echo %q, %v", msg, err)
	}
}
