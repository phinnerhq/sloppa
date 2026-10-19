// The bootstrap password seeds the first user and resets it when changed, but one left set in the
// environment mustn't undo a password changed from the app since.
package accounts_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/db"
	"github.com/phinnerhq/sloppa/internal/httpapi"
)

var _ httpapi.Accounts = (*accounts.Service)(nil)

// stateDB is a fresh state database. Tests close it themselves, so that inside a synctest bubble
// its connection goroutines end before the bubble does.
func stateDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	return d
}

// boot is a start of the server on d, with pass as BOOTSTRAP_PASSWORD.
func boot(t *testing.T, d *sql.DB, pass string) *accounts.Service {
	t.Helper()
	s, err := accounts.New(t.Context(), d, "admin", config.Secret(pass))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type session struct {
	account accounts.Account
	token   string
}

func signIn(t *testing.T, s *accounts.Service, pass string) (session, error) {
	t.Helper()
	a, token, _, err := s.Login(t.Context(), "admin", pass, "")
	return session{a, token}, err
}

func mustSignIn(t *testing.T, s *accounts.Service, pass string) session {
	t.Helper()
	got, err := signIn(t, s, pass)
	if err != nil {
		t.Fatalf("sign in with %q: %v", pass, err)
	}
	return got
}

func signsIn(t *testing.T, s *accounts.Service, pass string) bool {
	t.Helper()
	_, err := signIn(t, s, pass)
	if err != nil && !api.KindInvalidCredentials.Is(err) {
		t.Fatal(err)
	}
	return err == nil
}

func isSignedIn(t *testing.T, s *accounts.Service, token string) bool {
	t.Helper()
	_, ok, err := s.Authenticate(t.Context(), accounts.KindSession, token)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestKeepsAPasswordChangedInTheAppWhenTheBootstrapOneStaysSet(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	s := boot(t, d, "first-pass")
	here := mustSignIn(t, s, "first-pass")
	if err := s.ChangePassword(t.Context(), here.account, here.token, "first-pass", "second-pass"); err != nil {
		t.Fatal(err)
	}

	s = boot(t, d, "first-pass")
	if !signsIn(t, s, "second-pass") {
		t.Error("the changed password no longer signs in")
	}
	if signsIn(t, s, "first-pass") {
		t.Error("the bootstrap password undid the change")
	}
}

func TestResetsThePasswordSigningBrowsersOutWhenTheBootstrapOneChanges(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	s := boot(t, d, "first-pass")
	here := mustSignIn(t, s, "first-pass")
	if err := s.ChangePassword(t.Context(), here.account, here.token, "first-pass", "second-pass"); err != nil {
		t.Fatal(err)
	}
	agent, err := s.CreateAgent(t.Context(), here.account, "kept")
	if err != nil {
		t.Fatal(err)
	}

	s = boot(t, d, "third-pass")
	if !signsIn(t, s, "third-pass") {
		t.Error("the new bootstrap password doesn't sign in")
	}
	if signsIn(t, s, "second-pass") {
		t.Error("the old password still signs in")
	}
	if isSignedIn(t, s, here.token) {
		t.Error("the browser stayed signed in")
	}
	if _, ok, _ := s.Authenticate(t.Context(), accounts.KindAgent, agent.Token); !ok {
		t.Error("the reset revoked agents too")
	}
}

func TestAdoptsAUserFromBeforeSeedsWereKept(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	boot(t, d, "first-pass")
	if _, err := d.Exec(`UPDATE users SET seed_hash = NULL`); err != nil {
		t.Fatal(err)
	}
	s := boot(t, d, "first-pass")
	if !signsIn(t, s, "first-pass") {
		t.Fatal("the bootstrap password stopped signing in")
	}
	var seeded bool
	if err := d.QueryRow(`SELECT seed_hash = password_hash FROM users`).Scan(&seeded); err != nil || !seeded {
		t.Fatalf("seed not kept: %v", err)
	}
}

func TestRefusesToStartWithNoUsers(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	for _, pass := range []string{"", "some-pass"} {
		user := "admin"
		if pass != "" {
			user = "  "
		}
		_, err := accounts.New(t.Context(), d, user, config.Secret(pass))
		if err == nil || !strings.Contains(err.Error(), "BOOTSTRAP_USERNAME and BOOTSTRAP_PASSWORD") {
			t.Fatalf("New(%q, %q) = %v, want a refusal", user, pass, err)
		}
	}
	// Once a user exists, the bootstrap variables can go.
	boot(t, d, "first-pass")
	if _, err := accounts.New(t.Context(), d, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestSignsOutEveryOtherBrowserOnAChangeAndRefusesAWrongCurrentPassword(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	s := boot(t, d, "first-pass")
	here := mustSignIn(t, s, "first-pass")
	there := mustSignIn(t, s, "first-pass")

	err := s.ChangePassword(t.Context(), here.account, here.token, "not-it", "second-pass")
	if !api.KindInvalidCredentials.Is(err) {
		t.Fatalf("wrong current password: %v", err)
	}
	if !isSignedIn(t, s, there.token) {
		t.Fatal("a refused change signed a browser out")
	}

	if err := s.ChangePassword(t.Context(), here.account, here.token, "first-pass", "second-pass"); err != nil {
		t.Fatal(err)
	}
	if !isSignedIn(t, s, here.token) {
		t.Error("the browser changing the password was signed out")
	}
	if isSignedIn(t, s, there.token) {
		t.Error("another browser stayed signed in")
	}
	if !signsIn(t, s, "second-pass") {
		t.Error("the new password doesn't sign in")
	}
}

func TestLoginIgnoresCaseAndSpaceInTheUsername(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	s := boot(t, d, "first-pass")
	a, token, expires, err := s.Login(t.Context(), "  ADMIN ", "first-pass", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Username != "admin" || a.Kind != accounts.KindSession || a.CredentialID == "" || a.ID == 0 {
		t.Errorf("account = %+v", a)
	}
	if len(token) != 43 {
		t.Errorf("session token %q isn't 32 random bytes", token)
	}
	if d := time.Until(expires); d < accounts.SessionTTL-time.Minute || d > accounts.SessionTTL {
		t.Errorf("session expires in %v", d)
	}
	got, ok, err := s.Authenticate(t.Context(), accounts.KindSession, token)
	if err != nil || !ok || got != a {
		t.Errorf("Authenticate = %+v, %v, %v; want %+v", got, ok, err, a)
	}
	if _, ok, _ := s.Authenticate(t.Context(), accounts.KindAgent, token); ok {
		t.Error("a session token passed as an agent's")
	}
}

func TestLoginRefusesUnknownUsers(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	s := boot(t, d, "first-pass")
	_, _, _, err := s.Login(t.Context(), "nobody", "first-pass", "")
	if !api.KindInvalidCredentials.Is(err) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestLoginIsThrottled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := stateDB(t)
		defer d.Close()
		s := boot(t, d, "first-pass")
		ctx := t.Context()
		for i := range 5 {
			if _, _, _, err := s.Login(ctx, "admin", "wrong", "10.0.0.1"); !api.KindInvalidCredentials.Is(err) {
				t.Fatalf("attempt %d: %v", i+1, err)
			}
		}
		_, _, _, err := s.Login(ctx, "admin", "first-pass", "10.0.0.2")
		var e *api.Error
		if !errors.As(err, &e) || e.Code != "too_many_attempts" || e.RetryAfter != 1 || e.Status != 429 {
			t.Fatalf("sixth attempt: %#v", err)
		}
		// Another username from the same address waits too.
		if _, _, _, err := s.Login(ctx, "someone", "x", "10.0.0.1"); !api.KindTooManyAttempts.Is(err) {
			t.Fatalf("same address: %v", err)
		}

		time.Sleep(time.Second)
		if _, _, _, err := s.Login(ctx, "admin", "first-pass", "10.0.0.2"); err != nil {
			t.Fatalf("after the wait: %v", err)
		}
		// A success forgets the failures.
		if _, _, _, err := s.Login(ctx, "admin", "wrong", "10.0.0.2"); !api.KindInvalidCredentials.Is(err) {
			t.Fatalf("after a success: %v", err)
		}
	})
}

// Anyone with an IPv6 /64 has the whole block to rotate through, so it's one client to the throttle.
func TestLoginThrottlesIPv6ClientsBy64(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := stateDB(t)
		defer d.Close()
		s := boot(t, d, "first-pass")
		for i := range 5 {
			_, _, _, err := s.Login(t.Context(), fmt.Sprintf("user%d", i), "x", fmt.Sprintf("2001:db8::%x", i+1))
			if !api.KindInvalidCredentials.Is(err) {
				t.Fatalf("attempt %d: %v", i+1, err)
			}
		}
		if _, _, _, err := s.Login(t.Context(), "user5", "x", "2001:db8::ffff:6"); !api.KindTooManyAttempts.Is(err) {
			t.Fatalf("another address in the /64: %v", err)
		}
		if _, _, _, err := s.Login(t.Context(), "user6", "x", "2001:db8:0:1::1"); !api.KindInvalidCredentials.Is(err) {
			t.Fatalf("another /64: %v", err)
		}
	})
}

// A flood of sign-ins from fresh addresses and usernames gets past every throttle, but hashing has
// a budget: past it they're refused at once, rather than each taking 64 MiB.
func TestLoginRefusesAFloodPastTheHashingBudget(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	s := boot(t, d, "first-pass")
	const n = 64
	errs := make(chan error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			_, _, _, err := s.Login(t.Context(), fmt.Sprintf("user%d", i), "x", fmt.Sprintf("10.0.%d.1", i))
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	busy := 0
	for err := range errs {
		var e *api.Error
		switch {
		case api.KindInvalidCredentials.Is(err):
		case errors.As(err, &e) && e.Code == "too_many_attempts" && e.RetryAfter == 1:
			busy++
		default:
			t.Fatalf("unexpected: %v", err)
		}
	}
	if busy == 0 {
		t.Fatal("every sign-in of the flood was hashed")
	}
}

func TestChangePasswordIsThrottled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := stateDB(t)
		defer d.Close()
		s := boot(t, d, "first-pass")
		here := mustSignIn(t, s, "first-pass")
		for range 5 {
			s.ChangePassword(t.Context(), here.account, here.token, "wrong", "second-pass")
		}
		err := s.ChangePassword(t.Context(), here.account, here.token, "first-pass", "second-pass")
		if !api.KindTooManyAttempts.Is(err) {
			t.Fatalf("sixth change: %v", err)
		}
	})
}

func TestSessionsExpire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := stateDB(t)
		defer d.Close()
		s := boot(t, d, "first-pass")
		here := mustSignIn(t, s, "first-pass")
		time.Sleep(accounts.SessionTTL - time.Second)
		if !isSignedIn(t, s, here.token) {
			t.Fatal("signed out early")
		}
		time.Sleep(time.Second)
		if isSignedIn(t, s, here.token) {
			t.Fatal("still signed in after 30 days")
		}
		// Issuing anything sweeps the expired session out.
		mustSignIn(t, s, "first-pass")
		var n int
		d.QueryRow(`SELECT COUNT(*) FROM credentials WHERE kind = 'session'`).Scan(&n)
		if n != 1 {
			t.Fatalf("%d sessions stored, want only the new one", n)
		}
	})
}

func TestLogout(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	s := boot(t, d, "first-pass")
	here := mustSignIn(t, s, "first-pass")
	if err := s.Logout(t.Context(), here.token); err != nil {
		t.Fatal(err)
	}
	if isSignedIn(t, s, here.token) {
		t.Fatal("still signed in")
	}
}

func TestAgents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := stateDB(t)
		defer d.Close()
		s := boot(t, d, "first-pass")
		ctx := context.Background()
		me := mustSignIn(t, s, "first-pass").account

		if agents, err := s.Agents(ctx, me); err != nil || agents == nil || len(agents) != 0 {
			t.Fatalf("no agents yet: %#v, %v", agents, err)
		}
		first, err := s.CreateAgent(ctx, me, "first")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(first.Token, api.AgentTokenPrefix) || first.Agent.Name != "first" || first.Agent.LastUsedAt != nil {
			t.Fatalf("created %+v", first)
		}
		time.Sleep(time.Second)
		second, _ := s.CreateAgent(ctx, me, "second")

		time.Sleep(time.Second)
		a, ok, err := s.Authenticate(ctx, accounts.KindAgent, first.Token)
		if err != nil || !ok {
			t.Fatalf("agent token refused: %v", err)
		}
		if a.Kind != accounts.KindAgent || a.CredentialID != first.Agent.ID || a.ID != me.ID || a.Username != "admin" {
			t.Fatalf("agent account = %+v", a)
		}
		used := api.Time(time.Now())

		agents, _ := s.Agents(ctx, me)
		if len(agents) != 2 || agents[0].ID != second.Agent.ID || agents[1].ID != first.Agent.ID {
			t.Fatalf("agents, newest first: %+v", agents)
		}
		if agents[1].LastUsedAt == nil || !agents[1].LastUsedAt.Equal(used) || agents[0].LastUsedAt != nil {
			t.Fatalf("last used: %+v", agents)
		}
		if !agents[1].CreatedAt.Equal(first.Agent.CreatedAt) {
			t.Fatalf("created at %v, listed as %v", first.Agent.CreatedAt, agents[1].CreatedAt)
		}

		if err := s.RenameAgent(ctx, me, first.Agent.ID, "renamed"); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeAgent(ctx, me, second.Agent.ID); err != nil {
			t.Fatal(err)
		}
		agents, _ = s.Agents(ctx, me)
		if len(agents) != 1 || agents[0].Name != "renamed" {
			t.Fatalf("after rename and revoke: %+v", agents)
		}
		if _, ok, _ := s.Authenticate(ctx, accounts.KindAgent, second.Token); ok {
			t.Fatal("a revoked token still works")
		}
		// Someone else's id changes nothing.
		other := accounts.Account{ID: me.ID + 1}
		s.RevokeAgent(ctx, other, first.Agent.ID)
		s.RenameAgent(ctx, other, first.Agent.ID, "hijacked")
		if agents, _ = s.Agents(ctx, me); len(agents) != 1 || agents[0].Name != "renamed" {
			t.Fatalf("another account changed an agent: %+v", agents)
		}
	})
}

func TestSetupLinks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := stateDB(t)
		defer d.Close()
		s := boot(t, d, "first-pass")
		ctx := context.Background()
		me := mustSignIn(t, s, "first-pass").account

		id, code, expires, err := s.CreateSetup(ctx, me)
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 32 || id == "" || !expires.Equal(api.Time(time.Now().Add(accounts.SetupTTL))) {
			t.Fatalf("setup %q %q %v", id, code, expires)
		}
		if got, ok, err := s.SetupExpiry(ctx, code); err != nil || !ok || !got.Equal(expires) {
			t.Fatalf("SetupExpiry = %v, %v, %v", got, ok, err)
		}
		// A setup code is no session or agent token.
		for _, kind := range []string{accounts.KindSession, accounts.KindAgent, accounts.KindSetup} {
			if _, ok, _ := s.Authenticate(ctx, kind, code); ok {
				t.Fatalf("a setup code authenticated as %s", kind)
			}
		}

		first, ok, err := s.RedeemSetup(ctx, code, "Codex on laptop")
		if err != nil || !ok || !strings.HasPrefix(first, api.AgentTokenPrefix) {
			t.Fatalf("redeem: %q %v %v", first, ok, err)
		}
		other, _, _ := s.RedeemSetup(ctx, code, "Claude Code on laptop")
		// Setting the same app up again replaces its token.
		again, _, _ := s.RedeemSetup(ctx, code, "Codex on laptop")
		if _, ok, _ := s.Authenticate(ctx, accounts.KindAgent, first); ok {
			t.Error("the replaced token still works")
		}
		for _, token := range []string{other, again} {
			if _, ok, _ := s.Authenticate(ctx, accounts.KindAgent, token); !ok {
				t.Error("a redeemed token doesn't work")
			}
		}
		if agents, _ := s.Agents(ctx, me); len(agents) != 2 {
			t.Errorf("agents: %+v", agents)
		}

		time.Sleep(accounts.SetupTTL)
		if _, ok, _ := s.RedeemSetup(ctx, code, "late"); ok {
			t.Error("redeemed an expired link")
		}
		if _, ok, _ := s.SetupExpiry(ctx, code); ok {
			t.Error("an expired link has an expiry")
		}
		if _, ok, _ := s.Authenticate(ctx, accounts.KindAgent, again); !ok {
			t.Error("agent tokens expired with their link")
		}
	})
}

func TestRevokeSetup(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	s := boot(t, d, "first-pass")
	ctx := t.Context()
	me := mustSignIn(t, s, "first-pass").account

	_, code, _, _ := s.CreateSetup(ctx, me)
	if revoked, err := s.RevokeSetup(ctx, code); err != nil || !revoked {
		t.Fatalf("RevokeSetup = %v, %v", revoked, err)
	}
	if revoked, _ := s.RevokeSetup(ctx, code); revoked {
		t.Fatal("revoked twice")
	}
	if _, ok, _ := s.RedeemSetup(ctx, code, "x"); ok {
		t.Fatal("redeemed a revoked link")
	}

	id, code, _, _ := s.CreateSetup(ctx, me)
	s.RevokeSetupByID(ctx, accounts.Account{ID: me.ID + 1}, id)
	if _, ok, _ := s.SetupExpiry(ctx, code); !ok {
		t.Fatal("another account revoked the link")
	}
	if err := s.RevokeSetupByID(ctx, me, id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.SetupExpiry(ctx, code); ok {
		t.Fatal("the link survived its revocation")
	}
}

func TestOnlyHashesAreStored(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	s := boot(t, d, "first-pass")
	here := mustSignIn(t, s, "first-pass")
	agent, _ := s.CreateAgent(t.Context(), here.account, "a")
	rows, err := d.Query(`SELECT hash FROM credentials`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		rows.Scan(&hash)
		if hash == here.token || hash == agent.Token || len(hash) != 64 {
			t.Fatalf("stored %q", hash)
		}
	}
}
