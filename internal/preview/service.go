// Package preview serves dev servers shared with `sloppa preview run` as subdomains of
// PREVIEW_ORIGIN.
//
// A preview is a row in the state database, made by POST /api/previews for the signed-in account.
// The CLI then holds a tunnel to it (see internal/tunnel), reconnecting as often as it needs to,
// and the proxy routes `<label>.<origin>` through whichever tunnel is up. Revoking the token that
// started the preview, stopping it, or letting it expire deletes the row and ends its tunnel with a
// final close code, which tells the CLI to stop its dev server.
//
// What's live (tunnels, request counts, verified passwords) is only in memory: a restart drops the
// tunnels with a service-restart close and the CLIs reconnect to the same rows.
package preview

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"

	"github.com/coder/websocket"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/password"
	"github.com/phinnerhq/sloppa/internal/tunnel"
)

const (
	// reconnectGrace is how long a preview without a tunnel keeps its name from other users: long
	// enough for its CLI to get through a network blip, or this server restarting.
	reconnectGrace = time.Minute
	// sweepEvery bounds how long a preview whose token was revoked, or whose time is up, keeps its
	// tunnel.
	sweepEvery = 5 * time.Second

	passwordMax = 200
	commandMax  = 300
	hostnameMax = 100
)

var unavailable = api.PreviewsUnavailable("Previews aren't set up on this server: set PREVIEW_ORIGIN")

type Options struct {
	// DB is the state database, migrated.
	DB *sql.DB
	// Config is nil when previews are off.
	Config *config.Preview
	// PublicURL is where the CLI reaches the app, and so the tunnel.
	PublicURL string
	// TrustProxy takes the client's address from X-Forwarded-For, for the proxy's rate limit.
	TrustProxy bool
}

// Service is the previews: their rows, their tunnels, and the proxy in front of them.
type Service struct {
	db         *sql.DB
	cfg        *config.Preview
	apex       string
	tunnelBase string
	trustProxy bool
	misses     *misses

	// writes serializes changes to the rows, so deciding who holds a label and acting on it is one
	// step.
	writes sync.Mutex

	mu      sync.Mutex
	byID    map[string]*preview
	byLabel map[string]*preview
	closed  bool

	stopSweep context.CancelFunc
	swept     chan struct{}
}

// preview is a row, with what's live about it.
type preview struct {
	row
	url    string
	public *url.URL

	// Guarded by Service.mu.
	lease       *lease
	connectedAt time.Time
	// idleSince is when the preview last had no tunnel, for reconnectGrace.
	idleSince time.Time

	requests atomic.Int64
	// open counts requests going through right now, upgraded ones (HMR) included.
	open atomic.Int64

	verifiedMu sync.Mutex
	// verified holds SHA-256s of Authorization headers that had the right password, so argon2 runs
	// once per browser rather than once per request.
	verified map[[32]byte]struct{}
}

const verifiedMax = 64

func (p *preview) isVerified(key [32]byte) bool {
	p.verifiedMu.Lock()
	defer p.verifiedMu.Unlock()
	_, ok := p.verified[key]
	return ok
}

func (p *preview) remember(key [32]byte) {
	p.verifiedMu.Lock()
	defer p.verifiedMu.Unlock()
	if len(p.verified) >= verifiedMax {
		clear(p.verified)
	}
	p.verified[key] = struct{}{}
}

// New loads the previews still live, so their CLIs can reconnect, and sweeps them every few
// seconds until Close.
func New(ctx context.Context, o Options) (*Service, error) {
	s := &Service{db: o.DB, cfg: o.Config, trustProxy: o.TrustProxy, byID: map[string]*preview{}, byLabel: map[string]*preview{}}
	if o.Config == nil {
		return s, nil
	}
	public, err := url.Parse(o.PublicURL)
	if err != nil || public.Host == "" {
		return nil, fmt.Errorf("preview: expected the app's public URL, got %q", o.PublicURL)
	}
	scheme := "ws"
	if public.Scheme == "https" {
		scheme = "wss"
	}
	s.tunnelBase = scheme + "://" + public.Host + strings.TrimRight(public.Path, "/") + "/api/previews/"
	s.apex = strings.ToLower(o.Config.Origin.Hostname())
	s.misses = newMisses(o.Config.MissBurst, o.Config.MissEvery.D())

	now := time.Now()
	if err := deleteExpired(ctx, s.db, now); err != nil {
		return nil, fmt.Errorf("preview: clean up expired previews: %w", err)
	}
	rows, err := liveRows(ctx, s.db, now)
	if err != nil {
		return nil, fmt.Errorf("preview: load previews: %w", err)
	}
	for _, r := range rows {
		s.add(r, now)
	}

	sweepCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.stopSweep, s.swept = cancel, make(chan struct{})
	go s.sweepLoop(sweepCtx, sweepEvery)
	return s, nil
}

// Close ends every tunnel with 1012 (service restart), which CLIs answer by reconnecting to the next
// process. Not 1001: the tunnel reads that one as a plain EOF, losing the reason.
func (s *Service) Close() {
	if s.stopSweep != nil {
		s.stopSweep()
		<-s.swept
	}
	s.mu.Lock()
	s.closed = true
	var leases []*lease
	for _, p := range s.byID {
		if p.lease != nil {
			leases = append(leases, p.lease)
			p.lease = nil
		}
	}
	s.mu.Unlock()
	for _, l := range leases {
		l.end(websocket.StatusServiceRestart, "sloppa is restarting")
	}
	for _, l := range leases {
		<-l.closed
	}
}

// add mirrors a row in memory, without a tunnel since now.
func (s *Service) add(r row, now time.Time) *preview {
	u := s.cfg.PreviewURL(r.label)
	public, _ := url.Parse(u)
	p := &preview{row: r, url: u, public: public, idleSince: now, verified: map[[32]byte]struct{}{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[r.id] = p
	s.byLabel[r.label] = p
	return p
}

// forget drops a preview whose row is gone, ending its tunnel with code.
func (s *Service) forget(id string, code websocket.StatusCode, reason string) {
	s.mu.Lock()
	p := s.byID[id]
	if p == nil {
		s.mu.Unlock()
		return
	}
	delete(s.byID, id)
	if s.byLabel[p.label] == p {
		delete(s.byLabel, p.label)
	}
	l := p.lease
	p.lease = nil
	s.mu.Unlock()
	if l != nil {
		l.end(code, reason)
	}
}

// lookup is the live preview a label routes to, and its tunnel if one is up.
func (s *Service) lookup(label string) (*preview, *lease) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.byLabel[label]
	if p == nil || !time.Now().Before(p.expires) {
		return nil, nil
	}
	return p, p.lease
}

// holds tells whether the preview id still has a claim on its label: its tunnel is up, or was a
// moment ago.
func (s *Service) holds(id string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.byID[id]
	return p != nil && (p.lease != nil || now.Sub(p.idleSince) < reconnectGrace)
}

func (s *Service) List(ctx context.Context, a accounts.Account) ([]api.Preview, error) {
	if s.cfg == nil {
		return nil, unavailable
	}
	rows, err := liveRows(ctx, s.db, time.Now())
	if err != nil {
		return nil, err
	}
	previews := []api.Preview{}
	s.mu.Lock()
	for _, r := range rows {
		if r.user != a.ID {
			continue
		}
		if p := s.byID[r.id]; p != nil {
			previews = append(previews, p.api())
		}
	}
	s.mu.Unlock()
	slices.SortFunc(previews, func(a, b api.Preview) int {
		return cmp.Or(b.ConnectedAt.Compare(a.ConnectedAt), strings.Compare(a.ID, b.ID))
	})
	return previews, nil
}

// api is the preview as listed. Call with Service.mu held.
func (p *preview) api() api.Preview {
	connectedAt := p.connectedAt
	if connectedAt.IsZero() {
		connectedAt = p.created
	}
	return api.Preview{
		ID:          p.id,
		URL:         p.url,
		Name:        p.name,
		Protected:   p.passwordHash != "",
		Port:        p.port,
		Command:     p.command,
		Cwd:         p.cwd,
		Hostname:    p.hostname,
		ConnectedAt: api.Time(connectedAt),
		ExpiresAt:   api.Time(p.expires),
		Connected:   p.lease != nil,
		Connections: int(p.open.Load()),
		Requests:    int(p.requests.Load()),
	}
}

// length is a string's length as JavaScript counts it, which the limits were written in.
func length(s string) int { return len(utf16.Encode([]rune(s))) }

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

func validate(in api.StartPreview) (ttl time.Duration, err error) {
	if in.Name != "" && !api.IsPreviewName(in.Name) {
		return 0, api.BadRequest("name: expected lowercase letters, digits and dashes, at most 40, not starting or ending with a dash")
	}
	if length(in.Password) > passwordMax {
		return 0, api.BadRequest(fmt.Sprintf("password: expected at most %d characters", passwordMax))
	}
	ttl = api.PreviewTTLDefault
	if in.TTL != 0 {
		ttl = time.Duration(in.TTL) * time.Second
	}
	if ttl < api.PreviewTTLMin || ttl > api.PreviewTTLMax {
		return 0, api.BadRequest(fmt.Sprintf("ttl: expected seconds between %d and %d", int(api.PreviewTTLMin.Seconds()), int(api.PreviewTTLMax.Seconds())))
	}
	if in.Port < 1 || in.Port > 65535 {
		return 0, api.BadRequest("port: expected a port between 1 and 65535")
	}
	return ttl, nil
}

func (s *Service) Start(ctx context.Context, a accounts.Account, in api.StartPreview) (api.StartedPreview, error) {
	if s.cfg == nil {
		return api.StartedPreview{}, unavailable
	}
	ttl, err := validate(in)
	if err != nil {
		return api.StartedPreview{}, err
	}
	hash := ""
	if in.Password != "" {
		if hash, err = password.Hash(ctx, in.Password); err != nil {
			// Every password check in the process shares a few slots: a busy server says so, as logins do.
			if errors.Is(err, password.ErrBusy) {
				return api.StartedPreview{}, api.TooManyAttempts(1)
			}
			return api.StartedPreview{}, fmt.Errorf("preview: hash the password: %w", err)
		}
	}

	s.writes.Lock()
	defer s.writes.Unlock()
	now := time.Now()
	r := row{
		name:         in.Name,
		user:         a.ID,
		credential:   a.CredentialID,
		passwordHash: hash,
		port:         in.Port,
		command:      truncate(in.Command, commandMax),
		cwd:          truncate(in.Cwd, commandMax),
		hostname:     truncate(in.Hostname, hostnameMax),
		created:      now,
		expires:      now.Add(ttl),
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return api.StartedPreview{}, err
	}
	defer tx.Rollback()
	if err := deleteExpired(ctx, tx, now); err != nil {
		return api.StartedPreview{}, err
	}

	// The same user starting a preview under a name they hold replaces it: that's the previous run
	// of the same command, most likely. Someone else's waits until it's been gone a while.
	var evicted row
	if in.Name != "" {
		held, ok, err := findRow(ctx, tx, "label = ?", in.Name)
		if err != nil {
			return api.StartedPreview{}, err
		}
		if ok {
			if held.user != a.ID && s.holds(held.id, now) {
				return api.StartedPreview{}, api.PreviewNameTaken(in.Name)
			}
			if err := deleteRow(ctx, tx, held.id); err != nil {
				return api.StartedPreview{}, err
			}
			evicted = held
		}
	}
	for {
		r.id = newID()
		r.label = cmp.Or(in.Name, r.id)
		_, taken, err := findRow(ctx, tx, "id = ? OR label = ?", r.id, r.label)
		if err != nil {
			return api.StartedPreview{}, err
		}
		if !taken {
			break
		}
	}
	if err := insertRow(ctx, tx, r); err != nil {
		return api.StartedPreview{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.StartedPreview{}, err
	}

	if evicted.id != "" {
		reason := fmt.Sprintf("replaced by another preview named %q", in.Name)
		s.forget(evicted.id, tunnel.CloseReplaced, reason)
	}
	p := s.add(r, now)
	slog.Info("Preview started", "preview", r.label, "id", r.id, "user", a.Username, "port", r.port)
	return api.StartedPreview{
		ID:        r.id,
		URL:       p.url,
		TunnelURL: s.tunnelBase + r.id + "/tunnel",
		ExpiresAt: api.Time(r.expires),
	}, nil
}

func (s *Service) Stop(ctx context.Context, a accounts.Account, id string) error {
	if s.cfg == nil {
		return unavailable
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	// Only one's own, and only while it lasts.
	res, err := s.db.ExecContext(ctx, "DELETE FROM previews WHERE id = ? AND user_id = ? AND expires_ms > ?", id, a.ID, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return api.PreviewNotFound(id)
	}
	s.forget(id, tunnel.CloseStopped, "stopped from sloppa")
	return nil
}

func (s *Service) sweepLoop(ctx context.Context, every time.Duration) {
	defer close(s.swept)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.sweep(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("Couldn't sweep the previews", "err", err)
			}
		}
	}
}

// sweep ends what the database says is over: previews whose time is up, previews whose row went with
// the token or user that started them, and tunnels whose own token was revoked.
func (s *Service) sweep(ctx context.Context) error {
	s.writes.Lock()
	defer s.writes.Unlock()
	now := time.Now()
	if err := deleteExpired(ctx, s.db, now); err != nil {
		return err
	}
	rows, err := liveRows(ctx, s.db, now)
	if err != nil {
		return err
	}
	alive := map[string]bool{}
	for _, r := range rows {
		alive[r.id] = true
	}

	type ending struct {
		id     string
		code   websocket.StatusCode
		reason string
	}
	var over []ending
	var leases []*lease
	var credentials []string
	s.mu.Lock()
	for id, p := range s.byID {
		switch {
		case !now.Before(p.expires):
			over = append(over, ending{id, tunnel.CloseExpired, "the preview's time is up"})
		case !alive[id]:
			over = append(over, ending{id, tunnel.CloseUnauthorized, "the token that started the preview was revoked"})
		case p.lease != nil:
			leases = append(leases, p.lease)
			credentials = append(credentials, p.lease.credential)
		}
	}
	s.mu.Unlock()
	for _, e := range over {
		s.forget(e.id, e.code, e.reason)
	}

	live, err := liveCredentials(ctx, s.db, credentials, now)
	if err != nil {
		return err
	}
	for _, l := range leases {
		if !live[l.credential] {
			s.detach(l)
			l.end(tunnel.CloseUnauthorized, "the token the tunnel connected with was revoked")
		}
	}
	s.misses.prune()
	return nil
}
