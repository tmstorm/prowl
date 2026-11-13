// Package core is prowl without a UI: it syncs and classifies the inbox,
// acts on pull requests and runs agent reviews. The TUI and `prowl watch`
// are clients of it.
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/inbox"
	"github.com/tmstorm/prowl/internal/platform"
	"github.com/tmstorm/prowl/internal/store"
)

// Core is safe for use from several goroutines. Calls to GitHub never hold
// its lock.
type Core struct {
	gh *gh.Client
	st *store.Store

	syncing sync.Mutex // one sync at a time

	mu       sync.Mutex
	cfg      config.Config
	cfgMod   time.Time // the config file's mtime when last read or written
	quiet    bool      // this process never notifies (prowl list)
	accounts []Account
	inbox    Inbox
	peek     map[string]bool     // repos (owner/name) whose open PRs are fetched this session
	opened   map[string]prNumber // PRs opened by link this session, kept through syncs
	runs     map[string][]*store.AgentReview
	pending  map[string][]gh.DraftComment // your inline comments, by PR, until the next review
	drafted  map[string]store.Review      // agent drafts on their way out, as prowl wrote them
	unsent   map[string]Draft             // reviews that failed to post, by PR, until reopened

	updateStep  string    // what a running update is doing, "" when none runs
	updateRetry time.Time // a failed background install waits until then to try again

	serviceOnce sync.Once
	service     bool // prowl watch runs at login

	summaries     map[string]store.Summary
	summarizing   map[string]bool
	summaryFailed map[string]string // PR -> the head commit its summary failed on

	events chan Event
}

type prNumber struct {
	repo string
	n    int
}

// Inbox is the classified inbox as of the last sync.
type Inbox struct {
	Items   []inbox.Item
	Snoozed map[string]time.Time
	Viewer  string
	Teams   []string // your teams, for classifying a single PR later
	At      time.Time
}

// New starts from the inbox saved by the last sync, if any, so a UI can
// draw it at once while the first sync runs.
func New(cfg config.Config, c *gh.Client, st *store.Store) *Core {
	co := &Core{gh: c, st: st, cfg: cfg, cfgMod: config.ModTime(), accounts: accountList(cfg), events: make(chan Event, 64)}
	co.loadRuns()
	co.summaries = map[string]store.Summary{}
	if sums, err := st.Summaries(); err == nil {
		for key, sum := range sums {
			co.summaries[key] = *sum
		}
	}
	data, at, err := st.LastInbox()
	if err != nil || data == nil {
		return co
	}
	var in gh.Inbox
	if json.Unmarshal(data, &in) != nil {
		return co
	}
	items := co.classify(&in)
	snoozed, _ := st.Snoozed(heads(items))
	co.inbox = Inbox{Items: items, Snoozed: snoozed, Viewer: in.Viewer, Teams: in.Teams, At: at}
	co.inbox.Items = co.withLocals(items)
	return co
}

// SetConfig replaces the config without saving it. Update saves.
func (co *Core) SetConfig(cfg config.Config) {
	accts := accountList(cfg)
	co.mu.Lock()
	defer co.mu.Unlock()
	for i := range accts {
		for _, old := range co.accounts {
			if old.Key == accts[i].Key {
				accts[i].Auth = old.Auth
			}
		}
	}
	co.cfg, co.accounts = cfg, accts
}

// Inbox returns a copy of the current inbox.
func (co *Core) Inbox() Inbox {
	co.mu.Lock()
	defer co.mu.Unlock()
	in := co.inbox
	in.Items = slices.Clone(in.Items)
	in.Snoozed = maps.Clone(in.Snoozed)
	in.Teams = slices.Clone(in.Teams)
	return in
}

// Peek fetches every open PR of repo on each sync this session, as if it
// were watched.
func (co *Core) Peek(repo string) {
	co.mu.Lock()
	defer co.mu.Unlock()
	if co.peek == nil {
		co.peek = map[string]bool{}
	}
	co.peek[repo] = true
}

// Unpeek stops fetching the repos Peek added.
func (co *Core) Unpeek() {
	co.mu.Lock()
	defer co.mu.Unlock()
	co.peek = nil
}

// Open fetches a PR outside the inbox (a link from Slack, say) and keeps it
// in the inbox for the rest of the session.
func (co *Core) Open(repo string, n int) (inbox.Item, error) {
	pr, err := co.gh.PR(repo, n)
	if err != nil {
		return inbox.Item{}, fmt.Errorf("open %s#%d: %w", repo, n, err)
	}
	co.mu.Lock()
	defer co.mu.Unlock()
	if co.opened == nil {
		co.opened = map[string]prNumber{}
	}
	co.opened[pr.Key()] = prNumber{repo, n}
	co.inbox.Items = co.withOpened(co.inbox.Items, []gh.PR{*pr}, co.inbox.Viewer)
	if it, ok := co.item(pr.Key()); ok {
		return it, nil
	}
	return inbox.Item{}, fmt.Errorf("open %s#%d: not classified", repo, n)
}

// Refresh syncs the inbox with GitHub and returns what changed since the
// last sync that notified, whenever that was (nothing in a process that
// doesn't notify): PRs that newly need you, moved section, or
// were pushed to while addressed, and your own PRs that moved. Snoozed PRs
// are left out. Before the first sync ever, every such PR is a change.
func (co *Core) Refresh() ([]inbox.Item, error) {
	co.syncing.Lock()
	defer co.syncing.Unlock()
	co.mu.Lock()
	cfg := co.cfg
	cfg.WatchRepos = slices.Clone(cfg.WatchRepos)
	for repo := range co.peek {
		if !slices.Contains(cfg.WatchRepos, repo) {
			cfg.WatchRepos = append(cfg.WatchRepos, repo)
		}
	}
	opened := slices.Collect(maps.Values(co.opened))
	co.mu.Unlock()

	in, err := co.gh.FetchInbox(cfg.Orgs, cfg.WatchRepos)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	if data, err := json.Marshal(in); err == nil {
		_ = co.st.SaveInbox(data, time.Now())
	}
	items := co.classify(in)
	var changes []inbox.Item
	if co.notifies() {
		prev, err := co.st.AllStates()
		if err != nil {
			return nil, fmt.Errorf("refresh: %w", err)
		}
		changes = changed(items, prev)
		if err := co.st.ReplaceStates(states(items)); err != nil {
			return nil, fmt.Errorf("refresh: %w", err)
		}
	}
	open := heads(items)
	co.pruneClosed(open)
	snoozed, err := co.st.Snoozed(open)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	var extra []gh.PR
	for _, o := range opened {
		if pr, err := co.gh.PR(o.repo, o.n); err == nil && pr.State == "OPEN" {
			extra = append(extra, *pr)
		}
	}

	co.pruneLocals()
	co.foldLocals(items, in.Viewer)
	co.mu.Lock()
	co.inbox = Inbox{Items: co.withOpened(items, extra, in.Viewer), Snoozed: snoozed, Viewer: in.Viewer, Teams: in.Teams, At: time.Now()}
	co.inbox.Items = co.withLocals(co.inbox.Items)
	co.mu.Unlock()
	return slices.DeleteFunc(changes, func(it inbox.Item) bool { _, ok := snoozed[it.PR.Key()]; return ok }), nil
}

func (co *Core) classify(in *gh.Inbox) []inbox.Item {
	co.mu.Lock()
	cfg := co.cfg
	co.mu.Unlock()
	in.Bots, in.RecheckAfterPush = bots(cfg), cfg.RecheckAfterPush
	return inbox.Classify(in)
}

// withOpened adds PRs opened by link to items, unless a sync already
// brought them in. co.mu is held.
func (co *Core) withOpened(items []inbox.Item, prs []gh.PR, viewer string) []inbox.Item {
	have := map[string]bool{}
	for _, it := range items {
		have[it.PR.Key()] = true
	}
	var add []gh.PR
	for _, pr := range prs {
		if !have[pr.Key()] {
			add = append(add, pr)
		}
	}
	if len(add) == 0 {
		return items
	}
	extra := &gh.Inbox{Viewer: viewer, PRs: add, Bots: bots(co.cfg), RecheckAfterPush: co.cfg.RecheckAfterPush}
	return append(items, inbox.Classify(extra)...)
}

// changed lists the PRs that need you (or are yours) and are new, moved
// section, or were pushed to while addressed.
func changed(items []inbox.Item, prev map[string]store.PRState) []inbox.Item {
	var out []inbox.Item
	for _, it := range items {
		if !it.Bucket.NeedsMe() && it.Bucket != inbox.Mine {
			continue
		}
		p, seen := prev[it.PR.Key()]
		if !seen || p.Bucket != int(it.Bucket) || p.Reason != it.Reason ||
			(it.Bucket == inbox.Addressed && p.HeadOid != it.PR.HeadRefOid) {
			out = append(out, it)
		}
	}
	return out
}

func states(items []inbox.Item) []store.PRState {
	out := make([]store.PRState, 0, len(items))
	for _, it := range items {
		out = append(out, store.PRState{
			Key: it.PR.Key(), Bucket: int(it.Bucket), Reason: it.Reason,
			HeadOid: it.PR.HeadRefOid, Title: it.PR.Title, URL: it.PR.URL,
		})
	}
	return out
}

func heads(items []inbox.Item) map[string]string {
	out := map[string]string{}
	for _, it := range items {
		out[it.PR.Key()] = it.PR.HeadRefOid
	}
	return out
}

// Notify sends a desktop notification per change, or one summary when there
// are more than a few (the first sync ever, or catching up after a while).
func Notify(changes []inbox.Item, timeout time.Duration) {
	changes = slices.DeleteFunc(slices.Clone(changes), func(it inbox.Item) bool {
		return it.Bucket == inbox.Mine && it.Reason != "new feedback on your PR" && it.Reason != "approved, ready to merge"
	})
	if len(changes) > 3 {
		need := 0
		for _, it := range changes {
			if it.Bucket.NeedsMe() {
				need++
			}
		}
		platform.Notify("prowl", fmt.Sprintf("%d updates, %d PRs need you", len(changes), need), timeout)
		return
	}
	for _, it := range changes {
		title := fmt.Sprintf("%s: %s#%d", it.Bucket, it.PR.Repository.Name, it.PR.Number)
		platform.Notify(title, fmt.Sprintf("%s — %s\n%s", it.PR.AuthorLogin(), it.PR.Title, it.Reason), timeout)
	}
}

// pruneClosed deletes the review history and worktrees of PRs that GitHub
// says are merged or closed. A PR missing from the inbox isn't enough: it may
// have been opened by link, or be in a repo only peeked at.
func (co *Core) pruneClosed(open map[string]string) {
	keys, err := co.st.ReviewedPRs()
	if err != nil {
		return
	}
	for _, key := range keys {
		if _, ok := open[key]; ok {
			continue
		}
		repo, n, err := parseKey(key)
		if err != nil {
			continue
		}
		if pr, err := co.gh.PR(repo, n); err == nil && pr.State != "OPEN" {
			_ = co.st.DeleteReviews(key)
			agent.RemoveWorktrees(repo, n)
		}
	}
}

func bots(cfg config.Config) map[string]bool {
	out := map[string]bool{}
	for _, b := range cfg.Bots {
		out[b] = true
	}
	return out
}

// RefreshPR re-fetches one PR after you acted on it, so its section updates
// in about a second rather than at the next sync, which also lags behind
// GitHub by a few seconds. A merged or closed PR leaves the inbox.
func (co *Core) RefreshPR(key string) error {
	repo, n, err := parseKey(key)
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	pr, err := co.gh.PR(repo, n)
	if err != nil {
		return fmt.Errorf("refresh %s: %w", key, err)
	}
	co.mu.Lock()
	defer co.mu.Unlock()
	i := slices.IndexFunc(co.inbox.Items, func(it inbox.Item) bool { return it.PR.Key() == key })
	if i < 0 {
		return nil
	}
	if pr.State != "OPEN" {
		co.inbox.Items = slices.Delete(co.inbox.Items, i, i+1)
		return nil
	}
	watched := map[string]bool{}
	for _, r := range co.cfg.WatchRepos {
		watched[r] = true
	}
	for r := range co.peek {
		watched[r] = true
	}
	in := &gh.Inbox{Viewer: co.inbox.Viewer, Teams: co.inbox.Teams, Watched: watched, Bots: bots(co.cfg),
		RecheckAfterPush: co.cfg.RecheckAfterPush, PRs: []gh.PR{*pr}}
	if items := inbox.Classify(in); len(items) == 1 {
		co.inbox.Items[i] = items[0]
	}
	return nil
}

// ErrWatching is Watching's answer while another `prowl watch` runs.
var ErrWatching = errors.New("prowl watch is already running")

// Watching records that this process is `prowl watch`, which sends the
// desktop notifications for everyone. Only one runs: with another alive, it
// returns ErrWatching.
func (co *Core) Watching() error {
	if co.WatchRunning() {
		return fmt.Errorf("%w (process %d)", ErrWatching, co.st.Watcher())
	}
	co.mu.Lock()
	co.quiet = false
	co.mu.Unlock()
	return co.st.SetWatcher(os.Getpid())
}

// StopWatching clears this process's record as `prowl watch`, so the prowl
// it starts to restart on an update isn't turned away while this one exits.
func (co *Core) StopWatching() {
	_ = co.st.SetWatcher(0)
}

// Quiet marks this process as one that never notifies, like `prowl list`.
func (co *Core) Quiet() {
	co.mu.Lock()
	defer co.mu.Unlock()
	co.quiet = true
}

// notifies reports whether this process tells you about changes: `prowl
// watch` while it runs, otherwise a UI. Only that process measures changes
// against the stored snapshot and moves it on, so another process's sync
// can't swallow a change before it is notified.
func (co *Core) notifies() bool {
	co.mu.Lock()
	quiet := co.quiet
	co.mu.Unlock()
	return !quiet && !co.WatchRunning()
}

// WatchRunning reports whether another process is `prowl watch`. A UI then
// leaves desktop notifications to it, so none arrive twice.
func (co *Core) WatchRunning() bool {
	pid := co.st.Watcher()
	return pid != 0 && pid != os.Getpid() && platform.Alive(pid)
}

// Config is the config core works with.
func (co *Core) Config() config.Config {
	co.mu.Lock()
	defer co.mu.Unlock()
	return co.cfg
}

// Watch adds repo (owner/name) to watch_repos, or removes it, and saves the
// config. The next Refresh fetches every open PR in a watched repo.
func (co *Core) Watch(repo string, on bool) error {
	_, err := co.Update(func(c *config.Config) error {
		c.WatchRepos = slices.DeleteFunc(c.WatchRepos, func(r string) bool { return r == repo })
		if on {
			c.WatchRepos = append(c.WatchRepos, repo)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("watch %s: %w", repo, err)
	}
	return nil
}

// Snooze hides a PR for d, or until its author pushes.
func (co *Core) Snooze(key string, d time.Duration) error {
	co.mu.Lock()
	defer co.mu.Unlock()
	it, ok := co.item(key)
	if !ok {
		return fmt.Errorf("snooze %s: not in the inbox", key)
	}
	if err := co.st.Snooze(key, it.PR.HeadRefOid, d); err != nil {
		return err
	}
	if co.inbox.Snoozed == nil {
		co.inbox.Snoozed = map[string]time.Time{}
	}
	co.inbox.Snoozed[key] = time.Now().Add(d)
	return nil
}

// Unsnooze brings a snoozed PR back.
func (co *Core) Unsnooze(key string) error {
	co.mu.Lock()
	defer co.mu.Unlock()
	if err := co.st.Unsnooze(key); err != nil {
		return err
	}
	delete(co.inbox.Snoozed, key)
	return nil
}

// MarkViewed records that you are looking at a PR and returns when you
// last did (zero if never), so a UI can fold what you have already read.
func (co *Core) MarkViewed(key string) (time.Time, error) {
	return co.st.MarkViewed(key)
}

// RequestReviews asks people and teams to review a PR, or to review it
// again. Each name is a login or org/team.
func (co *Core) RequestReviews(key string, names []string) error {
	repo, n, err := parseKey(key)
	if err != nil {
		return fmt.Errorf("request reviews: %w", err)
	}
	var logins, teams []string
	for _, name := range names {
		name = strings.TrimPrefix(strings.TrimSpace(name), "@")
		switch _, team, ok := strings.Cut(name, "/"); {
		case name == "":
		case ok:
			teams = append(teams, team)
		default:
			logins = append(logins, name)
		}
	}
	if len(logins)+len(teams) == 0 {
		return fmt.Errorf("request reviews on %s: name someone", key)
	}
	if err := co.gh.RequestReviewers(repo, n, logins, teams); err != nil {
		return fmt.Errorf("request reviews on %s: %w", key, err)
	}
	return co.RefreshPR(key)
}

// Claim requests you as a reviewer on GitHub, for a PR shared without
// asking you (a link in Slack, say), and moves it to its new section.
func (co *Core) Claim(key string) error {
	repo, n, err := parseKey(key)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	co.mu.Lock()
	me := co.inbox.Viewer
	co.mu.Unlock()
	if err := co.gh.RequestReviewers(repo, n, []string{me}, nil); err != nil {
		return fmt.Errorf("claim %s: %w", key, err)
	}
	return co.RefreshPR(key)
}

// parseKey splits a PR key (owner/name#number).
func parseKey(key string) (repo string, n int, err error) {
	repo, num, _ := strings.Cut(key, "#")
	if n, err = strconv.Atoi(num); err != nil {
		return "", 0, fmt.Errorf("bad PR key %q: %w", key, err)
	}
	return repo, n, nil
}

// item is the inbox item for key. co.mu is held.
func (co *Core) item(key string) (inbox.Item, bool) {
	i := slices.IndexFunc(co.inbox.Items, func(it inbox.Item) bool { return it.PR.Key() == key })
	if i < 0 {
		return inbox.Item{}, false
	}
	return co.inbox.Items[i], true
}

// Detail fetches what a PR page shows: the description, activity, review
// threads and changed files.
func (co *Core) Detail(key string) (*gh.Detail, error) {
	if l, err := co.local(key); err == nil {
		return localDetail(l), nil
	}
	repo, n, err := parseKey(key)
	if err != nil {
		return nil, fmt.Errorf("detail: %w", err)
	}
	det, err := co.gh.FetchDetail(repo, n)
	if err != nil {
		return nil, fmt.Errorf("detail %s: %w", key, err)
	}
	return det, nil
}

// Diff fetches a PR's diff, or with sinceReview only what changed since
// your last review.
func (co *Core) Diff(key string, sinceReview bool) (string, error) {
	if l, err := co.local(key); err == nil {
		return localDiff(l), nil
	}
	co.mu.Lock()
	it, ok := co.item(key)
	co.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("diff %s: not in the inbox", key)
	}
	pr := it.PR
	var diff string
	var err error
	if sinceReview {
		diff, err = co.gh.CompareDiff(pr.Repository.NameWithOwner, it.LastReviewedOid, pr.HeadRefOid)
	} else {
		diff, err = co.gh.PRDiff(pr.Repository.NameWithOwner, pr.Number)
	}
	if err != nil {
		return "", fmt.Errorf("diff %s: %w", key, err)
	}
	return diff, nil
}

// checkLogLines is how much of a failed job's log CheckLog returns.
const checkLogLines = 40

// CheckLog is the end of a failed GitHub Actions check's log, and the step
// that failed, for a check from Detail with a JobID.
func (co *Core) CheckLog(key string, jobID int64) (step string, lines []string, err error) {
	repo, _, err := parseKey(key)
	if err != nil {
		return "", nil, fmt.Errorf("check log: %w", err)
	}
	return co.gh.JobLogTail(repo, jobID, checkLogLines)
}

// SetFileViewed ticks or clears GitHub's "Viewed" box on a file of a PR.
func (co *Core) SetFileViewed(key, path string, viewed bool) error {
	co.mu.Lock()
	it, ok := co.item(key)
	co.mu.Unlock()
	if !ok {
		return fmt.Errorf("mark %s viewed: %s is not in the inbox", path, key)
	}
	return co.gh.SetFileViewed(it.PR.ID, path, viewed)
}

// Reply answers a review thread.
func (co *Core) Reply(threadID, body string) error { return co.gh.ReplyToThread(threadID, body) }

// Resolve resolves a review thread, or reopens it.
func (co *Core) Resolve(threadID string, resolved bool) error {
	if resolved {
		return co.gh.ResolveThread(threadID)
	}
	return co.gh.UnresolveThread(threadID)
}

// OrgRepos lists every repo (owner/name) in your orgs, e.g. to pick one to
// peek at. A failing org is skipped.
func (co *Core) OrgRepos() []string {
	var all []string
	for _, org := range co.Config().Orgs {
		if repos, err := co.gh.OrgRepos(org); err == nil {
			all = append(all, repos...)
		}
	}
	return all
}
