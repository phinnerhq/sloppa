package artifacts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/storage"
)

type Options struct {
	// DataDir holds the index and the thumbnails, both derived from Store and safe to delete.
	DataDir string
	// PublicURL is where browsers reach the app, without a trailing slash: links are made from it.
	PublicURL string
	Store     storage.Store
}

// Service is everything one can do with artifacts, shared by the HTTP API and the MCP server.
type Service struct {
	store     storage.Store
	index     *index
	thumbs    *thumbnails
	publicURL string
	now       func() time.Time

	// mutation serializes moves and deletes, which rewrite storage, then the index, and backfills,
	// which read storage, then write the index. One at a time, so a move can't bring back a sidecar a
	// delete just removed, nor leave the index naming the old collection.
	mutation sync.Mutex
}

// Open opens the index under DataDir, rebuilding it from storage when it needs to be.
func Open(ctx context.Context, o Options) (*Service, error) {
	thumbs, err := newThumbnails(filepath.Join(o.DataDir, "thumbs"), o.Store)
	if err != nil {
		return nil, err
	}
	ix, err := openIndex(ctx, filepath.Join(o.DataDir, "index.sqlite"), o.Store)
	if err != nil {
		thumbs.close()
		return nil, err
	}
	return &Service{
		store:     o.Store,
		index:     ix,
		thumbs:    thumbs,
		publicURL: strings.TrimRight(o.PublicURL, "/"),
		now:       time.Now,
	}, nil
}

func (s *Service) Close() error { return errors.Join(s.index.Close(), s.thumbs.close()) }

func (s *Service) present(a storage.Artifact) api.Artifact {
	out := api.Artifact{
		ID:          a.ID,
		Name:        a.Name,
		ContentType: a.ContentType,
		Size:        a.Size,
		CreatedAt:   api.Time(a.CreatedAt),
		Collection:  a.Collection,
		URL:         s.publicURL + api.ViewerPath(a.ID),
		RawURL:      s.publicURL + api.RawPath(a.ID, a.Name),
	}
	if hasThumbnail(a.ContentType) {
		out.ThumbURL = s.publicURL + api.ThumbPath(a.ID)
	}
	return out
}

func (s *Service) presentAll(stored []storage.Artifact) []api.Artifact {
	out := make([]api.Artifact, len(stored))
	for i, a := range stored {
		out[i] = s.present(a)
	}
	return out
}

// storageError is how a storage failure reaches the client.
func storageError(id string, err error) error {
	if errors.Is(err, storage.ErrNotFound) {
		return api.ArtifactNotFound(id)
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return api.StorageError(err.Error())
}

// collectionName is a collection as given, trimmed, or a BadRequest saying what's wrong with it.
func collectionName(collection string) (string, error) {
	collection = strings.TrimSpace(collection)
	if collection == "" {
		return "", api.BadRequest("collection: expected a name, got nothing")
	}
	// Counted as JavaScript does, in UTF-16 code units, so the limit is the dashboard's.
	if n := len(utf16.Encode([]rune(collection))); n > api.CollectionMax {
		return "", api.BadRequest(fmt.Sprintf("collection: expected at most %d characters, got %d", api.CollectionMax, n))
	}
	return collection, nil
}

func (s *Service) List(ctx context.Context, q ListQuery) (api.ArtifactPage, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = api.PageSize
	}
	items, next, err := s.index.list(ctx, strings.TrimSpace(q.Collection), strings.TrimSpace(q.Q), q.Cursor, limit)
	if err != nil {
		return api.ArtifactPage{}, fmt.Errorf("list artifacts: %w", err)
	}
	return api.ArtifactPage{Items: s.presentAll(items), NextCursor: next}, nil
}

func (s *Service) Mosaic(ctx context.Context, cursor string, limit int) (api.GroupPage, error) {
	if limit <= 0 {
		limit = api.GroupPageSize
	}
	groups, next, err := s.index.groups(ctx, cursor, limit, api.GroupPreviewSize)
	if err != nil {
		return api.GroupPage{}, fmt.Errorf("list groups: %w", err)
	}
	page := api.GroupPage{Items: make([]api.ArtifactGroup, len(groups)), NextCursor: next}
	for i, g := range groups {
		page.Items[i] = api.ArtifactGroup{
			Collection:   g.collection,
			Count:        g.count,
			LastUploadAt: api.Millis(g.lastMs),
			Preview:      s.presentAll(g.preview),
		}
	}
	return page, nil
}

func (s *Service) Collections(ctx context.Context) ([]api.CollectionSummary, error) {
	groups, err := s.index.collections(ctx)
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	out := make([]api.CollectionSummary, len(groups))
	for i, g := range groups {
		out[i] = api.CollectionSummary{Collection: g.collection, Count: g.count, LastUploadAt: api.Millis(g.lastMs)}
	}
	return out, nil
}

func (s *Service) Stats(ctx context.Context) (api.Stats, error) {
	st, err := s.index.stats(ctx, s.now())
	if err != nil {
		return api.Stats{}, fmt.Errorf("count artifacts: %w", err)
	}
	out := api.Stats{Total: st.total, TotalBytes: st.totalBytes, Last24h: st.last24h}
	if st.lastMs.Valid {
		last := api.Millis(st.lastMs.Int64)
		out.LastUploadAt = &last
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, id string) (api.Artifact, error) {
	a, ok, err := s.index.get(ctx, id)
	if err != nil {
		return api.Artifact{}, fmt.Errorf("get %s: %w", id, err)
	}
	if !ok {
		if a, err = s.backfill(ctx, id); err != nil {
			return api.Artifact{}, err
		}
	}
	return s.present(a), nil
}

// backfill indexes an artifact the index doesn't have (yet), looking it up in storage, which is
// authoritative. It takes its turn with moves and deletes: a Head before a delete or a move, indexed
// after it, would bring back an artifact that is gone, or file it under its old collection.
func (s *Service) backfill(ctx context.Context, id string) (storage.Artifact, error) {
	if !storage.IsID(id) {
		return storage.Artifact{}, api.ArtifactNotFound(id)
	}
	s.mutation.Lock()
	defer s.mutation.Unlock()
	// A mutation that came first may have indexed it already, or found it gone.
	a, ok, err := s.index.get(ctx, id)
	if err != nil {
		return storage.Artifact{}, fmt.Errorf("get %s: %w", id, err)
	}
	if ok {
		return a, nil
	}
	if a, err = s.store.Head(ctx, id); err != nil {
		return storage.Artifact{}, storageError(id, err)
	}
	if err := s.index.insert(ctx, a); err != nil {
		return storage.Artifact{}, err
	}
	return a, nil
}

// indexTimeout bounds indexing an upload once storage has it, which the request no longer can.
const indexTimeout = 30 * time.Second

func (s *Service) Upload(ctx context.Context, u Upload) (api.Artifact, error) {
	collection, err := collectionName(u.Collection)
	if err != nil {
		return api.Artifact{}, err
	}
	a, err := s.store.Put(ctx, storage.PutInput{
		Name:        u.Name,
		ContentType: ResolveContentType(u.Name, u.ContentType),
		Body:        u.Body,
		Size:        u.Size,
		Collection:  collection,
	})
	if err != nil {
		return api.Artifact{}, api.StorageError(err.Error())
	}
	// Stored, the upload is indexed even if the request is gone by now: storage is only scanned when
	// the index is empty, so one never indexed would never be listed. Failing that, it is taken back
	// out of storage, as the client is told it failed.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), indexTimeout)
	defer cancel()
	if err := s.index.insert(ctx, a); err != nil {
		if rmErr := s.store.Remove(ctx, a.ID); rmErr != nil {
			slog.Warn("Couldn't remove an upload that failed to be indexed", "id", a.ID, "err", rmErr)
		}
		return api.Artifact{}, err
	}
	slog.Info("Stored an artifact", "name", a.Name, "id", a.ID, "collection", a.Collection, "size", a.Size)
	return s.present(a), nil
}

// Move files an artifact under another collection. Like Remove, it isn't cut off halfway when the
// request is.
func (s *Service) Move(ctx context.Context, id, collection string) (api.Artifact, error) {
	collection, err := collectionName(collection)
	if err != nil {
		return api.Artifact{}, err
	}
	ctx = context.WithoutCancel(ctx)
	s.mutation.Lock()
	defer s.mutation.Unlock()
	a, err := s.store.SetCollection(ctx, id, collection)
	if err != nil {
		return api.Artifact{}, storageError(id, err)
	}
	if err := s.index.insert(ctx, a); err != nil {
		return api.Artifact{}, err
	}
	return s.present(a), nil
}

// Remove deletes an artifact. It is unindexed once it's gone from storage, or already was; a failed
// delete leaves it listed.
func (s *Service) Remove(ctx context.Context, id string) error {
	ctx = context.WithoutCancel(ctx)
	s.mutation.Lock()
	defer s.mutation.Unlock()
	err := s.store.Remove(ctx, id)
	missing := errors.Is(err, storage.ErrNotFound)
	if err != nil && !missing {
		return storageError(id, err)
	}
	s.thumbs.remove(id)
	if err := s.index.remove(ctx, id); err != nil {
		return err
	}
	if missing {
		return api.ArtifactNotFound(id)
	}
	return nil
}

func (s *Service) Open(ctx context.Context, id, rangeHeader string) (*storage.Object, error) {
	o, err := s.store.Read(ctx, id, rangeHeader)
	if err != nil {
		return nil, storageError(id, err)
	}
	return o, nil
}

func (s *Service) Thumbnail(ctx context.Context, id string) ([]byte, error) {
	return s.thumbs.get(ctx, id)
}
