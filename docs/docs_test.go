package docs_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tmstorm/prowl/docs"
)

// The docs come in reading order with their titles, and a section runs from
// its heading to the next heading of the same level, found by text or anchor.
func TestDocs(t *testing.T) {
	var names []string
	for _, p := range docs.All() {
		names = append(names, p.Name)
		if p.Title == "" || p.Markdown == "" {
			t.Errorf("%s has no title or text", p.Name)
		}
	}
	if !slices.Equal(names[:7], []string{"inbox", "reviewing", "ai-reviews", "slack", "tui", "cli", "config"}) {
		t.Errorf("order = %v", names)
	}
	if p, ok := docs.Get("slack.md"); !ok || p.Title != "Slack" {
		t.Errorf("Doc(slack.md) = %+v, %v", p, ok)
	}
	for _, heading := range []string{"Running tests", "running-tests", "RUNNING TESTS"} {
		s, err := docs.Section("ai-reviews", heading)
		if err != nil || !strings.HasPrefix(s, "## Running tests\n") || strings.Contains(s, "\n## ") {
			t.Errorf("DocSection(ai-reviews, %q) = %.60q…, %v", heading, s, err)
		}
	}
	if _, err := docs.Section("ai-reviews", "nothing like it"); err == nil {
		t.Error("a missing section should be an error")
	}
	if _, err := docs.Section("nope", "Running tests"); err == nil {
		t.Error("a missing page should be an error")
	}
}

// Search finds lines with every word, and says which section each is in.
func TestSearchDocs(t *testing.T) {
	hits := docs.Search("SLACK_AFTER_REVIEW react")
	i := slices.IndexFunc(hits, func(h docs.Hit) bool { return h.Page == "config" })
	if i < 0 || !strings.Contains(hits[i].Line, "slack_after_review") || hits[i].Heading != "Slack" {
		t.Fatalf("hits = %+v", hits)
	}
	if docs.Search("  ") != nil {
		t.Error("an empty search should find nothing")
	}
}

// A topic leads to its tag's places: a page, a heading by text or anchor, or
// a table row, matching case when a tag does and the start of a tag when
// none is the topic.
func TestFind(t *testing.T) {
	for _, tc := range []struct {
		topic, page, heading, line string
	}{
		{"slack", "slack", "Slack", "# Slack"},
		{"running-tests", "ai-reviews", "Running tests", "## Running tests"},
		{"snoozing", "inbox", "Snoozing", "## Snoozing"},
		{"stale_after", "config", "Inbox", "| `stale_after` |"},
		{"stale", "config", "Inbox", "| `stale_after` |"},
		{"<leader>cc", "tui", "Agent keys", "| `<leader>cc` |"},
		{"re-check", "inbox", "Buckets", "| re-check |"},
		{"H", "tui", "Moving around", "| `H`/`M`/`L` |"},
		{"h", "tui", "Moving around", "| `h`, `esc`, `←` |"},
		{"should_fix", "config", "Agent", "| `should_fix`, `nits` |"},
	} {
		tags := docs.Find(tc.topic)
		if len(tags) != 1 {
			t.Errorf("Find(%q) = %+v, want one place", tc.topic, tags)
			continue
		}
		p, _ := docs.Get(tags[0].Page)
		line := strings.Split(p.Markdown, "\n")[tags[0].Line]
		if tags[0].Page != tc.page || tags[0].Heading != tc.heading || !strings.HasPrefix(line, tc.line) {
			t.Errorf("Find(%q) = %+v at %q", tc.topic, tags[0], line)
		}
	}
	if md := docs.Find("stale_after")[0].Markdown(); !strings.HasPrefix(md, "## Inbox\n\n| Setting |") || !strings.HasSuffix(md, "\n| `stale_after` | `24h` | a PR waiting on you longer than this is due, and overdue at twice that |") {
		t.Errorf("stale_after's Markdown = %q", md)
	}
	if md := docs.Find("running-tests")[0].Markdown(); !strings.HasPrefix(md, "## Running tests\n") || strings.Contains(md, "\n## ") {
		t.Errorf("running-tests' Markdown = %.60q…", md)
	}
	if tags := docs.Find("c"); len(tags) < 2 {
		t.Errorf("c means something in the diff and in the review tab: %+v", tags)
	}
	if tags := docs.Find("nothing like it"); tags != nil {
		t.Errorf("Find(nothing like it) = %+v", tags)
	}
	for _, tag := range docs.Tags() {
		if !slices.ContainsFunc(docs.Find(tag.Tag), func(d docs.Tag) bool { return d.Page == tag.Page && d.Line == tag.Line }) {
			t.Errorf("%q doesn't lead to %s line %d", tag.Tag, tag.Page, tag.Line+1)
		}
	}
}

// An agent reads the docs by topic, falls back to the lines that mention
// words that aren't one, and lists the pages for nothing.
func TestDocsText(t *testing.T) {
	if s := docs.Text("stale_after"); !strings.Contains(s, "| `stale_after` |") {
		t.Errorf("stale_after: %q", s)
	}
	if s := docs.Text("keyring"); !strings.Contains(s, "slack › Setting it up: ") {
		t.Errorf("keyring: %q", s)
	}
	if s := docs.Text(""); !strings.HasPrefix(s, "inbox: The inbox\n") {
		t.Errorf("no topic: %q", s)
	}
	if s := docs.Text("zzz qqq"); !strings.HasPrefix(s, "Nothing in prowl's docs") {
		t.Errorf("zzz qqq: %q", s)
	}
}

var docLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// Every relative link in the docs reaches a page, a heading in it, or a file
// in the repo, the same way it does on GitHub.
func TestDocLinks(t *testing.T) {
	for _, p := range docs.All() {
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
				if page, ok = docs.Get(path); !ok {
					t.Errorf("%s links %s: no such page", p.Name, target)
					continue
				}
			default:
				if _, err := os.Stat(path); err != nil {
					t.Errorf("%s links %s: %v", p.Name, target, err)
				}
				continue
			}
			if anchor != "" {
				if _, err := docs.Section(page.Name, anchor); err != nil {
					t.Errorf("%s links %s: %v", p.Name, target, err)
				}
			}
		}
	}
}
