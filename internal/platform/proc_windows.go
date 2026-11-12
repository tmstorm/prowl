//go:build windows

package platform

import (
	"os"
	"os/exec"
	"syscall"
)

// Alive reports whether a process exists.
func Alive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}

// Detach makes cmd outlive the parent.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008}
}

// Restart starts a fresh run of exe with the same arguments; the caller
// exits after it returns nil.
func Restart(exe string) error {
	cmd := exec.Command(exe, os.Args[1:]...)
	Detach(cmd)
	return cmd.Start()
}
