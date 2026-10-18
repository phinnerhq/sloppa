package db

import (
	"context"
	"testing"
)

func TestMigrateFresh(t *testing.T) {
	db, err := OpenState(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, created_ms) VALUES ('a', 'x', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO credentials (id, hash, kind, user_id, created_ms) VALUES ('c', 'h', 'agent', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO previews (id, label, user_id, credential_id, port, created_ms, expires_ms)
		VALUES ('abcdefgh', 'abcdefgh', 1, 'c', 3000, 1, 2)`); err != nil {
		t.Fatal(err)
	}
	// Revoking the token ends its previews.
	if _, err := db.Exec(`DELETE FROM credentials WHERE id = 'c'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM previews`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("previews left: %d, %v", n, err)
	}
}
