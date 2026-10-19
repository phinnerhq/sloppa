package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/phinnerhq/sloppa/internal/api"
)

func TestAgents(t *testing.T) {
	h := newHarness(t)
	session := h.login()

	res := h.send(h.req("GET", "/api/agents", nil).cookie(session))
	if res.Code != http.StatusOK || strings.TrimSpace(res.Body.String()) != "[]" {
		t.Fatalf("no agents: %d %s", res.Code, res.Body)
	}

	res = h.send(h.req("POST", "/api/agents", map[string]string{"name": "  test agent "}).cookie(session))
	like(t, res, "05-agent-create", false)
	created := decode[api.CreatedAgent](t, res)
	if created.Agent.Name != "test agent" || !strings.HasPrefix(created.Token, "sloppa_") {
		t.Errorf("created %+v", created)
	}

	like(t, h.send(h.req("GET", "/api/agents", nil).cookie(session)), "06-agents", false)

	// The token works, and is no way to manage agents.
	like(t, h.send(h.req("GET", "/api/agents", nil).bearer(created.Token)), "21-agents-with-bearer", true)
	like(t, h.send(h.req("GET", "/api/session", nil).bearer(created.Token)), "04-session", true)
	listed := decode[[]api.Agent](t, h.send(h.req("GET", "/api/agents", nil).cookie(session)))
	if len(listed) != 1 || listed[0].LastUsedAt == nil {
		t.Fatalf("after its use: %+v", listed)
	}

	res = h.send(h.req("PATCH", "/api/agents/"+created.Agent.ID, map[string]string{"name": "renamed"}).cookie(session))
	if res.Code != http.StatusNoContent || res.Body.Len() != 0 {
		t.Fatalf("rename: %d %s", res.Code, res.Body)
	}
	if listed := decode[[]api.Agent](t, h.send(h.req("GET", "/api/agents", nil).cookie(session))); listed[0].Name != "renamed" {
		t.Errorf("renamed to %q", listed[0].Name)
	}
	res = h.send(h.req("DELETE", "/api/agents/"+created.Agent.ID, nil).cookie(session))
	if res.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", res.Code, res.Body)
	}
	if res := h.send(h.req("GET", "/api/session", nil).bearer(created.Token)); res.Code != http.StatusUnauthorized {
		t.Errorf("a revoked token still works: %d", res.Code)
	}
}

func TestAgentNamesAreValidated(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	for _, name := range []string{"", "   ", strings.Repeat("x", api.AgentNameMax+1)} {
		res := h.send(h.req("POST", "/api/agents", map[string]string{"name": name}).cookie(session))
		if code, _ := errorCode(t, res); res.Code != http.StatusBadRequest || code != "bad_request" {
			t.Errorf("create %q: %d %s", name, res.Code, res.Body)
		}
		res = h.send(h.req("PATCH", "/api/agents/x", map[string]string{"name": name}).cookie(session))
		if res.Code != http.StatusBadRequest {
			t.Errorf("rename to %q: %d %s", name, res.Code, res.Body)
		}
	}
	res := h.send(h.req("POST", "/api/agents", map[string]string{"name": strings.Repeat("é", api.AgentNameMax)}).cookie(session))
	if res.Code != http.StatusOK {
		t.Errorf("%d characters: %d %s", api.AgentNameMax, res.Code, res.Body)
	}
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
			"curl -fsS '" + link.URL + "' --data-urlencode \"agent=<app> on $(hostname)\" | sloppa login 'http://sloppa.test'",
			"sloppa whoami",
			"go install github.com/phinnerhq/sloppa/cmd/sloppa@latest",
			"https://github.com/phinnerhq/sloppa/releases",
			"skills/sloppa/SKILL.md",
			"curl -fsS -X DELETE '" + link.URL + "'",
		} {
			if !strings.Contains(link.Prompt, want) {
				t.Errorf("the prompt lacks %q:\n%s", want, link.Prompt)
			}
		}
		if strings.Contains(strings.ToLower(link.Prompt), "mcp") {
			t.Errorf("the prompt still mentions MCP:\n%s", link.Prompt)
		}
		// The prompt reads as is in the JSON, for anyone checking it with curl.
		if !strings.Contains(res.Body.String(), `agent=<app> on $(hostname)\" | sloppa`) {
			t.Errorf("HTML escaped in %s", res.Body)
		}
		path := strings.TrimPrefix(link.URL, "http://sloppa.test")

		res = h.do("GET", path, nil)
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "works for 30 more minute(s)") ||
			res.Header().Get("Cache-Control") != "no-store" || res.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
			t.Errorf("GET: %d %v %s", res.Code, res.Header(), res.Body)
		}
		time.Sleep(29*time.Minute + 50*time.Second)
		if res := h.do("GET", path, nil); !strings.Contains(res.Body.String(), "works for 1 more minute(s)") {
			t.Errorf("near the end: %s", res.Body)
		}

		redeem := func(form string) string {
			r := h.req("POST", path, form).header("Content-Type", "application/x-www-form-urlencoded")
			res := h.send(r)
			if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("redeem %q: %d %s", form, res.Code, res.Body)
			}
			token, ok := strings.CutSuffix(res.Body.String(), "\n")
			if !ok || !strings.HasPrefix(token, "sloppa_") {
				t.Fatalf("redeemed %q", res.Body)
			}
			return token
		}
		first := redeem("agent=" + url.QueryEscape("  Codex on laptop "))
		again := redeem("agent=" + url.QueryEscape("Codex on laptop"))
		redeem("")
		redeem("agent=" + strings.Repeat("y", 150))
		if res := h.send(h.req("GET", "/api/session", nil).bearer(first)); res.Code != http.StatusUnauthorized {
			t.Error("setting the same app up again kept its old token")
		}
		if res := h.send(h.req("GET", "/api/session", nil).bearer(again)); res.Code != http.StatusOK {
			t.Error("the redeemed token doesn't work")
		}
		names := map[string]bool{}
		for _, a := range decode[[]api.Agent](t, h.send(h.req("GET", "/api/agents", nil).cookie(session))) {
			names[a.Name] = true
		}
		if len(names) != 3 || !names["Codex on laptop"] || !names["an agent"] || !names[strings.Repeat("y", api.AgentNameMax)] {
			t.Errorf("agents %v", names)
		}

		time.Sleep(10 * time.Second)
		for _, method := range []string{"GET", "POST"} {
			res := h.do(method, path, nil)
			if res.Code != http.StatusGone || !strings.Contains(res.Body.String(), "expired or was revoked") {
				t.Errorf("%s once expired: %d %s", method, res.Code, res.Body)
			}
		}
	})
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
	if res := h.do("POST", path, "agent=x"); res.Code != http.StatusGone {
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
	h.do("POST", "/setup/secret-code", "agent=x")
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
