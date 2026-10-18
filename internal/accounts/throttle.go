package accounts

import (
	"container/list"
	"net/netip"
	"sync"
	"time"
)

const (
	// freeAttempts are allowed before each failure makes the next attempt wait, doubling up to maxWait.
	freeAttempts = 5
	maxWait      = 15 * time.Minute
	// maxKeys bounds what the throttle remembers: past it the stalest keys are forgotten, so a flood
	// of usernames can't grow memory forever.
	maxKeys = 10_000
)

type failures struct {
	key   string
	count int
	last  time.Time
}

// throttle counts failed sign-ins per username and per client address, in memory: a restart forgets
// them. Per address alone, many addresses get unlimited guesses at one account; per username alone,
// anyone could keep its owner out for long, so a key on its own waits at most maxWait.
type throttle struct {
	mu sync.Mutex
	// order runs from the stalest key to the latest, for forgetting the stalest first.
	order *list.List
	keys  map[string]*list.Element
}

func newThrottle() *throttle {
	return &throttle{order: list.New(), keys: map[string]*list.Element{}}
}

func (t *throttle) wait(key string, now time.Time) time.Duration {
	el, ok := t.keys[key]
	if !ok {
		return 0
	}
	f := el.Value.(*failures)
	if f.count < freeAttempts {
		return 0
	}
	wait := maxWait
	// Past 2^10 seconds the wait is maxWait anyway; stopping there keeps the shift from overflowing.
	if doublings := f.count - freeAttempts; doublings < 10 {
		wait = min(time.Second<<doublings, maxWait)
	}
	return max(0, f.last.Add(wait).Sub(now))
}

// attempt is how long until all of keys may try again. When that's 0, the attempt counts as a
// failure until succeeded says otherwise.
func (t *throttle) attempt(keys []string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	var wait time.Duration
	for _, key := range keys {
		wait = max(wait, t.wait(key, now))
	}
	if wait > 0 {
		return wait
	}
	// Counted up front, so parallel guesses can't all slip in before the first one fails.
	for _, key := range keys {
		el, ok := t.keys[key]
		if !ok {
			t.keys[key] = t.order.PushBack(&failures{key: key, count: 1, last: now})
			continue
		}
		f := el.Value.(*failures)
		if now.Sub(f.last) > 2*maxWait {
			f.count = 0
		}
		f.count++
		f.last = now
		t.order.MoveToBack(el)
	}
	for t.order.Len() > maxKeys {
		stalest := t.order.Front()
		t.order.Remove(stalest)
		delete(t.keys, stalest.Value.(*failures).key)
	}
	return 0
}

// undo takes back an attempt that never got to check a password, as when hashing was too busy:
// a flood from elsewhere mustn't lock out whoever retries through it.
func (t *throttle) undo(keys []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, key := range keys {
		if el, ok := t.keys[key]; ok {
			if f := el.Value.(*failures); f.count > 0 {
				f.count--
			}
		}
	}
}

func (t *throttle) succeeded(keys []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, key := range keys {
		if el, ok := t.keys[key]; ok {
			t.order.Remove(el)
			delete(t.keys, key)
		}
	}
}

// clientKey groups IPv6 clients by /64, as the preview proxy does: anyone with one has the whole
// block to rotate through.
func clientKey(client string) string {
	addr, err := netip.ParseAddr(client)
	if err != nil {
		return client
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}
