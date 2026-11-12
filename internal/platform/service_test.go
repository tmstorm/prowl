package platform_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
