package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/tmstorm/prowl/internal/core"
)

func (d *detail) buildReview() []brow {
	cur := d.current()
	d.flat = d.flat[:0]
	var rows []brow
	if len(cur) == 0 {
		text := []string{
			"",
			"  No AI review yet. " + sBold.Render(leader+"cc") + " starts one, " + sBold.Render(leader+"cC") + " picks the agent.",
			"",
			sDim.Render("  " + agentTitle + " checks the PR out in a worktree, reads the diff and the code"),
			sDim.Render("  around it, and returns findings here. Nothing is posted until you"),
			sDim.Render("  press " + leader + "p and save the review in your editor."),
		}
		switch {
		case !playful:
		case d.w >= 96:
			text = beside(append([]string{""}, owl("O", 0, true)...), owlWidth(true)+1, text)
		default:
			text = append(text, "")
			text = append(text, owl("O", 0, true)...)
		}
		for _, l := range text {
			rows = append(rows, plainRow(l))
		}
		return rows
	}
	total, inc := 0, len(core.Combine(cur, false))
	for _, r := range cur {
		total += len(r.Findings)
	}
	var head []string
	if total > 0 {
		h := fmt.Sprintf("%d of %d findings will be posted, as one review", inc, total)
		if len(cur) > 1 && inc < total {
			h += " (findings both agents raised count once)"
		}
		head = append(head, sDim.Render(h))
	}
	switch n := unsentReplies(cur); n {
	case 0:
	case 1:
		head = append(head, sYellow.Render("1 reply")+sDim.Render(" goes out with it, in its thread"))
	default:
		head = append(head, sYellow.Render(fmt.Sprintf("%d replies", n))+sDim.Render(" go out with it, each in its thread"))
	}
	if len(head) > 0 {
		rows = append(rows, plainRow("  "+strings.Join(head, sDim.Render(" · "))), plainRow(""))
	}
	for _, run := range cur {
		rows = append(rows, d.agentSection(run, cur)...)
		rows = append(rows, plainRow(""))
	}
	return rows
}

// agentSection is one agent's current review, folded under its name, with
// its earlier runs in a History fold at the end.
func (d *detail) agentSection(run *core.Review, cur []*core.Review) []brow {
	name := run.Reviewer()
	id := "agent:" + name
	folded := d.folded(id, false)
	a := &rowAdder{}
	a.begin(id, "", folded)
	head := foldGlyph(folded) + sTitle.Bold(true).Render(core.ReviewerTitle(name)) + sDim.Render(" · ") + runState(run)
	if run.Model != "" {
		head += sDim.Render(" · ") + sPink.Render(modelName(run.Model))
	}
	if run.Finished() {
		head += sDim.Render(fmt.Sprintf(" · %d findings", len(run.Findings)))
		if n := run.Unsent(); n > 0 {
			head += sYellow.Render(fmt.Sprintf(" · %d to send", n))
		}
		if n := run.StillOpen(); n > 0 {
			head += sYellow.Render(fmt.Sprintf(" · %d still open", n))
		}
		switch run.Action {
		case "close":
			head += sRed.Render(" · suggests closing")
		case "draft":
			head += sYellow.Render(" · suggests a draft")
		}
		head += sDim.Render(" · at " + short(run.HeadOid) + " · " + ranText(run))
		if run.Origin == "local" {
			head += sDim.Render(" · in your checkout, before the PR")
		}
		if run.HeadOid != d.item.HeadOid {
			head += sYellow.Render(" · new commits since")
		}
	}
	a.add(head)
	addF := func(s string, f int) {
		a.add(s)
		a.rows[len(a.rows)-1].finding = f
	}
	if !folded {
		if run.Status == core.ReviewRunning && run.Summary == "" {
			a.add("  " + sDim.Render("reviewing… progress is shown above"))
		}
		if run.Status == core.ReviewFailed {
			for _, l := range wrap(run.Error, d.w-4) {
				a.add("  " + sRed.Render(l))
			}
		}
		if g := guidanceLine(run.Guidance); g != "" {
			for _, l := range wrap(g, d.w-4) {
				a.add("  " + sDim.Render(l))
			}
		}
		if run.Summary != "" {
			a.add("")
			for _, l := range renderMarkdown(run.Summary, d.w-4) {
				a.add("  " + l)
			}
		}
		if run.Action != "" {
			a.add("")
			a.doc = "Closing or drafting"
			what, key := "Suggests closing this PR", leader+"X"
			if run.Action == "draft" {
				what, key = "Suggests making this PR a draft", leader+"D"
			}
			a.add("  " + sSection.Render(what) + sDim.Render(" · "+key+", or event: "+run.Action+" in "+leader+"p"))
			for _, l := range renderMarkdown(run.ActionReason, d.w-6) {
				a.add("    " + l)
			}
			a.doc = ""
		}
		if log := d.logs[name]; len(log) > 0 {
			a.add("")
			a.add("  " + sSection.Render("Notes") + sDim.Render(" · between you and "+core.ReviewerTitle(name)+", never posted"))
			for _, e := range log {
				who := sDim.Render("·")
				switch e.Who {
				case "you":
					who = sPink.Render("you")
				case "agent":
					who = sTitle.Render(strings.ToLower(core.ReviewerTitle(run.Agent)))
				}
				for i, l := range wrap(e.Text, d.w-14) {
					if i == 0 {
						a.add("    " + fit(who, 8) + l)
					} else {
						a.add("            " + l)
					}
				}
			}
		}
		if run.Finished() && len(run.Findings) == 0 {
			a.add("")
			a.add("  " + sGreen.Render("No findings."))
		}
		for i, f := range run.Findings {
			k := len(d.flat)
			d.flat = append(d.flat, findingRef{run.ID, i})
			addF("", k)
			box := sGreen.Render("[x]")
			title := sBold.Render(f.Title)
			if f.Excluded {
				box = sDim.Render("[ ]")
				title = sDim.Strikethrough(true).Render(f.Title)
			}
			sev := severityStyle(f.Severity).Render(fmt.Sprintf("%-10s", f.Severity))
			line := fmt.Sprintf("  %s %s %s", box, sev, title)
			if f.Posted {
				line += sGreen.Render("  posted")
			}
			addF(line, k)
			loc := sBlue.Render(fmt.Sprintf("%s:%d", f.Path, f.Line))
			if f.Path == "" {
				loc = sBlue.Render("PR") + sDim.Render(" (goes in the review body)")
			}
			if f.Rule != "" {
				loc += sDim.Render(" · per ") + sOrange.Render(f.Rule)
			}
			if also := alsoFoundBy(f, run, cur); also != "" {
				loc += sDim.Render(" · ") + sGreen.Render("also found by "+also)
			}
			addF("      "+loc, k)
			for _, l := range renderMarkdown(f.Body, d.w-8) {
				if f.Excluded {
					l = sDim.Render(ansi.Strip(l))
				}
				addF("      "+l, k)
			}
		}
		d.pointRows(a, run)
		d.replyRows(a, run)
	}
	rows := a.rows
	if !folded {
		rows = append(rows, d.historyRows(run)...)
	}
	for i := range rows {
		rows[i].agent = name
	}
	return rows
}

// historyRows are an agent's earlier runs on the PR, folded away, each with
// what became of its findings.
func (d *detail) historyRows(current *core.Review) []brow {
	var earlier []*core.Review
	for _, r := range d.runs {
		if r.Reviewer() == current.Reviewer() && r.ID != current.ID {
			earlier = append(earlier, r)
		}
	}
	if len(earlier) == 0 {
		return nil
	}
	id := "hist:" + current.Reviewer()
	folded := d.folded(id, true)
	a := &rowAdder{doc: "History"}
	a.begin(id, "", folded)
	n := "run"
	if len(earlier) > 1 {
		n = "runs"
	}
	a.add("  " + foldGlyph(folded) + sSection.Render("History") + sDim.Render(fmt.Sprintf(" · %d earlier %s", len(earlier), n)))
	rows := append([]brow{plainRow("")}, a.rows...)
	if folded {
		return rows
	}
	for _, r := range earlier {
		rid := fmt.Sprintf("run:%d", r.ID)
		rf := d.folded(rid, true)
		b := &rowAdder{doc: "History"}
		b.begin(rid, "", rf)
		var posted, excluded int
		for _, f := range r.Findings {
			switch {
			case f.Posted:
				posted++
			case f.Excluded:
				excluded++
			}
		}
		line := "    " + foldGlyph(rf) + sDim.Render(ranText(r)+" · at "+short(r.HeadOid)+" · ") + runState(r)
		if r.Model != "" {
			line += sDim.Render(" · " + modelName(r.Model))
		}
		line += sDim.Render(fmt.Sprintf(" · %d findings", len(r.Findings)))
		if posted+excluded > 0 {
			line += sDim.Render(fmt.Sprintf(" (%d posted, %d excluded)", posted, excluded))
		}
		b.add(line)
		if !rf {
			for _, f := range r.Findings {
				state := sDim.Render("not posted")
				switch {
				case f.Posted:
					state = sGreen.Render("posted")
				case f.Excluded:
					state = sDim.Render("excluded")
				}
				where := f.Path
				if where == "" {
					where = "PR"
				} else {
					where = fmt.Sprintf("%s:%d", f.Path, f.Line)
				}
				b.add("        " + severityStyle(f.Severity).Render(fmt.Sprintf("%-10s", f.Severity)) + f.Title + sDim.Render(" · "+where+" · ") + state)
			}
		}
		rows = append(rows, b.rows...)
	}
	return rows
}

func severityStyle(sev core.Severity) lipgloss.Style {
	switch sev {
	case core.MustFix:
		return sRed
	case core.ShouldFix:
		return sYellow
	}
	return sDim
}

// runState is a run's status in a word or two.
func runState(r *core.Review) string {
	switch r.Status {
	case core.ReviewRunning:
		return sYellow.Render("reviewing")
	case core.ReviewFailed:
		return sRed.Render("failed")
	case core.ReviewPosted:
		return sGreen.Render("posted")
	}
	return sGreen.Render("ready")
}

// alsoFoundBy names the other agents whose current review raised f too.
func alsoFoundBy(f core.Finding, run *core.Review, cur []*core.Review) string {
	var names []string
	for _, o := range cur {
		if o == run {
			continue
		}
		for _, g := range o.Findings {
			if core.Agree(f, g) {
				names = append(names, core.ReviewerTitle(o.Reviewer()))
				break
			}
		}
	}
	return strings.Join(names, ", ")
}

// guidanceLine summarises what a review followed.
func guidanceLine(raw string) string {
	var g struct {
		Guidelines []string `json:"guidelines"`
		Skills     []string `json:"skills"`
		Verified   []int    `json:"verified"`
	}
	if raw == "" || json.Unmarshal([]byte(raw), &g) != nil {
		return ""
	}
	gl, sk := "none", "none"
	if len(g.Guidelines) > 0 {
		gl = strings.Join(g.Guidelines, ", ")
	}
	if len(g.Skills) > 0 {
		sk = strings.Join(g.Skills, ", ")
	}
	out := "followed " + gl + " · skills: " + sk
	if len(g.Verified) == 2 && g.Verified[0] > 0 {
		out += fmt.Sprintf(" · verified %d → %d findings", g.Verified[0], g.Verified[1])
	}
	return out
}

// reviewStatus is the AI review tab's live top line: which agents are done,
// and what the running ones are doing.
func (d *detail) reviewStatus(m *Model) string {
	cur := d.current()
	if len(cur) == 0 {
		var names []string
		for _, n := range m.cfg.Agent.Reviewers {
			names = append(names, core.ReviewerTitle(n))
		}
		return sDim.Render("idle · " + leader + "cc runs " + strings.Join(names, " and "))
	}
	var parts []string
	running := 0
	for _, r := range cur {
		if r.Status == core.ReviewRunning {
			running++
		}
	}
	for _, r := range cur {
		name := core.ReviewerTitle(r.Reviewer())
		switch r.Status {
		case core.ReviewRunning:
			p := spinner(m) + " " + sYellow.Render(name) + sDim.Render(" "+time.Since(r.StartedAt).Round(time.Second).String())
			if running == 1 {
				p += sDim.Render(" · " + trunc(Progress(r, time.Now(), playful), max(d.w-50, 10)))
			}
			parts = append(parts, p)
		case core.ReviewFailed:
			parts = append(parts, sRed.Render("✗ "+name+" failed"))
		case core.ReviewPosted:
			parts = append(parts, sGreen.Render("↑ "+name+" posted"))
		default:
			parts = append(parts, sGreen.Render("✓ "+name))
		}
	}
	return strings.Join(parts, sDim.Render("  ·  "))
}

// replyRows lists the replies a reviewer drafted to review threads.
func (d *detail) replyRows(a *rowAdder, run *core.Review) {
	if len(run.Replies) == 0 {
		return
	}
	a.add("")
	a.add("  " + sSection.Render("Draft replies") + sDim.Render(" · x includes or excludes, c edits and sends one now"))
	for _, r := range run.Replies {
		add := func(s string) {
			a.add(s)
			a.rows[len(a.rows)-1].reply = r.ThreadID
		}
		a.add("")
		state := sGreen.Render("[x]") + " " + sYellow.Render("↩ draft")
		switch {
		case r.Posted:
			state = sGreen.Render("✓ sent")
		case r.Excluded:
			state = sDim.Render("[ ] ↩ left out")
		}
		add("  " + state + " " + sBlue.Render(fmt.Sprintf("%s:%d", r.Path, r.Line)) + sDim.Render(" · to "+r.To))
		opener, last := d.threadEnds(r)
		if opener != nil {
			add("      " + sDim.Render("you: "+firstLine(opener.Body, d.w-16)))
		}
		head, body := r.To+" wrote:", r.Last
		if last != nil {
			head, body = last.Author.Login+", "+agoText(last.CreatedAt)+":", last.Body
		}
		add("      " + sPink.Render(head))
		quote := renderMarkdown(body, d.w-10)
		for i, l := range quote {
			if i == quotedLines {
				add("      " + sBorder.Render("┃ ") + sDim.Render("… "+count(len(quote)-quotedLines, "more line")+"; the whole thread is in the Overview"))
				break
			}
			add("      " + sBorder.Render("┃ ") + l)
		}
		add("      " + sSection.Render("your reply:"))
		for _, l := range renderMarkdown(r.Body, d.w-8) {
			if r.Excluded {
				l = sDim.Strikethrough(true).Render(ansi.Strip(l))
			}
			add("      " + l)
		}
	}
}

// quotedLines is how much of the comment a draft reply answers it quotes.
const quotedLines = 12

// threadEnds are the first comment of a draft reply's thread, when you
// wrote it, and its latest comment, from the PR as last loaded; nil when
// the thread isn't loaded.
func (d *detail) threadEnds(r core.ThreadReply) (opener, last *core.Comment) {
	if d.det == nil {
		return nil, nil
	}
	for _, t := range d.det.Threads {
		if t.ID != r.ThreadID || len(t.Comments) == 0 {
			continue
		}
		cms := t.Comments
		if cms[0].Author.Login == d.me && len(cms) > 1 {
			opener = &cms[0]
		}
		return opener, &cms[len(cms)-1]
	}
	return nil, nil
}

// draftReply is the unsent reply a current review drafted for a thread, and
// the run it belongs to.
func (d *detail) draftReply(threadID string) (*core.ThreadReply, int64) {
	for _, run := range d.current() {
		for i := range run.Replies {
			if r := &run.Replies[i]; r.ThreadID == threadID && !r.Posted {
				return r, run.ID
			}
		}
	}
	return nil, 0
}

// replyPopup opens the reply box for a thread, filled with the agent's
// draft when there is one.
func (m *Model) replyPopup(d *detail, threadID string) tea.Cmd {
	var t *core.Thread
	if d.det != nil {
		for i := range d.det.Threads {
			if d.det.Threads[i].ID == threadID {
				t = &d.det.Threads[i]
			}
		}
	}
	if t == nil {
		m.fail("that thread isn't on the PR any more")
		return nil
	}
	draft, run := d.draftReply(threadID)
	body := ""
	if draft != nil {
		body = draft.Body
	}
	p := newTextPopup(m, popReply, d.item, nil, body)
	p.thread, p.replyRun = t, run
	m.popup = p
	return p.ta.Focus()
}

// pointVerdicts orders and marks the verdicts on points others raised:
// what still needs attention first.
var pointVerdicts = []struct {
	verdict, mark string
}{{"open", "●"}, {"unclear", "?"}, {"wrong", "✗"}, {"addressed", "✓"}}

// pointRows lists what reviewers, bots and checks raised, as the agent
// checked it against the code. They are for you only.
func (d *detail) pointRows(a *rowAdder, run *core.Review) {
	if len(run.Points) == 0 {
		return
	}
	a.add("")
	a.doc = "Re-reviews and points raised"
	a.add("  " + sSection.Render("Points raised") + sDim.Render(" · what others raised, checked against the code; never posted"))
	style := map[string]lipgloss.Style{"open": sYellow, "unclear": sDim, "wrong": sRed, "addressed": sGreen}
	for _, v := range pointVerdicts {
		for _, p := range run.Points {
			if p.Verdict != v.verdict {
				continue
			}
			who := p.Who
			if who == d.me {
				who = "you"
			}
			where := ""
			if p.Path != "" {
				where = sBlue.Render(fmt.Sprintf(" %s:%d", p.Path, p.Line))
			}
			a.add("  " + style[v.verdict].Render(v.mark+" "+v.verdict) + sDim.Render(" · ") + sPink.Render(who) + where)
			for _, l := range wrap(p.Point, d.w-8) {
				a.add("      " + l)
			}
			for _, l := range wrap(p.Why, d.w-8) {
				a.add("      " + sDim.Render(l))
			}
		}
	}
	a.doc = ""
}

// ranText is when a review started and how long it took.
func ranText(r *core.Review) string {
	s := r.StartedAt.Format("Jan 2 15:04")
	if !r.FinishedAt.IsZero() {
		s += ", took " + r.FinishedAt.Sub(r.StartedAt).Round(time.Second).String()
	}
	return s
}
