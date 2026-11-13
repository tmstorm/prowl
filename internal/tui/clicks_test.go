package tui_test

import (
	"os"
	"strings"
	"testing"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
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
	a.want("Review profiles", "adversarial", "AI summary for new PRs", "Learn from my edits", "Claude summary model", "*: golang:1.27, acme/web: node:22")
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
