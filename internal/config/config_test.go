package config

import (
	"strings"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
)

func parse(t *testing.T, vars map[string]string) (*Config, error) {
	t.Helper()
	return Parse(env.Options{Environment: vars})
}

func TestDefaults(t *testing.T) {
	c, err := parse(t, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicURL != "http://127.0.0.1:4100" || c.DataDir != ".data" || c.S3 != nil || c.Preview != nil {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestWildcardHost(t *testing.T) {
	c, err := parse(t, map[string]string{"HOST": "0.0.0.0", "PORT": "8080"})
	if err != nil || c.PublicURL != "http://localhost:8080" {
		t.Fatalf("got %v, %v", c, err)
	}
}

func TestPreview(t *testing.T) {
	c, err := parse(t, map[string]string{
		"PUBLIC_URL":         "https://sloppa.example.com/",
		"PREVIEW_ORIGIN":     "https://preview.example.net",
		"PREVIEW_MISS_EVERY": "5 seconds",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicURL != "https://sloppa.example.com" || c.Preview.MissEvery.D() != 5*time.Second || c.Preview.MissBurst != 20 {
		t.Fatalf("unexpected: %+v %+v", c, c.Preview)
	}
	if got := c.Preview.PreviewURL("demo"); got != "https://demo.preview.example.net/" {
		t.Fatal(got)
	}
}

func TestInvalid(t *testing.T) {
	for _, vars := range []map[string]string{
		{"PREVIEW_ORIGIN": "https://1.2.3.4"},
		{"PREVIEW_ORIGIN": "ftp://preview.example.net"},
		{"PREVIEW_ORIGIN": "https://preview.example.net/path"},
		{"PUBLIC_URL": "https://a.preview.example.net", "PREVIEW_ORIGIN": "https://preview.example.net"},
		{"S3_ENDPOINT": "https://s3.example.com"},
		{"PREVIEW_ORIGIN": "https://preview.example.net", "PREVIEW_MISS_EVERY": "soon"},
	} {
		if _, err := parse(t, vars); err == nil {
			t.Errorf("%v: expected an error", vars)
		}
	}
}

func TestSecretRedacted(t *testing.T) {
	c, err := parse(t, map[string]string{"BOOTSTRAP_PASSWORD": "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if s := c.BootstrapPassword.String(); strings.Contains(s, "hunter2") {
		t.Fatal(s)
	}
	if c.BootstrapPassword.Value() != "hunter2" {
		t.Fatal("lost the value")
	}
}
