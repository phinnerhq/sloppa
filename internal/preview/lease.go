package preview

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/tunnel"
)

// dialTimeout is how long a browser's connection waits for the CLI to reach its dev server.
const dialTimeout = 10 * time.Second

// maxStreams caps the connections a preview holds open through its tunnel, idle ones in the pool and
// upgraded ones (HMR) included: each is a connection to the developer's dev server, and anyone with
// the preview's address could open them. Past it, requests are turned away at once rather than
// queued. The CLI has its own cap, higher.
const maxStreams = 128

// errTooManyStreams is a dial refused for maxStreams.
var errTooManyStreams = errors.New("too many connections through the preview's tunnel")

// lease is one tunnel to a preview, routed for as long as it stays up. Every connection its
// transport opens is a stream through the tunnel to the dev server, so pooling them works as for
// any upstream.
type lease struct {
	p   *preview
	srv *tunnel.Server
	// credential is the token the tunnel connected with, which may not be the one that started the
	// preview.
	credential  string
	connectedAt time.Time
	transport   *http.Transport
	proxy       *httputil.ReverseProxy
	// streams holds a place for each connection open through the tunnel.
	streams chan struct{}

	once sync.Once
	// closed is closed once the CLI was told how the lease ended.
	closed chan struct{}
}

func (s *Service) newLease(p *preview, srv *tunnel.Server, credential string) *lease {
	l := &lease{p: p, srv: srv, credential: credential, connectedAt: time.Now(), streams: make(chan struct{}, maxStreams), closed: make(chan struct{})}
	// No ResponseHeaderTimeout: a dev server's first compile can take minutes, and the browser's
	// giving up ends the request anyway.
	l.transport = &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			select {
			case l.streams <- struct{}{}:
			default:
				return nil, errTooManyStreams
			}
			ctx, cancel := context.WithTimeout(ctx, dialTimeout)
			defer cancel()
			conn, err := srv.Dial(ctx)
			if err != nil {
				<-l.streams
				return nil, err
			}
			return &stream{Conn: conn, release: func() { <-l.streams }}, nil
		},
		// Whatever encoding the dev server picked goes through untouched.
		DisableCompression:  true,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     30 * time.Second,
	}
	l.proxy = s.reverseProxy(p, l.transport)
	return l
}

// stream is a connection through the tunnel, which gives its place back once closed.
type stream struct {
	net.Conn
	once    sync.Once
	release func()
}

func (s *stream) Close() error {
	err := s.Conn.Close()
	s.once.Do(s.release)
	return err
}

// end closes the tunnel with code, which tells the CLI whether to reconnect. It doesn't wait for
// the CLI to answer: closed says when it has.
func (l *lease) end(code websocket.StatusCode, reason string) {
	l.once.Do(func() {
		slog.Info("Preview tunnel closed", "preview", l.p.label, "id", l.p.id, "code", int(code), "reason", reason)
		go func() {
			defer close(l.closed)
			l.srv.Close(code, reason)
			l.transport.CloseIdleConnections()
		}()
	})
}

// attach routes p through l, ending the tunnel it replaces. When it can't, it ends l: p is gone
// already, or this server is shutting down.
func (s *Service) attach(l *lease) bool {
	s.mu.Lock()
	p := l.p
	if s.closed || s.byID[p.id] != p {
		closed := s.closed
		s.mu.Unlock()
		if closed {
			l.end(websocket.StatusServiceRestart, "sloppa is restarting")
		} else {
			l.end(tunnel.CloseStopped, "the preview is over")
		}
		return false
	}
	old := p.lease
	p.lease = l
	p.connectedAt = l.connectedAt
	s.mu.Unlock()
	if old != nil {
		old.end(tunnel.CloseReplaced, "replaced by a reconnect")
	}
	return true
}

// detach stops routing through l, if p still does.
func (s *Service) detach(l *lease) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l.p.lease == l {
		l.p.lease = nil
		l.p.idleSince = time.Now()
	}
}

// Tunnel accepts the WebSocket before checking the preview, even for one that's over: the CLI
// reads why in the close code, and only a final one stops it. An HTTP error, by contrast, could as
// well come from a proxy in front during a deploy.
func (s *Service) Tunnel(w http.ResponseWriter, r *http.Request, id string) {
	if s.cfg == nil {
		writeError(w, unavailable)
		return
	}
	a, ok := accounts.FromContext(r.Context())
	if !ok {
		writeError(w, api.Unauthorized("Sign in, or pass an agent token as a bearer"))
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{tunnel.Subprotocol}})
	if err != nil {
		return
	}
	if ws.Subprotocol() != tunnel.Subprotocol {
		ws.Close(tunnel.CloseProtocol, "this CLI is too old or too new for this server")
		return
	}
	p, code, reason := s.admitTunnel(r.Context(), a, id)
	if p == nil {
		ws.Close(code, reason)
		return
	}
	srv, err := tunnel.NewServer(r.Context(), ws)
	if err != nil {
		ws.Close(websocket.StatusInternalError, "couldn't start the tunnel")
		return
	}
	l := s.newLease(p, srv, a.CredentialID)
	if !s.attach(l) {
		<-l.closed
		return
	}
	slog.Info("Preview connected", "preview", p.label, "id", p.id, "port", p.port, "client", clientAddr(r, s.trustProxy))
	<-srv.Done()
	s.detach(l)
	l.end(websocket.StatusNormalClosure, "the tunnel went down")
	<-l.closed
}

// admitTunnel is the preview id, if a tunnel for a may serve it; otherwise how to close the tunnel.
func (s *Service) admitTunnel(ctx context.Context, a accounts.Account, id string) (*preview, websocket.StatusCode, string) {
	r, ok, err := findRow(ctx, s.db, "id = ?", id)
	if err != nil {
		slog.Error("Couldn't look up a preview", "id", id, "err", err)
		return nil, websocket.StatusInternalError, "couldn't look up the preview"
	}
	// Any of the user's tokens will do: the one that started the preview may have been rotated
	// since, and revoking it ends the preview anyway.
	if !ok || r.user != a.ID {
		return nil, tunnel.CloseUnauthorized, "no such preview: it was stopped, or isn't yours"
	}
	if !time.Now().Before(r.expires) {
		return nil, tunnel.CloseExpired, "the preview's time is up"
	}
	s.mu.Lock()
	p := s.byID[id]
	s.mu.Unlock()
	if p == nil {
		return nil, tunnel.CloseUnauthorized, "no such preview: it was stopped, or isn't yours"
	}
	return p, 0, ""
}

// writeError answers like the API does, for the tunnel's refusals before it's a WebSocket.
func writeError(w http.ResponseWriter, e *api.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	json.NewEncoder(w).Encode(e)
}
