package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/db"
	"github.com/phinnerhq/sloppa/internal/storage"
	"github.com/phinnerhq/sloppa/internal/storage/fs"
)

const publicURL = "http://sloppa.test"

func newFsStore(t *testing.T, dir string) *fs.Store {
	t.Helper()
	s, err := fs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func open(t *testing.T, dataDir string, store storage.Store) *Service {
	t.Helper()
	s, err := Open(t.Context(), Options{DataDir: dataDir, PublicURL: publicURL, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newService is a service over an empty filesystem store of its own.
func newService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	return open(t, dir, newFsStore(t, filepath.Join(dir, "files")))
}

func upload(t *testing.T, s *Service, name, body, collection string) api.Artifact {
	t.Helper()
	a, err := s.Upload(t.Context(), Upload{Name: name, Body: strings.NewReader(body), Size: -1, Collection: collection})
	if err != nil {
		t.Fatalf("upload %s: %v", name, err)
	}
	return a
}

// reference is the JSON body of a response captured from the TypeScript server.
func reference(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "httpapi", "testdata", "reference", name))
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(data), "\n\n")
	if !ok {
		t.Fatalf("%s has no body", name)
	}
	return strings.TrimSpace(body)
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func apiError(t *testing.T, err error) *api.Error {
	t.Helper()
	var e *api.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *api.Error", err)
	}
	return e
}

// What an upload answers with is shaped as the reference server's.
func TestUploadAnswersLikeTheTypeScriptServer(t *testing.T) {
	s := newService(t)
	var got []map[string]any
	for _, f := range []struct{ name, contentType, body string }{
		{"hello.txt", "text/plain", "hello world\n"},
		{"notes.md", "", "# md\n"},
	} {
		a, err := s.Upload(t.Context(), Upload{Name: f.name, ContentType: f.contentType, Body: strings.NewReader(f.body), Size: int64(len(f.body)), Collection: " owner/repo #42 "})
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal([]byte(asJSON(t, a)), &m)
		got = append(got, m)
	}
	var want []map[string]any
	json.Unmarshal([]byte(reference(t, "07-upload.http")), &want)
	for i := range want {
		for _, varying := range []string{"id", "createdAt", "url", "rawUrl"} {
			if _, ok := got[i][varying]; !ok {
				t.Errorf("upload %d has no %s", i, varying)
			}
			delete(got[i], varying)
			delete(want[i], varying)
		}
	}
	if asJSON(t, got) != asJSON(t, want) {
		t.Errorf("got  %s\nwant %s", asJSON(t, got), asJSON(t, want))
	}
}

func TestPresent(t *testing.T) {
	s := newService(t)
	a := upload(t, s, "écran 1.png", "not really a png", "c")
	want := api.Artifact{
		ID:          a.ID,
		Name:        "écran 1.png",
		ContentType: "image/png",
		Size:        16,
		CreatedAt:   a.CreatedAt,
		Collection:  "c",
		URL:         publicURL + "/a/" + a.ID,
		RawURL:      publicURL + "/f/" + a.ID + "/%C3%A9cran%201.png",
		ThumbURL:    publicURL + "/t/" + a.ID,
	}
	if a != want {
		t.Errorf("got  %+v\nwant %+v", a, want)
	}
	for contentType, thumb := range map[string]bool{
		"image/jpeg": true, "IMAGE/PNG": true, "image/webp; q=1": true, "image/gif": true,
		"image/tiff": false, "image/avif": false, "image/svg+xml": false, "text/plain": false, "": false,
	} {
		if hasThumbnail(contentType) != thumb {
			t.Errorf("hasThumbnail(%q) = %v", contentType, !thumb)
		}
	}
}

func TestEmptyIsNotNull(t *testing.T) {
	s := newService(t)
	ctx := t.Context()
	page, _ := s.List(ctx, ListQuery{})
	mosaic, _ := s.Mosaic(ctx, "", 0)
	collections, _ := s.Collections(ctx)
	stats, _ := s.Stats(ctx)
	for _, tt := range []struct {
		v    any
		want string
	}{
		{page, `{"items":[]}`},
		{mosaic, `{"items":[]}`},
		{collections, `[]`},
		{stats, `{"total":0,"totalBytes":0,"last24h":0}`},
	} {
		if got := asJSON(t, tt.v); got != tt.want {
			t.Errorf("got %s, want %s", got, tt.want)
		}
	}
}

func TestCollectionsAreValidated(t *testing.T) {
	s := newService(t)
	a := upload(t, s, "a.txt", "a", "  spaced out  ")
	if a.Collection != "spaced out" {
		t.Errorf("collection %q, want it trimmed", a.Collection)
	}
	long := strings.Repeat("x", api.CollectionMax)
	if _, err := s.Move(t.Context(), a.ID, long); err != nil {
		t.Errorf("move to %d characters: %v", api.CollectionMax, err)
	}
	// 101 emoji are 202 UTF-16 code units, as JavaScript counts them.
	for _, bad := range []string{"", "   ", long + "x", strings.Repeat("😀", 101)} {
		if _, err := s.Upload(t.Context(), Upload{Name: "b.txt", Body: strings.NewReader("b"), Size: 1, Collection: bad}); apiError(t, err).Tag != "BadRequest" {
			t.Errorf("upload to %q: %v", bad, err)
		}
		_, err := s.Move(t.Context(), a.ID, bad)
		if e := apiError(t, err); e.Tag != "BadRequest" || e.Status != 400 || e.Message == "" {
			t.Errorf("move to %q: %+v", bad, e)
		}
	}
	if _, err := s.Move(t.Context(), a.ID, strings.Repeat("😀", 100)); err != nil {
		t.Errorf("move to 100 emoji: %v", err)
	}
}

func TestMove(t *testing.T) {
	s := newService(t)
	a := upload(t, s, "a.txt", "a", "before")
	moved, err := s.Move(t.Context(), a.ID, " after ")
	if err != nil {
		t.Fatal(err)
	}
	want := a
	want.Collection = "after"
	if moved != want {
		t.Errorf("got %+v\nwant %+v", moved, want)
	}
	page, _ := s.List(t.Context(), ListQuery{Collection: "after"})
	if len(page.Items) != 1 || page.Items[0] != want {
		t.Errorf("listed %+v", page.Items)
	}
	if page, _ := s.List(t.Context(), ListQuery{Collection: "before"}); len(page.Items) != 0 {
		t.Errorf("still listed under the old collection: %+v", page.Items)
	}
	_, err = s.Move(t.Context(), "00000000-0000-4000-8000-000000000000", "x")
	if e := apiError(t, err); e.Tag != "ArtifactNotFound" {
		t.Errorf("moving a missing artifact: %v", err)
	}
}

// A move or delete the client gives up on still finishes: storage and the index never disagree.
func TestMutationsOutliveTheRequest(t *testing.T) {
	s := newService(t)
	a := upload(t, s, "a.txt", "a", "before")
	b := upload(t, s, "b.txt", "b", "before")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Move(ctx, a.ID, "after"); err != nil {
		t.Fatalf("move with a cancelled request: %v", err)
	}
	if err := s.Remove(ctx, b.ID); err != nil {
		t.Fatalf("remove with a cancelled request: %v", err)
	}
	got, err := s.Get(t.Context(), a.ID)
	if err != nil || got.Collection != "after" {
		t.Errorf("after the move: %+v, %v", got, err)
	}
	if _, err := s.Get(t.Context(), b.ID); apiError(t, err).Tag != "ArtifactNotFound" {
		t.Errorf("after the remove: %v", err)
	}
}

// Moves and deletes racing each other never leave a deleted artifact behind.
func TestMovesAndRemovesRace(t *testing.T) {
	s := newService(t)
	a := upload(t, s, "a.txt", "a", "c")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if i == 4 {
				_ = s.Remove(t.Context(), a.ID)
			} else {
				_, _ = s.Move(t.Context(), a.ID, "c"+strings.Repeat("!", i))
			}
		})
	}
	wg.Wait()
	if _, err := s.store.Head(t.Context(), a.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("head after the race: %v", err)
	}
	if _, ok, _ := s.index.get(t.Context(), a.ID); ok {
		t.Error("still indexed after the race")
	}
}

func TestRemove(t *testing.T) {
	s := newService(t)
	a := upload(t, s, "shot.png", "x", "c")
	if err := s.thumbs.root.WriteFile(fileOf(a.ID), []byte("thumb"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.thumbs.root.Stat(fileOf(a.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("thumbnail left behind: %v", err)
	}
	if page, _ := s.List(t.Context(), ListQuery{}); len(page.Items) != 0 {
		t.Errorf("still listed: %+v", page.Items)
	}
	if e := apiError(t, s.Remove(t.Context(), a.ID)); e.Tag != "ArtifactNotFound" || e.ID != a.ID {
		t.Errorf("removing again: %+v", e)
	}
}

// An artifact gone from storage behind the index's back is unindexed by a delete, which says it was
// missing.
func TestRemoveUnindexesWhatStorageLost(t *testing.T) {
	s := newService(t)
	a := upload(t, s, "a.txt", "a", "c")
	if err := s.store.Remove(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	if e := apiError(t, s.Remove(t.Context(), a.ID)); e.Tag != "ArtifactNotFound" {
		t.Errorf("remove: %+v", e)
	}
	if page, _ := s.List(t.Context(), ListQuery{}); len(page.Items) != 0 {
		t.Errorf("still listed: %+v", page.Items)
	}
}

type failingStore struct {
	storage.Store
	err error
}

func (f failingStore) Remove(context.Context, string) error { return f.err }
func (f failingStore) Put(context.Context, storage.PutInput) (storage.Artifact, error) {
	return storage.Artifact{}, f.err
}

func TestStorageFailures(t *testing.T) {
	dir := t.TempDir()
	inner := newFsStore(t, filepath.Join(dir, "files"))
	a, err := inner.Put(t.Context(), storage.PutInput{Name: "a.txt", ContentType: "text/plain", Body: strings.NewReader("a"), Size: 1, Collection: "c"})
	if err != nil {
		t.Fatal(err)
	}
	s := open(t, dir, failingStore{inner, errors.New("delete artifact: the disk is on fire")})
	e := apiError(t, s.Remove(t.Context(), a.ID))
	if e.Tag != "StorageError" || e.Status != 502 || e.Message != "delete artifact: the disk is on fire" {
		t.Errorf("remove: %+v", e)
	}
	// A failed delete leaves it listed.
	if page, _ := s.List(t.Context(), ListQuery{}); len(page.Items) != 1 {
		t.Errorf("listed %+v", page.Items)
	}
	_, err = s.Upload(t.Context(), Upload{Name: "b", Body: strings.NewReader("b"), Size: 1, Collection: "c"})
	if e := apiError(t, err); e.Tag != "StorageError" {
		t.Errorf("upload: %+v", e)
	}
}

// Storage is authoritative: what the index doesn't have yet is looked up there, and indexed.
func TestGetBackfillsTheIndex(t *testing.T) {
	s := newService(t)
	upload(t, s, "indexed.txt", "a", "c")
	a, err := s.store.Put(t.Context(), storage.PutInput{Name: "behind.txt", ContentType: "text/plain", Body: strings.NewReader("b"), Size: 1, Collection: "c"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(t.Context(), a.ID)
	if err != nil || got.Name != "behind.txt" {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if page, _ := s.List(t.Context(), ListQuery{}); len(page.Items) != 2 {
		t.Errorf("listed %d, want the backfilled one too", len(page.Items))
	}
	_, err = s.Get(t.Context(), "not-a-uuid")
	if e := apiError(t, err); e.Tag != "ArtifactNotFound" {
		t.Errorf("get a bad id: %+v", e)
	}
}

// hookedStore runs afterPut once a Put is stored, and afterHead once a Head has read its artifact.
type hookedStore struct {
	storage.Store
	afterPut, afterHead func()
}

func (h *hookedStore) Put(ctx context.Context, in storage.PutInput) (storage.Artifact, error) {
	a, err := h.Store.Put(ctx, in)
	if err == nil && h.afterPut != nil {
		h.afterPut()
	}
	return a, err
}

func (h *hookedStore) Head(ctx context.Context, id string) (storage.Artifact, error) {
	a, err := h.Store.Head(ctx, id)
	if err == nil && h.afterHead != nil {
		h.afterHead()
	}
	return a, err
}

// An upload stored as the request is cancelled is indexed all the same: the index is only rebuilt
// when empty, so it would never be listed otherwise.
func TestUploadsOutliveTheRequestOnceStored(t *testing.T) {
	dir := t.TempDir()
	store := &hookedStore{Store: newFsStore(t, filepath.Join(dir, "files"))}
	s := open(t, dir, store)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store.afterPut = cancel
	a, err := s.Upload(ctx, Upload{Name: "a.txt", Body: strings.NewReader("a"), Size: 1, Collection: "c"})
	if err != nil {
		t.Fatalf("upload cancelled once stored: %v", err)
	}
	if page, _ := s.List(t.Context(), ListQuery{}); len(page.Items) != 1 || page.Items[0].ID != a.ID {
		t.Errorf("listed %+v", page.Items)
	}
}

// An upload that can't be indexed is taken back out of storage, as it failed.
func TestUploadsThatCantBeIndexedArentKept(t *testing.T) {
	s := newService(t)
	if err := s.index.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upload(t.Context(), Upload{Name: "a.txt", Body: strings.NewReader("a"), Size: 1, Collection: "c"}); err == nil {
		t.Fatal("uploaded without an index")
	}
	for a, err := range s.store.Scan(t.Context()) {
		t.Errorf("left in storage: %+v, %v", a, err)
	}
}

// A backfill racing a delete or a move doesn't index what storage held before it: a Head made before
// the mutation is indexed before it too.
func TestBackfillsWaitForMutations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(s *Service, id string) error
		want   string
	}{
		{"remove", func(s *Service, id string) error { return s.Remove(t.Context(), id) }, ""},
		{"move", func(s *Service, id string) error {
			_, err := s.Move(t.Context(), id, "after")
			return err
		}, "after"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := &hookedStore{Store: newFsStore(t, filepath.Join(dir, "files"))}
			s := open(t, dir, store)
			a, err := store.Put(t.Context(), storage.PutInput{Name: "a.txt", ContentType: "text/plain", Body: strings.NewReader("a"), Size: 1, Collection: "before"})
			if err != nil {
				t.Fatal(err)
			}
			headed, release := make(chan struct{}), make(chan struct{})
			store.afterHead = func() {
				store.afterHead = nil
				close(headed)
				<-release
			}
			got := make(chan error, 1)
			go func() {
				_, err := s.Get(t.Context(), a.ID)
				got <- err
			}()
			<-headed
			mutated := make(chan error, 1)
			go func() { mutated <- tc.mutate(s, a.ID) }()
			// The mutation waits for the backfill, so give it the time it would take otherwise.
			select {
			case err := <-mutated:
				mutated <- err
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			if err := <-got; err != nil {
				t.Errorf("get: %v", err)
			}
			if err := <-mutated; err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			indexed, ok, err := s.index.get(t.Context(), a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" && ok {
				t.Errorf("indexed after the remove: %+v", indexed)
			}
			if tc.want != "" && indexed.Collection != tc.want {
				t.Errorf("indexed under %q, want %q", indexed.Collection, tc.want)
			}
		})
	}
}

func TestOpen(t *testing.T) {
	s := newService(t)
	a := upload(t, s, "hello.txt", "hello world\n", "c")
	o, err := s.Open(t.Context(), a.ID, "bytes=0-4")
	if err != nil {
		t.Fatal(err)
	}
	defer o.Body.Close()
	body, _ := io.ReadAll(o.Body)
	if string(body) != "hello" || o.Range == nil || *o.Range != (storage.Range{Start: 0, End: 4}) || o.Artifact.Size != 12 {
		t.Errorf("read %q, %+v, %+v", body, o.Range, o.Artifact)
	}
	if o.Artifact.ContentType != "text/plain; charset=utf-8" {
		t.Errorf("content type %q", o.Artifact.ContentType)
	}
	_, err = s.Open(t.Context(), "00000000-0000-4000-8000-000000000000", "")
	if e := apiError(t, err); e.Tag != "ArtifactNotFound" {
		t.Errorf("open a missing artifact: %+v", e)
	}
}

func TestStats(t *testing.T) {
	s := newService(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	for i, age := range []time.Duration{0, time.Hour, 25 * time.Hour, 24*time.Hour + time.Millisecond, 24 * time.Hour} {
		if err := s.index.insert(t.Context(), storage.Artifact{
			ID:        "00000000-0000-4000-8000-00000000000" + string(rune('0'+i)),
			Size:      int64(10 * (i + 1)),
			CreatedAt: api.Time(now.Add(-age)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.Stats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 5 || stats.TotalBytes != 150 || stats.Last24h != 3 || !stats.LastUploadAt.Equal(api.Time(now)) {
		t.Errorf("stats %+v", stats)
	}
}

// The index is rebuilt when it's gone, empty, or from another schema, and kept otherwise.
func TestIndexRebuilds(t *testing.T) {
	dataDir := t.TempDir()
	store := newFsStore(t, filepath.Join(dataDir, "files"))
	s := open(t, dataDir, store)
	a := upload(t, s, "a.txt", "a", "c")
	s.Close()

	// Kept: storage gaining an artifact behind its back doesn't show until a rebuild.
	if _, err := store.Put(t.Context(), storage.PutInput{Name: "b.txt", ContentType: "text/plain", Body: strings.NewReader("b"), Size: 1, Collection: "c"}); err != nil {
		t.Fatal(err)
	}
	s = open(t, dataDir, store)
	if page, _ := s.List(t.Context(), ListQuery{}); len(page.Items) != 1 || page.Items[0].ID != a.ID {
		t.Errorf("reopened, listed %+v", page.Items)
	}
	s.Close()

	// Another schema version: rebuilt.
	sqlite, err := db.Open(filepath.Join(dataDir, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	sqlite.Close()
	s = open(t, dataDir, store)
	if page, _ := s.List(t.Context(), ListQuery{}); len(page.Items) != 2 {
		t.Errorf("after a version change, listed %d", len(page.Items))
	}
	s.Close()

	// Gone: rebuilt.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(filepath.Join(dataDir, "index.sqlite"+suffix))
	}
	s = open(t, dataDir, store)
	if page, _ := s.List(t.Context(), ListQuery{}); len(page.Items) != 2 {
		t.Errorf("after deleting the index, listed %d", len(page.Items))
	}
}

// A scan that fails leaves nothing half-built: the next start tries again.
func TestIndexRebuildIsAllOrNothing(t *testing.T) {
	dataDir := t.TempDir()
	inner := newFsStore(t, filepath.Join(dataDir, "files"))
	for range 3 {
		if _, err := inner.Put(t.Context(), storage.PutInput{Name: "a", ContentType: "text/plain", Body: strings.NewReader("a"), Size: 1, Collection: "c"}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Open(t.Context(), Options{DataDir: dataDir, PublicURL: publicURL, Store: brokenScan{inner}})
	if err == nil {
		t.Fatal("opened with a failing scan")
	}
	s := open(t, dataDir, inner)
	if page, _ := s.List(t.Context(), ListQuery{}); len(page.Items) != 3 {
		t.Errorf("listed %d, want all 3", len(page.Items))
	}
}

type brokenScan struct{ storage.Store }

func (b brokenScan) Scan(ctx context.Context) iter.Seq2[storage.Artifact, error] {
	return func(yield func(storage.Artifact, error) bool) {
		for a, err := range b.Store.Scan(ctx) {
			if !yield(a, err) {
				return
			}
			yield(storage.Artifact{}, errors.New("list storage dir: gone"))
			return
		}
	}
}

func TestUploadStreamsUnknownSizes(t *testing.T) {
	s := newService(t)
	body := bytes.Repeat([]byte("x"), 1<<20)
	a, err := s.Upload(t.Context(), Upload{Name: "big.bin", Body: io.MultiReader(bytes.NewReader(body)), Size: -1, Collection: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Size != 1<<20 || a.ContentType != "application/octet-stream" {
		t.Errorf("got %+v", a)
	}
}
