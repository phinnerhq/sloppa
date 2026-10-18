package storage

import (
	"context"
	"errors"
	"io/fs"
	"iter"
	"log/slog"
	"math"
	"math/rand/v2"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsID tells whether id is a UUID, the only kind of id storage hands out. Backends refuse any other,
// so an id never names a path or key outside their own.
func IsID(id string) bool { return uuidPattern.MatchString(id) }

var rangePattern = regexp.MustCompile(`^bytes=(\d*)-(\d*)$`)

// ParseRange reads `bytes=a-b`, `bytes=a-` or `bytes=-n` against a file of size bytes. Anything else,
// or a range it can't satisfy, is nil: the whole file is served instead.
func ParseRange(header string, size int64) *Range {
	m := rangePattern.FindStringSubmatch(strings.TrimSpace(header))
	if m == nil || m[1] == "" && m[2] == "" {
		return nil
	}
	var start, end int64
	if m[1] == "" {
		start, end = max(0, size-number(m[2])), size-1
	} else {
		start, end = number(m[1]), size-1
		if m[2] != "" {
			end = min(number(m[2]), size-1)
		}
	}
	// The end is clamped to the last byte, so a start past it (or past an empty file) fails this too.
	if start > end {
		return nil
	}
	return &Range{Start: start, End: end}
}

// number is a run of digits, saturating rather than failing: a range past any file is still one.
func number(digits string) int64 {
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return math.MaxInt64
	}
	return n
}

// scanConcurrency is how many artifacts a scan heads at once.
const scanConcurrency = 16

// HeadAll heads every id that ids yields, a few at a time, for a backend's Scan. An artifact gone by
// the time it's headed is skipped quietly, one that can't be read is skipped with a warning, so a
// single bad file doesn't keep the rest from being indexed. An error from ids ends the scan. The
// order artifacts come out in is not the order of ids.
func HeadAll(ctx context.Context, ids iter.Seq2[string, error], head func(context.Context, string) (Artifact, error)) iter.Seq2[Artifact, error] {
	return func(yield func(Artifact, error) bool) {
		parent := ctx
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		type result struct {
			artifact Artifact
			err      error
		}
		results := make(chan result)
		send := func(r result) {
			select {
			case results <- r:
			case <-ctx.Done():
			}
		}
		go func() {
			var wg sync.WaitGroup
			defer func() {
				wg.Wait()
				close(results)
			}()
			slots := make(chan struct{}, scanConcurrency)
			for id, err := range ids {
				if err != nil {
					send(result{err: err})
					return
				}
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				wg.Go(func() {
					defer func() { <-slots }()
					a, err := head(ctx, id)
					switch {
					case errors.Is(err, ErrNotFound) || ctx.Err() != nil:
					case err != nil:
						slog.Warn("Skipped an unreadable artifact", "id", id, "err", err)
					default:
						send(result{artifact: a})
					}
				})
			}
		}()
		for r := range results {
			if !yield(r.artifact, r.err) || r.err != nil {
				cancel()
				for range results {
				}
				return
			}
		}
		// Heads cut off by the caller's context were skipped: a scan that stopped short must say so.
		if err := parent.Err(); err != nil {
			yield(Artifact{}, err)
		}
	}
}

// CreateTemp is os.CreateTemp within root: a new file named prefix, something random and ".tmp".
// It returns the file's name in root, to rename it by.
func CreateTemp(root *os.Root, prefix string) (*os.File, string, error) {
	for {
		name := prefix + strconv.FormatUint(rand.Uint64(), 36) + ".tmp"
		f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			return f, name, err
		}
	}
}
