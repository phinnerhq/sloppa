package artifacts

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/storage"
)

// Paging through the index: what the end-to-end tests can't arrange, artifacts uploaded within the
// same millisecond, which a page boundary must neither skip nor repeat.

func newIndex(t *testing.T) *index {
	t.Helper()
	dir := t.TempDir()
	// Empty, so the index starts empty and holds only what the tests insert.
	ix, err := openIndex(t.Context(), filepath.Join(dir, "index.sqlite"), newFsStore(t, filepath.Join(dir, "files")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix
}

func artifactAt(n int, ms int64, collection string) storage.Artifact {
	return storage.Artifact{
		ID:          fmt.Sprintf("00000000-0000-4000-8000-%012d", n),
		Name:        fmt.Sprintf("%d.txt", n),
		ContentType: "text/plain",
		Size:        1,
		CreatedAt:   api.Millis(ms),
		Collection:  collection,
	}
}

func insert(t *testing.T, ix *index, artifacts ...storage.Artifact) {
	t.Helper()
	for _, a := range artifacts {
		if err := ix.insert(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
}

// walk is every page of a listing, limit at a time, as a client would walk them.
func walk(t *testing.T, ix *index, collection, search string, limit int) [][]string {
	t.Helper()
	var pages [][]string
	cursor := ""
	for {
		items, next, err := ix.list(t.Context(), collection, search, cursor, limit)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, a := range items {
			names = append(names, a.Name)
		}
		pages = append(pages, names)
		if next == "" {
			return pages
		}
		cursor = next
	}
}

func TestListPagesThroughTheSameMillisecond(t *testing.T) {
	ix := newIndex(t)
	for _, n := range []int{1, 2, 3, 4, 5} {
		insert(t, ix, artifactAt(n, 1000, "same ms"))
	}
	got := walk(t, ix, "same ms", "", 2)
	want := [][]string{{"5.txt", "4.txt"}, {"3.txt", "2.txt"}, {"1.txt"}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("pages %v, want %v", got, want)
	}
}

func TestListOrdersByTimeFirst(t *testing.T) {
	ix := newIndex(t)
	insert(t, ix, artifactAt(9, 1000, "by time"), artifactAt(1, 3000, "by time"), artifactAt(5, 2000, "by time"))
	got := walk(t, ix, "by time", "", 10)
	if want := [][]string{{"1.txt", "5.txt", "9.txt"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("pages %v, want %v", got, want)
	}
}

func TestListEndsWithoutACursorOnAFullLastPage(t *testing.T) {
	ix := newIndex(t)
	insert(t, ix, artifactAt(1, 1000, "exact"), artifactAt(2, 2000, "exact"))
	got := walk(t, ix, "exact", "", 2)
	if want := [][]string{{"2.txt", "1.txt"}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("pages %v, want %v", got, want)
	}
}

func TestListFiltersByCollection(t *testing.T) {
	ix := newIndex(t)
	insert(t, ix, artifactAt(1, 1000, "a"), artifactAt(2, 2000, "b"), artifactAt(3, 3000, "a"))
	if got := walk(t, ix, "a", "", 10); !slices.Equal(got[0], []string{"3.txt", "1.txt"}) {
		t.Errorf("listed %v", got)
	}
	if got := walk(t, ix, "", "", 10); !slices.Equal(got[0], []string{"3.txt", "2.txt", "1.txt"}) {
		t.Errorf("listed %v", got)
	}
}

func TestSearch(t *testing.T) {
	ix := newIndex(t)
	named := func(n int, name, contentType, collection string) storage.Artifact {
		a := artifactAt(n, int64(n)*1000, collection)
		a.Name, a.ContentType = name, contentType
		return a
	}
	insert(t, ix,
		named(1, "Screenshot.PNG", "image/png", "acme/web #1"),
		named(2, "100% done.txt", "text/plain", "misc"),
		named(3, "100x done.txt", "text/plain", "misc"),
		named(4, "snake_case.log", "text/plain", "misc"),
		named(5, "snakeXcase.log", "text/plain", "misc"),
		named(6, `back\slash.txt`, "text/plain", "misc"),
		named(7, "trace.json", "application/json", "acme/api #2"),
	)
	tests := []struct {
		search string
		want   []string
	}{
		{"screenshot", []string{"Screenshot.PNG"}},
		{"% done", []string{"100% done.txt"}},
		{"e_c", []string{"snake_case.log"}},
		{`k\s`, []string{`back\slash.txt`}},
		{"acme", []string{"trace.json", "Screenshot.PNG"}},
		{"application/", []string{"trace.json"}},
		{"-8000-000000000004", []string{"snake_case.log"}},
		{"nothing like it", nil},
	}
	for _, tt := range tests {
		if got := walk(t, ix, "", tt.search, 10)[0]; !slices.Equal(got, tt.want) {
			t.Errorf("search %q: %v, want %v", tt.search, got, tt.want)
		}
	}
	// Search and collection together, paged.
	if got := walk(t, ix, "misc", "done", 1); !slices.EqualFunc(got, [][]string{{"100x done.txt"}, {"100% done.txt"}}, slices.Equal) {
		t.Errorf("paged search: %v", got)
	}
}

// Cursors round-trip, and a garbled one starts from the top.
func TestCursors(t *testing.T) {
	ix := newIndex(t)
	insert(t, ix, artifactAt(1, 1792363108495, "c"), artifactAt(2, 1792363108498, "c"))
	_, next, err := ix.list(t.Context(), "", "", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := encodeCursor(artifactCursorAt(1792363108498, artifactAt(2, 0, "").ID)); next != want {
		t.Errorf("cursor %q, want %q", next, want)
	}
	if c, ok := decodeCursor[artifactCursor](next); !ok || *c.Ms != 1792363108498 || *c.ID != artifactAt(2, 0, "").ID {
		t.Errorf("decoded %+v, %v", c, ok)
	}
	padded := base64.URLEncoding.EncodeToString([]byte(`{"ms":1792363108498,"id":"x"}`))
	if _, ok := decodeCursor[artifactCursor](padded); !ok {
		t.Errorf("padded cursor %q refused", padded)
	}
	for _, bad := range []string{"!!!", "e30", base64.RawURLEncoding.EncodeToString([]byte(`{"ms":"1","id":"x"}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"ms":1}`)), base64.RawURLEncoding.EncodeToString([]byte(`[1]`))} {
		if _, ok := decodeCursor[artifactCursor](bad); ok {
			t.Errorf("decoded %q", bad)
		}
		items, _, err := ix.list(t.Context(), "", "", bad, 10)
		if err != nil || len(items) != 2 {
			t.Errorf("listing from %q: %d items, %v", bad, len(items), err)
		}
	}
	// An artifact cursor isn't a group cursor.
	if _, ok := decodeCursor[groupCursor](next); ok {
		t.Error("took an artifact cursor for a group cursor")
	}
	if got := encodeCursor(groupCursorAt(1, "a <b> & c")); got != base64.RawURLEncoding.EncodeToString([]byte(`{"ms":1,"collection":"a <b> & c"}`)) {
		t.Errorf("group cursor %q", got)
	}
}

func TestGroups(t *testing.T) {
	ix := newIndex(t)
	// Collections a and b were last active at the same time, c before them, d first.
	insert(t, ix,
		artifactAt(1, 1000, "d"),
		artifactAt(2, 2000, "c"), artifactAt(3, 2500, "c"),
		artifactAt(4, 3000, "a"), artifactAt(5, 3000, "b"),
	)
	for n := 10; n < 16; n++ {
		insert(t, ix, artifactAt(n, int64(100+n), "a"))
	}
	type page struct {
		groups []string
		next   bool
	}
	var got []page
	var all []group
	cursor := ""
	for {
		groups, next, err := ix.groups(t.Context(), cursor, 2, api.GroupPreviewSize)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, g := range groups {
			names = append(names, g.collection)
		}
		got = append(got, page{names, next != ""})
		all = append(all, groups...)
		if next == "" {
			break
		}
		cursor = next
	}
	want := []page{{[]string{"b", "a"}, true}, {[]string{"c", "d"}, false}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("pages %v, want %v", got, want)
	}
	a := all[1]
	var preview []string
	for _, p := range a.preview {
		preview = append(preview, p.Name)
	}
	if a.count != 7 || a.lastMs != 3000 || !slices.Equal(preview, []string{"4.txt", "15.txt", "14.txt", "13.txt"}) {
		t.Errorf("group a: count %d, last %d, preview %v", a.count, a.lastMs, preview)
	}

	collections, err := ix.collections(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range collections {
		names = append(names, fmt.Sprintf("%s:%d", c.collection, c.count))
	}
	if want := []string{"b:1", "a:7", "c:2", "d:1"}; !slices.Equal(names, want) {
		t.Errorf("collections %v, want %v", names, want)
	}
}

func TestInsertUpdates(t *testing.T) {
	ix := newIndex(t)
	a := artifactAt(1, 1000, "before")
	insert(t, ix, a)
	a.Collection = "after"
	insert(t, ix, a)
	got, ok, err := ix.get(t.Context(), a.ID)
	if err != nil || !ok || got != a {
		t.Errorf("got %+v, %v, %v", got, ok, err)
	}
	if err := ix.remove(t.Context(), a.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ix.get(t.Context(), a.ID); ok {
		t.Error("still there after remove")
	}
}
