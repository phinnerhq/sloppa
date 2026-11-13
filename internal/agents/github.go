package agents

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/phinnerhq/sloppa/internal/api"
)

// archiveMax bounds a repository's tarball, compressed.
const archiveMax = 256 << 20

// fetchTimeout bounds downloading one.
const fetchTimeout = 2 * time.Minute

// repoSkill is a skill found in a repository: a folder with a SKILL.md, and every file under it but
// those of skills nested in it.
type repoSkill struct {
	name, description, path string
	files                   []api.SkillFile
	// err is why the skill can't be imported as it is, such as being too large.
	err error
}

// repository is a GitHub repository's skills at one commit.
type repository struct {
	source, ref string
	skills      []repoSkill
}

func (r repository) skill(name string) (repoSkill, bool) {
	i := slices.IndexFunc(r.skills, func(s repoSkill) bool { return s.name == name })
	if i < 0 {
		return repoSkill{}, false
	}
	return r.skills[i], true
}

// GitHub reads repositories from GitHub's API, or another server that answers like it.
type GitHub struct {
	// BaseURL is like https://api.github.com, without a trailing slash.
	BaseURL string
	// Token, if set, authenticates the downloads.
	Token  string
	Client *http.Client
}

// skipped are folders never searched for skills: dependencies and version control.
var skipped = map[string]bool{"node_modules": true, ".git": true}

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// fetch downloads source's default branch and finds the skills in it. The tarball goes to a temporary
// file and is read twice: once to find the skills, once for their files only.
func (g *GitHub) fetch(ctx context.Context, source string) (repository, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.BaseURL+"/repos/"+source+"/tarball", nil)
	if err != nil {
		return repository{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "sloppa")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return repository{}, api.SourceUnavailable(fmt.Sprintf("Couldn't reach GitHub for %s: %v", source, err))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		hint := ""
		if g.Token == "" {
			hint = " (private repositories need GITHUB_TOKEN on the server)"
		}
		return repository{}, api.SourceUnavailable(fmt.Sprintf("GitHub has no repository %s%s", source, hint))
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return repository{}, api.SourceUnavailable(fmt.Sprintf("GitHub refused to send %s, likely its rate limit: try again later, or set GITHUB_TOKEN on the server", source))
	case resp.StatusCode != http.StatusOK:
		return repository{}, api.SourceUnavailable(fmt.Sprintf("GitHub answered %s for %s", resp.Status, source))
	}

	tmp, err := os.CreateTemp("", "sloppa-skills-*.tar.gz")
	if err != nil {
		return repository{}, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, archiveMax+1))
	if err != nil {
		return repository{}, api.SourceUnavailable(fmt.Sprintf("Couldn't download %s: %v", source, err))
	}
	if n > archiveMax {
		return repository{}, api.SourceUnavailable(fmt.Sprintf("%s is larger than %d MiB compressed, too large to read skills from", source, archiveMax>>20))
	}
	repo, err := readArchive(tmp, source)
	if err != nil {
		return repository{}, api.SourceUnavailable(fmt.Sprintf("Couldn't read %s's archive: %v", source, err))
	}
	return repo, nil
}

// walk calls f with each regular file of the tarball in r, its path without the top folder GitHub
// puts everything in, and returns the commit the archive says it is of.
func walk(r io.ReadSeeker, f func(name string, h *tar.Header, tr *tar.Reader) error) (ref string, err error) {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return ref, nil
		}
		if err != nil {
			return "", err
		}
		// git archive puts the commit in a global header's comment.
		if h.Typeflag == tar.TypeXGlobalHeader {
			if c := h.PAXRecords["comment"]; commitSHA.MatchString(c) {
				ref = c
			}
			continue
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		_, name, ok := strings.Cut(strings.TrimPrefix(h.Name, "./"), "/")
		if !ok || name == "" || slices.ContainsFunc(strings.Split(name, "/"), func(p string) bool { return skipped[p] }) {
			continue
		}
		if err := f(name, h, tr); err != nil {
			return "", err
		}
	}
}

func readArchive(r io.ReadSeeker, source string) (repository, error) {
	// First the skills' folders and SKILL.md files.
	found := map[string][]byte{}
	ref, err := walk(r, func(name string, h *tar.Header, tr *tar.Reader) error {
		if path.Base(name) != SkillFile || h.Size > api.SkillBytesMax {
			return nil
		}
		content, err := io.ReadAll(tr)
		found[path.Dir(name)] = content
		return err
	})
	if err != nil {
		return repository{}, err
	}
	// Then each folder's files, going to the deepest skill folder holding them.
	owner := func(name string) (string, bool) {
		for dir := path.Dir(name); ; dir = path.Dir(dir) {
			if _, ok := found[dir]; ok {
				return dir, true
			}
			if dir == "." {
				return "", false
			}
		}
	}
	files := map[string][]api.SkillFile{}
	sizes := map[string]int64{}
	tooLarge := map[string]bool{}
	_, err = walk(r, func(name string, h *tar.Header, tr *tar.Reader) error {
		dir, ok := owner(name)
		if !ok || tooLarge[dir] {
			return nil
		}
		sizes[dir] += h.Size
		if sizes[dir] > api.SkillBytesMax || len(files[dir]) >= api.SkillFilesMax {
			tooLarge[dir] = true
			files[dir] = nil
			return nil
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(name, dir+"/")
		if dir == "." {
			rel = name
		}
		files[dir] = append(files[dir], api.SkillFile{Path: rel, Executable: h.Mode&0o111 != 0, Content: content})
		return nil
	})
	if err != nil {
		return repository{}, err
	}

	repo := repository{source: source, ref: ref}
	names := map[string]bool{}
	for _, dir := range slices.Sorted(maps.Keys(found)) {
		fmName, description := Frontmatter(found[dir])
		folder := path.Base(dir)
		if dir == "." {
			folder = path.Base(source)
		}
		name := SkillName(fmName, folder)
		if name == "" || names[name] {
			continue
		}
		names[name] = true
		s := repoSkill{name: name, description: description, path: dir}
		if dir == "." {
			s.path = ""
		}
		if tooLarge[dir] {
			s.err = api.PayloadTooLarge(fmt.Sprintf("%s in %s is larger than a skill may be", name, source))
		} else if s.files, err = checkFiles(files[dir]); err != nil {
			s.err = err
		}
		repo.skills = append(repo.skills, s)
	}
	return repo, nil
}
