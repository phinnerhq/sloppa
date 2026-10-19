package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/artifacts"
)

const (
	helloID   = "f1f8369a-8ed1-466c-afbd-39d7e7cc7c67"
	missingID = "00000000-0000-4000-8000-000000000000"
)

// withHello is a harness whose storage holds the golden responses' hello.txt.
func withHello(t *testing.T) *harness {
	h := newHarness(t)
	hello := fixtureBody[api.Artifact](t, "14-get")
	h.artifacts.add(hello, "hello world\n")
	return h
}

func TestListings(t *testing.T) {
	h := newHarness(t)
	token := h.machineToken()
	get := func(path string) request { return h.req("GET", path, nil).bearer(token) }

	h.artifacts.page = fixtureBody[api.ArtifactPage](t, "08-list")
	like(t, h.send(get("/api/artifacts?limit=1")), "08-list", true)
	if q := h.artifacts.lastList; q != (artifacts.ListQuery{Limit: 1}) {
		t.Errorf("list query %+v", q)
	}

	h.artifacts.page = fixtureBody[api.ArtifactPage](t, "09-list-cursor")
	like(t, h.send(get("/api/artifacts?cursor=abc&collection=owner%2Frepo+%2342")), "09-list-cursor", true)
	if q := h.artifacts.lastList; q != (artifacts.ListQuery{Cursor: "abc", Collection: "owner/repo #42", Limit: api.PageSize}) {
		t.Errorf("list query %+v", q)
	}

	h.artifacts.page = fixtureBody[api.ArtifactPage](t, "10-list-q")
	like(t, h.send(get("/api/artifacts?q=notes")), "10-list-q", true)
	if q := h.artifacts.lastList; q.Q != "notes" {
		t.Errorf("list query %+v", q)
	}

	h.artifacts.groups = fixtureBody[api.GroupPage](t, "11-mosaic")
	like(t, h.send(get("/api/collections/mosaic")), "11-mosaic", true)
	if m := h.artifacts.lastMosaic; m.limit != api.GroupPageSize || m.cursor != "" {
		t.Errorf("mosaic query %+v", m)
	}

	h.artifacts.collections = fixtureBody[[]api.CollectionSummary](t, "12-collections")
	like(t, h.send(get("/api/collections")), "12-collections", true)

	h.artifacts.stats = fixtureBody[api.Stats](t, "13-stats")
	like(t, h.send(get("/api/stats")), "13-stats", true)
}

func TestEmptyListingsAreArrays(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	for path, want := range map[string]string{
		"/api/artifacts":          `{"items":[]}`,
		"/api/collections/mosaic": `{"items":[]}`,
		"/api/collections":        `[]`,
		"/api/stats":              `{"total":0,"totalBytes":0,"last24h":0}`,
		"/api/previews":           `[]`,
	} {
		res := h.send(h.req("GET", path, nil).cookie(session))
		if res.Code != http.StatusOK || res.Body.String() != want {
			t.Errorf("%s: %d %s, want %s", path, res.Code, res.Body, want)
		}
	}
}

func TestListingsNeedSignIn(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/api/artifacts", "/api/collections/mosaic", "/api/collections", "/api/stats", "/api/previews"} {
		like(t, h.do("GET", path, nil), "01-session-unauth", true)
	}
}

func TestListingBounds(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	for _, path := range []string{
		"/api/artifacts?limit=0", "/api/artifacts?limit=201", "/api/artifacts?limit=x",
		"/api/collections/mosaic?limit=0", "/api/collections/mosaic?limit=101",
	} {
		res := h.send(h.req("GET", path, nil).cookie(session))
		if code, msg := errorCode(t, res); res.Code != http.StatusBadRequest || code != "bad_request" || !strings.Contains(msg, "limit") {
			t.Errorf("%s: %d %s", path, res.Code, res.Body)
		}
	}
	for _, path := range []string{"/api/artifacts?limit=200", "/api/collections/mosaic?limit=100"} {
		if res := h.send(h.req("GET", path, nil).cookie(session)); res.Code != http.StatusOK {
			t.Errorf("%s: %d %s", path, res.Code, res.Body)
		}
	}
}

func TestGetArtifact(t *testing.T) {
	h := withHello(t)
	// No sign-in: the id is the secret.
	like(t, h.do("GET", "/api/artifacts/"+helloID, nil), "14-get", true)
	like(t, h.do("GET", "/api/artifacts/"+missingID, nil), "15-get-missing", true)

	res := h.do("GET", "/api/artifacts/not-a-uuid", nil)
	like(t, res, "16-get-badid", false)
	if code, _ := errorCode(t, res); code != "bad_request" {
		t.Errorf("code %q", code)
	}
}

func TestMoveArtifact(t *testing.T) {
	h := withHello(t)
	token := h.machineToken()
	move := func(id string, body any) request {
		return h.req("PATCH", "/api/artifacts/"+id, body).bearer(token)
	}

	// Surrounding space is trimmed rather than refused.
	res := h.send(move(helloID, map[string]string{"collection": "  owner/repo #43 "}))
	if res.Code != http.StatusOK || decode[api.Artifact](t, res).Collection != "owner/repo #43" {
		t.Fatalf("move: %d %s", res.Code, res.Body)
	}
	like(t, res, "14-get", false)

	for _, body := range []any{
		map[string]string{"collection": ""},
		map[string]string{"collection": "   "},
		map[string]string{"collection": strings.Repeat("x", api.CollectionMax+1)},
		map[string]string{},
	} {
		res := h.send(move(helloID, body))
		like(t, res, "18-move-empty", false)
		if code, _ := errorCode(t, res); code != "bad_request" {
			t.Errorf("%v: %s", body, res.Body)
		}
	}
	like(t, h.send(move(missingID, map[string]string{"collection": "x"})), "15-get-missing", true)
	if res := h.do("PATCH", "/api/artifacts/"+helloID, map[string]string{"collection": "x"}); res.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated move: %d", res.Code)
	}
	if moves := h.artifacts.moves; len(moves) != 1 {
		t.Errorf("moves %q", moves)
	}
}

func TestDeleteArtifact(t *testing.T) {
	h := newHarness(t)
	token := h.machineToken()
	h.artifacts.add(api.Artifact{ID: helloID, Name: "hello.txt"}, "hello")
	res := h.send(h.req("DELETE", "/api/artifacts/"+helloID, nil).bearer(token))
	like(t, res, "26-delete", false)
	if res.Body.Len() != 0 {
		t.Errorf("body %q", res.Body)
	}
	like(t, h.send(h.req("DELETE", "/api/artifacts/"+helloID, nil).bearer(token)), "27-delete-again", true)
}

func TestFailuresHaveCodes(t *testing.T) {
	h := newHarness(t)
	session := h.login()
	h.artifacts.err = api.StorageError("index unreachable")
	res := h.send(h.req("GET", "/api/stats", nil).cookie(session))
	if code, msg := errorCode(t, res); res.Code != http.StatusBadGateway || code != "storage_error" || msg != "index unreachable" {
		t.Errorf("storage error: %d %s", res.Code, res.Body)
	}
	h.artifacts.err = fmt.Errorf("wrapped: %w", errBoom)
	res = h.send(h.req("GET", "/api/stats", nil).cookie(session))
	if code, msg := errorCode(t, res); res.Code != http.StatusInternalServerError || code != "internal_error" || strings.Contains(msg, "boom") {
		t.Errorf("unexpected error: %d %s", res.Code, res.Body)
	}
}

func TestRawFile(t *testing.T) {
	h := withHello(t)
	res := h.do("GET", "/f/"+helloID+"/hello.txt", nil)
	like(t, res, "19-raw", false)
	if res.Body.String() != "hello world\n" {
		t.Errorf("body %q", res.Body)
	}
	if res.Header().Get("Content-Length") != "12" {
		t.Errorf("Content-Length %q", res.Header().Get("Content-Length"))
	}
	if res := h.do("GET", "/f/"+helloID, nil); res.Code != http.StatusOK || res.Body.String() != "hello world\n" {
		t.Errorf("without a name: %d %q", res.Code, res.Body)
	}

	r := h.req("GET", "/f/"+helloID+"/hello.txt?download", nil).header("Range", "bytes=0-4")
	res = h.send(r)
	like(t, res, "20-raw-range", false)
	if res.Body.String() != "hello" || res.Header().Get("Content-Length") != "5" {
		t.Errorf("range: %q, length %q", res.Body, res.Header().Get("Content-Length"))
	}

	if res := h.do("HEAD", "/f/"+helloID+"/hello.txt", nil); res.Code != http.StatusOK || res.Body.Len() != 0 ||
		res.Header().Get("Content-Length") != "12" {
		t.Errorf("HEAD: %d %q %v", res.Code, res.Body, res.Header())
	}
}

func TestRawFileFailures(t *testing.T) {
	h := withHello(t)
	for _, path := range []string{"/f/not-a-uuid", "/f/not-a-uuid/x.png", "/f/" + missingID + "/x.png", "/t/nope", "/t/" + missingID} {
		res := h.do("GET", path, nil)
		if res.Code != http.StatusNotFound || res.Body.String() != "Not found" {
			t.Errorf("%s: %d %q", path, res.Code, res.Body)
		}
	}
	res := h.do("GET", "/f/00000000-0000-4000-8000-000000000502/x", nil)
	if res.Code != http.StatusBadGateway || res.Body.String() != "read the file: connection reset" {
		t.Errorf("storage error: %d %q", res.Code, res.Body)
	}
}

func TestContentDisposition(t *testing.T) {
	for name, want := range map[string]string{
		"hello.txt":       `inline; filename="hello.txt"; filename*=UTF-8''hello.txt`,
		`say "hi".txt`:    `inline; filename="say _hi_.txt"; filename*=UTF-8''say%20%22hi%22.txt`,
		"résumé.pdf":      `inline; filename="r_sum_.pdf"; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf`,
		"🎉.png":           `inline; filename="__.png"; filename*=UTF-8''%F0%9F%8E%89.png`,
		"tab\there":       `inline; filename="tab_here"; filename*=UTF-8''tab%09here`,
		"it's (1)~!*.txt": `inline; filename="it's (1)~!*.txt"; filename*=UTF-8''it's%20(1)~!*.txt`,
	} {
		if got := contentDisposition("inline", name); got != want {
			t.Errorf("%q:\n got %s\nwant %s", name, got, want)
		}
	}
}

func TestThumbnails(t *testing.T) {
	h := withHello(t)
	res := h.do("GET", "/t/"+helloID, nil)
	if res.Code != http.StatusFound || res.Header().Get("Location") != "/f/"+helloID {
		t.Errorf("no thumbnail: %d %v", res.Code, res.Header())
	}

	h.artifacts.thumbnail[helloID] = []byte("RIFF....WEBP")
	res = h.do("GET", "/t/"+helloID, nil)
	want := loadFixture(t, "19-raw").header
	if res.Code != http.StatusOK || res.Body.String() != "RIFF....WEBP" || res.Header().Get("Content-Type") != "image/webp" {
		t.Fatalf("thumbnail: %d %v %q", res.Code, res.Header(), res.Body)
	}
	for _, key := range []string{"Cache-Control", "X-Content-Type-Options", "Access-Control-Allow-Origin"} {
		if got := res.Header().Get(key); got != want.Get(key) {
			t.Errorf("%s = %q, want %q", key, got, want.Get(key))
		}
	}

	// Too many being made: come back later, not cached.
	h.artifacts.thumbnailErr = api.TooManyAttempts(1)
	res = h.do("GET", "/t/"+helloID, nil)
	if res.Code != http.StatusTooManyRequests || res.Header().Get("Retry-After") != "1" || res.Header().Get("Cache-Control") != "" {
		t.Errorf("busy: %d %v %q", res.Code, res.Header(), res.Body)
	}
}

// Raw files may be framed by the dashboard's viewer, and only by it, on top of their sandbox.
func TestRawFilesAreFramedOnlyByTheDashboard(t *testing.T) {
	h := withHello(t)
	res := h.do("GET", "/f/"+helloID+"/hello.txt", nil)
	if got := res.Header().Get("Content-Security-Policy"); got != "sandbox allow-scripts allow-popups allow-forms allow-downloads; frame-ancestors 'self'" {
		t.Errorf("Content-Security-Policy %q", got)
	}
}
