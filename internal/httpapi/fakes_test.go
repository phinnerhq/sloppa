package httpapi

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/artifacts"
	"github.com/phinnerhq/sloppa/internal/storage"
)

// fakeArtifacts keeps artifacts in memory and notes what it was asked.
type fakeArtifacts struct {
	mu        sync.Mutex
	items     map[string]api.Artifact
	content   map[string][]byte
	thumbnail map[string][]byte
	// thumbnailErr, if set, is what making any thumbnail fails with.
	thumbnailErr error

	lastList   artifacts.ListQuery
	lastMosaic struct {
		cursor string
		limit  int
	}
	uploads []artifacts.Upload
	moves   []string
	removed []string

	// failUpload, when set, fails the nth upload (from 1) after reading its body.
	failUpload int
	// err, when set, is what every listing call fails with.
	err error

	page        api.ArtifactPage
	groups      api.GroupPage
	collections []api.CollectionSummary
	stats       api.Stats
}

func newFakeArtifacts() *fakeArtifacts {
	return &fakeArtifacts{items: map[string]api.Artifact{}, content: map[string][]byte{}, thumbnail: map[string][]byte{}}
}

func (f *fakeArtifacts) add(a api.Artifact, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a.Size = int64(len(content))
	f.items[a.ID] = a
	f.content[a.ID] = []byte(content)
}

func (f *fakeArtifacts) List(_ context.Context, q artifacts.ListQuery) (api.ArtifactPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastList = q
	return f.page, f.err
}

func (f *fakeArtifacts) Mosaic(_ context.Context, cursor string, limit int) (api.GroupPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastMosaic.cursor, f.lastMosaic.limit = cursor, limit
	return f.groups, f.err
}

func (f *fakeArtifacts) Collections(context.Context) ([]api.CollectionSummary, error) {
	return f.collections, f.err
}

func (f *fakeArtifacts) Stats(context.Context) (api.Stats, error) { return f.stats, f.err }

func (f *fakeArtifacts) Get(_ context.Context, id string) (api.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.items[id]
	if !ok {
		return a, api.ArtifactNotFound(id)
	}
	return a, nil
}

func (f *fakeArtifacts) Upload(_ context.Context, u artifacts.Upload) (api.Artifact, error) {
	content, err := io.ReadAll(u.Body)
	if err != nil {
		return api.Artifact{}, fmt.Errorf("store: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u.Body = nil
	f.uploads = append(f.uploads, u)
	if len(f.uploads) == f.failUpload {
		return api.Artifact{}, api.StorageError("bucket on fire")
	}
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", len(f.uploads))
	a := api.Artifact{
		ID: id, Name: u.Name, ContentType: u.ContentType, Size: int64(len(content)),
		CreatedAt: api.Time(time.Now()), Collection: u.Collection,
		URL: "http://sloppa.test/a/" + id, RawURL: "http://sloppa.test" + api.RawPath(id, u.Name),
	}
	f.items[id] = a
	f.content[id] = content
	return a, nil
}

func (f *fakeArtifacts) Move(_ context.Context, id, collection string) (api.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.items[id]
	if !ok {
		return a, api.ArtifactNotFound(id)
	}
	f.moves = append(f.moves, collection)
	a.Collection = collection
	f.items[id] = a
	return a, nil
}

func (f *fakeArtifacts) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[id]; !ok {
		return api.ArtifactNotFound(id)
	}
	delete(f.items, id)
	f.removed = append(f.removed, id)
	return nil
}

var simpleRange = regexp.MustCompile(`^bytes=(\d+)-(\d+)$`)

func (f *fakeArtifacts) Open(_ context.Context, id, rangeHeader string) (*storage.Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == "00000000-0000-4000-8000-000000000502" {
		return nil, api.StorageError("read the file: connection reset")
	}
	a, ok := f.items[id]
	if !ok {
		return nil, fmt.Errorf("open %s: %w", id, storage.ErrNotFound)
	}
	content := f.content[id]
	obj := &storage.Object{
		Artifact:      storage.Artifact{ID: a.ID, Name: a.Name, ContentType: a.ContentType, Size: a.Size, CreatedAt: a.CreatedAt, Collection: a.Collection},
		Body:          io.NopCloser(bytes.NewReader(content)),
		ContentLength: int64(len(content)),
	}
	if m := simpleRange.FindStringSubmatch(rangeHeader); m != nil {
		start, _ := strconv.ParseInt(m[1], 10, 64)
		end, _ := strconv.ParseInt(m[2], 10, 64)
		obj.Body = io.NopCloser(bytes.NewReader(content[start : end+1]))
		obj.ContentLength = end - start + 1
		obj.Range = &storage.Range{Start: start, End: end}
	}
	return obj, nil
}

func (f *fakeArtifacts) Thumbnail(_ context.Context, id string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.items[id]; !ok {
		return nil, api.ArtifactNotFound(id)
	}
	if f.thumbnailErr != nil {
		return nil, f.thumbnailErr
	}
	if webp, ok := f.thumbnail[id]; ok {
		return webp, nil
	}
	return nil, fmt.Errorf("decode: unknown format: %w", artifacts.ErrNoThumbnail)
}

// fakePreviews serves *.preview.test, and notes what it was asked.
type fakePreviews struct {
	mu      sync.Mutex
	started []api.StartPreview
	stopped []string
	tunnels []string
	proxied []string
}

func (p *fakePreviews) List(context.Context, accounts.Account) ([]api.Preview, error) {
	return nil, nil
}

func (p *fakePreviews) Start(_ context.Context, a accounts.Account, in api.StartPreview) (api.StartedPreview, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if in.Name == "taken" {
		return api.StartedPreview{}, api.PreviewNameTaken(in.Name)
	}
	p.started = append(p.started, in)
	label := cmp.Or(in.Name, "74m2w312")
	return api.StartedPreview{
		ID:        "74m2w312",
		URL:       "http://" + label + ".preview.test/",
		TunnelURL: "ws://sloppa.test/api/previews/74m2w312/tunnel",
		ExpiresAt: api.Time(time.Now().Add(api.PreviewTTLDefault)),
	}, nil
}

func (p *fakePreviews) Stop(_ context.Context, _ accounts.Account, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id != "74m2w312" {
		return api.PreviewNotFound(id)
	}
	p.stopped = append(p.stopped, id)
	return nil
}

func (p *fakePreviews) Tunnel(w http.ResponseWriter, r *http.Request, id string) {
	a, _ := accounts.FromContext(r.Context())
	p.mu.Lock()
	p.tunnels = append(p.tunnels, id+" as "+a.Username+" with "+a.Kind)
	p.mu.Unlock()
	w.WriteHeader(http.StatusSwitchingProtocols)
}

func (p *fakePreviews) IsPreviewHost(host string) bool {
	return strings.HasSuffix(host, ".preview.test") || host == "preview.test"
}

func (p *fakePreviews) ServeProxy(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.proxied = append(p.proxied, r.Method+" "+r.Host+r.URL.Path)
	p.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, "<h1>No preview here</h1>")
}

var errBoom = errors.New("boom")
