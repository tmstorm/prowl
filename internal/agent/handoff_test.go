package agent_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/store"
)

// A posted review lines up with its draft: rewritten comments show both
// versions, deleted ones are dropped, your own are added, and untouched
// pieces are only counted.
func TestEditsMarkdown(t *testing.T) {
	post := store.Post{Key: "o/api#7", URL: "https://github.com/o/api/pull/7#r1", At: time.Now(),
		Draft: store.Review{Body: "Two problems.", Comments: []store.Comment{
			{Path: "a.go", Line: 10, Body: "This map write is not guarded and can panic under load."},
			{Path: "a.go", Line: 40, Body: "Nit: rename this."},
			{Path: "b.go", Line: 3, Body: "Missing test."},
		}},
		Posted: store.Review{Body: "Two problems.", Comments: []store.Comment{
			{Path: "a.go", Line: 10, Body: "Unguarded map write; this panics under load."},
			{Path: "b.go", Line: 3, Body: "Missing test."},
			{Path: "c.go", Line: 1, Body: "Also this."},
		}},
	}
	md := agent.EditsMarkdown([]store.Post{post})
	for _, want := range []string{
		"### a.go:10", "> This map write is not guarded", "> Unguarded map write",
		"### Dropped: a.go:40", "### Added: c.go:1",
		"2 of the drafts' pieces (bodies and comments) went out unchanged across 1 reviews",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "### b.go:3") {
		t.Error("an unchanged comment should only be counted")
	}
}

// The findings carry each reviewer's note, which holds what it left out of
// the findings, but not your side of the chat. The prompt for a coding agent
// says a note is not a finding.
func TestFindingsMarkdownNotes(t *testing.T) {
	runs := []*store.AgentReview{{Agent: "claude", Status: "done", HeadOid: "bd7038b2aa", FinishedAt: time.Now()}}
	notes := map[string][]store.LogEntry{"claude": {
		{Who: "agent", Text: "Not blocking: watch retries a failed install on every poll."},
		{Who: "you", Text: "why?"},
	}}
	md := agent.FindingsMarkdown(runs, notes, "o/api#7", "", false)
	for _, want := range []string{"No findings.", "## Note from Claude", "watch retries a failed install on every poll"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "why?") {
		t.Errorf("your chat leaked into the findings:\n%s", md)
	}
	if p := agent.HandoffPrompt(md); !strings.Contains(p, "\"Note from\" section is the reviewer writing to me, not a finding") {
		t.Errorf("the prompt doesn't say a note isn't a finding:\n%s", p)
	}
}
