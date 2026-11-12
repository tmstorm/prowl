package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/inbox"
	"github.com/tmstorm/prowl/internal/store"
)

// ---- layout

type layout struct {
	split              bool
	listW, detW        int
	contentH           int
	detX               int
	detTextW, detBodyH int
}

const detHeaderH = 4 // title, meta, status, blank

func (m *Model) layout() layout {
	l := layout{contentH: max(m.h-1, 5)}
	if m.w >= 100 && !(m.fullPR && m.focus == fDetail) {
		l.split = true
		l.listW = min(max(m.w*42/100, 44), 92)
		l.detW = m.w - l.listW
		l.detX = l.listW
	} else {
		l.listW, l.detW = m.w, m.w
	}
	l.detTextW = max(l.detW-4, 10)
	l.detBodyH = max(l.contentH-2-detHeaderH, 3)
	return l
}

// legendHeight is the rows the inbox legend takes (separator + lines).
func (m *Model) legendHeight(l layout) int {
	if !m.cfg.ShowLegend || len(m.rows) == 0 {
		return 0
	}
	return 1 + len(m.legend(l.listW-2))
}

// summaryHeight is the triage line above the inbox rows.
func (m *Model) summaryHeight() int {
	if len(m.items) == 0 {
		return 0
	}
	return 2
}

func (m *Model) layoutNavs() {
	l := m.layout()
	m.list.setLen(len(m.rows), max(l.contentH-2-m.legendHeight(l)-len(m.tipLines(l.listW-2))-m.summaryHeight(), 1))
	for _, d := range m.details {
		d.resize(l.detTextW, l.detBodyH)
	}
}

// ---- top level

// View draws the panes and one bottom row, like nvim with cmdheight=0: the
// statusline, replaced by the command line while it has something to show.
func (m *Model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	l := m.layout()
	m.hits = hitMap{}
	var panes string
	switch {
	case l.split:
		panes = lipgloss.JoinHorizontal(lipgloss.Top, m.listPane(l, 0), m.detailPane(l, l.detX))
	case m.focus == fDetail && m.current() != nil:
		panes = m.detailPane(l, 0)
	default:
		panes = m.listPane(l, 0)
	}

	bottom := m.cmdline()
	if bottom == "" {
		bottom = m.statusline()
	}
	screen := panes + "\n" + bottom
	if m.mode == modeLeader {
		wk := m.whichKey()
		x, y := place(wk, m.w, m.h, "bottom-right")
		for _, c := range m.wkCells {
			key := c.key
			m.hits.add(x+2+c.x, y+1+c.y, c.w, 1, func(m *Model) tea.Cmd {
				return m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
			})
		}
		screen = overlay(screen, wk, m.w, m.h, "bottom-right")
	}
	if m.confirm != nil {
		screen = m.confirmView(screen)
	}
	if m.popup != nil {
		pv := m.popup.view(m)
		m.popX, m.popY = place(pv, m.w, m.h, "center")
		m.popW, m.popH = lipgloss.Width(pv), lipgloss.Height(pv)
		screen = overlay(screen, pv, m.w, m.h, "center")
	}
	return screen
}

// box draws a rounded border with a title embedded in the top edge, lazygit style.
func box(title, footer string, lines []string, w, h int, focused bool) string {
	bs := sBorder
	if focused {
		bs = sBorderOn
	}
	inner := w - 2
	top := bs.Render("╭─") + title
	top += bs.Render(strings.Repeat("─", max(inner-1-lipgloss.Width(title), 0)) + "╮")
	top = trunc(top, w)
	var b strings.Builder
	b.WriteString(top)
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		b.WriteString("\n" + bs.Render("│") + fit(line, inner) + bs.Render("│"))
	}
	bottom := strings.Repeat("─", max(inner-lipgloss.Width(footer)-1, 0))
	b.WriteString("\n" + bs.Render("╰"+bottom) + footer + bs.Render("─╯"))
	return b.String()
}

// ---- list pane

func (m *Model) listPane(l layout, x int) string {
	focused := m.focus == fList || !l.split
	w, h := l.listW, l.contentH

	num := sDim.Render("[0]")
	if m.focus == fList {
		num = sTitle.Render("[0]")
	}
	lead := num + " "
	if m.loading && len(m.items) > 0 {
		lead += spinner(m) + " "
	}
	if m.repoFocus != "" {
		lead += repoLabel(m.repoFocus) + " "
	}
	if m.typeFocus != "" {
		lead += typeStyle(m.typeFocus).Bold(true).Render(m.typeFocus) + " "
	}
	if m.repoFocus != "" || m.typeFocus != "" {
		lead += sBorder.Render("│")
	}

	shortNames := map[filter]string{fNeedsMe: "Me", fAll: "All", fMine: "Mine", fApproved: "Appr", fWaiting: "Wait"}

	label := func(level, i int, f filter) string {
		name, n := filterNames[f], m.tabCount(f)
		if level >= 2 {
			name = shortNames[f]
		}
		switch {
		case level == 0:
			return fmt.Sprintf(" [%d] %s (%s) ", i+2, name, n)
		case level <= 2:
			return fmt.Sprintf("[%d] %s (%s)", i+2, name, n)
		case level == 3 || f == m.filter:
			return fmt.Sprintf("[%d]%s(%s)", i+2, name, n)
		}
		return fmt.Sprintf("[%d](%s)", i+2, n)
	}
	avail := w - 4 - lipgloss.Width(lead)
	var chosen []string
	for level := 0; level <= 4; level++ {
		chosen = chosen[:0]
		for i, f := range filterOrder {
			chosen = append(chosen, label(level, i, f))
		}
		if lipgloss.Width(strings.Join(chosen, "│")) <= avail {
			break
		}
	}
	var parts []string
	tx := x + 2 + lipgloss.Width(lead)
	for i, f := range filterOrder {
		label := chosen[i]
		var s string
		if f == m.filter {
			s = sTitle.Render(label)
		} else {
			s = sDim.Render(label)
		}
		f := f
		m.hits.add(tx, 0, lipgloss.Width(label), 1, func(m *Model) tea.Cmd { m.setFilter(f); return nil })
		tx += lipgloss.Width(label) + 1
		parts = append(parts, s)
	}
	title := lead + strings.Join(parts, sBorder.Render("│"))

	inner := w - 2
	var lines []string
	if len(m.items) == 0 && m.loading {
		lines = skeletonList(m, inner, h-2)
		if !l.split {
			lines = splash(m, inner, h-2)
		}
	} else {
		cols := m.listColumns()
		tip := m.tipLines(inner)
		legendH, sumH := m.legendHeight(l)+len(tip), m.summaryHeight()
		if sumH > 0 {
			lines = append(lines, m.summaryLine(inner), sBorder.Render(strings.Repeat("┄", inner)))
		}
		for i := m.list.top; i < len(m.rows) && i < m.list.top+h-2-legendH-sumH; i++ {
			row := m.renderListRow(m.rows[i], inner, i == m.list.cur, cols)
			lines = append(lines, row)
			idx, y := i, 1+sumH+i-m.list.top
			m.hits.add(x+1, y, inner, 1, func(m *Model) tea.Cmd {
				m.focus = fList
				if r := m.rows[idx]; r.header {
					m.toggleGroup(groupID(r), nil)
					return nil
				}
				m.list.jump(idx)
				return m.schedulePreview()
			})
		}
		if len(m.rows) == 0 && !m.loading {
			lines = append(lines, m.emptyInbox(inner, h-2-legendH-sumH)...)
		}
		for legendH > 0 && len(lines) < h-2-legendH {
			lines = append(lines, "")
		}
		if legendH > len(tip) {
			lines = append(lines, sBorder.Render("┄┄ ")+sDim.Render("legend")+sBorder.Render(" "+strings.Repeat("┄", max(inner-10, 0))))
			lines = append(lines, m.legend(inner)...)
			m.hits.add(x+1, len(lines), inner, 1, func(m *Model) tea.Cmd {
				m.popup = &helpPopup{}
				return nil
			})
		}
		lines = append(lines, tip...)
	}
	m.hits.addScroll(x, 0, w, h, &m.list)
	footer := ""
	if it := m.selectedItem(); it != nil {
		pos, total := 0, 0
		for i, r := range m.rows {
			if !r.header {
				total++
				if i <= m.list.cur {
					pos++
				}
			}
		}
		footer = sDim.Render(fmt.Sprintf(" %d of %d ", pos, total))
	}
	if len(m.snoozed) > 0 {
		s := fmt.Sprintf(" %d snoozed ", len(m.snoozed))
		if m.showSnoozed {
			s = fmt.Sprintf(" showing %d snoozed ", len(m.snoozed))
		}
		footer = sDim.Render(s) + footer
	}
	return box(title, footer, lines, w, h, focused)
}

// repoTrim is stripped from repo names on screen (the repo_prefix setting).
var repoTrim = ""

func shortRepo(name string) string {
	if repoTrim != "" && strings.HasPrefix(name, repoTrim) && len(name) > len(repoTrim) {
		return name[len(repoTrim):]
	}
	return name
}

// maxRepoWidth caps the inbox's repo column.
const maxRepoWidth = 20

// squeeze cuts a name longer than w in the middle, keeping both ends:
// repos that share a prefix differ at the end.
func squeeze(name string, w int) string {
	r := []rune(name)
	if len(r) <= w || w < 3 {
		return name
	}
	head := (w - 1) / 2
	return string(r[:head]) + "…" + string(r[len(r)-(w-1-head):])
}

// repoColor gives each repo a stable color so the eye can find it.
func repoColor(name string) lipgloss.Style {
	if !colorRepos {
		return sBlue
	}
	colors := []string{pal.Cyan, pal.Pink, pal.Green, pal.Orange, pal.Accent}
	h := 0
	for _, r := range name {
		h = h*31 + int(r)
	}
	if h < 0 {
		h = -h
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(colors[h%len(colors)]))
}

func reviewDot(status string) string {
	switch status {
	case "approved":
		return sGreen.Render("●")
	case "changes requested":
		return sRed.Render("●")
	case "needs rebase":
		return sYellow.Render("↻")
	case "draft":
		return sDim.Render("◌")
	}
	return sDim.Render("○")
}

var bucketTags = map[inbox.Bucket]string{
	inbox.NeedsReview: "review", inbox.ReReview: "re-review", inbox.Addressed: "addressed",
	inbox.Activity: "activity", inbox.TeamRequest: "team", inbox.Mine: "mine",
	inbox.WaitingOnAuthor: "waiting", inbox.Approved: "approved", inbox.Involved: "involved",
	inbox.ReApprove: "re-approve", inbox.Unclaimed: "unclaimed", inbox.Watching: "watching", inbox.Recheck: "re-check", inbox.Local: "local",
}

// legend explains the icons currently on screen, wrapped to at most two lines.
func (m *Model) legend(w int) []string {
	seen := map[string]bool{}
	for _, r := range m.rows {
		if r.header {
			continue
		}
		it := m.items[r.item]
		switch {
		case it.ChangedSinceApproval:
			seen["changed"] = true
		default:
			seen[it.ReviewStatus] = true
		}
		if s := it.PR.CIState(); s != "" {
			seen["ci:"+s] = true
		}
		if runs := m.review[it.PR.Key()]; runs != nil {
			seen["review"] = true
			seen["replies"] = seen["replies"] || unsentReplies(runs) > 0
		}
		if _, ok := m.snoozed[it.PR.Key()]; ok {
			seen["snoozed"] = true
		}
		if threadText(it) != "" {
			seen["threads"] = true
		}
	}
	nerd := m.cfg.NerdFont
	var parts []string
	add := func(key, icon, label string) {
		if seen[key] {
			parts = append(parts, icon+" "+sDim.Render(label))
		}
	}
	add("approved", sGreen.Render("●"), "approved")
	add("changed", sGreen.Render("◐"), "approved, changed since")
	add("changes requested", sRed.Render("●"), "changes requested")
	add("needs rebase", sYellow.Render("↻"), "needs rebase")
	add("needs review", sDim.Render("○"), "awaiting review")
	add("draft", sDim.Render("◌"), "draft")
	add("ci:SUCCESS", ciBadge("SUCCESS", nerd), "CI passed")
	if seen["ci:FAILURE"] || seen["ci:ERROR"] {
		parts = append(parts, ciBadge("FAILURE", nerd)+" "+sDim.Render("CI failed"))
	}
	if seen["ci:PENDING"] || seen["ci:EXPECTED"] {
		parts = append(parts, ciBadge("PENDING", nerd)+" "+sDim.Render("CI running"))
	}
	if seen["review"] {
		icon := "C"
		if nerd {
			icon = "\U000f06a9"
		}
		parts = append(parts, sBlue.Render(icon)+" "+sDim.Render("AI review"))
		if seen["replies"] {
			parts = append(parts, sYellow.Render(icon)+" "+sDim.Render("replies to send"))
		}
	}
	add("snoozed", sDim.Render("z"), "snoozed")
	if seen["threads"] {
		parts = append(parts, sYellow.Render("2/3")+" "+sDim.Render("threads resolved"))
	}
	parts = append(parts, sYellow.Render("1d")+" "+sDim.Render("due"), sRed.Bold(true).Render("3d")+" "+sDim.Render("overdue"))

	var lines []string
	line := ""
	for _, p := range parts {
		cand := p
		if line != "" {
			cand = line + sDim.Render("  ") + p
		}
		if lipgloss.Width(cand) > w-2 && line != "" {
			lines = append(lines, " "+line)
			line = p
			continue
		}
		line = cand
	}
	if line != "" {
		lines = append(lines, " "+line)
	}
	if len(lines) > 2 {
		lines = lines[:2]
	}
	return append(lines, " "+keysLine(w-2))
}

// tipLines is the one-time tip, or news about prowl's own updates, at the
// bottom of the inbox under its own divider, so it shows with the legend off.
func (m *Model) tipLines(w int) []string {
	if m.tip == "" {
		return nil
	}
	lines := []string{sBorder.Render("┄┄ ") + sDim.Render("prowl") + sBorder.Render(" "+strings.Repeat("┄", max(w-9, 0)))}
	for _, l := range wrap(strings.ReplaceAll(m.tip, "<leader>", leader), w-2) {
		lines = append(lines, " "+sPink.Render(strings.TrimRight(l, " ")))
	}
	return lines
}

// keysLine is the legend's reminder of the basic keys for people new to
// prowl, shortened to fit w; "? keys" always stays.
func keysLine(w int) string {
	hints := []string{"↑↓ move", "←→ panes", "enter open", "? keys"}
	for len(hints) > 1 {
		if line := sDim.Render(strings.Join(hints, " · ")); lipgloss.Width(line) <= w {
			return line
		}
		hints = hints[1:]
	}
	return sDim.Render(hints[0])
}

// listCols are column widths shared by every row so they line up.
type listCols struct {
	repo, num      int
	snooze, review bool // only reserve icon slots something in the list uses
	size           int  // width of the "+120 -30" column, 0 = hidden
	threads        int  // width of the thread progress column, 0 = hidden
}

func (m *Model) listColumns() listCols {
	c := listCols{num: 2}
	for _, r := range m.rows {
		if r.header {
			continue
		}
		pr := m.items[r.item].PR
		c.repo = max(c.repo, len(shortRepo(pr.Repository.Name)))
		c.num = max(c.num, lipgloss.Width(prNumber(pr)))
		if _, ok := m.snoozed[pr.Key()]; ok {
			c.snooze = true
		}
		if m.review[pr.Key()] != nil {
			c.review = true
		}
		c.size = max(c.size, len(sizeText(pr.Additions, pr.Deletions)))
		if t := threadText(m.items[r.item]); t != "" {
			c.threads = max(c.threads, len(t)+1)
		}
	}
	c.repo = min(c.repo, maxRepoWidth)
	return c
}

// sizeText is a PR's change size, e.g. "+120 -30" or "+1.2k -40".
func sizeText(add, del int) string {
	n := func(v int) string {
		if v >= 1000 {
			return fmt.Sprintf("%.1fk", float64(v)/1000)
		}
		return strconv.Itoa(v)
	}
	switch {
	case del == 0:
		return "+" + n(add)
	case add == 0:
		return "-" + n(del)
	}
	return "+" + n(add) + " -" + n(del)
}

// sizeStyle: small changes fade, large ones stand out.
func sizeStyle(add, del int) lipgloss.Style {
	switch n := add + del; {
	case n >= 800:
		return sOrange
	case n >= 100:
		return lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Fg))
	}
	return sDim
}

// threadText is review-thread progress, "2/3" resolved, on PRs where the
// author is answering review feedback.
func threadText(it inbox.Item) string {
	if it.ThreadCount == 0 {
		return ""
	}
	switch it.Bucket {
	case inbox.Addressed, inbox.ReReview, inbox.WaitingOnAuthor, inbox.Mine:
		return fmt.Sprintf("%d/%d", it.ThreadCount-it.UnresolvedCount, it.ThreadCount)
	}
	return ""
}

// waitStyle colors how long a PR has waited on you: its bucket color until
// stale_after, yellow past it, bold red past twice that.
func (m *Model) waitStyle(it inbox.Item) lipgloss.Style {
	due := m.cfg.StaleAfter.Duration
	if !it.Bucket.NeedsMe() || due <= 0 {
		return bucketStyle(it.Bucket)
	}
	switch waited := time.Since(it.Since); {
	case waited > 2*due:
		return sRed.Bold(true)
	case waited > due:
		return sYellow
	}
	return bucketStyle(it.Bucket)
}

// snoozedNeeds counts the snoozed PRs that would need you, and when the
// first comes back.
func (m *Model) snoozedNeeds() (int, time.Time) {
	n, first := 0, time.Time{}
	for _, it := range m.items {
		until, ok := m.snoozed[it.PR.Key()]
		if !ok || !it.Bucket.NeedsMe() {
			continue
		}
		n++
		if first.IsZero() || until.Before(first) {
			first = until
		}
	}
	return n, first
}

// needs counts the PRs waiting on you (snoozed ones aside) and returns the
// one that has waited longest.
func (m *Model) needs() (int, *inbox.Item) {
	var need int
	var oldest *inbox.Item
	for i := range m.items {
		it := &m.items[i]
		if _, ok := m.snoozed[it.PR.Key()]; ok || !it.Bucket.NeedsMe() {
			continue
		}
		need++
		if oldest == nil || it.Since.Before(oldest.Since) {
			oldest = it
		}
	}
	return need, oldest
}

// statusOwl is the statusline's owl: asleep when nothing needs you, awake
// when something does, wide-eyed and red when something is overdue.
func (m *Model) statusOwl() (face, color string) {
	need, oldest := m.needs()
	switch {
	case need == 0:
		return "(-,-)", pal.Muted
	case m.cfg.StaleAfter.Duration > 0 && time.Since(oldest.Since) > m.cfg.StaleAfter.Duration:
		return "(O,O)", pal.Red
	}
	return "(o,o)", pal.Yellow
}

// windowTitle is the terminal tab's title, so you see what needs you from
// another tab.
func (m *Model) windowTitle() string {
	if need, _ := m.needs(); need > 0 {
		return fmt.Sprintf("prowl · %d need you", need)
	}
	return "prowl"
}

// repoSummary is the summary line while one repo is in focus: where each of
// its open PRs stands, for draining a repo before a release.
func (m *Model) repoSummary(w int) string {
	var open, need, unclaimed, waiting, approved, drafts int
	for _, it := range m.items {
		if it.PR.Repository.NameWithOwner != m.repoFocus {
			continue
		}
		open++
		switch {
		case it.PR.IsDraft:
			drafts++
		case it.Bucket.NeedsMe():
			need++
		case it.Bucket == inbox.Unclaimed:
			unclaimed++
		case it.PR.ReviewDecision == "APPROVED":
			approved++
		case it.Bucket == inbox.WaitingOnAuthor || it.PR.ReviewDecision == "CHANGES_REQUESTED":
			waiting++
		}
	}
	parts := []string{repoLabel(m.repoFocus), fmt.Sprintf("%d open", open)}
	add := func(n int, label string, st lipgloss.Style) {
		if n > 0 {
			parts = append(parts, st.Render(fmt.Sprintf("%d %s", n, label)))
		}
	}
	add(need, "need you", sRed.Bold(true))
	add(unclaimed, "unclaimed", sOrange)
	add(waiting, "waiting on author", sDim)
	add(approved, "approved, not merged", sGreen)
	add(drafts, "draft", sDim)
	if !m.watching(m.repoFocus) {
		parts = append(parts, sDim.Render(leader+"w keeps watching it"))
	}
	return fit(" "+strings.Join(parts, sDim.Render(" · ")), w)
}

// watching is true when every open PR of the repo (owner/name) is fetched.
func (m *Model) watching(repo string) bool {
	return slices.ContainsFunc(m.cfg.WatchRepos, func(r string) bool { return strings.EqualFold(r, repo) })
}

// repoLabel is the focused repo (owner/name) as the header shows it: its
// name, colored like its rows.
func repoLabel(repo string) string {
	_, name, _ := strings.Cut(repo, "/")
	return repoColor(name).Bold(true).Render(shortRepo(name))
}

// summaryLine is the triage line above the inbox: what needs you, how long
// the oldest has waited, and trouble on your own PRs.
func (m *Model) summaryLine(w int) string {
	if m.repoFocus != "" {
		return m.repoSummary(w)
	}
	need, oldest := m.needs()
	var failing, conflicts int
	for i := range m.items {
		it := &m.items[i]
		if _, ok := m.snoozed[it.PR.Key()]; ok {
			continue
		}
		if m.viewer != "" && it.PR.AuthorLogin() == m.viewer && !it.PR.IsDraft {
			if s := it.PR.CIState(); s == "FAILURE" || s == "ERROR" {
				failing++
			}
			if it.PR.Mergeable == "CONFLICTING" {
				conflicts++
			}
		}
	}
	sep := sDim.Render(" · ")
	var parts []string
	if need == 0 {
		parts = append(parts, sGreen.Render("nothing needs you"))
	} else {
		parts = append(parts, sRed.Bold(true).Render(strconv.Itoa(need))+sDim.Render(" need you"))
		parts = append(parts, sDim.Render("oldest ")+m.waitStyle(*oldest).Render(ago(oldest.Since)))
	}
	if n, back := m.snoozedNeeds(); n > 0 {
		when := back.Format("15:04")
		if y, d := back.YearDay(), time.Now().YearDay(); y != d {
			when = back.Format("Mon 15:04")
		}
		parts = append(parts, sDim.Render(fmt.Sprintf("%d snoozed until %s", n, when)))
	}
	var mine []string
	if failing > 0 {
		mine = append(mine, sRed.Render(fmt.Sprintf("%d failing CI", failing)))
	}
	if conflicts > 0 {
		mine = append(mine, sYellow.Render(fmt.Sprintf("%d conflicting", conflicts)))
	}
	if len(mine) > 0 {
		parts = append(parts, sDim.Render("yours: ")+strings.Join(mine, sDim.Render(", ")))
	}
	return fit(" "+strings.Join(parts, sep), w)
}

func (m *Model) renderListRow(r listRow, w int, sel bool, cols listCols) string {
	byRepo := m.cfg.GroupBy == "repo"
	if r.header {
		folded := m.collapsed[groupID(r)]
		var s string
		if r.repo != "" {
			s = " " + foldGlyph(folded) + repoColor(r.repo).Bold(true).Render(shortRepo(r.repo)) + sDim.Render(fmt.Sprintf(" %d", r.count))
			if r.needsMe > 0 {
				s += sDim.Render(" · ") + sRed.Render(fmt.Sprintf("%d need you", r.needsMe))
			}
		} else {
			s = " " + foldGlyph(folded) + bucketStyle(r.bucket).Bold(true).Render(strings.ToUpper(r.bucket.String())) +
				sDim.Render(fmt.Sprintf(" %d", r.count))
		}
		s += " " + sBorder.Render(strings.Repeat("─", max(w-lipgloss.Width(s)-2, 0)))
		if sel {
			return highlight(s, w, sTitle)
		}
		return s
	}
	it := m.items[r.item]
	pr := it.PR
	ageS := m.waitStyle(it).Render(fmt.Sprintf("%4s", ago(it.Since)))

	cell := func(s string) string { return fit(s, 1) + "  " }
	snz, cl := " ", " "
	if _, ok := m.snoozed[pr.Key()]; ok {
		snz = sDim.Render("z")
	}
	if runs := m.review[pr.Key()]; len(runs) > 0 {
		cl = reviewGlyph(m, store.Current(runs))
	}
	badges := " "
	if cols.snooze {
		badges += cell(snz)
	}
	if cols.review {
		badges += cell(cl)
	}
	dot := reviewDot(it.ReviewStatus)
	if it.ChangedSinceApproval {
		dot = sGreen.Render("◐")
	}
	badges += cell(dot) + cell(ciBadge(pr.CIState(), m.cfg.NerdFont))

	n := prNumber(pr)
	numS := strings.Repeat(" ", max(cols.num-lipgloss.Width(n), 0))
	if pr.Local() {
		numS += sPink.Render(n)
	} else {
		numS += sDim.Render("#") + sBold.Render(n[1:])
	}
	title := styledTitle(pr.Title, sel)
	left := " " + ageS + " "
	if byRepo {
		left += fit(bucketStyle(it.Bucket).Render(bucketTags[it.Bucket]), 10) + numS + "  " + title
	} else {
		left += fit(repoColor(pr.Repository.Name).Render(squeeze(shortRepo(pr.Repository.Name), cols.repo)), cols.repo) + " " + numS + "  " + title
	}

	extra := ""
	if cols.threads > 0 {
		t := threadText(it)
		st := sYellow
		if it.UnresolvedCount == 0 {
			st = sGreen
		}
		extra += fmt.Sprintf("%*s", cols.threads-len(t), "") + st.Render(t)
	}
	if cols.size > 0 && w >= 70 {
		s := sizeText(pr.Additions, pr.Deletions)
		extra += "  " + fmt.Sprintf("%*s", cols.size-len(s), "") + sizeStyle(pr.Additions, pr.Deletions).Render(s)
	}
	bw := lipgloss.Width(badges) + lipgloss.Width(extra)
	row := fit(left, w-bw) + extra + badges
	if sel {
		st := sTitle
		if m.focus != fList {
			st = sDim
		}
		row = highlight(row, w, st)
	}
	return row
}

// reviewGlyph is the inbox icon for a PR's AI reviews: a spinner while any
// agent is reviewing, red when they all failed, green once posted.
func reviewGlyph(m *Model, cur []*store.AgentReview) string {
	icon := "C"
	if m.cfg.NerdFont {
		icon = "\U000f06a9"
	}
	failed, posted := 0, 0
	for _, r := range cur {
		switch r.Status {
		case "running":
			return spinner(m)
		case "failed":
			failed++
		case "posted":
			posted++
		}
	}
	switch {
	case failed == len(cur):
		return sRed.Render(icon)
	case unsentReplies(cur) > 0:
		return sYellow.Render(icon)
	case posted > 0:
		return sGreen.Render(icon)
	}
	return sBlue.Render(icon)
}

// unsentReplies counts the drafted thread replies of the current reviews
// that haven't been sent.
func unsentReplies(runs []*store.AgentReview) int {
	n := 0
	for _, r := range store.Current(runs) {
		n += r.Unsent()
	}
	return n
}

// ---- detail pane

// detailPane draws the PR pane. A click on a fold head (description,
// activity item, thread, file) toggles it; elsewhere a first click moves the
// cursor and a second on the same line acts on it: a finding is included or
// excluded, a diff line gets a comment.
func (m *Model) detailPane(l layout, x int) string {
	focused := m.focus == fDetail || !l.split
	d := m.current()
	w, h := l.detW, l.contentH
	if d == nil || m.waking {
		var lines []string
		if m.waking || m.loading && len(m.items) == 0 {
			lines = splash(m, w-2, h-2)
		} else {
			lines = []string{"", sDim.Render("  select a PR")}
		}
		return box(sDim.Render("[1]")+sTitle.Render(" "+appName+" "), "", lines, w, h, focused)
	}

	pr := d.item.PR
	pnum := sDim.Render("[1]")
	if m.focus == fDetail {
		pnum = sTitle.Render("[1]")
	}
	ref := " " + prName(pr) + " "
	title := pnum + sBold.Render(ref)
	tx := x + 2 + lipgloss.Width(pnum) + lipgloss.Width(ref)
	for i, name := range tabNames {
		label := fmt.Sprintf(" [%d] %s", i+2, name)
		switch {
		case i == tabDiff && d.sinceReview:
			label += " (since review)"
		case i == tabReview:
			n, running := 0, false
			for _, r := range store.Current(d.runs) {
				n += len(r.Findings)
				running = running || r.Status == "running"
			}
			var counts []string
			if n > 0 {
				counts = append(counts, strconv.Itoa(n))
			}
			if r := unsentReplies(d.runs); r > 0 {
				counts = append(counts, "↩"+strconv.Itoa(r))
			}
			if running {
				label += " " + spinner(m)
			} else if len(counts) > 0 {
				label += " (" + strings.Join(counts, " · ") + ")"
			}
		}
		label += " "
		sep := sBorder.Render("│")
		if i == d.tab {
			title += sep + sTitle.Render(label)
		} else {
			title += sep + sDim.Render(label)
		}
		t := i
		m.hits.add(tx+1, 0, lipgloss.Width(label), 1, func(m *Model) tea.Cmd {
			m.focus = fDetail
			return d.setTab(t, m.core)
		})
		tx += lipgloss.Width(label) + 1
	}
	title += " "

	lines := []string{}
	for _, hl := range d.header(m) {
		lines = append(lines, " "+hl)
	}
	lines = append(lines, "")
	for _, b := range d.body(m) {
		lines = append(lines, " "+b)
	}
	v := &d.navs[d.tab]
	bodyY := 1 + detHeaderH
	if d.tab == tabReview {
		bodyY += 2
	}
	m.hits.items = append(m.hits.items, hit{x: x + 1, y: bodyY, w: w - 2, h: v.h, click: func(m *Model, y int) tea.Cmd {
		m.focus = fDetail
		prev, at := v.cur, v.top+y-bodyY
		v.jump(at)
		rows := d.rows[d.tab]
		if v.cur != at || at >= len(rows) {
			return nil
		}

		switch r := rows[at]; {
		case r.head:
			d.foldAt("za")
		case prev != at:
		case d.tab == tabReview && r.finding >= 0:
			d.toggleIncluded(m)
		case d.tab == tabDiff && r.diff >= 0:
			return m.commentOnSelection(d, false)
		}
		return nil
	}})
	m.hits.addScroll(x, 0, w, h, v)
	hint := " + full width "
	if m.fullPR {
		hint = " + back to inbox "
	}
	pos := fmt.Sprintf(" %d/%d ", v.cur+1, max(v.n, 1))
	footer := sDim.Render(hint) + sDim.Render(pos)
	m.hits.add(x+w-2-lipgloss.Width(hint+pos), h-1, lipgloss.Width(hint), 1, func(m *Model) tea.Cmd {
		return m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
	})
	return box(title, footer, lines, w, h, focused)
}

// ---- statusline (lualine-ish)

func (m *Model) currentMode() mode {
	if m.popup != nil {
		return m.popup.mode()
	}
	return m.mode
}

func modeColor(md mode) string {
	switch md {
	case modeInsert:
		return pal.Green
	case modeVisual:
		return pal.Pink
	case modeSearch:
		return pal.Yellow
	case modeLeader:
		return pal.Orange
	case modeConfirm:
		return pal.Red
	}
	return pal.Accent
}

func (m *Model) statusline() string {
	md := m.currentMode()
	mc := lipgloss.Color(modeColor(md))
	surf := lipgloss.Color(pal.Surface)
	dark := lipgloss.Color(pal.Bg)
	fg := lipgloss.Color(pal.Fg)

	segA := lipgloss.NewStyle().Background(mc).Foreground(dark).Bold(true)
	segB := lipgloss.NewStyle().Background(surf).Foreground(fg)
	segC := lipgloss.NewStyle().Foreground(fg)

	sepR, sepL, thinR := "", "", "│"
	if m.cfg.NerdFont {
		sepR, sepL, thinR = "", "", ""
	}
	trans := func(from, to lipgloss.Color, glyph string, toTransparent bool) string {
		if glyph == "" {
			return ""
		}
		st := lipgloss.NewStyle().Foreground(from)
		if !toTransparent {
			st = st.Background(to)
		}
		return st.Render(glyph)
	}

	user := m.viewer
	if user == "" {
		user = "…"
	}
	gitIcon := ""
	if m.cfg.NerdFont {
		gitIcon = " "
	}
	ctx := "inbox"
	if it := m.selectedItem(); it != nil {
		ctx = prName(it.PR)
		if d := m.details[it.PR.Key()]; d != nil && m.focus == fDetail {
			ctx += " " + sDim.Render(thinR) + " " + strings.ToLower(tabNames[d.tab])
			if n := len(d.pending); n > 0 {
				ctx += sYellow.Render(fmt.Sprintf(" ◆%d", n))
			}
		}
	}

	owl := ""
	if m.cfg.Playful && len(m.items) > 0 {
		face, color := m.statusOwl()
		owl = lipgloss.NewStyle().Background(surf).Foreground(lipgloss.Color(color)).Bold(true).Render(" "+face+" ") +
			trans(surf, mc, sepR, false)
		m.hits.add(0, m.h-1, lipgloss.Width(owl), 1, func(m *Model) tea.Cmd {
			m.focus = fList
			m.setFilter(fNeedsMe)
			return m.schedulePreview()
		})
	}
	left := owl + segA.Render(" "+md.String()+" ") + trans(mc, surf, sepR, false) +
		segB.Render(" "+gitIcon+user+" ") + trans(surf, dark, sepR, true) +
		segC.Render(" "+ctx)

	var sync string
	switch {
	case m.loading:
		sync = spinner(m) + " syncing"
	case m.syncErr != nil:
		sync = sRed.Render("sync failed")
	case !m.lastSync.IsZero():
		sync = sDim.Render("synced " + agoText(m.lastSync))
	}
	switch running := m.runningReviews(); {
	case running == 1:
		for _, runs := range m.review {
			for _, r := range store.Current(runs) {
				if r.Status == "running" {
					sync = sYellow.Render(core.ReviewerTitle(r.Reviewer())+" reviewing") + " " + sDim.Render(thinR) + " " + sync
				}
			}
		}
	case running > 1:
		sync = sYellow.Render(fmt.Sprintf("reviewing ×%d", running)) + " " + sDim.Render(thinR) + " " + sync
	}
	if step := m.core.Updating(); step != "" {
		sync = updatingMark(m) + " " + sYellow.Render(step) + " " + sDim.Render(thinR) + " " + sync
	}
	syncW := lipgloss.Width(sync)
	authW := 0
	if w := m.authWarning(); w != "" {
		sync = w + " " + sDim.Render(thinR) + " " + sync
		authW = lipgloss.Width(w)
	}
	if sc := m.showcmd(); sc != "" {
		sync = sBold.Render(sc) + "  " + sync
	}
	v := m.activeNav()
	pos := trans(surf, dark, sepL, true) +
		segB.Render(" "+v.percent()+" ") + trans(mc, surf, sepL, false) +
		segA.Render(fmt.Sprintf(" %d:%d ", v.cur+1, max(v.n, 1)))

	menu := sDim.Render(leader + " menu")
	menuW := lipgloss.Width(menu) + 2
	menu += "  "
	for i, right := range []string{segC.Render(menu+sync+" ") + pos, segC.Render(menu+strings.TrimSpace(m.authWarning()+" "+m.showcmd())+" ") + pos, segC.Render(menu)} {
		if gap := m.w - lipgloss.Width(left) - lipgloss.Width(right); gap >= 1 {
			x := m.w - lipgloss.Width(right)
			m.hits.add(x, m.h-1, menuW-2, 1, func(m *Model) tea.Cmd {
				m.mode = modeLeader
				return nil
			})
			if i < 2 {
				if authW > 0 {
					m.hits.add(x+menuW, m.h-1, authW, 1, func(m *Model) tea.Cmd {
						m.popup = &accountsPopup{loading: true}
						return m.checkAuth()
					})
				}
				if i == 0 {
					m.hits.add(x+menuW+lipgloss.Width(sync)-syncW, m.h-1, syncW, 1, func(m *Model) tea.Cmd { return m.refresh() })
				}
			}
			return left + strings.Repeat(" ", gap) + right
		}
	}
	return trunc(left, m.w)
}

// cmdline is the bottom row's text when prowl needs it (search input, a
// prompt, a message), or "" to show the statusline.
func (m *Model) cmdline() string {
	var left string
	switch {
	case m.mode == modeSearch:
		left = m.searchInput.View()
	case m.msg != "" && time.Since(m.msgAt) < 6*time.Second:
		if m.msgErr {
			left = sErr.Render(m.msg)
		} else {
			left = m.msg
		}
	case m.mode == modeVisual:
		left = sBold.Render("-- VISUAL LINE --") + sDim.Render("  c comment on selection · esc cancel")
	}
	if left == "" {
		return ""
	}
	right := sDim.Render(m.showcmd())
	return fit(left, m.w-lipgloss.Width(right)-1) + " " + right
}

// showcmd is a partly typed key sequence, like vim's 'showcmd'.
func (m *Model) showcmd() string {
	if m.mode == modeLeader {
		return leader + m.leaderPrefix
	}
	return m.pending
}

// ---- helpers: highlight, overlay, skeleton

// highlight paints a full-width selection background behind an already styled
// line, re-applying the background after every SGR reset.
func highlight(s string, w int, marker lipgloss.Style) string {
	s = fit(s, w)
	probe := sSelRow.Render("\x00")
	pre, post, ok := strings.Cut(probe, "\x00")
	if !ok || pre == "" {
		return marker.Render("▌") + ansi.Cut(s, 1, w)
	}
	s = strings.ReplaceAll(s, "\x1b[0m", "\x1b[0m"+pre)
	s = strings.ReplaceAll(s, "\x1b[m", "\x1b[m"+pre)
	return marker.Render("▌") + pre + ansi.Cut(s, 1, w) + post
}

// place is where overlay puts a block: centred, or above the statusline at
// the bottom right.
func place(top string, w, h int, where string) (int, int) {
	tw, th := lipgloss.Width(top), lipgloss.Height(top)
	x, y := (w-tw)/2, (h-th)/2
	if where == "bottom-right" {
		x, y = w-tw-1, h-1-th
	}
	return max(x, 0), max(y, 0)
}

// confirmView draws a pending [y/N] question as a popup over the screen,
// with clickable buttons, so a confirmation is hard to miss.
func (m *Model) confirmView(screen string) string {
	q := strings.TrimSpace(strings.TrimSuffix(m.confirm.prompt, "[y/N]"))
	yes, no := sGreen.Bold(true).Render("[ y  yes ]"), sDim.Render("[ n  no ]")
	w := min(max(lipgloss.Width(q)+6, 40), popWidth(m))
	body := strings.Join(wrap(q, w-4), "\n") + "\n\n" + yes + "   " + no
	box := popBox("Confirm", body, w, pal.Orange)
	x, y := place(box, m.w, m.h, "center")
	by := y + lipgloss.Height(box) - 2
	for _, b := range []struct {
		label, key string
		dx         int
	}{{yes, "y", 2}, {no, "n", 2 + lipgloss.Width(yes) + 3}} {
		key := b.key
		m.hits.items = append(m.hits.items, hit{x: x + b.dx, y: by, w: lipgloss.Width(b.label), h: 1, modal: true,
			click: func(m *Model, _ int) tea.Cmd { return m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}) }})
	}
	return overlay(screen, box, m.w, m.h, "center")
}

// overlay composites a block over the base screen.
func overlay(base, top string, w, h int, where string) string {
	bl := strings.Split(base, "\n")
	tl := strings.Split(top, "\n")
	x, y := place(top, w, h, where)
	for i, l := range tl {
		row := y + i
		if row >= len(bl) {
			break
		}
		b := bl[row]
		leftPart := ansi.Cut(b, 0, x)
		if lipgloss.Width(leftPart) < x {
			leftPart += strings.Repeat(" ", x-lipgloss.Width(leftPart))
		}
		rightPart := ansi.Cut(b, x+lipgloss.Width(l), w)
		bl[row] = leftPart + "\x1b[0m" + l + "\x1b[0m" + rightPart
	}
	return strings.Join(bl, "\n")
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinner(m *Model) string {
	return sTitle.Render(spinFrames[m.frame%len(spinFrames)])
}

// updatingMark leads the statusline while prowl updates itself: the owl
// watching and blinking now and then, as in `prowl update`, or the spinner.
func updatingMark(m *Model) string {
	if !m.cfg.Playful {
		return spinner(m)
	}
	if m.frame%30 < 2 {
		return sTitle.Render("(-,-)")
	}
	return sTitle.Render("(o,o)")
}

// skeletonList draws placeholder rows with a shimmer while the first sync runs.
func skeletonList(m *Model, w, h int) []string {
	lines := []string{""}
	widths := []int{62, 48, 70, 55, 66, 40, 58, 72, 50, 64, 45, 60}
	for i := 0; i < h-1 && i < 14; i++ {
		if i == 0 || i == 6 {
			lines = append(lines, " "+shimmer(m, strings.Repeat("▀", 12), 1, i))
			continue
		}
		n := max(w*widths[i%len(widths)]/100, 8)
		lines = append(lines, "  "+shimmer(m, strings.Repeat("▀", 4), 2, i)+" "+shimmer(m, strings.Repeat("▀", n), 7, i))
	}
	return lines
}

func skeleton(m *Model, w, h int) []string {
	lines := []string{}
	widths := []int{30, 90, 85, 70, 0, 25, 80, 60, 95, 40, 0, 35, 75, 65, 88, 50}
	for i := 0; i < h && i < len(widths); i++ {
		if widths[i] == 0 {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, shimmer(m, strings.Repeat("▀", max(w*widths[i]/100, 4)), 0, i))
	}
	return lines
}

// shimmer renders s with a highlight band sweeping left to right.
func shimmer(m *Model, s string, offset, row int) string {
	runes := []rune(s)
	pos := (m.frame*3 - row*2) % 120
	var b strings.Builder
	for i, r := range runes {
		d := pos - (i + offset)
		if d >= 0 && d < 8 {
			b.WriteString(sSkelHi.Render(string(r)))
		} else {
			b.WriteString(sSkel.Render(string(r)))
		}
	}
	return b.String()
}

func splash(m *Model, w, h int) []string {
	logo := bigName()
	var art []string
	small := h < 20
	if m.cfg.Playful && h >= 14 {
		art = append(wakingOwl(int(time.Since(m.started)/(360*time.Millisecond)), small), "")
	}
	var lines []string
	for i := 0; i < (h-len(logo)-len(art)-4)/2; i++ {
		lines = append(lines, "")
	}
	for _, l := range art {
		lines = append(lines, centerBlock(l, w, owlWidth(small)))
	}
	for _, l := range logo {
		lines = append(lines, center(sTitle.Render(l), w))
	}
	lines = append(lines, "")
	dots := strings.Repeat("·", m.frame%4) + strings.Repeat(" ", 3-m.frame%4)
	from := "GitHub"
	if len(m.cfg.Orgs) > 0 {
		from = strings.Join(m.cfg.Orgs, ", ")
	}
	lines = append(lines, center(sDim.Render("fetching your pull requests from "+from+" "+dots), w))
	return lines
}

// centerBlock centres a line of a block that is bw wide, so a drawing's
// lines stay aligned with each other.
func centerBlock(s string, w, bw int) string {
	return strings.Repeat(" ", max((w-bw)/2, 0)) + s
}

// emptyInbox is what the inbox shows with no rows: the owl asleep, or a
// line of text with playful off.
func (m *Model) emptyInbox(w, h int) []string {
	msg, sub := "nothing here", ""
	switch {
	case m.repoFocus != "" || m.typeFocus != "":
		msg, sub = "nothing here", "esc shows everything"
	case len(m.items) == 0:
		msg, sub = "no open pull requests", "prowl is resting until there are some"
	case m.filter == fNeedsMe:
		msg, sub = "nothing needs you", "prowl is resting until something does"
	}
	if !m.cfg.Playful || h < 11 {
		return []string{"", sDim.Render("  " + msg), sDim.Render("  " + sub)}
	}
	small := h < 16
	art := sleepingOwl(m.frame, small)
	var out []string
	for i := 0; i < (h-len(art)-3)/2; i++ {
		out = append(out, "")
	}
	for _, l := range art {
		out = append(out, centerBlock(l, w, owlWidth(small)))
	}
	return append(out, "", center(lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Fg)).Render(msg), w), center(sDim.Render(sub), w))
}

func center(s string, w int) string {
	pad := (w - lipgloss.Width(s)) / 2
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + s
}

// ---- which-key

// wkCell is a clickable entry in the leader menu, relative to its content.
type wkCell struct {
	x, y, w int
	key     string
}

// wkBlock is one category column of the leader menu.
type wkBlock struct {
	lines, keys []string
	w           int
}

// whichKey draws the leader menu: one column per category, wrapping onto
// more rows of columns when the terminal is narrow.
func (m *Model) whichKey() string {
	keys, title := leaderKeys, leader
	switch m.leaderPrefix {
	case "t":
		keys, title = threadKeys, leader+"t  threads"
	case "c":
		keys, title = agentKeys, leader+"c  AI review"
	case "f":
		keys, title = findKeys, leader+"f  find & focus"
	}
	var blocks []*wkBlock
	byCat := map[string]*wkBlock{}
	for _, k := range keys {
		b := byCat[k.cat]
		if b == nil {
			b = &wkBlock{lines: []string{sSection.Render(k.cat)}, keys: []string{""}}
			byCat[k.cat] = b
			blocks = append(blocks, b)
		}
		desc := sDim.Render(k.desc)
		switch {
		case k.group:
			desc = sBlue.Render("+" + k.desc)
		case k.write:
			desc = sPink.Render(k.desc)
		}
		b.lines = append(b.lines, sOrange.Bold(true).Render(fmt.Sprintf("%-2s", k.key))+" "+desc)
		b.keys = append(b.keys, k.key)
	}
	for _, b := range blocks {
		for _, l := range b.lines {
			b.w = max(b.w, lipgloss.Width(l))
		}
	}
	const gap = 4
	maxW := max(m.w-8, 30)
	rows := []string{sTitle.Render(title), ""}
	m.wkCells = m.wkCells[:0]
	for i := 0; i < len(blocks); {
		j, width := i, 0
		for j < len(blocks) && (j == i || width+gap+blocks[j].w <= maxW) {
			width += blocks[j].w + gap
			j++
		}
		band := blocks[i:j]
		h := 0
		for _, b := range band {
			h = max(h, len(b.lines))
		}
		for line := 0; line < h; line++ {
			var r strings.Builder
			x := 0
			for _, b := range band {
				cell := ""
				if line < len(b.lines) {
					cell = b.lines[line]
					if b.keys[line] != "" {
						m.wkCells = append(m.wkCells, wkCell{x: x, y: len(rows), w: b.w, key: b.keys[line]})
					}
				}
				r.WriteString(fit(cell, b.w+gap))
				x += b.w + gap
			}
			rows = append(rows, strings.TrimRight(r.String(), " "))
		}
		rows = append(rows, "")
		i = j
	}
	rows = append(rows, sPink.Render("pink")+sDim.Render(" writes to GitHub · ")+sBlue.Render("+more")+sDim.Render(" opens a menu · esc cancel"))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(pal.Orange)).
		Padding(0, 1).Render(strings.Join(rows, "\n"))
}

// ---- mouse

type hit struct {
	x, y, w, h int
	modal      bool // a confirm button: the only clicks taken while one is open
	click      func(m *Model, y int) tea.Cmd
	scroll     *nav
}

type hitMap struct{ items []hit }

func (hm *hitMap) add(x, y, w, h int, f func(m *Model) tea.Cmd) {
	hm.items = append(hm.items, hit{x: x, y: y, w: w, h: h, click: func(m *Model, _ int) tea.Cmd { return f(m) }})
}

func (hm *hitMap) addScroll(x, y, w, h int, v *nav) {
	hm.items = append(hm.items, hit{x: x, y: y, w: w, h: h, scroll: v})
}

func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	if !m.cfg.Mouse {
		return nil
	}
	if m.popup != nil {
		return m.popupMouse(msg)
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		d := 3
		if msg.Button == tea.MouseButtonWheelUp {
			d = -3
		}
		for _, h := range m.hits.items {
			if h.scroll != nil && inside(h, msg.X, msg.Y) {
				h.scroll.scrollView(d)
				if h.scroll == &m.list {
					return m.schedulePreview()
				}
				return nil
			}
		}
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return nil
		}
		for i := len(m.hits.items) - 1; i >= 0; i-- {
			h := m.hits.items[i]
			if m.confirm != nil && !h.modal {
				continue
			}
			if h.click != nil && inside(h, msg.X, msg.Y) {
				return h.click(m, msg.Y)
			}
		}
	}
	return nil
}

// rowPopup is a popup showing a list. Its rows start at line first of the
// popup (counting the border), top is the first row drawn, n is how many
// rows there are. Clicking a row selects it; clicking it again chooses it,
// as enter does, so a double-click works too. The wheel moves the selection.
type rowPopup interface {
	rows(m *Model) (first, top, n int)
	cursor() *int
}

// clickPopup handles clicks itself, at x, y from its top-left corner.
type clickPopup interface {
	click(m *Model, x, y int) tea.Cmd
	wheel(m *Model, d int)
}

func (m *Model) popupMouse(msg tea.MouseMsg) tea.Cmd {
	x, y := msg.X-m.popX, msg.Y-m.popY
	in := x >= 0 && y >= 0 && x < m.popW && y < m.popH
	if p, ok := m.popup.(*chatPopup); ok {
		p.mouse(msg, x-1, y-1, in)
		return nil
	}
	d := 0
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		d = -1
	case msg.Button == tea.MouseButtonWheelDown:
		d = 1
	case msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress:
		return nil
	}
	if !in {
		if _, typing := m.popup.(*textPopup); d == 0 && !typing {
			m.popup = nil
		}
		return nil
	}
	switch p := m.popup.(type) {
	case clickPopup:
		if d != 0 {
			p.wheel(m, d)
			return nil
		}
		return p.click(m, x, y)
	case rowPopup:
		first, top, n := p.rows(m)
		cur := p.cursor()
		if n == 0 {
			return nil
		}
		if d != 0 {
			*cur = min(max(*cur+d, 0), n-1)
			return nil
		}
		i := top + y - first
		if i < 0 || i >= n || i >= top+m.popH {
			return nil
		}
		if *cur == i {
			return m.popup.update(m, tea.KeyMsg{Type: tea.KeyEnter})
		}
		*cur = i
	}
	return nil
}

func inside(h hit, x, y int) bool {
	return x >= h.x && x < h.x+h.w && y >= h.y && y < h.y+h.h
}

// prNumber is how a row names its PR: #855, or ⌂ for a local review.
func prNumber(pr gh.PR) string {
	if pr.Local() {
		return "⌂"
	}
	return "#" + strconv.Itoa(pr.Number)
}

// prName names a PR for people: api#855, or api ⌂ feat/x for a local review.
func prName(pr gh.PR) string {
	if pr.Local() {
		return shortRepo(pr.Repository.Name) + " ⌂ " + pr.HeadRefName
	}
	return shortRepo(pr.Repository.Name) + prNumber(pr)
}
