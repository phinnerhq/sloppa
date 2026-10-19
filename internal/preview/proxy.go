package preview

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/phinnerhq/sloppa/internal/password"
	"github.com/phinnerhq/sloppa/internal/tunnel"
)

// IsPreviewHost tells whether host is the preview domain's: the apex or one of its subdomains.
// The app's own host never is, which config checks.
func (s *Service) IsPreviewHost(host string) bool {
	if s.cfg == nil {
		return false
	}
	h := hostOf(host)
	return h == s.apex || strings.HasSuffix(h, "."+s.apex)
}

// ServeProxy routes by Host alone: `<label>.<apex>` is a preview, and no header ever picks where a
// request goes beyond the label. Unknown names and wrong passwords cost the client a miss.
func (s *Service) ServeProxy(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil {
		notFound.write(w)
		return
	}
	host := hostOf(r.Host)
	if host == s.apex {
		if r.URL.Path == "/healthz" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write([]byte("ok\n"))
			return
		}
		apexPage.write(w)
		return
	}
	label, ok := strings.CutSuffix(host, "."+s.apex)
	if !ok {
		notFound.write(w)
		return
	}
	client := clientAddr(r, s.trustProxy)
	if s.misses.blocked(client) {
		tooMany(s.misses.retryAfter()).write(w)
		return
	}
	p, l := s.lookup(label)
	if p == nil {
		s.misses.miss(client)
		noPreview.write(w)
		return
	}
	if p.passwordHash != "" {
		if refused, ok := s.checkPassword(r.Context(), p, label, r.Header.Get("Authorization"), client); !ok {
			refused.write(w)
			return
		}
	}
	if l == nil {
		disconnected.write(w)
		return
	}
	p.requests.Add(1)
	p.open.Add(1)
	defer p.open.Add(-1)
	l.proxy.ServeHTTP(w, r)
}

var basicAuth = regexp.MustCompile(`(?i)^basic\s+(\S+)$`)

// basicPassword is the password from Basic auth: any user name goes, the password is the secret.
func basicPassword(header string) (string, bool) {
	m := basicAuth.FindStringSubmatch(header)
	if m == nil {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		if decoded, err = base64.RawStdEncoding.DecodeString(m[1]); err != nil {
			return "", false
		}
	}
	_, pw, ok := strings.Cut(string(decoded), ":")
	return pw, ok
}

// checkPassword tells whether header carries p's password, and if not, the refusal to answer with.
// A password to check takes one of the client's misses before waiting its turn to be hashed, and
// gets it back if it's right or never got checked.
func (s *Service) checkPassword(ctx context.Context, p *preview, label, header, client string) (refusal, bool) {
	key := sha256.Sum256([]byte(header))
	if p.isVerified(key) {
		return refusal{}, true
	}
	pw, ok := basicPassword(header)
	if !ok {
		s.misses.miss(client)
		return passwordNeeded(label), false
	}
	if !s.misses.take(client) {
		return tooMany(s.misses.retryAfter()), false
	}
	ok, err := password.Verify(ctx, pw, p.passwordHash)
	if err != nil {
		s.misses.refund(client)
		return checkingBusy, false
	}
	if !ok {
		return passwordNeeded(label), false
	}
	s.misses.refund(client)
	p.remember(key)
	return refusal{}, true
}

// reverseProxy passes requests through one tunnel. Bodies stream both ways with backpressure,
// responses are flushed as they come (server-sent events), and upgrades (HMR) are spliced.
func (s *Service) reverseProxy(p *preview, transport http.RoundTripper) *httputil.ReverseProxy {
	// What the dev server thinks it's called: a loopback address passes Vite's `allowedHosts` and
	// the like.
	local := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port))}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(local)
			// Replaced with the proxy's own (Rewrite drops the browser's), so the dev server can trust
			// what it reads in them.
			h := pr.Out.Header
			if client := clientAddr(pr.In, s.trustProxy); client != "" {
				h.Set("X-Forwarded-For", client)
			}
			h.Set("X-Forwarded-Host", p.public.Host)
			h.Set("X-Forwarded-Proto", p.public.Scheme)
			h.Del("Expect")
			// The preview's password is the proxy's business, not the dev server's.
			if p.passwordHash != "" && basicAuth.MatchString(h.Get("Authorization")) {
				h.Del("Authorization")
			}
		},
		Transport:     transport,
		FlushInterval: -1,
		ModifyResponse: func(res *http.Response) error {
			if location := res.Header.Get("Location"); location != "" {
				res.Header.Set("Location", rewriteLocation(location, p.port, p.public))
			}
			for i, cookie := range res.Header["Set-Cookie"] {
				res.Header["Set-Cookie"][i] = scopeCookie(cookie, p.public.Hostname())
			}
			if _, ok := res.Header["X-Robots-Tag"]; !ok {
				res.Header.Set("X-Robots-Tag", "noindex, nofollow")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// A dev server not listening is just a 502.
			if refused := (*tunnel.RefusedError)(nil); errors.As(err, &refused) {
				notListening(p.port).write(w)
				return
			}
			if errors.Is(err, errTooManyStreams) {
				busy.write(w)
				return
			}
			if r.Context().Err() == nil {
				slog.Warn("Preview proxy error", "preview", p.label, "err", err)
			}
			unreachable(err).write(w)
		},
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}
}

var loopback = map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true, "0.0.0.0": true}

// rewriteLocation points redirects to the dev server's own address back at the preview.
func rewriteLocation(location string, port int, public *url.URL) string {
	u, err := url.Parse(location)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Port() != strconv.Itoa(port) || !loopback[strings.ToLower(u.Hostname())] {
		return location
	}
	rewritten := url.URL{Scheme: public.Scheme, Host: public.Host, Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery, Fragment: u.Fragment, RawFragment: u.RawFragment}
	if rewritten.Path == "" {
		rewritten.Path = "/"
	}
	return rewritten.String()
}

// scopeCookie drops Domain from a cookie unless it's this preview's own host: a cookie set for a
// parent domain would reach every other preview, and anything else under it.
func scopeCookie(cookie, host string) string {
	parts := strings.Split(cookie, ";")
	kept := []string{parts[0]}
	for _, attribute := range parts[1:] {
		name, domain, _ := strings.Cut(strings.TrimSpace(attribute), "=")
		if !strings.EqualFold(name, "domain") || strings.EqualFold(strings.TrimPrefix(domain, "."), host) {
			kept = append(kept, attribute)
		}
	}
	return strings.Join(kept, ";")
}
