package core

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
)

// Verdict is a reviewer's latest review of a PR.
type Verdict struct {
	Reviewer string `json:"reviewer"`
	State    string `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED
	Bot      bool   `json:"bot,omitempty"`
}

// AuthorItem is something raised on your PR for you to act on: a review's
// body or a finding in it, a conversation comment, a review thread, or a
// failing check.
type AuthorItem struct {
	Source     string   `json:"source"` // review | comment | thread | check
	Kind       string   `json:"kind"`   // finding (posted by prowl) | comment (from a person) | bot | check
	ThreadID   string   `json:"thread_id,omitempty"`
	Path       string   `json:"path,omitempty"`
	Line       int      `json:"line,omitempty"`
	Severity   Severity `json:"severity,omitempty"`
	Rule       string   `json:"rule,omitempty"`
	Author     string   `json:"author"`
	Outdated   bool     `json:"outdated,omitempty"`
	Resolved   bool     `json:"resolved,omitempty"`
	Failing    bool     `json:"failing,omitempty"` // a check that failed, rather than one with warnings
	Body       string   `json:"body"`
	Suggestion string   `json:"suggestion,omitempty"`
	Hunk       string   `json:"hunk,omitempty"`
	Replies    []string `json:"replies,omitempty"` // "login: text", oldest first
}

// AuthorReview is what reviewers left on your PR on GitHub, in the order to
// work through it.
type AuthorReview struct {
	Verdicts []Verdict    `json:"verdicts"`
	Items    []AuthorItem `json:"items"`
}

var suggestionRe = regexp.MustCompile("(?s)```suggestion\\s*\\n(.*?)```")

// ReviewsOnGitHub reads what was raised on the PR key, when it is yours,
// through the same agent.ActivityOf an AI reviewer's history comes from:
// each reviewer's verdict, as GitHub decides it (their latest approval or
// changes request; a comment, such as the empty review a thread reply makes,
// doesn't change it), the review bodies since that verdict, the recent
// conversation, the unresolved threads (all of them with all), and failing
// checks. Bots count as the bots setting says, and only each bot's latest
// review or comment. It returns nil when someone else wrote the PR.
func ReviewsOnGitHub(ctx context.Context, c *gh.Client, cfg config.Config, key string, all bool) (*AuthorReview, error) {
	repo, n, err := parseKey(key)
	if err != nil {
		return nil, err
	}
	me, err := c.Viewer(ctx)
	if err != nil {
		return nil, err
	}
	pr, err := c.PR(ctx, repo, n)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(pr.Author.Name(), me) {
		return nil, nil
	}
	d, err := c.FetchDetail(ctx, repo, n)
	if err != nil {
		return nil, err
	}
	return authorReview(agent.ActivityOf(d, cfg.BotSet()), me, all), nil
}

func authorReview(act agent.Activity, me string, all bool) *AuthorReview {
	out := &AuthorReview{Verdicts: []Verdict{}, Items: []AuthorItem{}}
	verdict := map[string]agent.Remark{}
	for _, r := range act.Reviews {
		if strings.EqualFold(r.Author, me) {
			continue
		}
		if v, ok := verdict[r.Author]; !ok || r.State != "COMMENTED" || v.State == "COMMENTED" {
			verdict[r.Author] = r
		}
	}
	changes := map[string]bool{}
	for _, who := range slices.Sorted(maps.Keys(verdict)) {
		v := verdict[who]
		if v.State != "DISMISSED" {
			out.Verdicts = append(out.Verdicts, Verdict{Reviewer: who, State: v.State, Bot: v.Bot})
		}
		changes[who] = v.State == "CHANGES_REQUESTED"
		for _, r := range act.Reviews {
			if r.Author == who && !r.At.Before(v.At) && !r.Superseded && strings.TrimSpace(r.Body) != "" {
				out.Items = append(out.Items, reviewBodyItems(r)...)
			}
		}
	}
	verdictRank := map[string]int{"CHANGES_REQUESTED": 0, "COMMENTED": 1, "APPROVED": 2}
	slices.SortFunc(out.Verdicts, func(a, b Verdict) int {
		return cmp.Or(cmp.Compare(verdictRank[a.State], verdictRank[b.State]), strings.Compare(a.Reviewer, b.Reviewer))
	})
	for _, cm := range act.Conversation() {
		if !strings.EqualFold(cm.Author, me) && !cm.Superseded && strings.TrimSpace(cm.Body) != "" {
			out.Items = append(out.Items, remarkItem("comment", cm))
		}
	}
	for _, t := range act.Threads {
		if len(t.Comments.Nodes) == 0 || (t.IsResolved && !all) {
			continue
		}
		first := t.Comments.Nodes[0]
		if strings.EqualFold(first.Author.Name(), me) && len(t.Comments.Nodes) == 1 {
			continue
		}
		it := remarkItem("thread", agent.Remark{Author: first.Author.Name(), Bot: act.Bot(first.Author), Body: first.Body})
		it.ThreadID, it.Path, it.Line = t.ID, t.Path, t.LineShown()
		it.Outdated, it.Resolved, it.Hunk = t.IsOutdated, t.IsResolved, lastHunkLines(first.DiffHunk, 6)
		for _, r := range t.Comments.Nodes[1:] {
			it.Replies = append(it.Replies, r.Author.Name()+": "+strings.TrimSpace(r.Body))
		}
		out.Items = append(out.Items, it)
	}
	for _, c := range act.Checks {
		body := strings.TrimSpace(strings.Join([]string{c.Title, c.Summary}, "\n\n"))
		for _, an := range c.Annotations {
			body += fmt.Sprintf("\n- %s:%d %s: %s", an.Path, an.Line, strings.ToLower(an.Level), an.Message)
		}
		out.Items = append(out.Items, AuthorItem{Source: "check", Kind: "check", Author: c.Name, Failing: c.State == "FAIL", Body: strings.TrimSpace(body)})
	}
	slices.SortStableFunc(out.Items, func(a, b AuthorItem) int {
		return cmp.Or(cmp.Compare(itemRank(a, changes), itemRank(b, changes)), strings.Compare(a.Path, b.Path), cmp.Compare(a.Line, b.Line))
	})
	return out
}

// remarkItem is a review body, comment or thread opener as an item: a
// finding prowl posted, what a bot said, or what a person said.
func remarkItem(source string, r agent.Remark) AuthorItem {
	it := AuthorItem{Source: source, Kind: "comment", Author: r.Author, Body: strings.TrimSpace(r.Body)}
	if sev, rule, text, ok := agent.ParseFinding(r.Body); ok {
		it.Kind, it.Severity, it.Rule, it.Body = "finding", Severity(sev), rule, text
	} else if r.Bot {
		it.Kind = "bot"
	}
	if m := suggestionRe.FindStringSubmatch(it.Body); m != nil {
		it.Suggestion = m[1]
	}
	return it
}

// itemRank orders what to work through: prowl's must-fix findings, failing
// checks, what a reviewer requesting changes said, should-fix findings,
// what people said, what bots said and checks' warnings, then nits.
func itemRank(it AuthorItem, changes map[string]bool) int {
	switch {
	case it.Kind == "finding" && it.Severity == MustFix:
		return 0
	case it.Kind == "check" && it.Failing:
		return 1
	case it.Kind != "finding" && it.Kind != "check" && changes[it.Author]:
		return 2
	case it.Kind == "finding" && it.Severity == ShouldFix:
		return 3
	case it.Kind == "comment":
		return 4
	case it.Kind == "bot" || it.Kind == "check":
		return 5
	}
	return 6
}

// reviewBodyItems splits a review's body into the findings prowl posted in
// it, one bullet each with the lines that continue it, and the rest, what
// the reviewer wrote, as one item. A bullet that starts like prowl's, with
// its severity in bold, runs on to its marker, so a list inside a finding
// stays part of it; one the reviewer wrote never does.
func reviewBodyItems(r agent.Remark) []AuthorItem {
	var items []AuthorItem
	var rest []string
	lines := strings.Split(r.Body, "\n")
	for i := 0; i < len(lines); i++ {
		bullet, ok := strings.CutPrefix(lines[i], "- ")
		if !ok {
			rest = append(rest, lines[i])
			continue
		}
		end := i + 1
		for end < len(lines) && strings.TrimSpace(lines[end]) != "" && !strings.HasPrefix(lines[end], "- ") {
			end++
		}
		if _, _, _, prowls := agent.ParseFinding(bullet); prowls && !agent.HasFindingMarker(strings.Join(lines[i:end], "\n")) {
			for j := end; j < len(lines) && strings.TrimSpace(lines[j]) != ""; j++ {
				if agent.HasFindingMarker(lines[j]) {
					end = j + 1
					break
				}
			}
		}
		text := strings.Join(append([]string{bullet}, lines[i+1:end]...), "\n")
		if sev, rule, body, ok := agent.ParseFinding(text); ok {
			items = append(items, AuthorItem{Source: "review", Kind: "finding", Severity: Severity(sev), Rule: rule, Author: r.Author, Body: body})
		} else {
			rest = append(rest, lines[i:end]...)
		}
		i = end - 1
	}
	if body := strings.TrimSpace(strings.Join(rest, "\n")); body != "" {
		items = append(items, remarkItem("review", agent.Remark{Author: r.Author, Bot: r.Bot, Body: body}))
	}
	return items
}

func lastHunkLines(hunk string, n int) string {
	lines := strings.Split(strings.TrimRight(hunk, "\n"), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// AuthorMarkdown is what was raised on your PR, for you or your agent to
// work through: the verdicts, then each item with where it is, who or what
// raised it (a person, a bot, prowl, a check), and its thread ID for
// drafting a reply.
func AuthorMarkdown(r *AuthorReview) string {
	var b strings.Builder
	b.WriteString("# On GitHub\n\n")
	if len(r.Verdicts) == 0 {
		b.WriteString("Nobody has reviewed it yet.\n")
	}
	for _, v := range r.Verdicts {
		who := v.Reviewer
		if v.Bot {
			who += " (bot)"
		}
		fmt.Fprintf(&b, "- %s: %s\n", who, strings.ToLower(strings.ReplaceAll(v.State, "_", " ")))
	}
	for i, it := range r.Items {
		where := "the PR"
		if it.Path != "" {
			where = fmt.Sprintf("`%s:%d`", it.Path, it.Line)
		}
		var head string
		switch it.Kind {
		case "finding":
			head = fmt.Sprintf("%s, a finding prowl posted for %s, on %s", it.Severity, it.Author, where)
		case "bot":
			head = fmt.Sprintf("%s, a bot, wrote on %s", it.Author, where)
		case "check":
			head = "the check " + it.Author + " has warnings"
			if it.Failing {
				head = "the check " + it.Author + " failed"
			}
		default:
			head = fmt.Sprintf("%s wrote on %s", it.Author, where)
		}
		fmt.Fprintf(&b, "\n## %d. %s\n\n", i+1, head)
		var tags []string
		if it.ThreadID != "" {
			tags = append(tags, "thread "+it.ThreadID)
		}
		if it.Outdated {
			tags = append(tags, "outdated: the code has changed since")
		}
		if it.Resolved {
			tags = append(tags, "resolved")
		}
		if it.Rule != "" {
			tags = append(tags, "per "+it.Rule)
		}
		if len(tags) > 0 {
			b.WriteString("_" + strings.Join(tags, " · ") + "_\n\n")
		}
		if it.Hunk != "" {
			b.WriteString("```diff\n" + it.Hunk + "\n```\n\n")
		}
		b.WriteString(it.Body + "\n")
		if it.Suggestion != "" {
			fmt.Fprintf(&b, "\nSuggested change, to replace %s:\n\n```\n%s```\n", where, it.Suggestion)
		}
		for _, rp := range it.Replies {
			b.WriteString("\n> " + strings.ReplaceAll(rp, "\n", "\n> ") + "\n")
		}
	}
	return b.String()
}
