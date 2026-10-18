//go:build windows

package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/windows"
)

const stillActive = 259

func alive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code == stillActive
}

func kill(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
	}
}

// Windows alone has .cmd shims to run.
func TestAwkwardArgumentsGetThroughARealCmdShimIntact(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "sloppa spawn")
	os.MkdirAll(dir, 0o700)
	// Like the shims npm and pnpm install: a batch file passing %* on to a program.
	os.WriteFile(filepath.Join(dir, "echo-args.cmd"), []byte("@\""+self+"\" %*\r\n"), 0o600)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(roleEnv, "echoargs")

	spawn := resolveSpawnCommand("echo-args", awkward, "windows", os.Getenv, isFile)
	if !spawn.shell {
		t.Fatalf("%+v", spawn)
	}
	out, err := command(spawn).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(out, &got); err != nil || !slices.Equal(got, awkward) {
		t.Errorf("%v: got %q, want %q", err, got, awkward)
	}
}
