package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/phinnerhq/sloppa/internal/api"
)

func TestMachines(t *testing.T) {
	h := newHarness(t)
	session := h.login()

	res := h.send(h.req("GET", "/api/machines", nil).cookie(session))
	if res.Code != http.StatusOK || strings.TrimSpace(res.Body.String()) != "[]" {
		t.Fatalf("no machines: %d %s", res.Code, res.Body)
	}

	res = h.send(h.req("POST", "/api/machines", map[string]string{"name": "  test machine "}).cookie(session))
	like(t, res, "05-machine-create", false)
	created := decode[api.CreatedMachine](t, res)
	if created.Machine.Name != "test machine" || !strings.HasPrefix(created.Token, "sloppa_") {
		t.Errorf("created %+v", created)
	}

	like(t, h.send(h.req("GET", "/api/machines", nil).cookie(session)), "06-machines", false)

	// The token works, says which machine it is, and is no way to manage machines.
	like(t, h.send(h.req("GET", "/api/machines", nil).bearer(created.Token)), "21-machines-with-bearer", true)
	if user := decode[api.User](t, h.send(h.req("GET", "/api/session", nil).bearer(created.Token))); user != (api.User{Username: "admin", Machine: "test machine"}) {
		t.Errorf("session as the machine: %+v", user)
	}
	like(t, h.send(h.req("GET", "/api/session", nil).cookie(session)), "04-session", true)
	listed := decode[[]api.Machine](t, h.send(h.req("GET", "/api/machines", nil).cookie(session)))
	if len(listed) != 1 || listed[0].LastSeenAt == nil {
		t.Fatalf("after its use: %+v", listed)
	}

	res = h.send(h.req("PATCH", "/api/machines/"+created.Machine.ID, map[string]string{"name": "renamed"}).cookie(session))
	if res.Code != http.StatusNoContent || res.Body.Len() != 0 {
		t.Fatalf("rename: %d %s", res.Code, res.Body)
	}
	if listed := decode[[]api.Machine](t, h.send(h.req("GET", "/api/machines", nil).cookie(session))); listed[0].Name != "renamed" {
		t.Errorf("renamed to %q", listed[0].Name)
	}
	res = h.send(h.req("DELETE", "/api/machines/"+created.Machine.ID, nil).cookie(session))
	if res.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", res.Code, res.Body)
	}
	if res := h.send(h.req("GET", "/api/session", nil).bearer(created.Token)); res.Code != http.StatusUnauthorized {
		t.Errorf("a revoked machine's token still works: %d", res.Code)
	}
}

func TestMachineNamesAreValidated(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	for _, name := range []string{"", "   ", strings.Repeat("x", api.MachineNameMax+1)} {
		res := h.send(h.req("POST", "/api/machines", map[string]string{"name": name}).cookie(session))
		if code, _ := errorCode(t, res); res.Code != http.StatusBadRequest || code != "bad_request" {
			t.Errorf("create %q: %d %s", name, res.Code, res.Body)
		}
		res = h.send(h.req("PATCH", "/api/machines/x", map[string]string{"name": name}).cookie(session))
		if res.Code != http.StatusBadRequest {
			t.Errorf("rename to %q: %d %s", name, res.Code, res.Body)
		}
	}
	res := h.send(h.req("POST", "/api/machines", map[string]string{"name": strings.Repeat("é", api.MachineNameMax)}).cookie(session))
	if res.Code != http.StatusOK {
		t.Errorf("%d characters: %d %s", api.MachineNameMax, res.Code, res.Body)
	}
}

// redeem spends a setup link with body, and the bearer token if any, as `sloppa setup` would.
func (h *harness) redeem(path, body, bearer string) *httptest.ResponseRecorder {
	r := h.req("POST", path, body).header("Content-Type", "application/json")
	if bearer != "" {
		r = r.bearer(bearer)
	}
	return h.send(r)
}

// setupLink is the path of a fresh setup link of admin's.
func (h *harness) setupLink(session string) string {
	link := decode[api.SetupLink](h.t, h.send(h.req("POST", "/api/setup-links", nil).cookie(session)))
	return strings.TrimPrefix(link.URL, "http://sloppa.test")
}

func TestSetupLinkRoundTrip(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		session := h.login()

		res := h.send(h.req("POST", "/api/setup-links", nil).cookie(session))
		like(t, res, "22-setup-create", false)
		link := decode[api.SetupLink](t, res)
		if !strings.HasPrefix(link.URL, "http://sloppa.test/setup/") || !link.ExpiresAt.Equal(api.Time(time.Now().Add(30*time.Minute))) {
			t.Fatalf("link %+v", link)
		}
		for _, want := range []string{
			"sloppa setup '" + link.URL + "'",
			"sloppa whoami",
			"go install github.com/phinnerhq/sloppa/cmd/sloppa@latest",
			"https://github.com/phinnerhq/sloppa/releases",
			"skills/sloppa/SKILL.md",
		} {
			if !strings.Contains(link.Prompt, want) {
				t.Errorf("the prompt lacks %q:\n%s", want, link.Prompt)
			}
		}
		for _, gone := range []string{"mcp", "curl", "hostname", "revoke"} {
			if strings.Contains(strings.ToLower(link.Prompt), gone) {
				t.Errorf("the prompt still mentions %s:\n%s", gone, link.Prompt)
			}
		}
		// The prompt reads as is in the JSON, for anyone checking it with curl.
		if !strings.Contains(res.Body.String(), "sloppa setup '"+link.URL+"'") {
			t.Errorf("escaped in %s", res.Body)
		}
		path := strings.TrimPrefix(link.URL, "http://sloppa.test")

		res = h.do("GET", path, nil)
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "works once, for 30 more minute(s)") ||
			res.Header().Get("Cache-Control") != "no-store" || res.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("GET: %d %v %s", res.Code, res.Header(), res.Body)
		}
		time.Sleep(29*time.Minute + 50*time.Second)
		if res := h.do("GET", path, nil); !strings.Contains(res.Body.String(), "for 1 more minute(s)") {
			t.Errorf("near the end: %s", res.Body)
		}

		res = h.redeem(path, `{"hostname":" laptop ","os":"linux","arch":"amd64","cliVersion":"1.2.3"}`, "")
		if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("redeem: %d %s", res.Code, res.Body)
		}
		got := decode[api.SetupResult](t, res)
		want := api.Machine{ID: got.Machine.ID, Name: "laptop", Hostname: "laptop", OS: "linux", Arch: "amd64", CLIVersion: "1.2.3",
			CreatedAt: api.Time(time.Now()), LastSeenAt: new(api.Time(time.Now()))}
		if !strings.HasPrefix(got.Token, "sloppa_") || got.Repaired || !reflect.DeepEqual(got.Machine, want) {
			t.Fatalf("redeemed %+v", got)
		}
		if res := h.send(h.req("GET", "/api/session", nil).bearer(got.Token)); res.Code != http.StatusOK {
			t.Error("the redeemed token doesn't work")
		}

		// Once only.
		for _, method := range []string{"GET", "POST"} {
			res := h.do(method, path, nil)
			if res.Code != http.StatusGone {
				t.Errorf("%s once used: %d %s", method, res.Code, res.Body)
			}
		}
		if code, _ := errorCode(t, h.redeem(path, "{}", "")); code != "setup_link_gone" {
			t.Errorf("redeemed again: %s", code)
		}

		path = h.setupLink(session)
		time.Sleep(30 * time.Minute)
		if res := h.redeem(path, "{}", ""); res.Code != http.StatusGone {
			t.Errorf("redeemed once expired: %d %s", res.Code, res.Body)
		}
		if res := h.do("GET", path, nil); res.Code != http.StatusGone || !strings.Contains(res.Body.String(), "used already, expired or was revoked") {
			t.Errorf("GET once expired: %d %s", res.Code, res.Body)
		}
	})
}

func TestSetupRepairsTheMachineWhoseTokenComesAlong(t *testing.T) {
	h := newHarness(t)
	session := h.login()

	first := decode[api.SetupResult](t, h.redeem(h.setupLink(session), `{"hostname":"laptop","os":"linux","arch":"amd64"}`, ""))
	h.send(h.req("PATCH", "/api/machines/"+first.Machine.ID, map[string]string{"name": "work laptop"}).cookie(session))

	again := decode[api.SetupResult](t, h.redeem(h.setupLink(session), `{"hostname":"laptop","os":"linux","arch":"arm64","cliVersion":"2.0.0"}`, first.Token))
	if !again.Repaired || again.Machine.ID != first.Machine.ID || again.Machine.Name != "work laptop" ||
		again.Machine.Arch != "arm64" || again.Machine.CLIVersion != "2.0.0" || again.Token == first.Token {
		t.Fatalf("re-paired %+v", again)
	}
	if res := h.send(h.req("GET", "/api/session", nil).bearer(first.Token)); res.Code != http.StatusUnauthorized {
		t.Errorf("the old token still works: %d", res.Code)
	}

	// Without a token, or with one that's no good, it's another machine.
	for _, bearer := range []string{"", first.Token, "sloppa_nonsense"} {
		res := decode[api.SetupResult](t, h.redeem(h.setupLink(session), `{"hostname":"laptop"}`, bearer))
		if res.Repaired || res.Machine.ID == first.Machine.ID {
			t.Errorf("with %q: %+v", bearer, res)
		}
	}
	// So is one without anything known about it.
	if res := decode[api.SetupResult](t, h.redeem(h.setupLink(session), "", "")); res.Machine.Name != "machine" || res.Machine.Hostname != "" {
		t.Errorf("an empty body: %+v", res)
	}
	if machines := decode[[]api.Machine](t, h.send(h.req("GET", "/api/machines", nil).cookie(session))); len(machines) != 5 {
		t.Errorf("%d machines", len(machines))
	}
	if res := h.redeem(h.setupLink(session), "not json", ""); res.Code != http.StatusBadRequest {
		t.Errorf("a body that isn't JSON: %d", res.Code)
	}
}

func TestRevokeSetupLinks(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	link := decode[api.SetupLink](t, h.send(h.req("POST", "/api/setup-links", nil).cookie(session)))
	path := strings.TrimPrefix(link.URL, "http://sloppa.test")

	res := h.do("DELETE", path, nil)
	if res.Code != http.StatusOK || !strings.HasPrefix(res.Body.String(), "Revoked") {
		t.Fatalf("revoke: %d %s", res.Code, res.Body)
	}
	if res := h.do("DELETE", path, nil); res.Code != http.StatusNotFound {
		t.Errorf("revoke again: %d", res.Code)
	}
	if res := h.redeem(path, "{}", ""); res.Code != http.StatusGone {
		t.Errorf("redeem a revoked link: %d", res.Code)
	}

	link = decode[api.SetupLink](t, h.send(h.req("POST", "/api/setup-links", nil).cookie(session)))
	res = h.send(h.req("DELETE", "/api/setup-links/"+link.ID, nil).cookie(session))
	if res.Code != http.StatusNoContent {
		t.Fatalf("revoke by id: %d %s", res.Code, res.Body)
	}
	if res := h.do("GET", strings.TrimPrefix(link.URL, "http://sloppa.test"), nil); res.Code != http.StatusGone {
		t.Errorf("a link revoked by id: %d", res.Code)
	}
}

func TestLogsHideSetupCodes(t *testing.T) {
	var logs bytes.Buffer
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))

	h := newHarness(t)
	h.do("GET", "/setup/secret-code", nil)
	h.do("POST", "/setup/secret-code", "{}")
	h.do("GET", "/api/session", nil)
	if strings.Contains(logs.String(), "secret-code") {
		t.Errorf("logged the code:\n%s", logs.String())
	}
	if n := strings.Count(logs.String(), "path=/setup/<code>"); n != 2 {
		t.Errorf("logged /setup/<code> %d times:\n%s", n, logs.String())
	}
	// Other spellings are redirected to /setup/, or not routed at all, but logged all the same.
	for _, target := range []string{"//setup/secret-code", "/./setup/secret-code", "/setup/../setup/secret-code",
		"/x/../setup/secret-code", "/%73etup/secret-code", "/setup%2Fsecret-code", "/SETUP/secret-code", "/setup/secret-code/more"} {
		h.do("GET", target, nil)
	}
	if strings.Contains(logs.String(), "secret-code") {
		t.Errorf("logged the code:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "method=GET host=sloppa.test path=/api/session status=401") {
		t.Errorf("no request log:\n%s", logs.String())
	}
}

// A handler panicking on a setup link doesn't log its code either.
func TestPanicLogsHideSetupCodes(t *testing.T) {
	var logs bytes.Buffer
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	handler := logRequests(recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("oops") })))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "//setup/secret-code", nil))
	if strings.Contains(logs.String(), "secret-code") || strings.Count(logs.String(), "path=//setup/<code>") != 2 {
		t.Errorf("logs:\n%s", logs.String())
	}
}

func TestLogPath(t *testing.T) {
	for p, want := range map[string]string{
		"/setup/code":           "/setup/<code>",
		"//setup/code":          "//setup/<code>",
		"/./setup/code":         "/./setup/<code>",
		"/setup/../setup/code":  "/setup/<code>",
		"/a/../setup/code/more": "/a/../setup/<code>",
		"/Setup/code":           "/Setup/<code>",
		"/setup":                "/setup",
		"/api/setup-links":      "/api/setup-links",
		"/api/session":          "/api/session",
	} {
		if got := logPath(p); got != want {
			t.Errorf("logPath(%q) = %q, want %q", p, got, want)
		}
	}
}
