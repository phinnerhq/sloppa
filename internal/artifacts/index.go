package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/db"
	"github.com/phinnerhq/sloppa/internal/storage"
)

// schemaVersion is the index's, kept in PRAGMA user_version. The TypeScript server's file has 0. Bump
// it to change the schema: the index is rebuilt from storage, there's nothing to migrate.
const schemaVersion = 1

const schema = `
CREATE TABLE artifacts (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	content_type TEXT NOT NULL,
	size INTEGER NOT NULL,
	collection TEXT NOT NULL,
	created_ms INTEGER NOT NULL
);
CREATE INDEX artifacts_recent ON artifacts (created_ms DESC, id DESC);
CREATE INDEX artifacts_collection ON artifacts (collection, created_ms DESC, id DESC);
`

const columns = "id, name, content_type, size, collection, created_ms"

const upsert = `INSERT INTO artifacts (` + columns + `) VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT (id) DO UPDATE SET name = excluded.name, content_type = excluded.content_type,
	size = excluded.size, collection = excluded.collection, created_ms = excluded.created_ms`

// index is a queryable copy of what storage holds. It is only ever derived from storage: when the file
// is missing, empty or from another schema it is rebuilt with a full scan, so deleting it is always
// safe.
type index struct {
	db *sql.DB
}

func openIndex(ctx context.Context, path string, store storage.Store) (*index, error) {
	sqlite, err := db.Open(path)
	if err != nil {
		return nil, err
	}
	ix := &index{db: sqlite}
	if err := ix.prepare(ctx, store); err != nil {
		sqlite.Close()
		return nil, err
	}
	return ix, nil
}

func (ix *index) prepare(ctx context.Context, store storage.Store) error {
	var version, tables int
	if err := ix.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read the index version: %w", err)
	}
	if err := ix.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table'").Scan(&tables); err != nil {
		return fmt.Errorf("read the index schema: %w", err)
	}
	switch {
	case tables == 0:
		slog.Info("Building the index from storage")
	case version != schemaVersion:
		slog.Info("Index is from another version, rebuilding it from storage", "version", version, "want", schemaVersion)
	default:
		var n int
		if err := ix.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM artifacts").Scan(&n); err != nil {
			return fmt.Errorf("count the index: %w", err)
		}
		if n > 0 {
			return nil
		}
		slog.Info("Index is empty, rebuilding it from storage")
	}
	return ix.rebuild(ctx, store)
}

// rebuild starts the index over from a full scan, all or nothing: a rebuild cut short would leave a
// nonempty index, never rebuilt again.
func (ix *index) rebuild(ctx context.Context, store storage.Store) error {
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Whatever was there goes, the TypeScript server's migrations table included.
	var tables []string
	rows, err := tx.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, table := range tables {
		if _, err := tx.ExecContext(ctx, `DROP TABLE "`+strings.ReplaceAll(table, `"`, `""`)+`"`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create the index: %w", err)
	}
	insert, err := tx.PrepareContext(ctx, upsert)
	if err != nil {
		return err
	}
	defer insert.Close()
	n := 0
	for a, err := range store.Scan(ctx) {
		if err != nil {
			return fmt.Errorf("rebuild the index: %w", err)
		}
		if _, err := insert.ExecContext(ctx, rowOf(a)...); err != nil {
			return fmt.Errorf("rebuild the index: %w", err)
		}
		n++
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info("Indexed the artifacts in storage", "count", n)
	return nil
}

func (ix *index) Close() error { return ix.db.Close() }

func rowOf(a storage.Artifact) []any {
	return []any{a.ID, a.Name, a.ContentType, a.Size, a.Collection, a.CreatedAt.UnixMilli()}
}

func scanArtifacts(rows *sql.Rows) ([]storage.Artifact, error) {
	defer rows.Close()
	artifacts := []storage.Artifact{}
	for rows.Next() {
		var a storage.Artifact
		var ms int64
		if err := rows.Scan(&a.ID, &a.Name, &a.ContentType, &a.Size, &a.Collection, &ms); err != nil {
			return nil, err
		}
		a.CreatedAt = api.Millis(ms)
		artifacts = append(artifacts, a)
	}
	return artifacts, rows.Err()
}

func (ix *index) insert(ctx context.Context, a storage.Artifact) error {
	if _, err := ix.db.ExecContext(ctx, upsert, rowOf(a)...); err != nil {
		return fmt.Errorf("index %s: %w", a.ID, err)
	}
	return nil
}

func (ix *index) remove(ctx context.Context, id string) error {
	if _, err := ix.db.ExecContext(ctx, "DELETE FROM artifacts WHERE id = ?", id); err != nil {
		return fmt.Errorf("unindex %s: %w", id, err)
	}
	return nil
}

func (ix *index) get(ctx context.Context, id string) (a storage.Artifact, ok bool, err error) {
	rows, err := ix.db.QueryContext(ctx, "SELECT "+columns+" FROM artifacts WHERE id = ?", id)
	if err != nil {
		return a, false, err
	}
	found, err := scanArtifacts(rows)
	if err != nil || len(found) == 0 {
		return a, false, err
	}
	return found[0], true, nil
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// list is a page of artifacts, newest first. A search is a plain scan, walking the newest-first index
// and stopping at a page's worth of matches: plenty for tens of thousands of artifacts. LIKE ignores
// ASCII case; `%` and `_` in the search are literal.
func (ix *index) list(ctx context.Context, collection, search, cursor string, limit int) ([]storage.Artifact, string, error) {
	var where []string
	var args []any
	if collection != "" {
		where = append(where, "collection = ?")
		args = append(args, collection)
	}
	if search != "" {
		pattern := "%" + likeEscaper.Replace(search) + "%"
		where = append(where, `(name LIKE ? ESCAPE '\' OR id LIKE ? ESCAPE '\' OR collection LIKE ? ESCAPE '\' OR content_type LIKE ? ESCAPE '\')`)
		args = append(args, pattern, pattern, pattern, pattern)
	}
	if after, ok := decodeCursor[artifactCursor](cursor); ok {
		where = append(where, "(created_ms < ? OR (created_ms = ? AND id < ?))")
		args = append(args, *after.Ms, *after.Ms, *after.ID)
	}
	query := "SELECT " + columns + " FROM artifacts"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	// One extra row tells whether there is a next page.
	query += " ORDER BY created_ms DESC, id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := ix.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	page, err := scanArtifacts(rows)
	if err != nil {
		return nil, "", err
	}
	if len(page) <= limit {
		return page, "", nil
	}
	page = page[:limit]
	last := page[len(page)-1]
	return page, encodeCursor(artifactCursorAt(last.CreatedAt.UnixMilli(), last.ID)), nil
}

type group struct {
	collection string
	count      int
	lastMs     int64
	preview    []storage.Artifact
}

func scanGroups(rows *sql.Rows) ([]group, error) {
	defer rows.Close()
	groups := []group{}
	for rows.Next() {
		var g group
		if err := rows.Scan(&g.collection, &g.count, &g.lastMs); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

// groups is a page of collections, most recently active first, each with its newest few artifacts.
func (ix *index) groups(ctx context.Context, cursor string, limit, previewSize int) ([]group, string, error) {
	query := "SELECT collection, COUNT(*) AS count, MAX(created_ms) AS last_ms FROM artifacts GROUP BY collection"
	var args []any
	if after, ok := decodeCursor[groupCursor](cursor); ok {
		query += " HAVING last_ms < ? OR (last_ms = ? AND collection < ?)"
		args = append(args, *after.Ms, *after.Ms, *after.Collection)
	}
	query += " ORDER BY last_ms DESC, collection DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := ix.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	// Read through before the previews are queried: there's one connection.
	page, err := scanGroups(rows)
	if err != nil {
		return nil, "", err
	}
	var next string
	if len(page) > limit {
		page = page[:limit]
		last := page[len(page)-1]
		next = encodeCursor(groupCursorAt(last.lastMs, last.collection))
	}
	// One small lookup per group, each walking the (collection, created_ms) index: cheaper than
	// ranking the whole table.
	for i := range page {
		rows, err := ix.db.QueryContext(ctx, "SELECT "+columns+" FROM artifacts WHERE collection = ? ORDER BY created_ms DESC, id DESC LIMIT ?", page[i].collection, previewSize)
		if err != nil {
			return nil, "", err
		}
		if page[i].preview, err = scanArtifacts(rows); err != nil {
			return nil, "", err
		}
	}
	return page, next, nil
}

func (ix *index) collections(ctx context.Context) ([]group, error) {
	rows, err := ix.db.QueryContext(ctx, "SELECT collection, COUNT(*) AS count, MAX(created_ms) AS last_ms FROM artifacts GROUP BY collection ORDER BY last_ms DESC, collection DESC")
	if err != nil {
		return nil, err
	}
	return scanGroups(rows)
}

type stats struct {
	total, last24h int
	totalBytes     int64
	lastMs         sql.NullInt64
}

// stats counts what's indexed, and what of it was uploaded in the day before now.
func (ix *index) stats(ctx context.Context, now time.Time) (stats, error) {
	var s stats
	var totalBytes, last24h sql.NullInt64
	err := ix.db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(size), SUM(CASE WHEN created_ms >= ? THEN 1 ELSE 0 END), MAX(created_ms) FROM artifacts`,
		now.Add(-24*time.Hour).UnixMilli()).Scan(&s.total, &totalBytes, &last24h, &s.lastMs)
	s.totalBytes, s.last24h = totalBytes.Int64, int(last24h.Int64)
	return s, err
}
