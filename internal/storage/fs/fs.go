// Package fs keeps artifacts as plain files under a directory: `<uuid>` holds the bytes and
// `<uuid>.json` the metadata. For development and small single-box setups; production sets S3_ENDPOINT.
// The layout is the TypeScript server's, so a directory it wrote reads back as it was.
package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/storage"
)

// Store keeps its files through an os.Root: besides keeping every name inside dir, on Windows its
// files can be deleted or renamed over while open, as a download in progress keeps one.
type Store struct {
	dir  string
	root *os.Root
}

var _ storage.Store = (*Store)(nil)

// New uses dir, making it if needed. Temp files left by writes a crash cut short are removed: nothing
// else writes to dir while it opens.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("make the storage dir: %w", err)
	}
	stale, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open the storage dir: %w", err)
	}
	for _, path := range stale {
		_ = root.Remove(filepath.Base(path))
	}
	return &Store{dir: dir, root: root}, nil
}

// Close lets go of the storage dir. Objects being read stay readable.
func (s *Store) Close() error { return s.root.Close() }

// fileOf and metaOf are the names in the storage dir of an artifact's bytes and sidecar.
func fileOf(id string) string { return id }
func metaOf(id string) string { return id + ".json" }

// sidecar is written next to each file: everything S3 would keep as object metadata.
type sidecar struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	CreatedMs   int64  `json:"createdMs"`
	Collection  string `json:"collection"`
}

// storedSidecar is a sidecar as read back. Pointers tell a missing field from an empty one.
type storedSidecar struct {
	Name        *string  `json:"name"`
	ContentType *string  `json:"contentType"`
	Size        *float64 `json:"size"`
	CreatedMs   *float64 `json:"createdMs"`
	Collection  *string  `json:"collection"`
}

func sidecarOf(a storage.Artifact) sidecar {
	return sidecar{
		Name:        a.Name,
		ContentType: a.ContentType,
		Size:        a.Size,
		CreatedMs:   a.CreatedAt.UnixMilli(),
		Collection:  a.Collection,
	}
}

func decodeSidecar(id string, data []byte) (storage.Artifact, error) {
	var m storedSidecar
	if err := json.Unmarshal(data, &m); err != nil {
		return storage.Artifact{}, fmt.Errorf("decode metadata of %s: %w", id, err)
	}
	if m.Name == nil || m.ContentType == nil || m.Size == nil || m.CreatedMs == nil {
		return storage.Artifact{}, fmt.Errorf("decode metadata of %s: name, contentType, size and createdMs are required", id)
	}
	// Artifacts from before collections have none.
	collection := api.UnknownCollection
	if m.Collection != nil {
		collection = *m.Collection
	}
	return storage.Artifact{
		ID:          id,
		Name:        *m.Name,
		ContentType: *m.ContentType,
		Size:        int64(*m.Size),
		CreatedAt:   api.Millis(int64(*m.CreatedMs)),
		Collection:  collection,
	}, nil
}

func (s *Store) Head(_ context.Context, id string) (storage.Artifact, error) {
	if !storage.IsID(id) {
		return storage.Artifact{}, storage.ErrNotFound
	}
	data, err := s.root.ReadFile(metaOf(id))
	if errors.Is(err, os.ErrNotExist) {
		return storage.Artifact{}, storage.ErrNotFound
	}
	if err != nil {
		return storage.Artifact{}, fmt.Errorf("read metadata: %w", err)
	}
	return decodeSidecar(id, data)
}

// fsync is (*os.File).Sync, swapped by tests to see what is flushed when.
var fsync = (*os.File).Sync

// syncDir flushes the storage dir itself, so the files renamed into it or removed from it stay so
// through a crash. Windows can't flush a directory, nor needs to: NTFS journals renames. Some file
// systems can't either, and say EINVAL.
func (s *Store) syncDir() error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	err = fsync(d)
	if closeErr := d.Close(); err == nil {
		err = closeErr
	}
	if errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}

// writeAside writes a file under a name of its own, then renames it to name, so a reader sees the
// old file or the new one, never half of one. Each write gets its own temp file: two at once (moves
// racing) can't interleave in a shared one. It returns once the file is on disk, under its name: the
// bytes are flushed before the rename, or a crash could leave the name on an empty file.
func (s *Store) writeAside(name string, write func(io.Writer) error) error {
	f, temp, err := storage.CreateTemp(s.root, name+".")
	if err != nil {
		return err
	}
	err = write(f)
	if err == nil {
		err = fsync(f)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = s.root.Rename(temp, name)
	}
	if err != nil {
		_ = s.root.Remove(temp)
		return err
	}
	return s.syncDir()
}

func (s *Store) writeMeta(id string, m sidecar) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := s.writeAside(metaOf(id), func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	}); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}
	return nil
}

func (s *Store) Put(ctx context.Context, in storage.PutInput) (storage.Artifact, error) {
	id := in.ID
	if id == "" {
		id = uuid.NewString()
	} else if !storage.IsID(id) {
		return storage.Artifact{}, fmt.Errorf("write artifact: %q is not a UUID", id)
	}
	createdAt := api.Time(time.Now())
	// Bytes first, metadata last: an artifact exists once its sidecar does. The body is streamed to
	// disk, never buffered whole.
	var size int64
	err := s.writeAside(fileOf(id), func(w io.Writer) error {
		var err error
		size, err = io.Copy(w, contextReader{ctx, in.Body})
		if err == nil && in.Size >= 0 && size != in.Size {
			err = fmt.Errorf("expected %d bytes, got %d", in.Size, size)
		}
		return err
	})
	if err != nil {
		return storage.Artifact{}, fmt.Errorf("write artifact: %w", err)
	}
	a := storage.Artifact{
		ID:          id,
		Name:        in.Name,
		ContentType: in.ContentType,
		Size:        size,
		CreatedAt:   createdAt,
		Collection:  in.Collection,
	}
	if err := s.writeMeta(id, sidecarOf(a)); err != nil {
		_ = s.root.Remove(fileOf(id))
		return storage.Artifact{}, err
	}
	return a, nil
}

func (s *Store) Read(ctx context.Context, id, rangeHeader string) (*storage.Object, error) {
	a, err := s.Head(ctx, id)
	if err != nil {
		return nil, err
	}
	f, err := s.root.Open(fileOf(id))
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	o := &storage.Object{Artifact: a, Body: f, ContentLength: a.Size}
	if r := storage.ParseRange(rangeHeader, a.Size); r != nil {
		o.Range = r
		o.ContentLength = r.End - r.Start + 1
		o.Body = struct {
			io.Reader
			io.Closer
		}{io.NewSectionReader(f, r.Start, o.ContentLength), f}
	}
	return o, nil
}

func (s *Store) Scan(ctx context.Context) iter.Seq2[storage.Artifact, error] {
	ids := func(yield func(string, error) bool) {
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			yield("", fmt.Errorf("list storage dir: %w", err))
			return
		}
		for _, entry := range entries {
			id, ok := strings.CutSuffix(entry.Name(), ".json")
			if ok && storage.IsID(id) && !yield(id, nil) {
				return
			}
		}
	}
	return storage.HeadAll(ctx, ids, s.Head)
}

func (s *Store) Remove(ctx context.Context, id string) error {
	if _, err := s.Head(ctx, id); err != nil {
		return err
	}
	// The sidecar goes first, for good before the bytes do: without it the artifact is gone, even if
	// the bytes linger.
	for _, name := range []string{metaOf(id), fileOf(id)} {
		if err := s.root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete artifact: %w", err)
		}
		if err := s.syncDir(); err != nil {
			return fmt.Errorf("delete artifact: %w", err)
		}
	}
	return nil
}

func (s *Store) SetCollection(ctx context.Context, id, collection string) (storage.Artifact, error) {
	a, err := s.Head(ctx, id)
	if err != nil {
		return storage.Artifact{}, err
	}
	a.Collection = collection
	if err := s.writeMeta(id, sidecarOf(a)); err != nil {
		return storage.Artifact{}, err
	}
	return a, nil
}

// contextReader stops a copy once ctx is done, so an abandoned upload doesn't keep writing.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
