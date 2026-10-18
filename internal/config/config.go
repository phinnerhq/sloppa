// Package config reads the server's settings from the environment (and `.env`, in development).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Secret is a string that stays out of logs and error messages.
type Secret string

func (Secret) String() string                 { return "<redacted>" }
func (Secret) GoString() string               { return "<redacted>" }
func (Secret) LogValue() slog.Value           { return slog.StringValue("<redacted>") }
func (s Secret) MarshalText() ([]byte, error) { return []byte("<redacted>"), nil }

// Value is the secret itself, for the one place that needs it.
func (s Secret) Value() string { return string(s) }

type Config struct {
	Host string `env:"HOST" envDefault:"127.0.0.1"`
	Port int    `env:"PORT" envDefault:"4100"`
	// PublicURL is where browsers reach the app, without a trailing slash.
	PublicURL string `env:"PUBLIC_URL"`

	// BootstrapUsername and BootstrapPassword create the first user, and reset its password
	// whenever the password changes. They can go once the user exists.
	BootstrapUsername string `env:"BOOTSTRAP_USERNAME"`
	BootstrapPassword Secret `env:"BOOTSTRAP_PASSWORD"`

	// TrustProxy takes the client's address from the last X-Forwarded-For entry, the one a reverse
	// proxy in front added, rather than the socket's, which is the proxy's for everyone.
	TrustProxy bool `env:"TRUST_PROXY"`

	// StaticDir serves the dashboard from disk instead of the copy built into the binary.
	StaticDir string `env:"STATIC_DIR"`
	// DataDir holds the accounts, the SQLite index and, without S3, the files themselves.
	DataDir string `env:"DATA_DIR" envDefault:".data"`

	S3 *S3 `env:",init" envPrefix:"S3_"`

	Preview *Preview `env:",init" envPrefix:"PREVIEW_"`
}

// S3 is set when S3_ENDPOINT is: artifacts then live in the bucket instead of DataDir.
type S3 struct {
	Endpoint        string `env:"ENDPOINT"`
	Region          string `env:"REGION" envDefault:"us-east-1"`
	Bucket          string `env:"BUCKET" envDefault:"artifacts"`
	AccessKeyID     Secret `env:"ACCESS_KEY_ID"`
	SecretAccessKey Secret `env:"SECRET_ACCESS_KEY"`
}

// Preview is set when PREVIEW_ORIGIN is: `sloppa preview run` serves dev servers as its subdomains,
// on the app's port, told apart by Host.
type Preview struct {
	// Origin is like https://preview.example.com: previews are its subdomains.
	Origin *url.URL `env:"ORIGIN"`
	// MissBurst failed guesses (unknown hosts, wrong passwords) a client gets, refilled one per
	// MissEvery.
	MissBurst int      `env:"MISS_BURST" envDefault:"20"`
	MissEvery Duration `env:"MISS_EVERY" envDefault:"3s"`
}

// Duration reads Go's `3s` as well as the `3 seconds` the earlier server took.
type Duration time.Duration

var spelled = regexp.MustCompile(`^(\d+)\s*(millis|milliseconds?|seconds?|minutes?|hours?)$`)

func (d *Duration) UnmarshalText(text []byte) error {
	raw := strings.TrimSpace(string(text))
	if m := spelled.FindStringSubmatch(raw); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := map[byte]time.Duration{'m': time.Minute, 's': time.Second, 'h': time.Hour}[m[2][0]]
		if strings.HasPrefix(m[2], "milli") {
			unit = time.Millisecond
		}
		*d = Duration(time.Duration(n) * unit)
		return nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("expected a duration like 3s, got %q", raw)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

// Load reads the environment. Errors name the variable to fix.
func Load() (*Config, error) {
	return Parse(env.Options{})
}

// Parse is Load with options, so tests can pass an environment of their own.
func Parse(opts env.Options) (*Config, error) {
	var c Config
	if err := env.ParseWithOptions(&c, opts); err != nil {
		return nil, err
	}
	if err := c.finish(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) finish() error {
	if c.PublicURL == "" {
		host := c.Host
		// A wildcard bind address (as in a container) isn't something a browser can open.
		if host == "0.0.0.0" || host == "::" {
			host = "localhost"
		}
		c.PublicURL = fmt.Sprintf("http://%s:%d", host, c.Port)
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	public, err := url.Parse(c.PublicURL)
	if err != nil || (public.Scheme != "http" && public.Scheme != "https") || public.Host == "" {
		return fmt.Errorf("PUBLIC_URL: expected an http:// or https:// URL, got %s", c.PublicURL)
	}

	c.BootstrapUsername = strings.TrimSpace(c.BootstrapUsername)

	if c.S3 != nil && c.S3.Endpoint == "" {
		c.S3 = nil
	}
	if c.S3 != nil && (c.S3.AccessKeyID == "" || c.S3.SecretAccessKey == "") {
		return errors.New("S3_ENDPOINT is set: S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY are needed too")
	}

	if c.Preview != nil && c.Preview.Origin == nil {
		c.Preview = nil
	}
	if c.Preview != nil {
		origin, err := previewOrigin(c.Preview.Origin)
		if err != nil {
			return err
		}
		c.Preview.Origin = origin
		// The app and the previews share a port, told apart by Host: the app's can't be a preview's.
		app := public.Hostname()
		if app == origin.Hostname() || strings.HasSuffix(app, "."+origin.Hostname()) {
			return fmt.Errorf("PUBLIC_URL: %s is the preview domain's, give the app a host of its own", app)
		}
		if c.Preview.MissBurst < 1 || c.Preview.MissEvery <= 0 {
			return errors.New("PREVIEW_MISS_BURST and PREVIEW_MISS_EVERY must be positive")
		}
	}
	return nil
}

var ipLike = regexp.MustCompile(`^[\d.]+$|^\[|:`)

func previewOrigin(u *url.URL) (*url.URL, error) {
	invalid := func(why string) error { return fmt.Errorf("PREVIEW_ORIGIN: expected %s, got %s", why, u) }
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, invalid("http:// or https://")
	}
	if ipLike.MatchString(u.Hostname()) || u.Hostname() == "" {
		return nil, invalid("a DNS name, whose subdomains are the previews")
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.User != nil {
		return nil, invalid("only a scheme, host and port")
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

// PreviewURL is where a preview labelled `label` is served.
func (p *Preview) PreviewURL(label string) string {
	return fmt.Sprintf("%s://%s.%s/", p.Origin.Scheme, label, p.Origin.Host)
}
