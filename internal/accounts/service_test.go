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
	machine, err := s.CreateMachine(t.Context(), here.account, "kept")
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
	if _, ok, _ := s.Authenticate(t.Context(), accounts.KindMachine, machine.Token); !ok {
		t.Error("the reset revoked machines too")
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
	if _, ok, _ := s.Authenticate(t.Context(), accounts.KindMachine, token); ok {
		t.Error("a session token passed as a machine's")
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

func TestMachines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := stateDB(t)
		defer d.Close()
		s := boot(t, d, "first-pass")
		ctx := context.Background()
		me := mustSignIn(t, s, "first-pass").account

		if machines, err := s.Machines(ctx, me); err != nil || machines == nil || len(machines) != 0 {
			t.Fatalf("no machines yet: %#v, %v", machines, err)
		}
		first, err := s.CreateMachine(ctx, me, "first")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(first.Token, api.TokenPrefix) || first.Machine.Name != "first" || first.Machine.LastSeenAt != nil ||
			first.Machine.Hostname != "" || !first.Machine.CreatedAt.Equal(api.Time(time.Now())) {
			t.Fatalf("created %+v", first)
		}
		time.Sleep(time.Second)
		second, _ := s.CreateMachine(ctx, me, "second")

		time.Sleep(time.Second)
		a, ok, err := s.Authenticate(ctx, accounts.KindMachine, first.Token)
		if err != nil || !ok {
			t.Fatalf("machine token refused: %v", err)
		}
		if a.Kind != accounts.KindMachine || a.CredentialID == "" || a.MachineID != first.Machine.ID || a.MachineName != "first" ||
			a.ID != me.ID || a.Username != "admin" {
			t.Fatalf("machine account = %+v", a)
		}
		used := api.Time(time.Now())

		machines, _ := s.Machines(ctx, me)
		if len(machines) != 2 || machines[0].ID != second.Machine.ID || machines[1].ID != first.Machine.ID {
			t.Fatalf("machines, newest first: %+v", machines)
		}
		if machines[1].LastSeenAt == nil || !machines[1].LastSeenAt.Equal(used) || machines[0].LastSeenAt != nil {
			t.Fatalf("last seen: %+v", machines)
		}
		if !machines[1].CreatedAt.Equal(first.Machine.CreatedAt) {
			t.Fatalf("created at %v, listed as %v", first.Machine.CreatedAt, machines[1].CreatedAt)
		}

		if err := s.RenameMachine(ctx, me, first.Machine.ID, "renamed"); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeMachine(ctx, me, second.Machine.ID); err != nil {
			t.Fatal(err)
		}
		machines, _ = s.Machines(ctx, me)
		if len(machines) != 1 || machines[0].Name != "renamed" {
			t.Fatalf("after rename and revoke: %+v", machines)
		}
		if _, ok, _ := s.Authenticate(ctx, accounts.KindMachine, second.Token); ok {
			t.Fatal("a revoked machine's token still works")
		}
		// Someone else's id changes nothing.
		other := accounts.Account{ID: me.ID + 1}
		s.RevokeMachine(ctx, other, first.Machine.ID)
		s.RenameMachine(ctx, other, first.Machine.ID, "hijacked")
		if machines, _ = s.Machines(ctx, me); len(machines) != 1 || machines[0].Name != "renamed" {
			t.Fatalf("another account changed a machine: %+v", machines)
		}
	})
}

// startPreview is a preview row started with a's token, as the preview service would add it.
func startPreview(t *testing.T, d *sql.DB, a accounts.Account, id string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO previews (id, label, user_id, credential_id, port, created_ms, expires_ms)
		VALUES (?, ?, ?, ?, 3000, 0, ?)`, id, id, a.ID, a.CredentialID, time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func previewCount(t *testing.T, d *sql.DB) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM previews`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
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
		// A setup code is no session or machine token.
		for _, kind := range []string{accounts.KindSession, accounts.KindMachine, accounts.KindSetup} {
			if _, ok, _ := s.Authenticate(ctx, kind, code); ok {
				t.Fatalf("a setup code authenticated as %s", kind)
			}
		}

		facts := api.MachineFacts{Hostname: "laptop", OS: "linux", Arch: "amd64", CLIVersion: "1.0.0"}
		first, ok, err := s.RedeemSetup(ctx, code, "", facts)
		if err != nil || !ok || !strings.HasPrefix(first.Token, api.TokenPrefix) || first.Repaired {
			t.Fatalf("redeem: %+v %v %v", first, ok, err)
		}
		m := first.Machine
		if m.Name != "laptop" || m.Hostname != "laptop" || m.OS != "linux" || m.Arch != "amd64" || m.CLIVersion != "1.0.0" ||
			m.LastSeenAt == nil || !m.LastSeenAt.Equal(api.Time(time.Now())) {
			t.Fatalf("redeemed machine %+v", m)
		}
		// Spent: not even its own machine gets it again.
		if _, ok, _ := s.RedeemSetup(ctx, code, first.Token, facts); ok {
			t.Fatal("redeemed a link twice")
		}
		if _, ok, _ := s.SetupExpiry(ctx, code); ok {
			t.Error("a spent link has an expiry")
		}

		_, code, _, _ = s.CreateSetup(ctx, me)
		time.Sleep(accounts.SetupTTL)
		if _, ok, _ := s.RedeemSetup(ctx, code, "", facts); ok {
			t.Error("redeemed an expired link")
		}
		if _, ok, _ := s.SetupExpiry(ctx, code); ok {
			t.Error("an expired link has an expiry")
		}
		if _, ok, _ := s.Authenticate(ctx, accounts.KindMachine, first.Token); !ok {
			t.Error("machine tokens expired with their link")
		}
	})
}

func TestSetupRepairsOnlyTheUsersOwnMachines(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	s := boot(t, d, "first-pass")
	ctx := t.Context()
	me := mustSignIn(t, s, "first-pass").account
	redeem := func(owner accounts.Account, current string, facts api.MachineFacts) api.SetupResult {
		t.Helper()
		_, code, _, _ := s.CreateSetup(ctx, owner)
		got, ok, err := s.RedeemSetup(ctx, code, current, facts)
		if err != nil || !ok {
			t.Fatalf("redeem: %v %v", ok, err)
		}
		return got
	}

	first := redeem(me, "", api.MachineFacts{Hostname: "laptop", OS: "linux", Arch: "amd64", CLIVersion: "1.0.0"})
	s.RenameMachine(ctx, me, first.Machine.ID, "work laptop")
	signedIn, _, _ := s.Authenticate(ctx, accounts.KindMachine, first.Token)
	startPreview(t, d, signedIn, "aaaaaaaa")

	again := redeem(me, first.Token, api.MachineFacts{Hostname: "laptop", OS: "linux", Arch: "arm64", CLIVersion: "2.0.0"})
	if !again.Repaired || again.Machine.ID != first.Machine.ID || again.Machine.Name != "work laptop" ||
		again.Machine.Arch != "arm64" || again.Machine.CLIVersion != "2.0.0" || !again.Machine.CreatedAt.Equal(first.Machine.CreatedAt) {
		t.Fatalf("re-paired %+v", again)
	}
	if _, ok, _ := s.Authenticate(ctx, accounts.KindMachine, first.Token); ok {
		t.Error("the old token still works")
	}
	if _, ok, _ := s.Authenticate(ctx, accounts.KindMachine, again.Token); !ok {
		t.Error("the new token doesn't work")
	}
	// The CLI running it held the old token: it's over, as if the token had been revoked.
	if n := previewCount(t, d); n != 0 {
		t.Errorf("%d previews left from the old token", n)
	}

	// Another user's link can't take my machine, even with its token.
	if _, err := d.Exec(`INSERT INTO users (username, password_hash, created_ms) VALUES ('bob', 'x', 0)`); err != nil {
		t.Fatal(err)
	}
	bob := accounts.Account{ID: me.ID + 1}
	theirs := redeem(bob, again.Token, api.MachineFacts{Hostname: "laptop"})
	if theirs.Repaired || theirs.Machine.ID == first.Machine.ID {
		t.Fatalf("bob's link re-paired my machine: %+v", theirs)
	}
	if _, ok, _ := s.Authenticate(ctx, accounts.KindMachine, again.Token); !ok {
		t.Error("bob's link revoked my machine's token")
	}
	if machines, _ := s.Machines(ctx, me); len(machines) != 1 {
		t.Errorf("my machines: %+v", machines)
	}

	// Nothing known about the machine still makes one.
	if blank := redeem(me, "", api.MachineFacts{}); blank.Machine.Name != "machine" || blank.Machine.Hostname != "" {
		t.Errorf("a machine nothing is known about: %+v", blank)
	}
}

func TestRevokingAMachineEndsItsPreviews(t *testing.T) {
	d := stateDB(t)
	defer d.Close()
	s := boot(t, d, "first-pass")
	ctx := t.Context()
	me := mustSignIn(t, s, "first-pass").account
	created, _ := s.CreateMachine(ctx, me, "ci")
	signedIn, _, _ := s.Authenticate(ctx, accounts.KindMachine, created.Token)
	startPreview(t, d, signedIn, "bbbbbbbb")
	if err := s.RevokeMachine(ctx, me, created.Machine.ID); err != nil {
		t.Fatal(err)
	}
	if n := previewCount(t, d); n != 0 {
		t.Errorf("%d previews outlived their machine", n)
	}
	var tokens int
	d.QueryRow(`SELECT COUNT(*) FROM credentials WHERE kind = 'machine'`).Scan(&tokens)
	if tokens != 0 {
		t.Errorf("%d tokens outlived their machine", tokens)
	}
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
	if _, ok, _ := s.RedeemSetup(ctx, code, "", api.MachineFacts{}); ok {
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
	machine, _ := s.CreateMachine(t.Context(), here.account, "a")
	rows, err := d.Query(`SELECT hash FROM credentials`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		rows.Scan(&hash)
		if hash == here.token || hash == machine.Token || len(hash) != 64 {
			t.Fatalf("stored %q", hash)
		}
	}
}
