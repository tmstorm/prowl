// Package core is prowl without a UI: it syncs and classifies the inbox,
// acts on pull requests and runs agent reviews. The TUI and `prowl watch`
// are clients of it.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
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
	"github.com/tmstorm/prowl/internal/slack"
	"github.com/tmstorm/prowl/internal/store"
)

// Core is safe for use from several goroutines. Calls to GitHub never hold
// its lock.
type Core struct {
	gh *gh.Client
	st *store.Store

	syncing sync.Mutex // one sync at a time

	mu          sync.Mutex
	cfg         config.Config
	cfgMod      time.Time // the config file's mtime when last read or written
	quiet       bool      // this process never notifies (prowl list)
	accounts    []Account
	inbox       classified
	peek        map[string]bool  // repos (owner/name) whose open PRs are fetched this session
	opened      map[string]prRef // PRs opened by link this session, kept through syncs
	runs        map[string][]*Review
	extJobs     map[string][]ExtensionJob   // each PR's newest job of each on-demand extension
	pending     map[string][]DraftComment   // your inline comments, by PR, until the next review
	drafted     map[string]store.Review     // agent drafts on their way out, as prowl wrote them
	unsent      map[string]Draft            // reviews that failed to post, by PR, until reopened
	shared      map[string]inbox.Share      // PRs shared in Slack, as of the last sync
	reactions   map[string]string           // Slack reactions on your PRs, as of the last sync
	slackLinks  map[string]string           // the Slack message that shared each PR, as of the last sync
	slackShares map[string]store.SlackShare // each shared PR's newest share by someone else
	candidates  map[string][]Candidate      // who can review, by repo, for the session

	slack      *slack.Client     // nil when you aren't signed in to Slack
	slackErr   error             // why the last sync couldn't read Slack
	slackMe    string            // your Slack user ID, once known; set under mu by a sync
	slackUsers []slack.User      // the workspace's people, read once
	slackWho   string            // who you are signed in as, e.g. "sam in Acme"
	slackTeam  string            // your Slack workspace ID
	slackApp   bool              // the Slack app opens slack:// links
	activity   []ChannelActivity // your channels and their PR links, read once
	dmLinks    int               // PR links that reached you in DMs, with activity

	updateStep  string    // what a running update is doing, "" when none runs
	updateRetry time.Time // a failed background install waits until then to try again

	serviceOnce sync.Once
	service     bool           // prowl watch runs at login
	watching    *platform.Lock // held while this process is prowl watch

	summaries     map[string]store.Summary
	summarizing   map[string]bool
	summaryFailed map[string]string // PR -> the head commit its summary failed on

	events chan Event
}

type prRef struct {
	repo string
	n    int
}

// Inbox is the classified inbox as of the last sync.
type Inbox struct {
	Items   []PR
	Snoozed map[string]time.Time
	Viewer  string
	At      time.Time
}

// synced is a sync's answer as it is saved: GitHub's inbox, and what Slack
// says about its PRs.
type synced struct {
	gh.Inbox
	Shared     map[string]inbox.Share
	Reactions  map[string]string
	SlackLinks map[string]string
}

// classified is the inbox as core keeps it.
type classified struct {
	Items   []inbox.Item
	Snoozed map[string]time.Time
	Viewer  string
	Teams   []string // your teams, for classifying a single PR later
	At      time.Time
}

// Bucket is an inbox section. It is the classifier's own type, with its
// order and names.
type Bucket = inbox.Bucket

const (
	NeedsReview     = inbox.NeedsReview
	ReReview        = inbox.ReReview
	Addressed       = inbox.Addressed
	Activity        = inbox.Activity
	TeamRequest     = inbox.TeamRequest
	Mine            = inbox.Mine
	WaitingOnAuthor = inbox.WaitingOnAuthor
	Approved        = inbox.Approved
	Involved        = inbox.Involved
	ReApprove       = inbox.ReApprove
	Unclaimed       = inbox.Unclaimed
	Watching        = inbox.Watching
	Recheck         = inbox.Recheck
	Local           = inbox.Local
	Shared          = inbox.Shared
)

// Why is the reason a PR is in its bucket, as a code: compare it, not
// Reason, whose wording can change.
type Why = inbox.Why

// The reasons a PR is in its bucket.
const (
	WhyReviewRequested        = inbox.ReviewRequested
	WhyTeamReviewRequested    = inbox.TeamReviewRequested
	WhyAskedAgain             = inbox.AskedAgain
	WhyReRequested            = inbox.ReRequested
	WhyReviewDismissed        = inbox.ReviewDismissed
	WhyChangeRequestDismissed = inbox.ChangeRequestDismissed
	WhyAuthorPushed           = inbox.AuthorPushed
	WhyAuthorReplied          = inbox.AuthorReplied
	WhyCommented              = inbox.Commented
	WhyRepliedInThread        = inbox.RepliedInThread
	WhyApprovalStands         = inbox.ApprovalStands
	WhyPushedSinceApproval    = inbox.PushedSinceApproval
	WhyYouRequestedChanges    = inbox.YouRequestedChanges
	WhyYouCommented           = inbox.YouCommented
	WhyYouApproved            = inbox.YouApproved
	WhyDraft                  = inbox.Draft
	WhyNobodyAsked            = inbox.NobodyAsked
	WhyWithOthers             = inbox.WithOthers
	WhyMentioned              = inbox.Mentioned
	WhyNewFeedback            = inbox.NewFeedback
	WhyReadyToMerge           = inbox.ReadyToMerge
	WhyChangesRequested       = inbox.ChangesRequested
	WhyAwaitingReviewers      = inbox.AwaitingReviewers
	WhySharedInChannel        = inbox.SharedInChannel
	WhySentToYou              = inbox.SentToYou
	WhyLocalReview            = inbox.LocalReview
)

// PR is a pull request in the inbox, or a local branch reviewed before it
// has one, with where it sits in the inbox and why.
type PR struct {
	Key            string
	Repo, RepoName string // owner/name, and name alone
	Number         int    // 0 for a local review
	Title, Body    string
	URL            string
	Author         string
	Branch, Base   string
	HeadOid        string
	Draft          bool
	ReviewDecision string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED or ""
	Mergeable      string // MERGEABLE, CONFLICTING, UNKNOWN
	CI             string // the head commit's checks: SUCCESS, FAILURE, PENDING, ERROR, EXPECTED or ""
	Additions      int
	Deletions      int
	ChangedFiles   int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Requests       []ReviewRequest // who is asked to review now
	Reviews        []PRReview

	Bucket               Bucket
	Why                  Why
	Reason               string    // Why for people, e.g. "re-requested, 2 commits since your review"
	Since                time.Time // when the ball landed in your court, or the state began
	ReviewStatus         string    // approved | changes requested | needs review | needs rebase | draft
	ChangedSinceApproval bool      // you approved and it changed since, but your approval still counts
	LastReviewedOid      string    // the commit you last reviewed, if any
	MyLastReview         *PRReview
	UnresolvedCount      int
	ThreadCount          int    // review threads, resolved or not
	SlackLink            string // the Slack message that shared it
	SlackReactions       string // on your PR: who reacted on its Slack share, e.g. "#reviews: Bob ✅"
}

// Local reports whether this is a local branch, not a pull request.
func (p PR) Local() bool { return p.Number == 0 }

// ReviewRequest is a person, or a team of the repo's org, asked to review.
type ReviewRequest struct {
	Login      string // a person's
	Team, Name string // a team's slug and name
}

// PRReview is a review on GitHub, by a person or a bot.
type PRReview struct {
	ID            string
	Author        Actor
	State         string // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING
	PreviousState string // what a DISMISSED review was, when known
	SubmittedAt   time.Time
	Body          string
}

// Actor is a GitHub account. A deleted one is "ghost", and counts as a bot.
type Actor struct {
	Login string
	Bot   bool // an app, Actions, a [bot] login, or in the bots setting
}

func actorOf(a *gh.Actor, bots map[string]bool) Actor {
	return Actor{Login: a.Name(), Bot: a.IsBot(bots)}
}

func prReviewOf(r gh.Review, bots map[string]bool) PRReview {
	return PRReview{ID: r.ID, Author: actorOf(r.Author, bots), State: r.State, PreviousState: r.PreviousState,
		SubmittedAt: r.SubmittedAt, Body: r.Body}
}

func prsOf(items []inbox.Item, bots map[string]bool) []PR {
	out := make([]PR, 0, len(items))
	for _, it := range items {
		out = append(out, prOf(it, bots))
	}
	return out
}

func prOf(it inbox.Item, bots map[string]bool) PR {
	pr := it.PR
	p := PR{Key: pr.Key(), Repo: pr.Repository.NameWithOwner, RepoName: pr.Repository.Name, Number: pr.Number,
		Title: pr.Title, Body: pr.Body, URL: pr.URL, Author: pr.Author.Name(), Branch: pr.HeadRefName,
		Base: pr.BaseRefName, HeadOid: pr.HeadRefOid, Draft: pr.IsDraft, ReviewDecision: pr.ReviewDecision,
		Mergeable: pr.Mergeable, CI: pr.CIState(), Additions: pr.Additions, Deletions: pr.Deletions,
		ChangedFiles: pr.ChangedFiles, CreatedAt: pr.CreatedAt, UpdatedAt: pr.UpdatedAt,
		Bucket: it.Bucket, Why: it.Why, Reason: it.Reason, Since: it.Since, ReviewStatus: it.ReviewStatus,
		ChangedSinceApproval: it.ChangedSinceApproval, LastReviewedOid: it.LastReviewedOid,
		UnresolvedCount: it.UnresolvedCount, ThreadCount: it.ThreadCount, SlackLink: it.SlackLink, SlackReactions: it.SlackReactions}
	if it.SharedToo != "" {
		p.Reason += " · " + it.SharedToo
	}
	for _, rr := range pr.ReviewRequests.Nodes {
		r := rr.RequestedReviewer
		p.Requests = append(p.Requests, ReviewRequest{Login: r.Login, Team: r.Slug, Name: r.Name})
	}
	for _, r := range pr.Reviews.Nodes {
		p.Reviews = append(p.Reviews, prReviewOf(r, bots))
	}
	if it.MyLastReview != nil {
		r := prReviewOf(*it.MyLastReview, bots)
		p.MyLastReview = &r
	}
	return p
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
	var in synced
	if json.Unmarshal(data, &in) != nil {
		return co
	}
	co.shared, co.reactions, co.slackLinks = in.Shared, in.Reactions, in.SlackLinks
	items := co.classify(&in)
	snoozed, _ := st.Snoozed(heads(items))
	co.inbox = classified{Items: items, Snoozed: snoozed, Viewer: in.Viewer, Teams: in.Teams, At: at}
	co.inbox.Items = co.withLocals(items)
	return co
}

// setConfig replaces the config without saving it. UpdateConfig saves.
func (co *Core) setConfig(cfg config.Config) {
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
	return Inbox{Items: prsOf(co.inbox.Items, co.cfg.BotSet()), Snoozed: maps.Clone(co.inbox.Snoozed),
		Viewer: co.inbox.Viewer, At: co.inbox.At}
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
func (co *Core) Open(ctx context.Context, repo string, n int) (PR, error) {
	pr, err := co.gh.PR(ctx, repo, n)
	if err != nil {
		return PR{}, fmt.Errorf("open %s#%d: %w", repo, n, err)
	}
	co.mu.Lock()
	defer co.mu.Unlock()
	if co.opened == nil {
		co.opened = map[string]prRef{}
	}
	co.opened[pr.Key()] = prRef{repo, n}
	co.inbox.Items = co.withOpened(co.inbox.Items, []gh.PR{*pr}, co.inbox.Viewer)
	if it, ok := co.item(pr.Key()); ok {
		return prOf(it, co.cfg.BotSet()), nil
	}
	return PR{}, fmt.Errorf("open %s#%d: not classified", repo, n)
}

// Refresh syncs the inbox with GitHub and returns what changed since the
// last sync that notified, whenever that was (nothing in a process that
// doesn't notify): PRs that newly need you, moved section, or
// were pushed to while addressed, and your own PRs that moved. Snoozed PRs
// are left out. Before the first sync ever, every such PR is a change.
func (co *Core) Refresh(ctx context.Context) (_ []PR, err error) {
	co.syncing.Lock()
	defer co.syncing.Unlock()
	start := time.Now()
	defer func() {
		took := time.Since(start).Round(100 * time.Millisecond)
		if err != nil {
			slog.Warn("sync failed", "took", took, "err", err)
			return
		}
		co.mu.Lock()
		n := len(co.inbox.Items)
		co.mu.Unlock()
		slog.Info("synced", "prs", n, "took", took, "github_rate_left", co.gh.RateLeft())
	}()
	co.mu.Lock()
	cfg := co.cfg
	cfg.WatchRepos = slices.Clone(cfg.WatchRepos)
	for repo := range co.peek {
		if !slices.Contains(cfg.WatchRepos, repo) {
			cfg.WatchRepos = append(cfg.WatchRepos, repo)
		}
	}
	opened := slices.Collect(maps.Values(co.opened))
	sl := co.slack
	slackOn := sl != nil && co.authErr("slack") == nil
	var known []string
	for _, it := range co.inbox.Items {
		if _, linked := co.opened[it.PR.Key()]; !linked && !it.PR.Local() {
			known = append(known, it.PR.ID)
		}
	}
	co.mu.Unlock()

	var kept []store.SlackShare
	if sl != nil {
		kept, _ = co.st.SlackShares()
	}
	var keep []string
	for _, s := range newestShares(cfg, co.slackMe, kept) {
		keep = append(keep, s.PRID)
	}
	byLink := make([]*gh.PR, len(opened))
	var fetching sync.WaitGroup
	for i, o := range opened {
		fetching.Go(func() {
			var err error
			byLink[i], err = co.gh.PR(ctx, o.repo, o.n)
			logErr(fmt.Sprintf("fetch %s#%d", o.repo, o.n), err)
		})
	}
	var newlyShared []gh.PR
	var slackErr error
	if slackOn {
		fetching.Go(func() { newlyShared, slackErr = co.syncSlack(ctx, sl, cfg, kept) })
	}
	fetched, err := co.gh.FetchInbox(ctx, cfg.Orgs, cfg.WatchRepos, known, keep)
	fetching.Wait()
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	in := &synced{Inbox: *fetched}
	var shares map[string]store.SlackShare
	if sl != nil {
		shares = co.withShares(in, cfg, newlyShared)
	}
	if slackOn {
		var err error
		in.Reactions, err = co.reactionsOnYours(ctx, sl, cfg, in)
		slackErr = errors.Join(slackErr, err)
	}
	if data, err := json.Marshal(in); err == nil {
		logErr("save inbox", co.st.SaveInbox(data, time.Now()))
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
	snoozed, err := co.st.Snoozed(heads(items))
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	var extra []gh.PR
	for _, pr := range byLink {
		if pr != nil && pr.State == "OPEN" {
			extra = append(extra, *pr)
		}
	}

	co.pruneLocals()
	co.foldLocals(items, in.Viewer)
	co.mu.Lock()
	co.shared, co.slackShares, co.reactions, co.slackLinks = in.Shared, shares, in.Reactions, in.SlackLinks
	co.inbox = classified{Items: co.withOpened(items, extra, in.Viewer), Snoozed: snoozed, Viewer: in.Viewer, Teams: in.Teams, At: time.Now()}
	co.inbox.Items = co.withLocals(co.inbox.Items)
	co.mu.Unlock()
	if slackOn && slackErr == nil {
		slackErr = co.reconcileReactions(ctx, sl)
	}
	if slack.LoggedOut(slackErr) {
		co.setLoggedOut("slack")
		slackErr = nil
	}
	logErr("slack sync", slackErr)
	co.mu.Lock()
	co.slackErr = slackErr
	co.mu.Unlock()
	changes = slices.DeleteFunc(changes, func(it inbox.Item) bool { _, ok := snoozed[it.PR.Key()]; return ok })
	return prsOf(changes, cfg.BotSet()), nil
}

func (co *Core) classify(in *synced) []inbox.Item {
	co.mu.Lock()
	opt := co.options(in.Shared, in.Reactions, in.SlackLinks)
	co.mu.Unlock()
	return inbox.Classify(&in.Inbox, opt)
}

// options classify with the settings and what Slack says about the PRs.
// co.mu is held.
func (co *Core) options(shared map[string]inbox.Share, reactions, links map[string]string) inbox.Options {
	return inbox.Options{Bots: co.cfg.BotSet(), RecheckAfterPush: co.cfg.RecheckAfterPush, Shared: shared, Reactions: reactions, SlackLinks: links}
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
	extra := &gh.Inbox{Viewer: viewer, PRs: add}
	return append(items, inbox.Classify(extra, co.options(co.shared, co.reactions, co.slackLinks))...)
}

// changed lists the PRs that need you (or are yours) and are new, moved
// section, or were pushed to while addressed. A Slack share isn't one: Slack
// already told you.
func changed(items []inbox.Item, prev map[string]store.PRState) []inbox.Item {
	var out []inbox.Item
	for _, it := range items {
		if !it.Bucket.NeedsMe() && it.Bucket != inbox.Mine || it.Bucket == inbox.Shared {
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
			Key: it.PR.Key(), Bucket: int(it.Bucket), Reason: it.Reason, HeadOid: it.PR.HeadRefOid,
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
func Notify(changes []PR, timeout time.Duration) {
	changes = slices.DeleteFunc(slices.Clone(changes), func(it PR) bool {
		return it.Bucket == Mine && it.SlackReactions == "" && it.Why != WhyNewFeedback && it.Why != WhyReadyToMerge
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
		title := fmt.Sprintf("%s: %s#%d", it.Bucket, it.RepoName, it.Number)
		platform.Notify(title, fmt.Sprintf("%s — %s\n%s", it.Author, it.Title, it.Reason), timeout)
	}
}

// PruneClosed deletes the review history, worktrees and refs of PRs that
// GitHub says are merged or closed. A PR missing from the inbox isn't enough:
// it may be in a repo only peeked at, so each is asked about. Run calls it
// after a sync, so a sync never waits for it.
func (co *Core) PruneClosed(ctx context.Context) {
	keys, err := co.st.ReviewedPRs()
	if err != nil {
		return
	}
	co.mu.Lock()
	open := heads(co.inbox.Items)
	co.mu.Unlock()
	co.pruneShares(ctx, open)
	for _, key := range keys {
		if _, ok := open[key]; ok {
			continue
		}
		repo, n, err := parseKey(key)
		if err != nil {
			continue
		}
		if pr, err := co.gh.PR(ctx, repo, n); err == nil && pr.State != "OPEN" {
			logErr("delete reviews of "+key, co.st.DeleteReviews(key))
			agent.RemoveReview(co.Config(), repo, n)
		}
	}
}

// PruneOrphans removes review worktrees and refs/prowl/* refs left for PRs
// prowl has no review of, in your checkouts and its own clones. A remote may
// spell the repo in other case than GitHub does, so keys match without case.
func (co *Core) PruneOrphans() {
	agent.PruneOrphans(co.Config(), func(repo string, n int) bool {
		keys, err := co.st.ReviewedPRs()
		want := gh.PRKey(repo, n)
		return err != nil || slices.ContainsFunc(keys, func(k string) bool { return strings.EqualFold(k, want) })
	})
}

// RefreshPR re-fetches one PR after you acted on it, so its section updates
// in about a second rather than at the next sync, which also lags behind
// GitHub by a few seconds. A merged or closed PR leaves the inbox.
func (co *Core) RefreshPR(ctx context.Context, key string) error {
	repo, n, err := parseKey(key)
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	pr, err := co.gh.PR(ctx, repo, n)
	if err != nil {
		return fmt.Errorf("refresh %s: %w", key, err)
	}
	co.mu.Lock()
	defer co.mu.Unlock()
	i := slices.IndexFunc(co.inbox.Items, func(it inbox.Item) bool { return it.PR.Key() == key })
	switch {
	case i < 0:
	case pr.State != "OPEN":
		co.inbox.Items = slices.Delete(co.inbox.Items, i, i+1)
	default:
		co.inbox.Items[i] = co.reclassify(*pr)
	}
	return nil
}

// reclassify is the inbox item for one PR as it is now. co.mu is held.
func (co *Core) reclassify(pr gh.PR) inbox.Item {
	watched := maps.Clone(co.peek)
	if watched == nil {
		watched = map[string]bool{}
	}
	for _, r := range co.cfg.WatchRepos {
		watched[r] = true
	}
	in := &gh.Inbox{Viewer: co.inbox.Viewer, Teams: co.inbox.Teams, Watched: watched, PRs: []gh.PR{pr}}
	return inbox.Classify(in, co.options(co.shared, co.reactions, co.slackLinks))[0]
}

// ErrWatching is Watching's answer while another `prowl watch` runs.
var ErrWatching = errors.New("prowl watch is already running")

// Watching makes this process `prowl watch`, which sends the desktop
// notifications for everyone. Only one runs: it holds a lock on watch.lock in
// the data dir for as long as it runs, which the OS drops when it ends, so a
// crash or a reboot never leaves a stale claim. A UI checking for a watcher
// holds the lock for a moment, so a taken lock is tried again for a second
// before it returns ErrWatching.
func (co *Core) Watching() error {
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	l, err := platform.TryLock(watchLock())
	for try := 0; errors.Is(err, platform.ErrLocked) && try < 10; try++ {
		time.Sleep(100 * time.Millisecond)
		l, err = platform.TryLock(watchLock())
	}
	if errors.Is(err, platform.ErrLocked) {
		return fmt.Errorf("%w (process %d)", ErrWatching, co.st.Watcher())
	}
	if err != nil {
		return fmt.Errorf("watch: %w", err)
	}
	co.mu.Lock()
	co.watching, co.quiet = l, false
	co.mu.Unlock()
	return co.st.SetWatcher(os.Getpid())
}

// StopWatching gives up being `prowl watch`, so the prowl it starts to
// restart on an update isn't turned away while this one exits.
func (co *Core) StopWatching() {
	co.mu.Lock()
	if co.watching != nil {
		co.watching.Unlock()
		co.watching = nil
	}
	co.mu.Unlock()
	logErr("clear watcher", co.st.SetWatcher(0))
}

func watchLock() string { return filepath.Join(config.DataDir(), "watch.lock") }

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
	co.mu.Lock()
	mine := co.watching != nil
	co.mu.Unlock()
	if mine {
		return false
	}
	l, err := platform.TryLock(watchLock())
	if err != nil {
		return errors.Is(err, platform.ErrLocked)
	}
	l.Unlock()
	return false
}

// Config is the config core works with.
func (co *Core) Config() config.Config {
	co.mu.Lock()
	defer co.mu.Unlock()
	return co.cfg
}

// WatchRepo adds repo (owner/name) to watch_repos, or removes it, and saves the
// config. The next Refresh fetches every open PR in a watched repo.
func (co *Core) WatchRepo(repo string, on bool) error {
	_, err := co.UpdateConfig(func(c *config.Config) error {
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
func (co *Core) RequestReviews(ctx context.Context, key string, names []string) error {
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
	return co.gh.RequestReviewers(ctx, repo, n, logins, teams)
}

// Candidate is someone who can be asked to review: a login, or org/team for
// a team.
type Candidate struct {
	Login string
	Name  string
	Team  bool
}

// ReviewCandidates lists who can be asked to review the PR: people who can
// be assigned in its repo, then its org's teams. It asks GitHub once per repo
// per session.
func (co *Core) ReviewCandidates(ctx context.Context, key string) ([]Candidate, error) {
	repo, _, err := parseKey(key)
	if err != nil {
		return nil, fmt.Errorf("reviewers: %w", err)
	}
	co.mu.Lock()
	cached, ok := co.candidates[repo]
	co.mu.Unlock()
	if ok {
		return cached, nil
	}
	found, err := co.gh.ReviewCandidates(ctx, repo)
	if err != nil {
		return nil, err
	}
	out := candidates(found)
	co.mu.Lock()
	if co.candidates == nil {
		co.candidates = map[string][]Candidate{}
	}
	co.candidates[repo] = out
	co.mu.Unlock()
	return out, nil
}

// SuggestedReviewers are the people GitHub suggests for the PR, from the code
// owners and recent authors of the files it changes.
func (co *Core) SuggestedReviewers(ctx context.Context, key string) ([]Candidate, error) {
	repo, n, err := parseKey(key)
	if err != nil {
		return nil, fmt.Errorf("suggested reviewers: %w", err)
	}
	found, err := co.gh.SuggestedReviewers(ctx, repo, n)
	return candidates(found), err
}

func candidates(found []gh.Candidate) []Candidate {
	var out []Candidate
	for _, c := range found {
		out = append(out, Candidate(c))
	}
	return out
}

// Claim requests you as a reviewer on GitHub, for a PR shared without
// asking you (a link in Slack, say).
func (co *Core) Claim(ctx context.Context, key string) error {
	repo, n, err := parseKey(key)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	co.mu.Lock()
	me := co.inbox.Viewer
	co.mu.Unlock()
	return co.gh.RequestReviewers(ctx, repo, n, []string{me}, nil)
}

// parseKey splits a PR key (owner/name#number).
func parseKey(key string) (repo string, n int, err error) {
	repo, num, _ := strings.Cut(key, "#")
	if n, err = strconv.Atoi(num); err != nil {
		return "", 0, fmt.Errorf("bad PR key %q: %w", key, err)
	}
	return repo, n, nil
}

// logErr logs what failed when prowl carries on without it.
func logErr(what string, err error) {
	if err != nil {
		slog.Warn(what+" failed", "err", err)
	}
}

// item is the inbox item for key. co.mu is held.
func (co *Core) item(key string) (inbox.Item, bool) {
	i := slices.IndexFunc(co.inbox.Items, func(it inbox.Item) bool { return it.PR.Key() == key })
	if i < 0 {
		return inbox.Item{}, false
	}
	return co.inbox.Items[i], true
}

// Diff fetches a PR's diff, or with sinceReview only what changed since
// your last review.
func (co *Core) Diff(ctx context.Context, key string, sinceReview bool) (string, error) {
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
		diff, err = co.gh.CompareDiff(ctx, pr.Repository.NameWithOwner, it.LastReviewedOid, pr.HeadRefOid)
	} else {
		diff, err = co.gh.PRDiff(ctx, pr.Repository.NameWithOwner, pr.Number)
	}
	return diff, err
}

// checkLogLines is how much of a failed job's log CheckLog returns.
const checkLogLines = 40

// CheckLog is the end of a failed GitHub Actions check's log, and the step
// that failed, for a check from Detail with a JobID.
func (co *Core) CheckLog(ctx context.Context, key string, jobID int64) (step string, lines []string, err error) {
	repo, _, err := parseKey(key)
	if err != nil {
		return "", nil, fmt.Errorf("check log: %w", err)
	}
	return co.gh.JobLogTail(ctx, repo, jobID, checkLogLines)
}

// SetFileViewed ticks or clears GitHub's "Viewed" box on a file of a PR.
func (co *Core) SetFileViewed(ctx context.Context, key, path string, viewed bool) error {
	co.mu.Lock()
	it, ok := co.item(key)
	co.mu.Unlock()
	if !ok {
		return fmt.Errorf("mark %s viewed: %s is not in the inbox", path, key)
	}
	return co.gh.SetFileViewed(ctx, it.PR.ID, path, viewed)
}

// Reply answers a review thread.
func (co *Core) Reply(ctx context.Context, threadID, body string) error {
	return co.gh.ReplyToThread(ctx, threadID, body)
}

// Resolve resolves a review thread, or reopens it.
func (co *Core) Resolve(ctx context.Context, threadID string, resolved bool) error {
	return co.gh.ResolveThread(ctx, threadID, resolved)
}

// OrgRepos lists every repo (owner/name) in your orgs, e.g. to pick one to
// peek at. A failing org is skipped.
func (co *Core) OrgRepos(ctx context.Context) []string {
	var all []string
	for _, org := range co.Config().Orgs {
		if repos, err := co.gh.OrgRepos(ctx, org); err == nil {
			all = append(all, repos...)
		}
	}
	return all
}
