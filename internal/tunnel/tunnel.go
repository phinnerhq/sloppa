// Package tunnel carries a preview's TCP connections between the server and `sloppa preview run`.
//
// The CLI opens one WebSocket to the server, authenticated with its machine token, and yamux runs over
// it. For each browser connection the server opens a stream; the CLI connects it to the dev server
// and answers with one status byte, then both sides copy bytes with half-close, so HTTP keep-alive,
// upgrades (HMR) and streaming all work as they would on a socket. yamux's flow control keeps a slow
// reader from making either side buffer without bound.
//
// The server ends a tunnel by closing the WebSocket with a code: 4000-4099 are final and the CLI
// stops its dev server; anything else (a network blip, the server restarting) is worth a reconnect.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// Subprotocol versions the tunnel, so an old CLI gets a clear refusal instead of garbage.
const Subprotocol = "sloppa-tunnel.v1"

// Close codes the server ends a tunnel with. All of them are final.
const (
	CloseProtocol     websocket.StatusCode = 4000
	CloseUnauthorized websocket.StatusCode = 4001
	CloseStopped      websocket.StatusCode = 4003
	CloseExpired      websocket.StatusCode = 4004
	CloseReplaced     websocket.StatusCode = 4005
)

// Final tells whether a close code means the preview is over, rather than worth a reconnect.
func Final(code websocket.StatusCode) bool { return code >= 4000 && code < 4100 }

const (
	statusOK      byte = 1
	statusRefused byte = 0
	// refusalMax bounds the message a refusal carries.
	refusalMax = 512
	// maxClientStreams caps the connections the CLI makes to the dev server at once, whatever the
	// server asks for: the server keeps under a lower cap of its own, but the developer's machine
	// needn't trust it to.
	maxClientStreams = 256
)

func config() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.EnableKeepAlive = true
	c.KeepAliveInterval = 20 * time.Second
	c.ConnectionWriteTimeout = 30 * time.Second
	c.StreamOpenTimeout = 30 * time.Second
	// A stream one side closed waits this long for the other's FIN before it's reset. Closing is
	// quick when both are done; one that isn't is a response the browser walked away from while the
	// dev server writes on, which would otherwise hold its connection and buffer for 5 minutes.
	c.StreamCloseTimeout = 30 * time.Second
	c.MaxStreamWindowSize = 1 << 20
	return c
}

// closeRecorder keeps the first error reading the WebSocket, which carries its close code.
type closeRecorder struct {
	net.Conn
	once sync.Once
	err  error
}

func (c *closeRecorder) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil {
		c.once.Do(func() { c.err = err })
	}
	return n, err
}

// Server is the server's end of one tunnel.
type Server struct {
	ws      *websocket.Conn
	session *yamux.Session
}

// NewServer runs the tunnel over ws, which the server accepted. ctx bounds its life.
func NewServer(ctx context.Context, ws *websocket.Conn) (*Server, error) {
	session, err := yamux.Client(websocket.NetConn(ctx, ws, websocket.MessageBinary), config())
	if err != nil {
		return nil, err
	}
	return &Server{ws: ws, session: session}, nil
}

// RefusedError is a connection the CLI couldn't make, usually because nothing listens on the port.
type RefusedError struct{ Message string }

func (e *RefusedError) Error() string { return "the dev server refused the connection: " + e.Message }

// Dial opens a connection to the dev server, for http.Transport's DialContext. ctx bounds waiting
// for the CLI's answer, refusal message and all: a CLI that sends the refusal byte and keeps the
// stream open mustn't hold the dial past it.
func (s *Server) Dial(ctx context.Context) (net.Conn, error) {
	stream, err := s.session.OpenStream()
	if err != nil {
		return nil, err
	}
	// ctx's deadline reaches the stream through this callback too, never as a deadline of the
	// stream's own: that timer could fire before ctx counts as done, and the error wouldn't say why.
	stop := context.AfterFunc(ctx, func() { stream.SetReadDeadline(time.Now()) })
	var status [1]byte
	_, err = io.ReadFull(stream, status[:])
	var message []byte
	if err == nil && status[0] != statusOK {
		message, err = io.ReadAll(io.LimitReader(stream, refusalMax))
	}
	// Once ctx is done, its callback may yet cut the stream's reads short: it's no use to anyone.
	if !stop() {
		stream.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		stream.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("tunnel: %w", err)
	}
	if status[0] != statusOK {
		stream.Close()
		return nil, &RefusedError{Message: string(message)}
	}
	stream.SetReadDeadline(time.Time{})
	return stream, nil
}

// Streams is how many connections are open through the tunnel.
func (s *Server) Streams() int { return s.session.NumStreams() }

// Done is closed once the tunnel is down, whichever side ended it.
func (s *Server) Done() <-chan struct{} { return s.session.CloseChan() }

// Close ends the tunnel with a code and a reason for the CLI.
func (s *Server) Close(code websocket.StatusCode, reason string) {
	s.ws.Close(code, reason)
	s.session.Close()
}

// ClosedError is how a tunnel ended, from the CLI's side.
type ClosedError struct {
	// Code is the WebSocket close code, or -1 when the connection just dropped.
	Code   websocket.StatusCode
	Reason string
	Err    error
}

func (e *ClosedError) Error() string {
	if e.Code == -1 {
		return fmt.Sprintf("tunnel dropped: %v", e.Err)
	}
	return fmt.Sprintf("tunnel closed (%d): %s", e.Code, e.Reason)
}

func (e *ClosedError) Unwrap() error { return e.Err }

// Final tells whether the server meant the preview to end.
func (e *ClosedError) Final() bool { return Final(e.Code) }

// Client is the CLI's end of one tunnel.
type Client struct {
	ws       *websocket.Conn
	recorder *closeRecorder
	session  *yamux.Session
}

// NewClient runs the tunnel over ws, which the CLI dialed. ctx bounds its life.
func NewClient(ctx context.Context, ws *websocket.Conn) (*Client, error) {
	recorder := &closeRecorder{Conn: websocket.NetConn(ctx, ws, websocket.MessageBinary)}
	session, err := yamux.Server(recorder, config())
	if err != nil {
		return nil, err
	}
	return &Client{ws: ws, recorder: recorder, session: session}, nil
}

// Serve connects each stream the server opens with dial, until the tunnel goes down; it returns a
// *ClosedError saying how. Past maxClientStreams at once, streams are refused without dialing.
func (c *Client) Serve(dial func() (net.Conn, error)) error {
	slots := make(chan struct{}, maxClientStreams)
	for {
		stream, err := c.session.AcceptStream()
		if err != nil {
			return c.closed(err)
		}
		select {
		case slots <- struct{}{}:
			go func() {
				defer func() { <-slots }()
				serveStream(stream, dial)
			}()
		default:
			// Here rather than in a goroutine: a server flooding the tunnel waits on its own refusals.
			refuse(stream, fmt.Sprintf("the CLI has %d connections open to the dev server already", maxClientStreams))
		}
	}
}

func (c *Client) closed(err error) *ClosedError {
	c.session.Close()
	cause := c.recorder.err
	if cause == nil {
		cause = err
	}
	code := websocket.CloseStatus(cause)
	var ce websocket.CloseError
	reason := ""
	if errors.As(cause, &ce) {
		reason = ce.Reason
	}
	return &ClosedError{Code: code, Reason: reason, Err: cause}
}

// Close hangs up, as when the dev server exits.
func (c *Client) Close() {
	c.ws.Close(websocket.StatusNormalClosure, "")
	c.session.Close()
}

// refuse answers a stream with a refusal, and why.
func refuse(stream *yamux.Stream, message string) {
	if len(message) > refusalMax {
		message = message[:refusalMax]
	}
	stream.Write(append([]byte{statusRefused}, message...))
	stream.Close()
}

func serveStream(stream *yamux.Stream, dial func() (net.Conn, error)) {
	local, err := dial()
	if err != nil {
		refuse(stream, err.Error())
		return
	}
	if _, err := stream.Write([]byte{statusOK}); err != nil {
		local.Close()
		stream.Close()
		return
	}
	Pipe(stream, local)
}

type closeWriter interface{ CloseWrite() error }

// Pipe copies between a and b until both directions are done. EOF on one side half-closes the other,
// so a request body can end while its response still streams; an error closes both.
func Pipe(a, b net.Conn) {
	var wg sync.WaitGroup
	copyHalf := func(dst, src net.Conn) {
		defer wg.Done()
		_, err := io.Copy(dst, src)
		if err != nil {
			slog.Debug("tunnel copy", "err", err)
			a.Close()
			b.Close()
			return
		}
		if cw, ok := dst.(closeWriter); ok {
			cw.CloseWrite()
		} else if s, ok := dst.(*yamux.Stream); ok {
			// A yamux stream's Close only ends our writes: reads go on until the other side's FIN.
			s.Close()
		} else {
			dst.Close()
		}
	}
	wg.Add(2)
	go copyHalf(a, b)
	go copyHalf(b, a)
	wg.Wait()
	a.Close()
	b.Close()
}
