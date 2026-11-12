package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/tmstorm/prowl/internal/gh"
)

// activity is one event in a PR's timeline.
type activity struct {
	at     time.Time
	who    *gh.Actor
	glyph  string
	action string // styled, e.g. "requested changes"
	body   string // markdown, may be empty
	id     string // fold id
	thread string
}

func glyphs(nerd bool) map[string]string {
	if nerd {
		return map[string]string{"approve": "", "changes": "", "review": "", "comment": "",
			"thread": "", "push": "", "force": "", "request": "", "ready": ""}
	}
	return map[string]string{"approve": "✓", "changes": "✗", "review": "◆", "comment": "›",
		"thread": "»", "push": "↑", "force": "↑", "request": "→", "ready": "○"}
}

func (d *detail) activities(nerd bool) []activity {
	g := glyphs(nerd)
	var out []activity
	for _, r := range d.det.Reviews {
		if r.State == "PENDING" {
			continue
		}
		body := strings.TrimSpace(htmlComment.ReplaceAllString(r.Body, ""))
		a := activity{at: r.SubmittedAt, who: r.Author, body: body, id: "review:" + r.ID}
		switch r.State {
		case "APPROVED":
			a.glyph, a.action = sGreen.Render(g["approve"]), sGreen.Render("approved")
		case "CHANGES_REQUESTED":
			a.glyph, a.action = sRed.Render(g["changes"]), sRed.Render("requested changes")
		case "DISMISSED":
			a.glyph, a.action = sDim.Render(g["review"]), sDim.Render("review dismissed")
			switch r.PreviousState {
			case "APPROVED":
				a.action = sDim.Render("approved, since dismissed")
			case "CHANGES_REQUESTED":
				a.action = sDim.Render("requested changes, since dismissed")
			}
		default:
			if body == "" {
				continue
			}
			a.glyph, a.action = sBlue.Render(g["review"]), sBlue.Render("reviewed")
		}
		out = append(out, a)
	}
	for _, c := range d.det.Comments {
		out = append(out, activity{at: c.CreatedAt, who: c.Author, body: c.Body, id: "comment:" + c.ID,
			glyph: sBlue.Render(g["comment"]), action: sDim.Render("commented")})
	}
	for _, t := range d.det.Threads {
		line := t.Line
		if line == 0 {
			line = t.OriginalLine
		}
		for i, c := range t.Comments.Nodes {
			verb := "replied on "
			if i == 0 {
				verb = "commented on "
			}
			out = append(out, activity{at: c.CreatedAt, who: c.Author, body: c.Body, id: "tc:" + c.ID, thread: t.ID,
				glyph: sYellow.Render(g["thread"]), action: sDim.Render(verb) + sBlue.Render(fmt.Sprintf("%s:%d", t.Path, line))})
		}
	}
	out = append(out, d.pushes(g)...)
	for _, e := range d.det.Events {
		switch e.Typename {
		case "HeadRefForcePushedEvent":
			out = append(out, activity{at: e.CreatedAt, who: e.Actor, id: "",
				glyph: sOrange.Render(g["force"]), action: sOrange.Render("force-pushed") + sDim.Render(" (rebased or rewrote history)")})
		case "ReviewRequestedEvent":
			if e.RequestedReviewer == nil {
				continue
			}
			n := e.RequestedReviewer.Login
			if n == "" {
				n = "@" + e.RequestedReviewer.Slug
			}
			out = append(out, activity{at: e.CreatedAt, who: e.Actor,
				glyph: sDim.Render(g["request"]), action: sDim.Render("requested review from ") + sPink.Render(n)})
		case "ReadyForReviewEvent":
			out = append(out, activity{at: e.CreatedAt, who: e.Actor,
				glyph: sGreen.Render(g["ready"]), action: sGreen.Render("marked ready for review")})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.After(out[j].at) })
	return out
}

// pushCommit is a commit grouped into a push.
type pushCommit struct {
	at        time.Time
	who       *gh.Actor
	oid, head string
}

// pushes groups commits made close together by the same author into one event.
func (d *detail) pushes(g map[string]string) []activity {
	var cs []pushCommit
	for _, e := range d.det.Events {
		if e.Typename != "PullRequestCommit" || e.Commit == nil {
			continue
		}
		var who *gh.Actor
		if e.Commit.Author != nil {
			who = e.Commit.Author.User
		}
		cs = append(cs, pushCommit{e.Commit.CommittedDate, who, e.Commit.Oid, e.Commit.MessageHeadline})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].at.Before(cs[j].at) })
	var out []activity
	for i := 0; i < len(cs); {
		j := i + 1
		for j < len(cs) && author(cs[j].who) == author(cs[i].who) && cs[j].at.Sub(cs[j-1].at) < 15*time.Minute {
			j++
		}
		group := cs[i:j]
		var b strings.Builder
		for _, c := range group {
			fmt.Fprintf(&b, "- `%s` %s\n", gh.Short(c.oid), c.head)
		}
		action := "pushed 1 commit"
		if len(group) > 1 {
			action = fmt.Sprintf("pushed %d commits", len(group))
		}
		out = append(out, activity{at: group[len(group)-1].at, who: group[0].who, body: b.String(),
			id: "push:" + group[0].oid, glyph: sBlue.Render(g["push"]), action: sBlue.Render(action)})
		i = j
	}
	return out
}

// timeColumn renders a fixed-width age, brighter the newer it is.
func timeColumn(t time.Time, fresh bool) string {
	s := fmt.Sprintf("%4s", ago(t))
	switch {
	case fresh:
		return sYellow.Bold(true).Render(s)
	case time.Since(t) < 24*time.Hour:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Fg)).Render(s)
	}
	return sDim.Render(s)
}

func (d *detail) activityRows(w int) []brow {
	acts := d.activities(nerdIcons)
	var rows []brow
	newAbove, dividerDone := false, false
	const authorW = 16
	for _, ac := range acts {
		bot := ac.who.IsBot(botLogins)
		fresh := !bot && !d.read(ac.at, ac.who)
		older := d.read(ac.at, nil)
		if older && newAbove && !dividerDone {
			dividerDone = true
			label := " you last looked " + ago(d.seen()) + " ago · older below "
			rows = append(rows, plainRow(" "+sYellow.Render("──"+label+strings.Repeat("─", max(w-lipgloss.Width(label)-4, 2)))))
		}
		newAbove = newAbove || !older
		folded := ac.body == "" || d.folded(ac.id, !fresh)
		name := author(ac.who)
		if ac.who != nil && ac.who.Login == d.me {
			name = "you"
		}
		nameS := sPink.Render(name)
		if bot {
			nameS = sDim.Render(name)
		}
		marker := "  "
		if ac.body != "" {
			marker = foldGlyph(folded)
		}
		head := " " + timeColumn(ac.at, fresh) + " " + ac.glyph + " " + marker + fit(nameS, authorW) + " " + ac.action
		if folded && ac.body != "" {
			head += sDim.Render("  " + firstLine(ac.body, w))
		}
		a := &rowAdder{}
		a.begin(ac.id, ac.thread, folded)
		if ac.id == "" {
			a.end()
		}
		a.add(head)
		if !folded {
			for _, l := range renderMarkdown(ac.body, w-10, themeName) {
				a.add("          " + l)
			}
		}
		if fresh && len(a.rows) > 0 {
			a.rows[0].fresh = true
		}
		rows = append(rows, a.rows...)
	}
	if len(rows) == 0 {
		rows = append(rows, plainRow(sDim.Render("  nothing yet")))
	}
	return rows
}
