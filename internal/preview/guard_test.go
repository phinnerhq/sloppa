package preview

import (
	"fmt"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func TestClientAddr(t *testing.T) {
	request := func(remote string, forwarded ...string) *http.Request {
		r := &http.Request{RemoteAddr: remote, Header: http.Header{}}
		for _, f := range forwarded {
			r.Header.Add("X-Forwarded-For", f)
		}
		return r
	}
	for _, c := range []struct {
		r     *http.Request
		trust bool
		want  string
	}{
		// The socket's peer, unless behind a trusted proxy.
		{request("203.0.113.9:5000", "198.51.100.1"), false, "203.0.113.9"},
		{request("[::ffff:203.0.113.9]:5000"), false, "203.0.113.9"},
		{request("[2001:DB8::1]:5000"), false, "2001:db8::1"},
		// Only the entry the proxy added, the last one, is believed.
		{request("10.0.0.2:5000", "1.1.1.1, 198.51.100.7"), true, "198.51.100.7"},
		{request("10.0.0.2:5000", "1.1.1.1", "198.51.100.8"), true, "198.51.100.8"},
		{request("10.0.0.2:5000", "not an address"), true, "10.0.0.2"},
		{request("10.0.0.2:5000"), true, "10.0.0.2"},
	} {
		if got := clientAddr(c.r, c.trust); got != c.want {
			t.Errorf("clientAddr(%s, %v, trust %v) = %q, want %q", c.r.RemoteAddr, c.r.Header, c.trust, got, c.want)
		}
	}
}

func TestMissKeyGroupsIPv6By64(t *testing.T) {
	if missKey("2001:db8:0:0:ffff::2") != missKey("2001:db8::9") {
		t.Error("one /64, two keys")
	}
	if missKey("2001:db8::1") == missKey("2001:db9::1") {
		t.Error("two /64s, one key")
	}
	if got := missKey("203.0.113.9"); got != "203.0.113.9" {
		t.Error(got)
	}
}

func TestHostOf(t *testing.T) {
	for host, want := range map[string]string{
		"Demo.Preview.Test:4100": "demo.preview.test",
		"preview.test":           "preview.test",
		"[::1]:80":               "::1",
		"":                       "",
	} {
		if got := hostOf(host); got != want {
			t.Errorf("hostOf(%q) = %q", host, got)
		}
	}
}

func TestMissesBlockAndRefill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMisses(2, time.Second)
		m.miss("2001:db8::1")
		if m.blocked("2001:db8::3") {
			t.Fatal("blocked with a miss to spare")
		}
		m.miss("2001:db8::2")
		if !m.blocked("2001:db8::3") {
			t.Fatal("the /64 spent its burst")
		}
		if m.blocked("2001:db9::1") || m.blocked("203.0.113.9") || m.blocked("") {
			t.Fatal("someone else is blocked")
		}
		// Checking doesn't spend.
		for range 5 {
			m.blocked("203.0.113.9")
		}
		if m.blocked("203.0.113.9") {
			t.Fatal("checks spent misses")
		}
		time.Sleep(time.Second)
		if m.blocked("2001:db8::3") {
			t.Fatal("not refilled")
		}
		m.miss("2001:db8::3")
		if !m.blocked("2001:db8::3") {
			t.Fatal("refilled by more than one")
		}
		if m.retryAfter() != 1 {
			t.Fatal(m.retryAfter())
		}
	})
}

func TestMissesTakeAndRefund(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMisses(2, time.Second)
		if !m.take("2001:db8::1") || !m.take("2001:db8::2") {
			t.Fatal("refused with misses to spare")
		}
		if m.take("2001:db8::3") {
			t.Fatal("took a miss the /64 doesn't have")
		}
		// A right password gives its miss back, never past the burst.
		m.refund("2001:db8::1")
		if !m.take("2001:db8::1") || m.take("2001:db8::1") {
			t.Fatal("refunded other than one")
		}
		time.Sleep(time.Hour)
		m.refund("2001:db8::1")
		if !m.take("2001:db8::1") || !m.take("2001:db8::1") || m.take("2001:db8::1") {
			t.Fatal("refunded past the burst")
		}
		if !m.take("") {
			t.Fatal("no client is never limited")
		}
	})
}

func TestMissesForgetRefilledClients(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := newMisses(3, time.Second)
		for i := range 100 {
			m.miss(fmt.Sprintf("198.51.100.%d", i))
		}
		// Pruned once a minute.
		time.Sleep(time.Minute + time.Second)
		m.miss("203.0.113.1")
		m.mu.Lock()
		n := len(m.buckets)
		m.mu.Unlock()
		if n != 1 {
			t.Fatalf("%d buckets kept", n)
		}
	})
}

func TestMissesShareABucketPastTheirBound(t *testing.T) {
	m := newMisses(1, time.Hour)
	for i := range missesMax {
		m.miss(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255))
	}
	m.miss("203.0.113.1")
	if !m.blocked("203.0.113.2") {
		t.Fatal("clients past the bound aren't limited together")
	}
}
