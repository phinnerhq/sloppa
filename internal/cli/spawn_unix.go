//go:build unix

package cli

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func command(s spawnCommand) *exec.Cmd { return exec.Command(s.command, s.args...) }

// prepare starts the dev server in a session of its own, which makes it a process group leader:
// the group is everything it starts, for signals to reach at once. A new session rather than only a
// group keeps it off the terminal's job control, so a dev server reading the terminal, like Vite's
// shortcuts do, isn't stopped for reading from the background; Node's `detached` does the same.
// Ctrl-C reaches this command alone, then, which hands it on.
func prepare(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// processTree is the dev server's process group, by its id: the dev server's pid.
type processTree struct{ group int }

func attach(cmd *exec.Cmd) (processTree, error) { return processTree{group: cmd.Process.Pid}, nil }

// signal sends sig to the whole group, and tells whether anyone was there to get it.
func (t processTree) signal(sig os.Signal) bool {
	s, ok := sig.(syscall.Signal)
	if !ok {
		s = syscall.SIGTERM
	}
	return syscall.Kill(-t.group, s) == nil
}

// alive tells whether anything is left in the group, even if the dev server itself exited.
func (t processTree) alive() bool {
	err := syscall.Kill(-t.group, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (t processTree) kill() { syscall.Kill(-t.group, syscall.SIGKILL) }

func (t processTree) release() {}
