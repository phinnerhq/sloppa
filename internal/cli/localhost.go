package cli

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// isLocalhost tells whether host is `localhost` or a name under it, all loopback (RFC 6761) as
// browsers have them: a local sloppa's previews live at `<name>.preview.localhost`, which the system's
// resolver may not know.
func isLocalhost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

var dialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

// dialContext is net.Dialer's, but with localhost and every name under it on loopback.
func dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || !isLocalhost(host) {
		return dialer.DialContext(ctx, network, address)
	}
	return dialLoopback(ctx, dialer, port)
}

// dialLoopback tries 127.0.0.1, then ::1 for servers that bound `localhost` on IPv6 only.
func dialLoopback(ctx context.Context, d *net.Dialer, port string) (net.Conn, error) {
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	if err == nil {
		return conn, nil
	}
	if conn, err6 := d.DialContext(ctx, "tcp", net.JoinHostPort("::1", port)); err6 == nil {
		return conn, nil
	}
	return nil, err
}

var localDialer = &net.Dialer{Timeout: 3 * time.Second}

// connectLocal opens a connection to the dev server. Loopback alone, ever: the port is local to
// this machine, whatever the server asks.
func connectLocal(ctx context.Context, port int) (net.Conn, error) {
	conn, err := dialLoopback(ctx, localDialer, strconv.Itoa(port))
	if err != nil {
		return nil, fmt.Errorf("connection refused on 127.0.0.1:%d", port)
	}
	return conn, nil
}

// freePort is a loopback port nothing listens on, for the dev server. It's freed before the dev
// server binds it, so it's a guess, but a good one.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
