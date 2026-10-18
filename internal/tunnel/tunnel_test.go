package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// pair is a tunnel between an httptest server and a client, with the server's end handed over.
func pair(t *testing.T, dial func() (net.Conn, error)) (*Server, chan error) {
	t.Helper()
	servers := make(chan *Server, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}})
		if err != nil {
			t.Error(err)
			return
		}
		s, err := NewServer(r.Context(), ws)
		if err != nil {
			t.Error(err)
			return
		}
		servers <- s
		<-s.Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{Subprotocols: []string{Subprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(ctx, ws)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Serve(dial) }()
	return <-servers, done
}

func TestProxiesHTTP(t *testing.T) {
	dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Write([]byte("got " + r.URL.Path + " " + string(body)))
	}))
	defer dev.Close()
	s, _ := pair(t, func() (net.Conn, error) { return net.Dial("tcp", dev.Listener.Addr().String()) })

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return s.Dial(ctx) },
	}}
	for i := range 3 {
		resp, err := client.Post("http://dev/hello", "text/plain", strings.NewReader("body"))
		if err != nil {
			t.Fatal(i, err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(got) != "got /hello body" {
			t.Fatalf("%d: %q", i, got)
		}
	}
}

func TestRefused(t *testing.T) {
	s, _ := pair(t, func() (net.Conn, error) { return nil, errors.New("nothing on port 3000") })
	_, err := s.Dial(context.Background())
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Message != "nothing on port 3000" {
		t.Fatalf("got %v", err)
	}
}

func TestCloseCodeReachesClient(t *testing.T) {
	s, done := pair(t, func() (net.Conn, error) { return nil, errors.New("unused") })
	s.Close(CloseStopped, "stopped from sloppa")
	select {
	case err := <-done:
		var closed *ClosedError
		if !errors.As(err, &closed) || closed.Code != CloseStopped || !closed.Final() || closed.Reason != "stopped from sloppa" {
			t.Fatalf("got %#v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client never noticed")
	}
}
