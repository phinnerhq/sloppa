// Package agentsync brings a machine's agent setup in line with the plan the server has for it:
// instructions in ~/.agents/AGENTS.md, linked where each agent harness reads them, and skills in
// ~/.agents/skills, linked into the harnesses that don't read that folder themselves.
//
// It only touches what it manages: the skills it installed (recorded in ~/.agents/.sloppa-sync.json),
// the skills the plan names and the instruction files. Whatever it replaces or removes is moved to
// ~/.agents/.sloppa-backups/<time>/ first, so nothing is lost.
package agentsync

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/phinnerhq/sloppa/internal/api"
)

// backupsKept is how many backups are kept, newest first.
const backupsKept = 10

// Harness is an agent harness: where its config folder is, and what in it should point at the
// shared setup.
type Harness struct {
	Name string
	// Dir is the harness's config folder. Harnesses whose folder doesn't exist are left alone.
	Dir string
	// Instructions is the file in Dir the harness reads user instructions from.
	Instructions string
	// Skills is the folder in Dir it reads skills from, when it doesn't read ~/.agents/skills
	// itself: each skill is linked there.
	Skills string
}

// Harnesses are the harnesses of the user whose home is home, as their environment says.
func Harnesses(home string, getenv func(string) string) []Harness {
	in := func(env, rel string) string {
		if dir := getenv(env); dir != "" {
			return dir
		}
		return filepath.Join(home, rel)
	}
	return []Harness{
		// Claude Code reads no user AGENTS.md and nothing under ~/.agents.
		{Name: "Claude Code", Dir: in("CLAUDE_CONFIG_DIR", ".claude"), Instructions: "CLAUDE.md", Skills: "skills"},
		// Codex and opencode read ~/.agents/skills on their own.
		{Name: "Codex", Dir: in("CODEX_HOME", ".codex"), Instructions: "AGENTS.md"},
		{Name: "opencode", Dir: filepath.Join(in("XDG_CONFIG_HOME", ".config"), "opencode"), Instructions: "AGENTS.md"},
	}
}

// Options say where and how to sync.
type Options struct {
	Home      string
	Harnesses []Harness
	// Fetch gets a skill's files, for the skills that need writing.
	Fetch func(ctx context.Context, name string) (api.SkillContent, error)
	// DryRun says what would change, changing nothing.
	DryRun bool
	Now    time.Time
}

// Result is what a sync did, or would do.
type Result struct {
	// Changes are what changed, for people, like "installed skill review".
	Changes []string
	// Problems are what couldn't be done.
	Problems []string
	// Backup is where what was replaced or removed went, "" when nothing was.
	Backup string
}

// state is what the last sync left, so the next one knows what it may remove.
type state struct {
	// Skills are the skills it installed, by name.
	Skills map[string]installed `json:"skills"`
	// Instructions tells whether it wrote ~/.agents/AGENTS.md.
	Instructions bool `json:"instructions,omitempty"`
}

type installed struct {
	// Hash is the version installed, as the server named it.
	Hash string `json:"hash"`
	// Local is a hash of the folder right after, as this machine reads it back: a folder that still
	// reads the same wasn't touched since.
	Local string `json:"local"`
}

type syncer struct {
	ctx     context.Context
	opts    Options
	agents  string
	skills  string
	result  Result
	backup  string
	backups int
}

// Sync brings the machine in line with plan. A failed step is reported and the others still go.
func Sync(ctx context.Context, plan api.SyncPlan, opts Options) Result {
	s := &syncer{ctx: ctx, opts: opts, agents: filepath.Join(opts.Home, ".agents")}
	s.skills = filepath.Join(s.agents, "skills")
	s.backup = filepath.Join(s.agents, ".sloppa-backups", opts.Now.UTC().Format("20060102T150405.000Z"))

	prev, err := s.readState()
	if err != nil {
		s.problem("read %s: %v", s.statePath(), err)
	}
	next := state{Skills: map[string]installed{}}
	next.Instructions = s.instructions(plan.Instructions, prev.Instructions)
	s.syncSkills(plan.Skills, prev, &next)
	if !opts.DryRun {
		if err := s.writeState(next); err != nil {
			s.problem("write %s: %v", s.statePath(), err)
		}
		s.pruneBackups()
	}
	if s.backups > 0 {
		s.result.Backup = s.backup
	}
	return s.result
}

func (s *syncer) change(format string, args ...any) {
	s.result.Changes = append(s.result.Changes, fmt.Sprintf(format, args...))
}

func (s *syncer) problem(format string, args ...any) {
	s.result.Problems = append(s.result.Problems, fmt.Sprintf(format, args...))
}

// tilde is p as people write it, under ~ when it's in the home folder.
func (s *syncer) tilde(p string) string {
	if rel, err := filepath.Rel(s.opts.Home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(filepath.Join("~", rel))
	}
	return p
}

func (s *syncer) statePath() string { return filepath.Join(s.agents, ".sloppa-sync.json") }

func (s *syncer) readState() (state, error) {
	st := state{Skills: map[string]installed{}}
	raw, err := os.ReadFile(s.statePath())
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return state{Skills: map[string]installed{}}, err
	}
	if st.Skills == nil {
		st.Skills = map[string]installed{}
	}
	return st, nil
}

func (s *syncer) writeState(st state) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.agents, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(s.statePath(), append(raw, '\n'), 0o644)
}

// moveAside moves p into this sync's backup, keeping its place under the home folder.
func (s *syncer) moveAside(p string) error {
	rel, err := filepath.Rel(s.opts.Home, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = filepath.Join("outside-home", filepath.Base(p))
	}
	dest := filepath.Join(s.backup, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(p, dest); err != nil {
		return err
	}
	s.backups++
	return nil
}

func (s *syncer) pruneBackups() {
	dir := filepath.Dir(s.backup)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	for len(names) > backupsKept {
		os.RemoveAll(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// instructions writes content to ~/.agents/AGENTS.md and links each harness's instructions to it.
// Blank instructions write nothing, and remove the file when an earlier sync wrote it. It's whether
// the file is sloppa's now.
func (s *syncer) instructions(content string, wrote bool) bool {
	target := filepath.Join(s.agents, "AGENTS.md")
	if content == "" {
		if !wrote {
			return false
		}
		if _, err := os.Lstat(target); err == nil {
			s.change("removed %s, as there are no instructions", s.tilde(target))
			if !s.opts.DryRun {
				if err := s.moveAside(target); err != nil {
					s.problem("remove %s: %v", s.tilde(target), err)
					return true
				}
			}
		}
		for _, h := range s.opts.Harnesses {
			s.unlinkInstructions(h, target)
		}
		return false
	}

	current, err := os.ReadFile(target)
	switch {
	case err == nil && string(current) == content:
	case err == nil || errors.Is(err, fs.ErrNotExist):
		if err == nil {
			s.change("updated %s", s.tilde(target))
		} else {
			s.change("wrote %s", s.tilde(target))
		}
		if !s.opts.DryRun {
			if err == nil && !wrote {
				// Someone else's file: kept in the backup.
				if err := copyToBackup(s, target); err != nil {
					s.problem("back up %s: %v", s.tilde(target), err)
					return wrote
				}
			}
			if err := os.MkdirAll(s.agents, 0o755); err != nil {
				s.problem("write %s: %v", s.tilde(target), err)
				return wrote
			}
			if err := writeFileAtomic(target, []byte(content), 0o644); err != nil {
				s.problem("write %s: %v", s.tilde(target), err)
				return wrote
			}
		}
	default:
		s.problem("read %s: %v", s.tilde(target), err)
		return wrote
	}
	for _, h := range s.opts.Harnesses {
		if h.Instructions != "" && isDir(h.Dir) {
			s.link(filepath.Join(h.Dir, h.Instructions), target, h.Name+"'s instructions")
		}
	}
	return true
}

// unlinkInstructions removes a harness's link to target, the instructions no longer there.
func (s *syncer) unlinkInstructions(h Harness, target string) {
	if h.Instructions == "" {
		return
	}
	p := filepath.Join(h.Dir, h.Instructions)
	if dest, err := os.Readlink(p); err == nil && sameFile(p, dest, target) {
		s.change("unlinked %s", s.tilde(p))
		if !s.opts.DryRun {
			if err := os.Remove(p); err != nil {
				s.problem("unlink %s: %v", s.tilde(p), err)
			}
		}
	}
}

// link makes p a symlink to target, moving whatever is at p aside. Where symlinks can't be made, as
// on Windows without developer mode, p becomes a copy instead.
func (s *syncer) link(p, target, what string) {
	if dest, err := os.Readlink(p); err == nil && sameFile(p, dest, target) {
		return
	}
	fi, err := os.Lstat(p)
	exists := err == nil
	if exists && fi.Mode().IsRegular() && runtime.GOOS == "windows" && sameContent(p, target) {
		return
	}
	if exists && fi.IsDir() && runtime.GOOS == "windows" {
		if l, err := localHash(p); err == nil {
			if t, err := localHash(target); err == nil && l == t {
				return
			}
		}
	}
	verb := "linked"
	if exists {
		verb = "relinked"
	}
	s.change("%s %s to %s (%s)", verb, s.tilde(p), s.tilde(target), what)
	if s.opts.DryRun {
		return
	}
	if exists {
		if err := s.moveAside(p); err != nil {
			s.problem("move %s aside: %v", s.tilde(p), err)
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.problem("link %s: %v", s.tilde(p), err)
		return
	}
	if err := os.Symlink(target, p); err != nil {
		if cerr := copyPath(target, p); cerr != nil {
			s.problem("link %s: %v; copying instead failed too: %v", s.tilde(p), err, cerr)
		}
	}
}

// syncSkills installs, updates and removes skills so that ~/.agents/skills has wanted.
func (s *syncer) syncSkills(wanted []api.SkillRef, prev state, next *state) {
	names := map[string]bool{}
	for _, ref := range wanted {
		names[ref.Name] = true
		if !api.IsSkillName(ref.Name) {
			s.problem("skip skill %q: not a name it can have on disk", ref.Name)
			continue
		}
		if done, ok := s.installSkill(ref, prev.Skills[ref.Name]); ok {
			next.Skills[ref.Name] = done
		} else if had, ok := prev.Skills[ref.Name]; ok {
			// Still sloppa's to remove later, even though updating it failed.
			next.Skills[ref.Name] = had
		}
		// No link to a folder that isn't there, as after a failed first install.
		if !s.opts.DryRun && !isDir(filepath.Join(s.skills, ref.Name)) {
			continue
		}
		for _, h := range s.opts.Harnesses {
			if s.linksSkills(h) {
				s.link(filepath.Join(h.Dir, h.Skills, ref.Name), filepath.Join(s.skills, ref.Name), h.Name+"'s skills")
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(prev.Skills)) {
		if names[name] || !api.IsSkillName(name) {
			continue
		}
		s.removeSkill(name)
	}
}

// linksSkills tells whether h needs each skill linked into its skills folder: it has one to make, and
// it isn't ~/.agents/skills itself already, through a link.
func (s *syncer) linksSkills(h Harness) bool {
	if h.Skills == "" || !isDir(h.Dir) {
		return false
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(h.Dir, h.Skills))
	if err != nil {
		return true
	}
	ours, err := filepath.EvalSymlinks(s.skills)
	return err != nil || dir != ours
}

// installSkill makes ~/.agents/skills/<name> ref's version. ok is false when that failed.
func (s *syncer) installSkill(ref api.SkillRef, had installed) (installed, bool) {
	dir := filepath.Join(s.skills, ref.Name)
	local, err := localHash(dir)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.problem("read %s: %v", s.tilde(dir), err)
		return installed{}, false
	}
	// Unchanged since sloppa installed this version, or already the very files the server has.
	if exists && (had.Hash == ref.Hash && had.Local == local || local == ref.Hash) {
		return installed{Hash: ref.Hash, Local: local}, true
	}
	switch {
	case !exists:
		s.change("installed skill %s", ref.Name)
	case had.Hash == "":
		s.change("replaced skill %s, which sloppa didn't install", ref.Name)
	case had.Hash == ref.Hash:
		s.change("restored skill %s, changed on this machine", ref.Name)
	default:
		s.change("updated skill %s", ref.Name)
	}
	if s.opts.DryRun {
		return installed{Hash: ref.Hash, Local: local}, true
	}
	content, err := s.opts.Fetch(s.ctx, ref.Name)
	if err != nil {
		s.problem("download skill %s: %v", ref.Name, err)
		return installed{}, false
	}
	if got := api.SkillHash(content.Files); got != ref.Hash {
		s.problem("download skill %s: got version %.12s, expected %.12s; sync again", ref.Name, got, ref.Hash)
		return installed{}, false
	}
	// A fresh folder of its own, made by this sync: nothing left or put there before is reused.
	if err := os.MkdirAll(s.skills, 0o755); err != nil {
		s.problem("write skill %s: %v", ref.Name, err)
		return installed{}, false
	}
	staging, err := os.MkdirTemp(s.skills, "."+ref.Name+".sloppa-new-*")
	if err != nil {
		s.problem("write skill %s: %v", ref.Name, err)
		return installed{}, false
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		os.RemoveAll(staging)
		s.problem("write skill %s: %v", ref.Name, err)
		return installed{}, false
	}
	if err := writeSkill(staging, content.Files); err != nil {
		os.RemoveAll(staging)
		s.problem("write skill %s: %v", ref.Name, err)
		return installed{}, false
	}
	if exists {
		if err := s.moveAside(dir); err != nil {
			os.RemoveAll(staging)
			s.problem("move %s aside: %v", s.tilde(dir), err)
			return installed{}, false
		}
	} else if fi, err := os.Lstat(dir); err == nil && !fi.IsDir() {
		// A file or a dangling link where the folder goes.
		if err := s.moveAside(dir); err != nil {
			os.RemoveAll(staging)
			s.problem("move %s aside: %v", s.tilde(dir), err)
			return installed{}, false
		}
	}
	if err := os.Rename(staging, dir); err != nil {
		os.RemoveAll(staging)
		s.problem("install skill %s: %v", ref.Name, err)
		return installed{}, false
	}
	local, err = localHash(dir)
	if err != nil {
		s.problem("read %s back: %v", s.tilde(dir), err)
		return installed{}, false
	}
	return installed{Hash: ref.Hash, Local: local}, true
}

// removeSkill takes away a skill sloppa installed, and the harness links to it.
func (s *syncer) removeSkill(name string) {
	dir := filepath.Join(s.skills, name)
	for _, h := range s.opts.Harnesses {
		if !s.linksSkills(h) {
			continue
		}
		p := filepath.Join(h.Dir, h.Skills, name)
		if dest, err := os.Readlink(p); err == nil && sameFile(p, dest, dir) {
			if !s.opts.DryRun {
				if err := os.Remove(p); err != nil {
					s.problem("unlink %s: %v", s.tilde(p), err)
				}
			}
		} else if fi, err := os.Lstat(p); err == nil && fi.IsDir() && runtime.GOOS == "windows" {
			// The copy made where links couldn't be.
			if !s.opts.DryRun {
				if err := s.moveAside(p); err != nil {
					s.problem("remove %s: %v", s.tilde(p), err)
				}
			}
		}
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return
	}
	s.change("removed skill %s", name)
	if !s.opts.DryRun {
		if err := s.moveAside(dir); err != nil {
			s.problem("remove skill %s: %v", name, err)
		}
	}
}

// writeSkill writes files into dir, a new empty folder.
func writeSkill(dir string, files []api.SkillFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, f := range files {
		name := filepath.FromSlash(f.Path)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%q isn't a path inside the skill", f.Path)
		}
		if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if f.Executable {
			mode = 0o755
		}
		if err := root.WriteFile(name, f.Content, mode); err != nil {
			return err
		}
	}
	return nil
}

// localHash is api.SkillHash of the folder at dir as this machine reads it: its regular files, links
// in it left out. It's fs.ErrNotExist when there's no folder.
func localHash(dir string) (string, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fs.ErrNotExist
	}
	var files []api.SkillFile
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		files = append(files, api.SkillFile{Path: filepath.ToSlash(rel), Executable: info.Mode()&0o100 != 0, Content: content})
		return nil
	})
	if err != nil {
		return "", err
	}
	slices.SortFunc(files, func(a, b api.SkillFile) int { return cmp.Compare(a.Path, b.Path) })
	return api.SkillHash(files), nil
}

// sameFile tells whether dest, the target of the link at p, is target.
func sameFile(p, dest, target string) bool {
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(p), dest)
	}
	return filepath.Clean(dest) == filepath.Clean(target)
}

func sameContent(a, b string) bool {
	x, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	y, err := os.ReadFile(b)
	return err == nil && string(x) == string(y)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func writeFileAtomic(p string, content []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// copyToBackup copies p into the backup, leaving it where it is.
func copyToBackup(s *syncer, p string) error {
	rel, err := filepath.Rel(s.opts.Home, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = filepath.Join("outside-home", filepath.Base(p))
	}
	if err := copyPath(p, filepath.Join(s.backup, rel)); err != nil {
		return err
	}
	s.backups++
	return nil
}

// copyPath copies the file or folder at src to dst.
func copyPath(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		content, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, content, fi.Mode().Perm())
	}
	return os.CopyFS(dst, os.DirFS(src))
}
