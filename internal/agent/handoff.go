package agent

import (
	"fmt"
	"os"
	"strings"

	"github.com/tmstorm/prowl/internal/config"

	"github.com/tmstorm/prowl/internal/store"
)

// Agree is true when two findings point at the same problem: the same file
// within a few lines, or both about the PR itself.
func Agree(a, b store.Finding) bool {
	if a.Path != b.Path {
		return false
	}
	if a.Path == "" {
		return a.Title == b.Title
	}
	d := a.Line - b.Line
	return d >= -3 && d <= 3
}

var severityRank = map[string]int{"must-fix": 0, "should-fix": 1, "nit": 2}

// Combine merges the findings of several agents' reviews into one list, as
// they are posted: excluded findings left out (unless all), and a finding
// two agents agree on kept once, at the higher severity. Findings from the
// same agent are never merged, however close.
func Combine(runs []*store.AgentReview, all bool) []store.Finding {
	var out []store.Finding
	for _, r := range runs {
		if r.Status != "done" && r.Status != "posted" {
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
// notes (by reviewer, as core.Notes returns them): what it held back or
// couldn't check. Excluded findings are left out unless all is set.
func FindingsMarkdown(runs []*store.AgentReview, notes map[string][]store.LogEntry, key, currentHead string, all bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review findings for %s\n\n", key)
	var at *store.AgentReview
	for _, r := range runs {
		if r.Status == "done" || r.Status == "posted" {
			if at == nil || r.FinishedAt.After(at.FinishedAt) {
				at = r
			}
		}
	}
	if at != nil {
		head := at.HeadOid
		if len(head) > 8 {
			head = head[:8]
		}
		fmt.Fprintf(&b, "Reviewed at commit %s", head)
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
		title := Title(r.Agent)
		if r.Profile != "" {
			title += " · " + r.Profile
		}
		fmt.Fprintf(&b, "\n## Note from %s\n\n%s\n", title, strings.Join(said, "\n\n"))
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

// edit is one piece of an agent draft and what you posted in its place.
type edit struct {
	where, draft, posted string
}

// diffPost lines a posted review up with its draft: the body, then each
// inline comment, matched by file and line, then by order within the file.
// Comments you deleted are dropped; ones you wrote yourself are added.
func diffPost(p store.Post) (changed []edit, dropped, added []store.Comment, unchanged int) {
	if strings.TrimSpace(p.Draft.Body) != strings.TrimSpace(p.Posted.Body) {
		changed = append(changed, edit{"review body", p.Draft.Body, p.Posted.Body})
	} else {
		unchanged++
	}
	posted := append([]store.Comment{}, p.Posted.Comments...)
	used := make([]bool, len(posted))
	match := func(d store.Comment, sameLine bool) int {
		for i, c := range posted {
			if !used[i] && c.Path == d.Path && (!sameLine || c.Line == d.Line) {
				return i
			}
		}
		return -1
	}
	var unmatched []store.Comment
	pair := func(d store.Comment, i int) {
		used[i] = true
		if strings.TrimSpace(d.Body) == strings.TrimSpace(posted[i].Body) {
			unchanged++
			return
		}
		changed = append(changed, edit{fmt.Sprintf("%s:%d", d.Path, d.Line), d.Body, posted[i].Body})
	}
	for _, d := range p.Draft.Comments {
		if i := match(d, true); i >= 0 {
			pair(d, i)
		} else {
			unmatched = append(unmatched, d)
		}
	}
	for _, d := range unmatched {
		if i := match(d, false); i >= 0 {
			pair(d, i)
		} else {
			dropped = append(dropped, d)
		}
	}
	for i, c := range posted {
		if !used[i] {
			added = append(added, c)
		}
	}
	return changed, dropped, added, unchanged
}

// EditsMarkdown shows how you edited agent drafts before posting them: each
// change as draft then posted, the comments you dropped and added, and how
// many pieces went out unchanged. It's meant for you, or an agent you ask to
// learn your style from it.
func EditsMarkdown(posts []store.Post) string {
	var b strings.Builder
	b.WriteString("# How I edit agent reviews\n\n")
	total := 0
	for _, p := range posts {
		changed, dropped, added, unchanged := diffPost(p)
		total += unchanged
		if len(changed)+len(dropped)+len(added) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s · %s\n\n%s\n\n", p.Key, p.At.Format("2006-01-02"), p.URL)
		for _, e := range changed {
			fmt.Fprintf(&b, "### %s\n\nDraft:\n\n%s\n\nPosted:\n\n%s\n\n", e.where, quote(e.draft), quote(e.posted))
		}
		for _, c := range dropped {
			fmt.Fprintf(&b, "### Dropped: %s:%d\n\n%s\n\n", c.Path, c.Line, quote(c.Body))
		}
		for _, c := range added {
			fmt.Fprintf(&b, "### Added: %s:%d\n\n%s\n\n", c.Path, c.Line, quote(c.Body))
		}
	}
	fmt.Fprintf(&b, "%d of the drafts' pieces (bodies and comments) went out unchanged across %d reviews.\n", total, len(posts))
	return b.String()
}

// editExamples is a prompt section with your n most recent rewrites of agent
// drafts, so the agent writes more the way you do.
func editExamples(posts []store.Post, n int) string {
	var ex []edit
	for i := len(posts) - 1; i >= 0 && len(ex) < n; i-- {
		changed, _, _, _ := diffPost(posts[i])
		for _, e := range changed {
			if len(ex) < n {
				ex = append(ex, e)
			}
		}
	}
	if len(ex) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## How the reviewer rewrites agent drafts\n\nRecent text an agent drafted, and what the reviewer posted instead. Write findings the way the reviewer does.\n\n")
	for _, e := range ex {
		fmt.Fprintf(&b, "Draft:\n%s\nPosted:\n%s\n\n", quote(oneLine(e.draft, 600)), quote(oneLine(e.posted, 600)))
	}
	return b.String()
}

func quote(s string) string { return indent(strings.TrimSpace(s), "> ") }

// summarySchema is the answer to a summary turn: markdown in summary.
const summarySchema = `{"type":"object","properties":{"summary":{"type":"string","description":"the summary, as markdown"}},"required":["summary"],"additionalProperties":false}`

// diffLimit caps how much of a PR's diff goes into a summary prompt.
const diffLimit = 150_000

// Summarize asks the main reviewer for a plain-language summary of a PR:
// what it does, the notable changes, and what deserves a careful look. It is
// one turn with the diff in the prompt, in an empty directory, so it is quick
// and needs no checkout. It returns the markdown and the model that wrote it.
func Summarize(cfg config.Config, j Job, diff string) (string, string, error) {
	be, err := For(cfg, "")
	if err != nil {
		return "", "", err
	}
	dir, err := os.MkdirTemp("", "prowl-summary-")
	if err != nil {
		return "", "", fmt.Errorf("summary dir: %w", err)
	}
	defer os.RemoveAll(dir)
	cut := ""
	if len(diff) > diffLimit {
		diff, cut = diff[:diffLimit], "\n(The diff is cut off here; it is longer.)\n"
	}
	prompt := fmt.Sprintf(`Summarize pull request #%d in %s for a reviewer about to read it.

Title: %s
Author: %s

Description:
"""
%s
"""

Diff:
`+"```diff\n%s\n```"+`%s
Write markdown with three parts, and nothing else:
1. Two or three sentences on what the PR does and why, in plain words. If the description is missing or wrong about something, say what the code actually does.
2. "Notable changes": up to six bullets, each naming the file or area in backticks and what changed there.
3. "Look closely at": up to four bullets on what deserves a careful read: migrations, auth, concurrency, public API or contract changes, deleted tests, anything surprising. Say "nothing stands out" if so.
Be concise. No praise, no review findings, no verdict.
`, j.Number, j.Repo, j.Title, j.Author, j.Body, diff, cut)
	model := cfg.Agent.SummaryModel(be.Name())
	used := model
	res, _, err := be.Review(Turn{Dir: dir, Prompt: prompt, Model: model, Schema: summarySchema, SetModel: func(m string) { used = m }})
	if err != nil {
		return "", "", fmt.Errorf("summarize %s#%d: %w", j.Repo, j.Number, err)
	}
	return strings.TrimSpace(res.Summary), used, nil
}
