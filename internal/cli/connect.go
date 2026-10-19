package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/tunnel"
)

// expiryGrace lets the server's own verdict on an expired preview arrive first, when it can.
var expiryGrace = 5 * time.Second

const (
	dialTimeout = 10 * time.Second
	// Reconnects back off doubling from backoffMin, to backoffMax at most.
	backoffMin = 500 * time.Millisecond
	backoffMax = 10 * time.Second
)

// closeLabels name the final close codes, for saying why a preview ended.
var closeLabels = map[websocket.StatusCode]string{
	tunnel.CloseProtocol:     "protocol error",
	tunnel.CloseUnauthorized: "unauthorized",
	tunnel.CloseStopped:      "stopped",
	tunnel.CloseExpired:      "expired",
	tunnel.CloseReplaced:     "replaced by another connection",
}

// tunnelEnd is why the preview is over, when the dev server didn't end it itself.
type tunnelEnd struct {
	message string
	// exit is the code `sloppa preview run` exits with.
	exit int
}

// closedBy is the end the server called with a final close code.
func closedBy(code websocket.StatusCode, reason string) tunnelEnd {
	label, ok := closeLabels[code]
	if !ok {
		label = fmt.Sprintf("closed (%d)", code)
	}
	if reason != "" {
		label += ": " + reason
	}
	// Stopped or out of time is the preview ending as asked; anything else is a failure.
	exit := 1
	if code == tunnel.CloseStopped || code == tunnel.CloseExpired {
		exit = 0
	}
	return tunnelEnd{message: label, exit: exit}
}

// previewTunnel keeps a preview's tunnel up, connecting each stream the server opens to the dev
// server.
type previewTunnel struct {
	client  *client
	preview api.StartedPreview
	port    int
	log     io.Writer
	// onConnect is called each time the tunnel comes up, with first set the first time.
	onConnect func(first bool)
	connected atomic.Bool
}

// run keeps the tunnel up until the server ends the preview: through network blips and server
// restarts, it reconnects to the same URL. The preview's expiry ends it here too, a little late, so a
// preview whose server is gone doesn't retry forever. Cancelling ctx hangs up.
func (t *previewTunnel) run(ctx context.Context) tunnelEnd {
	ctx, cancel := context.WithDeadline(ctx, t.preview.ExpiresAt.Add(expiryGrace))
	defer cancel()
	delay := backoffMin
	for {
		end, up, err := t.session(ctx)
		if end != nil {
			return *end
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if t.connected.Load() {
				return tunnelEnd{message: "expired: the preview's time is up", exit: 0}
			}
			// It never ran its time: it was dead on arrival, as the server would say.
			return tunnelEnd{message: "expired before its tunnel ever connected", exit: 1}
		}
		if ctx.Err() != nil {
			return tunnelEnd{}
		}
		if up {
			delay = backoffMin
		}
		fmt.Fprintf(t.log, "sloppa: tunnel lost (%v), reconnecting\n", err)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
		delay = min(delay*2, backoffMax)
	}
}

// session is one connection, for as long as it lasts. It returns an end when the server ended the
// preview for good; any other error is a reason to reconnect. up tells whether it connected at all.
func (t *previewTunnel) session(ctx context.Context) (end *tunnelEnd, up bool, err error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	ws, resp, err := websocket.Dial(dialCtx, t.preview.TunnelURL, &websocket.DialOptions{
		HTTPClient: t.client.http,
		HTTPHeader: http.Header{
			"Authorization": {"Bearer " + t.client.token},
			"User-Agent":    {t.client.userAgent},
		},
		Subprotocols: []string{tunnel.Subprotocol},
	})
	cancel()
	if err != nil {
		if end := refused(resp); end != nil {
			return end, false, nil
		}
		return nil, false, err
	}
	// The tunnel outlives ctx by a hang-up: cancelling ends it with a normal close, which the server
	// takes as the CLI leaving rather than a network blip.
	conn, err := tunnel.NewClient(context.WithoutCancel(ctx), ws)
	if err != nil {
		ws.CloseNow()
		return nil, false, err
	}
	defer context.AfterFunc(ctx, conn.Close)()
	t.onConnect(!t.connected.Swap(true))
	err = conn.Serve(func() (net.Conn, error) { return connectLocal(ctx, t.port) })
	if closed, ok := errors.AsType[*tunnel.ClosedError](err); ok && closed.Final() {
		return new(closedBy(closed.Code, closed.Reason)), true, nil
	}
	return nil, true, err
}

// refused is the end of a preview the server won't take a tunnel for at all: the token or the
// preview is no good anymore. Anything else, like a server restarting behind a proxy, may pass.
func refused(resp *http.Response) *tunnelEnd {
	if resp == nil {
		return nil
	}
	e := api.Error{}
	if resp.Body != nil {
		json.NewDecoder(resp.Body).Decode(&e)
	}
	reason := e.Message
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if reason == "" {
			reason = "the server refused this machine's token"
		}
		return new(closedBy(tunnel.CloseUnauthorized, reason))
	case http.StatusNotFound, http.StatusGone:
		return new(closedBy(tunnel.CloseStopped, "the server has no such preview anymore"))
	case http.StatusBadRequest, http.StatusConflict, http.StatusUpgradeRequired:
		if reason == "" {
			reason = "the server answered " + resp.Status
		}
		return new(closedBy(tunnel.CloseProtocol, reason))
	}
	return nil
}
