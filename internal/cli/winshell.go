package cli

import (
	"os"
	"regexp"
	"slices"
	"strings"
)

// spawnCommand is how to start a command so it runs the way a terminal would, after T3 Code's
// `resolveSpawnCommand` (packages/shared/src/shell.ts).
//
// On macOS and Linux that's the command itself. On Windows, `pnpm`, `npm`, `yarn`, `vite` and most
// other dev tools are `.cmd` shims, which only cmd.exe runs, and which take their arguments the way
// cmd.exe parses them rather than as a program would (the "BatBadBut" bug Go's own escaping leaves
// to its callers). So the command is looked up on PATH like cmd.exe would, and a `.cmd` or `.bat`
// runs through cmd.exe as one command line, escaped for it.
type spawnCommand struct {
	command string
	args    []string
	// shell says command is a whole cmd.exe command line, to run as `cmd.exe /d /s /c "<command>"`.
	shell bool
}

// windowsShellMeta is cmd.exe's metacharacters, caret-escaped outside and inside quotes alike.
var windowsShellMeta = regexp.MustCompile("([()\\][%!^\"`<>&|;, *?])")

func escapeMetaChars(text string) string { return windowsShellMeta.ReplaceAllString(text, "^${1}") }

var (
	backslashesBeforeQuote = regexp.MustCompile(`(\\*)"`)
	trailingBackslashes    = regexp.MustCompile(`(\\*)$`)
)

// escapeWindowsShellArg is one argument for a cmd.exe command line, read back as itself by the
// program it starts.
func escapeWindowsShellArg(arg string) string {
	escaped := backslashesBeforeQuote.ReplaceAllString(arg, `${1}${1}\"`)
	// Double up trailing backslashes so the closing quote is not escaped away.
	escaped = trailingBackslashes.ReplaceAllString(escaped, "${1}${1}")
	return escapeMetaChars(`"` + escaped + `"`)
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// windowsExt is path.extname for a Windows path: the base name's extension, if it has one besides a
// leading dot.
func windowsExt(path string) string {
	base := path[strings.LastIndexAny(path, `\/`)+1:]
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		return base[i:]
	}
	return ""
}

func windowsJoin(dir, name string) string { return strings.TrimRight(dir, `\/`) + `\` + name }

func pathExtensions(getenv func(string) string) []string {
	var listed []string
	for entry := range strings.SplitSeq(getenv("PATHEXT"), ";") {
		entry = strings.ToUpper(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if !strings.HasPrefix(entry, ".") {
			entry = "." + entry
		}
		if !slices.Contains(listed, entry) {
			listed = append(listed, entry)
		}
	}
	if len(listed) == 0 {
		return []string{".COM", ".EXE", ".BAT", ".CMD"}
	}
	return listed
}

// resolveWindowsExecutable is where cmd.exe would find command: as given if it has a known
// extension, else with each of PATHEXT's; "" if nowhere.
func resolveWindowsExecutable(command string, getenv func(string) string, exists func(string) bool) string {
	extensions := pathExtensions(getenv)
	var candidates []string
	if slices.Contains(extensions, strings.ToUpper(windowsExt(command))) {
		candidates = []string{command}
	} else {
		// Both cases, for the case-sensitive folders Windows can have too.
		for _, ext := range extensions {
			candidates = append(candidates, command+ext, command+strings.ToLower(ext))
		}
	}
	if strings.ContainsAny(command, `\/`) {
		for _, candidate := range candidates {
			if exists(candidate) {
				return candidate
			}
		}
		return ""
	}
	path := getenv("PATH")
	for _, name := range []string{"Path", "path"} {
		if path == "" {
			path = getenv(name)
		}
	}
	for entry := range strings.SplitSeq(path, ";") {
		dir := strings.TrimSpace(entry)
		if len(dir) >= 2 && dir[0] == '"' && dir[len(dir)-1] == '"' {
			dir = dir[1 : len(dir)-1]
		}
		if dir == "" {
			continue
		}
		for _, candidate := range candidates {
			if found := windowsJoin(dir, candidate); exists(found) {
				return found
			}
		}
	}
	return ""
}

func resolveSpawnCommand(command string, args []string, goos string, getenv func(string) string, exists func(string) bool) spawnCommand {
	if goos != "windows" {
		return spawnCommand{command: command, args: args}
	}
	resolved := resolveWindowsExecutable(command, getenv, exists)
	if resolved == "" {
		resolved = command
	}
	if ext := strings.ToLower(windowsExt(resolved)); ext != ".cmd" && ext != ".bat" {
		return spawnCommand{command: resolved, args: args}
	}
	// The shim hands its arguments on with %*, which cmd.exe parses once more: so they're escaped
	// twice, like cross-spawn does, or a quote inside one turns the next one's `|` loose.
	line := []string{escapeWindowsShellArg(resolved)}
	for _, arg := range args {
		line = append(line, escapeMetaChars(escapeWindowsShellArg(arg)))
	}
	return spawnCommand{command: strings.Join(line, " "), shell: true}
}
