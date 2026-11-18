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
	"github.com/tmstorm/prowl/internal/platform"
)

const (
	tabOverview = iota
	tabDiff
	tabReview
)

var tabNames = []string{"Overview", "Diff", "AI review"}

// botLogins are the bots setting: accounts dimmed and folded in activity.
var botLogins map[string]bool

// reviewerOrder is the configured reviewers, the order agents' sections are
// shown in.
var reviewerOrder = []string{"claude"}

// brow is one rendered row of a detail tab.
type brow struct {
	text    string
	plain   string
	diff    int    // index into the diff lines (rendered lazily), -1 otherwise
	file    string // diff: file the row belongs to
	block   string // fold block id
	head    bool   // first row of its block
	folded  bool   // head row: the block is currently folded
	off     int    // row offset inside its block
	fresh   bool   // first row of an item that is new to you
	thread  string // review thread id
	finding int    // index into detail.flat (a current review's finding), -1 otherwise
	agent   string // AI review tab: the agent whose section the row is in
	reply   string // AI review tab: the thread a draft reply answers
	pair    int    // split diff: the added line shown beside a removed one, -1 none
	doc     string // the docs topic of the part the row is in, when it has its own
}

// splitDiff shows the diff side by side: old on the left, new on the right.
var splitDiff bool

// commentLine is the line a comment on the row anchors to: in the split
// diff the new side when the row has one, else the old.
func commentLine(r brow, lines []diffLine) diffLine {
	if r.pair >= 0 && splitDiff {
		return lines[r.pair]
	}
	return lines[r.diff]
}

// findingRef points at one finding of a review run.
type findingRef struct {
	runID int64
	i     int
}

type detail struct {
	item core.PR
	me   string
	tab  int
	w, h int // text width and body height

	det        *gh.Detail
	detErr     error
	loadingDet bool
	stale      bool // det is older than what GitHub has; refetch when shown

	seenAt     time.Time // when this PR was last looked at before this session
	seenLoaded bool

	sinceReview bool
	diffs       map[bool][]diffLine
	diffLoading bool
	diffErr     error
	pending     []core.DraftComment
	viewed      map[string]string // path -> VIEWED / UNVIEWED / DISMISSED
	hl          map[bool]*highlighter

	folds   map[string]bool // explicit fold state per block
	foldAll *bool           // zR / zM override

	runs        []*core.Review // every agent's review runs, newest first
	summary     *core.Summary  // the AI summary, if one was written
	summarizing bool           // a summary is being written
	flat        []findingRef   // the current reviews' findings, as rows refer to them
	reviewSig   string
	logs        map[string][]core.Note // per agent: your instructions and its replies (local only)
	ciLogs      map[int64]ciLog        // failed Actions jobs' logs, as gl loaded them

	navs    [3]nav
	rows    [3][]brow
	dirty   bool
	jumpNew bool // put the cursor on the newest unread activity on first view
}

type detailMsg struct {
	key string
	det *gh.Detail
	err error
}

type diffMsg struct {
	key         string
	sinceReview bool
	raw         string
	err         error
}

func newDetail(it core.PR, me string) *detail {
	d := &detail{item: it, me: me, diffs: map[bool][]diffLine{}, dirty: true, hl: map[bool]*highlighter{},
		folds: map[string]bool{}, viewed: map[string]string{}}
	d.sinceReview = it.LastReviewedOid != "" && it.LastReviewedOid != it.HeadOid
	d.jumpNew = true
	for i := range d.navs {
		d.navs[i].scrolloff = 4
	}
	return d
}

func (d *detail) setItem(it core.PR) {
	if it.HeadOid != d.item.HeadOid {
		d.diffs = map[bool][]diffLine{}
		d.hl = map[bool]*highlighter{}
		d.stale = true
	}
	if !it.UpdatedAt.Equal(d.item.UpdatedAt) {
		d.stale = true
	}
	d.item = it
	d.dirty = true
}

// setRuns updates the review runs, rebuilding rows only when something
// visible in them changed (the live status line renders separately).
func (d *detail) setRuns(runs []*core.Review) {
	var sig strings.Builder
	for _, r := range runs {
		fmt.Fprintf(&sig, "#%d|%s|%s|%s|%d|%s", r.ID, r.Status, r.SessionID, r.Error, r.FinishedAt.Unix(), r.Summary)
		for _, f := range r.Findings {
			fmt.Fprintf(&sig, "|%s:%d:%t:%t:%s", f.Path, f.Line, f.Excluded, f.Posted, f.Title)
		}
	}
	d.runs = runs
	if sig.String() != d.reviewSig {
		d.reviewSig = sig.String()
		d.dirty = true
	}
}

func (d *detail) setSummary(sum *core.Summary, running bool) {
	if sum != d.summary || running != d.summarizing {
		d.summary, d.summarizing = sum, running
		d.dirty = true
	}
}

func (d *detail) setLogs(logs map[string][]core.Note) {
	changed := len(logs) != len(d.logs)
	for a, l := range logs {
		if len(l) != len(d.logs[a]) {
			changed = true
		}
	}
	if changed {
		d.logs = logs
		d.dirty = true
	}
}

// current is each agent's latest run, in reviewer order, then any other
// agent that has reviewed the PR.
func (d *detail) current() []*core.Review {
	cur := core.Current(d.runs)
	var out []*core.Review
	for _, name := range reviewerOrder {
		if r := core.Latest(cur, name); r != nil {
			out = append(out, r)
		}
	}
	for _, r := range cur {
		if !slices.Contains(reviewerOrder, r.Reviewer()) {
			out = append(out, r)
		}
	}
	return out
}

func (d *detail) resize(w, h int) {
	if w != d.w || h != d.h {
		d.w, d.h = w, h
		d.dirty = true
	}
}

// nav returns the cursor for the current tab, rebuilding rows if needed.
func (d *detail) nav() *nav {
	d.build()
	return &d.navs[d.tab]
}

func (d *detail) bodyH(tab int) int {
	if tab == tabReview {
		return max(d.h-2, 1)
	}
	return max(d.h, 1)
}

// rowID identifies a row across rebuilds so the cursor stays on the same
// content when rows above it change or a background refresh rebuilds them.
func rowID(r brow) string {
	switch {
	case r.diff >= 0:
		return "d" + strconv.Itoa(r.diff)
	case r.block != "":
		return "b" + r.block + "#" + strconv.Itoa(r.off)
	case r.plain != "":
		return "p" + r.plain
	}
	return ""
}

// cursorAnchor remembers the cursor's row so a rebuild can put it back.
type cursorAnchor struct {
	id, block string
	cur       int
	ok        bool
}

func (d *detail) build() {
	if !d.dirty {
		return
	}
	d.dirty = false
	var anchors [3]cursorAnchor
	for t := range d.rows {
		if c := d.navs[t].cur; c < len(d.rows[t]) {
			r := d.rows[t][c]
			anchors[t] = cursorAnchor{id: rowID(r), block: r.block, cur: c, ok: true}
			if r.block == "" && r.file != "" {
				anchors[t].block = "file:" + r.file
			}
		}
	}
	d.rows[tabOverview] = d.buildOverview()
	d.rows[tabDiff] = d.buildDiff()
	d.rows[tabReview] = d.buildReview()
	for t := range d.navs {
		v := &d.navs[t]
		a := anchors[t]
		if a.ok {
			v.cur = restoreCursor(d.rows[t], a.id, a.block, a.cur)
		}
		v.setLen(len(d.rows[t]), d.bodyH(t))
	}
	if d.jumpNew && d.det != nil {
		d.jumpNew = false
		for i, r := range d.rows[tabOverview] {
			if r.fresh {
				v := &d.navs[tabOverview]
				v.cur = i
				v.top = max(i-3, 0)
				v.clampTop()
				break
			}
		}
	}
}

// restoreCursor finds where the anchored row went: the same row (nearest
// match to where it was), else the head of its block if the block folded,
// else the same index.
func restoreCursor(rows []brow, id, block string, old int) int {
	best, bestDist := -1, 0
	if id != "" {
		for i, r := range rows {
			if rowID(r) != id {
				continue
			}
			dist := i - old
			if dist < 0 {
				dist = -dist
			}
			if best < 0 || dist < bestDist {
				best, bestDist = i, dist
			}
		}
	}
	if best >= 0 {
		return best
	}
	if block != "" {
		for i, r := range rows {
			if r.head && r.block == block {
				return i
			}
		}
	}
	return old
}

func (d *detail) ensureLoaded(co *core.Core) tea.Cmd {
	var cmds []tea.Cmd
	if (d.det == nil || d.stale) && !d.loadingDet {
		cmds = append(cmds, d.fetch(co))
	}
	if d.tab == tabDiff {
		cmds = append(cmds, d.loadDiff(co))
	}
	return tea.Batch(cmds...)
}

func (d *detail) fetch(co *core.Core) tea.Cmd {
	d.loadingDet = true
	d.stale = false
	key := d.item.Key
	return func() tea.Msg {
		det, err := co.Detail(key)
		return detailMsg{key, det, err}
	}
}

func (d *detail) setTab(t int, co *core.Core) tea.Cmd {
	d.tab = t
	return d.ensureLoaded(co)
}

func (d *detail) loadDiff(co *core.Core) tea.Cmd {
	if _, ok := d.diffs[d.sinceReview]; ok || d.diffLoading {
		return nil
	}
	key, since := d.item.Key, d.sinceReview
	d.diffLoading = true
	d.diffErr = nil
	return func() tea.Msg {
		raw, err := co.Diff(key, since)
		return diffMsg{key, since, raw, err}
	}
}

func (d *detail) handleDetail(msg detailMsg) {
	d.loadingDet = false
	if msg.err != nil && d.det != nil {
		return
	}
	d.det, d.detErr = msg.det, msg.err
	if msg.det != nil {
		d.item.Body = msg.det.Body
		for _, f := range msg.det.Files {
			d.viewed[f.Path] = f.ViewerViewedState
		}
	}
	d.dirty = true
}

func (d *detail) handleDiff(msg diffMsg) {
	d.diffLoading = false
	d.dirty = true
	if msg.err != nil {
		d.diffErr = msg.err
		if msg.sinceReview {
			d.diffErr = fmt.Errorf("can't diff since your last review, probably a force-push (%v). Press D for the full diff", msg.err)
		}
		return
	}
	d.diffs[msg.sinceReview] = parseDiff(msg.raw)
	delete(d.hl, msg.sinceReview)
}

// highlighter returns the syntax highlight cache for the current diff.
func (d *detail) highlighter() *highlighter {
	h := d.hl[d.sinceReview]
	if h == nil || h.theme != themeName {
		h = &highlighter{}
		h.reset(themeName)
		d.hl[d.sinceReview] = h
	}
	return h
}

func (d *detail) searchLines() []string {
	d.build()
	rows := d.rows[d.tab]
	lines := d.diffs[d.sinceReview]
	out := make([]string, len(rows))
	for i, r := range rows {
		if r.diff >= 0 && r.diff < len(lines) {
			out[i] = lines[r.diff].text
		} else {
			out[i] = r.plain
		}
	}
	return out
}

// ---- folds

func (d *detail) folded(id string, def bool) bool {
	if v, ok := d.folds[id]; ok {
		return v
	}
	if d.foldAll != nil {
		return *d.foldAll
	}
	return def
}

func (d *detail) fileFolded(path string) bool {
	return d.folded("file:"+path, d.viewed[path] == "VIEWED")
}

// read reports whether something created at t has already been seen.
func (d *detail) read(t time.Time, author *gh.Actor) bool {
	if author != nil && author.Login == d.me {
		return true
	}
	seen := d.seen()
	return !seen.IsZero() && !t.After(seen)
}

// seen is when you last caught up on the PR: your last visit or your last
// review, whichever is later.
func (d *detail) seen() time.Time {
	if r := d.item.MyLastReview; r != nil && r.SubmittedAt.After(d.seenAt) {
		return r.SubmittedAt
	}
	return d.seenAt
}

func (d *detail) isFolded(id string) bool {
	if path, ok := strings.CutPrefix(id, "file:"); ok {
		return d.fileFolded(path)
	}
	for _, x := range d.rows[d.tab] {
		if x.block == id && x.head {
			return x.folded
		}
	}
	return false
}

// foldAt applies a vim fold command (za zo zc) to the block under the cursor.
// In the diff, a line outside any thread folds its whole file.
func (d *detail) foldAt(op string) {
	rows := d.rows[d.tab]
	v := &d.navs[d.tab]
	if v.cur >= len(rows) {
		return
	}
	r := rows[v.cur]
	id := r.block
	if id == "" && r.file != "" {
		id = "file:" + r.file
	}
	if id == "" {
		return
	}
	switch op {
	case "za":
		d.folds[id] = !d.isFolded(id)
	case "zo":
		d.folds[id] = false
	case "zc":
		d.folds[id] = true
	}
	d.dirty = true
}

func (d *detail) foldEverything(fold bool) {
	d.folds = map[string]bool{}
	d.foldAll = &fold
	d.dirty = true
}

// ---- row builders

func plainRow(s string) brow {
	return brow{text: s, plain: ansi.Strip(s), finding: -1, diff: -1, pair: -1}
}

type rowAdder struct {
	rows   []brow
	block  string
	off    int
	head   bool
	folded bool
	thread string
	doc    string
}

func (a *rowAdder) add(s string) {
	r := plainRow(s)
	r.block, r.thread, r.doc = a.block, a.thread, a.doc
	if a.block != "" {
		r.off = a.off
		a.off++
	}
	if a.head {
		r.head = true
		r.folded = a.folded
		a.head = false
	}
	a.rows = append(a.rows, r)
}

// begin starts a foldable block; folded is its current state.
func (a *rowAdder) begin(block, thread string, folded bool) {
	a.block, a.thread, a.head, a.off, a.folded = block, thread, block != "", 0, folded
}

func (a *rowAdder) end() { a.block, a.thread, a.head = "", "", false }

func author(a *gh.Actor) string {
	if a == nil {
		return "ghost"
	}
	return a.Login
}

func firstLine(s string, w int) string {
	s = strings.TrimSpace(htmlComment.ReplaceAllString(s, ""))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return trunc(s, w)
}

func foldGlyph(folded bool) string {
	if folded {
		return sDim.Render("▸ ")
	}
	return sTitle.Render("▾ ")
}

func (d *detail) newMark(t time.Time, a *gh.Actor) string {
	if d.read(t, a) {
		return ""
	}
	return sYellow.Render(" ● new")
}

// summaryRows is the AI summary at the top of the Overview, or a hint on how
// to get one.
func (d *detail) summaryRows(a *rowAdder, w int) {
	switch {
	case d.summarizing:
		a.add(" " + sSection.Render("AI summary") + sDim.Render(" · writing…"))
	case d.summary == nil:
		a.add(" " + sDim.Render(leader+"ca writes an AI summary of this PR"))
	default:
		folded := d.folded("summary", false)
		a.begin("summary", "", folded)
		head := " " + foldGlyph(folded) + sSection.Render("AI summary") + sDim.Render(" · "+modelName(d.summary.Model)+" · for "+gh.Short(d.summary.HeadOid))
		if d.summary.HeadOid != d.item.HeadOid {
			head += sYellow.Render(" · new commits since, " + leader + "ca to refresh")
		}
		a.add(head)
		if !folded {
			for _, l := range renderMarkdown(d.summary.Text, w-4) {
				a.add("  " + l)
			}
		}
		a.end()
	}
	a.add("")
}

func (d *detail) buildOverview() []brow {
	a := &rowAdder{}
	w := d.w
	section := func(s string) {
		a.end()
		if len(a.rows) > 0 {
			a.add("")
		}
		a.add(sSection.Render(s))
	}
	if d.detErr != nil {
		a.add(sErr.Render(d.detErr.Error()))
	}
	if d.det == nil {
		return a.rows
	}
	a.doc = "AI summary"
	d.summaryRows(a, w)
	a.doc = ""
	if len(d.pending) > 0 {
		a.doc = "Inline comments and suggestions"
		section(fmt.Sprintf("Pending comments (%d) · sent with your next review", len(d.pending)))
		for _, p := range d.pending {
			loc := fmt.Sprintf("%s:%d", p.Path, p.Line)
			if p.StartLine > 0 {
				loc = fmt.Sprintf("%s:%d-%d", p.Path, p.StartLine, p.Line)
			}
			a.add("  " + sYellow.Render("◆ "+loc))
			for _, l := range renderMarkdown(p.Body, w-4) {
				a.add("    " + l)
			}
		}
		a.doc = ""
	}

	a.end()
	local := d.item.Local()
	descFolded := d.folded("desc", !d.seenAt.IsZero() && !local)
	a.begin("desc", "", descFolded)
	title := "Description"
	if local {
		title = "Branch"
	}
	a.add(" " + foldGlyph(descFolded) + sSection.Render(title))
	if !descFolded {
		body := renderMarkdown(d.det.Body, w-2)
		if len(body) == 0 {
			a.add(sDim.Render("  (no description)"))
		}
		for _, l := range body {
			a.add("  " + l)
		}
	}

	if local {
		a.end()
		return a.rows
	}
	section("Reviewers")
	latest := map[string]gh.Review{}
	var order []string
	for _, r := range d.det.Reviews {
		n := author(r.Author)
		prev, ok := latest[n]
		if !ok {
			order = append(order, n)
		}
		decisive := r.State == "APPROVED" || r.State == "CHANGES_REQUESTED"
		prevDecisive := ok && (prev.State == "APPROVED" || prev.State == "CHANGES_REQUESTED")
		if decisive || !prevDecisive {
			latest[n] = r
		}
	}
	var summary []string
	for _, n := range order {
		switch latest[n].State {
		case "APPROVED":
			summary = append(summary, sGreen.Render(n+" approved"))
		case "CHANGES_REQUESTED":
			summary = append(summary, sRed.Render(n+" requested changes"))
		case "COMMENTED":
			summary = append(summary, sDim.Render(n+" commented"))
		}
	}
	for _, rr := range d.item.Requests {
		n := rr.Login
		if n == "" {
			n = "@" + rr.Team
		}
		summary = append(summary, sYellow.Render("waiting on "+n))
	}
	if len(summary) == 0 {
		a.add(sDim.Render("  none yet"))
	} else {
		for _, l := range wrap(strings.Join(summary, sDim.Render(" · ")), w-2) {
			a.add("  " + l)
		}
	}

	a.doc = "Checks"
	d.checksRows(a, w, section)
	a.doc = ""

	section("Activity")
	a.rows = append(a.rows, d.activityRows(w)...)

	unresolved := 0
	for _, t := range d.det.Threads {
		if !t.IsResolved {
			unresolved++
		}
	}
	section(fmt.Sprintf("Threads · %d unresolved, %d resolved", unresolved, len(d.det.Threads)-unresolved))
	for i := range d.det.Threads {
		t := &d.det.Threads[i]
		for _, r := range d.threadRows(t, w, true) {
			a.rows = append(a.rows, r)
		}
	}

	if len(d.det.Files) > 0 {
		viewed := 0
		for _, f := range d.det.Files {
			if d.viewed[f.Path] == "VIEWED" {
				viewed++
			}
		}
		section(fmt.Sprintf("Files · %d of %d viewed", viewed, len(d.det.Files)))
		for _, f := range d.det.Files {
			a.add("  " + fileState(d.viewed[f.Path]) + " " + fit(sGreen.Render(fmt.Sprintf("+%d", f.Additions))+" "+sRed.Render(fmt.Sprintf("-%d", f.Deletions)), 11) + f.Path)
		}
	}
	return a.rows
}

func fileState(s string) string {
	switch s {
	case "VIEWED":
		return sGreen.Render("✓")
	case "DISMISSED":
		return sYellow.Render("~")
	}
	return sDim.Render("·")
}

// threadRows renders a review thread. In the overview it includes the code it
// refers to; in the diff the code is already on screen.
func (d *detail) threadRows(t *gh.Thread, w int, withCode bool) []brow {
	if len(t.Comments.Nodes) == 0 {
		return nil
	}
	id := "thread:" + t.ID
	allRead := true
	for _, c := range t.Comments.Nodes {
		if !d.read(c.CreatedAt, c.Author) {
			allRead = false
		}
	}
	f := d.folded(id, t.IsResolved || allRead)
	bar := sYellow.Render("┃ ")
	state := sYellow.Render("unresolved")
	if t.IsResolved {
		bar, state = sDim.Render("┃ "), sDim.Render("resolved")
	}
	ind := "  "
	if !withCode {
		ind = "        "
	}
	line := t.Line
	if line == 0 {
		line = t.OriginalLine
	}
	a := &rowAdder{}
	a.begin(id, t.ID, f)
	loc := ""
	if withCode {
		loc = sBlue.Render(fmt.Sprintf("%s:%d", t.Path, line)) + " "
	}
	if t.IsOutdated {
		state += sDim.Render(" · outdated")
	}
	last := t.Comments.Nodes[len(t.Comments.Nodes)-1]
	newest := ""
	for _, c := range t.Comments.Nodes {
		if m := d.newMark(c.CreatedAt, c.Author); m != "" {
			newest = m
		}
	}
	head := ind + foldGlyph(f) + loc + state + sDim.Render(" · "+count(len(t.Comments.Nodes), "comment")) + newest
	if r, _ := d.draftReply(t.ID); r != nil && !r.Excluded {
		head += sYellow.Render(" · ↩ draft reply")
	}
	if f {
		head += sDim.Render("  " + author(last.Author) + ": " + firstLine(last.Body, w))
	}
	a.add(head)
	if f {
		return a.rows
	}
	inner := ind + "  " + bar
	cw := w - lipgloss.Width(inner)
	if withCode && t.Comments.Nodes[0].DiffHunk != "" {
		hunk := parseDiff("diff --git a/x b/" + t.Path + "\n" + t.Comments.Nodes[0].DiffHunk)
		var code []diffLine
		for _, l := range hunk {
			if l.commentable() {
				code = append(code, l)
			}
		}
		if len(code) > 4 {
			code = code[len(code)-4:]
		}
		for _, l := range code {
			a.add(inner + renderDiffLine(l, cw))
		}
	}
	for _, c := range t.Comments.Nodes {
		a.add(inner + sBold.Render(author(c.Author)) + sDim.Render(" "+agoText(c.CreatedAt)) + d.newMark(c.CreatedAt, c.Author))
		for _, l := range renderMarkdown(c.Body, cw-2) {
			a.add(inner + "  " + l)
		}
	}
	return a.rows
}

// ---- detail keys

func (m *Model) detailKey(d *detail, key string) tea.Cmd {
	v := d.nav()
	rows := d.rows[d.tab]
	jumpRow := func(match func(r brow, i int) bool, step int) {
		for i := v.cur + step; i >= 0 && i < len(rows); i += step {
			if match(rows[i], i) {
				v.jump(i)
				break
			}
		}
	}
	threadStart := func(r brow, _ int) bool { return r.thread != "" && r.head }

	switch key {
	case "za", "zo", "zc":
		d.foldAt(key)
		return nil
	case "zR":
		d.foldEverything(false)
		m.info("all folds open")
		return nil
	case "zM":
		d.foldEverything(true)
		m.info("all folds closed")
		return nil
	case "v":
		if m.mode == modeVisual {
			m.mode = modeNormal
		} else {
			m.mode = modeVisual
			m.visualAnchor = v.cur
		}
		return nil
	case "y":
		m.yank(d)
		return nil
	}

	switch d.tab {
	case tabOverview:
		switch key {
		case "]n":
			jumpRow(func(r brow, _ int) bool { return r.fresh }, 1)
		case "[n":
			jumpRow(func(r brow, _ int) bool { return r.fresh }, -1)
		case "]t":
			jumpRow(threadStart, 1)
			v.align('t')
		case "[t":
			jumpRow(threadStart, -1)
			v.align('t')
		case "gl":
			return m.loadCheckLog(d)
		}
	case tabDiff:
		lines := d.diffs[d.sinceReview]
		kind := func(k lineKind) func(r brow, i int) bool {
			return func(r brow, _ int) bool { return r.diff >= 0 && lines[r.diff].kind == k }
		}
		switch key {
		case "]c":
			jumpRow(kind(lkHunk), 1)
		case "[c":
			jumpRow(kind(lkHunk), -1)
		case "]f":
			jumpRow(kind(lkFile), 1)
			v.align('t')
		case "[f":
			jumpRow(kind(lkFile), -1)
			v.align('t')
		case "]t":
			jumpRow(threadStart, 1)
		case "[t":
			jumpRow(threadStart, -1)
		case "V":
			return m.toggleViewed(d)
		case "s":
			splitDiff = !splitDiff
			for _, other := range m.details {
				other.dirty = true
			}
			if splitDiff {
				m.info("side-by-side diff · s for unified")
			} else {
				m.info("unified diff · s for side-by-side")
			}
		case "D":
			if d.item.LastReviewedOid == "" {
				m.info("you haven't reviewed this PR yet, so there's only the full diff")
				return nil
			}
			d.sinceReview = !d.sinceReview
			d.dirty = true
			m.mode = modeNormal
			if d.sinceReview {
				m.info("diff: changes since your last review")
			} else {
				m.info("diff: full PR")
			}
			return d.loadDiff(m.core)
		case "c":
			return m.commentOnSelection(d, false)
		case "S":
			return m.commentOnSelection(d, true)
		case "dd":
			if v.cur < len(rows) && rows[v.cur].diff >= 0 && d.removePending(commentLine(rows[v.cur], lines)) {
				m.core.SetPending(d.item.Key, d.pending)
				m.info("dropped pending comment")
			}
		}
	case tabReview:
		switch key {
		case "c":
			if v.cur >= len(rows) || rows[v.cur].reply == "" {
				m.info("put the cursor on a draft reply")
				return nil
			}
			return m.replyPopup(d, rows[v.cur].reply)
		case "x":
			d.toggleIncluded(m)
		case "]f", "[f":
			step := 1
			if key[0] == '[' {
				step = -1
			}
			jumpRow(func(r brow, i int) bool {
				return r.finding >= 0 && (i == 0 || rows[i-1].finding != r.finding) && (v.cur >= len(rows) || r.finding != rows[v.cur].finding)
			}, step)
			v.align('t')
		case "m", "u":
			cur := d.current()
			if len(cur) == 0 {
				return nil
			}
			for _, r := range cur {
				for i := range r.Findings {
					r.Findings[i].Excluded = key == "m" && r.Findings[i].Severity != core.MustFix
				}
			}
			if err := m.core.MustFixOnly(d.item.Key, key == "m"); err != nil {
				m.fail(err.Error())
				return nil
			}
			d.dirty = true
			if key == "m" {
				m.info("keeping must-fix findings only")
			} else {
				m.info("all findings included")
			}
		}
	}
	return nil
}

type viewedMsg struct {
	key, path string
	viewed    bool
	err       error
}

func (m *Model) toggleViewed(d *detail) tea.Cmd {
	v := d.nav()
	rows := d.rows[tabDiff]
	if v.cur >= len(rows) || rows[v.cur].file == "" {
		return nil
	}
	path := rows[v.cur].file
	viewed := d.viewed[path] != "VIEWED"
	if viewed {
		d.viewed[path] = "VIEWED"
	} else {
		d.viewed[path] = "UNVIEWED"
	}
	delete(d.folds, "file:"+path)
	d.dirty = true
	d.build()

	for i, r := range d.rows[tabDiff] {
		if r.head && r.block == "file:"+path {
			v.jump(i)
			if viewed {
				for j := i + 1; j < len(d.rows[tabDiff]); j++ {
					if d.rows[tabDiff][j].head && strings.HasPrefix(d.rows[tabDiff][j].block, "file:") {
						v.jump(j)
						break
					}
				}
			}
			break
		}
	}
	co, key := m.core, d.item.Key
	return func() tea.Msg {
		return viewedMsg{key: key, path: path, viewed: viewed, err: co.SetFileViewed(key, path, viewed)}
	}
}

// toggleIncluded includes or excludes the finding, or the draft reply,
// under the cursor in what gets sent.
func (d *detail) toggleIncluded(m *Model) {
	v := d.nav()
	rows := d.rows[tabReview]
	if v.cur >= len(rows) {
		return
	}
	d.dirty = true
	if id := rows[v.cur].reply; id != "" {
		for _, run := range d.current() {
			for i := range run.Replies {
				if r := &run.Replies[i]; r.ThreadID == id && !r.Posted {
					r.Excluded = !r.Excluded
					if err := m.core.SetReplyExcluded(run.ID, id, r.Excluded); err != nil {
						m.fail(err.Error())
					}
				}
			}
		}
		return
	}
	if rows[v.cur].finding < 0 || rows[v.cur].finding >= len(d.flat) {
		return
	}
	ref := d.flat[rows[v.cur].finding]
	for _, run := range d.runs {
		if run.ID == ref.runID && ref.i < len(run.Findings) {
			run.Findings[ref.i].Excluded = !run.Findings[ref.i].Excluded
			if err := m.core.SetExcluded(run.ID, ref.i, run.Findings[ref.i].Excluded); err != nil {
				m.fail(err.Error())
			}
		}
	}
}

// press is the left button going down on a row of a PR: again when that
// row already held the cursor, which makes the click act on it.
type press struct {
	on, again bool
	row       int
}

// bodyMouse handles the left button in a PR's body, whose first row is on
// screen line top. A press moves the cursor and a drag selects lines, as v
// does. A click folds a head row and, on the row that held the cursor,
// includes a finding or comments on a diff line.
func (m *Model) bodyMouse(d *detail, top int, msg tea.MouseMsg) tea.Cmd {
	v := d.nav()
	rows := d.rows[d.tab]
	at := v.top + msg.Y - top
	switch msg.Action {
	case tea.MouseActionPress:
		m.focus = fDetail
		if m.mode == modeVisual {
			m.mode = modeNormal
		}
		again := v.cur == at
		v.jump(at)
		m.press = press{on: v.cur == at && at < len(rows), again: again, row: at}
	case tea.MouseActionMotion:
		if !m.press.on || at == v.cur {
			return nil
		}
		if m.mode != modeVisual {
			m.mode, m.visualAnchor = modeVisual, m.press.row
		}
		v.jump(at)
	case tea.MouseActionRelease:
		p := m.press
		if !p.on || p.row != at || at >= len(rows) || m.mode == modeVisual {
			return nil
		}
		switch r := rows[at]; {
		case r.head:
			d.foldAt("za")
		case !p.again:
		case d.tab == tabReview && r.finding >= 0:
			d.toggleIncluded(m)
		case d.tab == tabDiff && r.diff >= 0:
			return m.commentOnSelection(d, false)
		}
	}
	return nil
}

// yank copies the cursor's row, or the visual selection, to the clipboard.
func (m *Model) yank(d *detail) {
	a, b := m.selection(d.nav())
	m.mode = modeNormal
	text, n := d.yankText(a, b)
	if n == 0 {
		return
	}
	if err := platform.Copy(text); err != nil {
		m.fail(err.Error())
		return
	}
	m.info("copied " + count(n, "line"))
}

// yankText is rows a through b of the open tab as text to paste, and how
// many lines that is. A diff row gives its line of code, without the line
// numbers or the +/- mark; a side-by-side row that pairs a removed line
// with an added one gives both.
func (d *detail) yankText(a, b int) (string, int) {
	rows := d.rows[d.tab]
	lines := d.diffs[d.sinceReview]
	var out []string
	for i := a; i <= b && i < len(rows); i++ {
		r := rows[i]
		if d.tab != tabDiff || r.diff < 0 || r.diff >= len(lines) {
			out = append(out, strings.TrimRight(r.plain, " "))
			continue
		}
		out = append(out, lines[r.diff].text)
		if splitDiff && lines[r.diff].kind == lkDel && r.pair >= 0 {
			out = append(out, lines[r.pair].text)
		}
	}
	return strings.Join(out, "\n"), len(out)
}

// selection is the rows visual mode covers, or the cursor's row alone.
func (m *Model) selection(v *nav) (int, int) {
	if m.mode == modeVisual {
		return min(m.visualAnchor, v.cur), max(m.visualAnchor, v.cur)
	}
	return v.cur, v.cur
}

// mark puts row i of the PR behind its gutter, which shows the cursor's mark
// when the PR pane has focus and the row is selected or under the cursor.
func (m *Model) mark(row string, w int, v *nav, i int) string {
	row = " " + row
	if m.focus != fDetail {
		return row
	}
	a, b := m.selection(v)
	switch {
	case m.mode == modeVisual && i >= a && i <= b:
		return highlight(row, w+1, sPink)
	case i == v.cur:
		return highlight(row, w+1, sTitle)
	}
	return row
}

// gutter puts lines that have no cursor behind the PR pane's gutter.
func gutter(lines []string) []string {
	for i, l := range lines {
		lines[i] = " " + l
	}
	return lines
}

// commentOnSelection starts an inline comment on the line under the cursor
// or the visual selection. With suggest, it starts a GitHub suggested change
// instead: a suggestion block holding the new version of those lines, for
// the author to apply with a click.
func (m *Model) commentOnSelection(d *detail, suggest bool) tea.Cmd {
	v := d.nav()
	rows := d.rows[tabDiff]
	lines := d.diffs[d.sinceReview]
	if len(rows) == 0 {
		return nil
	}
	if d.sinceReview {
		m.fail("GitHub anchors comments to the full PR diff. Press D to switch, then comment")
		return nil
	}
	a, b := m.selection(v)
	var sel []diffLine
	for i := a; i <= b && i < len(rows); i++ {
		if r := rows[i]; r.diff >= 0 && lines[r.diff].commentable() {
			if l := commentLine(r, lines); !suggest || l.newLine > 0 {
				sel = append(sel, l)
			}
		}
	}
	switch {
	case len(sel) == 0 && suggest:
		m.fail("a suggestion replaces lines of the new version; select added or unchanged lines")
		return nil
	case len(sel) == 0:
		m.fail("can't comment here; move to a changed or context line")
		return nil
	}
	if sel[0].path != sel[len(sel)-1].path {
		m.fail("a comment range has to stay inside one file")
		return nil
	}
	m.mode = modeNormal
	body := d.pendingAt(sel[len(sel)-1])
	if suggest {
		var code []string
		for _, l := range sel {
			code = append(code, l.text)
		}
		body = "```suggestion\n" + strings.Join(code, "\n") + "\n```\n"
	}
	p := newTextPopup(m, popInline, d.item, sel, body)
	m.popup = p
	return p.ta.Focus()
}

// docsTopic is the docs topic for the row under the cursor: the part it is
// in when that has its own section (rowAdder.doc: the AI summary, checks,
// points raised, history, a pending comment), its finding, draft reply or
// thread, or else its tab.
func (d *detail) docsTopic() string {
	rows := d.rows[d.tab]
	if v := d.nav(); v.cur < len(rows) {
		switch r := rows[v.cur]; {
		case r.doc != "":
			return r.doc
		case d.tab == tabDiff && r.diff >= 0 && d.pendingIdx(commentLine(r, d.diffs[d.sinceReview])) >= 0:
			return "Inline comments and suggestions"
		case r.finding >= 0:
			return "Severities"
		case r.reply != "":
			return "Draft replies"
		case r.thread != "":
			return "Threads"
		}
	}
	return []string{"Overview", "The diff", "Findings"}[d.tab]
}

// threadAtCursor returns the review thread under the cursor, if any.
func (d *detail) threadAtCursor() *gh.Thread {
	if d.det == nil || d.tab == tabReview {
		return nil
	}
	v := d.nav()
	rows := d.rows[d.tab]
	if v.cur >= len(rows) || rows[v.cur].thread == "" {
		return nil
	}
	for i := range d.det.Threads {
		if d.det.Threads[i].ID == rows[v.cur].thread {
			return &d.det.Threads[i]
		}
	}
	return nil
}

// ---- rendering

// header returns the fixed lines above the tab body.
func (d *detail) header(m *Model) []string {
	pr := d.item
	w := d.w
	if pr.Local() {
		return []string{
			fit(styledTitle(pr.Title, true), w),
			fit(sPink.Render("⌂ "+pr.Branch)+sDim.Render(" → "+pr.Base+" · reviewed "+agoText(d.item.Since)), w),
			fit(bucketStyle(d.item.Bucket).Render("local")+sDim.Render(": "+d.item.Reason), w),
		}
	}
	meta := []string{
		sPink.Render(pr.Author),
		sDim.Render(pr.Branch + " → " + pr.Base),
		sGreen.Render(fmt.Sprintf("+%d", pr.Additions)) + " " + sRed.Render(fmt.Sprintf("-%d", pr.Deletions)) + sDim.Render(fmt.Sprintf(" in %d files", pr.ChangedFiles)),
		sDim.Render("opened " + agoText(pr.CreatedAt)),
	}
	reason := bucketStyle(d.item.Bucket).Render(d.item.Bucket.String()) + sDim.Render(": "+d.item.Reason)
	if d.item.SlackLink != "" {
		reason = ansi.SetHyperlink(d.item.SlackLink) + reason + ansi.ResetHyperlink()
	}
	status := []string{reason, statusBadge(d.item.ReviewStatus)}
	if ci := pr.CI; ci != "" {
		status = append(status, ciBadge(ci)+sDim.Render(" ci"))
	}
	if d.item.UnresolvedCount > 0 {
		status = append(status, sYellow.Render(fmt.Sprintf("%d unresolved", d.item.UnresolvedCount)))
	}
	if len(d.pending) > 0 {
		status = append(status, sYellow.Render(fmt.Sprintf("◆ %d pending", len(d.pending))))
	}
	sep := sDim.Render(" · ")
	return []string{
		fit(styledTitle(pr.Title, true), w),
		fit(strings.Join(meta, sep), w),
		fit(strings.Join(status, sep), w) + ansi.ResetHyperlink(),
	}
}

func (d *detail) body(m *Model) []string {
	d.build()
	v := &d.navs[d.tab]
	h := d.bodyH(d.tab)
	var out []string
	switch d.tab {
	case tabOverview:
		if d.det == nil && d.detErr == nil {
			return gutter(skeleton(m, d.w, min(h, 12)))
		}
		out = d.window(m, d.rows[tabOverview], v, h)
	case tabDiff:
		lines := d.diffs[d.sinceReview]
		switch {
		case d.diffErr != nil:
			for _, l := range wrap(d.diffErr.Error(), d.w) {
				out = append(out, " "+sErr.Render(l))
			}
		case lines == nil:
			return gutter(skeleton(m, d.w, min(h, 16)))
		case len(lines) == 0:
			out = append(out, " "+sDim.Render("no changes"))
		default:
			rows := d.rows[tabDiff]
			for i := v.top; i < len(rows) && i < v.top+h; i++ {
				r := rows[i]
				var row string
				switch {
				case r.diff >= 0 && lines[r.diff].kind == lkFile:
					row = d.fileHeader(r.file)
				case r.diff >= 0 && lines[r.diff].commentable() && splitDiff:
					row = d.splitRow(r, lines)
				case r.diff >= 0 && lines[r.diff].commentable():
					l := lines[r.diff]
					row = renderCode(l, d.highlighter().get(lines, r.diff), d.w, d.pendingIdx(l) >= 0, lineNum(l.oldLine)+" "+lineNum(l.newLine)+" ")
				case r.diff >= 0:
					row = renderDiffLine(lines[r.diff], d.w)
				default:
					row = fit(r.text, d.w)
				}
				out = append(out, m.mark(row, d.w, v, i))
			}
		}
	case tabReview:
		out = append(out, " "+d.reviewStatus(m), "")
		if cur := d.current(); playful && firstReviewRunning(cur) {
			step := int(time.Since(cur[0].StartedAt)/(360*time.Millisecond)) + 5
			watch := append(wakingOwl(step, true), "", sDim.Render("    reading the PR…"))
			return append(out, gutter(watch)...)
		}
		out = append(out, d.window(m, d.rows[tabReview], v, h)...)
	}
	return out
}

func (d *detail) window(m *Model, rows []brow, v *nav, h int) []string {
	var out []string
	for i := v.top; i < len(rows) && i < v.top+h; i++ {
		out = append(out, m.mark(fit(rows[i].text, d.w), d.w, v, i))
	}
	return out
}

// modelName turns "claude-opus-5-5" into "Opus 5.5".
func modelName(id string) string {
	id = strings.TrimPrefix(id, "claude-")
	parts := strings.Split(id, "-")
	if len(parts) < 2 || parts[0] == "" {
		return id
	}
	var ver []string
	for _, p := range parts[1:] {
		if len(p) > 4 {
			break
		}
		ver = append(ver, p)
	}
	return strings.ToUpper(parts[0][:1]) + parts[0][1:] + " " + strings.Join(ver, ".")
}

// firstReviewRunning is true while every agent is on its first draft, so the
// tab has nothing to show yet.
func firstReviewRunning(cur []*core.Review) bool {
	if len(cur) == 0 {
		return false
	}
	for _, r := range cur {
		if r.Status != core.ReviewRunning || r.Summary != "" {
			return false
		}
	}
	return true
}

// agoText is how long ago t was, as words: "just now", "3h ago".
func agoText(t time.Time) string {
	if a := ago(t); a != "now" {
		return a + " ago"
	}
	return "just now"
}
