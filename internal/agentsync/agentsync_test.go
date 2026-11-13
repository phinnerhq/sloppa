package agentsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phinnerhq/sloppa/internal/api"
)

// server is the skills a fake server has, by name, and how often each was fetched.
type server struct {
	skills  map[string][]api.SkillFile
	fetched map[string]int
}

func newServer() *server {
	return &server{skills: map[string][]api.SkillFile{}, fetched: map[string]int{}}
}

func (s *server) put(name string, files ...api.SkillFile) api.SkillRef {
	slices.SortFunc(files, func(a, b api.SkillFile) int { return strings.Compare(a.Path, b.Path) })
	s.skills[name] = files
	return api.SkillRef{Name: name, Hash: api.SkillHash(files)}
}

func (s *server) fetch(_ context.Context, name string) (api.SkillContent, error) {
	s.fetched[name]++
	files, ok := s.skills[name]
	if !ok {
		return api.SkillContent{}, errors.New("no such skill")
	}
	return api.SkillContent{Name: name, Hash: api.SkillHash(files), Files: files}, nil
}

func md(body string) api.SkillFile { return api.SkillFile{Path: "SKILL.md", Content: []byte(body)} }

type machine struct {
	t      *testing.T
	home   string
	server *server
	clock  time.Time
}

// newMachine is a home with Claude Code and Codex set up, and no opencode.
func newMachine(t *testing.T) *machine {
	home := t.TempDir()
	for _, dir := range []string{".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &machine{t: t, home: home, server: newServer(), clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (m *machine) sync(plan api.SyncPlan, dryRun bool) Result {
	m.t.Helper()
	m.clock = m.clock.Add(time.Second)
	res := Sync(m.t.Context(), plan, Options{
		Home:      m.home,
		Harnesses: Harnesses(m.home, func(string) string { return "" }),
		Fetch:     m.server.fetch,
		DryRun:    dryRun,
		Now:       m.clock,
	})
	if len(res.Problems) > 0 {
		m.t.Fatalf("problems: %v", res.Problems)
	}
	return res
}

func (m *machine) path(rel string) string { return filepath.Join(m.home, filepath.FromSlash(rel)) }

func (m *machine) read(rel string) string {
	m.t.Helper()
	b, err := os.ReadFile(m.path(rel))
	if err != nil {
		m.t.Fatal(err)
	}
	return string(b)
}

func (m *machine) write(rel, content string) {
	m.t.Helper()
	os.MkdirAll(filepath.Dir(m.path(rel)), 0o755)
	if err := os.WriteFile(m.path(rel), []byte(content), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) exists(rel string) bool {
	_, err := os.Lstat(m.path(rel))
	return err == nil
}

func (m *machine) linksTo(rel, target string) bool {
	dest, err := os.Readlink(m.path(rel))
	return err == nil && filepath.Clean(dest) == m.path(target)
}

func skipWithoutSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need developer mode on Windows")
	}
}

func TestSyncInstallsAndLinks(t *testing.T) {
	skipWithoutSymlinks(t)
	m := newMachine(t)
	review := m.server.put("review", md("review"), api.SkillFile{Path: "bin/run", Executable: true, Content: []byte("#!/bin/sh")})
	plan := api.SyncPlan{Instructions: "Be nice.\n", Skills: []api.SkillRef{review}}

	res := m.sync(plan, false)
	want := []string{
		"wrote ~/.agents/AGENTS.md",
		"linked ~/.claude/CLAUDE.md to ~/.agents/AGENTS.md (Claude Code's instructions)",
		"linked ~/.codex/AGENTS.md to ~/.agents/AGENTS.md (Codex's instructions)",
		"installed skill review",
		"linked ~/.claude/skills/review to ~/.agents/skills/review (Claude Code's skills)",
	}
	if !slices.Equal(res.Changes, want) {
		t.Errorf("changes:\n%s\nwant\n%s", strings.Join(res.Changes, "\n"), strings.Join(want, "\n"))
	}
	if res.Backup != "" {
		t.Errorf("backed up %s with nothing to replace", res.Backup)
	}
	if m.read(".agents/AGENTS.md") != "Be nice.\n" || m.read(".claude/CLAUDE.md") != "Be nice.\n" {
		t.Error("instructions not written")
	}
	if !m.linksTo(".claude/skills/review", ".agents/skills/review") || m.read(".claude/skills/review/SKILL.md") != "review" {
		t.Error("skill not linked")
	}
	if fi, _ := os.Stat(m.path(".agents/skills/review/bin/run")); fi.Mode().Perm() != 0o755 {
		t.Errorf("bin/run mode %v", fi.Mode())
	}
	if m.exists(".config/opencode") {
		t.Error("made opencode's folder, which wasn't there")
	}

	// Nothing to do the second time, and nothing fetched.
	if res := m.sync(plan, false); len(res.Changes) != 0 {
		t.Errorf("second sync changed %v", res.Changes)
	}
	if m.server.fetched["review"] != 1 {
		t.Errorf("fetched %d times", m.server.fetched["review"])
	}
}

func TestSyncUpdatesRestoresAndRemoves(t *testing.T) {
	skipWithoutSymlinks(t)
	m := newMachine(t)
	review := m.server.put("review", md("v1"))
	other := m.server.put("other", md("other"))
	m.sync(api.SyncPlan{Skills: []api.SkillRef{review, other}}, false)

	// A new version upstream.
	review = m.server.put("review", md("v2"))
	res := m.sync(api.SyncPlan{Skills: []api.SkillRef{review, other}}, false)
	if !slices.Equal(res.Changes, []string{"updated skill review"}) || m.read(".agents/skills/review/SKILL.md") != "v2" {
		t.Errorf("update: %v", res.Changes)
	}
	if res.Backup == "" || readFile(t, filepath.Join(res.Backup, ".agents/skills/review/SKILL.md")) != "v1" {
		t.Errorf("v1 not backed up in %q", res.Backup)
	}

	// Edited on the machine: put back.
	m.write(".agents/skills/review/SKILL.md", "edited")
	res = m.sync(api.SyncPlan{Skills: []api.SkillRef{review, other}}, false)
	if !slices.Equal(res.Changes, []string{"restored skill review, changed on this machine"}) || m.read(".agents/skills/review/SKILL.md") != "v2" {
		t.Errorf("restore: %v", res.Changes)
	}

	// Dropped from the plan: removed, with its link; a skill sloppa didn't install stays.
	m.write(".agents/skills/handmade/SKILL.md", "mine")
	res = m.sync(api.SyncPlan{Skills: []api.SkillRef{review}}, false)
	if !slices.Equal(res.Changes, []string{"removed skill other"}) || m.exists(".agents/skills/other") || m.exists(".claude/skills/other") {
		t.Errorf("remove: %v", res.Changes)
	}
	if !m.exists(".agents/skills/handmade") {
		t.Error("removed a skill sloppa didn't install")
	}
	if readFile(t, filepath.Join(res.Backup, ".agents/skills/other/SKILL.md")) != "other" {
		t.Error("removed skill not backed up")
	}
}

func TestSyncTakesOverWhatsThere(t *testing.T) {
	skipWithoutSymlinks(t)
	m := newMachine(t)
	m.write(".claude/CLAUDE.md", "my old instructions")
	m.write(".agents/AGENTS.md", "someone's AGENTS.md")
	m.write(".agents/skills/review/SKILL.md", "installed by another tool")
	m.write(".claude/skills/review/SKILL.md", "a copy in Claude's folder")
	// Already the server's version: adopted as it is.
	m.write(".agents/skills/same/SKILL.md", "same")
	review := m.server.put("review", md("review"))
	same := m.server.put("same", md("same"))

	res := m.sync(api.SyncPlan{Instructions: "New.\n", Skills: []api.SkillRef{review, same}}, false)
	for _, want := range []string{
		"updated ~/.agents/AGENTS.md",
		"relinked ~/.claude/CLAUDE.md to ~/.agents/AGENTS.md (Claude Code's instructions)",
		"replaced skill review, which sloppa didn't install",
		"relinked ~/.claude/skills/review to ~/.agents/skills/review (Claude Code's skills)",
	} {
		if !slices.Contains(res.Changes, want) {
			t.Errorf("no %q in %v", want, res.Changes)
		}
	}
	if m.server.fetched["same"] != 0 {
		t.Error("fetched a skill it had")
	}
	for rel, want := range map[string]string{
		".claude/CLAUDE.md":              "my old instructions",
		".agents/AGENTS.md":              "someone's AGENTS.md",
		".agents/skills/review/SKILL.md": "installed by another tool",
		".claude/skills/review/SKILL.md": "a copy in Claude's folder",
	} {
		if got := readFile(t, filepath.Join(res.Backup, rel)); got != want {
			t.Errorf("backup of %s = %q, want %q", rel, got, want)
		}
	}
	if m.read(".claude/CLAUDE.md") != "New.\n" {
		t.Error("CLAUDE.md not linked")
	}

	// It's sloppa's now: dropping it removes it.
	res = m.sync(api.SyncPlan{Skills: []api.SkillRef{review}}, false)
	if !slices.Contains(res.Changes, "removed skill same") || !slices.Contains(res.Changes, "removed ~/.agents/AGENTS.md, as there are no instructions") || m.exists(".claude/CLAUDE.md") {
		t.Errorf("dropping: %v", res.Changes)
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	skipWithoutSymlinks(t)
	m := newMachine(t)
	m.write(".claude/CLAUDE.md", "mine")
	review := m.server.put("review", md("review"))
	res := m.sync(api.SyncPlan{Instructions: "x\n", Skills: []api.SkillRef{review}}, true)
	if len(res.Changes) != 5 {
		t.Errorf("changes: %v", res.Changes)
	}
	if m.exists(".agents") || m.read(".claude/CLAUDE.md") != "mine" || m.server.fetched["review"] != 0 {
		t.Error("a dry run changed things")
	}
}

// The skills CLI's setups often link Claude's whole skills folder to ~/.agents/skills: then there's
// nothing to link per skill, and nothing must be moved aside.
func TestClaudeSkillsLinkedToAgentsSkills(t *testing.T) {
	skipWithoutSymlinks(t)
	m := newMachine(t)
	os.MkdirAll(m.path(".agents/skills"), 0o755)
	if err := os.Symlink(m.path(".agents/skills"), m.path(".claude/skills")); err != nil {
		t.Fatal(err)
	}
	review := m.server.put("review", md("review"))
	res := m.sync(api.SyncPlan{Skills: []api.SkillRef{review}}, false)
	if !slices.Equal(res.Changes, []string{"installed skill review"}) || m.read(".claude/skills/review/SKILL.md") != "review" {
		t.Errorf("changes: %v", res.Changes)
	}
	res = m.sync(api.SyncPlan{}, false)
	if !slices.Equal(res.Changes, []string{"removed skill review"}) {
		t.Errorf("changes: %v", res.Changes)
	}
}

func TestAFailedDownloadIsAProblem(t *testing.T) {
	m := newMachine(t)
	res := Sync(t.Context(), api.SyncPlan{Instructions: "x\n", Skills: []api.SkillRef{{Name: "gone", Hash: "abc"}}}, Options{
		Home: m.home, Fetch: m.server.fetch, Now: m.clock,
	})
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "download skill gone") {
		t.Errorf("problems: %v", res.Problems)
	}
	// The rest still went.
	if m.read(".agents/AGENTS.md") != "x\n" {
		t.Error("instructions not written")
	}
}

func TestBackupsArePruned(t *testing.T) {
	m := newMachine(t)
	for i := range backupsKept + 3 {
		m.sync(api.SyncPlan{Skills: []api.SkillRef{m.server.put("review", md(strings.Repeat("v", i+1)))}}, false)
	}
	entries, _ := os.ReadDir(m.path(".agents/.sloppa-backups"))
	if len(entries) != backupsKept {
		t.Errorf("%d backups kept", len(entries))
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
