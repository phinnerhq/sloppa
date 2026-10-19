// Package db opens sloppa's SQLite databases: the state (accounts and previews), which is the one
// file to back up, and the artifact index, a copy of what storage holds that can always be rebuilt.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens a SQLite file for one process: WAL, foreign keys, and a single connection, since SQLite
// takes one writer at a time anyway. Never query the *sql.DB from inside one of its transactions:
// with one connection, that waits forever.
func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// Kept open: an in-memory database is gone with its last connection.
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return db, nil
}

// OpenMemory is a private in-memory database, for tests.
func OpenMemory() (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)
	return db, db.Ping()
}

// OpenState opens DATA_DIR/accounts.sqlite (the name the TypeScript server gave it, so its database
// carries over) and brings its schema up to date.
func OpenState(ctx context.Context, dataDir string) (*sql.DB, error) {
	db, err := Open(filepath.Join(dataDir, "accounts.sqlite"))
	if err != nil {
		return nil, err
	}
	if err := Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrator runs the state migrations in migrations/ on db.
func migrator(db *sql.DB) (*goose.Provider, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	return goose.NewProvider(goose.DialectSQLite3, db, sub)
}

// Migrate applies the state migrations in migrations/.
func Migrate(ctx context.Context, db *sql.DB) error {
	provider, err := migrator(db)
	if err != nil {
		return err
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate the state database: %w", err)
	}
	for _, r := range results {
		slog.Info("Migrated the state database", "version", r.Source.Version, "file", filepath.Base(r.Source.Path))
	}
	return nil
}
