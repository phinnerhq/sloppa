package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/config"
)

type openAPIDoc struct {
	OpenAPI    string `json:"openapi"`
	Paths      map[string]map[string]openAPIOperation
	Components struct {
		SecuritySchemes map[string]map[string]string `json:"securitySchemes"`
	}
}

type openAPIOperation struct {
	OperationID string                `json:"operationId"`
	Security    []map[string][]string `json:"security"`
	Parameters  []struct {
		Name, In string
	}
	Responses   map[string]json.RawMessage
	RequestBody *struct {
		Content map[string]json.RawMessage
	} `json:"requestBody"`
}

func TestErrorResponsesAreDocumentedByCode(t *testing.T) {
	h := newHarness(t)
	var doc struct {
		Paths      map[string]map[string]struct{ Responses map[string]json.RawMessage }
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct{ Const string }
				Required   []string
			}
		}
	}
	json.Unmarshal(h.do("GET", "/api/openapi.json", nil).Body.Bytes(), &doc)
	for _, kind := range api.ErrorKinds {
		s, ok := doc.Components.Schemas[kind.Name()]
		if !ok {
			t.Errorf("no %s schema", kind.Name())
			continue
		}
		if s.Properties["code"].Const != kind.Code || !slices.Contains(s.Required, "code") || !slices.Contains(s.Required, "message") {
			t.Errorf("%s schema %+v", kind.Name(), s)
		}
		if kind.Field != "" && !slices.Contains(s.Required, kind.Field) {
			t.Errorf("%s schema doesn't require %s", kind.Name(), kind.Field)
		}
	}
	login := doc.Paths["/api/session"]["post"].Responses
	if !strings.Contains(string(login["401"]), "#/components/schemas/InvalidCredentials") ||
		!strings.Contains(string(login["429"]), "#/components/schemas/TooManyAttempts") {
		t.Errorf("login's errors: %s", login)
	}
	password := string(doc.Paths["/api/session/password"]["put"].Responses["401"])
	if !strings.Contains(password, `"propertyName":"code"`) || !strings.Contains(password, "InvalidCredentials") ||
		!strings.Contains(password, "Unauthorized") {
		t.Errorf("a status with several errors isn't a union by code: %s", password)
	}
	// The hand-written operations are documented like the rest.
	for path, method := range map[string]string{"/api/artifacts": "post", "/api/previews/{id}/tunnel": "get"} {
		if _, ok := doc.Paths[path][method].Responses["401"]; !ok {
			t.Errorf("%s %s isn't documented", method, path)
		}
	}
}

// openapi.json at the repository's root is what the dashboard's client is generated from.
var updateOpenAPI = flag.Bool("update-openapi", false, "rewrite openapi.json from the API")

func TestOpenAPIDocumentIsCommitted(t *testing.T) {
	h := newHarness(t)
	var doc any
	if err := json.Unmarshal(h.do("GET", "/api/openapi.json", nil).Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const path = "../../openapi.json"
	if *updateOpenAPI {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("openapi.json is out of date: run `pnpm gen`")
	}
}

func TestStaticDashboard(t *testing.T) {
	files := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>sloppa</title>")},
		"assets/index-abc1.js": {Data: []byte("console.log(1)")},
		"favicon.svg":          {Data: []byte("<svg/>")},
	}
	h := newHarness(t, withStatic(files))
	for _, tc := range []struct {
		path, body, cache string
		status            int
	}{
		{"/", "<!doctype html><title>sloppa</title>", "no-cache", 200},
		{"/a/f1f8369a-8ed1-466c-afbd-39d7e7cc7c67", "<!doctype html><title>sloppa</title>", "no-cache", 200},
		{"/credits/", "<!doctype html><title>sloppa</title>", "no-cache", 200},
		{"/assets", "<!doctype html><title>sloppa</title>", "no-cache", 200},
		{"/assets/index-abc1.js", "console.log(1)", "public, max-age=31536000, immutable", 200},
		{"/favicon.svg", "<svg/>", "no-cache", 200},
		{"/assets/index-old.js", "Not found", "", 404},
		{"/api/nope", `{"code":"not_found","message":"No such API route"}`, "", 404},
		{"/api", `{"code":"not_found","message":"No such API route"}`, "", 404},
	} {
		res := h.do("GET", tc.path, nil)
		if res.Code != tc.status || res.Body.String() != tc.body || res.Header().Get("Cache-Control") != tc.cache {
			t.Errorf("%s: %d %q (Cache-Control %q), want %d %q (%q)", tc.path, res.Code, res.Body, res.Header().Get("Cache-Control"), tc.status, tc.body, tc.cache)
		}
	}
	if res := h.do("GET", "/index.html", nil); res.Code != http.StatusMovedPermanently && res.Code != http.StatusOK {
		t.Errorf("/index.html: %d", res.Code)
	}
	if ct := h.do("GET", "/", nil).Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("index.html served as %q", ct)
	}
	// No page may frame the dashboard, whichever path it's served under.
	for _, path := range []string{"/", "/index.html", "/a/f1f8369a-8ed1-466c-afbd-39d7e7cc7c67", "/credits/", "/favicon.svg"} {
		res := h.do("GET", path, nil)
		if csp, xfo := res.Header().Get("Content-Security-Policy"), res.Header().Get("X-Frame-Options"); csp != "frame-ancestors 'none'" || xfo != "DENY" {
			t.Errorf("%s: Content-Security-Policy %q, X-Frame-Options %q", path, csp, xfo)
		}
	}
}

func TestNoDashboard(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/", "/a/x", "/assets/x.js"} {
		if res := h.do("GET", path, nil); res.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, res.Code)
		}
	}
}

func TestPanicsAreA500(t *testing.T) {
	cfg := &config.Config{PublicURL: "http://sloppa.test"}
	handler := New(Deps{Config: cfg, Accounts: nil, Artifacts: panicky{newFakeArtifacts()}, Previews: &fakePreviews{}})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/artifacts/"+helloID, nil))
	if code, _ := errorCode(t, rec); rec.Code != http.StatusInternalServerError || code != "internal_error" {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

type panicky struct{ *fakeArtifacts }

func (panicky) Get(context.Context, string) (api.Artifact, error) { panic("oops") }
