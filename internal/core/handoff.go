package core

import (
	"fmt"
	"strings"

	"github.com/tmstorm/prowl/internal/gh"
)

// Agree is true when two findings point at the same problem: the same file
// within a few lines, or both about the PR itself.
func Agree(a, b Finding) bool {
	if a.Path != b.Path {
		return false
	}
	if a.Path == "" {
		return a.Title == b.Title
	}
	d := a.Line - b.Line
	return d >= -3 && d <= 3
}

var severityRank = map[Severity]int{MustFix: 0, ShouldFix: 1, Nit: 2}

// Combine merges the findings of several agents' reviews into one list, as
// they are posted: excluded findings left out (unless all), and a finding
// two agents agree on kept once, at the higher severity. Findings from the
// same agent are never merged, however close.
func Combine(runs []*Review, all bool) []Finding {
	var out []Finding
	for _, r := range runs {
		if !r.Finished() {
			continue
		}
		earlier := len(out)
	next:
		for _, f := range r.Findings {
			if f.Excluded && !all {
				continue
			}
			for i := range out[:earlier] {
				if Agree(out[i], f) {
					if severityRank[f.Severity] < severityRank[out[i].Severity] {
						out[i].Severity = f.Severity
					}
					continue next
				}
			}
			out = append(out, f)
		}
	}
	return out
}

// FindingsMarkdown renders the current reviews' findings, combined, for
// another agent (or a person) to act on, then each reviewer's notes from
// notes (by reviewer, as Notes returns them): what it held back or
// couldn't check. Excluded findings are left out unless all is set.
func FindingsMarkdown(runs []*Review, notes map[string][]Note, key, currentHead string, all bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review findings for %s\n\n", key)
	var at *Review
	for _, r := range runs {
		if r.Finished() {
			if at == nil || r.FinishedAt.After(at.FinishedAt) {
				at = r
			}
		}
	}
	if at != nil {
		fmt.Fprintf(&b, "Reviewed at commit %s", gh.Short(at.HeadOid))
		if !at.FinishedAt.IsZero() {
			fmt.Fprintf(&b, " on %s", at.FinishedAt.Format("2006-01-02 15:04"))
		}
		b.WriteString(".")
		if currentHead != "" && at.HeadOid != "" && currentHead != at.HeadOid {
			b.WriteString(" The branch has moved since, so some findings may already be fixed.")
		}
		b.WriteString("\n\n")
	}
	n := 0
	for _, f := range Combine(runs, all) {
		n++
		loc := "the PR description"
		if f.Path != "" {
			loc = fmt.Sprintf("`%s:%d`", f.Path, f.Line)
		}
		fmt.Fprintf(&b, "## %d. %s: %s\n\n%s\n\n", n, f.Severity, f.Title, loc)
		b.WriteString(strings.TrimSpace(f.Body) + "\n")
		if f.Rule != "" {
			fmt.Fprintf(&b, "\nRule: %s\n", f.Rule)
		}
		if f.Excluded {
			b.WriteString("\n(excluded by the reviewer)\n")
		}
		b.WriteString("\n")
	}
	if n == 0 {
		b.WriteString("No findings.\n")
	}
	for _, r := range runs {
		var said []string
		for _, e := range notes[r.Reviewer()] {
			if e.Who == "agent" {
				said = append(said, strings.TrimSpace(e.Text))
			}
		}
		if len(said) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## Note from %s\n\n%s\n", ReviewerTitle(r.Reviewer()), strings.Join(said, "\n\n"))
	}
	return b.String()
}

// HandoffPrompt asks a coding agent in the author's checkout to deal with the
// findings, without treating them as gospel.
func HandoffPrompt(findings string) string {
	return "Check these review findings against the code on this branch and fix the ones that hold up. " +
		"Verify each one first; if you disagree with a finding, say why instead of changing code. " +
		"A \"Note from\" section is the reviewer writing to me, not a finding: don't change code for it, but tell me anything in it I should act on. " +
		"Don't commit or push; I'll review your changes.\n\n" + findings
}
