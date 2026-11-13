package core_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/ghtest"
	"github.com/tmstorm/prowl/internal/skill"
)

// Uninstalling takes off everything prowl put outside a package manager: the
// service, its worktrees and refs in your checkouts, the skill as it wrote
// it, the binary it installed and, with purge, the config and data. A skill
// you edited and a checkout prowl never used stay, and when a step fails the
// binary stays too, so uninstall can run again. A package manager's prowl is
// left to the package manager.
func TestUninstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the binary goes after prowl exits, and its folder off the real user PATH")
	}
	path := os.Getenv("PATH")
	running(t, "0.1.0")
	co, _ := newCore(t, &ghtest.GitHub{})
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\necho \"$@\" >> "+calls+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+path)

	if _, err := skill.Install(skill.Dir("claude")); err != nil {
		t.Fatal(err)
	}
	edited := skill.Dir("codex")
	_ = os.MkdirAll(edited, 0o755)
	_ = os.WriteFile(filepath.Join(edited, "SKILL.md"), []byte("my own version"), 0o644)
	unit := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "systemd", "user", "prowl-watch.service")
	if runtime.GOOS == "linux" {
		_ = os.MkdirAll(filepath.Dir(unit), 0o755)
		_ = os.WriteFile(unit, []byte("[Service]\n"), 0o644)
	}

	root := t.TempDir()
	repo, other := filepath.Join(root, "api"), filepath.Join(root, "web")
	for _, dir := range []string{repo, other} {
		_ = os.MkdirAll(dir, 0o755)
		gitIn(t, dir, "init", "-q")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
	}
	gitIn(t, repo, "update-ref", "refs/prowl/pr-1-claude", "HEAD")
	wt := filepath.Join(config.DataDir(), "worktrees", "o", "api", "1", "claude")
	gitIn(t, repo, "worktree", "add", "-q", "--detach", wt, "refs/prowl/pr-1-claude")
	if _, err := co.Update(func(c *config.Config) error {
		c.RepoRoots = []string{root}
		c.Agent.RunImages = map[string]string{"*": "golang:1.27", "acme/api": "golang:1.27", "acme/web": "node:22"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "prowl")
	_ = os.WriteFile(exe, []byte("prowl"), 0o755)

	f := core.FindFootprint(co.Config(), exe, true)
	if f.Service != (runtime.GOOS == "linux") || !slices.Equal(f.Checkouts, []string{repo}) ||
		!slices.Equal(f.Skills, []string{skill.Dir("claude")}) || !slices.Equal(f.Kept, []string{edited}) || f.Binary != exe || f.Command != "" ||
		!slices.Equal(f.Dirs, []string{config.Dir(), config.DataDir()}) || !slices.Equal(f.Images, []string{"golang:1.27", "node:22"}) {
		t.Fatalf("footprint: %+v", f)
	}
	if err := core.Uninstall(f); err != nil {
		t.Fatal(err)
	}

	out, _ := exec.Command("git", "-C", repo, "for-each-ref", "refs/prowl/").Output()
	list, _ := exec.Command("git", "-C", repo, "worktree", "list").Output()
	if len(out) > 0 || strings.Count(string(list), "\n") != 1 {
		t.Errorf("prowl's refs or worktree left in the checkout:\n%s%s", out, list)
	}
	for _, gone := range []string{skill.Dir("claude"), exe, config.Dir(), config.DataDir(), unit} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s is still there", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(edited, "SKILL.md")); err != nil {
		t.Error("the edited skill was removed")
	}
	if b, _ := os.ReadFile(calls); runtime.GOOS == "linux" && !strings.Contains(string(b), "--user disable --now prowl-watch.service") {
		t.Errorf("the service wasn't stopped: %q", b)
	}

	stuck := core.Footprint{Checkouts: []string{filepath.Join(root, "gone")}, Binary: filepath.Join(t.TempDir(), "prowl")}
	_ = os.WriteFile(stuck.Binary, []byte("prowl"), 0o755)
	if err := core.Uninstall(stuck); err == nil {
		t.Error("cleaning a missing checkout should fail")
	}
	if _, err := os.Stat(stuck.Binary); err != nil {
		t.Error("the binary went although a step failed, so uninstall can't run again")
	}

	if runtime.GOOS == "linux" {
		f := core.FindFootprint(co.Config(), "/usr/bin/prowl", false)
		if f.Binary != "" || f.Command != "sudo pacman -R prowl-bin" || f.Dirs != nil {
			t.Errorf("a package's prowl: %+v", f)
		}
	}
}
