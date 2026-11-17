package platform_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/platform"
)

// On Linux, the service is a systemd user unit that runs prowl watch from
// the installed binary, quoted so a path with a space works; uninstalling
// disables and removes it.
func TestServiceOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd is Linux only")
	}
	cfg, bin := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("PATH", bin)
	calls := filepath.Join(bin, "calls")
	_ = os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\necho \"$@\" >> "+calls+"\n"), 0o755)

	if _, err := platform.InstallService("/opt/my apps/prowl"); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(cfg, "systemd", "user", "prowl-watch.service"))
	if err != nil || !strings.Contains(string(unit), `ExecStart="/opt/my apps/prowl" watch`) {
		t.Fatalf("unit: %v\n%s", err, unit)
	}
	if err := platform.UninstallService(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg, "systemd", "user", "prowl-watch.service")); err == nil {
		t.Fatal("the unit should be gone")
	}
	got, _ := os.ReadFile(calls)
	for _, want := range []string{"--user enable --now prowl-watch.service", "--user disable --now prowl-watch.service"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("systemctl was not asked to %q:\n%s", want, got)
		}
	}
}

// A link opener exits on its own; prowl reaps it, so none stays behind as a
// zombie while prowl runs.
func TestOpenLeavesNoZombie(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	_ = os.WriteFile(filepath.Join(bin, "xdg-open"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	for range 3 {
		if err := platform.Open("https://github.com/acme/api/pull/1"); err != nil {
			t.Fatal(err)
		}
	}
	for range 100 {
		if zombies() == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%d openers left as zombies", zombies())
}

// zombies counts this process's children that exited and weren't reaped.
func zombies() int {
	stats, _ := filepath.Glob("/proc/[0-9]*/stat")
	n := 0
	for _, path := range stats {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		_, rest, _ := strings.Cut(string(b), ") ")
		f := strings.Fields(rest)
		if len(f) > 1 && f[0] == "Z" && f[1] == strconv.Itoa(os.Getpid()) {
			n++
		}
	}
	return n
}

// On Linux, web links open with xdg-open and other apps' links with gio,
// which hands them over unchanged.
func TestOpenOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("xdg-open and gio are Linux only")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	calls := filepath.Join(bin, "calls")
	for _, name := range []string{"xdg-open", "gio"} {
		_ = os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho "+name+" \"$@\" >> "+calls+"\n"), 0o755)
	}
	_ = platform.Open("https://github.com/acme/api/pull/1")
	_ = platform.Open("slack://channel?team=T1&id=C1")
	want := "gio open slack://channel?team=T1&id=C1\nxdg-open https://github.com/acme/api/pull/1"
	ran := func() string {
		b, _ := os.ReadFile(calls)
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		slices.Sort(lines)
		return strings.Join(lines, "\n")
	}
	for range 100 {
		if ran() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ran:\n%s\nwant:\n%s", ran(), want)
}
