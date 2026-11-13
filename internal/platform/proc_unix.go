//go:build !windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Alive reports whether a process exists.
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// Detach makes cmd outlive the parent (own session, no controlling terminal).
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// Restart replaces this process with a fresh run of exe, same arguments and
// environment, keeping its process ID for the service manager. It returns
// only on failure.
func Restart(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}

// Reopen replaces this terminal program with a fresh run of exe in the same
// terminal. It returns only on failure.
func Reopen(exe string) error { return Restart(exe) }

// Lock is an exclusive lock on a file, held until Unlock or until the process
// ends, however it ends.
type Lock struct{ f *os.File }

// TryLock takes the lock on path, creating the file, or returns ErrLocked
// while another process holds it. The file is closed on exec, so a process
// that restarts itself in place gives the lock up to its new self.
func TryLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{f}, nil
}

// Unlock gives the lock up.
func (l *Lock) Unlock() { _ = l.f.Close() }
