package fs_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/phinnerhq/sloppa/internal/storage"
	"github.com/phinnerhq/sloppa/internal/storage/fs"
	"github.com/phinnerhq/sloppa/internal/storage/storagetest"
)

// open is a store over dir, closed once the test is done: on Windows an open store keeps its dir
// from being deleted.
func open(t *testing.T, dir string) *fs.Store {
	t.Helper()
	s, err := fs.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newStore(t *testing.T) storage.Store { return open(t, filepath.Join(t.TempDir(), "files")) }

func TestContract(t *testing.T) {
	storagetest.Run(t, newStore)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Sidecars written before collections have none: they read back as unknown.
func TestReadsSidecarsWithoutCollection(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	tests := []struct {
		id, meta, want string
	}{
		{"00000000-0000-4000-8000-000000000001", `"repo":"o/r","pr":"https://github.com/o/r/pull/7"`, "unknown"},
		{"00000000-0000-4000-8000-000000000002", `"other":1`, "unknown"},
		{"00000000-0000-4000-8000-000000000003", `"collection":"new"`, "new"},
	}
	for _, tt := range tests {
		write(t, dir, tt.id, "x")
		write(t, dir, tt.id+".json", `{"name":"a.png","contentType":"image/png","size":1,"createdMs":1000,`+tt.meta+`}`)
		a, err := s.Head(t.Context(), tt.id)
		if err != nil {
			t.Fatal(err)
		}
		if a.Collection != tt.want {
			t.Errorf("%s: collection %q, want %q", tt.meta, a.Collection, tt.want)
		}
	}

	// A move rewrites the sidecar as this version does.
	if _, err := s.SetCollection(t.Context(), tests[0].id, "moved"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, tests[0].id+".json"))
	if want := `{"name":"a.png","contentType":"image/png","size":1,"createdMs":1000,"collection":"moved"}`; string(data) != want {
		t.Errorf("sidecar after a move:\n%s\nwant\n%s", data, want)
	}
}

// The bytes and the sidecar are laid out as stores already on disk hold them.
func TestWritesTheSidecarFormat(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	a, err := s.Put(t.Context(), storage.PutInput{Name: "n.txt", ContentType: "text/plain", Body: strings.NewReader("hi"), Size: 2, Collection: "c"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, a.ID+".json"))
	ms := a.CreatedAt.UnixMilli()
	want := `{"name":"n.txt","contentType":"text/plain","size":2,"createdMs":` + strconv.FormatInt(ms, 10) + `,"collection":"c"}`
	if string(data) != want {
		t.Errorf("sidecar:\n%s\nwant\n%s", data, want)
	}
	if bytes, _ := os.ReadFile(filepath.Join(dir, a.ID)); string(bytes) != "hi" {
		t.Errorf("bytes: %q", bytes)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("left %d files, want the bytes and the sidecar", len(entries))
	}
}

func TestPutRefusesSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	if _, err := s.Put(t.Context(), storage.PutInput{Name: "n", ContentType: "text/plain", Body: strings.NewReader("short"), Size: 10, Collection: "c"}); err == nil {
		t.Error("put a body shorter than its size")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a failed put left %d files", len(entries))
	}
}

func TestPutRefusesIDsThatAreNotUUIDs(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put(t.Context(), storage.PutInput{ID: "../escape", Name: "n", Body: strings.NewReader(""), Size: 0}); err == nil {
		t.Error("put under a path")
	}
}

// Scans skip what isn't an artifact and what can't be read, and index the rest.
func TestScanSkipsUnreadable(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	good, err := s.Put(t.Context(), storage.PutInput{Name: "good", ContentType: "text/plain", Body: strings.NewReader("x"), Size: 1, Collection: "c"})
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "00000000-0000-4000-8000-00000000000b.json", "{not json")
	write(t, dir, "00000000-0000-4000-8000-00000000000c.json", `{"name":"no type"}`)
	write(t, dir, "notes.json", `{"name":"a","contentType":"b","size":1,"createdMs":1}`)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}

	var ids []string
	for a, err := range s.Scan(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	if len(ids) != 1 || ids[0] != good.ID {
		t.Errorf("scanned %v, want only %s", ids, good.ID)
	}
	if _, err := s.Head(t.Context(), "00000000-0000-4000-8000-00000000000b"); err == nil {
		t.Error("headed a sidecar that isn't JSON")
	}
}

func TestNewRemovesStaleTempFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "00000000-0000-4000-8000-000000000001.123.tmp", "half an upload")
	write(t, dir, "keep", "x")
	open(t, dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "keep" {
		t.Errorf("left %v", entries)
	}
}

// A file being read doesn't keep its artifact from being deleted, on Windows too, and reads on.
func TestRemoveWhileReading(t *testing.T) {
	s := newStore(t)
	a, err := s.Put(t.Context(), storage.PutInput{Name: "a.txt", ContentType: "text/plain", Body: strings.NewReader("still here"), Size: 10, Collection: "c"})
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.Read(t.Context(), a.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer o.Body.Close()
	if err := s.Remove(t.Context(), a.ID); err != nil {
		t.Fatalf("remove while reading: %v", err)
	}
	if body, err := io.ReadAll(o.Body); err != nil || string(body) != "still here" {
		t.Errorf("read on: %q, %v", body, err)
	}
	if _, err := s.Head(t.Context(), a.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("head after remove: %v", err)
	}
}
