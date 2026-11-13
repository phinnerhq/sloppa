package agents

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/api"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/db"
)

// TestMain keeps the migrations' log out of test output.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

// tarFile is a file in a repository tarball made for a test.
type tarFile struct {
	name string
	mode int64
	body string
}

// tarball is a repository as GitHub's tarball endpoint sends it: everything under one folder, and
// the commit in a global header.
func tarball(t *testing.T, commit string, files ...tarFile) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": commit}}); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		mode := f.mode
		if mode == 0 {
			mode = 0o644
		}
		tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "owner-repo-abc1234/" + f.name, Mode: mode, Size: int64(len(f.body))})
		tw.Write([]byte(f.body))
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

// fakeGitHub serves tarballs by repository, and counts the downloads.
type fakeGitHub struct {
	*httptest.Server
	mu        sync.Mutex
	repos     map[string][]byte
	downloads map[string]int
	auth      string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{repos: map[string][]byte{}, downloads: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		repo, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/repos/"), "/tarball")
		f.auth = r.Header.Get("Authorization")
		body, found := f.repos[repo]
		if !ok || !found {
			http.NotFound(w, r)
			return
		}
		f.downloads[repo]++
		w.Write(body)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGitHub) set(repo string, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repos[repo] = body
}

type fixture struct {
	svc      *Service
	accts    *accounts.Service
	github   *fakeGitHub
	user     accounts.Account
	machines []api.CreatedMachine
}

func newFixture(t *testing.T) *fixture {
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
	gh := newFakeGitHub(t)
	f := &fixture{svc: New(state, &GitHub{BaseURL: gh.URL, Token: "gh-token"}), accts: accts, github: gh, user: user}
	for _, name := range []string{"laptop", "desktop"} {
		m, err := accts.CreateMachine(t.Context(), user, name)
		if err != nil {
			t.Fatal(err)
		}
		f.machines = append(f.machines, m)
	}
	return f
}

// as is the account signed in as the nth machine.
func (f *fixture) as(t *testing.T, n int) accounts.Account {
	t.Helper()
	a, ok, err := f.accts.Authenticate(t.Context(), accounts.KindMachine, f.machines[n].Token)
	if err != nil || !ok {
		t.Fatalf("authenticate machine %d: %v %v", n, ok, err)
	}
	return a
}

func skillFiles(body string) []api.SkillFile {
	return []api.SkillFile{{Path: "SKILL.md", Content: []byte(body)}}
}

func TestFrontmatter(t *testing.T) {
	for _, tc := range []struct{ in, name, description string }{
		{"---\nname: review\ndescription: Reviews code.\n---\n# Review", "review", "Reviews code."},
		{"---\r\nname: \"quoted\"\r\ndescription: 'It''s quoted'\r\n---\r\n", "quoted", "It's quoted"},
		{"---\nname: folded\ndescription: >-\n  One line,\n  and another.\nmetadata:\n  x: y\n---\n", "folded", "One line, and another."},
		{"# No frontmatter", "", ""},
		{"---\nname: unterminated\n", "", ""},
	} {
		name, description := Frontmatter([]byte(tc.in))
		if name != tc.name || description != tc.description {
			t.Errorf("Frontmatter(%q) = %q, %q; want %q, %q", tc.in, name, description, tc.name, tc.description)
		}
	}
}

func TestSkillName(t *testing.T) {
	for _, tc := range []struct{ frontmatter, folder, want string }{
		{"review", "whatever", "review"},
		{"Code Review!", "x", "code-review"},
		{"", "My_Skill", "my_skill"},
		{"", "---", ""},
	} {
		if got := SkillName(tc.frontmatter, tc.folder); got != tc.want {
			t.Errorf("SkillName(%q, %q) = %q, want %q", tc.frontmatter, tc.folder, got, tc.want)
		}
	}
}

func TestCheckFiles(t *testing.T) {
	ok := func(files ...api.SkillFile) []api.SkillFile { return files }
	md := api.SkillFile{Path: "SKILL.md", Content: []byte("x")}
	for name, files := range map[string][]api.SkillFile{
		"no SKILL.md":   ok(api.SkillFile{Path: "README.md"}),
		"nested only":   ok(api.SkillFile{Path: "sub/SKILL.md"}),
		"absolute":      ok(md, api.SkillFile{Path: "/etc/passwd"}),
		"climbing":      ok(md, api.SkillFile{Path: "a/../../x"}),
		"dot":           ok(md, api.SkillFile{Path: "./x"}),
		"backslash":     ok(md, api.SkillFile{Path: `a\b`}),
		"twice":         ok(md, md),
		"file and dir":  ok(md, api.SkillFile{Path: "a"}, api.SkillFile{Path: "a/b"}),
		"empty segment": ok(md, api.SkillFile{Path: "a//b"}),
	} {
		if _, err := checkFiles(files); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	sorted, err := checkFiles(ok(api.SkillFile{Path: "z/run.sh", Executable: true}, md))
	if err != nil || sorted[0].Path != "SKILL.md" {
		t.Errorf("checkFiles = %v, %v", sorted, err)
	}
}

func TestPutSkillAndPlan(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	laptop, desktop := f.as(t, 0), f.as(t, 1)

	empty, err := f.svc.Plan(ctx, laptop)
	if err != nil || empty.Instructions != "" || len(empty.Skills) != 0 {
		t.Fatalf("empty plan = %+v, %v", empty, err)
	}

	if _, err := f.svc.PutSkill(ctx, f.user, "Bad Name", skillFiles("x")); !api.KindBadRequest.Is(err) {
		t.Errorf("bad name: %v", err)
	}
	k, err := f.svc.PutSkill(ctx, f.user, "review", []api.SkillFile{
		{Path: "scripts/run.sh", Executable: true, Content: []byte("#!/bin/sh\n")},
		{Path: "SKILL.md", Content: []byte("---\nname: review\ndescription: Reviews.\n---\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if k.Description != "Reviews." || k.Files != 2 || !k.Enabled || k.Source != "" || len(k.DisabledOn) != 0 {
		t.Errorf("put = %+v", k)
	}

	if err := f.svc.SetInstructions(ctx, f.user, "Be nice.\n\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetMachineInstructions(ctx, f.user, f.machines[0].Machine.ID, "On the laptop.\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetMachineInstructions(ctx, f.user, "nope", "x"); !api.KindMachineNotFound.Is(err) {
		t.Errorf("unknown machine: %v", err)
	}

	lp, _ := f.svc.Plan(ctx, laptop)
	dp, _ := f.svc.Plan(ctx, desktop)
	if lp.Instructions != "Be nice.\n\nOn the laptop.\n" || dp.Instructions != "Be nice.\n" {
		t.Errorf("instructions: %q, %q", lp.Instructions, dp.Instructions)
	}
	if len(lp.Skills) != 1 || lp.Skills[0] != (api.SkillRef{Name: "review", Hash: k.Hash}) {
		t.Errorf("laptop skills = %+v", lp.Skills)
	}
	if lp.Fingerprint == dp.Fingerprint {
		t.Error("different plans, same fingerprint")
	}

	// Off on the desktop only, then everywhere.
	if _, err := f.svc.ConfigureSkill(ctx, f.user, "review", nil, []string{f.machines[1].Machine.ID}); err != nil {
		t.Fatal(err)
	}
	if dp2, _ := f.svc.Plan(ctx, desktop); len(dp2.Skills) != 0 || dp2.Fingerprint == dp.Fingerprint {
		t.Errorf("desktop plan with the skill off = %+v", dp2)
	}
	if lp2, _ := f.svc.Plan(ctx, laptop); lp2.Fingerprint != lp.Fingerprint {
		t.Error("the laptop's plan changed with the desktop's")
	}
	if _, err := f.svc.ConfigureSkill(ctx, f.user, "review", new(false), nil); err != nil {
		t.Fatal(err)
	}
	if lp3, _ := f.svc.Plan(ctx, laptop); len(lp3.Skills) != 0 {
		t.Errorf("laptop plan with the skill off = %+v", lp3)
	}
	if _, err := f.svc.ConfigureSkill(ctx, f.user, "review", nil, []string{"nope"}); !api.KindMachineNotFound.Is(err) {
		t.Errorf("unknown machine: %v", err)
	}
	if _, err := f.svc.ConfigureSkill(ctx, f.user, "nope", new(true), nil); !api.KindSkillNotFound.Is(err) {
		t.Errorf("unknown skill: %v", err)
	}

	content, err := f.svc.SkillContent(ctx, laptop, "review")
	if err != nil || len(content.Files) != 2 || content.Hash != k.Hash || api.SkillHash(content.Files) != k.Hash {
		t.Errorf("content = %+v, %v", content, err)
	}
	if !content.Files[1].Executable {
		t.Error("lost the executable bit")
	}

	// Deleting a machine drops its own instructions and exclusions.
	o, _ := f.svc.Overview(ctx, f.user)
	if len(o.Machines) != 2 || o.Skills[0].DisabledOn[0] != f.machines[1].Machine.ID || o.Instructions != "Be nice.\n\n" {
		t.Fatalf("overview = %+v", o)
	}
	if err := f.accts.RevokeMachine(ctx, f.user, f.machines[1].Machine.ID); err != nil {
		t.Fatal(err)
	}
	o, _ = f.svc.Overview(ctx, f.user)
	if len(o.Machines) != 1 || len(o.Skills[0].DisabledOn) != 0 {
		t.Errorf("overview after revoking = %+v", o)
	}

	if err := f.svc.DeleteSkill(ctx, f.user, "review"); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.DeleteSkill(ctx, f.user, "review"); !api.KindSkillNotFound.Is(err) {
		t.Errorf("deleted twice: %v", err)
	}
	if _, err := f.svc.SkillContent(ctx, laptop, "review"); !api.KindSkillNotFound.Is(err) {
		t.Errorf("deleted's content: %v", err)
	}
}

func TestReportShowsInTheOverview(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	laptop := f.as(t, 0)
	p, _ := f.svc.Plan(ctx, laptop)
	if err := f.svc.Report(ctx, laptop, p.Fingerprint, nil, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	o, err := f.svc.Overview(ctx, f.user)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(o.Machines, func(m api.MachineAgents) bool { return m.MachineID == laptop.MachineID })
	m := o.Machines[i]
	if m.LastSync == nil || m.LastSync.Fingerprint != m.Fingerprint || m.LastSync.CLIVersion != "1.2.3" || len(m.LastSync.Problems) != 0 {
		t.Errorf("laptop = %+v", m)
	}
	if other := o.Machines[1-i]; other.LastSync != nil {
		t.Errorf("the other machine synced: %+v", other)
	}

	many := make([]string, 80)
	for i := range many {
		many[i] = strings.Repeat("x", 2000)
	}
	f.svc.Report(ctx, laptop, "old", many, "")
	o, _ = f.svc.Overview(ctx, f.user)
	m = o.Machines[i]
	if len(m.LastSync.Problems) != problemsMax || !strings.HasPrefix(m.LastSync.Problems[problemsMax-1], "…and 31 more") || len(m.LastSync.Problems[0]) > problemMax+5 {
		t.Errorf("problems kept: %d", len(m.LastSync.Problems))
	}
}

const commit1 = "1111111111111111111111111111111111111111"
const commit2 = "2222222222222222222222222222222222222222"

func TestImportFromGitHub(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.github.set("owner/repo", tarball(t, commit1,
		tarFile{name: "README.md", body: "not a skill"},
		tarFile{name: "skills/review/SKILL.md", body: "---\nname: review\ndescription: Reviews.\n---\n"},
		tarFile{name: "skills/review/scripts/check.sh", mode: 0o755, body: "#!/bin/sh"},
		tarFile{name: "skills/review/nested/SKILL.md", body: "---\nname: nested\n---\n"},
		tarFile{name: "skills/review/nested/more.md", body: "nested's"},
		tarFile{name: "skills/Other Thing/SKILL.md", body: "no frontmatter"},
		tarFile{name: "node_modules/dep/SKILL.md", body: "---\nname: dep\n---\n"},
	))

	l, err := f.svc.ListSource(ctx, f.user, "https://github.com/owner/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, k := range l.Skills {
		names = append(names, k.Name+"@"+k.Path)
	}
	if l.Source != "owner/repo" || l.Ref != commit1 || !slices.Equal(names, []string{"other-thing@skills/Other Thing", "review@skills/review", "nested@skills/review/nested"}) {
		t.Errorf("listing = %+v (%v)", l, names)
	}
	if f.github.auth != "Bearer gh-token" {
		t.Errorf("authorization %q", f.github.auth)
	}

	imported, err := f.svc.ImportSkills(ctx, f.user, "owner/repo", []string{"review", "nested"})
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 2 || imported[0].Source != "owner/repo" || imported[0].SourcePath != "skills/review" || imported[0].SourceRef != commit1 {
		t.Fatalf("imported = %+v", imported)
	}
	// The nested skill's files are its own, not the outer one's.
	review, _ := f.svc.SkillContent(ctx, f.user, "review")
	var paths []string
	for _, file := range review.Files {
		paths = append(paths, file.Path)
	}
	if !slices.Equal(paths, []string{"SKILL.md", "scripts/check.sh"}) || !review.Files[1].Executable {
		t.Errorf("review's files = %v", review.Files)
	}
	if _, err := f.svc.ImportSkills(ctx, f.user, "owner/repo", []string{"missing"}); !api.KindSourceUnavailable.Is(err) {
		t.Errorf("missing skill: %v", err)
	}
	if _, err := f.svc.ListSource(ctx, f.user, "owner/gone"); !api.KindSourceUnavailable.Is(err) {
		t.Errorf("missing repo: %v", err)
	}
	if _, err := f.svc.ListSource(ctx, f.user, "not a repo"); !api.KindBadRequest.Is(err) {
		t.Errorf("bad source: %v", err)
	}
	if l, _ := f.svc.ListSource(ctx, f.user, "owner/repo"); !l.Skills[1].Imported || l.Skills[0].Imported {
		t.Errorf("imported flags: %+v", l.Skills)
	}

	// Upstream changes review, and drops nested; a skill uploaded by hand isn't updated.
	f.svc.PutSkill(ctx, f.user, "mine", skillFiles("mine"))
	f.github.set("owner/repo", tarball(t, commit2,
		tarFile{name: "skills/review/SKILL.md", body: "---\nname: review\ndescription: Reviews better.\n---\n"},
	))
	before := f.github.downloads["owner/repo"]
	u, err := f.svc.UpdateSkills(ctx, f.user, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(u.Updated, []string{"review"}) || len(u.Unchanged) != 0 || len(u.Failed) != 1 || u.Failed[0].Name != "nested" {
		t.Errorf("updates = %+v", u)
	}
	if f.github.downloads["owner/repo"] != before+1 {
		t.Errorf("downloaded %d times for one source", f.github.downloads["owner/repo"]-before)
	}
	o, _ := f.svc.Overview(ctx, f.user)
	for _, k := range o.Skills {
		if k.Name == "review" && (k.Description != "Reviews better." || k.SourceRef != commit2) {
			t.Errorf("review after the update = %+v", k)
		}
	}
	u, _ = f.svc.UpdateSkills(ctx, f.user, []string{"review"})
	if !slices.Equal(u.Unchanged, []string{"review"}) || len(u.Updated) != 0 {
		t.Errorf("second update = %+v", u)
	}
	if _, err := f.svc.UpdateSkills(ctx, f.user, []string{"nope"}); !api.KindSkillNotFound.Is(err) {
		t.Errorf("unknown skill: %v", err)
	}

	// Uploading over an imported skill makes it a hand-made one.
	k, err := f.svc.PutSkill(ctx, f.user, "review", review.Files)
	if err != nil || k.Source != "" || k.SourceRef != "" {
		t.Errorf("uploaded over = %+v, %v", k, err)
	}
}

func TestComposeInstructions(t *testing.T) {
	for _, tc := range []struct{ shared, machine, want string }{
		{"", "", ""},
		{"a\n\n", "", "a\n"},
		{"", "  b \n", "b\n"},
		{"a\n", "b", "a\n\nb\n"},
	} {
		if got := api.ComposeInstructions(tc.shared, tc.machine); got != tc.want {
			t.Errorf("ComposeInstructions(%q, %q) = %q, want %q", tc.shared, tc.machine, got, tc.want)
		}
	}
}

func TestNormalizeSource(t *testing.T) {
	for in, want := range map[string]string{
		"owner/repo":                               "owner/repo",
		" https://github.com/owner/repo.git ":      "owner/repo",
		"github.com/owner/repo/tree/main/skills/x": "owner/repo",
		"https://www.github.com/owner/repo/":       "owner/repo",
	} {
		if got := NormalizeSource(in); got != want {
			t.Errorf("NormalizeSource(%q) = %q, want %q", in, got, want)
		}
	}
}

// A repository past the bounds is refused, rather than read into memory: here, too many skills.
func TestImportBoundsRepositories(t *testing.T) {
	f := newFixture(t)
	files := make([]tarFile, skillsMax+1)
	for i := range files {
		files[i] = tarFile{name: fmt.Sprintf("s%d/SKILL.md", i), body: "x"}
	}
	f.github.set("owner/huge", tarball(t, commit1, files...))
	if _, err := f.svc.ListSource(t.Context(), f.user, "owner/huge"); !api.KindSourceUnavailable.Is(err) || !strings.Contains(err.Error(), "too large") {
		t.Errorf("listing a huge repository: %v", err)
	}
}
