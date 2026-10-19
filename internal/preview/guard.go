package preview

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"
)

// clientAddr is who sent a request, as the app's own routes tell it: behind a reverse proxy
// (TRUST_PROXY), the last X-Forwarded-For entry, the one the proxy added; the socket's peer
// otherwise. Empty when there's no telling.
func clientAddr(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if header := strings.Join(r.Header.Values("X-Forwarded-For"), ","); header != "" {
			entries := strings.Split(header, ",")
			if addr, err := netip.ParseAddr(strings.TrimSpace(entries[len(entries)-1])); err == nil {
				return addr.Unmap().WithZone("").String()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// `::ffff:1.2.3.4`, as dual-stack sockets report IPv4 peers, is just 1.2.3.4.
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Unmap().WithZone("").String()
	}
	return strings.ToLower(host)
}

// missKey groups IPv6 clients by /64, since anyone with one has the whole block to rotate through.
func missKey(client string) string {
	addr, err := netip.ParseAddr(client)
	if err != nil || !addr.Is6() {
		return client
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}

var port = regexp.MustCompile(`:\d+$`)

// hostOf is a Host header without its port, which plays no part in routing.
func hostOf(host string) string {
	host = port.ReplaceAllString(host, "")
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return strings.ToLower(host)
}

// missesMax bounds how many clients are tracked at once. Past it, new clients share one bucket:
// someone rotating through addresses slows down everyone else who's guessing, not themselves less.
const missesMax = 50_000

// misses rate-limits failures per client: unknown hosts and wrong passwords each spend a token, so
// guessing preview names or passwords gets slow. Successes cost nothing.
type misses struct {
	burst int
	every time.Duration

	mu       sync.Mutex
	buckets  map[string]*bucket
	overflow *bucket
	swept    time.Time
}

// bucket is a client's tokens, burst at most, refilled one per every.
type bucket struct {
	tokens float64
	// at is when tokens was counted.
	at time.Time
}

func newMisses(burst int, every time.Duration) *misses {
	now := time.Now()
	return &misses{
		burst:    burst,
		every:    every,
		buckets:  map[string]*bucket{},
		overflow: &bucket{tokens: float64(burst), at: now},
		swept:    now,
	}
}

// bucketLocked is client's bucket, with its tokens counted as of now.
func (m *misses) bucketLocked(client string, now time.Time) *bucket {
	if now.Sub(m.swept) > time.Minute {
		m.pruneLocked(now)
	}
	key := missKey(client)
	b := m.buckets[key]
	if b == nil {
		if len(m.buckets) >= missesMax {
			b = m.overflow
		} else {
			b = &bucket{tokens: float64(m.burst), at: now}
			m.buckets[key] = b
		}
	}
	b.tokens = min(float64(m.burst), b.tokens+float64(now.Sub(b.at))/float64(m.every))
	b.at = now
	return b
}

// pruneLocked forgets the buckets that have refilled to their burst, which are the same as none:
// memory follows recent misses.
func (m *misses) pruneLocked(now time.Time) {
	full := m.every * time.Duration(m.burst)
	for key, b := range m.buckets {
		if now.Sub(b.at) > full {
			delete(m.buckets, key)
		}
	}
	m.swept = now
}

func (m *misses) prune() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(time.Now())
}

// blocked tells whether the client spent its misses; checking doesn't spend one.
func (m *misses) blocked(client string) bool {
	if client == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bucketLocked(client, time.Now()).tokens < 1
}

// take spends one of the client's misses, if it has one left. A guess that takes a while to check
// takes its miss first: checking and spending after would let a batch of guesses all pass the check
// before the first one fails.
func (m *misses) take(client string) bool {
	if client == "" {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.bucketLocked(client, time.Now())
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (m *misses) miss(client string) { m.take(client) }

// refund gives back a miss taken for a guess that turned out right, or never got checked.
func (m *misses) refund(client string) {
	if client == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.bucketLocked(client, time.Now())
	b.tokens = min(float64(m.burst), b.tokens+1)
}

// retryAfter is how long until a blocked client may miss again, in whole seconds.
func (m *misses) retryAfter() int {
	return max(1, int(math.Round(m.every.Seconds())))
}
