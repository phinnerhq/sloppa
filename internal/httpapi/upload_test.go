package httpapi

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/phinnerhq/sloppa/internal/api"
)

// part is one part of a multipart body: a file when it has a file name.
type part struct {
	field, filename, contentType string
	body                         io.Reader
}

func field(name, value string) part { return part{field: name, body: strings.NewReader(value)} }

func file(name, contentType, content string) part {
	return part{field: "files", filename: name, contentType: contentType, body: strings.NewReader(content)}
}

// multipartBody streams parts, as the CLI sends them, and is its Content-Type.
func multipartBody(parts ...part) (io.Reader, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		for _, p := range parts {
			header := textproto.MIMEHeader{}
			disposition := `form-data; name="` + p.field + `"`
			if p.filename != "" {
				disposition += `; filename="` + p.filename + `"`
			}
			header.Set("Content-Disposition", disposition)
			if p.contentType != "" {
				header.Set("Content-Type", p.contentType)
			}
			w, err := mw.CreatePart(header)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := io.Copy(w, p.body); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		pw.CloseWithError(mw.Close())
	}()
	return pr, mw.FormDataContentType()
}

func (h *harness) upload(token string, parts ...part) *httptest.ResponseRecorder {
	h.t.Helper()
	body, contentType := multipartBody(parts...)
	r := h.req("POST", "/api/artifacts", body).header("Content-Type", contentType)
	if token != "" {
		r = r.bearer(token)
	}
	res := h.send(r)
	// Whatever the server didn't read, so the writing goroutine ends.
	io.Copy(io.Discard, body)
	return res
}

func TestUpload(t *testing.T) {
	h := newHarness(t)
	token := h.agentToken()
	rec := h.upload(token,
		field("collection", " owner/repo #42 "),
		file("hello.txt", "text/plain", "hello world\n"),
		file("notes.md", "", "notes"),
	)
	like(t, rec, "07-upload", false)

	uploads := h.artifacts.uploads
	if len(uploads) != 2 {
		t.Fatalf("uploads %+v", uploads)
	}
	for i, want := range []struct{ name, contentType string }{{"hello.txt", "text/plain"}, {"notes.md", ""}} {
		u := uploads[i]
		if u.Name != want.name || u.ContentType != want.contentType || u.Size != -1 || u.Collection != "owner/repo #42" {
			t.Errorf("upload %d: %+v", i, u)
		}
	}
	got := decode[[]api.Artifact](t, rec)
	if got[0].Size != 12 || got[1].Size != 5 {
		t.Errorf("sizes %d, %d", got[0].Size, got[1].Size)
	}
}

func TestUploadTakesTheSessionCookie(t *testing.T) {
	h := newHarness(t)
	body, contentType := multipartBody(field("collection", "x"), file("a.txt", "", "a"))
	r := h.req("POST", "/api/artifacts", body).header("Content-Type", contentType).cookie(h.login())
	if res := h.send(r); res.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", res.Code, res.Body)
	}

	body, contentType = multipartBody(field("collection", "x"), file("a.txt", "", "a"))
	r = h.req("POST", "/api/artifacts", body).header("Content-Type", contentType).cookie(h.login()).header("Sec-Fetch-Site", "cross-site")
	if res := h.send(r); res.Code != http.StatusForbidden {
		t.Fatalf("cross-site upload: %d %s", res.Code, res.Body)
	}
}

func TestUploadNeedsSignIn(t *testing.T) {
	h := newHarness(t)
	rec := (h.upload("", field("collection", "x"), file("a.txt", "", "a")))
	like(t, rec, "01-session-unauth", true)
	if len(h.artifacts.uploads) != 0 {
		t.Error("stored an unauthenticated upload")
	}
}

func TestUploadRefusals(t *testing.T) {
	h := newHarness(t)
	token := h.agentToken()
	for name, parts := range map[string][]part{
		"collection after the files": {file("a.txt", "", "a"), field("collection", "x")},
		"no collection":              {file("a.txt", "", "a")},
		"a blank collection":         {field("collection", "  "), file("a.txt", "", "a")},
		"a long collection":          {field("collection", strings.Repeat("x", api.CollectionMax+1)), file("a.txt", "", "a")},
		"two collections":            {field("collection", "x"), field("collection", "y"), file("a.txt", "", "a")},
		"no files":                   {field("collection", "x")},
		"nothing":                    {},
	} {
		rec := (h.upload(token, parts...))
		if code, msg := errorCode(t, rec); rec.Code != http.StatusBadRequest || code != "bad_request" {
			t.Errorf("%s: %d %s %s", name, rec.Code, code, msg)
		}
	}
	if len(h.artifacts.items) != 0 {
		t.Errorf("kept %d artifacts", len(h.artifacts.items))
	}

	r := h.req("POST", "/api/artifacts", map[string]string{"collection": "x"}).bearer(token)
	if res := h.send(r); res.Code != http.StatusBadRequest {
		t.Errorf("JSON body: %d %s", res.Code, res.Body)
	}
}

func TestUploadIgnoresOtherFields(t *testing.T) {
	h := newHarness(t)
	rec := (h.upload(h.agentToken(), field("note", "hi"), field("collection", "x"), file("a.txt", "", "a"), field("files", "")))
	if rec.Code != http.StatusOK || len(decode[[]api.Artifact](t, rec)) != 1 {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestUploadFailsAsAWhole(t *testing.T) {
	h := newHarness(t)
	h.artifacts.failUpload = 2
	rec := (h.upload(h.agentToken(), field("collection", "x"), file("a.txt", "", "a"), file("b.txt", "", "b"), file("c.txt", "", "c")))
	if code, msg := errorCode(t, rec); rec.Code != http.StatusBadGateway || code != "storage_error" || msg != "bucket on fire" {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if len(h.artifacts.items) != 0 || len(h.artifacts.removed) != 1 {
		t.Errorf("left %d artifacts, removed %q", len(h.artifacts.items), h.artifacts.removed)
	}
	if len(h.artifacts.uploads) != 2 {
		t.Errorf("went on after the failure: %d uploads", len(h.artifacts.uploads))
	}
}

func TestUploadLimitsEachFile(t *testing.T) {
	h := newHarness(t)
	token := h.agentToken()
	exactly := part{field: "files", filename: "big.bin", body: io.LimitReader(zeros{}, api.MaxUploadBytes)}
	rec := (h.upload(token, field("collection", "x"), exactly))
	if rec.Code != http.StatusOK {
		t.Fatalf("a file at the limit: %d %s", rec.Code, rec.Body)
	}

	over := part{field: "files", filename: "big.bin", body: io.LimitReader(zeros{}, api.MaxUploadBytes+1)}
	rec = (h.upload(token, field("collection", "x"), file("small.txt", "", "a"), over))
	if code, _ := errorCode(t, rec); rec.Code != http.StatusRequestEntityTooLarge || code != "payload_too_large" {
		t.Errorf("a file past the limit: %d %s", rec.Code, rec.Body)
	}
	if len(h.artifacts.items) != 1 {
		t.Errorf("kept %d artifacts, want only the first upload's", len(h.artifacts.items))
	}
}

func TestUploadLimitsTheWhole(t *testing.T) {
	h := newHarness(t)
	parts := []part{field("collection", "x")}
	for range 3 {
		parts = append(parts, part{field: "files", filename: "big.bin", body: io.LimitReader(zeros{}, 50<<20)})
	}
	rec := (h.upload(h.agentToken(), parts...))
	if code, _ := errorCode(t, rec); rec.Code != http.StatusRequestEntityTooLarge || code != "payload_too_large" {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if len(h.artifacts.items) != 0 {
		t.Errorf("kept %d artifacts", len(h.artifacts.items))
	}
}

func TestCappedReader(t *testing.T) {
	for _, tc := range []struct {
		size, limit int64
		over        bool
	}{{0, 0, false}, {1, 0, true}, {10, 10, false}, {11, 10, true}, {100_000, 10, true}} {
		c := &cappedReader{r: bytes.NewReader(make([]byte, tc.size)), left: tc.limit}
		n, err := io.Copy(io.Discard, c)
		if over := err != nil; over != tc.over || n > tc.limit {
			t.Errorf("%d bytes capped at %d: read %d, %v", tc.size, tc.limit, n, err)
		}
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
