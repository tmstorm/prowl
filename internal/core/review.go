package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/store"
)

// Event tells a UI what changed in the background, so it can re-read it.
// Changes a UI made itself through a call are not reported.
type Event struct {
	Kind    EventKind
	Changes []PR   // InboxChanged: what changed, as Refresh returns it
	Key     string // SummaryChanged: the PR
	Err     error  // InboxChanged, SummaryChanged, ConfigChanged: what failed
}

type EventKind int

const (
	Syncing        EventKind = iota // a background sync started
	InboxChanged                    // a background sync finished
	RunsChanged                     // an agent review or on-demand extension started, progressed or finished
	SummaryChanged                  // an auto summary was written, or failed
	AuthChanged                     // the periodic login check finished
	ConfigChanged                   // the config file was edited outside prowl
	ReleaseChanged                  // the hourly update check ran, or an update moved on a step
	LocalsChanged                   // a local review started by another prowl joined the inbox
)

// Events delivers events to one reader. An event that finds the buffer full
// is dropped; the next getter call catches up.
func (co *Core) Events() <-chan Event { return co.events }

func (co *Core) emit(e Event) {
	select {
	case co.events <- e:
	default:
	}
}

// Run syncs the inbox every poll_interval, watches agent reviews and keeps
// your Slack emoji in step with them as they start and end, writes
// summaries when auto_summary is on, checks the logins every half hour,
// picks up edits to the config file and checks for updates (installing them
// when no prowl watch runs to do it), until ctx ends. Each is reported as
// events. The first sync is the caller's. It also prunes orphaned review
// worktrees and refs once, and what is kept for closed PRs at the start and
// after each sync.
func (co *Core) Run(ctx context.Context) {
	var tasks sync.WaitGroup
	tasks.Go(co.PruneOrphans)
	tasks.Go(func() { co.PruneClosed(ctx) })
	tasks.Go(func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if co.loadRuns() {
					if co.showLocals() {
						co.emit(Event{Kind: LocalsChanged})
					}
					co.emit(Event{Kind: RunsChanged})
				}
				if changed, err := co.reloadConfig(); changed {
					co.emit(Event{Kind: ConfigChanged, Err: err})
				}
				co.autoSummaries(ctx)
			}
		}
	})
	tasks.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-co.reactWake:
				co.reactNow(ctx)
			}
		}
	})
	tasks.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(co.Config().PollEvery()):
				co.emit(Event{Kind: Syncing})
				changes, err := co.Refresh(ctx)
				co.emit(Event{Kind: InboxChanged, Changes: changes, Err: err})
				co.PruneClosed(ctx)
			}
		}
	})
	tasks.Go(func() {
		exe := Executable()
		for {
			if co.notifies() {
				_, err := co.AutoUpgrade(ctx, exe)
				logErr("update", err)
			}
			co.emit(Event{Kind: ReleaseChanged})
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Hour):
			}
		}
	})
	tasks.Go(func() {
		for {
			co.CheckAuth(ctx)
			co.emit(Event{Kind: AuthChanged})
			select {
			case <-ctx.Done():
				return
			case <-time.After(AuthEvery):
			}
		}
	})
	tasks.Wait()
}

// Review is an AI review run: an agent reviewing a PR at one commit. A
// revise or sync updates it; a new review starts a new one, and the earlier
// runs are the agent's history for that PR.
type Review struct {
	ID                    int64
	Agent, Profile        string // Profile is "" for a plain review
	Status                ReviewStatus
	HeadOid, SessionID    string
	StartedAt, FinishedAt time.Time
	Activity              string    // what it is doing while it runs
	ActivityAt            time.Time // when Activity last changed
	Model, Guidance       string
	Summary, Error        string
	Findings              []Finding
	Replies               []ThreadReply
	Points                []Point
	Action, ActionReason  string // the agent suggests closing the PR ("close") or a draft ("draft"), and why
	Origin                string // "local" for a run made in your checkout before the PR existed
}

type ReviewStatus string

const (
	ReviewRunning ReviewStatus = "running"
	ReviewDone    ReviewStatus = "done"
	ReviewPosted  ReviewStatus = "posted"
	ReviewFailed  ReviewStatus = "failed"
)

type Finding struct {
	Severity         Severity
	Rule             string // the guideline it comes from
	Path             string
	Line             int
	Title, Body      string
	Excluded, Posted bool
}

type Severity string

const (
	MustFix   Severity = "must-fix"
	ShouldFix Severity = "should-fix"
	Nit       Severity = "nit"
)

// Point is something a reviewer, a bot or a check raised on the PR, as an
// agent checked it against the code. Points are never posted.
type Point struct {
	Who     string // login, or the check's name
	Path    string
	Line    int
	Point   string
	Verdict string // addressed | open | wrong | unclear
	Why     string
}

// ThreadReply is a reply an agent drafted to a review thread, for you to
// edit and send.
type ThreadReply struct {
	ThreadID string
	Path     string
	Line     int
	To       string // who wrote the comment it answers
	Last     string // that comment
	Body     string
	Excluded bool // you chose not to send it
	Posted   bool
}

// Note is a line between you ("you") and a reviewer ("agent") about its
// review.
type Note struct {
	At        time.Time
	Who, Text string
}

func reviewOf(r *store.Run) *Review {
	out := &Review{ID: r.ID, Agent: r.Agent, Profile: r.Profile, Status: ReviewStatus(r.Status), HeadOid: r.HeadOid,
		SessionID: r.SessionID, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Activity: r.Activity, ActivityAt: r.ActivityAt,
		Model: r.Model, Guidance: r.Guidance, Summary: r.Summary, Error: r.Error, Action: r.Action,
		ActionReason: r.ActionReason, Origin: r.Origin}
	for _, f := range r.Findings {
		out.Findings = append(out.Findings, Finding{Severity: Severity(f.Severity), Rule: f.Rule, Path: f.Path, Line: f.Line,
			Title: f.Title, Body: f.Body, Excluded: f.Excluded, Posted: f.Posted})
	}
	for _, rp := range r.Replies {
		out.Replies = append(out.Replies, ThreadReply(rp))
	}
	for _, p := range r.Points {
		out.Points = append(out.Points, Point(p))
	}
	return out
}

func storedFindings(findings []Finding) []store.Finding {
	var out []store.Finding
	for _, f := range findings {
		out = append(out, store.Finding{Severity: string(f.Severity), Rule: f.Rule, Path: f.Path, Line: f.Line,
			Title: f.Title, Body: f.Body, Excluded: f.Excluded, Posted: f.Posted})
	}
	return out
}

// MustFix counts the review's must-fix findings.
func (r *Review) MustFix() int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == MustFix {
			n++
		}
	}
	return n
}

// StillOpen counts the points raised that still hold.
func (r *Review) StillOpen() int {
	n := 0
	for _, p := range r.Points {
		if p.Verdict == "open" {
			n++
		}
	}
	return n
}

// Unsent counts the drafted thread replies still to send: not sent, and
// not left out.
func (r *Review) Unsent() int {
	n := 0
	for _, rp := range r.Replies {
		if !rp.Posted && !rp.Excluded {
			n++
		}
	}
	return n
}

// Reviewer names who reviewed: the agent, or agent:profile.
func (r *Review) Reviewer() string { return store.Reviewer(r.Agent, r.Profile) }

// Finished reports whether the review ran to its end, posted or not.
func (r *Review) Finished() bool { return r.Status == ReviewDone || r.Status == ReviewPosted }

// Latest is a reviewer's current review in reviews (newest first), or nil.
func Latest(reviews []*Review, reviewer string) *Review {
	for _, r := range reviews {
		if r.Reviewer() == reviewer {
			return r
		}
	}
	return nil
}

// Current is each reviewer's latest review, newest first.
func Current(reviews []*Review) []*Review {
	var out []*Review
	seen := map[string]bool{}
	for _, r := range reviews {
		if !seen[r.Reviewer()] {
			seen[r.Reviewer()] = true
			out = append(out, r)
		}
	}
	return out
}

// loadRuns reloads every review run and on-demand extension job, and
// reports whether any changed. A run or job starting or ending wakes the
// Slack emoji.
func (co *Core) loadRuns() bool {
	stored, err := co.st.AllRuns()
	if err != nil {
		return false
	}
	all := make(map[string][]*Review, len(stored))
	for key, runs := range stored {
		for _, r := range runs {
			all[key] = append(all[key], reviewOf(r))
		}
	}
	storedJobs, err := co.st.ExtensionJobs()
	if err != nil {
		return false
	}
	jobs := make(map[string][]ExtensionJob, len(storedJobs))
	for key, js := range storedJobs {
		for _, j := range js {
			jobs[key] = append(jobs[key], extensionJobOf(j))
		}
	}
	co.mu.Lock()
	defer co.mu.Unlock()
	if reflect.DeepEqual(all, co.runs) && reflect.DeepEqual(jobs, co.extJobs) {
		return false
	}
	if !reflect.DeepEqual(progress(all, jobs), progress(co.runs, co.extJobs)) {
		co.wakeReactions()
	}
	co.runs, co.extJobs = all, jobs
	return true
}

// progress is when each PR's review runs and extension jobs started and
// where they stand, all the Slack emoji read of them. An agent's activity
// changes a run every step without changing this.
func progress(runs map[string][]*Review, jobs map[string][]ExtensionJob) map[string][]string {
	out := map[string][]string{}
	for key, rs := range runs {
		for _, r := range rs {
			out[key] = append(out[key], r.StartedAt.String()+" "+string(r.Status))
		}
	}
	for key, js := range jobs {
		for _, j := range js {
			out[key] = append(out[key], j.StartedAt.String()+" "+string(j.Status))
		}
	}
	return out
}

// Runs are every PR's review runs, newest first, by PR key.
func (co *Core) Runs() map[string][]*Review {
	co.mu.Lock()
	defer co.mu.Unlock()
	out := make(map[string][]*Review, len(co.runs))
	for key, runs := range co.runs {
		for _, r := range runs {
			c := *r
			c.Findings = slices.Clone(r.Findings)
			c.Replies = slices.Clone(r.Replies)
			c.Points = slices.Clone(r.Points)
			out[key] = append(out[key], &c)
		}
	}
	return out
}

// Notes are what you and each current reviewer said to each other about a
// PR, by reviewer. They are never posted.
func (co *Core) Notes(key string) map[string][]Note {
	co.mu.Lock()
	runs := Current(co.runs[key])
	co.mu.Unlock()
	return notesOf(co.st, key, runs)
}

func notesOf(st *store.Store, key string, reviews []*Review) map[string][]Note {
	out := map[string][]Note{}
	for _, r := range reviews {
		log, _ := st.Notes(key, r.Reviewer())
		for _, e := range log {
			out[r.Reviewer()] = append(out[r.Reviewer()], Note(e))
		}
	}
	return out
}

// ReviewsOf is the reviews of one PR or local branch, newest first, and the
// notes of the current ones, read from st without loading the rest of core:
// prowl findings needs only these.
func ReviewsOf(st *store.Store, key string) ([]*Review, map[string][]Note, error) {
	runs, err := st.Runs(key)
	if err != nil {
		return nil, nil, err
	}
	out := make([]*Review, 0, len(runs))
	for _, r := range runs {
		out = append(out, reviewOf(r))
	}
	return out, notesOf(st, key, Current(out)), nil
}

// Review starts a review of a PR in the background by reviewer: an agent
// ("" is the main one) or agent:profile. It returns the reviewer it started.
func (co *Core) Review(key, reviewer string) (string, error) {
	return co.launch(key, reviewer, "", false)
}

// Revise has a reviewer revise its current review per your instruction.
func (co *Core) Revise(key, reviewer, instruction string) (string, error) {
	return co.launch(key, reviewer, instruction, false)
}

// SyncChat has a reviewer update its review from a chat you just had with it.
func (co *Core) SyncChat(key, reviewer string) (string, error) {
	return co.launch(key, reviewer, agent.SyncInstruction, true)
}

func (co *Core) launch(key, reviewer, refine string, sync bool) (string, error) {
	job, err := co.job(key, reviewer, refine, sync)
	if err != nil {
		return "", err
	}
	if err := agent.Launch(co.st, job); err != nil {
		return "", err
	}
	co.loadRuns()
	return store.Reviewer(job.Agent, job.Profile), nil
}

// prJob is a job on the PR key: the PR as GitHub has it, or for a local
// review, the branch in your checkout.
func (co *Core) prJob(key string) (agent.Job, error) {
	co.mu.Lock()
	it, ok := co.item(key)
	viewer := co.inbox.Viewer
	co.mu.Unlock()
	if !ok {
		return agent.Job{}, errors.New("not in the inbox")
	}
	pr := it.PR
	job := agent.Job{Repo: pr.Repository.NameWithOwner, Number: pr.Number, Title: pr.Title,
		Author: pr.Author.Name(), Base: pr.BaseRefName, Head: pr.HeadRefOid, Body: pr.Body, Viewer: viewer}
	if pr.Local() {
		l, err := co.local(key)
		if err != nil {
			return agent.Job{}, err
		}
		job.Dir, job.Branch, job.Head = l.Dir, l.Branch, git(l.Dir, "rev-parse", "HEAD")
	}
	return job, nil
}

// job is the agent job that reviews key with reviewer, revising its current
// run when refine is set.
func (co *Core) job(key, reviewer, refine string, sync bool) (agent.Job, error) {
	name, profile, _ := strings.Cut(reviewer, ":")
	cfg := co.Config()
	if name == "" {
		name = cfg.Agent.Main()
	}
	be, err := agent.For(cfg, name)
	if err == nil {
		err = be.Check()
	}
	if err != nil {
		return agent.Job{}, err
	}
	reviewer = store.Reviewer(be.Name(), profile)
	title := ReviewerTitle(reviewer)
	co.mu.Lock()
	cur := Latest(co.runs[key], reviewer)
	co.mu.Unlock()
	if cur != nil && cur.Status == ReviewRunning {
		return agent.Job{}, fmt.Errorf("%s is already reviewing %s", title, key)
	}
	if err := co.loggedIn(be.Name()); err != nil {
		return agent.Job{}, err
	}
	job, err := co.prJob(key)
	if err != nil {
		return agent.Job{}, fmt.Errorf("review %s: %w", key, err)
	}
	job.Refine, job.Sync, job.Agent, job.Profile = refine, sync, be.Name(), profile
	if job.Dir != "" && refine == "" {
		l, err := co.local(key)
		if err != nil {
			return agent.Job{}, err
		}
		if err := unchanged(l); err != nil {
			return agent.Job{}, err
		}
	}
	if refine != "" {
		if cur == nil {
			return agent.Job{}, fmt.Errorf("no %s review to revise yet", title)
		}
		if cur.Status == ReviewFailed {
			return agent.Job{}, fmt.Errorf("%s's last review failed, so there is nothing to revise; start a new one", title)
		}
		if cur.Origin == "local" {
			return agent.Job{}, fmt.Errorf("%s reviewed this branch in your checkout before the PR; start a new review to revise", title)
		}
		job.RunID = cur.ID
	}
	if _, err := exec.LookPath("git"); err != nil {
		return agent.Job{}, errors.New("AI reviews need git, which isn't on your PATH; install it from https://git-scm.com")
	}
	return job, nil
}

// Cancel stops a running review run, or a revise of it, within a second or
// so. Its agent-run ends it as it ends a failed one: a review fails with
// "cancelled", a revise keeps the review as it was.
func (co *Core) Cancel(runID int64) error {
	ok, err := co.st.CancelRun(runID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("that review isn't running")
	}
	return nil
}

// Discard deletes a review run and its notes; the reviewer's previous run,
// if any, is current again. A local review with no runs left is forgotten.
func (co *Core) Discard(runID int64) error {
	run, err := co.st.Run(runID)
	if err != nil {
		return fmt.Errorf("discard: %w", err)
	}
	if run == nil {
		return fmt.Errorf("discard: no review run %d", runID)
	}
	if err := co.st.DeleteRun(runID); err != nil {
		return fmt.Errorf("discard: %w", err)
	}
	if err := co.st.ClearNotes(run.Key, run.Reviewer()); err != nil {
		return fmt.Errorf("discard: %w", err)
	}
	co.loadRuns()
	if _, err := co.local(run.Key); err == nil && len(co.Runs()[run.Key]) == 0 {
		if err := co.st.DeleteLocal(run.Key); err != nil {
			return fmt.Errorf("discard: %w", err)
		}
		co.mu.Lock()
		co.inbox.Items = co.withLocals(co.inbox.Items)
		co.mu.Unlock()
	}
	return nil
}

// SetExcluded includes a finding in what gets posted, or leaves it out.
func (co *Core) SetExcluded(runID int64, finding int, excluded bool) error {
	return co.editFindings(func(r *Review) bool { return r.ID == runID }, func(i int, f Finding) bool {
		if i == finding {
			return excluded
		}
		return f.Excluded
	})
}

// MustFixOnly leaves out every finding of a PR's current reviews that isn't
// must-fix, or with on false includes them all again.
func (co *Core) MustFixOnly(key string, on bool) error {
	co.mu.Lock()
	var ids []int64
	for _, r := range Current(co.runs[key]) {
		ids = append(ids, r.ID)
	}
	co.mu.Unlock()
	return co.editFindings(func(r *Review) bool { return slices.Contains(ids, r.ID) }, func(_ int, f Finding) bool {
		return on && f.Severity != MustFix
	})
}

// editFindings sets Excluded on the findings of the runs that match.
func (co *Core) editFindings(match func(*Review) bool, excluded func(int, Finding) bool) error {
	co.mu.Lock()
	defer co.mu.Unlock()
	for _, runs := range co.runs {
		for _, r := range runs {
			if !match(r) {
				continue
			}
			for i := range r.Findings {
				r.Findings[i].Excluded = excluded(i, r.Findings[i])
			}
			if err := co.st.SaveFindings(r.ID, storedFindings(r.Findings)); err != nil {
				return fmt.Errorf("save findings: %w", err)
			}
		}
	}
	return nil
}

// Command is an interactive program for a UI to run in a terminal: an agent
// chat, or a login.
type Command struct {
	Args []string // the program and its arguments
	Dir  string   // "" is the UI's own directory
	Env  []string // nil is the UI's own environment
	Quit string   // what ends a chat, e.g. "/exit"
}

func commandOf(cmd *exec.Cmd, quit string) Command {
	return Command{Args: cmd.Args, Dir: cmd.Dir, Env: cmd.Env, Quit: quit}
}

// Chat is the command for talking to a reviewer about its review of a PR.
func (co *Core) Chat(key, reviewer string) (Command, error) {
	co.mu.Lock()
	run := Latest(co.runs[key], reviewer)
	co.mu.Unlock()
	switch {
	case run != nil && run.Status == ReviewRunning:
		return Command{}, fmt.Errorf("wait for the review to finish before chatting")
	case run == nil || run.SessionID == "":
		return Command{}, fmt.Errorf("no review session to talk to yet; start a review first")
	case run.Origin == "local":
		return Command{}, fmt.Errorf("this review ran in your checkout before the PR; start a new review to chat with it")
	}
	cfg := co.Config()
	be, err := agent.For(cfg, run.Agent)
	if err == nil {
		err = be.Check()
	}
	if err != nil {
		return Command{}, err
	}
	var repo, wt string
	l, err := co.local(key)
	if err == nil {
		repo, wt = l.Repo, l.Dir
	} else {
		var n int
		if repo, n, err = parseKey(key); err != nil {
			return Command{}, fmt.Errorf("chat: %w", err)
		}
		wt = agent.WorktreePath(repo, n, reviewer)
	}
	if _, err := os.Stat(wt); err != nil {
		return Command{}, fmt.Errorf("the review's worktree is gone; run the review again")
	}
	cmd := be.Chat(run.SessionID, wt, cfg.Agent.Model(be.Name()), agent.ToolServerCommand(cfg, be, repo, wt, l.Dir != "", true))
	if cmd == nil {
		return Command{}, fmt.Errorf("%s has no interactive session to chat in; ask it to revise instead", agent.Title(be.Name()))
	}
	return commandOf(cmd, be.ChatQuit()), nil
}

// ReviewerTitle names a reviewer for people: "Codex", "Codex · adversarial".
var ReviewerTitle = agent.ReviewerTitle
