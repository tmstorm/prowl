//go:build !windows

package platform

import (
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
