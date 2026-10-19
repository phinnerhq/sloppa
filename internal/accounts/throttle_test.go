package accounts

import (
	"fmt"
	"testing"
	"time"
)

func TestThrottleDoublesWaitsAfterFreeAttempts(t *testing.T) {
	th := newThrottle()
	keys := []string{"user:admin"}
	now := time.Unix(1_000_000, 0)
	for i := range freeAttempts {
		if wait := th.attempt(keys, now); wait != 0 {
			t.Fatalf("attempt %d waited %v, want none", i+1, wait)
		}
	}
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		if wait := th.attempt(keys, now); wait != want {
			t.Fatalf("wait = %v, want %v", wait, want)
		}
		now = now.Add(want)
		if wait := th.attempt(keys, now); wait != 0 {
			t.Fatalf("after waiting %v, still waits %v", want, wait)
		}
	}
}

func TestThrottleCapsTheWait(t *testing.T) {
	th := newThrottle()
	keys := []string{"user:admin"}
	now := time.Unix(1_000_000, 0)
	for range 40 {
		th.attempt(keys, now)
		now = now.Add(th.wait(keys[0], now))
	}
	if wait := th.attempt(keys, now); wait != 0 {
		t.Fatalf("waited %v right when allowed", wait)
	}
	if wait := th.attempt(keys, now); wait != maxWait {
		t.Fatalf("wait = %v, want the cap %v", wait, maxWait)
	}
}

func TestThrottleWaitsForTheSlowestKey(t *testing.T) {
	th := newThrottle()
	now := time.Unix(1_000_000, 0)
	for range freeAttempts {
		th.attempt([]string{"client:1.2.3.4"}, now)
	}
	// A fresh username from a throttled address still waits for the address.
	if wait := th.attempt([]string{"user:other", "client:1.2.3.4"}, now); wait != time.Second {
		t.Fatalf("wait = %v, want 1s", wait)
	}
	if wait := th.attempt([]string{"user:other", "client:5.6.7.8"}, now); wait != 0 {
		t.Fatalf("another address waited %v", wait)
	}
}

func TestThrottleForgetsOnSuccessAndWhenStale(t *testing.T) {
	th := newThrottle()
	keys := []string{"user:admin"}
	now := time.Unix(1_000_000, 0)
	for range freeAttempts {
		th.attempt(keys, now)
	}
	th.succeeded(keys)
	if wait := th.attempt(keys, now); wait != 0 {
		t.Fatalf("waited %v after a success", wait)
	}

	for range 10 {
		th.attempt(keys, now)
		now = now.Add(th.wait(keys[0], now))
	}
	// Long enough after the last failure, the count starts over.
	now = now.Add(2*maxWait + time.Second)
	for i := range freeAttempts {
		if wait := th.attempt(keys, now); wait != 0 {
			t.Fatalf("stale attempt %d waited %v", i+1, wait)
		}
	}
}

func TestThrottleForgetsTheStalestPastMaxKeys(t *testing.T) {
	th := newThrottle()
	now := time.Unix(1_000_000, 0)
	for range freeAttempts {
		th.attempt([]string{"user:first"}, now)
	}
	for i := range maxKeys {
		now = now.Add(time.Millisecond)
		th.attempt([]string{fmt.Sprintf("user:flood-%d", i)}, now)
	}
	if len(th.keys) != maxKeys || th.order.Len() != maxKeys {
		t.Fatalf("remembers %d keys (%d in order), want %d", len(th.keys), th.order.Len(), maxKeys)
	}
	if _, ok := th.keys["user:first"]; ok {
		t.Fatal("kept the stalest key")
	}
}

func TestThrottleUndoesAnAttemptThatNeverChecked(t *testing.T) {
	th := newThrottle()
	keys := []string{"user:admin", "client:192.0.2.1"}
	now := time.Unix(1_000_000, 0)
	for range freeAttempts {
		th.attempt(keys, now)
		th.undo(keys)
	}
	if wait := th.attempt(keys, now); wait != 0 {
		t.Fatalf("undone attempts made it wait %v", wait)
	}
}

func TestClientKeyGroupsIPv6By64(t *testing.T) {
	for client, want := range map[string]string{
		"2001:db8::1":        "2001:db8::/64",
		"2001:db8::ffff:1":   "2001:db8::/64",
		"2001:db8:0:1::1":    "2001:db8:0:1::/64",
		"fe80::1%eth0":       "fe80::/64",
		"203.0.113.9":        "203.0.113.9",
		"::ffff:203.0.113.9": "203.0.113.9",
		"not an address":     "not an address",
	} {
		if got := clientKey(client); got != want {
			t.Errorf("clientKey(%q) = %q, want %q", client, got, want)
		}
	}
}
