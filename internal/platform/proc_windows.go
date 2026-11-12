//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// RemoveBinary deletes prowl.exe at exe once this prowl exits, since Windows
// won't delete a running program. When its folder holds nothing else, it goes
// too and comes off your PATH, where install.ps1 put it.
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
	script := fmt.Sprintf(`ping -n 3 127.0.0.1 >nul & del /f /q "%s"`, exe)
	if alone {
		script += fmt.Sprintf(` & rmdir /s /q "%s"`, dir)
		path := fmt.Sprintf(`$d='%s'; $p=[Environment]::GetEnvironmentVariable('Path','User'); `+
			`[Environment]::SetEnvironmentVariable('Path', (($p -split ';') | Where-Object { $_ -and $_ -ne $d }) -join ';', 'User')`,
			strings.ReplaceAll(dir, "'", "''"))
		if err := exec.Command("powershell", "-NoProfile", "-Command", path).Run(); err != nil {
			return fmt.Errorf("take %s off PATH: %w", dir, err)
		}
	}
	cmd := exec.Command("cmd", "/c", script)
	Detach(cmd)
	return cmd.Start()
}
