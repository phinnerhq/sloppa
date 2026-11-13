//go:build unix

package agentsync

import (
	"os"

	"golang.org/x/sys/unix"
)

// lock takes the lock file at p for this process, without waiting; unlock gives it back. The OS
// drops it if the process dies.
func lock(p string) (unlock func(), err error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}, nil
}
