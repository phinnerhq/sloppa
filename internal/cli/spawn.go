package cli

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

// devServer is the command a preview runs, with everything it starts: its own process group on
// macOS and Linux, a job object on Windows, so stopping it stops all of them.
type devServer struct {
	cmd  *exec.Cmd
	done chan struct{}
	tree processTree
}

// startDevServer starts args with env, on this command's own stdin, stdout and stderr: a terminal
// stays one for the dev server.
func startDevServer(args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) (*devServer, error) {
	cmd := command(resolveSpawnCommand(args[0], args[1:], runtime.GOOS, os.Getenv, isFile))
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	prepare(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	d := &devServer{cmd: cmd, done: make(chan struct{})}
	tree, err := attach(cmd)
	if err != nil {
		cmd.Wait()
		return nil, err
	}
	d.tree = tree
	go func() {
		cmd.Wait()
		close(d.done)
	}()
	return d, nil
}

// Done is closed once the dev server's own process has exited.
func (d *devServer) Done() <-chan struct{} { return d.done }

// exitCode is the dev server's, once it's done; 1 when a signal ended it.
func (d *devServer) exitCode() int {
	if code := d.cmd.ProcessState.ExitCode(); code >= 0 {
		return code
	}
	return 1
}

// stop asks the dev server and everything it started to exit with sig, and kills whatever is left
// after grace. It returns once the dev server's own process is gone.
func (d *devServer) stop(sig os.Signal, grace time.Duration) {
	defer d.tree.release()
	if d.tree.signal(sig) {
		deadline := time.NewTimer(grace)
		defer deadline.Stop()
		d.waitGone(deadline.C)
	}
	d.tree.kill()
	<-d.done
}

// waitGone waits until the dev server and its group are gone, or the deadline passes.
func (d *devServer) waitGone(deadline <-chan time.Time) {
	select {
	case <-d.done:
	case <-deadline:
		return
	}
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for d.tree.alive() {
		select {
		case <-tick.C:
		case <-deadline:
			return
		}
	}
}
