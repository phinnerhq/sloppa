package db

import (
	"context"
	"path/filepath"
	"reflect"
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
	if _, err := db.Exec(`INSERT INTO credentials (id, hash, kind, user_id, created_ms) VALUES ('c', 'h', 'machine', 1, 1)`); err != nil {
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

// Agent tokens from before machines become machines of their own, still signed in, with the
// previews they started.
func TestMigrateAgentsToMachines(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "accounts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider, err := migrator(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, username, password_hash, created_ms) VALUES (1, 'a', 'x', 1)`,
		`INSERT INTO credentials (id, hash, kind, user_id, name, created_ms, last_used_ms) VALUES
			('laptop', 'h1', 'agent', 1, 'Codex on laptop', 10, 20),
			('unnamed', 'h2', 'agent', 1, NULL, 11, NULL),
			('orphan', 'h3', 'agent', 99, 'gone', 12, NULL),
			('browser', 'h4', 'session', 1, NULL, 13, NULL),
			('link', 'h5', 'setup', 1, NULL, 14, NULL)`,
		`INSERT INTO previews (id, label, user_id, credential_id, port, created_ms, expires_ms)
			VALUES ('abcdefgh', 'abcdefgh', 1, 'laptop', 3000, 1, 2)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	type machine struct {
		id, name, kind, machineID string
		created                   int64
	}
	rows, err := db.Query(`SELECT machines.id, machines.name, kind, machine_id, machines.created_ms FROM machines
		JOIN credentials ON credentials.machine_id = machines.id ORDER BY machines.id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []machine
	for rows.Next() {
		var m machine
		if err := rows.Scan(&m.id, &m.name, &m.kind, &m.machineID, &m.created); err != nil {
			t.Fatal(err)
		}
		got = append(got, m)
	}
	rows.Close()
	want := []machine{{"laptop", "Codex on laptop", "machine", "laptop", 10}, {"unnamed", "machine", "machine", "unnamed", 11}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("machines %+v, want %+v", got, want)
	}
	var kinds string
	db.QueryRow(`SELECT GROUP_CONCAT(id || ':' || kind, ' ') FROM (SELECT id, kind FROM credentials ORDER BY id)`).Scan(&kinds)
	if kinds != "browser:session laptop:machine link:setup unnamed:machine" {
		t.Errorf("credentials %s", kinds)
	}

	// Deleting a machine revokes its token, which ends its previews.
	if _, err := db.Exec(`DELETE FROM machines WHERE id = 'laptop'`); err != nil {
		t.Fatal(err)
	}
	var left int
	db.QueryRow(`SELECT (SELECT COUNT(*) FROM previews) + (SELECT COUNT(*) FROM credentials WHERE id = 'laptop')`).Scan(&left)
	if left != 0 {
		t.Errorf("%d rows outlived the machine", left)
	}

	// And down again: the agents are back, under their names.
	if _, err := provider.DownTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM credentials WHERE id = 'unnamed' AND kind = 'agent'`).Scan(&name); err != nil || name != "machine" {
		t.Errorf("down: %q %v", name, err)
	}
}
