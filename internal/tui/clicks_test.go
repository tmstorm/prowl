package tui_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/tui"
)

// The leader menu is grouped by category, and its entries are clickable.
func TestClickLeaderMenu(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}})
	a.keys("space")
	a.want("Review", "Agent", "Inbox", "Share", "approve")
	a.click(",  options")
	a.want("Options", "Appearance")
}

// In options a click selects a setting, a second changes it (and saves it),
// and a click outside closes the menu.
func TestClickOptions(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}})
	a.keys("space", ",")
	a.clickFirst("Icon legend")
	a.want("show_legend")
	a.clickFirst("Icon legend")
	a.send(mouseAt(0, 0))
	a.wantNot("Appearance", "┄┄ legend")
	if b, _ := os.ReadFile(config.Path()); !strings.Contains(string(b), "\nshow_legend = false") {
		t.Fatalf("the change wasn't saved:\n%s", b)
	}
}

// In a picker a click selects a row and a second click chooses it.
func TestClickPicker(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one"), needsReview("web", 2, "fix: two")}})
	a.keys("space", "f", "r")
	a.click("web ")
	a.click("web ")
	a.want("fix: two")
	a.wantNot("fix: one")
}

// Every setting has a place in the options menu, including the newer ones.
func TestOptionsCoverAgentSettings(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}}, func(c *config.Config) {
		c.Agent.RunImages = map[string]string{"*": "golang:1.27", "acme/web": "node:22"}
	})
	a.resize(160, 80)
	a.keys("space", ",")
	a.want("Due after", "Review profiles", "adversarial", "AI summary for new PRs", "Learn from my edits", "Claude summary model", "*: golang:1.27, acme/web: node:22")
}

// The cursor's mark sits in the gutter, so it doesn't hide the first
// character of a line that starts at the edge: a section in the PR pane, a
// heading in the docs.
func TestCursorMarkInGutter(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}})
	a.keys("enter")
	a.search("Reviewers")
	if c := a.cursor(); !strings.Contains(c, "▌Reviewers") {
		t.Fatalf("cursor = %q", c)
	}
	a.keys("esc", "?", "d", "/", "Snoozing", "enter")
	a.want("▌Snoozing")
}

// Reviewers in the options says which keys run them, with the leader you
// use rather than <leader>.
func TestReviewersHint(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}})
	a.resize(160, 80)
	a.keys("space", ",")
	a.clickFirst("Reviewers")
	a.keys("enter")
	a.want("<space>cc runs every checked agent", "reviews alone with <space>cC")
}

// Where the chat can't open in a window (Windows), the options don't offer
// the choice.
func TestChatModeOption(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}})
	a.resize(160, 80)
	a.keys("space", ",")
	a.want("Chat window")
	floating := *tui.FloatingChat
	*tui.FloatingChat = false
	t.Cleanup(func() { *tui.FloatingChat = floating })
	a.keys("esc", "space", ",")
	a.wantNot("Chat window")
}

// The PR pane's border offers focus mode, and a click on it toggles it.
func TestFocusModeHint(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}})
	a.click("+ full width")
	a.want("+ back to inbox")
	if top := a.lines()[0]; strings.Contains(top, "[0]") {
		t.Fatalf("clicking the hint should enter focus mode: %q", top)
	}
	a.click("+ back to inbox")
	a.want("[0]", "+ full width")
}

// The wheel scrolls a PR three lines a notch, every notch.
func TestWheelScrollsPR(t *testing.T) {
	body := ""
	for i := 1; i <= 80; i++ {
		body += fmt.Sprintf("row %02d\n\n", i)
	}
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}, Bodies: map[int]string{1: body}})
	a.keys("enter")
	a.want("row 01")
	top := func() string {
		for _, l := range a.lines() {
			if i := strings.Index(l, "row "); i >= 0 {
				return l[i : i+6]
			}
		}
		return ""
	}
	seen := []string{}
	for range 6 {
		a.send(tea.MouseMsg{X: a.w - 10, Y: a.h / 2, Button: tea.MouseButtonWheelDown})
		seen = append(seen, top())
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Fatalf("notch %d didn't scroll down: %v", i+1, seen)
		}
	}
}

// In the docs a click on a page's name opens it, the wheel scrolls, and a
// click on a line selects it and a second follows its link.
func TestClickDocs(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}})
	a.keys("?", "d")
	a.clickFirst("slack · tui")
	a.want("prowl docs · Slack")
	a.clickFirst("inbox · reviewing")
	a.want("prowl docs · The inbox", "close   1/")
	for range 4 {
		a.send(tea.MouseMsg{X: a.w / 2, Y: a.h / 2, Button: tea.MouseButtonWheelDown})
	}
	a.wantNot("close   1/")
	a.keys("/", "review them; see", "enter", "k")
	a.click("review them; see")
	a.click("review them; see")
	a.want("prowl docs · Slack", "Setting it up")
}

// A popup's list moves with the panes' motions, g for gg, the wheel moves
// its selection a row a notch, and ctrl+c closes it.
func TestPopupListMotions(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one"), needsReview("web", 2, "fix: two"), needsReview("cli", 3, "fix: three")}})
	a.keys("space", "s", "G", "enter")
	a.want("for 1 week")
	a.keys("space", "s", "G", "g", "enter")
	a.want("for 1 hour")
	a.keys("space", "s")
	a.want("Snooze o/cli#3")
	for range 2 {
		a.send(tea.MouseMsg{X: a.w / 2, Y: a.h / 2, Button: tea.MouseButtonWheelDown})
	}
	a.keys("enter")
	a.want("for 1 day")

	a.resize(160, 24)
	a.keys("space", ",")
	a.want("Inbox", "Group by")
	a.keys("G")
	a.wantNot("Group by")
	a.keys("g")
	a.want("Group by")
	a.keys("ctrl+c")
	a.wantNot("Options")
}

// Pickers with no text input are in normal mode, and q closes them.
func TestPickerMode(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}})
	for _, keys := range [][]string{{"space", "c", "C"}, {"space", "c", "p"}, {"space", "l"}} {
		a.keys(keys...)
		a.want("enter")
		if l := a.lines()[a.h-1]; !strings.Contains(l, "NORMAL") {
			t.Fatalf("%v: %q", keys, l)
		}
		a.keys("q")
		a.wantNot("esc cancel", "esc close")
	}
}
