package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/phinnerhq/sloppa/internal/storage"
)

// flushes records what is flushed while f runs, with whether the bytes and the sidecar of id are in
// place at that point.
func flushes(t *testing.T, s *Store, id string, f func()) []string {
	t.Helper()
	var got []string
	fsync = func(file *os.File) error {
		exists := func(name string) bool {
			_, err := s.root.Stat(name)
			return err == nil
		}
		what := "dir"
		if strings.HasPrefix(filepath.Base(file.Name()), id+".json.") {
			what = "sidecar temp"
		} else if file.Name() != s.dir {
			what = "bytes temp"
		}
		got = append(got, fmt.Sprintf("%s, bytes %v, sidecar %v", what, exists(fileOf(id)), exists(metaOf(id))))
		return file.Sync()
	}
	defer func() { fsync = (*os.File).Sync }()
	f()
	return got
}

// Writes are flushed before they're acknowledged, the bytes before the sidecar that makes them an
// artifact; deletes too, the sidecar first.
func TestFlushes(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := "00000000-0000-4000-8000-000000000001"
	got := flushes(t, s, id, func() {
		if _, err := s.Put(t.Context(), storage.PutInput{ID: id, Name: "a.txt", Body: strings.NewReader("a"), Size: 1}); err != nil {
			t.Fatal(err)
		}
	})
	want := []string{
		"bytes temp, bytes false, sidecar false",
		"dir, bytes true, sidecar false",
		"sidecar temp, bytes true, sidecar false",
		"dir, bytes true, sidecar true",
	}
	want = onThisOS(want)
	if !slices.Equal(got, want) {
		t.Errorf("put flushed:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	got = flushes(t, s, id, func() {
		if err := s.Remove(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	})
	want = []string{
		"dir, bytes true, sidecar false",
		"dir, bytes false, sidecar false",
	}
	want = onThisOS(want)
	if !slices.Equal(got, want) {
		t.Errorf("remove flushed:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// onThisOS is the flushes wanted, less the directory ones on Windows, which has none.
func onThisOS(want []string) []string {
	if runtime.GOOS != "windows" {
		return want
	}
	return slices.DeleteFunc(want, func(f string) bool { return strings.HasPrefix(f, "dir,") })
}
