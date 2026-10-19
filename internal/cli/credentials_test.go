package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/phinnerhq/sloppa/internal/api"
)

func readSaved(t *testing.T) credentials {
	t.Helper()
	path, err := credentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c credentials
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	return c
}

func TestLoginSavesTheTokenPipedIn(t *testing.T) {
	fake := newFakeAPI(t)
	isolate(t)

	res := run(t, fake.token+"\n", "login", fake.URL+"/")

	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	path, _ := credentialsPath()
	if want := "Signed in to " + fake.URL + " as test; saved to " + path + "\n"; res.stdout != want {
		t.Errorf("stdout = %q, want %q", res.stdout, want)
	}
	if saved := readSaved(t); saved != (credentials{URL: fake.URL, Token: fake.token}) {
		t.Errorf("saved %+v", saved)
	}
	if runtime.GOOS != "windows" {
		file, _ := os.Stat(path)
		dir, _ := os.Stat(filepath.Dir(path))
		if file.Mode().Perm() != 0o600 || dir.Mode().Perm() != 0o700 {
			t.Errorf("modes: file %v, dir %v", file.Mode().Perm(), dir.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}

	// The other commands use it from then on.
	if who := run(t, "", "whoami"); who.code != 0 || who.stdout != "test\n" {
		t.Errorf("whoami: %+v", who)
	}
}

func TestLoginReplacesASavedLogin(t *testing.T) {
	fake := newFakeAPI(t)
	isolate(t)
	if _, err := saveCredentials(credentials{URL: "http://old.example", Token: "old"}); err != nil {
		t.Fatal(err)
	}

	if res := run(t, fake.token, "login", fake.URL); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}

	if saved := readSaved(t); saved.Token != fake.token {
		t.Errorf("saved %+v", saved)
	}
}

func TestLoginWantsATokenOnStdin(t *testing.T) {
	fake := newFakeAPI(t)
	isolate(t)

	res := run(t, "  \n", "login", fake.URL)

	if res.code != 1 || !strings.Contains(res.stderr, "Pipe a machine token in, so it stays out of your shell history:\n  "+signInAgain) {
		t.Errorf("%+v", res)
	}
	if fake.requested("GET /api/session") {
		t.Error("asked the server without a token")
	}
}

func TestLoginRefusesATokenTheServerDoesnt(t *testing.T) {
	fake := newFakeAPI(t)
	isolate(t)

	res := run(t, "not-a-token", "login", fake.URL)

	if res.code != 1 || !strings.Contains(res.stderr, fake.URL+" didn't take that token: the server refused this machine's token") {
		t.Errorf("%+v", res)
	}
	if _, ok := readCredentials(); ok {
		t.Error("saved a token the server refused")
	}
}

func TestLoginWantsAURL(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{"login"}, {"login", "sloppa.example.com"}, {"login", "a", "b"}} {
		if res := run(t, "token", args...); res.code != 1 {
			t.Errorf("%v: %+v", args, res)
		}
	}
}

func TestNotSignedIn(t *testing.T) {
	isolate(t)

	res := run(t, "", "artifact", "list")

	if res.code != 1 || !strings.Contains(res.stderr, "Not signed in to sloppa: run `sloppa setup '<setup-link>'`") {
		t.Errorf("%+v", res)
	}
}

func TestEnvironmentWinsOverTheFile(t *testing.T) {
	fake := newFakeAPI(t)
	isolate(t)
	if _, err := saveCredentials(credentials{URL: fake.URL, Token: "stale"}); err != nil {
		t.Fatal(err)
	}

	// The file alone, with its stale token, is refused.
	if res := run(t, "", "whoami"); res.code != 1 || !strings.Contains(res.stderr, "refused this machine's token") {
		t.Errorf("file only: %+v", res)
	}
	// Each variable wins on its own, the other coming from the file.
	t.Setenv("SLOPPA_TOKEN", fake.token)
	if res := run(t, "", "whoami"); res.code != 0 {
		t.Errorf("token from the environment: %+v", res)
	}
	t.Setenv("SLOPPA_TOKEN", "")
	if _, err := saveCredentials(credentials{URL: "http://127.0.0.1:1", Token: fake.token}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLOPPA_URL", fake.URL+"/")
	if res := run(t, "", "whoami"); res.code != 0 {
		t.Errorf("URL from the environment: %+v", res)
	}
	// Both, with no file at all.
	path, _ := credentialsPath()
	os.RemoveAll(filepath.Dir(path))
	t.Setenv("SLOPPA_TOKEN", fake.token)
	if res := run(t, "", "whoami", "--json"); res.code != 0 || res.stdout != "{\n  \"username\": \"test\"\n}\n" {
		t.Errorf("environment only: %+v", res)
	}
}

func TestReadsTheTypeScriptCLIsLogin(t *testing.T) {
	fake := newFakeAPI(t)
	isolate(t)
	legacy := legacyCredentialsPath()
	if want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "sloppa", "credentials.json"); legacy != want {
		t.Errorf("legacy path %s, want %s", legacy, want)
	}
	os.MkdirAll(filepath.Dir(legacy), 0o700)
	os.WriteFile(legacy, []byte(`{"url":"`+fake.URL+`","token":"`+fake.token+`"}`), 0o600)

	if res := run(t, "", "whoami"); res.code != 0 {
		t.Errorf("%+v", res)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	if runtime.GOOS != "windows" {
		if want := filepath.Join(os.Getenv("HOME"), ".config", "sloppa", "credentials.json"); legacyCredentialsPath() != want {
			t.Errorf("without XDG_CONFIG_HOME: %s, want %s", legacyCredentialsPath(), want)
		}
	}
}

func TestSetupRegistersThisMachine(t *testing.T) {
	fake := newFakeAPI(t)
	isolate(t)
	hostname, _ := os.Hostname()
	path, _ := credentialsPath()

	res := run(t, "", "setup", fake.URL+"/setup/first")

	if res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if want := fmt.Sprintf("Registered this machine as %q with %s as test; saved its token to %s\n", hostname, fake.URL, path); res.stdout != want {
		t.Errorf("stdout = %q, want %q", res.stdout, want)
	}
	if saved := readSaved(t); saved != (credentials{URL: fake.URL, Token: fake.token}) {
		t.Errorf("saved %+v", saved)
	}
	want := fakeSetup{"first", "", api.MachineFacts{Hostname: hostname, OS: runtime.GOOS, Arch: runtime.GOARCH, CLIVersion: "test"}}
	if len(fake.posted()) != 1 || fake.posted()[0] != want {
		t.Errorf("posted %+v, want %+v", fake.posted(), want)
	}
	if who := run(t, "", "whoami"); who.code != 0 || who.stdout != "test on "+hostname+"\n" {
		t.Errorf("whoami: %+v", who)
	}

	// Again, the saved token comes along to re-pair the machine.
	res = run(t, "", "setup", fake.URL+"/setup/second")
	if res.code != 0 || !strings.HasPrefix(res.stdout, fmt.Sprintf("Re-paired machine %q with %s as test, under a new token", hostname, fake.URL)) {
		t.Errorf("again: %+v", res)
	}
	if len(fake.posted()) != 2 || fake.posted()[1].bearer != fake.token {
		t.Errorf("posted %+v", fake.posted())
	}
}

func TestSetupSendsNoTokenOfAnotherServer(t *testing.T) {
	fake := newFakeAPI(t)
	isolate(t)
	if _, err := saveCredentials(credentials{URL: "http://other.example", Token: "theirs"}); err != nil {
		t.Fatal(err)
	}
	// Nor the environment's, which re-pairing would leave dead.
	t.Setenv("SLOPPA_TOKEN", fake.token)

	if res := run(t, "", "setup", fake.URL+"/setup/code"); res.code != 0 {
		t.Fatalf("exit %d: %s", res.code, res.stderr)
	}
	if len(fake.posted()) != 1 || fake.posted()[0].bearer != "" {
		t.Errorf("posted %+v", fake.posted())
	}
}

func TestSetupRefusesASpentLink(t *testing.T) {
	fake := newFakeAPI(t)
	isolate(t)

	res := run(t, "", "setup", fake.URL+"/setup/spent")

	if res.code != 1 || !strings.Contains(res.stderr, "Couldn't set this machine up: This setup link was used already") {
		t.Errorf("%+v", res)
	}
	if _, ok := readCredentials(); ok {
		t.Error("saved something")
	}
}

func TestSetupWantsALink(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{"setup"}, {"setup", "sloppa.example.com/setup/x"}, {"setup", "https://sloppa.example.com/"},
		{"setup", "https://sloppa.example.com/setup/"}, {"setup", "a", "b"}} {
		if res := run(t, "", args...); res.code != 1 {
			t.Errorf("%v: %+v", args, res)
		}
	}
}

func TestSplitSetupLink(t *testing.T) {
	for link, want := range map[string][2]string{
		"https://sloppa.example.com/setup/abc":       {"https://sloppa.example.com", "abc"},
		" http://127.0.0.1:4100/setup/abc ":          {"http://127.0.0.1:4100", "abc"},
		"https://example.com/sloppa/setup/abc?x=1#y": {"https://example.com/sloppa", "abc"},
		"https://example.com/setup/sloppa/setup/abc": {"https://example.com/setup/sloppa", "abc"},
		"https://sloppa.example.com/setup/abc/more":  {"", ""},
		"ftp://sloppa.example.com/setup/abc":         {"", ""},
	} {
		server, code, _ := splitSetupLink(link)
		if [2]string{server, code} != want {
			t.Errorf("splitSetupLink(%q) = %q, %q; want %q", link, server, code, want)
		}
	}
}
