package preview

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// row is a preview as the `previews` table keeps it.
type row struct {
	id    string
	label string
	name  string
	user  int64
	// credential is the session or agent token that started the preview: revoking it deletes the row.
	credential   string
	passwordHash string
	port         int
	command      string
	cwd          string
	hostname     string
	created      time.Time
	expires      time.Time
}

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const columns = "id, label, name, user_id, credential_id, password_hash, port, command, cwd, hostname, created_ms, expires_ms"

func scan(s interface{ Scan(...any) error }) (row, error) {
	var r row
	var name, password, command, cwd, hostname sql.NullString
	var created, expires int64
	err := s.Scan(&r.id, &r.label, &name, &r.user, &r.credential, &password, &r.port, &command, &cwd, &hostname, &created, &expires)
	r.name, r.passwordHash, r.command, r.cwd, r.hostname = name.String, password.String, command.String, cwd.String, hostname.String
	r.created, r.expires = time.UnixMilli(created), time.UnixMilli(expires)
	return r, err
}

func nullable(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// liveRows is every preview that hasn't expired.
func liveRows(ctx context.Context, q querier, now time.Time) ([]row, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+columns+" FROM previews WHERE expires_ms > ?", now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []row
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	return all, rows.Err()
}

func findRow(ctx context.Context, q querier, where string, args ...any) (row, bool, error) {
	r, err := scan(q.QueryRowContext(ctx, "SELECT "+columns+" FROM previews WHERE "+where+" LIMIT 1", args...))
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, false, nil
	}
	return r, err == nil, err
}

func insertRow(ctx context.Context, q querier, r row) error {
	_, err := q.ExecContext(ctx, "INSERT INTO previews ("+columns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		r.id, r.label, nullable(r.name), r.user, r.credential, nullable(r.passwordHash), r.port,
		nullable(r.command), nullable(r.cwd), nullable(r.hostname), r.created.UnixMilli(), r.expires.UnixMilli())
	return err
}

func deleteRow(ctx context.Context, q querier, id string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM previews WHERE id = ?", id)
	return err
}

func deleteExpired(ctx context.Context, q querier, now time.Time) error {
	_, err := q.ExecContext(ctx, "DELETE FROM previews WHERE expires_ms <= ?", now.UnixMilli())
	return err
}

// liveCredentials is which of ids are still valid credentials: not revoked, not expired.
func liveCredentials(ctx context.Context, q querier, ids []string, now time.Time) (map[string]bool, error) {
	live := map[string]bool{}
	if len(ids) == 0 {
		return live, nil
	}
	args := []any{now.UnixMilli()}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, "SELECT id FROM credentials WHERE (expires_ms IS NULL OR expires_ms > ?) AND id IN (?"+strings.Repeat(", ?", len(ids)-1)+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[id] = true
	}
	return live, rows.Err()
}
