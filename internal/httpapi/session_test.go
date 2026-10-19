package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/config"
)

func TestSessionUnauthenticated(t *testing.T) {
	h := newHarness(t)
	like(t, h.do("GET", "/api/session", nil), "01-session-unauth", true)
	like(t, h.send(h.req("GET", "/api/session", nil).cookie("not-a-session")), "01-session-unauth", true)
	like(t, h.send(h.req("GET", "/api/session", nil).bearer("sloppa_nope")), "01-session-unauth", true)
}

func TestLoginBad(t *testing.T) {
	h := newHarness(t)
	res := h.do("POST", "/api/session", map[string]string{"username": "admin", "password": "wrong"})
	like(t, res, "02-login-bad", true)
	if cookies := res.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("a failed login set %v", cookies)
	}
}

func TestLoginSetsTheSessionCookie(t *testing.T) {
	h := newHarness(t)
	res := h.do("POST", "/api/session", map[string]string{"username": "admin", "password": testPassword})
	like(t, res, "03-login-ok", true)

	want, err := http.ParseSetCookie(loadFixture(t, "03-login-ok").header.Get("Set-Cookie"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := http.ParseSetCookie(res.Header().Get("Set-Cookie"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != want.Name || got.Path != want.Path || got.HttpOnly != want.HttpOnly || got.SameSite != want.SameSite ||
		got.Secure || got.MaxAge != 0 || len(got.Value) != len(want.Value) {
		t.Errorf("cookie %q, want like %q", res.Header().Get("Set-Cookie"), loadFixture(t, "03-login-ok").header.Get("Set-Cookie"))
	}
	if d := time.Until(got.Expires); d < accounts.SessionTTL-time.Minute || d > accounts.SessionTTL {
		t.Errorf("cookie expires in %v", d)
	}
	like(t, h.send(h.req("GET", "/api/session", nil).cookie(got.Value)), "04-session", true)
}

func TestLoginCookieIsSecureOverHTTPS(t *testing.T) {
	h := newHarness(t, withConfig(func(c *config.Config) { c.PublicURL = "https://sloppa.test" }))
	res := h.do("POST", "/api/session", map[string]string{"username": "admin", "password": testPassword})
	if c := res.Header().Get("Set-Cookie"); !strings.Contains(c, "; Secure") {
		t.Errorf("cookie %q isn't Secure", c)
	}
	res = h.send(h.req("DELETE", "/api/session", nil).cookie(h.login()))
	if c := res.Header().Get("Set-Cookie"); !strings.Contains(c, "; Secure") {
		t.Errorf("cleared cookie %q isn't Secure", c)
	}
}

// Over HTTPS the cookie is __Host-: a page on a sibling subdomain can't set one, so it can't shadow
// the app's session with its own by setting `sloppa_session` for the parent domain.
func TestSessionCookieIsHostOnlyOverHTTPS(t *testing.T) {
	h := newHarness(t, withConfig(func(c *config.Config) { c.PublicURL = "https://sloppa.test" }))
	res := h.do("POST", "/api/session", map[string]string{"username": "admin", "password": testPassword})
	got, err := http.ParseSetCookie(res.Header().Get("Set-Cookie"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "__Host-sloppa_session" || !got.Secure || got.Path != "/" || got.Domain != "" {
		t.Fatalf("cookie %q", res.Header().Get("Set-Cookie"))
	}
	if res := h.send(h.req("GET", "/api/session", nil).cookie(got.Value)); res.Code != http.StatusOK {
		t.Errorf("the __Host- cookie: %d", res.Code)
	}
	r := h.req("GET", "/api/session", nil)
	r.AddCookie(&http.Cookie{Name: "sloppa_session", Value: got.Value})
	if res := h.send(r); res.Code != http.StatusUnauthorized {
		t.Errorf("the plain cookie was read over HTTPS: %d", res.Code)
	}
	var doc openAPIDoc
	json.Unmarshal(h.do("GET", "/api/openapi.json", nil).Body.Bytes(), &doc)
	if name := doc.Components.SecuritySchemes["session"]["name"]; name != "__Host-sloppa_session" {
		t.Errorf("OpenAPI documents the cookie as %q", name)
	}
}

// Over plain HTTP a sibling subdomain can set its own sloppa_session for the parent domain, which the
// browser may send first: with two, there's no telling which is the app's, so neither counts.
func TestSeveralSessionCookiesAreNone(t *testing.T) {
	h := newHarness(t)
	mine, theirs := h.login(), h.login()
	r := h.req("GET", "/api/session", nil).cookie(theirs).cookie(mine)
	if res := h.send(r); res.Code != http.StatusUnauthorized {
		t.Errorf("two sessions: %d", res.Code)
	}
	r = h.req("GET", "/api/session", nil).cookie(mine).cookie(mine)
	if res := h.send(r); res.Code != http.StatusOK {
		t.Errorf("the same session twice: %d", res.Code)
	}
}

func TestLoginValidatesItsBody(t *testing.T) {
	h := newHarness(t)
	for _, body := range []any{
		map[string]string{"username": strings.Repeat("a", 201), "password": "x"},
		map[string]string{"username": "admin", "password": strings.Repeat("a", 1025)},
		map[string]string{"username": "admin"},
		"not json",
	} {
		res := h.send(h.req("POST", "/api/session", body).header("Content-Type", "application/json"))
		if code, _ := errorCode(t, res); res.Code != http.StatusBadRequest || code != "bad_request" {
			t.Errorf("%v: %d %s", body, res.Code, res.Body)
		}
	}
}

func TestLoginIsThrottled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t)
		for range 5 {
			h.do("POST", "/api/session", map[string]string{"username": "admin", "password": "wrong"})
		}
		res := h.do("POST", "/api/session", map[string]string{"username": "admin", "password": testPassword})
		like(t, res, "32-login-throttled", true)
	})
}

func TestLogout(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	res := h.send(h.req("DELETE", "/api/session", nil).cookie(session))
	like(t, res, "30-logout", true)
	got, err := http.ParseSetCookie(res.Header().Get("Set-Cookie"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "sloppa_session" || got.Value != "" || got.MaxAge != -1 || got.Path != "/" || !got.HttpOnly || got.SameSite != http.SameSiteLaxMode {
		t.Errorf("cleared cookie %q, want like %q", res.Header().Get("Set-Cookie"), loadFixture(t, "30-logout").header.Get("Set-Cookie"))
	}
	if res := h.send(h.req("GET", "/api/session", nil).cookie(session)); res.Code != http.StatusUnauthorized {
		t.Errorf("still signed in: %d", res.Code)
	}
}

func TestMachinesCanLogOutWithoutEndingSessions(t *testing.T) {
	h := newHarness(t)
	token := h.machineToken()
	session := h.login()
	if res := h.send(h.req("DELETE", "/api/session", nil).bearer(token)); res.Code != http.StatusNoContent {
		t.Fatalf("logout as a machine: %d %s", res.Code, res.Body)
	}
	if res := h.send(h.req("GET", "/api/session", nil).cookie(session)); res.Code != http.StatusOK {
		t.Errorf("a machine's logout ended a session: %d", res.Code)
	}
}

func TestChangePassword(t *testing.T) {
	h := newHarness(t)
	here, there := h.login(), h.login()

	res := h.send(h.req("PUT", "/api/session/password", map[string]string{"current": testPassword, "next": "short"}).cookie(here))
	like(t, res, "28-change-password-short", false)
	if code, _ := errorCode(t, res); code != "bad_request" {
		t.Errorf("code %q", code)
	}

	res = h.send(h.req("PUT", "/api/session/password", map[string]string{"current": "wrong-password", "next": "second-pass"}).cookie(here))
	like(t, res, "29-change-password-wrong", true)

	res = h.send(h.req("PUT", "/api/session/password", map[string]string{"current": testPassword, "next": "second-pass"}).bearer(h.machineToken()))
	if code, msg := errorCode(t, res); res.Code != http.StatusUnauthorized || msg != "Sign in from a browser for this" {
		t.Errorf("as a machine: %d %s %s", res.Code, code, msg)
	}

	res = h.send(h.req("PUT", "/api/session/password", map[string]string{"current": testPassword, "next": "second-pass"}).cookie(here))
	if res.Code != http.StatusNoContent {
		t.Fatalf("change: %d %s", res.Code, res.Body)
	}
	if res := h.send(h.req("GET", "/api/session", nil).cookie(here)); res.Code != http.StatusOK {
		t.Error("the browser that changed the password was signed out")
	}
	if res := h.send(h.req("GET", "/api/session", nil).cookie(there)); res.Code != http.StatusUnauthorized {
		t.Error("another browser stayed signed in")
	}
}

func TestClientAddress(t *testing.T) {
	for _, tc := range []struct {
		trust             bool
		remote, forwarded string
		want              string
	}{
		{false, "192.0.2.1:1234", "203.0.113.9", "192.0.2.1"},
		{true, "192.0.2.1:1234", "198.51.100.1, 203.0.113.9", "203.0.113.9"},
		{true, "192.0.2.1:1234", " 203.0.113.9 ", "203.0.113.9"},
		{true, "192.0.2.1:1234", "", "192.0.2.1"},
		{true, "[2001:db8::1]:443", "", "2001:db8::1"},
	} {
		s := &server{cfg: &config.Config{TrustProxy: tc.trust}}
		if got := s.client(tc.remote, tc.forwarded); got != tc.want {
			t.Errorf("client(%q, %q) trusting %v = %q, want %q", tc.remote, tc.forwarded, tc.trust, got, tc.want)
		}
	}
}

func TestThrottleTakesTheForwardedClientBehindAProxy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, withConfig(func(c *config.Config) { c.TrustProxy = true }))
		login := func(user, forwarded string) int {
			r := h.req("POST", "/api/session", map[string]string{"username": user, "password": "wrong"}).header("X-Forwarded-For", forwarded)
			return h.send(r).Code
		}
		for range 5 {
			login("someone", "10.0.0.1, 203.0.113.9")
		}
		if code := login("someone-else", "203.0.113.9"); code != http.StatusTooManyRequests {
			t.Errorf("same client: %d", code)
		}
		if code := login("someone-else", "203.0.113.10"); code != http.StatusUnauthorized {
			t.Errorf("another client: %d", code)
		}
	})
}

func TestCrossOriginProtection(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	token := h.machineToken()
	body := map[string]string{"name": "x"}

	res := h.send(h.req("POST", "/api/machines", body).cookie(session).header("Sec-Fetch-Site", "cross-site"))
	if code, _ := errorCode(t, res); res.Code != http.StatusForbidden || code != "forbidden" {
		t.Errorf("cross-site with the cookie: %d %s", res.Code, res.Body)
	}
	// A page on a preview's subdomain is same-site, and still not the app.
	res = h.send(h.req("POST", "/api/machines", body).cookie(session).header("Sec-Fetch-Site", "same-site"))
	if res.Code != http.StatusForbidden {
		t.Errorf("same-site with the cookie: %d", res.Code)
	}
	res = h.send(h.req("POST", "/api/machines", body).cookie(session).header("Origin", "http://evil.test"))
	if res.Code != http.StatusForbidden {
		t.Errorf("another origin with the cookie: %d", res.Code)
	}
	res = h.send(h.req("POST", "/api/machines", body).cookie(session).header("Sec-Fetch-Site", "same-origin"))
	if res.Code != http.StatusOK {
		t.Errorf("same-origin: %d %s", res.Code, res.Body)
	}
	res = h.send(h.req("POST", "/api/machines", body).cookie(session).header("Origin", "http://sloppa.test"))
	if res.Code != http.StatusOK {
		t.Errorf("the app's origin: %d %s", res.Code, res.Body)
	}
	res = h.send(h.req("GET", "/api/machines", nil).cookie(session).header("Sec-Fetch-Site", "cross-site"))
	if res.Code != http.StatusOK {
		t.Errorf("a cross-site GET: %d", res.Code)
	}
	// Bearer tokens aren't sent by browsers on their own: whoever holds one may use it anywhere.
	res = h.send(h.req("POST", "/api/previews", map[string]any{"port": 3000}).bearer(token).header("Sec-Fetch-Site", "cross-site"))
	if res.Code != http.StatusOK {
		t.Errorf("cross-site with a bearer token: %d %s", res.Code, res.Body)
	}
	// Nor can another site sign a browser in as someone else.
	res = h.send(h.req("POST", "/api/session", map[string]string{"username": "admin", "password": testPassword}).header("Sec-Fetch-Site", "cross-site"))
	if res.Code != http.StatusForbidden || res.Header().Get("Set-Cookie") != "" {
		t.Errorf("cross-site login: %d %s", res.Code, res.Header().Get("Set-Cookie"))
	}
}

func TestCrossOriginProtectionTrustsThePublicURL(t *testing.T) {
	// Behind a proxy that rewrites Host, only the Origin header tells.
	h := newHarness(t, withConfig(func(c *config.Config) { c.PublicURL = "https://sloppa.example.com" }))
	r := h.req("POST", "/api/session", map[string]string{"username": "admin", "password": testPassword}).header("Origin", "https://sloppa.example.com")
	r.Host = "127.0.0.1:4100"
	if res := h.send(r); res.Code != http.StatusOK {
		t.Errorf("the public origin: %d %s", res.Code, res.Body)
	}
}
