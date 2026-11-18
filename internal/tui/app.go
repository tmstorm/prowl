package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/tmstorm/prowl/docs"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/platform"
)

type filter int

const (
	fNeedsMe filter = iota
	fAll
	fMine
	fWaiting
	fApproved
)

// tab order in the inbox title (filter values are stable, display order isn't)
var filterOrder = []filter{fNeedsMe, fAll, fMine, fApproved, fWaiting}
var filterNames = map[filter]string{fNeedsMe: "Needs me", fAll: "All", fMine: "Mine", fApproved: "Approved", fWaiting: "Waiting"}

func (f filter) next(step int) filter {
	for i, x := range filterOrder {
		if x == f {
			return filterOrder[(i+step+len(filterOrder))%len(filterOrder)]
		}
	}
	return fNeedsMe
}

type focus int

const (
	fList focus = iota
	fDetail
)

type mode int

const (
	modeNormal mode = iota
	modeVisual
	modeInsert
	modeSearch
	modeLeader
	modeConfirm
)

func (m mode) String() string {
	return [...]string{"NORMAL", "VISUAL", "INSERT", "SEARCH", "LEADER", "CONFIRM"}[m]
}

type listRow struct {
	header  bool
	bucket  core.Bucket
	repo    string // repo header (group by repo)
	count   int
	needsMe int // repo header: PRs that need you
	item    int // index into Model.items
}

// popup is a modal overlay that receives keys while open.
type popup interface {
	update(m *Model, k tea.KeyMsg) tea.Cmd
	view(m *Model) string
	mode() mode
}

// forwarder popups also receive non-key messages (cursor blink etc).
type forwarder interface {
	forward(m *Model, msg tea.Msg) tea.Cmd
}

type confirm struct {
	prompt string
	yes    func() tea.Cmd
	no     func() tea.Cmd // n or esc; while set, other keys leave the question up. nil: any key cancels
}

type Model struct {
	cfg  config.Config
	core *core.Core

	items    []core.PR
	snoozed  map[string]time.Time
	runs     map[string][]*core.Review // every PR's review runs, newest first
	running  int                       // agents reviewing right now, across PRs
	viewer   string
	lastSync time.Time
	loading  bool
	syncErr  error

	filter      filter
	showSnoozed bool
	rows        []listRow
	list        nav
	focus       focus
	details     map[string]*detail
	previewSeq  int
	previewKey  string

	w, h      int
	frame     int
	animating bool

	// vim state
	mode         mode
	pending      string
	visualAnchor int
	press        press
	search       string
	searchBack   bool
	searchInput  textinput.Model

	popup     popup
	confirm   *confirm
	chats     map[string]*chatSession
	repoFocus string          // show only this repo (owner/name)
	collapsed map[string]bool // folded inbox sections, by groupID
	started   time.Time       // the owl wakes up from here
	synced    bool            // a sync finished this session; the first one's changes are on screen, not notified
	news      string          // news about prowl's own updates under the legend
	tip       string          // a one-time onboarding tip under the legend, for this session
	tipTopic  string          // what tip is about, e.g. "slack names"
	exe       string          // the running prowl's path, for updates
	restart   bool            // quit to start the prowl <leader>U installed
	waking    bool            // the first sync runs and you haven't touched anything: the PR pane shows the owl
	title     string          // the terminal tab title last set
	orgRepos  []string        // every repo in your orgs (owner/name), for <leader>fr
	sticky    string          // the selected PR, kept visible after a sync moved it out of the tab
	fullPR    bool            // focus mode: the PR pane takes the full width

	// mouse: where the popup and the leader menu's entries were last drawn
	popX, popY, popW, popH int
	wkCells                []wkCell
	typeFocus              string // show only this conventional commit type ("other" for non-conventional)
	leaderPrefix           string

	msg    string
	msgErr bool
	msgAt  time.Time

	hits hitMap
}

// leader is how hints show the leader key ("<space>", ","), and
// leaderPress is the key press that opens the action menu.
var leader, leaderPress = "<space>", " "

// applyConfig sets the package-level display settings from cfg.
func applyConfig(cfg config.Config) {
	leader, leaderPress = cfg.Leader, cfg.Leader
	if cfg.Leader == "space" || cfg.Leader == "" {
		leader, leaderPress = "<space>", " "
	}
	applyTheme(cfg.Theme)
	nerdIcons = cfg.NerdFont
	colorRepos = cfg.ColorRepos
	typeColors = cfg.TypeColors
	playful = cfg.Playful
	agentTitle = core.ReviewerTitle(cfg.Agent.Main())
	reviewerOrder = cfg.Agent.Reviewers
	repoTrim = cfg.RepoPrefix
}

// New is the TUI on co, which the caller runs (core.Run) so background
// syncs and review progress reach it.
func New(co *core.Core) *Model {
	cfg := co.Config()
	applyConfig(cfg)
	ti := textinput.New()
	ti.Prompt = ""
	ti.ShowSuggestions = true
	m := &Model{
		cfg: cfg, core: co, loading: true, waking: true, started: time.Now(),
		runs:        map[string][]*core.Review{},
		snoozed:     map[string]time.Time{},
		details:     map[string]*detail{},
		searchInput: ti,
		exe:         core.Executable(),
	}
	m.list.scrolloff = 2
	m.loadRuns()
	if !m.core.Inbox().At.IsZero() {
		m.takeInbox("")
	}

	m.list.skip = func(i int) bool { return m.rows[i].header && !m.collapsed[groupID(m.rows[i])] }
	m.releaseTip()
	return m
}

// ---- messages

type refreshMsg struct {
	changes []core.PR
	err     error
}
type coreEvent core.Event

type animTick struct{}
type previewMsg struct{ seq int }
type errMsg struct{ err error }
type infoMsg string
type submittedMsg struct {
	key    string
	event  core.ReviewEvent
	posted bool   // your review went out, even if err says a reply didn't
	slack  string // what prowl did on the PR's Slack share, if anything
	err    error
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), m.listen(), m.setTitle(), m.loadOrgRepos())
}

func (m *Model) refresh() tea.Cmd {
	m.loading = true
	co := m.core
	return tea.Batch(func() tea.Msg {
		changes, err := co.Refresh(context.Background())
		return refreshMsg{changes, err}
	}, m.startAnim())
}

// reload syncs the inbox and fetches the open PR's page again.
func (m *Model) reload() tea.Cmd {
	var page tea.Cmd
	if d := m.current(); d != nil {
		d.stale = true
		page = d.ensureLoaded(m.core)
	}
	return tea.Batch(m.refresh(), page)
}

// takeInbox shows the inbox core holds now in the list, the open PRs and
// an open finder, whose results point into the list. sel stays selected,
// and visible even when it no longer fits the tab. Read sel before the
// list changes: rows index into it.
func (m *Model) takeInbox(sel string) {
	in := m.core.Inbox()
	m.items, m.snoozed, m.viewer, m.lastSync = in.Items, in.Snoozed, in.Viewer, in.At
	for _, it := range m.items {
		if d := m.details[it.Key]; d != nil {
			d.setItem(it)
		}
	}
	m.sticky = sel
	m.rebuildRowsKeeping(sel)
	if f, ok := m.popup.(*finder); ok {
		f.filter(m)
	}
}

// configChanged applies the config core holds now: display settings, the
// inbox layout and the mouse.
func (m *Model) configChanged() tea.Cmd {
	mouse := m.cfg.Mouse
	m.cfg = m.core.Config()
	applyConfig(m.cfg)
	for _, d := range m.details {
		d.dirty = true
	}
	m.rebuildRows()
	switch {
	case m.cfg.Mouse == mouse:
		return nil
	case m.cfg.Mouse:
		return tea.EnableMouseCellMotion
	}
	return tea.DisableMouse
}

// askIcons asks whether the terminal shows Nerd Font icons, which only the
// person looking can tell, and turns nerd_font on when it does.
func (m *Model) askIcons() {
	m.confirm = &confirm{
		prompt: "Do you see three icons here: \uf09b  \ue725  \uf00c ? Yes turns on Nerd Font icons.",
		yes: func() tea.Cmd {
			if _, err := m.core.Update(func(c *config.Config) error { c.NerdFont = true; return nil }); err != nil {
				m.fail(err.Error())
				return nil
			}
			m.info("Nerd Font icons on")
			return m.configChanged()
		},
		no: func() tea.Cmd {
			m.info("icons stay off; " + leader + ", → Nerd Font icons turns them on")
			return nil
		},
	}
}

// applySync takes the inbox after a sync and notifies what changed, except
// on the first sync, whose changes are on screen already, and while
// prowl watch runs: it sends the notifications then.
func (m *Model) applySync(changes []core.PR, err error) tea.Cmd {
	m.loading, m.waking = false, false
	if err != nil {
		m.syncErr = err
		m.fail("sync failed: " + err.Error())
		return nil
	}
	m.syncErr = nil
	if m.synced && !m.core.WatchRunning() {
		core.Notify(changes, m.cfg.NotifyTimeout.Duration)
	}
	if !m.synced && m.core.FirstTime("service") && !m.core.Service() {
		m.offerTip("service", "tip: <leader>, → Background notifications tells you about PRs while prowl is closed")
	}
	if !m.synced && !m.cfg.NerdFont && m.core.FirstTime("nerd font") {
		m.askIcons()
	}
	var offerNames tea.Cmd
	switch {
	case m.synced:
	case m.core.SlackConnected() && len(m.cfg.SlackChannels) == 0 && m.core.FirstTime("slack channels"):
		m.offerTip("slack channels", "Slack: pick the channels whose PR links you want in the inbox: <leader>, → Slack channels")
	case m.core.SlackConnected() && !m.core.Shown("slack names"):
		offerNames = m.matchSlackNames(namesProposedMsg{tip: true})
	}
	m.synced = true
	if err := m.core.SlackErr(); err != nil {
		msg := err.Error()
		if !strings.HasPrefix(msg, "slack") {
			msg = "slack: " + msg
		}
		m.fail(msg)
	}
	m.loadRuns()
	m.takeInbox(m.selectionKey())
	var reload tea.Cmd
	if d := m.current(); d != nil {
		reload = d.ensureLoaded(m.core)
	}
	return tea.Batch(m.schedulePreview(), reload, m.setTitle(), offerNames)

}

// listen waits for core's next event.
func (m *Model) listen() tea.Cmd {
	co := m.core
	return func() tea.Msg { return coreEvent(<-co.Events()) }
}

// startAnim begins the animation ticker if it isn't running.
func (m *Model) startAnim() tea.Cmd {
	if m.animating {
		return nil
	}
	d := m.animInterval()
	if d == 0 {
		return nil
	}
	m.animating = true
	return tea.Tick(d, func(time.Time) tea.Msg { return animTick{} })
}

// animInterval is how often to redraw: fast for spinners and the waking
// owl, slowly for the sleeping owl's z's, or never (0).
func (m *Model) animInterval() time.Duration {
	switch {
	case m.needsAnim():
		return 90 * time.Millisecond
	case m.owlAsleep():
		return 700 * time.Millisecond
	}
	return 0
}

// owlAsleep is true while the owl sleeps on an empty inbox.
func (m *Model) owlAsleep() bool {
	return playful && !m.loading && len(m.rows) == 0 && m.popup == nil
}

func (m *Model) needsAnim() bool {
	if m.loading {
		return true
	}
	if m.running > 0 || len(m.core.Summarizing()) > 0 || m.core.Updating() != "" {
		return true
	}
	if d := m.current(); d != nil && (d.loadingDet || d.diffLoading) {
		return true
	}
	return false
}

// setTitle updates the terminal tab's title when it changes.
func (m *Model) setTitle() tea.Cmd {
	t := m.windowTitle()
	if t == m.title {
		return nil
	}
	m.title = t
	return tea.SetWindowTitle(t)
}

type orgReposMsg struct{ repos []string }

// loadOrgRepos lists every repo in your orgs for the repo picker.
func (m *Model) loadOrgRepos() tea.Cmd {
	if len(m.cfg.Orgs) == 0 {
		return nil
	}
	co := m.core
	return func() tea.Msg { return orgReposMsg{co.OrgRepos(context.Background())} }
}

type claimedMsg struct {
	key string
	err error
}

// claim requests you as a reviewer on GitHub, for a PR shared without
// asking you (a link in Slack, say).
func (m *Model) claim(it core.PR) tea.Cmd {
	co, key := m.core, it.Key
	m.info("requesting you as reviewer on " + key + "…")
	return func() tea.Msg { return claimedMsg{key, co.Claim(context.Background(), key)} }
}

// toggleWatch adds or removes the current repo (focused, or the selected
// PR's) from watch_repos.
func (m *Model) toggleWatch(it *core.PR) tea.Cmd {
	repo := ""
	switch {
	case it != nil:
		repo = it.Repo
	case m.repoFocus != "":
		repo = m.repoFocus
	}
	if repo == "" {
		m.fail("select a PR or focus a repo to watch it")
		return nil
	}
	on := !slices.Contains(m.cfg.WatchRepos, repo)
	if err := m.core.Watch(repo, on); err != nil {
		m.fail(err.Error())
		return nil
	}
	m.cfg = m.core.Config()
	if on {
		m.info("watching " + repo + ": every open PR shows in All")
	} else {
		m.info("stopped watching " + repo)
	}
	return m.refresh()
}

type prFetchedMsg struct {
	item core.PR
	err  error
}

// fetchPR opens a PR that isn't in the inbox, e.g. a link from Slack.
func (m *Model) fetchPR(ref prRef) tea.Cmd {
	co := m.core
	m.info(fmt.Sprintf("fetching %s#%d…", ref.repo, ref.n))
	return func() tea.Msg {
		it, err := co.Open(context.Background(), ref.repo, ref.n)
		return prFetchedMsg{it, err}
	}
}

// openFetched selects a PR opened by link. It stays through syncs; <leader>R
// claims it so it stays for good.
func (m *Model) openFetched(msg prFetchedMsg) tea.Cmd {
	if msg.err != nil {
		m.fail(msg.err.Error())
		return nil
	}
	key := msg.item.Key
	m.filter = fAll
	m.takeInbox(key)
	m.focus = fDetail
	m.info("opened " + key + " · " + leader + "R requests you as reviewer")
	if it := m.selectedItem(); it != nil {
		return m.detailFor(*it).ensureLoaded(m.core)
	}
	return nil
}

func (m *Model) info(s string) { m.msg, m.msgErr, m.msgAt = oneLine(s), false, time.Now() }
func (m *Model) fail(s string) { m.msg, m.msgErr, m.msgAt = oneLine(s), true, time.Now() }

// oneLine keeps a message to the bottom row: the lines of one with several,
// such as errors.Join's, joined with "; " and without repeats.
func oneLine(s string) string {
	var lines []string
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" && !slices.Contains(lines, l) {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "; ")
}

// ---- update

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		md.r = nil
		m.layoutNavs()
		m.resizeChats()
		return m, nil

	case chatOutMsg:
		if cs := m.chats[msg.key]; cs != nil {
			return m, cs.listen()
		}
		return m, nil

	case chatExitMsg:
		return m, tea.Batch(m.chatExited(msg), m.mouseBack())

	case animTick:
		m.frame++
		if d := m.animInterval(); d > 0 {
			return m, tea.Tick(d, func(time.Time) tea.Msg { return animTick{} })
		}
		m.animating = false
		return m, nil

	case refreshMsg:
		return m, m.applySync(msg.changes, msg.err)

	case coreEvent:
		switch msg.Kind {
		case core.Syncing:
			m.loading = true
			return m, tea.Batch(m.listen(), m.startAnim())
		case core.InboxChanged:
			return m, tea.Batch(m.listen(), m.applySync(msg.Changes, msg.Err))
		case core.RunsChanged:
			m.loadRuns()
		case core.LocalsChanged:
			m.applyPR(prRefreshedMsg{})
		case core.SummaryChanged:
			m.summaryDone(msg.Key, msg.Err)
		case core.AuthChanged:
			if p, ok := m.popup.(*accountsPopup); ok {
				p.loading = false
			}
		case core.ReleaseChanged:
			m.releaseTip()
		case core.ConfigChanged:
			if msg.Err != nil {
				m.fail(msg.Err.Error())
				break
			}
			m.info("reloaded " + config.Path())
			return m, tea.Batch(m.listen(), m.configChanged())
		}
		return m, tea.Batch(m.listen(), m.startAnim())

	case orgReposMsg:
		m.orgRepos = msg.repos
		return m, nil

	case upgradeMsg:
		m.upgraded(msg)
		return m, nil

	case claimedMsg:
		if msg.err != nil {
			m.fail("request reviewer: " + msg.err.Error())
			return m, nil
		}
		m.info("you're a requested reviewer on " + msg.key + " now")
		return m, m.refreshPR(msg.key)

	case summaryMsg:
		m.summaryDone(msg.key, msg.err)
		return m, nil

	case prRefreshedMsg:
		m.applyPR(msg)
		return m, m.schedulePreview()

	case prFetchedMsg:
		return m, m.openFetched(msg)

	case authCheckedMsg:
		if p, ok := m.popup.(*accountsPopup); ok {
			p.loading = false
		}
		if msg.err != nil {
			m.fail(msg.err.Error())
		}
		return m, nil

	case loginDoneMsg:
		if msg.err != nil {
			m.fail(msg.name + " login: " + msg.err.Error())
		} else {
			m.info(msg.name + " login finished")
		}
		if p, ok := m.popup.(*accountsPopup); ok {
			p.loading = true
		}
		check := m.checkAuth()
		if msg.key == "slack" && msg.err == nil {
			check = m.slackSignedIn()
		}
		return m, tea.Batch(check, m.mouseBack())

	case previewMsg:
		if msg.seq != m.previewSeq {
			return m, nil
		}
		if it := m.selectedItem(); it != nil {
			d := m.detailFor(*it)
			m.markSeen(d)
			return m, tea.Batch(d.ensureLoaded(m.core), m.startAnim())
		}
		return m, nil

	case viewedMsg:
		if msg.err != nil {
			if d := m.details[msg.key]; d != nil {
				if msg.viewed {
					d.viewed[msg.path] = "UNVIEWED"
				} else {
					d.viewed[msg.path] = "VIEWED"
				}
				d.dirty = true
			}
			m.fail("couldn't update viewed state: " + msg.err.Error())
		} else if msg.viewed {
			m.info("viewed " + msg.path)
		} else {
			m.info("unviewed " + msg.path)
		}
		return m, nil

	case threadDoneMsg:
		if msg.err != nil {
			m.fail(msg.verb + " failed: " + msg.err.Error())
		} else {
			m.info(msg.verb)
		}
		m.loadRuns()
		var reload tea.Cmd
		if d := m.details[msg.key]; d != nil {
			reload = d.fetch(m.core)
		}
		return m, tea.Batch(reload, m.refreshPR(msg.key))

	case detailMsg:
		if d := m.details[msg.key]; d != nil {
			d.handleDetail(msg)
		}
		return m, nil

	case ciLogMsg:
		if d := m.details[msg.key]; d != nil {
			d.ciLogs[msg.job] = ciLog{step: msg.step, lines: msg.lines, err: msg.err}
			d.dirty = true
		}
		return m, nil

	case diffMsg:
		if d := m.details[msg.key]; d != nil {
			d.handleDiff(msg)
		}
		return m, nil

	case editorDoneMsg:
		return m, m.editorDone(msg)

	case infoMsg:
		m.info(string(msg))
		return m, nil

	case errMsg:
		m.fail(msg.err.Error())
		return m, nil

	case serviceMsg:
		switch {
		case msg.err != nil:
			m.fail("background notifications: " + msg.err.Error())
		case msg.on:
			m.dropTip("service")
			m.info("background notifications on: " + msg.what)
		default:
			m.info("background notifications off")
		}
		return m, nil

	case submittedMsg:
		return m, m.submitted(msg)

	case slackPostedMsg:
		if msg.err != nil {
			m.fail(msg.err.Error())
		} else {
			m.info(msg.done)
		}
		return m, nil

	case namesProposedMsg:
		return m, m.namesProposed(msg)

	case channelsMsg:
		m.channelsRead(msg)
		return m, nil

	case candidatesMsg:
		if msg.err != nil {
			m.fail(msg.err.Error())
			return m, nil
		}
		p := newReviewerPicker(m, msg.item, msg.candidates, msg.suggested)
		m.popup = p
		return m, p.input.Focus()

	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress {
			m.waking = false
		}
		selecting := m.activeNav()
		cmd := m.mouse(msg)
		m.endVisual(selecting)
		return m, cmd

	case tea.KeyMsg:
		return m, m.key(msg)
	}

	if f, ok := m.popup.(forwarder); ok {
		return m, f.forward(m, msg)
	}
	if m.mode == modeSearch {
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

// ---- list

// visibleItem is true for PRs in the current tab and focus, and for the
// sticky PR: the one under the cursor when a sync moved it out of the tab
// stays until you move off it, so the PR pane never switches by itself.
func (m *Model) visibleItem(it core.PR) bool {
	if it.Key == m.sticky {
		return true
	}
	if _, sn := m.snoozed[it.Key]; sn && !m.showSnoozed {
		return false
	}
	if m.repoFocus != "" && it.Repo != m.repoFocus {
		return false
	}
	if m.typeFocus != "" && titleType(it.Title) != m.typeFocus {
		return false
	}
	switch m.filter {
	case fNeedsMe:
		return it.Bucket.NeedsMe()
	case fMine:
		return it.Bucket == core.Mine || it.Bucket == core.Local
	case fApproved:
		return it.Bucket == core.Approved || it.Bucket == core.Recheck || it.Bucket == core.Mine && it.ReviewDecision == "APPROVED"
	case fWaiting:
		return it.Bucket == core.WaitingOnAuthor || it.Bucket == core.TeamRequest || it.Bucket == core.Involved
	}
	return true
}

// tabCount is a tab's count, as "focused/total" while a repo or type is in
// focus so the narrower count can't pass for the whole inbox.
func (m *Model) tabCount(f filter) string {
	n := m.filterCount(f)
	if m.repoFocus == "" && m.typeFocus == "" {
		return strconv.Itoa(n)
	}
	repo, typ := m.repoFocus, m.typeFocus
	m.repoFocus, m.typeFocus = "", ""
	total := m.filterCount(f)
	m.repoFocus, m.typeFocus = repo, typ
	return fmt.Sprintf("%d/%d", n, total)
}

func (m *Model) filterCount(f filter) int {
	saved := m.filter
	m.filter = f
	n := 0
	for _, it := range m.items {
		if m.visibleItem(it) {
			n++
		}
	}
	m.filter = saved
	return n
}

func (m *Model) rebuildRows() { m.rebuildRowsKeeping(m.selectionKey()) }

// selectionKey is selectedKey, or "group:<id>" when the cursor is on a
// folded section header, so a rebuild can put it back.
func (m *Model) selectionKey() string {
	if k := m.selectedKey(); k != "" {
		return k
	}
	if c := m.list.cur; c >= 0 && c < len(m.rows) && m.rows[c].header {
		return "group:" + groupID(m.rows[c])
	}
	return ""
}

// selectedKey is the selected PR's key, or "" (safe while rows are stale).
func (m *Model) selectedKey() string {
	if it := m.selectedItem(); it != nil {
		return it.Key
	}
	return ""
}

// rebuildRowsKeeping rebuilds the inbox rows and keeps selKey selected. The
// key must be read before m.items changes: rows index into m.items.
func (m *Model) rebuildRowsKeeping(selKey string) {
	m.rows = m.rows[:0]
	var idx []int
	for i, it := range m.items {
		if m.visibleItem(it) {
			idx = append(idx, i)
		}
	}
	byRepo := m.cfg.GroupBy == "repo"
	order := m.cfg.Types()

	sort.SliceStable(idx, func(a, b int) bool {
		x, y := m.items[idx[a]], m.items[idx[b]]
		if byRepo && x.Repo != y.Repo {
			return x.Repo < y.Repo
		}
		if x.Bucket != y.Bucket {
			return x.Bucket.Rank() < y.Bucket.Rank()
		}
		if m.cfg.SortByType {
			if rx, ry := core.TypeRank(core.ParseTitle(x.Title), order), core.TypeRank(core.ParseTitle(y.Title), order); rx != ry {
				return rx < ry
			}
		}
		return false
	})
	head := -1
	selGroup := ""
	for k, i := range idx {
		it := m.items[i]
		newGroup := k == 0
		if k > 0 {
			prev := m.items[idx[k-1]]
			if byRepo {
				newGroup = prev.Repo != it.Repo
			} else {
				newGroup = prev.Bucket != it.Bucket
			}
		}
		if newGroup {
			h := listRow{header: true, bucket: it.Bucket}
			if byRepo {
				h.repo = it.RepoName
			}
			m.rows = append(m.rows, h)
			head = len(m.rows) - 1
		}
		h := &m.rows[head]
		h.count++
		if it.Bucket.NeedsMe() {
			h.needsMe++
		}
		if it.Key == selKey {
			selGroup = groupID(*h)
		}
		if !m.collapsed[groupID(*h)] {
			m.rows = append(m.rows, listRow{item: i, bucket: it.Bucket})
		}
	}
	m.layoutNavs()
	for i, r := range m.rows {
		if selKey != "" && !r.header && m.items[r.item].Key == selKey ||
			r.header && selGroup != "" && groupID(r) == selGroup && m.collapsed[selGroup] ||
			r.header && selKey == "group:"+groupID(r) {
			m.list.jump(i)
			return
		}
	}
	m.list.snap(1)
}

// groupID names an inbox section for folding: a status bucket or a repo.
func groupID(r listRow) string {
	if r.repo != "" {
		return "repo:" + r.repo
	}
	return "bucket:" + r.bucket.String()
}

// currentGroup is the section the cursor is in.
func (m *Model) currentGroup() string {
	for i := min(m.list.cur, len(m.rows)-1); i >= 0; i-- {
		if m.rows[i].header {
			return groupID(m.rows[i])
		}
	}
	return ""
}

// toggleGroup folds or unfolds a section (to *set, or the opposite of now)
// and leaves the cursor on its header when folded.
func (m *Model) toggleGroup(id string, set *bool) {
	if id == "" {
		return
	}
	wasFolded := m.collapsed[id]
	fold := !wasFolded
	if set != nil {
		fold = *set
	}
	if m.collapsed == nil {
		m.collapsed = map[string]bool{}
	}
	m.collapsed[id] = fold
	sel := m.selectedKey()
	if fold {
		sel = ""
	}
	m.rebuildRowsKeeping(sel)
	for i, r := range m.rows {
		if r.header && groupID(r) == id {
			if fold {
				m.list.jump(i)
			} else if wasFolded && i+1 < len(m.rows) && !m.rows[i+1].header {
				m.list.jump(i + 1)
			}
			return
		}
	}
}

// listFold handles za/zo/zc/zR/zM in the inbox.
func (m *Model) listFold(key string) {
	t, f := true, false
	switch key {
	case "za":
		m.toggleGroup(m.currentGroup(), nil)
	case "zc":
		m.toggleGroup(m.currentGroup(), &t)
	case "zo":
		m.toggleGroup(m.currentGroup(), &f)
	case "zM", "zR":
		fold := key == "zM"
		cur := m.currentGroup()
		for _, r := range m.rows {
			if r.header {
				if m.collapsed == nil {
					m.collapsed = map[string]bool{}
				}
				m.collapsed[groupID(r)] = fold
			}
		}
		m.toggleGroup(cur, &fold)
	}
}

func (m *Model) selectedItem() *core.PR {
	if m.list.cur < 0 || m.list.cur >= len(m.rows) || m.rows[m.list.cur].header {
		return nil
	}
	i := m.rows[m.list.cur].item
	if i < 0 || i >= len(m.items) {
		return nil
	}
	return &m.items[i]
}

func (m *Model) detailFor(it core.PR) *detail {
	key := it.Key
	d := m.details[key]
	if d == nil {
		d = newDetail(it, m.viewer)
		d.pending = m.core.Pending(key)
		d.setRuns(m.runs[key])
		d.setLogs(m.core.Notes(key))
		d.setSummary(m.core.Summary(key), slices.Contains(m.core.Summarizing(), key))
		m.details[key] = d
		m.layoutNavs()
	}
	return d
}

// markSeen records that a PR is being looked at, remembering when it was
// last seen so older comments render folded.
func (m *Model) markSeen(d *detail) {
	if d.seenLoaded {
		return
	}
	d.seenLoaded = true
	if prev, err := m.core.MarkViewed(d.item.Key); err == nil {
		d.seenAt = prev
		d.dirty = true
	}
}

// current is the detail shown in the right pane.
func (m *Model) current() *detail {
	if it := m.selectedItem(); it != nil {
		return m.details[it.Key]
	}
	return nil
}

func (m *Model) schedulePreview() tea.Cmd {
	if m.sticky != "" && m.selectedKey() != m.sticky {
		m.sticky = ""
		m.rebuildRows()
	}
	it := m.selectedItem()
	if it == nil || it.Key == m.previewKey {
		return nil
	}
	m.previewKey = it.Key
	m.previewSeq++
	seq := m.previewSeq
	if d := m.details[it.Key]; d != nil && d.det != nil {
		return func() tea.Msg { return previewMsg{seq} }
	}
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg { return previewMsg{seq} })
}

// activeNav is the cursor that motions apply to.
func (m *Model) activeNav() *nav {
	if m.focus == fDetail {
		if d := m.current(); d != nil {
			return d.nav()
		}
	}
	return &m.list
}

// endVisual leaves visual mode once the tab or pane it selected in, whose
// nav was selecting, is no longer the one in use.
func (m *Model) endVisual(selecting *nav) {
	if m.mode == modeVisual && m.activeNav() != selecting {
		m.mode = modeNormal
	}
}

// ---- keys

func (m *Model) key(k tea.KeyMsg) tea.Cmd {
	m.waking = false
	key := k.String()
	if key == "ctrl+c" && m.popup == nil && m.mode != modeSearch {
		return tea.Quit
	}
	if m.confirm != nil {
		c := m.confirm
		if c.no != nil && !slices.Contains([]string{"y", "Y", "n", "N", "esc"}, key) {
			return nil
		}
		m.confirm = nil
		m.mode = modeNormal
		if key == "y" || key == "Y" {
			return c.yes()
		}
		if c.no != nil {
			return c.no()
		}
		m.info("cancelled")
		return nil
	}
	if m.popup != nil {
		if p, ok := m.popup.(rowPopup); ok {
			v, _ := p.list(m)
			if m.popup.mode() != modeInsert && v.motion(popupMotion(key)) {
				return nil
			}
		}
		return m.popup.update(m, k)
	}
	switch m.mode {
	case modeSearch:
		return m.searchKey(k)
	case modeLeader:
		m.mode = modeNormal
		menu := leaderEntry(m.leaderPrefix)
		m.leaderPrefix = ""
		if menu.run != nil {
			return menu.run(m, key)
		}
		return m.leader(key)
	}

	if m.pending != "" {
		key = m.pending + key
		m.pending = ""
	} else if strings.Contains("g]z[dy", key) && len(key) == 1 && !(key == "y" && m.mode == modeVisual) {
		m.pending = key
		return nil
	}
	selecting := m.activeNav()
	cmd := m.normal(key)
	m.endVisual(selecting)
	if m.focus == fList {
		return tea.Batch(cmd, m.schedulePreview())
	}
	return cmd
}

// popupMotion is the motion a key makes in a popup's list: g for gg, and
// ctrl+n and ctrl+p as in the pickers.
func popupMotion(key string) string {
	switch key {
	case "g":
		return "gg"
	case "ctrl+n":
		return "j"
	case "ctrl+p":
		return "k"
	}
	return key
}

func (m *Model) normal(key string) tea.Cmd {
	v := m.activeNav()
	d := m.current()
	if key == leaderPress {
		m.mode = modeLeader
		return nil
	}
	if v.motion(key) {
		return nil
	}
	switch key {
	case "0":
		m.focus, m.fullPR = fList, false
		m.layoutNavs()
		return nil
	case "+":
		it := m.selectedItem()
		if it == nil {
			return nil
		}
		m.fullPR = !m.fullPR
		m.focus = fDetail
		m.layoutNavs()
		return m.detailFor(*it).ensureLoaded(m.core)
	case "1":
		if it := m.selectedItem(); it != nil {
			m.focus = fDetail
			return m.detailFor(*it).ensureLoaded(m.core)
		}
		return nil
	case "2", "3", "4", "5", "6", "7", "8", "9":
		i := int(key[0] - '2')
		if m.focus == fList {
			if i < len(filterOrder) {
				m.setFilter(filterOrder[i])
			}
			return nil
		}
		if d != nil && i < len(tabNames) {
			return d.setTab(i, m.core)
		}
		return nil
	case ":":
		m.mode = modeSearch
		m.searchInput.Prompt = ":"
		m.searchInput.SetValue("")
		m.searchInput.SetSuggestions(nil)
		m.searchInput.Width = m.w - 4
		return m.searchInput.Focus()
	case "za", "zo", "zc", "zR", "zM":
		if m.focus == fList {
			m.listFold(key)
			return nil
		}
		if d != nil {
			return m.detailKey(d, key)
		}
	case "?":
		m.popup = &helpPopup{}
		return nil
	case "K":
		return m.openDocs(m.docsTopic())
	case "/":
		m.mode = modeSearch
		m.searchBack = false
		m.searchInput.Prompt = key
		m.searchInput.SetValue("")
		m.searchInput.SetSuggestions(nil)
		m.searchInput.Width = m.w - 4
		return m.searchInput.Focus()
	case "n":
		m.findNext(m.searchBack)
	case "N":
		m.findNext(!m.searchBack)
	case "ctrl+p":
		m.popup = newFinder(m)
	case "enter", "l", "right":
		if m.focus == fList {
			if r := m.rows; m.list.cur >= 0 && m.list.cur < len(r) && r[m.list.cur].header {
				f := false
				m.toggleGroup(groupID(r[m.list.cur]), &f)
				return nil
			}
			if it := m.selectedItem(); it != nil {
				m.focus = fDetail
				return m.detailFor(*it).ensureLoaded(m.core)
			}
		} else if d != nil && d.tab == tabReview && key == "enter" {
			d.toggleIncluded(m)
		}
	case "h", "left", "esc", "q":
		if m.mode == modeVisual {
			m.mode = modeNormal
			return nil
		}
		if m.focus == fDetail {
			m.focus, m.fullPR = fList, false
			m.layoutNavs()
			return nil
		}
		if key == "esc" && (m.repoFocus != "" || m.typeFocus != "") {
			m.repoFocus, m.typeFocus = "", ""
			m.core.Unpeek()
			m.rebuildRows()
			m.info("showing everything")
			return m.schedulePreview()
		}
		if key == "q" {
			return tea.Quit
		}
	case "tab", "shift+tab":
		step := 1
		if key == "shift+tab" {
			step = -1
		}
		if m.focus == fList {
			m.setFilter(m.filter.next(step))
		} else if d != nil {
			return d.setTab((d.tab+step+len(tabNames))%len(tabNames), m.core)
		}
	case "R":
		return m.reload()
	case "o":
		if d := m.current(); d != nil && m.focus == fDetail && d.tab == tabOverview {
			if c := d.checkAtCursor(); c != nil && c.URL != "" {
				return openURL(c.URL)
			}
		}
		if it := m.selectedItem(); it != nil {
			if it.Local() {
				m.info("a local review isn't on GitHub")
				return nil
			}
			return openURL(it.URL)
		}
	case "yy":
		if m.focus == fDetail && d != nil {
			m.yank(d)
		}
	case "ys":
		if it := m.selectedItem(); it != nil {
			return m.copySlack(*it)
		}
	case "yu":
		if it := m.selectedItem(); it != nil && !it.Local() {
			if err := platform.Copy(it.URL); err != nil {
				m.fail(err.Error())
			} else {
				m.info("copied " + it.URL)
			}
		}
	default:
		if m.focus == fDetail && d != nil {
			return m.detailKey(d, key)
		}
	}
	return nil
}

func (m *Model) setFilter(f filter) {
	m.filter = f
	m.sticky = ""
	m.list.cur = 0
	m.rebuildRows()
	m.list.first()
}

// ---- leader (which-key style)

type leaderKey struct {
	key, desc string
	cat       string // column heading in the menu
	write     bool   // touches GitHub
	sub       []leaderKey
	run       func(*Model, string) tea.Cmd // runs a key from sub
}

// leaderEntry is the leader menu's entry for key, or the zero entry.
func leaderEntry(key string) leaderKey {
	i := slices.IndexFunc(leaderKeys, func(k leaderKey) bool { return k.key == key })
	if i < 0 {
		return leaderKey{}
	}
	return leaderKeys[i]
}

var agentKeys = []leaderKey{
	{cat: "Run", key: "c", desc: "review with every reviewer"},
	{cat: "Run", key: "C", desc: "review with one reviewer"},
	{cat: "Run", key: "p", desc: "run a review profile"},
	{cat: "Run", key: "a", desc: "AI summary of the PR"},
	{cat: "Run", key: "d", desc: "discard review"},
	{cat: "Talk to it", key: "i", desc: "chat with the reviewer"},
	{cat: "Talk to it", key: "r", desc: "ask it to revise"},
	{cat: "Talk to it", key: "s", desc: "sync findings from chat"},
	{cat: "Hand off", key: "h", desc: "copy a fix-these prompt"},
}

var findKeys = []leaderKey{
	{cat: "Find", key: "f", desc: "find PR"},
	{cat: "Focus", key: "r", desc: "one repo"},
	{cat: "Focus", key: "t", desc: "one commit type"},
}

var threadKeys = []leaderKey{
	{cat: "Thread under the cursor", key: "r", desc: "resolve", write: true},
	{cat: "Thread under the cursor", key: "u", desc: "unresolve", write: true},
	{cat: "Thread under the cursor", key: "c", desc: "reply", write: true},
}

var leaderKeys = []leaderKey{
	{cat: "Review", key: "a", desc: "approve", write: true},
	{cat: "Review", key: "x", desc: "request changes", write: true},
	{cat: "Review", key: "m", desc: "comment", write: true},
	{cat: "Review", key: "t", desc: "threads", sub: threadKeys, run: (*Model).threadAction},
	{cat: "Review", key: "R", desc: "request me as reviewer", write: true},
	{cat: "Review", key: "n", desc: "comment in the conversation", write: true},
	{cat: "Review", key: "A", desc: "ask for reviews", write: true},
	{cat: "Review", key: "X", desc: "close with a comment", write: true},
	{cat: "Review", key: "D", desc: "draft / ready for review", write: true},
	{cat: "Agent", key: "c", desc: "AI review", sub: agentKeys, run: (*Model).agentAction},
	{cat: "Agent", key: "p", desc: "post the AI review", write: true},
	{cat: "Agent", key: "l", desc: "accounts / log in"},
	{cat: "Inbox", key: "f", desc: "find & focus", sub: findKeys, run: (*Model).findAction},
	{cat: "Inbox", key: "g", desc: "group by repo / status"},
	{cat: "Inbox", key: "s", desc: "snooze"},
	{cat: "Inbox", key: "S", desc: "unsnooze"},
	{cat: "Inbox", key: "z", desc: "show snoozed"},
	{cat: "Inbox", key: "w", desc: "watch / unwatch repo"},
	{cat: "Inbox", key: "r", desc: "refresh"},
	{cat: "Share", key: "y", desc: "post / copy slack line"},
	{cat: "Share", key: "Y", desc: "DM the author"},
	{cat: "Share", key: "o", desc: "open in GitHub"},
	{cat: "Share", key: "O", desc: "open in Slack"},
	{cat: "prowl", key: ",", desc: "options"},
	{cat: "prowl", key: "U", desc: "update prowl"},
	{cat: "prowl", key: "N", desc: "what's new"},
	{cat: "prowl", key: "?", desc: "help"},
	{cat: "prowl", key: "q", desc: "quit"},
}

func (m *Model) leader(key string) tea.Cmd {
	it := m.selectedItem()
	need := func() bool {
		if it == nil {
			m.fail("no PR selected")
			return false
		}
		return true
	}
	onGitHub := func() bool {
		if need() && it.Local() {
			m.fail("a local review isn't on GitHub; open a pull request for " + it.Branch + " first")
			return false
		}
		return it != nil
	}
	if leaderEntry(key).sub != nil {
		m.mode = modeLeader
		m.leaderPrefix = key
		return nil
	}
	switch key {
	case "esc":
		return nil
	case ",":
		m.popup = newOptions(m)
	case leaderPress:
		m.popup = newFinder(m)
	case "g":
		next, err := m.core.Update(func(c *config.Config) error {
			if m.cfg.GroupBy == "repo" {
				c.GroupBy = "status"
			} else {
				c.GroupBy = "repo"
			}
			return nil
		})
		if err != nil {
			m.fail(err.Error())
			return nil
		}
		m.cfg = next
		m.rebuildRows()
		m.info("grouped by " + next.GroupBy)
		return m.schedulePreview()
	case "r":
		return m.reload()
	case "l":
		m.popup = &accountsPopup{loading: true}
		return m.checkAuth()
	case "?":
		m.popup = &helpPopup{}
	case "q":
		return tea.Quit
	case "U":
		return m.upgrade()
	case "N":
		m.whatsNew()
		return nil
	case "z":
		m.showSnoozed = !m.showSnoozed
		m.rebuildRows()
	case "a":
		if onGitHub() {
			return m.askApprove(*it)
		}
	case "x":
		if onGitHub() {
			return m.editReview(*it, m.core.Draft(it.Key, core.EventRequestChanges))
		}
	case "m":
		if onGitHub() {
			return m.editReview(*it, m.core.Draft(it.Key, core.EventComment))
		}
	case "p":
		if onGitHub() {
			return m.postReview(*it)
		}
	case "y":
		if onGitHub() {
			return m.postSlack(*it)
		}
	case "Y":
		if onGitHub() {
			return m.dmAuthor(*it)
		}
	case "o":
		if onGitHub() {
			return openURL(it.URL)
		}
	case "O":
		if !need() {
			return nil
		}
		if it.SlackLink == "" {
			m.info("nobody shared this PR in a Slack channel you watch")
			return nil
		}
		return openURL(it.SlackLink)
	case "s":
		if need() {
			m.popup = newSnooze(*it)
		}
	case "R":
		if onGitHub() {
			return m.claim(*it)
		}
	case "n":
		if onGitHub() {
			return m.editReview(*it, core.Draft{Event: core.EventNote})
		}
	case "A":
		if onGitHub() {
			return m.askForReviews(*it)
		}
	case "X":
		if onGitHub() {
			d := m.core.Draft(it.Key, core.EventClose)
			if action, reason := m.suggestedAction(it.Key); action == "close" {
				d.Body = reason
			}
			return m.editReview(*it, d)
		}
	case "D":
		if onGitHub() {
			if it.Draft {
				co, key := m.core, it.Key
				m.confirm = &confirm{prompt: "mark " + prName(*it) + " ready for review?", yes: func() tea.Cmd {
					return func() tea.Msg {
						return threadDoneMsg{key, "marked ready for review", co.MarkReady(context.Background(), key)}
					}
				}}
				return nil
			}
			d := m.core.Draft(it.Key, core.EventDraft)
			if action, reason := m.suggestedAction(it.Key); action == "draft" {
				d.Body = reason
			}
			return m.editReview(*it, d)
		}
	case "w":
		return m.toggleWatch(it)
	case "S":
		if need() {
			if err := m.core.Unsnooze(it.Key); err != nil {
				m.fail(err.Error())
				return nil
			}
			m.snoozed = m.core.Inbox().Snoozed
			m.rebuildRows()
			m.info("unsnoozed " + it.Key)
		}
	default:
		m.fail("no leader mapping for " + key)
	}
	return nil
}

func (m *Model) findAction(key string) tea.Cmd {
	switch key {
	case "esc":
	case "f":
		m.popup = newFinder(m)
	case "r":
		m.popup = newRepoPicker(m)
	case "t":
		m.popup = newTypePicker(m)
	default:
		m.fail("no find mapping for " + key)
	}
	return nil
}

func (m *Model) agentAction(key string) tea.Cmd {
	if key == "esc" {
		return nil
	}
	it := m.selectedItem()
	if it == nil {
		m.fail("no PR selected")
		return nil
	}
	k := it.Key
	target := m.targetRun(*it)
	switch key {
	case "c":
		return m.startReviews(*it, m.cfg.Agent.Reviewers)
	case "C":
		m.popup = newAgentPicker(m, *it)
		return nil
	case "p":
		m.popup = &profilePicker{item: *it, profiles: m.cfg.Agent.AllProfiles()}
		return nil
	case "a":
		return m.startSummary(*it)
	case "i":
		if target == nil {
			m.fail("no review session to talk to yet; " + leader + "cc starts one")
			return nil
		}
		return m.openChat(*it, target.Reviewer())
	case "r":
		if target == nil || target.Status == core.ReviewRunning {
			m.fail("no finished review to revise yet (" + leader + "cc starts one)")
			return nil
		}
		p := newTextPopup(m, popRefine, *it, nil, "")
		p.agent = target.Reviewer()
		m.popup = p
		return p.ta.Focus()
	case "s":
		if target == nil || target.SessionID == "" {
			m.fail("no review session to sync from yet")
			return nil
		}
		return m.startReview(*it, "", true, target.Reviewer())
	case "h":
		var done []*core.Review
		for _, r := range core.Current(m.runs[k]) {
			if r.Finished() {
				done = append(done, r)
			}
		}
		if len(done) == 0 {
			m.fail("no finished review to hand off")
			return nil
		}
		prompt := core.HandoffPrompt(core.FindingsMarkdown(done, m.core.Notes(k), k, it.HeadOid, false))
		if err := platform.Copy(prompt); err != nil {
			m.fail(err.Error())
			return nil
		}
		m.info("copied a fix-these-findings prompt · paste it into the agent in your checkout, or run `prowl findings --prompt` there")
		return nil
	case "d":
		if target == nil {
			m.fail("no review to discard")
			return nil
		}
		if m.chats[chatKey(k, target.Reviewer())] != nil {
			m.fail("end the chat first")
			return nil
		}
		discard := func() tea.Cmd {
			if err := m.core.Discard(target.ID); err != nil {
				m.fail(err.Error())
				return nil
			}
			m.loadRuns()
			m.takeInbox(m.selectionKey())
			m.info("discarded " + core.ReviewerTitle(target.Reviewer()) + "'s review; its previous one (if any) is current again")
			return nil
		}
		if target.Status == core.ReviewRunning {
			m.confirm = &confirm{prompt: core.ReviewerTitle(target.Reviewer()) + " still shows as reviewing. Discard the run anyway?", yes: discard}
			return nil
		}
		return discard()
	}
	m.fail("no agent mapping for " + key)
	return nil
}

type threadDoneMsg struct {
	key, verb string
	err       error
}

func (m *Model) threadAction(key string) tea.Cmd {
	if key == "esc" {
		return nil
	}
	d := m.current()
	var t *core.Thread
	if d != nil && m.focus == fDetail {
		t = d.threadAtCursor()
	}
	if t == nil {
		m.fail("put the cursor on a review thread (overview or diff) first; ]t jumps to the next one")
		return nil
	}
	co, key2, id := m.core, d.item.Key, t.ID
	switch key {
	case "r":
		if t.Resolved {
			m.info("already resolved")
			return nil
		}
		t.Resolved = true
		d.dirty = true
		return func() tea.Msg {
			return threadDoneMsg{key2, "resolved thread", co.Resolve(context.Background(), id, true)}
		}
	case "u":
		if !t.Resolved {
			m.info("thread isn't resolved")
			return nil
		}
		t.Resolved = false
		d.dirty = true
		return func() tea.Msg {
			return threadDoneMsg{key2, "unresolved thread", co.Resolve(context.Background(), id, false)}
		}
	case "c":
		return m.replyPopup(d, t.ID)
	}
	m.fail("no thread mapping for " + key)
	return nil
}

// ---- search

func (m *Model) searchKey(k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc", "ctrl+c":
		m.mode = modeNormal
		m.searchInput.Blur()
		return nil
	case "enter":
		m.mode = modeNormal
		m.searchInput.Blur()
		q := strings.TrimSpace(m.searchInput.Value())
		if m.searchInput.Prompt == ":" {
			return m.exCommand(q)
		}
		if q != "" {
			m.search = q
		}
		m.findNext(m.searchBack)
		return nil
	}
	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(k)
	if m.searchInput.Prompt == ":" {
		m.searchInput.SetSuggestions(helpSuggestions(m.searchInput.Value()))
	}
	return cmd
}

// helpSuggestions completes a :help topic with the docs' tags, so tab fills
// one in.
func helpSuggestions(q string) []string {
	cmd, _, ok := strings.Cut(q, " ")
	if !ok || (cmd != "h" && cmd != "help") {
		return nil
	}
	var out []string
	for _, t := range docs.Tags() {
		if s := cmd + " " + t.Tag; !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// docsTopic is the docs topic for what the cursor is on: a PR's bucket, or in
// the PR pane its finding, thread, check or tab.
func (m *Model) docsTopic() string {
	if m.focus == fDetail {
		if d := m.current(); d != nil {
			return d.docsTopic()
		}
		return ""
	}
	c := m.list.cur
	if c < 0 || c >= len(m.rows) {
		return ""
	}
	switch r := m.rows[c]; {
	case r.repo != "":
		return "Grouping and focusing"
	case r.header:
		return r.bucket.String()
	default:
		return m.items[r.item].Bucket.String()
	}
}

// exCommand runs a vim-style : command.
func (m *Model) exCommand(q string) tea.Cmd {
	switch {
	case q == "":
		return nil
	case q == "q" || q == "q!" || q == "qa" || q == "qa!":
		return tea.Quit
	case q == "noh" || q == "nohlsearch":
		m.search = ""
		return nil
	}
	if cmd, topic, _ := strings.Cut(q, " "); cmd == "h" || cmd == "help" {
		return m.openDocs(strings.TrimSpace(topic))
	}
	if n, err := strconv.Atoi(q); err == nil {
		m.activeNav().gotoLine(n)
		m.activeNav().align('z')
		return nil
	}
	m.fail("not a command: " + q)
	return nil
}

func (m *Model) findNext(back bool) {
	if m.search == "" {
		return
	}
	q := m.search
	smart := strings.ToLower(q) == q
	if smart {
		q = strings.ToLower(q)
	}
	lines := m.searchLines()
	v := m.activeNav()
	if len(lines) == 0 {
		return
	}
	pos, found := v.cur, false
	for i := 1; i <= len(lines); i++ {
		j := pos + i
		if back {
			j = pos - i
		}
		j = ((j % len(lines)) + len(lines)) % len(lines)
		l := lines[j]
		if smart {
			l = strings.ToLower(l)
		}
		if l != "" && strings.Contains(l, q) && v.selectable(j) {
			pos, found = j, true
			break
		}
	}
	if !found {
		m.fail("pattern not found: " + m.search)
		return
	}
	v.jump(pos)
	v.align('z')
	m.info("/" + m.search)
}

func (m *Model) searchLines() []string {
	if m.focus == fDetail {
		if d := m.current(); d != nil {
			return d.searchLines()
		}
	}
	out := make([]string, len(m.rows))
	for i, r := range m.rows {
		if !r.header {
			it := m.items[r.item]
			out[i] = it.RepoName + " " + it.Title + " " + it.Author + " #" + strconv.Itoa(it.Number)
		}
	}
	return out
}

// ---- actions

func (m *Model) copySlack(it core.PR) tea.Cmd {
	line := m.core.SlackLine(it.Key, "", false)
	if err := platform.Copy(line); err != nil {
		m.fail(err.Error())
		return nil
	}
	m.info("yanked: " + ansi.Strip(line))
	return nil
}

// postSlack posts the Slack line in the thread of the message that shared
// the PR, once you confirm, or copies it when no message did.
func (m *Model) postSlack(it core.PR) tea.Cmd {
	where := m.core.SharedIn(it.Key)
	if where == "" {
		return m.copySlack(it)
	}
	line := m.core.SlackLine(it.Key, "", true)
	co, key := m.core, it.Key
	m.confirm = &confirm{prompt: "post in the " + where + " thread: " + ansi.Strip(line), yes: func() tea.Cmd {
		return func() tea.Msg {
			where, err := co.PostInThread(context.Background(), key, line)
			return slackPostedMsg{done: "posted in the " + where + " thread", err: err}
		}
	}}
	return nil
}

// dmAuthor sends the PR's author its Slack line in a DM, once you confirm.
// An author with no Slack user yet opens the Slack names to pick one.
func (m *Model) dmAuthor(it core.PR) tea.Cmd {
	login := it.Author
	if !m.core.HasSlackUser(login) {
		key := it.Key
		cmd := m.matchSlackNames(namesProposedMsg{focus: login, then: func(m *Model) tea.Cmd {
			i := slices.IndexFunc(m.items, func(it core.PR) bool { return it.Key == key })
			if i < 0 || !m.core.HasSlackUser(login) {
				return nil
			}
			return m.dmAuthor(m.items[i])
		}})
		m.info("no Slack user for " + login + " yet: matching names…")
		return cmd
	}
	line := m.core.SlackLine(it.Key, "", false)
	co := m.core
	m.confirm = &confirm{prompt: "DM " + login + ": " + ansi.Strip(line), yes: func() tea.Cmd {
		return func() tea.Msg {
			return slackPostedMsg{done: "sent " + login + " a DM", err: co.DMAuthor(context.Background(), it.Key)}
		}
	}}
	return nil
}

type slackPostedMsg struct {
	done string
	err  error
}

func (m *Model) askApprove(it core.PR) tea.Cmd {
	d := m.core.Draft(it.Key, core.EventApprove)
	prompt := "approve " + prName(it)
	if len(d.Comments) > 0 {
		prompt += " with " + count(len(d.Comments), "inline comment")
	}
	m.confirm = &confirm{prompt: prompt + "?", yes: func() tea.Cmd { return m.submit(it, d) }}
	return nil
}

func (m *Model) submit(it core.PR, d core.Draft) tea.Cmd {
	co, key := m.core, it.Key
	m.info("submitting " + strings.ToLower(strings.ReplaceAll(string(d.Event), "_", " ")) + "…")
	return func() tea.Msg {
		sent, err := co.Submit(context.Background(), key, d)
		return submittedMsg{key: key, event: d.Event, posted: sent.Review, slack: sent.Slack, err: err}
	}
}

// submitted shows a posted review: the PR in its new section, the findings
// that went out, and what prowl did on the PR's Slack share, or else a Slack
// line on the clipboard.
func (m *Model) submitted(msg submittedMsg) tea.Cmd {
	if dt := m.details[msg.key]; dt != nil {
		dt.pending = m.core.Pending(msg.key)
		dt.dirty = true
	}
	if msg.err != nil && !msg.posted {
		m.fail(msg.err.Error() + " · your review is kept: post it again to reopen it")
		return nil
	}
	m.loadRuns()
	var reload tea.Cmd
	if d := m.details[msg.key]; d != nil {
		d.pending = nil
		d.dirty = true
		reload = d.fetch(m.core)
	}
	m.applyPR(prRefreshedMsg{key: msg.key})
	note := map[core.ReviewEvent]string{core.EventApprove: "approved", core.EventRequestChanges: "requested changes", core.EventComment: "commented",
		core.EventClose: "closed", core.EventDraft: "made it a draft", core.EventNote: "commented in the conversation"}[msg.event]
	if msg.slack != "" {
		note += " · " + msg.slack
	}
	if msg.slack == "" && msg.event != core.EventClose && msg.event != core.EventDraft && msg.event != core.EventNote &&
		slices.ContainsFunc(m.items, func(it core.PR) bool { return it.Key == msg.key }) {
		if err := platform.Copy(m.core.SlackLine(msg.key, msg.event, false)); err == nil {
			note += " · slack line yanked"
		}
	}
	m.info(note)
	if msg.err != nil {
		m.fail(msg.err.Error())
	}
	return tea.Batch(reload, m.refreshPR(msg.key), m.schedulePreview())
}

type summaryMsg struct {
	key string
	err error
}

// startSummary has the main agent write a plain-language summary of a PR.
func (m *Model) startSummary(it core.PR) tea.Cmd {
	key := it.Key
	if slices.Contains(m.core.Summarizing(), key) {
		m.info("already writing the summary of " + key)
		return nil
	}
	if d := m.details[key]; d != nil {
		d.setSummary(m.core.Summary(key), true)
	}
	co := m.core
	return tea.Batch(m.startAnim(), func() tea.Msg {
		_, err := co.Summarize(context.Background(), key)
		return summaryMsg{key, err}
	})
}

// summaryDone shows a summary that was written, or why it wasn't.
func (m *Model) summaryDone(key string, err error) {
	if err != nil {
		m.fail(err.Error())
	}
	if d := m.details[key]; d != nil {
		d.setSummary(m.core.Summary(key), false)
	}
}

type prRefreshedMsg struct {
	key string
	err error
}

// refreshPR re-fetches one PR after you acted on it.
func (m *Model) refreshPR(key string) tea.Cmd {
	co := m.core
	return func() tea.Msg { return prRefreshedMsg{key, co.RefreshPR(context.Background(), key)} }
}

// applyPR shows a re-fetched PR in its new section, or drops it once it's
// merged or closed.
func (m *Model) applyPR(msg prRefreshedMsg) {
	if msg.err != nil {
		return
	}
	m.takeInbox(m.selectionKey())
}

// startReviews runs a review by each reviewer at once.
func (m *Model) startReviews(it core.PR, reviewers []string) tea.Cmd {
	var started, problems []string
	for _, name := range reviewers {
		title, err := m.launchReview(it, "", false, name)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		started = append(started, title)
	}
	switch {
	case len(started) == 0:
		m.fail(strings.Join(problems, " · "))
		return nil
	case len(problems) > 0:
		m.info(strings.Join(started, " and ") + " started · " + strings.Join(problems, " · "))
	case len(started) == 1:
		m.info(started[0] + " review started")
	default:
		m.info(strings.Join(started, " and ") + " are reviewing")
	}
	m.loadRuns()
	return m.startAnim()
}

// startReview runs one reviewer: an agent ("" = the main one), or
// agent:profile. A revise or sync continues that reviewer's current run.
func (m *Model) startReview(it core.PR, refine string, sync bool, reviewer string) tea.Cmd {
	title, err := m.launchReview(it, refine, sync, reviewer)
	if err != nil {
		m.fail(err.Error())
		return nil
	}
	switch {
	case sync:
		m.info("updating " + title + "'s review from your chat")
	case refine != "":
		m.info(title + " is revising its review")
	default:
		m.info(title + " review started")
	}
	m.loadRuns()
	return m.startAnim()
}

// startProfile runs a review profile with its reviewers (the main agent when
// it names none).
func (m *Model) startProfile(it core.PR, p config.Profile) tea.Cmd {
	agents := p.Reviewers
	if len(agents) == 0 {
		agents = []string{m.cfg.Agent.Main()}
	}
	var reviewers []string
	for _, name := range agents {
		reviewers = append(reviewers, name+":"+p.Name)
	}
	return m.startReviews(it, reviewers)
}

// launchReview starts one reviewer (agent or agent:profile; "" is the main
// agent), revising its current run when refine is set.
func (m *Model) launchReview(it core.PR, refine string, sync bool, reviewer string) (string, error) {
	key := it.Key
	if cs := m.chats[chatKey(key, reviewer)]; cs != nil {
		return "", fmt.Errorf("the %s chat for this PR is still open; end it (%s) first so the session isn't used twice", core.ReviewerTitle(reviewer), cs.quit)
	}
	var started string
	var err error
	switch {
	case sync:
		started, err = m.core.SyncChat(key, reviewer)
	case refine != "":
		started, err = m.core.Revise(key, reviewer, refine)
	default:
		started, err = m.core.Review(key, reviewer)
	}
	if err != nil {
		return "", err
	}
	return core.ReviewerTitle(started), nil
}

// loadRuns takes every review run, and the open PR's notes, from core.
func (m *Model) loadRuns() {
	m.runs = m.core.Runs()
	m.running = 0
	for _, runs := range m.runs {
		for _, r := range core.Current(runs) {
			if r.Status == core.ReviewRunning {
				m.running++
			}
		}
	}
	for k, d := range m.details {
		d.setRuns(m.runs[k])
	}
	if d := m.current(); d != nil {
		d.setLogs(m.core.Notes(d.item.Key))
	}
}

// targetRun is the review an agent action applies to: the agent whose
// section the cursor is in on the AI review tab, else the main reviewer's,
// else whichever agent has reviewed the PR.
func (m *Model) targetRun(it core.PR) *core.Review {
	runs := m.runs[it.Key]
	if d := m.details[it.Key]; d != nil && d.tab == tabReview && m.focus == fDetail {
		v := &d.navs[tabReview]
		if rows := d.rows[tabReview]; v.cur < len(rows) && rows[v.cur].agent != "" {
			return core.Latest(runs, rows[v.cur].agent)
		}
	}
	if r := core.Latest(runs, m.cfg.Agent.Main()); r != nil {
		return r
	}
	if cur := core.Current(runs); len(cur) > 0 {
		return cur[0]
	}
	return nil
}

// postReview opens one GitHub review in your editor made from every agent's
// included findings.
func (m *Model) postReview(it core.PR) tea.Cmd {
	d, err := m.core.AgentDraft(it.Key)
	if err != nil {
		m.fail(err.Error())
		return nil
	}
	return m.editReview(it, d)
}

func openURL(u string) tea.Cmd {
	return func() tea.Msg {
		if err := platform.Open(u); err != nil {
			return errMsg{err}
		}
		return infoMsg("opened " + u)
	}
}
