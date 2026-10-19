// Package storagetest is the contract every storage backend keeps, as tests: what the artifacts
// service and the raw file route count on, whichever backend is behind them.
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/google/uuid"

	"github.com/phinnerhq/sloppa/internal/storage"
)

// Run tests the backend newStore makes. Each test gets a store of its own, though the stores may share
// a bucket: Scan is only checked for what the test put.
func Run(t *testing.T, newStore func(t *testing.T) storage.Store) {
	tests := []struct {
		name string
		test func(t *testing.T, s storage.Store)
	}{
		{"PutThenHead", putThenHead},
		{"PutWithID", putWithID},
		{"PutUnknownSize", putUnknownSize},
		{"PutFreeFormText", putFreeFormText},
		{"PutFailingBody", putFailingBody},
		{"PutLarge", putLarge},
		{"ReadWhole", readWhole},
		{"ReadRanges", readRanges},
		{"ReadEmpty", readEmpty},
		{"Missing", missing},
		{"Remove", remove},
		{"SetCollection", setCollection},
		{"Scan", scan},
		{"ScanStopsEarly", scanStopsEarly},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.test(t, newStore(t))
		})
	}
}

func put(t *testing.T, s storage.Store, name string, body []byte, collection string) storage.Artifact {
	t.Helper()
	a, err := s.Put(t.Context(), storage.PutInput{
		Name:        name,
		ContentType: "text/plain; charset=utf-8",
		Body:        bytes.NewReader(body),
		Size:        int64(len(body)),
		Collection:  collection,
	})
	if err != nil {
		t.Fatalf("put %s: %v", name, err)
	}
	t.Cleanup(func() { _ = s.Remove(context.WithoutCancel(t.Context()), a.ID) })
	return a
}

func readAll(t *testing.T, s storage.Store, id, rangeHeader string) (*storage.Object, []byte) {
	t.Helper()
	o, err := s.Read(t.Context(), id, rangeHeader)
	if err != nil {
		t.Fatalf("read %s (%q): %v", id, rangeHeader, err)
	}
	defer o.Body.Close()
	body, err := io.ReadAll(o.Body)
	if err != nil {
		t.Fatalf("read the body of %s: %v", id, err)
	}
	if int64(len(body)) != o.ContentLength {
		t.Errorf("read %s (%q): got %d bytes, ContentLength says %d", id, rangeHeader, len(body), o.ContentLength)
	}
	return o, body
}

func same(t *testing.T, got, want storage.Artifact) {
	t.Helper()
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	got.CreatedAt, want.CreatedAt = time.Time{}, time.Time{}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func putThenHead(t *testing.T, s storage.Store) {
	before := time.Now().Add(-time.Second)
	a := put(t, s, "hello.txt", []byte("hello world\n"), "owner/repo #42")
	if !storage.IsID(a.ID) {
		t.Errorf("ID = %q, want a UUID", a.ID)
	}
	want := storage.Artifact{
		ID:          a.ID,
		Name:        "hello.txt",
		ContentType: "text/plain; charset=utf-8",
		Size:        12,
		CreatedAt:   a.CreatedAt,
		Collection:  "owner/repo #42",
	}
	same(t, a, want)
	if a.CreatedAt.Before(before) || a.CreatedAt.After(time.Now()) {
		t.Errorf("CreatedAt = %v, want about now", a.CreatedAt)
	}
	if a.CreatedAt.Location() != time.UTC || a.CreatedAt.Nanosecond()%int(time.Millisecond) != 0 {
		t.Errorf("CreatedAt = %v, want UTC to the millisecond", a.CreatedAt)
	}
	head, err := s.Head(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	same(t, head, want)
}

func putWithID(t *testing.T, s storage.Store) {
	id := uuid.NewString()
	a, err := s.Put(t.Context(), storage.PutInput{ID: id, Name: "a.txt", ContentType: "text/plain", Body: strings.NewReader("a"), Size: 1, Collection: "c"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Remove(context.WithoutCancel(t.Context()), id) })
	if a.ID != id {
		t.Errorf("ID = %q, want %q", a.ID, id)
	}
	if _, err := s.Head(t.Context(), id); err != nil {
		t.Error(err)
	}
}

func putUnknownSize(t *testing.T, s storage.Store) {
	body := bytes.Repeat([]byte("0123456789"), 1000)
	a, err := s.Put(t.Context(), storage.PutInput{
		Name:        "unknown.bin",
		ContentType: "application/octet-stream",
		// Hides the reader's length, as a multipart part does.
		Body:       iotest.HalfReader(bytes.NewReader(body)),
		Size:       -1,
		Collection: "c",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Remove(context.WithoutCancel(t.Context()), a.ID) })
	if a.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", a.Size, len(body))
	}
	if _, got := readAll(t, s, a.ID, ""); !bytes.Equal(got, body) {
		t.Error("read back different bytes")
	}
}

func putFreeFormText(t *testing.T, s storage.Store) {
	name, collection := `écran "final" ✓ 100% done.png`, "acme/web.app #42 — ünïcode & <tags>"
	a := put(t, s, name, []byte("x"), collection)
	head, err := s.Head(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if head.Name != name || head.Collection != collection {
		t.Errorf("read back %q in %q, want %q in %q", head.Name, head.Collection, name, collection)
	}
}

func putFailingBody(t *testing.T, s storage.Store) {
	failure := errors.New("the client went away")
	collection := "broken " + uuid.NewString()
	_, err := s.Put(t.Context(), storage.PutInput{
		Name:        "broken.txt",
		ContentType: "text/plain",
		Body:        io.MultiReader(strings.NewReader("some bytes"), iotest.ErrReader(failure)),
		Size:        -1,
		Collection:  collection,
	})
	if err == nil {
		t.Fatal("put succeeded with a failing body")
	}
	for a, err := range s.Scan(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		if a.Collection == collection {
			t.Errorf("a failed put left %+v", a)
		}
	}
}

// putLarge is past the size S3 uploads in parts from.
func putLarge(t *testing.T, s storage.Store) {
	if testing.Short() {
		t.Skip("short")
	}
	body := make([]byte, 21<<20)
	for i := range body {
		body[i] = byte(i * 7 % 251)
	}
	a, err := s.Put(t.Context(), storage.PutInput{Name: "large.bin", ContentType: "application/octet-stream", Body: iotest.HalfReader(bytes.NewReader(body)), Size: -1, Collection: "large"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Remove(context.WithoutCancel(t.Context()), a.ID) })
	if a.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", a.Size, len(body))
	}
	if _, got := readAll(t, s, a.ID, ""); !bytes.Equal(got, body) {
		t.Error("read back different bytes")
	}
	o, got := readAll(t, s, a.ID, "bytes=20000000-20000009")
	if !bytes.Equal(got, body[20000000:20000010]) || o.Range == nil || o.Artifact.Size != int64(len(body)) {
		t.Errorf("range read back %v, %+v", got, o.Range)
	}
}

func readWhole(t *testing.T, s storage.Store) {
	a := put(t, s, "hello.txt", []byte("hello world\n"), "c")
	o, body := readAll(t, s, a.ID, "")
	if string(body) != "hello world\n" || o.Range != nil || o.ContentLength != 12 {
		t.Errorf("read %q, range %+v, length %d", body, o.Range, o.ContentLength)
	}
	same(t, o.Artifact, a)
}

func readRanges(t *testing.T, s storage.Store) {
	a := put(t, s, "hello.txt", []byte("hello world\n"), "c")
	tests := []struct {
		header string
		want   string
		rng    *storage.Range
	}{
		{"bytes=0-4", "hello", &storage.Range{Start: 0, End: 4}},
		{"bytes=6-", "world\n", &storage.Range{Start: 6, End: 11}},
		{"bytes=-6", "world\n", &storage.Range{Start: 6, End: 11}},
		{"bytes=-100", "hello world\n", &storage.Range{Start: 0, End: 11}},
		{"bytes=6-100", "world\n", &storage.Range{Start: 6, End: 11}},
		{"bytes=11-11", "\n", &storage.Range{Start: 11, End: 11}},
		{"bytes=12-", "hello world\n", nil},
		{"bytes=5-2", "hello world\n", nil},
		{"bytes=-", "hello world\n", nil},
		{"pages=0-1", "hello world\n", nil},
		{"bytes=abc", "hello world\n", nil},
	}
	for _, tt := range tests {
		o, body := readAll(t, s, a.ID, tt.header)
		if string(body) != tt.want {
			t.Errorf("%q: read %q, want %q", tt.header, body, tt.want)
		}
		if !equalRange(o.Range, tt.rng) {
			t.Errorf("%q: range %+v, want %+v", tt.header, o.Range, tt.rng)
		}
		// The size is the file's, not the range's.
		if o.Artifact.Size != 12 {
			t.Errorf("%q: size %d, want 12", tt.header, o.Artifact.Size)
		}
	}
}

func equalRange(a, b *storage.Range) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func readEmpty(t *testing.T, s storage.Store) {
	a := put(t, s, "empty.txt", nil, "c")
	if a.Size != 0 {
		t.Errorf("Size = %d, want 0", a.Size)
	}
	for _, header := range []string{"", "bytes=0-", "bytes=-1"} {
		o, body := readAll(t, s, a.ID, header)
		if len(body) != 0 || o.Range != nil {
			t.Errorf("%q: read %q, range %+v", header, body, o.Range)
		}
	}
}

func missing(t *testing.T, s storage.Store) {
	ctx := t.Context()
	for _, id := range []string{uuid.NewString(), "not-a-uuid", "../../etc/passwd"} {
		if _, err := s.Head(ctx, id); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("head %q: %v, want ErrNotFound", id, err)
		}
		if _, err := s.Read(ctx, id, ""); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("read %q: %v, want ErrNotFound", id, err)
		}
		if _, err := s.Read(ctx, id, "bytes=0-1"); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("read a range of %q: %v, want ErrNotFound", id, err)
		}
		if err := s.Remove(ctx, id); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("remove %q: %v, want ErrNotFound", id, err)
		}
		if _, err := s.SetCollection(ctx, id, "c"); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("set the collection of %q: %v, want ErrNotFound", id, err)
		}
	}
}

func remove(t *testing.T, s storage.Store) {
	a := put(t, s, "doomed.txt", []byte("bye"), "c")
	if err := s.Remove(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Head(t.Context(), a.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("head after remove: %v, want ErrNotFound", err)
	}
	if err := s.Remove(t.Context(), a.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("remove again: %v, want ErrNotFound", err)
	}
}

func setCollection(t *testing.T, s storage.Store) {
	a := put(t, s, "moved.txt", []byte("moving"), "before")
	moved, err := s.SetCollection(t.Context(), a.ID, "after ✓")
	if err != nil {
		t.Fatal(err)
	}
	want := a
	want.Collection = "after ✓"
	same(t, moved, want)
	head, err := s.Head(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	same(t, head, want)
	if _, body := readAll(t, s, a.ID, ""); string(body) != "moving" {
		t.Errorf("bytes after a move: %q", body)
	}

	// Moves racing each other leave one of them, whole.
	var wg sync.WaitGroup
	for _, c := range []string{"x", "y", "z", "w"} {
		wg.Go(func() {
			if _, err := s.SetCollection(t.Context(), a.ID, c); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	head, err = s.Head(t.Context(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains([]string{"x", "y", "z", "w"}, head.Collection) {
		t.Errorf("collection after racing moves: %q", head.Collection)
	}
}

func scan(t *testing.T, s storage.Store) {
	collection := "scan " + uuid.NewString()
	want := map[string]storage.Artifact{}
	for i := range 40 {
		a := put(t, s, strings.Repeat("n", i+1), []byte("x"), collection)
		want[a.ID] = a
	}
	got := map[string]storage.Artifact{}
	for a, err := range s.Scan(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		if a.Collection == collection {
			if _, dup := got[a.ID]; dup {
				t.Errorf("scanned %s twice", a.ID)
			}
			got[a.ID] = a
		}
	}
	if len(got) != len(want) {
		t.Fatalf("scanned %d artifacts, want %d", len(got), len(want))
	}
	for id, a := range want {
		same(t, got[id], a)
	}
}

func scanStopsEarly(t *testing.T, s storage.Store) {
	for range 20 {
		put(t, s, "early.txt", []byte("x"), "early")
	}
	n := 0
	for _, err := range s.Scan(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		if n++; n == 3 {
			break
		}
	}
	if n != 3 {
		t.Errorf("stopped after %d, want 3", n)
	}
}
