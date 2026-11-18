package tui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/tui"
)

// A running review shows the agent's latest action, and once the agent has
// gone quiet to write, what the owl is up to in that step, or plain
// "writing" with the owl off. Preparing the worktree is never mistaken for
// writing.
func TestProgress(t *testing.T) {
	now := time.Now()
	at := func(act string, ago time.Duration) *core.Review {
		return &core.Review{Activity: act, ActivityAt: now.Add(-ago)}
	}
	if got := tui.Progress(&core.Review{}, now, true); got != "starting" {
		t.Errorf("no activity yet: %q", got)
	}
	if got := tui.Progress(at("reviewing: reading a.go", time.Second), now, true); got != "reviewing: reading a.go" {
		t.Errorf("busy: %q", got)
	}
	if got := tui.Progress(at("verifying findings: reading a.go", time.Minute), now, true); !strings.HasPrefix(got, "verifying findings: ") || strings.Contains(got, "a.go") || !strings.HasSuffix(got, "…") {
		t.Errorf("quiet: %q", got)
	}
	if got := tui.Progress(at("preparing worktree", time.Minute), now, true); got != "preparing worktree" {
		t.Errorf("cloning: %q", got)
	}
	if got := tui.Progress(at("reviewing: reading a.go", time.Minute), now, false); got != "reviewing: writing…" {
		t.Errorf("owl off: %q", got)
	}
}
