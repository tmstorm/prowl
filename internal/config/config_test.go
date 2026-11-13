package config_test

import (
	"github.com/tmstorm/prowl/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Every setting written by Save must round-trip, and defaults must be set.
func TestDefaultsAndRoundTrip(t *testing.T) {
	d := config.Default()
	if !d.ShowLegend || d.GroupBy == "" || d.ChatMode == "" || len(d.Bots) == 0 || d.PollInterval.Duration == 0 || d.Agent.Main() != "claude" {
		t.Fatalf("missing defaults: %+v", d)
	}
	// nothing specific to one person's machine or org
	if len(d.Orgs) != 0 || len(d.RepoRoots) != 0 || len(d.Agent.Skills) != 0 || len(d.Agent.ContextDirs) != 0 || len(d.Agent.RunImages) != 0 || d.NerdFont {
		t.Fatalf("defaults must be generic: %+v", d)
	}
	d.Agent.Skills = []string{"some-skill"}
	d.Agent.SetModel("codex", "gpt-x")
	d.TypeColors = map[string]string{"wip": "orange"}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	d.SlackNames = map[string]string{"alice": "@Alice"}
	d.Agent.ReviewInstructions = "line one\nline two"
	d.Agent.Nits = false
	d.Agent.RunImages = map[string]string{"*": "golang:1.27", "acme/web": "node:22"}
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load()
	if err != nil {
		b, _ := os.ReadFile(filepath.Join(dir, "prowl", "config.toml"))
		t.Fatalf("saved config does not parse: %v\n%s", err, b)
	}
	if got.GroupBy != d.GroupBy || got.ShowLegend != d.ShowLegend || got.SlackNames["alice"] != "@Alice" ||
		got.Agent.ReviewInstructions != d.Agent.ReviewInstructions || got.NotifyTimeout != d.NotifyTimeout || len(got.Bots) != len(d.Bots) ||
		got.Agent.Model("codex") != "gpt-x" || got.Agent.Nits || !got.Agent.ShouldFix || got.TypeColors["wip"] != "orange" || len(got.Agent.Skills) != 1 ||
		got.Agent.RunImage("acme/web") != "node:22" || got.Agent.RunImage("acme/api") != "golang:1.27" {
		t.Fatalf("round trip lost settings:\nsaved %+v\nread  %+v", d, got)
	}
}

// A new user's file pins no defaults: every default is commented out, so a
// later version's defaults (a new commit type, say) still apply.
func TestDefaultsWrittenCommented(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := config.Load(); err != nil { // first run writes the file
		t.Fatal(err)
	}
	b, _ := os.ReadFile(config.Path())
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		t.Errorf("default written as a live setting: %q", line)
	}
	if !strings.Contains(string(b), `# type_order = ["fix",`) {
		t.Error("the type order should still be listed, commented, for reference")
	}
	got, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.SortByType != true || len(got.Types()) != len(config.DefaultTypeOrder) || got.Agent.Main() != "claude" {
		t.Fatalf("defaults not applied from a commented file: %+v", got)
	}
}

// A setting changed in the app must not erase edits made to the file after
// the app loaded it (the bug that dropped the review skills).
func TestUpdateKeepsExternalEdits(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	inMemory := config.Default()
	if err := inMemory.Save(); err != nil {
		t.Fatal(err)
	}
	// someone edits the file while the app is running
	if _, err := config.Update(func(c *config.Config) error { c.Agent.Skills = []string{"write-prose"}; return nil }); err != nil {
		t.Fatal(err)
	}
	// the app toggles grouping from its stale in-memory copy
	got, err := config.Update(func(c *config.Config) error { c.GroupBy = "repo"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Agent.Skills) != 1 || got.GroupBy != "repo" {
		t.Fatalf("external edit lost: %+v", got)
	}
}

// Durations and prompts survive a save: 30s must not come back as "3", and a
// prompt containing ”' must not break the file.
func TestSaveKeepsValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := config.Default()
	c.PollInterval.Duration = 30 * time.Second
	c.StaleAfter.Duration = 90 * time.Minute
	c.NotifyTimeout.Duration = 10 * time.Minute
	c.Agent.ReviewInstructions = "use '''raw''' strings"
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load()
	if err != nil {
		b, _ := os.ReadFile(config.Path())
		t.Fatalf("saved config does not load: %v\n%s", err, b)
	}
	if got.PollInterval != c.PollInterval || got.StaleAfter != c.StaleAfter || got.NotifyTimeout != c.NotifyTimeout || got.Agent.ReviewInstructions != c.Agent.ReviewInstructions {
		t.Fatalf("lost values: %v %v %v %q", got.PollInterval, got.StaleAfter, got.NotifyTimeout, got.Agent.ReviewInstructions)
	}
}

// Profiles round-trip, and yours replace a built-in one by name.
func TestProfiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := config.Default()
	if p, ok := c.Agent.Profile("adversarial"); !ok || p.Prompt == "" || len(c.Agent.AllProfiles()) != 1 {
		t.Fatal("adversarial is built in, and listed")
	}
	c.Agent.Profiles = []config.Profile{{Name: "adversarial", Prompt: "break it", Reviewers: []string{"codex"}}, {Name: "security", Prompt: "secrets"}}
	c.Agent.EditExamples = 3
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load()
	if err != nil {
		b, _ := os.ReadFile(config.Path())
		t.Fatalf("%v\n%s", err, b)
	}
	if p, _ := got.Agent.Profile("adversarial"); p.Prompt != "break it" || p.Reviewers[0] != "codex" {
		t.Fatalf("your adversarial should win: %+v", p)
	}
	if len(got.Agent.AllProfiles()) != 2 || got.Agent.EditExamples != 3 {
		t.Fatalf("profiles %+v, examples %d", got.Agent.AllProfiles(), got.Agent.EditExamples)
	}
}

// The leader is one of the keys prowl leaves free; a key that would shadow
// a motion is refused when the config loads.
func TestLeader(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := config.Default()
	if c.LeaderKey() != " " || c.LeaderLabel() != "<space>" {
		t.Fatalf("default leader: %q %q", c.LeaderKey(), c.LeaderLabel())
	}
	c.Leader = `\`
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load()
	if err != nil || got.LeaderKey() != `\` || got.LeaderLabel() != `\` {
		t.Fatalf("backslash leader: %v %q", err, got.Leader)
	}
	if err := os.WriteFile(config.Path(), []byte("leader = \"j\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), `leader = "j"`) {
		t.Fatalf("want j refused, got %v", err)
	}
}
