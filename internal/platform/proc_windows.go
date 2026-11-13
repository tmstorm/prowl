//go:build windows

package platform

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
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

// Detach makes cmd outlive the parent. CREATE_NO_WINDOW gives it a console
// of its own with no window, which the git, gh and claude it starts share;
// DETACHED_PROCESS left it with none, so each of those flashed a new window.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
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

// RemoveBinary deletes prowl.exe at exe once this prowl exits, since Windows
// won't delete a running program: a hidden PowerShell retries the delete for a
// minute. When the folder holds nothing else, it goes too and comes off your
// PATH, where install.ps1 put it. The script goes in -EncodedCommand, so no
// quoting rules apply to the paths.
func RemoveBinary(exe string) error {
	dir := filepath.Dir(exe)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("remove %s: %w", exe, err)
	}
	name := filepath.Base(exe)
	old := strings.TrimSuffix(name, ".exe") + ".old.exe"
	alone := true
	for _, e := range entries {
		if !strings.EqualFold(e.Name(), name) && !strings.EqualFold(e.Name(), old) {
			alone = false
		}
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	script := fmt.Sprintf(`$exe = %s; for ($i = 0; $i -lt 60 -and (Test-Path -LiteralPath $exe); $i++) { Start-Sleep 1; Remove-Item -LiteralPath $exe -Force -ErrorAction SilentlyContinue }`, quote(exe))
	if alone {
		path := fmt.Sprintf(`$d = %s; $p = [Environment]::GetEnvironmentVariable('Path', 'User'); `+
			`[Environment]::SetEnvironmentVariable('Path', (($p -split ';') | Where-Object { $_ -and $_ -ne $d }) -join ';', 'User')`, quote(dir))
		if err := exec.Command("powershell", "-NoProfile", "-Command", path).Run(); err != nil {
			return fmt.Errorf("take %s off PATH: %w", dir, err)
		}
		script += fmt.Sprintf(`; Remove-Item -LiteralPath %s -Recurse -Force -ErrorAction SilentlyContinue`, quote(dir))
	}
	units := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		b[2*i], b[2*i+1] = byte(u), byte(u>>8)
	}
	cmd := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-EncodedCommand", base64.StdEncoding.EncodeToString(b))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("remove %s: %w", exe, err)
	}
	return nil
}
