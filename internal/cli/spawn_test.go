package cli

import (
	"slices"
	"testing"
)

// awkward is arguments a dev command can plausibly get, and every character cmd.exe would
// otherwise act on.
var awkward = []string{
	"plain",
	"two words",
	"--base=/some path/",
	`quote"inside`,
	"amp & pipe | less < more >",
	"caret ^ and percent %PATH% %",
	"(parens) [brackets] ; , * ?",
	"!bang!",
	`trailing\`,
	`C:\Program Files\x\"`,
	"",
}

func windowsEnv(name string) string {
	return map[string]string{"PATH": `C:\tools;C:\Program Files\nodejs`, "PATHEXT": ".COM;.EXE;.BAT;.CMD"}[name]
}

func TestSpawnLeavesCommandsAloneOutsideWindows(t *testing.T) {
	got := resolveSpawnCommand("pnpm", []string{"dev"}, "linux", windowsEnv, func(string) bool { return true })
	if got.command != "pnpm" || !slices.Equal(got.args, []string{"dev"}) || got.shell {
		t.Errorf("%+v", got)
	}
}

func TestSpawnRunsAWindowsExeDirectlyFoundOnPath(t *testing.T) {
	exists := func(path string) bool { return path == `C:\Program Files\nodejs\node.exe` }
	got := resolveSpawnCommand("node", []string{"server.js"}, "windows", windowsEnv, exists)
	if got.command != `C:\Program Files\nodejs\node.exe` || !slices.Equal(got.args, []string{"server.js"}) || got.shell {
		t.Errorf("%+v", got)
	}
}

func TestSpawnRunsACmdShimThroughCmdAsOneEscapedLine(t *testing.T) {
	exists := func(path string) bool { return path == `C:\Program Files\nodejs\pnpm.CMD` }
	got := resolveSpawnCommand("pnpm", []string{"dev", "--port", "3000"}, "windows", windowsEnv, exists)
	// The arguments twice, since the shim's %* is parsed again.
	want := `^"C:\Program^ Files\nodejs\pnpm.CMD^" ^^^"dev^^^" ^^^"--port^^^" ^^^"3000^^^"`
	if got.command != want || len(got.args) != 0 || !got.shell {
		t.Errorf("%+v, want %s", got, want)
	}
}

func TestSpawnFindsPathsWithExtensionsAndWithout(t *testing.T) {
	exists := func(path string) bool { return path == `.\bin\dev.bat` || path == `C:\tools\vite.cmd` }
	if got := resolveSpawnCommand(`.\bin\dev`, nil, "windows", windowsEnv, exists); got.command != `^".\bin\dev.bat^"` || !got.shell {
		t.Errorf("relative path: %+v", got)
	}
	// Lower case, as a case-sensitive folder may hold it.
	if got := resolveSpawnCommand("vite", nil, "windows", windowsEnv, exists); got.command != `^"C:\tools\vite.cmd^"` {
		t.Errorf("lower case extension: %+v", got)
	}
	if got := resolveSpawnCommand("missing", []string{"x"}, "windows", windowsEnv, exists); got.command != "missing" || got.shell {
		t.Errorf("not found: %+v", got)
	}
	defaults := resolveSpawnCommand("vite", nil, "windows", func(name string) string {
		if name == "Path" {
			return `"C:\tools"`
		}
		return ""
	}, exists)
	if defaults.command != `^"C:\tools\vite.cmd^"` {
		t.Errorf("default PATHEXT, quoted Path: %+v", defaults)
	}
}

func TestEscapeWindowsShellArg(t *testing.T) {
	for arg, want := range map[string]string{
		"a & b":    `^"a^ ^&^ b^"`,
		`say "hi"`: `^"say^ \^"hi\^"^"`,
		`dir\`:     `^"dir\\^"`,
	} {
		if got := escapeWindowsShellArg(arg); got != want {
			t.Errorf("escapeWindowsShellArg(%q) = %s, want %s", arg, got, want)
		}
	}
}
