package cli

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/agents"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/db"
	"github.com/phinnerhq/sloppa/internal/httpapi"
)

// agentsServer is the real API over a fresh database, with two machines of one user; the CLI is
// signed in as the first, "laptop".
func agentsServer(t *testing.T) (desktopToken string) {
	t.Helper()
	state, err := db.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { state.Close() })
	if err := db.Migrate(t.Context(), state); err != nil {
		t.Fatal(err)
	}
	accts, err := accounts.New(t.Context(), state, "admin", config.Secret("dev-password"))
	if err != nil {
		t.Fatal(err)
	}
	user, _, _, err := accts.Login(t.Context(), "admin", "dev-password", "")
	if err != nil {
		t.Fatal(err)
	}
	var tokens []string
	for _, name := range []string{"laptop", "desktop"} {
		m, err := accts.CreateMachine(t.Context(), user, name)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, m.Token)
	}
	srv := httptest.NewServer(httpapi.New(httpapi.Deps{
		Config:   &config.Config{PublicURL: "http://sloppa.test"},
		Accounts: accts,
		Agents:   agents.New(state, &agents.GitHub{BaseURL: "http://127.0.0.1:0"}),
	}))
	t.Cleanup(srv.Close)
	isolate(t)
	t.Setenv("SLOPPA_URL", srv.URL)
	t.Setenv("SLOPPA_TOKEN", tokens[0])
	return tokens[1]
}

func ok(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	res := run(t, stdin, args...)
	if res.code != 0 {
		t.Fatalf("sloppa %s: exit %d\n%s%s", strings.Join(args, " "), res.code, res.stdout, res.stderr)
	}
	return res.stdout
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestAgentsEditAndSyncFromTheCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sync links files, which needs developer mode on Windows")
	}
	desktop := agentsServer(t)
	home := os.Getenv("HOME")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)

	// Instructions: shared from a file, this machine's own from stdin.
	shared := filepath.Join(t.TempDir(), "shared.md")
	writeFile(t, shared, "Be nice.\n", 0o644)
	ok(t, "", "agents", "instructions", "set", shared)
	ok(t, "Laptop only.\n", "agents", "instructions", "set", "--machine", "this", "-")
	if got := ok(t, "", "agents", "instructions", "show", "--machine", "laptop", "--composed"); got != "Be nice.\n\nLaptop only.\n" {
		t.Errorf("composed = %q", got)
	}
	if res := run(t, "", "agents", "instructions", "show", "--machine", "nope"); res.code == 0 || !strings.Contains(res.stderr, `No machine "nope": there's`) {
		t.Errorf("unknown machine: %+v", res)
	}

	// Skills: a folder of them, executable bits and all.
	skills := t.TempDir()
	writeFile(t, filepath.Join(skills, "review", "SKILL.md"), "---\nname: review\ndescription: Reviews.\n---\n", 0o644)
	writeFile(t, filepath.Join(skills, "review", "bin", "check"), "#!/bin/sh\n", 0o755)
	writeFile(t, filepath.Join(skills, "review", "node_modules", "x.js"), "left out", 0o644)
	writeFile(t, filepath.Join(skills, "Other Skill", "SKILL.md"), "no frontmatter", 0o644)
	writeFile(t, filepath.Join(skills, "not-a-skill", "README.md"), "", 0o644)
	// A link out of the folder could point at anything: it's left out.
	secret := filepath.Join(t.TempDir(), "id_ed25519")
	writeFile(t, secret, "PRIVATE KEY", 0o600)
	if err := os.Symlink(secret, filepath.Join(skills, "review", "key")); err != nil {
		t.Fatal(err)
	}
	res := run(t, "", "agents", "skill", "push", skills)
	out := res.stdout
	if res.code != 0 || !strings.Contains(res.stderr, "left out "+filepath.Join(skills, "review", "key")) {
		t.Errorf("push: %+v", res)
	}
	if !strings.Contains(out, "pushed: other-skill\t") || !strings.Contains(out, "pushed: review\t") || !strings.Contains(out, "2 files") {
		t.Errorf("push:\n%s", out)
	}
	if got := ok(t, "", "agents", "skill", "show", "review", "--files"); got != "SKILL.md\nbin/check\n" {
		t.Errorf("files = %q", got)
	}
	ok(t, "", "agents", "skill", "disable", "other-skill", "--machine", "desktop")
	if got := ok(t, "", "agents", "skill", "list"); !strings.Contains(got, "other-skill\tuploaded\ton, off on desktop\t") || !strings.Contains(got, "review\tuploaded\ton\tReviews.") {
		t.Errorf("list:\n%s", got)
	}

	// Sync this machine.
	out = ok(t, "", "agents", "sync")
	if !strings.Contains(out, "installed skill review") {
		t.Errorf("sync:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md")); string(b) != "Be nice.\n\nLaptop only.\n" {
		t.Errorf("CLAUDE.md = %q", b)
	}
	if fi, err := os.Stat(filepath.Join(home, ".agents", "skills", "review", "bin", "check")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("bin/check: %v %v", fi, err)
	}
	if got := ok(t, "", "agents", "status"); !strings.HasPrefix(got, "desktop\tnever synced\t\nlaptop\tin sync\t") {
		t.Errorf("status:\n%s", got)
	}

	// Removing asks first, and then the next sync takes it away.
	if res := run(t, "", "agents", "skill", "remove", "review"); res.code == 0 || !strings.Contains(res.stderr, "--yes") {
		t.Errorf("remove without --yes: %+v", res)
	}
	ok(t, "", "agents", "skill", "remove", "review", "--yes")
	if out := ok(t, "", "agents", "sync"); !strings.Contains(out, "removed skill review") {
		t.Errorf("sync after removing:\n%s", out)
	}

	// Syncing is a machine's own: the desktop's token sees the desktop's plan.
	t.Setenv("SLOPPA_TOKEN", desktop)
	t.Setenv("HOME", t.TempDir())
	if out := ok(t, "", "agents", "sync", "--dry-run"); strings.Contains(out, "other-skill") || !strings.Contains(out, "wrote ~/.agents/AGENTS.md") {
		t.Errorf("the desktop would get a skill turned off there:\n%s", out)
	}
}
