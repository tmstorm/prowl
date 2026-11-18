package core_test

import (
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/core"
)

// Posting combines agents' findings: excluded ones out, a problem two agents
// raised kept once at the higher severity, one agent's findings never merged.
func TestCombine(t *testing.T) {
	runs := []*core.Review{
		{Status: "done", Findings: []core.Finding{{Severity: "should-fix", Path: "a.go", Line: 11, Title: "x"}, {Path: "c.go", Line: 1, Excluded: true}}},
		{Status: "done", Findings: []core.Finding{{Severity: "must-fix", Path: "a.go", Line: 9, Title: "y"}, {Severity: "nit", Path: "b.go", Line: 1}}},
		{Status: "running", Findings: []core.Finding{{Path: "z.go", Line: 1}}},
	}
	got := core.Combine(runs, false)
	if len(got) != 2 || got[0].Path != "a.go" || got[0].Severity != "must-fix" || got[1].Path != "b.go" {
		t.Fatalf("combined = %+v", got)
	}
	if len(core.Combine(runs, true)) != 3 {
		t.Fatal("all includes excluded findings")
	}
	one := []*core.Review{{Status: "done", Findings: []core.Finding{{Path: "a.go", Line: 10}, {Path: "a.go", Line: 12}}}}
	if len(core.Combine(one, false)) != 2 {
		t.Fatal("two findings from one agent are two findings")
	}
}

// The findings carry each reviewer's note, which holds what it left out of
// the findings, but not your side of the chat. The prompt for a coding agent
// says a note is not a finding.
func TestFindingsMarkdownNotes(t *testing.T) {
	runs := []*core.Review{{Agent: "claude", Status: "done", HeadOid: "bd7038b2aa", FinishedAt: time.Now()}}
	notes := map[string][]core.Note{"claude": {
		{Who: "agent", Text: "Not blocking: watch retries a failed install on every poll."},
		{Who: "you", Text: "why?"},
	}}
	md := core.FindingsMarkdown(runs, notes, "o/api#7", "", false)
	for _, want := range []string{"No findings.", "## Note from Claude", "watch retries a failed install on every poll"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "why?") {
		t.Errorf("your chat leaked into the findings:\n%s", md)
	}
	if p := core.HandoffPrompt(md); !strings.Contains(p, "\"Note from\" section is the reviewer writing to me, not a finding") {
		t.Errorf("the prompt doesn't say a note isn't a finding:\n%s", p)
	}
}
