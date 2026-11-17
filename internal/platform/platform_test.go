//go:build !windows

package platform_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// A command run on the terminal, as an editor is, writes to it directly and
// not through a pipe, though Bubble Tea hands it the writer the TUI draws
// through.
func TestTerminalCmd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "tty"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tty := platform.Terminal.File
	platform.Terminal.File = f
	t.Cleanup(func() { platform.Terminal.File = tty })
	c := platform.TerminalCmd{Cmd: exec.Command("sh", "-c", "[ -p /dev/stdout ] && echo pipe || echo direct")}
	c.SetStdin(os.Stdin)
	c.SetStdout(platform.Terminal)
	c.SetStderr(os.Stderr)
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(f.Name()); string(b) != "direct\n" {
		t.Fatalf("the command wrote %q", b)
	}
}
