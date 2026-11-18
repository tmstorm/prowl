package tui

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
)

func popBox(title string, body string, w int, color string) string {
	st := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(color)).Padding(0, 1).Width(w)
	return st.Render(sTitle.Render(title) + "\n\n" + body)
}

func popWidth(m *Model) int { return min(max(m.w-12, 40), 96) }

// ---- text popup: inline comment / ask the agent to revise

type popKind int

const (
	popInline popKind = iota
	popRefine
	popReply
)

type textPopup struct {
	kind     popKind
	agent    string // popRefine: who revises
	item     core.PR
	sel      []diffLine
	thread   *core.Thread
	replyRun int64 // popReply: the review run whose drafted reply this is, 0 for your own
	ta       textarea.Model
}

func newTextPopup(m *Model, kind popKind, it core.PR, sel []diffLine, value string) *textPopup {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Prompt = sDim.Render("┃ ")
	ta.SetWidth(popWidth(m) - 4)
	ta.SetHeight(min(max(m.h/4, 4), 10))
	ta.SetValue(value)
	switch kind {
	case popInline:
		ta.Placeholder = "comment…"
	case popRefine:
		ta.Placeholder = `e.g. "drop the second finding, it's intentional" or "dig into the migration"`
	case popReply:
		ta.Placeholder = "reply…"
	}
	ta.Focus()
	return &textPopup{kind: kind, item: it, sel: sel, ta: ta}
}

func (p *textPopup) mode() mode { return modeInsert }

func (p *textPopup) forward(_ *Model, msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	p.ta, cmd = p.ta.Update(msg)
	return cmd
}

// update handles keys in a text popup. esc on an inline comment keeps it as
// pending, like leaving insert mode keeps your text; ctrl+c discards it.
func (p *textPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "ctrl+s":
		return p.commit(m)
	case "esc":
		if p.kind == popInline && strings.TrimSpace(p.ta.Value()) != "" {
			return p.commit(m)
		}
		m.popup = nil
		return nil
	case "ctrl+c":
		m.popup = nil
		m.info("discarded")
		return nil
	case "ctrl+e":
		return m.editText(p.ta.Value(), func(m *Model, text string) {
			p.ta.SetValue(text)
			m.popup = p
		})
	}
	return p.forward(m, k)
}

func (p *textPopup) commit(m *Model) tea.Cmd {
	m.popup = nil
	body := strings.TrimSpace(p.ta.Value())
	switch p.kind {
	case popInline:
		if d := m.details[p.item.Key]; d != nil {
			d.setPending(p.sel, body)
			m.core.SetPending(d.item.Key, d.pending)
			if body == "" {
				m.info("comment removed")
			} else {
				m.info(fmt.Sprintf("pending comment saved (%d total) · sent with your next review", len(d.pending)))
			}
		}
	case popRefine:
		if body != "" {
			return m.startReview(p.item, body, false, p.agent)
		}
	case popReply:
		if body == "" {
			return nil
		}
		co, key, id, run := m.core, p.item.Key, p.thread.ID, p.replyRun
		m.info("posting reply…")
		return func() tea.Msg {
			if run != 0 {
				return threadDoneMsg{key, "replied", co.PostReply(context.Background(), run, id, body)}
			}
			return threadDoneMsg{key, "replied", co.Reply(context.Background(), id, body)}
		}
	}
	return nil
}

func (p *textPopup) view(m *Model) string {
	w := popWidth(m)
	var title, help string
	var ctx []string
	switch p.kind {
	case popInline:
		first, last := p.sel[0], p.sel[len(p.sel)-1]
		a, _ := first.anchor()
		b, _ := last.anchor()
		title = fmt.Sprintf("Comment on %s:%d", last.path, b)
		if len(p.sel) > 1 {
			title = fmt.Sprintf("Comment on %s:%d-%d", last.path, a, b)
		}
		for i, l := range p.sel {
			if i == 4 {
				ctx = append(ctx, sDim.Render("  … "+count(len(p.sel)-4, "more line")))
				break
			}
			ctx = append(ctx, renderDiffLine(l, w-4))
		}
		help = "ctrl+s / esc save as pending · ctrl+e edit in " + m.cfg.EditorCmd() + " · ctrl+c discard"
	case popRefine:
		title = "Ask " + core.ReviewerTitle(p.agent) + " to revise · " + p.item.Key
		help = "ctrl+s send · ctrl+e edit in " + m.cfg.EditorCmd() + " · esc cancel"
	case popReply:
		line := p.thread.Line
		if line == 0 {
			line = p.thread.OriginalLine
		}
		title = fmt.Sprintf("Reply on %s:%d", p.thread.Path, line)
		if n := len(p.thread.Comments); n > 0 {
			last := p.thread.Comments[n-1]
			for _, l := range renderMarkdown(last.Body, w-6) {
				ctx = append(ctx, sDim.Render("┃ ")+l)
				if len(ctx) >= 6 {
					break
				}
			}
		}
		help = "ctrl+s post reply · ctrl+e edit in " + m.cfg.EditorCmd() + " · esc cancel"
	}
	body := ""
	if len(ctx) > 0 {
		body = strings.Join(ctx, "\n") + "\n\n"
	}
	body += p.ta.View() + "\n\n" + sDim.Render(help)
	return popBox(title, body, w, pal.Green)
}

// ---- snooze

type snoozePopup struct {
	item core.PR
	nav
}

var snoozeChoices = []struct {
	label string
	d     time.Duration
}{{"1 hour", time.Hour}, {"4 hours", 4 * time.Hour}, {"1 day", 24 * time.Hour}, {"1 week", 7 * 24 * time.Hour}}

func newSnooze(it core.PR) *snoozePopup { return &snoozePopup{item: it} }
func (p *snoozePopup) mode() mode       { return modeNormal }

func (p *snoozePopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc", "q", "ctrl+c":
		m.popup = nil
	case "1", "2", "3", "4":
		p.cur = int(k.String()[0] - '1')
		fallthrough
	case "enter", "l":
		c := snoozeChoices[p.cur]
		key := p.item.Key
		m.popup = nil
		if err := m.core.Snooze(key, c.d); err != nil {
			m.fail(err.Error())
			return nil
		}
		m.snoozed = m.core.Inbox().Snoozed
		m.rebuildRows()
		m.info(fmt.Sprintf("snoozed %s for %s · it comes back early if the author pushes", key, c.label))
		return m.schedulePreview()
	}
	return nil
}

func (p *snoozePopup) view(m *Model) string {
	var rows []string
	for i, c := range snoozeChoices {
		row := fmt.Sprintf(" %d  %s", i+1, c.label)
		if i == p.cur {
			row = highlight(row, 24, sTitle)
		}
		rows = append(rows, row)
	}
	return popBox("Snooze "+p.item.Key, strings.Join(rows, "\n")+"\n\n"+sDim.Render("1-4 or j/k enter · esc"), 36, pal.Accent)
}

// ---- fuzzy finder (telescope-ish)

type finder struct {
	input   filterInput
	results []int  // indexes into m.items
	remote  *prRef // a pasted link or ref to a PR not in the inbox
	nav            // with remote set, row 0 is the "open from GitHub" row
}

// prRef names a PR: owner/name and number.
type prRef struct {
	repo string
	n    int
}

var (
	prURL  = regexp.MustCompile(`github\.com/([\w.-]+/[\w.-]+)/pull/(\d+)`)
	prRef1 = regexp.MustCompile(`^([\w.-]+/)?([\w.-]+)#(\d+)$`)
)

// parseRef reads a PR link, owner/repo#N, or repo#N (a repo you have PRs in,
// or in your one org).
func parseRef(s string, m *Model) (prRef, bool) {
	s = strings.TrimSpace(s)
	if g := prURL.FindStringSubmatch(s); g != nil {
		n, _ := strconv.Atoi(g[2])
		return prRef{g[1], n}, true
	}
	g := prRef1.FindStringSubmatch(s)
	if g == nil {
		return prRef{}, false
	}
	n, _ := strconv.Atoi(g[3])
	if g[1] != "" {
		return prRef{g[1] + g[2], n}, true
	}
	var repos []string
	for _, it := range m.items {
		if nwo := it.Repo; it.RepoName == g[2] && !slices.Contains(repos, nwo) {
			repos = append(repos, nwo)
		}
	}
	if len(repos) == 1 {
		return prRef{repos[0], n}, true
	}
	if len(m.cfg.Orgs) == 1 {
		return prRef{m.cfg.Orgs[0] + "/" + g[2], n}, true
	}
	return prRef{}, false
}

func (f *finder) offset() int {
	if f.remote != nil {
		return 1
	}
	return 0
}

func newFinder(m *Model) *finder {
	f := &finder{input: newFilterInput("repo, title, author, number, or paste a PR link", popWidth(m)-8)}
	f.filter(m)
	return f
}

func (f *finder) mode() mode { return modeInsert }

func (f *finder) forward(m *Model, msg tea.Msg) tea.Cmd {
	return f.input.update(msg, func() { f.filter(m) })
}

// finderHit is a finder result and its fuzzy score.
type finderHit struct{ idx, score int }

func (f *finder) filter(m *Model) {
	q := strings.ToLower(f.input.Value())
	var res []finderHit
	for i, it := range m.items {
		hay := strings.ToLower(prName(it) + " " + it.Branch + " " + it.Title + " " + it.Author)
		if s, ok := fuzzy(q, hay); ok {
			res = append(res, finderHit{i, s})
		}
	}
	sort.SliceStable(res, func(a, b int) bool { return res[a].score > res[b].score })
	f.results = f.results[:0]
	for _, r := range res {
		f.results = append(f.results, r.idx)
	}
	f.cur = 0
	f.remote = nil
	if ref, ok := parseRef(f.input.Value(), m); ok {
		key := fmt.Sprintf("%s#%d", ref.repo, ref.n)
		f.remote = &ref
		for i, it := range m.items {
			if it.Key == key {
				f.remote, f.results = nil, []int{i}
			}
		}
	}
}

// fuzzy is a subsequence match that rewards consecutive and word-start hits.
func fuzzy(q, s string) (int, bool) {
	if q == "" {
		return 0, true
	}
	score, qi, run := 0, 0, 0
	qr := []rune(q)
	prev := ' '
	for _, r := range s {
		if qi < len(qr) && r == qr[qi] {
			qi++
			run++
			score += run * 2
			if prev == ' ' || prev == '-' || prev == '/' || prev == '#' || prev == '(' {
				score += 6
			}
		} else {
			run = 0
		}
		prev = r
	}
	return score, qi == len(qr)
}

// filterInput is a picker's query: what you type filters its list, and the
// arrow keys or ctrl+n/p/j/k move it.
type filterInput struct{ textinput.Model }

func newFilterInput(placeholder string, width int) filterInput {
	ti := textinput.New()
	ti.Prompt = sTitle.Render(" ") + " "
	ti.Placeholder = placeholder
	ti.Width = width
	ti.Focus()
	return filterInput{ti}
}

// key moves v, or types k into the query.
func (f *filterInput) key(v *nav, k tea.KeyMsg, filter func()) tea.Cmd {
	switch k.String() {
	case "down", "ctrl+n", "ctrl+j":
		v.move(1)
		return nil
	case "up", "ctrl+p", "ctrl+k":
		v.move(-1)
		return nil
	}
	return f.update(k, filter)
}

// update runs filter when msg changed the query: typed, pasted, or read
// from the clipboard (ctrl+v), which arrives as its own message.
func (f *filterInput) update(msg tea.Msg, filter func()) tea.Cmd {
	before := f.Value()
	var cmd tea.Cmd
	f.Model, cmd = f.Model.Update(msg)
	if f.Value() != before {
		filter()
	}
	return cmd
}

func (f *finder) update(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc", "ctrl+c":
		m.popup = nil
		return nil
	case "enter":
		m.popup = nil
		if f.remote != nil && f.cur == 0 {
			return m.fetchPR(*f.remote)
		}
		i := f.cur - f.offset()
		if i < 0 || i >= len(f.results) {
			return nil
		}
		target := m.items[f.results[i]]
		if !m.visibleItem(target) {
			m.showSnoozed = true
			m.filter = fAll
			m.rebuildRows()
		}
		for i, r := range m.rows {
			if !r.header && m.items[r.item].Key == target.Key {
				m.list.jump(i)
				m.list.align('z')
			}
		}
		m.focus = fList
		return m.schedulePreview()
	}
	return f.input.key(&f.nav, k, func() { f.filter(m) })
}

func (f *finder) view(m *Model) string {
	w := popWidth(m)
	f.list(m)
	var rows []string
	for i := f.top; i < min(f.top+f.h, f.n); i++ {
		var row string
		if j := i - f.offset(); j >= 0 {
			it := m.items[f.results[j]]
			row = fmt.Sprintf(" %s %s %s %s",
				fit(bucketStyle(it.Bucket).Render(it.Bucket.String()), 17),
				sDim.Render(shortRepo(it.RepoName)+prNumber(it)),
				it.Title, sPink.Render(it.Author))
		} else {
			row = " " + sTitle.Render("↗ open ") + sBold.Render(fmt.Sprintf("%s#%d", f.remote.repo, f.remote.n)) + sDim.Render(" from GitHub (not in your inbox)")
		}
		row = fit(row, w-4)
		if i == f.cur {
			row = highlight(row, w-4, sTitle)
		}
		rows = append(rows, row)
	}
	for len(rows) < f.h {
		rows = append(rows, "")
	}
	body := f.input.View() + "\n" + sBorder.Render(strings.Repeat("─", w-4)) + "\n" + strings.Join(rows, "\n") +
		"\n" + sDim.Render(fmt.Sprintf("%d / %d · ctrl+j/k move · enter go · esc", len(f.results), len(m.items)))
	return popBox("Find PR", body, w, pal.Accent)
}

// ---- help

type helpPopup struct{}

func (p *helpPopup) mode() mode { return modeNormal }
func (p *helpPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	m.popup = nil
	if k.String() == "d" {
		return m.openDocs("")
	}
	return nil
}

func (p *helpPopup) view(m *Model) string {
	w := min(popWidth(m), 84)
	colW := (w - 4) / 2
	k := func(key, desc string) string { return fit(sOrange.Render(key), 11) + desc }
	finder := "^p " + leader + leader
	if leaderPress == "," {
		finder = "^p"
	}
	col1 := []string{
		sSection.Render("Motions"),
		k("j k", "down / up"),
		k("gg G", "top / bottom"),
		k(":42", "go to line 42 (:q quits)"),
		k("^d ^u", "half page"),
		k("^f ^b", "full page"),
		k("^e ^y", "scroll view"),
		k("H M L", "screen top / middle / bottom"),
		k("zz zt zb", "center / top / bottom"),
		k("/ n N", "search, next, previous"),
		k("?", "this help"),
		k("K", "docs for what's under the cursor"),
		k("← → ↑ ↓", "panes and rows, like h l k j"),
		"",
		sSection.Render("Panes"),
		k("0 1", "inbox pane / PR pane"),
		k("2-9 tab", "tabs in the focused pane"),
		k("l enter", "open PR pane"),
		k("+", "PR pane full width / back"),
		k("h esc q", "back to inbox"),
		k(finder, "find a PR, or paste a link"),
		k("o", "open in GitHub (the check, on one)"),
		k("v", "select lines in a PR"),
		k("y yy", "copy the selection / the line"),
		k("ys yu", "copy Slack line / PR URL"),
		k("R "+leader+"r", "refresh"),
		"",
		sSection.Render("Leader") + sDim.Render(" · a few of them"),
		k(leader, "every action; or click \"menu\" below"),
		k(leader+"c", leaderEntry("c").desc),
		k(leader+"f", leaderEntry("f").desc),
		k(leader+"l", leaderEntry("l").desc),
	}
	col2 := []string{
		sSection.Render("Overview"),
		k("]n [n", "next / prev new activity"),
		k("]t [t", "next / prev thread"),
		k("gl", "failed check: end of its log"),
		"",
		sSection.Render("Diff"),
		k("]c [c", "next / prev hunk"),
		k("]f [f", "next / prev file"),
		k("]t [t", "next / prev thread"),
		k("c", "comment line / selection"),
		k("S", "suggest a change"),
		k("dd", "drop pending comment"),
		k("V", "mark file viewed"),
		k("D", "since review / full diff"),
		k("s", "side by side / unified"),
		"",
		sSection.Render("Folds"),
		k("za zo zc", "toggle / open / close"),
		k("zR zM", "open all / close all"),
		"",
		sSection.Render("AI review"),
		k("x enter", "include / exclude finding or reply"),
		k("c", "edit and send a draft reply now"),
		k("]f [f", "next / prev finding"),
		k("m u", "must-fix only / include all"),
		"",
		sSection.Render("Chat"),
		k("ctrl+\\", "hide it (keeps running)"),
	}
	var rows []string
	for i := 0; i < max(len(col1), len(col2)); i++ {
		a, b := "", ""
		if i < len(col1) {
			a = col1[i]
		}
		if i < len(col2) {
			b = col2[i]
		}
		rows = append(rows, fit(a, colW)+fit(b, colW))
	}
	return popBox("Keys", strings.Join(rows, "\n")+"\n\n"+sDim.Render("prowl "+core.BuildVersion()+" · d or :help opens the docs · any other key closes"), w, pal.Accent)
}

// ---- repo picker (telescope-ish)

type repoPicker struct {
	kind    string // "repo" or "type"
	input   filterInput
	all     []repoCount
	results []repoCount
	nav
}

type repoCount struct {
	name           string
	nwo            string // owner/name
	total, needsMe int
}

func newRepoPicker(m *Model) *repoPicker {
	counts := map[string]*repoCount{}
	for _, it := range m.items {
		nwo := it.Repo
		if counts[nwo] == nil {
			counts[nwo] = &repoCount{name: it.RepoName, nwo: nwo}
		}
		counts[nwo].total++
		if it.Bucket.NeedsMe() {
			counts[nwo].needsMe++
		}
	}
	for _, nwo := range m.orgRepos {
		if counts[nwo] == nil {
			_, n, _ := strings.Cut(nwo, "/")
			counts[nwo] = &repoCount{name: n, nwo: nwo}
		}
	}
	names := map[string]int{}
	for _, c := range counts {
		names[c.name]++
	}
	p := &repoPicker{kind: "repo", input: newFilterInput("repo", 50)}
	for _, c := range counts {
		if names[c.name] > 1 {
			c.name = c.nwo
		}
		p.all = append(p.all, *c)
	}
	sort.Slice(p.all, func(i, j int) bool {
		a, b := p.all[i], p.all[j]
		switch {
		case a.needsMe != b.needsMe:
			return a.needsMe > b.needsMe
		case (a.total > 0) != (b.total > 0):
			return a.total > 0
		}
		return a.name < b.name
	})
	p.filter()
	return p
}

func (p *repoPicker) mode() mode { return modeInsert }

func (p *repoPicker) forward(_ *Model, msg tea.Msg) tea.Cmd { return p.input.update(msg, p.filter) }

func (p *repoPicker) filter() {
	q := strings.ToLower(p.input.Value())
	p.results = p.results[:0]
	for _, c := range p.all {
		if _, ok := fuzzy(q, strings.ToLower(c.name)); ok {
			p.results = append(p.results, c)
		}
	}
	p.cur = 0
}

func (p *repoPicker) update(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc", "ctrl+c":
		m.popup = nil
		return nil
	case "enter":
		m.popup = nil
		if p.cur >= len(p.results) {
			return nil
		}
		pick := p.results[p.cur]
		name := pick.name
		var fetch tea.Cmd
		if p.kind == "type" {
			m.typeFocus = name
		} else {
			m.repoFocus = pick.nwo
			if !m.watching(pick.nwo) {
				m.core.Peek(pick.nwo)
				fetch = m.refresh()
			}
		}
		m.filter = fAll
		m.focus = fList
		m.rebuildRows()
		m.list.first()
		m.info("showing only " + name + " · esc shows everything")
		if fetch != nil {
			m.info("fetching every open PR in " + name + " · esc shows everything")
		}
		return tea.Batch(m.schedulePreview(), fetch)
	}
	return p.input.key(&p.nav, k, p.filter)
}

func (p *repoPicker) view(m *Model) string {
	w := min(popWidth(m), 60)
	var rows []string
	p.list(m)
	for i := p.top; i < min(p.top+p.h, p.n); i++ {
		c := p.results[i]
		prs := fmt.Sprintf("%3d PRs", c.total)
		switch c.total {
		case 0:
			prs = "     –  "
		case 1:
			prs = "  1 PR "
		}
		nameS := repoColor(c.name).Render(c.name)
		if p.kind == "type" {
			nameS = typeStyle(c.name).Render(c.name)
		}
		row := " " + fit(nameS, 30) + sDim.Render(prs)
		if c.needsMe > 0 {
			row += "  " + sRed.Render(fmt.Sprintf("%d need you", c.needsMe))
		}
		row = fit(row, w-4)
		if i == p.cur {
			row = highlight(row, w-4, sTitle)
		}
		rows = append(rows, row)
	}
	body := p.input.View() + "\n" + sBorder.Render(strings.Repeat("─", w-4)) + "\n" + strings.Join(rows, "\n") +
		"\n\n" + sDim.Render("enter focus · esc in the inbox shows all again")
	title := "Focus repo"
	if p.kind == "type" {
		title = "Focus commit type"
	}
	return popBox(title, body, w, pal.Accent)
}

// newTypePicker lists the conventional commit types in the inbox, in sort order.
func newTypePicker(m *Model) *repoPicker {
	counts := map[string]*repoCount{}
	for _, it := range m.items {
		n := titleType(it.Title)
		if counts[n] == nil {
			counts[n] = &repoCount{name: n}
		}
		counts[n].total++
		if it.Bucket.NeedsMe() {
			counts[n].needsMe++
		}
	}
	order := m.cfg.Types()
	p := &repoPicker{kind: "type", input: newFilterInput("type", 50)}
	for _, c := range counts {
		p.all = append(p.all, *c)
	}
	sort.SliceStable(p.all, func(i, j int) bool {
		ri := core.TypeRank(core.Title{Type: p.all[i].name, OK: p.all[i].name != "other"}, order)
		rj := core.TypeRank(core.Title{Type: p.all[j].name, OK: p.all[j].name != "other"}, order)
		if ri != rj {
			return ri < rj
		}
		return p.all[i].name < p.all[j].name
	})
	p.filter()
	return p
}

// ---- agent picker: run one review with a different agent

type agentPicker struct {
	item  core.PR
	names []string
	errs  []error
	nav
}

func newAgentPicker(m *Model, it core.PR) *agentPicker {
	p := &agentPicker{item: it}
	for _, n := range core.Agents() {
		p.names = append(p.names, n)
		p.errs = append(p.errs, m.core.CheckAgent(n))
		if n == m.cfg.Agent.Main() {
			p.cur = len(p.names) - 1
		}
	}
	return p
}

func (p *agentPicker) mode() mode { return modeNormal }

func (p *agentPicker) update(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc", "ctrl+c", "q":
		m.popup = nil
	case "enter":
		m.popup = nil
		if err := p.errs[p.cur]; err != nil {
			m.fail(err.Error())
			return nil
		}
		return m.startReview(p.item, "", false, p.names[p.cur])
	}
	return nil
}

func (p *agentPicker) view(m *Model) string {
	w := min(popWidth(m), 64)
	var rows []string
	for i, n := range p.names {
		row := " " + fit(sBold.Render(core.ReviewerTitle(n)), 10) + sDim.Render(n)
		if n == m.cfg.Agent.Main() {
			row += sDim.Render(" · default")
		}
		if p.errs[i] != nil {
			row += "  " + sRed.Render("unavailable")
		}
		row = fit(row, w-4)
		if i == p.cur {
			row = highlight(row, w-4, sTitle)
		}
		rows = append(rows, row)
	}
	note := ""
	if err := p.errs[p.cur]; err != nil {
		note = "\n" + sErr.Render(" "+err.Error())
	}
	body := strings.Join(rows, "\n") + "\n" + note + "\n" + sDim.Render(" enter review with it · esc cancel · the default is set in options")
	return popBox("Review "+p.item.Key+" with", body, w, pal.Accent)
}

// ---- mouse: where each list popup draws its rows (see rowPopup)

func (p *snoozePopup) list(*Model) (*nav, int) {
	p.setLen(len(snoozeChoices), len(snoozeChoices))
	return &p.nav, 3
}

func (f *finder) list(m *Model) (*nav, int) {
	f.setLen(len(f.results)+f.offset(), min(m.h-10, 16))
	return &f.nav, 5
}

func (p *repoPicker) list(*Model) (*nav, int) {
	p.setLen(len(p.results), 16)
	return &p.nav, 5
}

func (p *agentPicker) list(*Model) (*nav, int) {
	p.setLen(len(p.names), len(p.names))
	return &p.nav, 3
}

// ---- profile picker: run a named review profile

type profilePicker struct {
	item     core.PR
	profiles []config.Profile
	nav
}

func (p *profilePicker) mode() mode { return modeNormal }

func (p *profilePicker) update(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc", "ctrl+c", "q":
		m.popup = nil
	case "enter":
		m.popup = nil
		return m.startProfile(p.item, p.profiles[p.cur])
	}
	return nil
}

func (p *profilePicker) view(m *Model) string {
	w := min(popWidth(m), 76)
	var rows []string
	for i, pr := range p.profiles {
		who := m.cfg.Agent.Main()
		if len(pr.Reviewers) > 0 {
			who = strings.Join(pr.Reviewers, ", ")
		}
		row := " " + fit(sBold.Render(pr.Name), 16) + sDim.Render(firstLine(pr.Prompt, w-36)) + "  " + sPink.Render(who)
		row = fit(row, w-4)
		if i == p.cur {
			row = highlight(row, w-4, sTitle)
		}
		rows = append(rows, row)
	}
	help := sDim.Render(" enter run it · esc cancel · add your own under [[agent.profiles]]")
	return popBox("Run a review profile on "+p.item.Key, strings.Join(rows, "\n")+"\n\n"+help, w, pal.Accent)
}

func (p *profilePicker) list(*Model) (*nav, int) {
	p.setLen(len(p.profiles), len(p.profiles))
	return &p.nav, 3
}

// reviewerPicker asks people and teams to review a PR: everyone who can
// review it, filtered as you type, with the ones it's with already first.
type reviewerPicker struct {
	item    core.PR
	all     []reviewerRow
	input   filterInput
	results []int // indexes into all
	nav
}

type reviewerRow struct {
	login, name string
	state       string // where they stand on the PR: asked, approved, …; "" for none
	on          bool
}

type candidatesMsg struct {
	item                  core.PR
	candidates, suggested []core.Candidate
	err                   error
}

// askForReviews loads who can review the PR, then opens the picker.
func (m *Model) askForReviews(it core.PR) tea.Cmd {
	co := m.core
	m.info("loading who can review…")
	return func() tea.Msg {
		c, err := co.ReviewCandidates(context.Background(), it.Key)
		if err != nil {
			return candidatesMsg{item: it, err: err}
		}
		s, err := co.SuggestedReviewers(context.Background(), it.Key)
		return candidatesMsg{item: it, candidates: c, suggested: s, err: err}
	}
}

// newReviewerPicker lists the people and teams asked already and the people
// who reviewed (not you, the author or a bot), then the people GitHub
// suggests, then everyone else who can review. Someone who reviewed and
// isn't asked now starts checked, to ask them again.
func newReviewerPicker(m *Model, it core.PR, candidates, suggested []core.Candidate) *reviewerPicker {
	p := &reviewerPicker{item: it}
	seen := map[string]bool{}
	add := func(r reviewerRow) {
		if !seen[strings.ToLower(r.login)] {
			seen[strings.ToLower(r.login)] = true
			p.all = append(p.all, r)
		}
	}
	for _, rr := range it.Requests {
		if n := rr.Login; n != "" && n != m.viewer {
			add(reviewerRow{login: n, state: "asked"})
		} else if slug := rr.Team; n == "" && slug != "" {
			owner, _, _ := strings.Cut(it.Repo, "/")
			add(reviewerRow{login: owner + "/" + slug, name: rr.Name, state: "asked"})
		}
	}
	states := map[string]string{"APPROVED": "approved", "CHANGES_REQUESTED": "changes requested", "COMMENTED": "commented", "DISMISSED": "dismissed"}
	reviews := slices.Clone(it.Reviews)
	slices.SortStableFunc(reviews, func(a, b core.PRReview) int { return b.SubmittedAt.Compare(a.SubmittedAt) })
	for _, r := range reviews {
		n := r.Author.Login
		if r.Author.Bot || n == m.viewer || n == it.Author || states[r.State] == "" {
			continue
		}
		add(reviewerRow{login: n, state: states[r.State], on: true})
	}
	for _, c := range suggested {
		if c.Login != m.viewer && c.Login != it.Author {
			add(reviewerRow{login: c.Login, name: c.Name, state: "suggested"})
		}
	}
	for _, c := range candidates {
		if c.Login != m.viewer && c.Login != it.Author {
			add(reviewerRow{login: c.Login, name: c.Name})
		}
	}
	for i := range p.all {
		for _, c := range candidates {
			if p.all[i].name == "" && strings.EqualFold(c.Login, p.all[i].login) {
				p.all[i].name = c.Name
			}
		}
	}
	p.input = newFilterInput("name, login or org/team", 40)
	p.filter()
	return p
}

func (p *reviewerPicker) mode() mode { return modeInsert }

func (p *reviewerPicker) filter() {
	q := strings.ToLower(p.input.Value())
	p.results = p.results[:0]
	for i, r := range p.all {
		if _, ok := fuzzy(q, strings.ToLower(r.login+" "+r.name)); ok {
			p.results = append(p.results, i)
		}
	}
}

func (p *reviewerPicker) forward(_ *Model, msg tea.Msg) tea.Cmd { return p.input.update(msg, p.filter) }

func (p *reviewerPicker) update(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case " ", "tab":
		if p.cur < len(p.results) {
			r := &p.all[p.results[p.cur]]
			r.on = !r.on
		}
	case "enter":
		return p.ask(m)
	case "esc", "ctrl+c":
		m.popup = nil
	default:
		return p.input.key(&p.nav, k, p.filter)
	}
	return nil
}

// ask requests everyone checked, and the name typed when it matches nobody.
// With nobody checked, a filter you typed asks the row under the cursor;
// enter on an untouched list asks nobody.
func (p *reviewerPicker) ask(m *Model) tea.Cmd {
	var names []string
	for _, r := range p.all {
		if r.on {
			names = append(names, r.login)
		}
	}
	switch typed := strings.TrimSpace(p.input.Value()); {
	case len(p.results) == 0 && typed != "":
		names = append(names, strings.Split(typed, ",")...)
	case len(names) == 0 && typed != "" && p.cur < len(p.results):
		names = []string{p.all[p.results[p.cur]].login}
	}
	m.popup = nil
	if len(names) == 0 {
		return nil
	}
	co, key, who := m.core, p.item.Key, strings.Join(names, ", ")
	m.info("asking " + who + " to review…")
	return func() tea.Msg {
		return threadDoneMsg{key, "asked " + who + " to review", co.RequestReviews(context.Background(), key, names)}
	}
}

func (p *reviewerPicker) list(m *Model) (*nav, int) {
	p.setLen(len(p.results), max(m.h-14, 6))
	return &p.nav, 5
}

// click checks or unchecks the row clicked.
func (p *reviewerPicker) click(m *Model, _, _, row int) tea.Cmd { return m.check(&p.nav, row) }

func (p *reviewerPicker) view(m *Model) string {
	w := min(popWidth(m), 80)
	var lines []string
	for _, i := range p.results {
		r := p.all[i]
		box := sDim.Render("[ ]")
		if r.on {
			box = sGreen.Render("[x]")
		}
		login := r.login
		if strings.Contains(login, "/") {
			login = "@" + login
		}
		row := " " + box + " " + fit(sBold.Render(login), 24) + sDim.Render(r.name)
		if r.state != "" {
			row += sDim.Render(" · ") + sYellow.Render(r.state)
		}
		lines = append(lines, fit(row, w-4))
	}
	p.list(m)
	if len(lines) == 0 {
		lines = append(lines, sDim.Render(" nobody matches: enter asks "+strings.TrimSpace(p.input.Value())))
	} else {
		lines[p.cur] = highlight(lines[p.cur], w-4, sTitle)
		lines = lines[p.top:min(p.top+p.h, p.n)]
	}
	help := sDim.Render(" type to filter · ↑↓ move · space or click check · enter ask · esc cancel")
	return popBox("Ask for reviews on "+prName(p.item), p.input.View()+"\n"+sBorder.Render(strings.Repeat("─", w-4))+"\n"+
		strings.Join(lines, "\n")+"\n\n"+help, w, pal.Accent)
}
