package httpapi

import (
	"context"
	"encoding/json"
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

func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	h := newHarness(t)
	res := h.do("GET", "/api/openapi.json", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	var got, want openAPIDoc
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/reference/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(got.OpenAPI, "3.1") {
		t.Errorf("openapi %q", got.OpenAPI)
	}
	for name, scheme := range want.Components.SecuritySchemes {
		for key, value := range scheme {
			if got.Components.SecuritySchemes[name][key] != value {
				t.Errorf("security scheme %s: %v, want %v", name, got.Components.SecuritySchemes[name], scheme)
			}
		}
	}

	// The tunnel is new: the TypeScript server took it on the preview domain, with a ticket.
	want.Paths["/api/previews/{id}/tunnel"] = map[string]openAPIOperation{
		"get": {OperationID: "previews.tunnel", Security: []map[string][]string{{"session": {}}, {"bearer": {}}}, Responses: map[string]json.RawMessage{"401": nil}},
	}
	for path, ops := range want.Paths {
		for method, w := range ops {
			g, ok := got.Paths[path][method]
			if !ok {
				t.Errorf("%s %s isn't documented", strings.ToUpper(method), path)
				continue
			}
			if g.OperationID != w.OperationID {
				t.Errorf("%s %s: operationId %q, want %q", method, path, g.OperationID, w.OperationID)
			}
			if !sameSecurity(g.Security, w.Security) {
				t.Errorf("%s %s: security %v, want %v", method, path, g.Security, w.Security)
			}
			for _, p := range w.Parameters {
				if !slices.ContainsFunc(g.Parameters, func(q struct{ Name, In string }) bool { return q == p }) {
					t.Errorf("%s %s: no %s parameter %s", method, path, p.In, p.Name)
				}
			}
			for status := range w.Responses {
				if _, ok := g.Responses[status]; !ok {
					t.Errorf("%s %s: no %s response", method, path, status)
				}
			}
			if _, ok := g.Responses["422"]; ok {
				t.Errorf("%s %s: documents a 422, sent as a 400", method, path)
			}
			if (w.RequestBody == nil) != (g.RequestBody == nil) {
				t.Errorf("%s %s: request body %v, want %v", method, path, g.RequestBody != nil, w.RequestBody != nil)
			}
			if w.RequestBody != nil {
				for contentType := range w.RequestBody.Content {
					if _, ok := g.RequestBody.Content[contentType]; !ok {
						t.Errorf("%s %s: no %s request body", method, path, contentType)
					}
				}
			}
		}
	}
	for path, ops := range got.Paths {
		for method := range ops {
			if _, ok := want.Paths[path][method]; !ok {
				t.Errorf("%s %s is documented but wasn't an API route", strings.ToUpper(method), path)
			}
		}
	}
}

// sameSecurity ignores the TypeScript document listing an empty requirement for public routes.
func sameSecurity(got, want []map[string][]string) bool {
	if len(want) == 0 {
		return len(got) == 0
	}
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		for name := range want[i] {
			if _, ok := got[i][name]; !ok {
				return false
			}
		}
	}
	return true
}

func TestErrorResponsesAreDocumentedAsTagged(t *testing.T) {
	h := newHarness(t)
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any
				Required   []string
			}
		}
	}
	json.Unmarshal(h.do("GET", "/api/openapi.json", nil).Body.Bytes(), &doc)
	e, ok := doc.Components.Schemas["Error"]
	if !ok {
		t.Fatalf("no Error schema in %v", doc.Components.Schemas)
	}
	if _, ok := e.Properties["_tag"]; !ok || !slices.Equal(e.Required, []string{"_tag"}) {
		t.Errorf("Error schema %+v", e)
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
		{"/api/nope", `{"_tag":"NotFound","message":"No such API route"}`, "", 404},
		{"/api", `{"_tag":"NotFound","message":"No such API route"}`, "", 404},
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
	if tag, _ := errorTag(t, rec); rec.Code != http.StatusInternalServerError || tag != "InternalServerError" {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

type panicky struct{ *fakeArtifacts }

func (panicky) Get(context.Context, string) (api.Artifact, error) { panic("oops") }
