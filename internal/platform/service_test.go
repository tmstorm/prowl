package platform_test

import (
	"errors"
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

// prowl watch rewrites a unit an older prowl wrote for the same binary, and
// only reloads systemd: it already runs the new binary. It leaves a missing
// unit, an up-to-date one, one edited since prowl wrote it and one that runs
// another binary alone. A unit installed before prowl kept its sum is
// remembered when it is up to date.
func TestRefreshServiceOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd is Linux only")
	}
	cfg, bin := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("PATH", bin)
	calls := filepath.Join(bin, "calls")
	_ = os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\necho \"$@\" >> "+calls+"\n"), 0o755)
	exe := "/opt/my apps/prowl"
	if sum, done, err := platform.RefreshService(exe, ""); sum != "" || done || err != nil {
		t.Fatalf("nothing installed: %q, %v, %v", sum, done, err)
	}

	path := filepath.Join(cfg, "systemd", "user", "prowl-watch.service")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	old := "[Service]\nExecStart=\"/opt/my apps/prowl\" watch\n"
	_ = os.WriteFile(path, []byte(old), 0o444)
	if os.Geteuid() != 0 {
		if sum, done, err := platform.RefreshService(exe, platform.Sum(old)); sum != platform.Sum(old) || done || err == nil {
			t.Fatalf("a rewrite that failed should keep what prowl wrote before: %q, %v, %v", sum, done, err)
		}
	}
	_ = os.Chmod(path, 0o644)
	sum, done, err := platform.RefreshService(exe, platform.Sum(old))
	if sum != platform.ServiceSum(exe) || !done || err != nil {
		t.Fatalf("an older unit prowl wrote: %q, %v, %v", sum, done, err)
	}
	unit, _ := os.ReadFile(path)
	if !strings.Contains(string(unit), `ExecStart="/opt/my apps/prowl" watch`) || !strings.Contains(string(unit), "Restart=on-failure") {
		t.Fatalf("unit:\n%s", unit)
	}
	if got, _ := os.ReadFile(calls); strings.TrimSpace(string(got)) != "--user daemon-reload" {
		t.Fatalf("systemctl calls:\n%s", got)
	}
	if sum, done, err := platform.RefreshService(exe, ""); sum != platform.ServiceSum(exe) || done || err != nil {
		t.Fatalf("an up-to-date unit prowl didn't remember: %q, %v, %v", sum, done, err)
	}

	edited := string(unit) + "Environment=GH_TOKEN=x\n"
	_ = os.WriteFile(path, []byte(edited), 0o644)
	if _, done, err := platform.RefreshService(exe, platform.ServiceSum(exe)); done || !errors.Is(err, platform.ErrEdited) {
		t.Fatalf("a unit edited by hand: %v, %v", done, err)
	}
	other := "[Service]\nExecStart=\"/home/alice/src/prowl/prowl\" watch\n"
	_ = os.WriteFile(path, []byte(other), 0o644)
	if _, done, err := platform.RefreshService(exe, platform.Sum(other)); done || !errors.Is(err, platform.ErrOtherProwl) {
		t.Fatalf("a unit for another binary: %v, %v", done, err)
	}
	for _, want := range []string{edited, other} {
		_ = os.WriteFile(path, []byte(want), 0o644)
		_, _, _ = platform.RefreshService(exe, platform.ServiceSum(exe))
		if unit, _ := os.ReadFile(path); string(unit) != want {
			t.Fatalf("a unit prowl should leave changed:\n%s", unit)
		}
	}
}

// On macOS, prowl watch rewrites a launchd agent an older prowl wrote for
// the same binary, and leaves one edited since alone.
func TestRefreshServiceOnMac(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd is macOS only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	exe := "/opt/my apps/prowl"
	path := filepath.Join(home, "Library", "LaunchAgents", "prowl-watch.plist")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	old := "<plist><string>/opt/my apps/prowl</string><string>watch</string></plist>"
	_ = os.WriteFile(path, []byte(old), 0o644)
	if _, done, err := platform.RefreshService(exe, platform.Sum(old)); !done || err != nil {
		t.Fatalf("an older agent prowl wrote: %v, %v", done, err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "<key>KeepAlive</key>") {
		t.Fatalf("agent:\n%s", b)
	}
	edited := strings.Replace(string(b), "</dict>", "<key>EnvironmentVariables</key><dict/></dict>", 1)
	_ = os.WriteFile(path, []byte(edited), 0o644)
	if _, _, err := platform.RefreshService(exe, platform.ServiceSum(exe)); !errors.Is(err, platform.ErrEdited) {
		t.Fatalf("an agent edited by hand: %v", err)
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
