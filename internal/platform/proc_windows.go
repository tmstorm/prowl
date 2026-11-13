//go:build windows

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

// Reopen runs exe again in this console with the same arguments and exits
// with its status once it is done: Windows can't replace a running process.
func Reopen(exe string) error {
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		return err
	}
	os.Exit(0)
	return nil
}

// Lock is an exclusive lock on a file, held until Unlock or until the process
// ends, however it ends.
type Lock struct{ h syscall.Handle }

// TryLock takes the lock on path, creating the file, or returns ErrLocked
// while another process holds it: the file is opened with no sharing, so a
// second open fails with ERROR_SHARING_VIOLATION. Child processes don't
// inherit it.
func TryLock(path string) (*Lock, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, syscall.Errno(32)) {
		return nil, ErrLocked
	}
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{h}, nil
}

// Unlock gives the lock up.
func (l *Lock) Unlock() { _ = syscall.CloseHandle(l.h) }
