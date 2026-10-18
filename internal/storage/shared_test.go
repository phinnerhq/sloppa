package storage_test

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strconv"
	"testing"

	"github.com/phinnerhq/sloppa/internal/storage"
)

func TestParseRange(t *testing.T) {
	tests := []struct {
		header string
		size   int64
		want   *storage.Range
	}{
		{"bytes=0-4", 12, &storage.Range{Start: 0, End: 4}},
		{" bytes=0-4 ", 12, &storage.Range{Start: 0, End: 4}},
		{"bytes=6-", 12, &storage.Range{Start: 6, End: 11}},
		{"bytes=-5", 12, &storage.Range{Start: 7, End: 11}},
		{"bytes=-50", 12, &storage.Range{Start: 0, End: 11}},
		{"bytes=3-99", 12, &storage.Range{Start: 3, End: 11}},
		{"bytes=0-99999999999999999999", 12, &storage.Range{Start: 0, End: 11}},
		{"bytes=-99999999999999999999", 12, &storage.Range{Start: 0, End: 11}},
		{"bytes=99999999999999999999-", 12, nil},
		{"bytes=12-", 12, nil},
		{"bytes=4-3", 12, nil},
		{"bytes=0-", 0, nil},
		{"bytes=-1", 0, nil},
		{"bytes=-0", 12, nil},
		{"bytes=-", 12, nil},
		{"", 12, nil},
		{"bytes=0-1,3-4", 12, nil},
		{"bytes = 0-1", 12, nil},
		{"items=0-1", 12, nil},
	}
	for _, tt := range tests {
		got := storage.ParseRange(tt.header, tt.size)
		if (got == nil) != (tt.want == nil) || got != nil && *got != *tt.want {
			t.Errorf("ParseRange(%q, %d) = %+v, want %+v", tt.header, tt.size, got, tt.want)
		}
	}
}

func TestHeadAllSkipsMissingAndUnreadable(t *testing.T) {
	var got []int
	for a, err := range storage.HeadAll(t.Context(), idsOf(100, -1), headOf) {
		if err != nil {
			t.Fatal(err)
		}
		n, _ := strconv.Atoi(a.ID)
		got = append(got, n)
	}
	slices.Sort(got)
	var want []int
	for i := range 100 {
		if i%10 != 3 && i%10 != 7 {
			want = append(want, i)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestHeadAllEndsOnAListingError(t *testing.T) {
	var err error
	for _, err = range storage.HeadAll(t.Context(), idsOf(100, 50), headOf) {
		if err != nil {
			break
		}
	}
	if err == nil || err.Error() != "listing failed" {
		t.Errorf("err = %v, want the listing's", err)
	}
}

func TestHeadAllStopsEarly(t *testing.T) {
	n := 0
	for range storage.HeadAll(t.Context(), idsOf(1000, -1), headOf) {
		if n++; n == 5 {
			break
		}
	}
	if n != 5 {
		t.Errorf("n = %d", n)
	}
}

// A scan cut off by its context says so, rather than looking finished.
func TestHeadAllReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var last error
	n := 0
	for _, err := range storage.HeadAll(ctx, idsOf(1000, -1), headOf) {
		if n++; n == 5 {
			cancel()
		}
		last = err
	}
	if !errors.Is(last, context.Canceled) {
		t.Errorf("last error = %v, want context.Canceled", last)
	}
}

func TestIsID(t *testing.T) {
	for id, want := range map[string]bool{
		"26a55539-b1bb-4ebf-b781-54c5ba3bf726": true,
		"26A55539-B1BB-4EBF-B781-54C5BA3BF726": true,
		"26a55539-b1bb-4ebf-b781-54c5ba3bf72":  false,
		"../26a55539-b1bb-4ebf-b781-54c5ba3bf": false,
		"":                                     false,
	} {
		if storage.IsID(id) != want {
			t.Errorf("IsID(%q) = %v", id, !want)
		}
	}
}

// idsOf lists the ids 0 to n-1, failing at failAt unless it's negative.
func idsOf(n, failAt int) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for i := range n {
			if i == failAt {
				yield("", errors.New("listing failed"))
				return
			}
			if !yield(strconv.Itoa(i), nil) {
				return
			}
		}
	}
}

// headOf finds every id but those ending in 3, deleted mid-scan, and in 7, unreadable.
func headOf(_ context.Context, id string) (storage.Artifact, error) {
	n, _ := strconv.Atoi(id)
	switch n % 10 {
	case 3:
		return storage.Artifact{}, storage.ErrNotFound
	case 7:
		return storage.Artifact{}, errors.New("corrupt metadata")
	}
	return storage.Artifact{ID: id}, nil
}
