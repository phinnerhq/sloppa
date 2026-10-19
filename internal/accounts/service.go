// Package accounts is users, and everything that signs in as one: browser sessions, the machines
// the CLI is set up on with their tokens, and the setup links that register machines. Unlike the artifact index this isn't derived from
// storage, so the state database is the one file to keep. The first user comes from
// BOOTSTRAP_USERNAME and BOOTSTRAP_PASSWORD, which can go once it exists.
package accounts

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/password"
)

const (
	SessionTTL = 30 * 24 * time.Hour
	SetupTTL   = 30 * time.Minute
)

// Service is the accounts in the state database.
type Service struct {
	db       *sql.DB
	throttle *throttle
	// decoy is checked against for unknown usernames, so timing doesn't tell which ones exist.
	decoy string
}

// New opens the accounts in db, a state database already migrated, creating the bootstrap user or
// resetting its password first. It refuses to start with no user at all, since nobody could sign
// in. A blank username or password leaves the users as they are.
func New(ctx context.Context, db *sql.DB, bootstrapUsername string, bootstrapPassword config.Secret) (*Service, error) {
	username := strings.TrimSpace(bootstrapUsername)
	if username != "" && bootstrapPassword.Value() != "" {
		if err := bootstrap(ctx, db, username, bootstrapPassword.Value()); err != nil {
			return nil, err
		}
	}
	var users int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		return nil, fmt.Errorf("count users: %w", err)
	}
	if users == 0 {
		return nil, errors.New("no users yet: set BOOTSTRAP_USERNAME and BOOTSTRAP_PASSWORD to create the first one")
	}
	decoy, err := password.Hash(ctx, secret(16))
	if err != nil {
		return nil, err
	}
	return &Service{db: db, throttle: newThrottle(), decoy: decoy}, nil
}

func bootstrap(ctx context.Context, db *sql.DB, username, pass string) error {
	const unset = "BOOTSTRAP_PASSWORD can go now: change the password from the account menu"
	user, found, err := findUser(ctx, db, username)
	if err != nil {
		return err
	}
	if !found {
		hash, err := password.Hash(ctx, pass)
		if err != nil {
			return err
		}
		_, err = db.ExecContext(ctx, `INSERT INTO users (username, password_hash, seed_hash, created_ms) VALUES (?, ?, ?, ?)`,
			username, hash, hash, time.Now().UnixMilli())
		if err != nil {
			return fmt.Errorf("create the bootstrap user: %w", err)
		}
		slog.Info(fmt.Sprintf("Created the user %s. %s", username, unset))
		return nil
	}
	// Users from before seeds were kept were last given the bootstrap password itself.
	seed := user.passwordHash
	if user.seedHash.Valid {
		seed = user.seedHash.String
	}
	same, err := password.Verify(ctx, pass, seed)
	if err != nil {
		return err
	}
	if same {
		if _, err := db.ExecContext(ctx, `UPDATE users SET seed_hash = ? WHERE id = ?`, seed, user.id); err != nil {
			return fmt.Errorf("keep the bootstrap seed: %w", err)
		}
		slog.Info("BOOTSTRAP_PASSWORD is still set but only matters when it changes, to reset the password: it can go")
		return nil
	}
	// A new bootstrap password is how one gets reset: whoever knew the old one is signed out.
	hash, err := password.Hash(ctx, pass)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, seed_hash = ? WHERE id = ?`, hash, hash, user.id); err != nil {
		return fmt.Errorf("reset the bootstrap password: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE user_id = ? AND kind = 'session'`, user.id); err != nil {
		return fmt.Errorf("sign out the bootstrap user: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	slog.Info(fmt.Sprintf("Reset the password of %s. %s", username, unset))
	return nil
}

type userRow struct {
	id           int64
	username     string
	passwordHash string
	seedHash     sql.NullString
}

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// findUser looks username up case-insensitively, as the column is COLLATE NOCASE.
func findUser(ctx context.Context, q querier, username string) (userRow, bool, error) {
	var u userRow
	err := q.QueryRowContext(ctx, `SELECT id, username, password_hash, seed_hash FROM users WHERE username = ?`, username).
		Scan(&u.id, &u.username, &u.passwordHash, &u.seedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return u, false, nil
	}
	if err != nil {
		return u, false, fmt.Errorf("find user: %w", err)
	}
	return u, true, nil
}

func secret(bytes int) string {
	b := make([]byte, bytes)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashOf is what's stored of a token: a SHA-256 is enough for random secrets this long, and a
// lookup by it stays an index lookup.
func hashOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func machineToken() string { return api.TokenPrefix + secret(32) }

type issued struct {
	id      string
	created time.Time
	expires time.Time
}

// issue stores a credential for token, sweeping out expired ones on the way. A zero ttl never expires;
// machineID is the machine a machine token belongs to, "" for other kinds.
func issue(ctx context.Context, q querier, kind string, userID int64, token, machineID string, ttl time.Duration) (issued, error) {
	now := api.Time(time.Now())
	out := issued{id: uuid.NewString(), created: now}
	expires := sql.NullInt64{}
	if ttl > 0 {
		out.expires = now.Add(ttl)
		expires = sql.NullInt64{Int64: out.expires.UnixMilli(), Valid: true}
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM credentials WHERE expires_ms <= ?`, now.UnixMilli()); err != nil {
		return out, fmt.Errorf("sweep expired credentials: %w", err)
	}
	_, err := q.ExecContext(ctx, `INSERT INTO credentials (id, hash, kind, user_id, machine_id, created_ms, expires_ms, last_used_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL)`,
		out.id, hashOf(token), kind, userID, nullable(machineID), now.UnixMilli(), expires)
	if err != nil {
		return out, fmt.Errorf("issue a credential: %w", err)
	}
	return out, nil
}

func nullable(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

type holderRow struct {
	credentialID string
	userID       int64
	username     string
	expiresMs    sql.NullInt64
	// machineID and machineName are set for machine tokens.
	machineID, machineName sql.NullString
}

// holder is who holds a live credential of kind, if anyone.
func holder(ctx context.Context, q querier, kind, token string) (holderRow, bool, error) {
	var h holderRow
	if token == "" {
		return h, false, nil
	}
	err := q.QueryRowContext(ctx, `SELECT credentials.id, credentials.user_id, username, expires_ms, machines.id, machines.name
		FROM credentials JOIN users ON users.id = credentials.user_id LEFT JOIN machines ON machines.id = credentials.machine_id
		WHERE hash = ? AND kind = ? AND (expires_ms IS NULL OR expires_ms > ?)`,
		hashOf(token), kind, time.Now().UnixMilli()).
		Scan(&h.credentialID, &h.userID, &h.username, &h.expiresMs, &h.machineID, &h.machineName)
	if errors.Is(err, sql.ErrNoRows) {
		return h, false, nil
	}
	if err != nil {
		return h, false, fmt.Errorf("look a credential up: %w", err)
	}
	return h, true, nil
}

func tooManyAttempts(wait time.Duration) error {
	return api.TooManyAttempts(int(math.Ceil(wait.Seconds())))
}

// verify checks pass against hash for an attempt on keys. When hashing is too busy to, the attempt
// doesn't count against keys, since it never got to guess, and the client is told to retry shortly.
func (s *Service) verify(ctx context.Context, keys []string, pass, hash string) (bool, error) {
	ok, err := password.Verify(ctx, pass, hash)
	if err != nil {
		s.throttle.undo(keys)
		if errors.Is(err, password.ErrBusy) {
			return false, tooManyAttempts(time.Second)
		}
		return false, err
	}
	return ok, nil
}

// Login signs username in, throttled per username and per client address; client may be "".
func (s *Service) Login(ctx context.Context, username, pass, client string) (Account, string, time.Time, error) {
	keys := []string{"user:" + strings.ToLower(strings.TrimSpace(username))}
	if client != "" {
		keys = append(keys, "client:"+clientKey(client))
	}
	if wait := s.throttle.attempt(keys, time.Now()); wait > 0 {
		return Account{}, "", time.Time{}, tooManyAttempts(wait)
	}
	user, found, err := findUser(ctx, s.db, strings.TrimSpace(username))
	if err != nil {
		return Account{}, "", time.Time{}, err
	}
	hash := s.decoy
	if found {
		hash = user.passwordHash
	}
	valid, err := s.verify(ctx, keys, pass, hash)
	if err != nil {
		return Account{}, "", time.Time{}, err
	}
	if !found || !valid {
		return Account{}, "", time.Time{}, api.InvalidCredentials()
	}
	session := secret(32)
	cred, err := s.startSession(ctx, user, session)
	if err != nil {
		return Account{}, "", time.Time{}, err
	}
	s.throttle.succeeded(keys)
	a := Account{ID: user.id, Username: user.username, CredentialID: cred.id, Kind: KindSession}
	return a, session, cred.expires, nil
}

// startSession issues the session token for user, whose password_hash was the one checked. The
// check runs outside any transaction and takes a while, so a password changed meanwhile fails the
// sign-in: a session from the old password would outlive the change meant to end them all.
func (s *Service) startSession(ctx context.Context, user userRow, token string) (issued, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return issued{}, err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, user.id).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return issued{}, api.InvalidCredentials()
	case err != nil:
		return issued{}, fmt.Errorf("recheck the password: %w", err)
	case current != user.passwordHash:
		return issued{}, api.InvalidCredentials()
	}
	cred, err := issue(ctx, tx, KindSession, user.id, token, "", SessionTTL)
	if err != nil {
		return issued{}, err
	}
	return cred, tx.Commit()
}

func (s *Service) Logout(ctx context.Context, session string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE hash = ? AND kind = 'session'`, hashOf(session))
	return err
}

// ChangePassword is throttled like a sign-in, and signs out every other browser; session stays
// signed in.
func (s *Service) ChangePassword(ctx context.Context, a Account, session, current, next string) error {
	keys := []string{"user:" + strings.ToLower(a.Username)}
	if wait := s.throttle.attempt(keys, time.Now()); wait > 0 {
		return tooManyAttempts(wait)
	}
	user, found, err := findUser(ctx, s.db, a.Username)
	if err != nil {
		return err
	}
	if !found {
		return api.InvalidCredentials()
	}
	valid, err := s.verify(ctx, keys, current, user.passwordHash)
	if err != nil {
		return err
	}
	if !valid {
		return api.InvalidCredentials()
	}
	s.throttle.succeeded(keys)
	hash, err := password.Hash(ctx, next)
	if errors.Is(err, password.ErrBusy) {
		return tooManyAttempts(time.Second)
	}
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Only from the password checked: of two changes at once, the second fails like a sign-in would.
	res, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ? AND password_hash = ?`,
		hash, a.ID, user.passwordHash)
	if err != nil {
		return fmt.Errorf("change the password: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return api.InvalidCredentials()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE user_id = ? AND kind = 'session' AND hash != ?`,
		a.ID, hashOf(session)); err != nil {
		return fmt.Errorf("sign out other browsers: %w", err)
	}
	return tx.Commit()
}

// Authenticate is who a session cookie or a machine token belongs to. Machine tokens note when they
// were last used, which is when the dashboard last saw the machine.
func (s *Service) Authenticate(ctx context.Context, kind, token string) (Account, bool, error) {
	if kind != KindSession && kind != KindMachine {
		return Account{}, false, nil
	}
	h, ok, err := holder(ctx, s.db, kind, token)
	if err != nil || !ok {
		return Account{}, false, err
	}
	if kind == KindMachine {
		if err := seen(ctx, s.db, h.credentialID); err != nil {
			return Account{}, false, err
		}
	}
	return Account{
		ID: h.userID, Username: h.username, CredentialID: h.credentialID, Kind: kind,
		MachineID: h.machineID.String, MachineName: h.machineName.String,
	}, true, nil
}

// seen notes a machine token's use.
func seen(ctx context.Context, q querier, credentialID string) error {
	if _, err := q.ExecContext(ctx, `UPDATE credentials SET last_used_ms = ? WHERE id = ?`,
		time.Now().UnixMilli(), credentialID); err != nil {
		return fmt.Errorf("note a machine's use: %w", err)
	}
	return nil
}

// selectMachines is machines with when their token was last used, to be followed by a WHERE and
// GROUP BY machines.id.
const selectMachines = `SELECT machines.id, machines.name, hostname, os, arch, cli_version, machines.created_ms,
	MAX(credentials.last_used_ms) FROM machines LEFT JOIN credentials ON credentials.machine_id = machines.id `

func scanMachine(s interface{ Scan(...any) error }) (api.Machine, error) {
	var (
		m                              api.Machine
		hostname, os, arch, cliVersion sql.NullString
		created                        int64
		lastUsed                       sql.NullInt64
	)
	if err := s.Scan(&m.ID, &m.Name, &hostname, &os, &arch, &cliVersion, &created, &lastUsed); err != nil {
		return m, err
	}
	m.Hostname, m.OS, m.Arch, m.CLIVersion = hostname.String, os.String, arch.String, cliVersion.String
	m.CreatedAt = api.Millis(created)
	if lastUsed.Valid {
		m.LastSeenAt = new(api.Millis(lastUsed.Int64))
	}
	return m, nil
}

// Machines are a's machines, newest first.
func (s *Service) Machines(ctx context.Context, a Account) ([]api.Machine, error) {
	rows, err := s.db.QueryContext(ctx, selectMachines+`WHERE machines.user_id = ?
		GROUP BY machines.id ORDER BY machines.created_ms DESC, machines.rowid DESC`, a.ID)
	if err != nil {
		return nil, fmt.Errorf("list machines: %w", err)
	}
	defer rows.Close()
	machines := []api.Machine{}
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, fmt.Errorf("list machines: %w", err)
		}
		machines = append(machines, m)
	}
	return machines, rows.Err()
}

func findMachine(ctx context.Context, q querier, id string) (api.Machine, error) {
	m, err := scanMachine(q.QueryRowContext(ctx, selectMachines+`WHERE machines.id = ? GROUP BY machines.id`, id))
	if err != nil {
		return m, fmt.Errorf("find a machine: %w", err)
	}
	return m, nil
}

// addMachine registers a machine of userID's, with the facts known about it.
func addMachine(ctx context.Context, q querier, userID int64, name string, facts api.MachineFacts) (string, error) {
	id := uuid.NewString()
	_, err := q.ExecContext(ctx, `INSERT INTO machines (id, user_id, name, hostname, os, arch, cli_version, created_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, userID, name, nullable(facts.Hostname), nullable(facts.OS), nullable(facts.Arch), nullable(facts.CLIVersion),
		time.Now().UnixMilli())
	if err != nil {
		return "", fmt.Errorf("add a machine: %w", err)
	}
	return id, nil
}

// CreateMachine makes a machine and its token by hand, for a script or a computer the setup prompt
// doesn't fit. Nothing is known about it but its name.
func (s *Service) CreateMachine(ctx context.Context, a Account, name string) (api.CreatedMachine, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.CreatedMachine{}, err
	}
	defer tx.Rollback()
	id, err := addMachine(ctx, tx, a.ID, name, api.MachineFacts{})
	if err != nil {
		return api.CreatedMachine{}, err
	}
	token := machineToken()
	if _, err := issue(ctx, tx, KindMachine, a.ID, token, id, 0); err != nil {
		return api.CreatedMachine{}, err
	}
	m, err := findMachine(ctx, tx, id)
	if err != nil {
		return api.CreatedMachine{}, err
	}
	return api.CreatedMachine{Machine: m, Token: token}, tx.Commit()
}

// RevokeMachine deletes one of a's machines, and with it its token and the previews it started.
func (s *Service) RevokeMachine(ctx context.Context, a Account, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM machines WHERE id = ? AND user_id = ?`, id, a.ID)
	return err
}

func (s *Service) RenameMachine(ctx context.Context, a Account, id, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE machines SET name = ? WHERE id = ? AND user_id = ?`, name, id, a.ID)
	return err
}

// CreateSetup makes a link one machine can trade for a token of its own, once, until it expires or
// is revoked.
func (s *Service) CreateSetup(ctx context.Context, a Account) (string, string, time.Time, error) {
	code := secret(24)
	cred, err := issue(ctx, s.db, KindSetup, a.ID, code, "", SetupTTL)
	if err != nil {
		return "", "", time.Time{}, err
	}
	return cred.id, code, cred.expires, nil
}

// RedeemSetup spends a live setup link on a machine with facts, and hands out its token. It's a new
// machine, unless current is the token of a machine of the link's user: that one is re-paired,
// rotating its token and updating its facts, so setting a machine up again keeps it. Rotating the
// token ends the previews it started, as revoking it would: the CLI running them holds the old token
// and couldn't reconnect them anyway. A link holder thus only touches machines whose token they have.
func (s *Service) RedeemSetup(ctx context.Context, code, current string, facts api.MachineFacts) (api.SetupResult, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.SetupResult{}, false, err
	}
	defer tx.Rollback()
	link, ok, err := holder(ctx, tx, KindSetup, code)
	if err != nil || !ok {
		return api.SetupResult{}, false, err
	}
	// Spent on first use: a link seen later, in a chat log or a terminal's scrollback, opens nothing.
	if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE id = ?`, link.credentialID); err != nil {
		return api.SetupResult{}, false, fmt.Errorf("spend the setup link: %w", err)
	}
	var out api.SetupResult
	machine, ok, err := holder(ctx, tx, KindMachine, current)
	if err != nil {
		return api.SetupResult{}, false, err
	}
	var id string
	if ok && machine.userID == link.userID && machine.machineID.Valid {
		id, out.Repaired = machine.machineID.String, true
		if _, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE machine_id = ?`, id); err != nil {
			return api.SetupResult{}, false, fmt.Errorf("revoke the machine's old token: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE machines SET hostname = ?, os = ?, arch = ?, cli_version = ? WHERE id = ?`,
			nullable(facts.Hostname), nullable(facts.OS), nullable(facts.Arch), nullable(facts.CLIVersion), id); err != nil {
			return api.SetupResult{}, false, fmt.Errorf("update the machine: %w", err)
		}
	} else if id, err = addMachine(ctx, tx, link.userID, cmp.Or(facts.Hostname, "machine"), facts); err != nil {
		return api.SetupResult{}, false, err
	}
	out.Token = machineToken()
	cred, err := issue(ctx, tx, KindMachine, link.userID, out.Token, id, 0)
	if err != nil {
		return api.SetupResult{}, false, err
	}
	// The machine was here just now.
	if err := seen(ctx, tx, cred.id); err != nil {
		return api.SetupResult{}, false, err
	}
	if out.Machine, err = findMachine(ctx, tx, id); err != nil {
		return api.SetupResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return api.SetupResult{}, false, err
	}
	return out, true, nil
}

// SetupExpiry is when a live setup link stops working; it redeems nothing.
func (s *Service) SetupExpiry(ctx context.Context, code string) (time.Time, bool, error) {
	link, ok, err := holder(ctx, s.db, KindSetup, code)
	if err != nil || !ok {
		return time.Time{}, false, err
	}
	return api.Millis(link.expiresMs.Int64), true, nil
}

// RevokeSetup is false when there was no such link left.
func (s *Service) RevokeSetup(ctx context.Context, code string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE hash = ? AND kind = 'setup'`, hashOf(code))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Service) RevokeSetupByID(ctx context.Context, a Account, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM credentials WHERE id = ? AND user_id = ? AND kind = 'setup'`, id, a.ID)
	return err
}
