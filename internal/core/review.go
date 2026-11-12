package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/inbox"
	"github.com/tmstorm/prowl/internal/store"
)

// Event tells a UI what changed in the background, so it can re-read it.
// Changes a UI made itself through a call are not reported.
type Event struct {
	Kind    EventKind
	Changes []inbox.Item // InboxChanged: what changed, as Refresh returns it
	Key     string       // SummaryChanged: the PR
	Err     error        // InboxChanged, SummaryChanged, ConfigChanged: what failed
}

type EventKind int

const (
	Syncing        EventKind = iota // a background sync started
	InboxChanged                    // a background sync finished
	RunsChanged                     // an agent review started, progressed or finished
	SummaryChanged                  // an auto summary was written, or failed
	AuthChanged                     // the periodic login check finished
	ConfigChanged                   // the config file was edited outside prowl
	ReleaseChanged                  // the hourly update check ran, or an update moved on a step
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

// Run syncs the inbox every poll_interval, watches agent reviews, writes
// summaries when auto_summary is on, checks the logins every half hour,
// picks up edits to the config file and checks for updates (installing them
// when no prowl watch runs to do it), until ctx ends. Each is reported as
// events. The first sync is the caller's.
func (co *Core) Run(ctx context.Context) {
	var tasks sync.WaitGroup
	tasks.Go(func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if co.loadRuns() {
					co.emit(Event{Kind: RunsChanged})
				}
				if changed, err := co.reloadConfig(); changed {
					co.emit(Event{Kind: ConfigChanged, Err: err})
				}
				co.autoSummaries()
			}
		}
	})
	tasks.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(max(co.Config().PollInterval.Duration, 30*time.Second)):
				co.emit(Event{Kind: Syncing})
				changes, err := co.Refresh()
				co.emit(Event{Kind: InboxChanged, Changes: changes, Err: err})
			}
		}
	})
	tasks.Go(func() {
		exe := Executable()
		for {
			if co.notifies() {
				_, _ = co.AutoUpgrade(exe)
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
			co.CheckAuth()
			co.emit(Event{Kind: AuthChanged})
			select {
			case <-ctx.Done():
				return
			case <-time.After(authEvery):
			}
		}
	})
	tasks.Wait()
}

// loadRuns reloads every review run and reports whether any changed.
func (co *Core) loadRuns() bool {
	all, err := co.st.AllRuns()
	if err != nil {
		return false
	}
	co.mu.Lock()
	defer co.mu.Unlock()
	if reflect.DeepEqual(all, co.runs) {
		return false
	}
	co.runs = all
	return true
}

// Reviews are every PR's review runs, newest first, by PR key.
func (co *Core) Reviews() map[string][]*store.AgentReview {
	co.mu.Lock()
	defer co.mu.Unlock()
	out := make(map[string][]*store.AgentReview, len(co.runs))
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
func (co *Core) Notes(key string) map[string][]store.LogEntry {
	co.mu.Lock()
	runs := store.Current(co.runs[key])
	co.mu.Unlock()
	out := map[string][]store.LogEntry{}
	for _, r := range runs {
		if log, err := co.st.ReviewLog(key, r.Reviewer()); err == nil && len(log) > 0 {
			out[r.Reviewer()] = log
		}
	}
	return out
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
	it, ok := co.item(key)
	viewer := co.inbox.Viewer
	cur := store.Latest(co.runs[key], reviewer)
	co.mu.Unlock()
	if !ok {
		return agent.Job{}, fmt.Errorf("review %s: not in the inbox", key)
	}
	if cur != nil && cur.Status == "running" {
		return agent.Job{}, fmt.Errorf("%s is already reviewing %s", title, key)
	}
	if err := co.loggedIn(be.Name()); err != nil {
		return agent.Job{}, err
	}
	pr := it.PR
	job := agent.Job{
		Repo: pr.Repository.NameWithOwner, Number: pr.Number, Title: pr.Title,
		Author: pr.AuthorLogin(), Base: pr.BaseRefName, Head: pr.HeadRefOid,
		Body: pr.Body, Refine: refine, Viewer: viewer, Sync: sync, Agent: be.Name(), Profile: profile,
	}
	if pr.Local() {
		l, err := co.local(key)
		if err != nil {
			return agent.Job{}, err
		}
		if err := unchanged(l); refine == "" && err != nil {
			return agent.Job{}, err
		}
		job.Dir, job.Branch, job.Head = l.Dir, l.Branch, git(l.Dir, "rev-parse", "HEAD")
	}
	if refine != "" {
		if cur == nil {
			return agent.Job{}, fmt.Errorf("no %s review to revise yet", title)
		}
		if cur.Status == "failed" {
			return agent.Job{}, fmt.Errorf("%s's last review failed, so there is nothing to revise; start a new one", title)
		}
		if cur.Origin == "local" {
			return agent.Job{}, fmt.Errorf("%s reviewed this branch in your checkout before the PR; start a new review to revise", title)
		}
		job.RunID = cur.ID
	}
	return job, nil
}

// Discard deletes a review run and its notes; the reviewer's previous run,
// if any, is current again. A local review with no runs left is forgotten.
func (co *Core) Discard(runID int64) error {
	run, err := co.st.Run(runID)
	if err != nil {
		return fmt.Errorf("discard: %w", err)
	}
	if err := co.st.DeleteRun(runID); err != nil {
		return fmt.Errorf("discard: %w", err)
	}
	if err := co.st.ClearReviewLog(run.Key, run.Reviewer()); err != nil {
		return fmt.Errorf("discard: %w", err)
	}
	co.loadRuns()
	if _, err := co.local(run.Key); err == nil && len(co.Reviews()[run.Key]) == 0 {
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
	return co.editFindings(func(r *store.AgentReview) bool { return r.ID == runID }, func(i int, f store.Finding) bool {
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
	for _, r := range store.Current(co.runs[key]) {
		ids = append(ids, r.ID)
	}
	co.mu.Unlock()
	return co.editFindings(func(r *store.AgentReview) bool { return slices.Contains(ids, r.ID) }, func(_ int, f store.Finding) bool {
		return on && f.Severity != "must-fix"
	})
}

// editFindings sets Excluded on the findings of the runs that match.
func (co *Core) editFindings(match func(*store.AgentReview) bool, excluded func(int, store.Finding) bool) error {
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
			if err := co.st.SaveFindings(r.ID, r.Findings); err != nil {
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
	run := store.Latest(co.runs[key], reviewer)
	co.mu.Unlock()
	switch {
	case run != nil && run.Status == "running":
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
	var wt string
	if l, err := co.local(key); err == nil {
		wt = l.Dir
	} else {
		repo, n, err := parseKey(key)
		if err != nil {
			return Command{}, fmt.Errorf("chat: %w", err)
		}
		wt = agent.WorktreePath(repo, n, reviewer)
	}
	if _, err := os.Stat(wt); err != nil {
		return Command{}, fmt.Errorf("the review's worktree is gone; run the review again")
	}
	cmd := be.Chat(run.SessionID, wt, cfg.Agent.Model(be.Name()))
	if cmd == nil {
		return Command{}, fmt.Errorf("%s has no interactive session to chat in; ask it to revise instead", be.Title())
	}
	return commandOf(cmd, be.ChatQuit()), nil
}

// ReviewerTitle names a reviewer for people: "Codex", "Codex · adversarial".
func ReviewerTitle(reviewer string) string {
	name, profile, _ := strings.Cut(reviewer, ":")
	if profile == "" {
		return agent.Title(name)
	}
	return agent.Title(name) + " · " + profile
}

// quietAfter is how long a review can go without a tool call before it
// counts as writing: the agent is thinking or writing out its review.
const quietAfter = 5 * time.Second

// owlWords are what the owl is up to while the agent writes, in turn.
var owlWords = []string{
	"mulling it over", "preening the findings", "sharpening its talons", "perched and pondering",
	"ruffling its feathers", "turning its head all the way round", "blinking slowly", "hooting softly",
}

// Progress is what a running review is doing, for people: its step and the
// agent's latest action, or once the agent has gone quiet to write, what the
// owl is up to (plain "writing" with the owl turned off).
func (co *Core) Progress(r *store.AgentReview, now time.Time) string {
	if r.Activity == "" {
		return "starting"
	}
	step, _, _ := strings.Cut(r.Activity, ": ")
	quiet := now.Sub(r.ActivityAt)
	if r.ActivityAt.IsZero() || quiet < quietAfter || step == "preparing worktree" {
		return r.Activity
	}
	if !co.Config().Playful {
		return step + ": writing…"
	}
	return step + ": " + owlWords[int((quiet-quietAfter)/(4*time.Second))%len(owlWords)] + "…"
}
