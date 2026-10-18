package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
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

// A CLI that refuses but never ends its refusal mustn't hold the dial past its context, whether
// that ends by deadline or by cancellation.
func TestRefusalThatNeverEndsHonoursTheContext(t *testing.T) {
	near, far := net.Pipe()
	session, err := yamux.Client(near, config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	cli, err := yamux.Server(far, config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	go func() {
		for {
			stream, err := cli.AcceptStream()
			if err != nil {
				return
			}
			stream.Write([]byte{statusRefused, 'n', 'o'})
		}
	}()
	s := &Server{session: session}

	deadline, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	canceled, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	for ctx, want := range map[context.Context]error{deadline: context.DeadlineExceeded, canceled: context.Canceled} {
		done := make(chan error, 1)
		go func() {
			_, err := s.Dial(ctx)
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, want) {
				t.Errorf("got %v, want %v", err, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the dial outlived its context, waiting for %v", want)
		}
	}
}

// The CLI connects so many streams to the dev server at once, whatever the server asks: past that,
// it refuses without dialing, until one closes.
func TestClientCapsStreams(t *testing.T) {
	var dials atomic.Int32
	var held []net.Conn
	var mu sync.Mutex
	s, _ := pair(t, func() (net.Conn, error) {
		dials.Add(1)
		local, dev := net.Pipe()
		mu.Lock()
		held = append(held, dev)
		mu.Unlock()
		return local, nil
	})
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			c.Close()
		}
	})
	var streams []net.Conn
	for i := range maxClientStreams {
		conn, err := s.Dial(t.Context())
		if err != nil {
			t.Fatalf("stream %d: %v", i, err)
		}
		streams = append(streams, conn)
	}
	_, err := s.Dial(t.Context())
	var refused *RefusedError
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "256 connections") {
		t.Fatalf("one too many: %v", err)
	}
	if n := dials.Load(); n != maxClientStreams {
		t.Errorf("dialed the dev server %d times, want %d", n, maxClientStreams)
	}

	streams[0].Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := s.Dial(t.Context())
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a closed stream's place never came back: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
