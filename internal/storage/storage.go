// Package storage is where artifacts' bytes live: an S3 bucket in production, plain files under
// DATA_DIR otherwise. Each artifact is keyed by its UUID alone, with its name, collection and upload
// time stored next to it, so storage stays the source of truth and the SQLite index can always be
// rebuilt from it.
package storage

import (
	"context"
	"errors"
	"io"
	"iter"
	"time"
)

// ErrNotFound is returned, wrapped or not, for an id storage doesn't have.
var ErrNotFound = errors.New("artifact not found")

// Artifact is what storage keeps about a file, besides its bytes.
type Artifact struct {
	ID          string
	Name        string
	ContentType string
	Size        int64
	CreatedAt   time.Time
	Collection  string
}

type PutInput struct {
	// ID is a fresh UUID when empty.
	ID          string
	Name        string
	ContentType string
	// Body is read to EOF. Size is how many bytes it holds, or -1 if unknown.
	Body       io.Reader
	Size       int64
	Collection string
}

// Range is a byte range of a file, inclusive, already resolved against its size.
type Range struct {
	Start, End int64
}

// Object is a file being read. Close it.
type Object struct {
	Artifact Artifact
	Body     io.ReadCloser
	// ContentLength is how many bytes Body holds: the range's, when one was asked for.
	ContentLength int64
	// Range is set when a byte range was served.
	Range *Range
}

// Store is what a storage backend does.
type Store interface {
	Put(ctx context.Context, in PutInput) (Artifact, error)
	Head(ctx context.Context, id string) (Artifact, error)
	// Read serves the whole file, or the range in an HTTP Range header when rangeHeader is one it can
	// satisfy.
	Read(ctx context.Context, id string, rangeHeader string) (*Object, error)
	// Scan yields every stored artifact, for rebuilding the index only. An artifact deleted mid-scan
	// is skipped quietly; one that can't be read is skipped with a warning.
	Scan(ctx context.Context) iter.Seq2[Artifact, error]
	Remove(ctx context.Context, id string) error
	// SetCollection files an artifact under another collection; its bytes, id and links stay.
	SetCollection(ctx context.Context, id, collection string) (Artifact, error)
}
