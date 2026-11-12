package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/inbox"
	"github.com/tmstorm/prowl/internal/store"
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
	item inbox.Item
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
	pending     []gh.DraftComment
	viewed      map[string]string // path -> VIEWED / UNVIEWED / DISMISSED
	hl          map[bool]*highlighter

	folds   map[string]bool // explicit fold state per block
	foldAll *bool           // zR / zM override

	runs        []*store.AgentReview // every agent's review runs, newest first
	summary     *store.Summary       // the AI summary, if one was written
	summarizing bool                 // a summary is being written
	flat        []findingRef         // the current reviews' findings, as rows refer to them
	reviewSig   string
	logs        map[string][]store.LogEntry // per agent: your instructions and its replies (local only)
	ciLogs      map[int64]ciLog             // failed Actions jobs' logs, as gl loaded them

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

func newDetail(it inbox.Item, me string) *detail {
	d := &detail{item: it, me: me, diffs: map[bool][]diffLine{}, dirty: true, hl: map[bool]*highlighter{},
		folds: map[string]bool{}, viewed: map[string]string{}}
	d.sinceReview = it.LastReviewedOid != "" && it.LastReviewedOid != it.PR.HeadRefOid
	d.jumpNew = true
	for i := range d.navs {
		d.navs[i].scrolloff = 4
	}
	return d
}

func (d *detail) setItem(it inbox.Item) {
	if it.PR.HeadRefOid != d.item.PR.HeadRefOid {
		d.diffs = map[bool][]diffLine{}
		d.hl = map[bool]*highlighter{}
		d.stale = true
	}
	if !it.PR.UpdatedAt.Equal(d.item.PR.UpdatedAt) {
		d.stale = true
	}
	d.item = it
	d.dirty = true
}

// setReview updates the review runs, rebuilding rows only when something
// visible in them changed (the live status line renders separately).
func (d *detail) setReview(runs []*store.AgentReview) {
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

func (d *detail) setSummary(sum *store.Summary, running bool) {
	if sum != d.summary || running != d.summarizing {
		d.summary, d.summarizing = sum, running
		d.dirty = true
	}
}

func (d *detail) setLogs(logs map[string][]store.LogEntry) {
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
func (d *detail) current() []*store.AgentReview {
	cur := store.Current(d.runs)
	var out []*store.AgentReview
	for _, name := range reviewerOrder {
		if r := store.Latest(cur, name); r != nil {
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
	key := d.item.PR.Key()
	return func() tea.Msg {
		det, err := co.Detail(key)
		return detailMsg{key, det, err}
	}
}

func (d *detail) setTab(t int, co *core.Core) tea.Cmd {
	d.tab = t
	d.dirty = true
	return d.ensureLoaded(co)
}

func (d *detail) loadDiff(co *core.Core) tea.Cmd {
	if _, ok := d.diffs[d.sinceReview]; ok || d.diffLoading {
		return nil
	}
	key, since := d.item.PR.Key(), d.sinceReview
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
		d.item.PR.Body = msg.det.Body
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

func plainRow(s string) brow { return brow{text: s, plain: ansi.Strip(s), finding: -1, diff: -1} }

type rowAdder struct {
	rows   []brow
	block  string
	off    int
	head   bool
	folded bool
	thread string
	file   string
}

func (a *rowAdder) add(s string) {
	r := plainRow(s)
	r.block, r.thread, r.file = a.block, a.thread, a.file
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
func (d *detail) summaryRows(a *rowAdder, w int, theme string) {
	switch {
	case d.summarizing:
		a.add(" " + sSection.Render("AI summary") + sDim.Render(" · writing…"))
	case d.summary == nil:
		a.add(" " + sDim.Render(leader+"ca writes an AI summary of this PR"))
	default:
		folded := d.folded("summary", false)
		a.begin("summary", "", folded)
		head := " " + foldGlyph(folded) + sSection.Render("AI summary") + sDim.Render(" · "+modelName(d.summary.Model)+" · for "+gh.Short(d.summary.HeadOid))
		if d.summary.HeadOid != d.item.PR.HeadRefOid {
			head += sYellow.Render(" · new commits since, " + leader + "ca to refresh")
		}
		a.add(head)
		if !folded {
			for _, l := range renderMarkdown(d.summary.Text, w-4, theme) {
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
	theme := themeName
	d.summaryRows(a, w, theme)
	if len(d.pending) > 0 {
		section(fmt.Sprintf("Pending comments (%d) · sent with your next review", len(d.pending)))
		for _, p := range d.pending {
			loc := fmt.Sprintf("%s:%d", p.Path, p.Line)
			if p.StartLine > 0 {
				loc = fmt.Sprintf("%s:%d-%d", p.Path, p.StartLine, p.Line)
			}
			a.add("  " + sYellow.Render("◆ "+loc))
			for _, l := range renderMarkdown(p.Body, w-4, theme) {
				a.add("    " + l)
			}
		}
	}

	a.end()
	local := d.item.PR.Local()
	descFolded := d.folded("desc", !d.seenAt.IsZero() && !local)
	a.begin("desc", "", descFolded)
	title := "Description"
	if local {
		title = "Branch"
	}
	a.add(" " + foldGlyph(descFolded) + sSection.Render(title))
	if !descFolded {
		body := renderMarkdown(d.det.Body, w-2, theme)
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
	for _, rr := range d.item.PR.ReviewRequests.Nodes {
		n := rr.RequestedReviewer.Login
		if n == "" {
			n = "@" + rr.RequestedReviewer.Slug
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

	d.checksRows(a, w, section)

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
	head := ind + foldGlyph(f) + loc + state + sDim.Render(fmt.Sprintf(" · %d comment(s)", len(t.Comments.Nodes))) + newest
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
			a.add(inner + renderDiffLine(l, cw, false))
		}
	}
	for _, c := range t.Comments.Nodes {
		a.add(inner + sBold.Render(author(c.Author)) + sDim.Render(" "+ago(c.CreatedAt)+" ago") + d.newMark(c.CreatedAt, c.Author))
		for _, l := range renderMarkdown(c.Body, cw-2, themeName) {
			a.add(inner + "  " + l)
		}
	}
	return a.rows
}

func (d *detail) buildDiff() []brow {
	lines := d.diffs[d.sinceReview]
	if lines == nil {
		return nil
	}
	threads := map[string][]*gh.Thread{}
	if d.det != nil {
		for i := range d.det.Threads {
			t := &d.det.Threads[i]
			if t.Line > 0 && !t.IsOutdated {
				side := t.DiffSide
				if side == "" {
					side = "RIGHT"
				}
				k := fmt.Sprintf("%s:%s:%d", t.Path, side, t.Line)
				threads[k] = append(threads[k], t)
			}
		}
	}
	var rows []brow
	file := ""
	folded := false
	addThreads := func(l diffLine) {
		ln, side := l.anchor()
		for _, t := range threads[fmt.Sprintf("%s:%s:%d", l.path, side, ln)] {
			for _, r := range d.threadRows(t, d.w, false) {
				r.file = file
				rows = append(rows, r)
			}
		}
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if l.kind == lkFile {
			file = l.path
			folded = d.fileFolded(file)
			rows = append(rows, brow{diff: i, file: file, block: "file:" + file, head: true, finding: -1, pair: -1})
			continue
		}
		if folded {
			continue
		}
		if splitDiff && l.kind == lkDel {
			dels, adds := changeRun(lines, i)
			for k := 0; k < max(len(dels), len(adds)); k++ {
				r := brow{diff: -1, file: file, finding: -1, pair: -1}
				switch {
				case k < len(dels) && k < len(adds):
					r.diff, r.pair = dels[k], adds[k]
				case k < len(dels):
					r.diff = dels[k]
				default:
					r.diff = adds[k]
				}
				rows = append(rows, r)
				addThreads(lines[r.diff])
				if r.pair >= 0 {
					addThreads(lines[r.pair])
				}
			}
			i += len(dels) + len(adds) - 1
			continue
		}
		rows = append(rows, brow{diff: i, file: file, finding: -1, pair: -1})
		if l.commentable() {
			addThreads(l)
		}
	}
	return rows
}

// changeRun is the removed lines starting at i and the added lines right
// after them, the lines the split diff pairs up row by row.
func changeRun(lines []diffLine, i int) (dels, adds []int) {
	for ; i < len(lines) && lines[i].kind == lkDel; i++ {
		dels = append(dels, i)
	}
	for ; i < len(lines) && lines[i].kind == lkAdd; i++ {
		adds = append(adds, i)
	}
	return dels, adds
}

// splitRow draws a row of the split diff: the old line on the left and the
// new line on the right, either side blank when the row has none.
func (d *detail) splitRow(r brow, lines []diffLine) string {
	left := (d.w - 1) / 2
	right := d.w - 1 - left
	var old, cur *diffLine
	l := lines[r.diff]
	switch {
	case l.kind == lkCtx:
		old, cur = &l, &l
	case l.kind == lkDel:
		old = &l
		if r.pair >= 0 {
			cur = &lines[r.pair]
		}
	default:
		cur = &l
	}
	side := func(dl *diffLine, idx, width int, n int) string {
		if dl == nil {
			return strings.Repeat(" ", width)
		}
		return renderCode(*dl, d.highlighter().get(lines, idx), width, d.pendingIdx(*dl) >= 0, lineNum(n)+" ")
	}
	newIdx := r.diff
	if r.pair >= 0 {
		newIdx = r.pair
	}
	var oldN, newN int
	if old != nil {
		oldN = old.oldLine
	}
	if cur != nil {
		newN = cur.newLine
	}
	return side(old, r.diff, left, oldN) + sBorder.Render("│") + side(cur, newIdx, right, newN)
}

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
	total, inc := 0, len(agent.Combine(cur, false))
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
func (d *detail) agentSection(run *store.AgentReview, cur []*store.AgentReview) []brow {
	name := run.Reviewer()
	id := "agent:" + name
	folded := d.folded(id, false)
	a := &rowAdder{}
	a.begin(id, "", folded)
	head := foldGlyph(folded) + sTitle.Bold(true).Render(core.ReviewerTitle(name)) + sDim.Render(" · ") + runState(run)
	if run.Model != "" {
		head += sDim.Render(" · ") + sPink.Render(modelName(run.Model))
	}
	if run.Status == "done" || run.Status == "posted" {
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
		head += sDim.Render(" · at " + gh.Short(run.HeadOid) + " · " + ranText(run))
		if run.Origin == "local" {
			head += sDim.Render(" · in your checkout, before the PR")
		}
		if run.HeadOid != d.item.PR.HeadRefOid {
			head += sYellow.Render(" · new commits since")
		}
	}
	a.add(head)
	addF := func(s string, f int) {
		a.add(s)
		a.rows[len(a.rows)-1].finding = f
	}
	if !folded {
		if run.Status == "running" && run.Summary == "" {
			a.add("  " + sDim.Render("reviewing… progress is shown above"))
		}
		if run.Status == "failed" {
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
			for _, l := range renderMarkdown(run.Summary, d.w-4, themeName) {
				a.add("  " + l)
			}
		}
		if run.Action != "" {
			a.add("")
			what, key := "Suggests closing this PR", leader+"X"
			if run.Action == "draft" {
				what, key = "Suggests making this PR a draft", leader+"D"
			}
			a.add("  " + sSection.Render(what) + sDim.Render(" · "+key+", or event: "+run.Action+" in "+leader+"p"))
			for _, l := range renderMarkdown(run.ActionReason, d.w-6, themeName) {
				a.add("    " + l)
			}
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
					who = sTitle.Render(strings.ToLower(agent.Title(run.Agent)))
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
		if (run.Status == "done" || run.Status == "posted") && len(run.Findings) == 0 {
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
			for _, l := range renderMarkdown(f.Body, d.w-8, themeName) {
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
func (d *detail) historyRows(current *store.AgentReview) []brow {
	var earlier []*store.AgentReview
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
	a := &rowAdder{}
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
		b := &rowAdder{}
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
		line := "    " + foldGlyph(rf) + sDim.Render(ranText(r)+" · at "+gh.Short(r.HeadOid)+" · ") + runState(r)
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

func severityStyle(sev string) lipgloss.Style {
	switch sev {
	case "must-fix":
		return sRed
	case "should-fix":
		return sYellow
	}
	return sDim
}

// runState is a run's status in a word or two.
func runState(r *store.AgentReview) string {
	switch r.Status {
	case "running":
		return sYellow.Render("reviewing")
	case "failed":
		return sRed.Render("failed")
	case "posted":
		return sGreen.Render("posted")
	}
	return sGreen.Render("ready")
}

// alsoFoundBy names the other agents whose current review raised f too.
func alsoFoundBy(f store.Finding, run *store.AgentReview, cur []*store.AgentReview) string {
	var names []string
	for _, o := range cur {
		if o == run {
			continue
		}
		for _, g := range o.Findings {
			if agent.Agree(f, g) {
				names = append(names, core.ReviewerTitle(o.Reviewer()))
				break
			}
		}
	}
	return strings.Join(names, ", ")
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
		case "v":
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
		case "V":
			if m.mode == modeVisual {
				m.mode = modeNormal
			} else {
				m.mode = modeVisual
				m.visualAnchor = v.cur
			}
		case "c":
			return m.commentOnSelection(d, false)
		case "S":
			return m.commentOnSelection(d, true)
		case "dd":
			if v.cur < len(rows) && rows[v.cur].diff >= 0 && d.removePending(commentLine(rows[v.cur], lines)) {
				m.core.SetPending(d.item.PR.Key(), d.pending)
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
					r.Findings[i].Excluded = key == "m" && r.Findings[i].Severity != "must-fix"
				}
			}
			if err := m.core.MustFixOnly(d.item.PR.Key(), key == "m"); err != nil {
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
	co, key := m.core, d.item.PR.Key()
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
	a, b := v.cur, v.cur
	if m.mode == modeVisual {
		a, b = min(m.visualAnchor, v.cur), max(m.visualAnchor, v.cur)
	}
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

func (d *detail) pendingIdx(l diffLine) int {
	line, side := l.anchor()
	for i, p := range d.pending {
		if p.Path == l.path && p.Line == line && p.Side == side {
			return i
		}
	}
	return -1
}

func (d *detail) pendingAt(l diffLine) string {
	if i := d.pendingIdx(l); i >= 0 {
		return d.pending[i].Body
	}
	return ""
}

func (d *detail) setPending(sel []diffLine, body string) {
	last := sel[len(sel)-1]
	line, side := last.anchor()
	i := d.pendingIdx(last)
	d.dirty = true
	if strings.TrimSpace(body) == "" {
		if i >= 0 {
			d.pending = append(d.pending[:i], d.pending[i+1:]...)
		}
		return
	}
	c := gh.DraftComment{Path: last.path, Line: line, Side: side, Body: body}
	if len(sel) > 1 {
		c.StartLine, c.StartSide = sel[0].anchor()
	}
	if i >= 0 {
		d.pending[i] = c
	} else {
		d.pending = append(d.pending, c)
	}
}

func (d *detail) removePending(l diffLine) bool {
	if i := d.pendingIdx(l); i >= 0 {
		d.pending = append(d.pending[:i], d.pending[i+1:]...)
		d.dirty = true
		return true
	}
	return false
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
	pr := d.item.PR
	w := d.w
	if pr.Local() {
		return []string{
			fit(styledTitle(pr.Title, true), w),
			fit(sPink.Render("⌂ "+pr.HeadRefName)+sDim.Render(" → "+pr.BaseRefName+" · reviewed "+agoText(d.item.Since)), w),
			fit(bucketStyle(d.item.Bucket).Render("local")+sDim.Render(": "+d.item.Reason), w),
		}
	}
	meta := []string{
		sPink.Render(pr.AuthorLogin()),
		sDim.Render(pr.HeadRefName + " → " + pr.BaseRefName),
		sGreen.Render(fmt.Sprintf("+%d", pr.Additions)) + " " + sRed.Render(fmt.Sprintf("-%d", pr.Deletions)) + sDim.Render(fmt.Sprintf(" in %d files", pr.ChangedFiles)),
		sDim.Render("opened " + ago(pr.CreatedAt) + " ago"),
	}
	status := []string{
		bucketStyle(d.item.Bucket).Render(d.item.Bucket.String()) + sDim.Render(": "+d.item.Reason),
		statusBadge(d.item.ReviewStatus),
	}
	if ci := pr.CIState(); ci != "" {
		status = append(status, ciBadge(ci, m.cfg.NerdFont)+sDim.Render(" ci"))
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
		fit(strings.Join(status, sep), w),
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
			return skeleton(m, d.w, min(h, 12))
		}
		out = d.window(m, d.rows[tabOverview], v, h)
	case tabDiff:
		lines := d.diffs[d.sinceReview]
		switch {
		case d.diffErr != nil:
			for _, l := range wrap(d.diffErr.Error(), d.w) {
				out = append(out, sErr.Render(l))
			}
		case lines == nil:
			return skeleton(m, d.w, min(h, 16))
		case len(lines) == 0:
			out = append(out, sDim.Render("no changes"))
		default:
			a, b := -1, -1
			if m.mode == modeVisual && m.focus == fDetail {
				a, b = min(m.visualAnchor, v.cur), max(m.visualAnchor, v.cur)
			}
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
					row = renderDiffLine(lines[r.diff], d.w, false)
				default:
					row = fit(r.text, d.w)
				}
				switch {
				case i >= a && i <= b:
					row = highlight(row, d.w, sPink)
				case i == v.cur && m.focus == fDetail:
					row = highlight(row, d.w, sTitle)
				}
				out = append(out, row)
			}
		}
	case tabReview:
		out = append(out, d.reviewStatus(m), "")
		if cur := d.current(); playful && firstReviewRunning(cur) {
			step := int(time.Since(cur[0].StartedAt)/(360*time.Millisecond)) + 5
			watch := append(wakingOwl(step, true), "", sDim.Render("    reading the PR…"))
			return append(out, watch...)
		}
		out = append(out, d.window(m, d.rows[tabReview], v, h)...)
	}
	return out
}

func (d *detail) fileHeader(path string) string {
	folded := d.fileFolded(path)
	var stats, state string
	if d.det != nil {
		for _, f := range d.det.Files {
			if f.Path == path {
				stats = sGreen.Render(fmt.Sprintf("+%d", f.Additions)) + " " + sRed.Render(fmt.Sprintf("-%d", f.Deletions))
			}
		}
	}
	switch d.viewed[path] {
	case "VIEWED":
		state = sGreen.Render(" ✓ viewed")
	case "DISMISSED":
		state = sYellow.Render(" ~ changed since viewed")
	}
	left := " " + foldGlyph(folded) + sTitle.Render(path) + " "
	right := " " + stats + state + " "
	fill := d.w - lipgloss.Width(left) - lipgloss.Width(right)
	return trunc(left+sBorder.Render(strings.Repeat("─", max(fill, 1)))+right, d.w)
}

func (d *detail) window(m *Model, rows []brow, v *nav, h int) []string {
	var out []string
	for i := v.top; i < len(rows) && i < v.top+h; i++ {
		row := fit(rows[i].text, d.w)
		if i == v.cur && m.focus == fDetail {
			row = highlight(row, d.w, sTitle)
		}
		out = append(out, row)
	}
	return out
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
func firstReviewRunning(cur []*store.AgentReview) bool {
	if len(cur) == 0 {
		return false
	}
	for _, r := range cur {
		if r.Status != "running" || r.Summary != "" {
			return false
		}
	}
	return true
}

// reviewStatus is the AI review tab's live top line: which agents are done,
// and what the running ones are doing.
func (d *detail) reviewStatus(m *Model) string {
	cur := d.current()
	if len(cur) == 0 {
		var names []string
		for _, n := range m.cfg.Agent.Reviewers {
			names = append(names, agent.Title(n))
		}
		return sDim.Render("idle · " + leader + "cc runs " + strings.Join(names, " and "))
	}
	var parts []string
	running := 0
	for _, r := range cur {
		if r.Status == "running" {
			running++
		}
	}
	for _, r := range cur {
		name := core.ReviewerTitle(r.Reviewer())
		switch r.Status {
		case "running":
			p := spinner(m) + " " + sYellow.Render(name) + sDim.Render(" "+time.Since(r.StartedAt).Round(time.Second).String())
			if running == 1 {
				p += sDim.Render(" · " + trunc(m.core.Progress(r, time.Now()), max(d.w-50, 10)))
			}
			parts = append(parts, p)
		case "failed":
			parts = append(parts, sRed.Render("✗ "+name+" failed"))
		case "posted":
			parts = append(parts, sGreen.Render("↑ "+name+" posted"))
		default:
			parts = append(parts, sGreen.Render("✓ "+name))
		}
	}
	return strings.Join(parts, sDim.Render("  ·  "))
}

// replyRows lists the replies a reviewer drafted to threads waiting on you.
func (d *detail) replyRows(a *rowAdder, run *store.AgentReview) {
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
			head, body = author(last.Author)+", "+agoText(last.CreatedAt)+":", last.Body
		}
		add("      " + sPink.Render(head))
		quote := renderMarkdown(body, d.w-10, themeName)
		for i, l := range quote {
			if i == quotedLines {
				add("      " + sBorder.Render("┃ ") + sDim.Render(fmt.Sprintf("… %d more lines; the whole thread is in the Overview", len(quote)-quotedLines)))
				break
			}
			add("      " + sBorder.Render("┃ ") + l)
		}
		add("      " + sSection.Render("your reply:"))
		for _, l := range renderMarkdown(r.Body, d.w-8, themeName) {
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
func (d *detail) threadEnds(r store.ThreadReply) (opener, last *gh.Comment) {
	if d.det == nil {
		return nil, nil
	}
	for _, t := range d.det.Threads {
		if t.ID != r.ThreadID || len(t.Comments.Nodes) == 0 {
			continue
		}
		cms := t.Comments.Nodes
		if author(cms[0].Author) == d.me && len(cms) > 1 {
			opener = &cms[0]
		}
		return opener, &cms[len(cms)-1]
	}
	return nil, nil
}

// draftReply is the unsent reply a current review drafted for a thread, and
// the run it belongs to.
func (d *detail) draftReply(threadID string) (*store.ThreadReply, int64) {
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
	var t *gh.Thread
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
func (d *detail) pointRows(a *rowAdder, run *store.AgentReview) {
	if len(run.Points) == 0 {
		return
	}
	a.add("")
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
}

// ranText is when a review started and how long it took.
func ranText(r *store.AgentReview) string {
	s := r.StartedAt.Format("Jan 2 15:04")
	if !r.FinishedAt.IsZero() {
		s += ", took " + r.FinishedAt.Sub(r.StartedAt).Round(time.Second).String()
	}
	return s
}

// agoText is how long ago t was, as words: "just now", "3h ago".
func agoText(t time.Time) string {
	if a := ago(t); a != "now" {
		return a + " ago"
	}
	return "just now"
}
