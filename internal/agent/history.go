package agent

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/store"
)

// history is what a reviewer would read on the PR page before reviewing:
// earlier reviews, the conversation, and open threads.
type history struct {
	text           string // markdown section for the prompt
	reader         string
	lastOid        string // commit the reader last reviewed
	lastAt         time.Time
	lastState      string
	lastOidMissing bool
	lastDismissed  bool
	base           string   // local review: the commit the branch left the default branch at
	byAgent        bool     // lastOid is the reader's last AI review, not a review on GitHub
	readerSince    []string // the reader's comments after their last review
	// threads the reader is in where someone else spoke last, by thread ID:
	// the ones the agent may draft a reply to
	waiting map[string]store.ThreadReply
}

var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

func clip(s string, n int) string {
	s = strings.TrimSpace(htmlComment.ReplaceAllString(s, ""))
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + " …"
	}
	return s
}

func login(a *gh.Actor) string {
	if a == nil {
		return "ghost"
	}
	return a.Login
}

func day(t time.Time) string { return t.Format("2006-01-02 15:04") }

func loadHistory(c *gh.Client, j Job) history {
	h := history{reader: j.Viewer}
	if c == nil {
		h.text = "(review history unavailable: not logged in to GitHub)\n"
		return h
	}
	if h.reader == "" {
		h.reader, _ = c.Viewer()
	}
	det, err := c.FetchDetail(j.Repo, j.Number)
	if err != nil {
		h.text = "(review history unavailable: " + err.Error() + ")\n"
		return h
	}

	var b strings.Builder

	latestBot := map[string]time.Time{}
	for _, r := range det.Reviews {
		if r.Author.IsBot(nil) && r.SubmittedAt.After(latestBot[login(r.Author)]) {
			latestBot[login(r.Author)] = r.SubmittedAt
		}
	}
	for _, cm := range det.Comments {
		if cm.Author.IsBot(nil) && cm.CreatedAt.After(latestBot[login(cm.Author)]) {
			latestBot[login(cm.Author)] = cm.CreatedAt
		}
	}
	superseded := func(a *gh.Actor, at time.Time) bool {
		return a.IsBot(nil) && at.Before(latestBot[login(a)])
	}
	if len(det.Reviews) > 0 {
		b.WriteString("### Reviews (oldest first)\n\n")
		for _, r := range det.Reviews {
			if r.State == "PENDING" {
				continue
			}
			oid := ""
			if r.Commit != nil {
				oid = gh.Short(r.Commit.Oid)
			}
			state := r.State
			if state == "DISMISSED" && r.PreviousState != "" {
				state = r.PreviousState
			}
			if login(r.Author) == h.reader && (state == "APPROVED" || state == "CHANGES_REQUESTED" || state == "COMMENTED") && r.SubmittedAt.After(h.lastAt) {
				h.lastAt, h.lastState, h.lastDismissed = r.SubmittedAt, state, r.State == "DISMISSED"
				if r.Commit != nil {
					h.lastOid = r.Commit.Oid
				}
			}
			body := clip(r.Body, 1500)
			if body == "" && r.State == "COMMENTED" {
				continue
			}
			if superseded(r.Author, r.SubmittedAt) {
				fmt.Fprintf(&b, "- %s, %s on %s: superseded by its later review; its points are settled\n", login(r.Author), day(r.SubmittedAt), oid)
				continue
			}
			fmt.Fprintf(&b, "- %s, %s, %s on %s", login(r.Author), reviewState(r), day(r.SubmittedAt), oid)
			if body != "" {
				b.WriteString(":\n" + indent(body, "  > "))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	cs := det.Comments
	if len(cs) > 15 {
		cs = cs[len(cs)-15:]
	}
	if len(cs) > 0 {
		b.WriteString("### Conversation (latest comments, oldest first)\n\n")
		for _, cm := range cs {
			body := clip(cm.Body, 1200)
			if body == "" {
				continue
			}
			if superseded(cm.Author, cm.CreatedAt) {
				fmt.Fprintf(&b, "- %s, %s: superseded by its later comment; its points are settled\n", login(cm.Author), day(cm.CreatedAt))
				continue
			}
			fmt.Fprintf(&b, "- %s, %s:\n%s\n", login(cm.Author), day(cm.CreatedAt), indent(body, "  > "))
		}
		b.WriteString("\n")
	}
	for _, cm := range det.Comments {
		if login(cm.Author) == h.reader && cm.CreatedAt.After(h.lastAt) && !h.lastAt.IsZero() {
			h.readerSince = append(h.readerSince, clip(cm.Body, 2000))
		}
	}

	h.waiting = map[string]store.ThreadReply{}
	var wb strings.Builder
	for _, t := range det.Threads {
		cms := t.Comments.Nodes
		if len(cms) == 0 || !slices.ContainsFunc(cms, func(c gh.Comment) bool { return login(c.Author) == h.reader }) {
			continue
		}
		last := cms[len(cms)-1]
		if login(last.Author) == h.reader || last.Author.IsBot(nil) {
			continue
		}
		line := t.Line
		if line == 0 {
			line = t.OriginalLine
		}
		h.waiting[t.ID] = store.ThreadReply{ThreadID: t.ID, Path: t.Path, Line: line, To: login(last.Author), Last: clip(last.Body, 2000)}
		fmt.Fprintf(&wb, "- id `%s` · %s:%d", t.ID, t.Path, line)
		if t.IsResolved {
			wb.WriteString(" (resolved)")
		}
		wb.WriteString("\n")
		for _, cm := range cms {
			fmt.Fprintf(&wb, "  - %s, %s:\n%s\n", login(cm.Author), day(cm.CreatedAt), indent(clip(cm.Body, 2000), "    > "))
		}
	}
	if wb.Len() > 0 {
		b.WriteString("### Threads waiting on " + h.reader + "\n\n" + h.reader + " is in these threads and someone else spoke last. Their ids are for `thread_replies`.\n\n" + wb.String() + "\n")
	}

	open, resolved := 0, 0
	var ob strings.Builder
	for _, t := range det.Threads {
		if _, ok := h.waiting[t.ID]; ok {
			if !t.IsResolved {
				open++
			} else {
				resolved++
			}
			continue
		}
		if t.IsResolved {
			resolved++
			continue
		}
		open++
		line := t.Line
		if line == 0 {
			line = t.OriginalLine
		}
		fmt.Fprintf(&ob, "- %s:%d", t.Path, line)
		if t.IsOutdated {
			ob.WriteString(" (outdated)")
		}
		ob.WriteString("\n")
		for _, cm := range t.Comments.Nodes {
			fmt.Fprintf(&ob, "  - %s: %s\n", login(cm.Author), strings.ReplaceAll(clip(cm.Body, 500), "\n", " "))
		}
	}
	fmt.Fprintf(&b, "### Review threads\n\n%d unresolved, %d resolved. Resolved threads are settled: do not raise them again as findings.\n", open, resolved)
	if open > 0 {
		b.WriteString("\nUnresolved:\n" + ob.String())
	}
	b.WriteString(checksText(det.Checks))
	h.text = b.String()
	return h
}

// checksText is what failed checks, and checks with annotations, reported
// on the head commit: points to check like any reviewer's.
func checksText(checks []gh.Check) string {
	var b strings.Builder
	for _, c := range checks {
		if c.State != "FAIL" && len(c.Annotations) == 0 {
			continue
		}
		fmt.Fprintf(&b, "- %s (%s)", c.Name, strings.ToLower(c.State))
		if c.Title != "" {
			b.WriteString(": " + c.Title)
		}
		b.WriteString("\n")
		if s := clip(c.Summary, 800); s != "" {
			b.WriteString(indent(s, "  > ") + "\n")
		}
		for _, a := range c.Annotations {
			fmt.Fprintf(&b, "  - %s:%d %s: %s\n", a.Path, a.Line, strings.ToLower(a.Level), clip(a.Message, 300))
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n### Checks\n\nWhat failing checks, and checks with annotations, reported on the head commit:\n\n" + b.String()
}

// reviewState is how a review reads in the history: a dismissed review
// keeps what it was, since dismissal doesn't change what the reviewer decided.
func reviewState(r gh.Review) string {
	s := strings.ToLower(strings.ReplaceAll(r.State, "_", " "))
	if r.State == "DISMISSED" && r.PreviousState != "" {
		s = strings.ToLower(strings.ReplaceAll(r.PreviousState, "_", " ")) + ", later dismissed"
	}
	return s
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// reReviewSection tells the agent to review like the reader does on a second
// pass: start from what changed and from what the reader said is left.
func reReviewSection(j Job, h history) string {
	if h.lastOid == "" || h.reader == "" {
		return ""
	}
	var b strings.Builder
	if h.byAgent {
		fmt.Fprintf(&b, "## This is a re-review\n\nThis is %s's own PR. An AI review for them last looked at it on %s, at commit %s (perhaps in their checkout, before the PR was opened).\n",
			h.reader, day(h.lastAt), gh.Short(h.lastOid))
	} else {
		fmt.Fprintf(&b, "## This is a re-review\n\n%s, who you are reviewing for, last reviewed this PR on %s at commit %s (%s).\n",
			h.reader, day(h.lastAt), gh.Short(h.lastOid), strings.ToLower(strings.ReplaceAll(h.lastState, "_", " ")))
		if h.lastDismissed {
			b.WriteString("GitHub has since dismissed that review, often automatically when new commits are pushed. That doesn't change what the reader decided at that commit.\n")
		}
	}
	if len(h.readerSince) > 0 {
		fmt.Fprintf(&b, "Since then %s wrote:\n\n", h.reader)
		for _, c := range h.readerSince {
			b.WriteString(indent(c, "> ") + "\n\n")
		}
		b.WriteString("Treat that as the reader's current position: what they consider settled and what they said is left.\n")
	}
	if h.lastOidMissing {
		b.WriteString("\nThat commit is no longer fetchable, so compare by the PR's commit dates instead.\n")
	} else {
		fmt.Fprintf(&b, "\nSee what changed since then with:\n  git range-diff origin/%s...%s origin/%s...HEAD\n  git diff %s HEAD\n(the plain diff also includes anything the branch picked up from %s in a rebase).\n",
			j.Base, gh.Short(h.lastOid), j.Base, gh.Short(h.lastOid), j.Base)
	}
	b.WriteString(`
On a re-review:
1. Check each request the reader (and other reviewers) left, in ` + "`points`" + `.
2. Review what changed since the reader's last review.
3. Only raise problems in code the reader already reviewed when they are must-fix (a bug, security issue, data loss, or broken contract). Do not re-review the whole PR.
`)
	return b.String()
}
