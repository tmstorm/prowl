package core_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tmstorm/prowl/internal/core"
)

// The docs come in reading order with their titles, and a section runs from
// its heading to the next heading of the same level, found by text or anchor.
func TestDocs(t *testing.T) {
	var names []string
	for _, p := range core.Docs() {
		names = append(names, p.Name)
		if p.Title == "" || p.Markdown == "" {
			t.Errorf("%s has no title or text", p.Name)
		}
	}
	if !slices.Equal(names[:6], []string{"inbox", "reviewing", "ai-reviews", "slack", "running", "config"}) {
		t.Errorf("order = %v", names)
	}
	if p, ok := core.Doc("slack.md"); !ok || p.Title != "Slack" {
		t.Errorf("Doc(slack.md) = %+v, %v", p, ok)
	}
	for _, heading := range []string{"Running tests", "running-tests", "RUNNING TESTS"} {
		s, err := core.DocSection("ai-reviews", heading)
		if err != nil || !strings.HasPrefix(s, "## Running tests\n") || strings.Contains(s, "\n## ") {
			t.Errorf("DocSection(ai-reviews, %q) = %.60q…, %v", heading, s, err)
		}
	}
	if _, err := core.DocSection("ai-reviews", "nothing like it"); err == nil {
		t.Error("a missing section should be an error")
	}
	if _, err := core.DocSection("nope", "Running tests"); err == nil {
		t.Error("a missing page should be an error")
	}
}

// Search finds lines with every word, and says which section each is in.
func TestSearchDocs(t *testing.T) {
	hits := core.SearchDocs("SLACK_AFTER_REVIEW react")
	i := slices.IndexFunc(hits, func(h core.DocHit) bool { return h.Page == "config" })
	if i < 0 || !strings.Contains(hits[i].Line, "slack_after_review") || hits[i].Heading != "Slack" {
		t.Fatalf("hits = %+v", hits)
	}
	if core.SearchDocs("  ") != nil {
		t.Error("an empty search should find nothing")
	}
}

// A topic leads to its tag's places: a page, a heading by text or anchor, or
// a table row, matching case when a tag does and the start of a tag when
// none is the topic.
func TestFindDocTag(t *testing.T) {
	for _, tc := range []struct {
		topic, page, heading, line string
	}{
		{"slack", "slack", "Slack", "# Slack"},
		{"running-tests", "ai-reviews", "Running tests", "## Running tests"},
		{"snoozing", "inbox", "Snoozing", "## Snoozing"},
		{"stale_after", "config", "Inbox", "| `stale_after` |"},
		{"stale", "config", "Inbox", "| `stale_after` |"},
		{"<leader>cc", "ai-reviews", "Starting a review", "| `<leader>cc` |"},
		{"re-check", "inbox", "Buckets", "| re-check |"},
		{"H", "reviewing", "Moving around", "| `H`/`M`/`L` |"},
		{"h", "reviewing", "Moving around", "| `h`, `esc`, `←` |"},
		{"should_fix", "config", "Agent", "| `should_fix`, `nits` |"},
	} {
		tags := core.FindDocTag(tc.topic)
		if len(tags) != 1 {
			t.Errorf("FindDocTag(%q) = %+v, want one place", tc.topic, tags)
			continue
		}
		p, _ := core.Doc(tags[0].Page)
		line := strings.Split(p.Markdown, "\n")[tags[0].Line]
		if tags[0].Page != tc.page || tags[0].Heading != tc.heading || !strings.HasPrefix(line, tc.line) {
			t.Errorf("FindDocTag(%q) = %+v at %q", tc.topic, tags[0], line)
		}
	}
	if md := core.FindDocTag("stale_after")[0].Markdown(); !strings.HasPrefix(md, "## Inbox\n\n| Setting |") || !strings.HasSuffix(md, "\n| `stale_after` | `24h` | a PR waiting on you longer than this turns yellow, and bold red at twice that |") {
		t.Errorf("stale_after's Markdown = %q", md)
	}
	if md := core.FindDocTag("running-tests")[0].Markdown(); !strings.HasPrefix(md, "## Running tests\n") || strings.Contains(md, "\n## ") {
		t.Errorf("running-tests' Markdown = %.60q…", md)
	}
	if tags := core.FindDocTag("c"); len(tags) < 2 {
		t.Errorf("c means something in the diff and in the review tab: %+v", tags)
	}
	if tags := core.FindDocTag("nothing like it"); tags != nil {
		t.Errorf("FindDocTag(nothing like it) = %+v", tags)
	}
	for _, tag := range core.DocTags() {
		if !slices.ContainsFunc(core.FindDocTag(tag.Tag), func(d core.DocTag) bool { return d.Page == tag.Page && d.Line == tag.Line }) {
			t.Errorf("%q doesn't lead to %s line %d", tag.Tag, tag.Page, tag.Line+1)
		}
	}
}

var docLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// Every relative link in the docs reaches a page, a heading in it, or a file
// in the repo, the same way it does on GitHub.
func TestDocLinks(t *testing.T) {
	for _, p := range core.Docs() {
		for _, m := range docLink.FindAllStringSubmatch(p.Markdown, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			path, anchor, _ := strings.Cut(target, "#")
			page := p
			switch {
			case path == "":
			case strings.HasSuffix(path, ".md") && !strings.Contains(path, "/"):
				var ok bool
				if page, ok = core.Doc(path); !ok {
					t.Errorf("%s links %s: no such page", p.Name, target)
					continue
				}
			default:
				if _, err := os.Stat(filepath.Join("..", "..", "docs", path)); err != nil {
					t.Errorf("%s links %s: %v", p.Name, target, err)
				}
				continue
			}
			if anchor != "" {
				if _, err := core.DocSection(page.Name, anchor); err != nil {
					t.Errorf("%s links %s: %v", p.Name, target, err)
				}
			}
		}
	}
}
