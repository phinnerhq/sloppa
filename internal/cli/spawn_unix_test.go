//go:build unix

package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func kill(pid int) { syscall.Kill(pid, syscall.SIGKILL) }

// cliProcess is `sloppa preview run` as a process of its own, for tests that signal it.
type cliProcess struct {
	cmd    *exec.Cmd
	stderr *lockedBuffer
	pids   string
	exited chan int
}

func startCLI(t *testing.T, fake *fakeAPI, env ...string) *cliProcess {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &cliProcess{stderr: &lockedBuffer{}, pids: filepath.Join(t.TempDir(), "pids"), exited: make(chan int, 1)}
	p.cmd = exec.Command(self, "preview", "run", "--", self)
	p.cmd.Env = append(os.Environ(), append([]string{roleEnv + "=cli", roleEnv + "_NEXT=devserver", "FIXTURE_PIDS=" + p.pids}, env...)...)
	p.cmd.Stderr = p.stderr
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		p.cmd.Wait()
		p.exited <- p.cmd.ProcessState.ExitCode()
	}()
	t.Cleanup(func() {
		p.cmd.Process.Kill()
		server, grandchild := p.processes()
		if server != 0 {
			syscall.Kill(-server, syscall.SIGKILL)
			kill(grandchild)
		}
	})
	fake.preview(t).next(t)
	eventually(t, "the preview to be ready; it said:\n"+p.stderr.String(), func() bool {
		server, _ := p.processes()
		return strings.Contains(p.stderr.String(), "preview_ready: ") && server != 0
	})
	return p
}

func (p *cliProcess) processes() (int, int) {
	return (&devPreview{pids: p.pids}).processes()
}

func (p *cliProcess) wait(t *testing.T) int {
	t.Helper()
	select {
	case code := <-p.exited:
		return code
	case <-time.After(30 * time.Second):
		t.Fatalf("the CLI didn't exit; it said:\n%s", p.stderr.String())
		return 0
	}
}

func TestSignalsStopTheDevServersWholeGroup(t *testing.T) {
	for _, tc := range []struct {
		signal syscall.Signal
		code   int
		says   string
	}{
		{syscall.SIGTERM, 130, "sloppa: interrupted; stopped the dev server"},
		{syscall.SIGINT, 130, "sloppa: interrupted; stopped the dev server"},
		{syscall.SIGHUP, 129, "sloppa: hung up; stopped the dev server"},
	} {
		t.Run(tc.signal.String(), func(t *testing.T) {
			fake := newFakeAPI(t)
			fake.signIn(t)
			p := startCLI(t, fake)
			server, grandchild := p.processes()

			p.cmd.Process.Signal(tc.signal)

			if code := p.wait(t); code != tc.code || !strings.Contains(p.stderr.String(), tc.says) {
				t.Errorf("exit %d: %s", code, p.stderr.String())
			}
			gone(t, server, grandchild)
			if !fake.requested("DELETE /api/previews/prev0001") {
				t.Error("didn't stop the preview on the server")
			}
		})
	}
}

func TestADevServerThatWontStopIsKilled(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	p := startCLI(t, fake, "FIXTURE_STUBBORN=1")
	server, grandchild := p.processes()

	stopping := time.Now()
	p.cmd.Process.Signal(syscall.SIGTERM)

	if code := p.wait(t); code != 130 {
		t.Errorf("exit %d: %s", code, p.stderr.String())
	}
	if took := time.Since(stopping); took < stopGrace-100*time.Millisecond {
		t.Errorf("killed after %v, before its grace period", took)
	}
	gone(t, server, grandchild)
}

func TestADevServerKilledBySignalExitsOne(t *testing.T) {
	fake := newFakeAPI(t)
	fake.signIn(t)
	d := startPreview(t, t.Context(), fake, nil)
	d.preview.next(t)
	server, grandchild := d.ready(t)

	kill(server)

	if res := d.wait(t); res.code != 1 || !strings.Contains(res.stderr, "the dev server exited (1)") {
		t.Errorf("exit %d: %s", res.code, res.stderr)
	}
	gone(t, grandchild)
}
