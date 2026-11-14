// Package agents is the agent setup an account keeps on the server for `sloppa agents sync` to bring
// to its machines: instructions, shared and per machine, and skills, uploaded by hand or imported
// from GitHub repositories, each turned on or off per machine.
package agents

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/phinnerhq/sloppa/internal/api"
)

// SkillFile is the file every skill has at its root.
const SkillFile = "SKILL.md"

// descriptionMax bounds the description kept from a SKILL.md.
const descriptionMax = 1024

// cleanPath is a file's path within its skill, or an error saying why it can't be one: relative,
// forward slashes, nothing that climbs out or hides as `.` or `..`.
func cleanPath(p string) (string, error) {
	if p == "" || strings.ContainsAny(p, "\x00\\") || !utf8.ValidString(p) || len(p) > 512 {
		return "", fmt.Errorf("%q isn't a file path", p)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%q must be relative to the skill's folder", p)
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("%q must be a plain relative path, without empty, . or .. parts", p)
		}
	}
	return p, nil
}

// checkFiles is files sorted by path, or a bad request saying what's wrong with them: one per path,
// a SKILL.md at the root, within the limits, and no file where another needs a folder.
func checkFiles(files []api.SkillFile) ([]api.SkillFile, error) {
	if len(files) == 0 {
		return nil, api.BadRequest("A skill needs files, a " + SkillFile + " at least")
	}
	if len(files) > api.SkillFilesMax {
		return nil, api.PayloadTooLarge(fmt.Sprintf("A skill has at most %d files", api.SkillFilesMax))
	}
	files = slices.Clone(files)
	var size int
	for i, f := range files {
		p, err := cleanPath(f.Path)
		if err != nil {
			return nil, api.BadRequest(err.Error())
		}
		files[i].Path = p
		size += len(f.Content)
	}
	if size > api.SkillBytesMax {
		return nil, api.PayloadTooLarge(fmt.Sprintf("A skill's files add up to at most %d MiB", api.SkillBytesMax>>20))
	}
	slices.SortFunc(files, func(a, b api.SkillFile) int { return cmp.Compare(a.Path, b.Path) })
	// Paths are compared ignoring case, as macOS and Windows disks do: SKILL.md and skill.md would be
	// one file there.
	paths := map[string]string{}
	for _, f := range files {
		if other, ok := paths[foldCase(f.Path)]; ok {
			if other == f.Path {
				return nil, api.BadRequest(fmt.Sprintf("%q comes twice", f.Path))
			}
			return nil, api.BadRequest(fmt.Sprintf("%q and %q differ only in case, and would be one file on many disks", other, f.Path))
		}
		paths[foldCase(f.Path)] = f.Path
	}
	for _, f := range files {
		for dir := path.Dir(f.Path); dir != "."; dir = path.Dir(dir) {
			if other, ok := paths[foldCase(dir)]; ok {
				return nil, api.BadRequest(fmt.Sprintf("%q is a file, and a folder of %q", other, f.Path))
			}
		}
	}
	if paths[foldCase(SkillFile)] != SkillFile {
		return nil, api.BadRequest("A skill needs a " + SkillFile + " at the root of its folder")
	}
	return files, nil
}

func foldCase(p string) string { return strings.ToLower(p) }

// Frontmatter is the name and description a SKILL.md's YAML frontmatter gives, "" for those it
// doesn't. Only the plain and folded forms skills use are read, not YAML at large.
func Frontmatter(skillMD []byte) (name, description string) {
	text := strings.ReplaceAll(string(skillMD), "\r\n", "\n")
	text = strings.TrimPrefix(text, "\ufeff")
	if !strings.HasPrefix(text, "---\n") {
		return "", ""
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return "", ""
	}
	lines := strings.Split(text[4:4+end], "\n")
	fields := map[string]string{}
	for i := 0; i < len(lines); i++ {
		key, value, ok := strings.Cut(lines[i], ":")
		if !ok || key == "" || key[0] == ' ' || key[0] == '\t' {
			continue
		}
		value = strings.TrimSpace(value)
		// A block scalar: the indented lines that follow, folded into one.
		if value != "" && (value[0] == '|' || value[0] == '>') {
			var parts []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t") || strings.TrimSpace(lines[i+1]) == "") {
				i++
				if s := strings.TrimSpace(lines[i]); s != "" {
					parts = append(parts, s)
				}
			}
			value = strings.Join(parts, " ")
		} else {
			value = unquote(value)
		}
		fields[strings.TrimSpace(key)] = value
	}
	description = strings.TrimSpace(fields["description"])
	if utf8.RuneCountInString(description) > descriptionMax {
		description = string([]rune(description)[:descriptionMax-1]) + "…"
	}
	return strings.TrimSpace(fields["name"]), description
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		inner := v[1 : len(v)-1]
		if v[0] == '\'' {
			return strings.ReplaceAll(inner, "''", "'")
		}
		return strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n").Replace(inner)
	}
	return v
}

// SkillName is the name a skill goes by: its frontmatter's if that's a valid one, otherwise its
// folder's made into one, lowercase with dashes for anything else.
func SkillName(frontmatterName, folder string) string {
	if api.IsSkillName(frontmatterName) {
		return frontmatterName
	}
	for _, candidate := range []string{frontmatterName, folder} {
		var b strings.Builder
		for _, r := range strings.ToLower(candidate) {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
				b.WriteRune(r)
			} else {
				b.WriteByte('-')
			}
		}
		name := strings.Trim(b.String(), "-._")
		if len(name) > api.SkillNameMax {
			name = strings.TrimRight(name[:api.SkillNameMax], "-._")
		}
		if api.IsSkillName(name) {
			return name
		}
	}
	return ""
}

// skillMD is the SKILL.md among files, sorted ones or not.
func skillMD(files []api.SkillFile) []byte {
	for _, f := range files {
		if f.Path == SkillFile {
			return f.Content
		}
	}
	return nil
}

func totalSize(files []api.SkillFile) int64 {
	var n int64
	for _, f := range files {
		n += int64(len(f.Content))
	}
	return n
}
