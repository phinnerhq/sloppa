package accounts

import (
	"errors"
	"testing"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/db"
)

// A sign-in still checking the old password when it changes mustn't get a session: the change
// signed out every other browser, and this one would slip past it.
func TestNoSessionFromAPasswordChangedMidCheck(t *testing.T) {
	d, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := db.Migrate(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	s, err := New(t.Context(), d, "admin", config.Secret("first-pass"))
	if err != nil {
		t.Fatal(err)
	}
	checked, _, err := findUser(t.Context(), d, "admin")
	if err != nil {
		t.Fatal(err)
	}
	a, session, _, err := s.Login(t.Context(), "admin", "first-pass", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(t.Context(), a, session, "first-pass", "second-pass"); err != nil {
		t.Fatal(err)
	}

	_, err = s.startSession(t.Context(), checked, secret(32))
	if e := (*api.Error)(nil); !errors.As(err, &e) || e.Code != "invalid_credentials" {
		t.Fatalf("session from the old password: %v", err)
	}
	var sessions int
	d.QueryRow(`SELECT COUNT(*) FROM credentials WHERE kind = 'session'`).Scan(&sessions)
	if sessions != 1 {
		t.Fatalf("%d sessions, want only the one that changed the password", sessions)
	}
}
