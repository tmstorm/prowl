//go:build !windows

package platform_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tmstorm/prowl/internal/platform"
)

// Copy uses only the tools the session can reach, and moves on to the next
// when one fails.
func TestCopy(t *testing.T) {
	bin, clip := t.TempDir(), filepath.Join(t.TempDir(), "clipboard")
	for name, script := range map[string]string{
		"wl-copy": "echo wayland > " + clip,
		"xclip":   "exit 1",
		"xsel":    "exec /bin/cat > " + clip,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", ":0")
	if err := platform.Copy("two\nlines"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(clip); string(b) != "two\nlines" {
		t.Fatalf("copied %q", b)
	}
}
