package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
	"github.com/tmstorm/prowl/internal/inbox"
	"github.com/tmstorm/prowl/internal/store"
)

func newCore(t *testing.T, fake *ghtest.GitHub) (*core.Core, *store.Store) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	c, err := fake.Client()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return core.New(config.Default(), c, st), st
}

func refresh(t *testing.T, co *core.Core) core.Inbox {
	t.Helper()
	if _, err := co.Refresh(); err != nil {
		t.Fatal(err)
	}
	return co.Inbox()
}

func find(in core.Inbox, key string) (inbox.Item, bool) {
	i := slices.IndexFunc(in.Items, func(it inbox.Item) bool { return it.PR.Key() == key })
	if i < 0 {
		return inbox.Item{}, false
	}
	return in.Items[i], true
}

// A sync classifies the inbox, and the next start shows it before syncing.
func TestRefreshAndRestart(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 1, "fix: one", "alice"), "me")}}
	co, st := newCore(t, fake)
	if it, ok := find(refresh(t, co), "o/api#1"); !ok || it.Bucket != inbox.NeedsReview {
		t.Fatalf("want o/api#1 in needs review, got %+v", it)
	}
	c, _ := fake.Client()
	next := core.New(config.Default(), c, st).Inbox()
	if _, ok := find(next, "o/api#1"); !ok || next.Viewer != "me" {
		t.Fatalf("a new core should start from the last sync: %+v", next)
	}
}

// A PR opened by link stays through syncs, and leaves once it's merged.
func TestOpenByLink(t *testing.T) {
	fake := &ghtest.GitHub{Others: []gh.PR{ghtest.PR("infra", 5, "chore: release", "bob")}, Closed: map[string]bool{}}
	co, _ := newCore(t, fake)
	if _, err := co.Open("o/infra", 5); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(refresh(t, co), "o/infra#5"); !ok {
		t.Fatal("a PR opened by link left the inbox on the next sync")
	}
	fake.Closed["o/infra#5"] = true
	if err := co.RefreshPR("o/infra#5"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(co.Inbox(), "o/infra#5"); ok {
		t.Fatal("a merged PR stayed in the inbox")
	}
}

// Peeking at a repo brings in every open PR in it until Unpeek.
func TestPeek(t *testing.T) {
	co, _ := newCore(t, &ghtest.GitHub{Others: []gh.PR{ghtest.PR("infra", 5, "chore: release", "bob")}})
	co.Peek("o/infra")
	if it, ok := find(refresh(t, co), "o/infra#5"); !ok || it.Bucket != inbox.Unclaimed {
		t.Fatalf("want the peeked repo's PR as unclaimed, got %+v", it)
	}
	co.Unpeek()
	if _, ok := find(refresh(t, co), "o/infra#5"); ok {
		t.Fatal("the PR stayed after Unpeek")
	}
}

// A reviewed PR that isn't in the inbox keeps its history while it is open
// (opened by link, say) and loses it once merged, with the refs its reviews
// fetched into your checkout. Refs and worktrees of a PR prowl has no review
// of go too, whatever case the remote spells the repo in, and a checkout of
// a repo that isn't on GitHub keeps its refs.
func TestRefreshPrunesOnlyClosedPRs(t *testing.T) {
	path := os.Getenv("PATH")
	fake := &ghtest.GitHub{Others: []gh.PR{ghtest.PR("r", 1, "a", "bob"), ghtest.PR("r", 2, "b", "bob")},
		Closed: map[string]bool{"o/r#2": true}}
	co, st := newCore(t, fake)
	t.Setenv("PATH", path)
	for _, key := range []string{"o/r#1", "o/r#2"} {
		if _, err := st.StartRun(key, "h", "claude", "", 0); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	repo := filepath.Join(root, "r")
	_ = os.MkdirAll(repo, 0o755)
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "remote", "add", "origin", "https://github.com/O/R.git")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	for _, n := range []string{"1", "2", "3"} {
		gitIn(t, repo, "update-ref", "refs/prowl/pr-"+n+"-claude", "HEAD")
	}
	elsewhere := filepath.Join(root, "mirror")
	_ = os.MkdirAll(elsewhere, 0o755)
	gitIn(t, elsewhere, "init", "-q")
	gitIn(t, elsewhere, "remote", "add", "origin", "https://git.example.com/o/r.git")
	gitIn(t, elsewhere, "commit", "-q", "--allow-empty", "-m", "first")
	gitIn(t, elsewhere, "update-ref", "refs/prowl/pr-9-claude", "HEAD")
	orphan := agent.WorktreePath("o/r", 3, "claude")
	gitIn(t, repo, "worktree", "add", "-q", "--detach", orphan, "HEAD")
	if _, err := co.Update(func(c *config.Config) error { c.RepoRoots = []string{root}; return nil }); err != nil {
		t.Fatal(err)
	}
	refresh(t, co)
	if runs, _ := st.Runs("o/r#1"); len(runs) != 1 {
		t.Error("an open PR outside the inbox lost its review history")
	}
	if runs, _ := st.Runs("o/r#2"); len(runs) != 0 {
		t.Error("a merged PR kept its review history")
	}
	out, _ := exec.Command("git", "-C", repo, "for-each-ref", "--format=%(refname)", "refs/prowl/").Output()
	if refs := strings.TrimSpace(string(out)); refs != "refs/prowl/pr-1-claude\nrefs/prowl/pr-3-claude" {
		t.Errorf("after the sync, refs left: %q", refs)
	}
	co.PruneOrphans()
	out, _ = exec.Command("git", "-C", repo, "for-each-ref", "--format=%(refname)", "refs/prowl/").Output()
	if refs := strings.TrimSpace(string(out)); refs != "refs/prowl/pr-1-claude" {
		t.Errorf("refs left: %q", refs)
	}
	if _, err := os.Stat(orphan); err == nil {
		t.Error("the orphaned worktree is still there")
	}
	if out, _ := exec.Command("git", "-C", elsewhere, "for-each-ref", "refs/prowl/").Output(); len(out) == 0 {
		t.Error("a checkout of a repo not on GitHub lost its refs")
	}
}

// Refresh reports what changed since the last sync, even one made by an
// earlier run, so `prowl watch` can catch up on what happened while it was
// off.
func TestChangesSinceLastRun(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 1, "fix: one", "alice"), "me")}}
	co, st := newCore(t, fake)
	if changes, _ := co.Refresh(); len(changes) != 1 {
		t.Fatalf("the first sync ever should report the PR that needs you: %v", changes)
	}
	fake.PRs = append(fake.PRs, ghtest.Requested(ghtest.PR("api", 2, "fix: two", "alice"), "me"))
	c, _ := fake.Client()
	changes, err := core.New(config.Default(), c, st).Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].PR.Key() != "o/api#2" {
		t.Fatalf("want only the PR that arrived while prowl was off, got %v", changes)
	}
}

// Watching a repo saves it in the config and brings in all its open PRs.
func TestWatch(t *testing.T) {
	co, _ := newCore(t, &ghtest.GitHub{Others: []gh.PR{ghtest.PR("infra", 5, "chore: release", "bob")}})
	if err := co.Watch("o/infra", true); err != nil {
		t.Fatal(err)
	}
	if saved, _ := config.Load(); !slices.Contains(saved.WatchRepos, "o/infra") || !slices.Contains(co.Config().WatchRepos, "o/infra") {
		t.Fatalf("watch_repos not saved: %v", saved.WatchRepos)
	}
	if _, ok := find(refresh(t, co), "o/infra#5"); !ok {
		t.Fatal("a watched repo's PR is missing")
	}
	if err := co.Watch("o/infra", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(refresh(t, co), "o/infra#5"); ok {
		t.Fatal("the PR stayed after unwatching")
	}
}

// A snoozed PR is marked in the inbox and left out of what changed, until
// you unsnooze it.
func TestSnooze(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.PR("api", 1, "feat: mine", "me")}}
	co, _ := newCore(t, fake)
	refresh(t, co)
	if err := co.Snooze("o/api#1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, ok := co.Inbox().Snoozed["o/api#1"]; !ok {
		t.Fatal("not marked snoozed")
	}
	fake.PRs[0].ReviewDecision = "CHANGES_REQUESTED"
	if changes, _ := co.Refresh(); len(changes) != 0 {
		t.Fatalf("a snoozed PR was reported as changed: %v", changes)
	}
	if err := co.Unsnooze("o/api#1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := co.Inbox().Snoozed["o/api#1"]; ok {
		t.Fatal("still snoozed")
	}
	fake.PRs[0].ReviewDecision = "APPROVED"
	if changes, _ := co.Refresh(); len(changes) != 1 {
		t.Fatalf("once unsnoozed, the approval should be a change: %v", changes)
	}
}

// Claiming a PR requests you as reviewer on GitHub.
func TestClaim(t *testing.T) {
	fake := &ghtest.GitHub{Others: []gh.PR{ghtest.PR("infra", 5, "chore: release", "bob")}}
	co, _ := newCore(t, fake)
	if _, err := co.Open("o/infra", 5); err != nil {
		t.Fatal(err)
	}
	if err := co.Claim("o/infra#5"); err != nil {
		t.Fatal(err)
	}
	if !fake.Wrote("POST /repos/o/infra/pulls/5/requested_reviewers") {
		t.Fatal("no review request sent")
	}
}

// MarkViewed returns when you last looked at a PR.
func TestMarkViewed(t *testing.T) {
	co, _ := newCore(t, &ghtest.GitHub{})
	if first, _ := co.MarkViewed("o/api#1"); !first.IsZero() {
		t.Fatalf("never viewed, got %v", first)
	}
	if again, _ := co.MarkViewed("o/api#1"); again.IsZero() {
		t.Fatal("the first view wasn't recorded")
	}
}

// Detail and Diff fetch what the PR page shows; Diff can start from the
// commit you last reviewed.
func TestDetailAndDiff(t *testing.T) {
	p := ghtest.Requested(ghtest.PR("api", 1, "fix: one", "alice"), "me")
	p.Reviews.Nodes = []gh.Review{{Author: &gh.Actor{Login: "me"}, State: "COMMENTED", SubmittedAt: time.Now(),
		Commit: &struct {
			Oid string `json:"oid"`
		}{"old"}}}
	fake := &ghtest.GitHub{PRs: []gh.PR{p}, Bodies: map[int]string{1: "the description"},
		Diffs: map[int]string{1: "full diff"}, Compares: map[string]string{"old...abc1234": "since your review"}}
	co, _ := newCore(t, fake)
	refresh(t, co)
	if det, err := co.Detail("o/api#1"); err != nil || det.Body != "the description" {
		t.Fatalf("detail: %v %+v", err, det)
	}
	if d, err := co.Diff("o/api#1", false); err != nil || d != "full diff" {
		t.Fatalf("full diff: %v %q", err, d)
	}
	if d, err := co.Diff("o/api#1", true); err != nil || d != "since your review" {
		t.Fatalf("diff since review: %v %q", err, d)
	}
}

// Marking a file viewed, replying to a thread and resolving it go to GitHub.
func TestFilesAndThreads(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 1, "fix: one", "alice"), "me")}}
	co, _ := newCore(t, fake)
	refresh(t, co)
	if err := co.SetFileViewed("o/api#1", "a.go", true); err != nil {
		t.Fatal(err)
	}
	if err := co.Reply("thread-1", "done"); err != nil {
		t.Fatal(err)
	}
	if err := co.Resolve("thread-1", true); err != nil {
		t.Fatal(err)
	}
	if err := co.Resolve("thread-1", false); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"mutation markFileAsViewed", "mutation addPullRequestReviewThreadReply", "mutation resolveReviewThread", "mutation unresolveReviewThread"} {
		if !fake.Wrote(w) {
			t.Errorf("missing %s", w)
		}
	}
}

// OrgRepos lists every repo in your orgs.
func TestOrgRepos(t *testing.T) {
	co, _ := newCore(t, &ghtest.GitHub{Repos: []string{"o/api", "o/infra"}})
	cfg := config.Default()
	cfg.Orgs = []string{"o"}
	co.SetConfig(cfg)
	if got := co.OrgRepos(); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

// fakeClaude puts a claude on PATH that says it's logged in.
func fakeClaude(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho '{\"loggedIn\":true}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

// Run reports a review that progressed in the background, e.g. one an agent
// process finished.
func TestRunReportsReviews(t *testing.T) {
	co, st := newCore(t, &ghtest.GitHub{})
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go co.Run(ctx)
	id, _ := st.StartRun("o/api#1", "abc", "claude", "", 0)
	_ = st.FinishReview(id, "s1", "looks fine", "", nil)
	waitEvent(t, co, core.RunsChanged)
	if runs := co.Reviews()["o/api#1"]; len(runs) != 1 || runs[0].Status != "done" {
		t.Fatalf("reviews: %+v", runs)
	}
}

// A review can't start without its agent or git, nor revise a review that
// doesn't exist.
func TestReviewNeedsAgentAndRun(t *testing.T) {
	co, _ := newCore(t, &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 1, "fix: one", "alice"), "me")}})
	refresh(t, co)
	if _, err := co.Review("o/api#1", ""); err == nil || !strings.Contains(err.Error(), "not on your PATH") {
		t.Fatalf("want a missing agent, got %v", err)
	}
	fakeClaude(t)
	if _, err := co.Review("o/api#1", ""); err == nil || !strings.Contains(err.Error(), "AI reviews need git") {
		t.Fatalf("want a missing git, got %v", err)
	}
	if _, err := co.Revise("o/api#1", "claude", "drop the nit"); err == nil || !strings.Contains(err.Error(), "no Claude review to revise") {
		t.Fatalf("want nothing to revise, got %v", err)
	}
}

// Excluding findings is saved, one at a time or all but must-fix, and
// discarding a run removes it with its notes.
func TestFindingsAndDiscard(t *testing.T) {
	_, st := newCore(t, &ghtest.GitHub{})
	id, _ := st.StartRun("o/api#1", "abc", "claude", "", 0)
	_ = st.FinishReview(id, "s1", "summary", "", []store.Finding{{Severity: "must-fix", Title: "a"}, {Severity: "nit", Title: "b"}})
	_ = st.AddReviewLog("o/api#1", "claude", "you", "drop b")
	co := core.New(config.Default(), mustClient(t), st)
	if err := co.SetExcluded(id, 0, true); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.Run(id); !r.Findings[0].Excluded || r.Findings[1].Excluded {
		t.Fatalf("after excluding the first: %+v", r.Findings)
	}
	if err := co.MustFixOnly("o/api#1", true); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.Run(id); r.Findings[0].Excluded || !r.Findings[1].Excluded {
		t.Fatalf("must-fix only: %+v", r.Findings)
	}
	if len(co.Notes("o/api#1")["claude"]) != 1 {
		t.Fatal("the note is missing")
	}
	if err := co.Discard(id); err != nil {
		t.Fatal(err)
	}
	if len(co.Reviews()["o/api#1"]) != 0 || len(co.Notes("o/api#1")) != 0 {
		t.Fatal("the run or its notes survived the discard")
	}
}

// Chat is the command that resumes the review's session in its worktree.
func TestChat(t *testing.T) {
	_, st := newCore(t, &ghtest.GitHub{})
	fakeClaude(t)
	id, _ := st.StartRun("o/api#1", "abc", "claude", "", 0)
	if _, err := core.New(config.Default(), mustClient(t), st).Chat("o/api#1", "claude"); err == nil || !strings.Contains(err.Error(), "wait for the review") {
		t.Fatalf("a running review can't be chatted with: %v", err)
	}
	_ = st.FinishReview(id, "s1", "summary", "", nil)
	co := core.New(config.Default(), mustClient(t), st)
	wt := agent.WorktreePath("o/api", 1, "claude")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	spec, err := co.Chat("o/api#1", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(spec.Args, " ") != "claude --resume s1 --setting-sources user" || spec.Dir != wt || spec.Quit == "" {
		t.Fatalf("chat: %q in %s, quit %q", spec.Args, spec.Dir, spec.Quit)
	}

	bin := t.TempDir()
	_ = os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfg := config.Default()
	cfg.Agent.RunImages = map[string]string{"*": "golang:1.27"}
	spec, err = core.New(cfg, mustClient(t), st).Chat("o/api#1", "claude")
	if err != nil {
		t.Fatal(err)
	}
	if args := strings.Join(spec.Args, " "); !strings.Contains(args, `"args":["agent-tools","o/api",`+strconv.Quote(wt)+`]`) || !strings.Contains(args, "--allowedTools mcp__prowl__run") {
		t.Fatalf("a chat about a PR review gets the run tool on its worktree: %s", args)
	}
}

func mustClient(t *testing.T) *gh.Client {
	t.Helper()
	c, err := (&ghtest.GitHub{}).Client()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// An agent draft is one review from every agent's included findings: the
// main reviewer's summary as the body, PR-level findings under it, your
// pending comments last, and a change request when a finding is must-fix.
// Posting it marks what went out and keeps the draft beside your edit, and
// goes on the commit the agent reviewed even after the author pushed.
func TestAgentDraftAndSubmit(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")}}
	_, st := newCore(t, fake)
	claude, _ := st.StartRun("o/api#7", "abc1234", "claude", "", 0)
	_ = st.FinishReview(claude, "s1", "**Verdict: request changes**\n\nThe map write races.", "", []store.Finding{
		{Severity: "must-fix", Path: "a.go", Line: 10, Title: "nil map write", Body: "Init it."},
		{Severity: "nit", Path: "a.go", Line: 20, Title: "naming", Body: "Rename.", Excluded: true}})
	codex, _ := st.StartRun("o/api#7", "abc1234", "codex", "", 0)
	_ = st.FinishReview(codex, "s2", "codex summary", "", []store.Finding{{Severity: "should-fix", Title: "description is stale", Body: "Update it."}})
	c, _ := fake.Client()
	co := core.New(config.Default(), c, st)
	refresh(t, co)
	co.SetPending("o/api#7", []gh.DraftComment{{Path: "b.go", Line: 3, Side: "RIGHT", Body: "mine"}})

	d, err := co.AgentDraft("o/api#7")
	if err != nil {
		t.Fatal(err)
	}
	if d.Event != "REQUEST_CHANGES" || !strings.HasPrefix(d.Body, "The map write races.") || !strings.Contains(d.Body, "description is stale") {
		t.Fatalf("draft: %+v", d)
	}
	if len(d.Comments) != 2 || d.Comments[0].Path != "a.go" || d.Comments[1].Body != "mine" {
		t.Fatalf("comments: %+v", d.Comments)
	}

	fake.PRs[0].HeadRefOid = "def5678"
	refresh(t, co)
	d.Body = "Races on the map. Init it first."
	if _, err := co.Submit("o/api#7", d); err != nil {
		t.Fatal(err)
	}
	if rv := fake.Reviews(); len(rv) != 1 || rv[0]["event"] != "REQUEST_CHANGES" || rv[0]["commit_id"] != "abc1234" {
		t.Fatalf("sent: %v", rv)
	}
	if r, _ := st.Run(claude); r.Status != "posted" || !r.Findings[0].Posted || r.Findings[1].Posted {
		t.Fatalf("claude run after posting: %+v", r)
	}
	posts, _ := st.Posts(time.Time{})
	if len(posts) != 1 || !strings.HasPrefix(posts[0].Draft.Body, "The map write races.") || posts[0].Posted.Body != d.Body {
		t.Fatalf("posts: %+v", posts)
	}
	if len(co.Pending("o/api#7")) != 0 {
		t.Fatal("pending comments survived the review")
	}
}

// Your own review carries your pending comments.
func TestApproveWithPending(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")}}
	co, st := newCore(t, fake)
	refresh(t, co)
	co.SetPending("o/api#7", []gh.DraftComment{{Path: "b.go", Line: 3, Side: "RIGHT", Body: "nice"}})
	d := co.Draft("o/api#7", "APPROVE")
	if len(d.Comments) != 1 {
		t.Fatalf("draft: %+v", d)
	}
	if _, err := co.Submit("o/api#7", d); err != nil {
		t.Fatal(err)
	}
	if rv := fake.Reviews(); len(rv) != 1 || rv[0]["event"] != "APPROVE" {
		t.Fatalf("sent: %v", rv)
	}
	if posts, _ := st.Posts(time.Time{}); len(posts) != 0 {
		t.Fatal("your own review isn't an agent post")
	}
	if it, ok := find(co.Inbox(), "o/api#7"); !ok || it.Bucket != inbox.Approved {
		t.Fatalf("after approving, the PR should be approved at once: %+v", it)
	}
}

// summarizer puts a claude on PATH that answers a summary turn with text,
// counting its runs in the returned file, or fails when text is empty.
func summarizer(t *testing.T, text string) string {
	t.Helper()
	bin := t.TempDir()
	count := filepath.Join(bin, "runs")
	answer := `echo '{"type":"result","session_id":"x","structured_output":{"summary":"` + text + `"}}'`
	if text == "" {
		answer = "exit 1"
	}
	script := "#!/bin/sh\ncase \"$1\" in auth) echo '{\"loggedIn\":true}';; *) echo run >> " + count + "; cat > /dev/null\n" + answer + ";; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return count
}

// Summarize writes a PR's summary and keeps it.
func TestSummarize(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")}, Diffs: map[int]string{7: "diff --git a/a.go b/a.go\n"}}
	co, st := newCore(t, fake)
	refresh(t, co)
	summarizer(t, "Adds retries.")
	sum, err := co.Summarize("o/api#7")
	if err != nil || sum.Text != "Adds retries." || sum.HeadOid != "abc1234" {
		t.Fatalf("summary: %v %+v", err, sum)
	}
	if co.Summary("o/api#7") == nil || len(co.Summarizing()) != 0 {
		t.Fatal("the summary isn't kept, or still shows as being written")
	}
	if saved, _ := st.Summaries(); saved["o/api#7"] == nil {
		t.Fatal("the summary wasn't saved")
	}
}

// With auto_summary on, Run writes the summary of a PR that needs you and
// reports it. A summary that fails isn't tried again until the PR changes.
func TestAutoSummary(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")}, Diffs: map[int]string{7: "diff\n"}}
	co, _ := newCore(t, fake)
	cfg := config.Default()
	cfg.Agent.AutoSummary = true
	co.SetConfig(cfg)
	refresh(t, co)
	runs := summarizer(t, "")
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go co.Run(ctx)
	if e := waitEvent(t, co, core.SummaryChanged); e.Err == nil || e.Key != "o/api#7" {
		t.Fatalf("want a failed summary of o/api#7, got %+v", e)
	}
	time.Sleep(2500 * time.Millisecond)
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "run") != 1 {
		t.Fatalf("a failed summary was retried on the same commit: %q", b)
	}
	summarizer(t, "Written on its own.")
	fake.PRs[0].HeadRefOid = "def5678"
	refresh(t, co)
	if e := waitEvent(t, co, core.SummaryChanged); e.Err != nil || co.Summary("o/api#7").Text != "Written on its own." {
		t.Fatalf("after a push: %+v %+v", e, co.Summary("o/api#7"))
	}
}

// waitEvent waits for core's next event of kind, skipping others.
func waitEvent(t *testing.T, co *core.Core, kind core.EventKind) core.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-co.Events():
			if e.Kind == kind {
				return e
			}
		case <-deadline:
			t.Fatalf("no event of kind %d", kind)
		}
	}
}

// A logged-out agent shows in Accounts after a check, and can't review.
// Login gives the command that logs it in.
func TestLoggedOut(t *testing.T) {
	co, _ := newCore(t, &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 1, "fix: one", "alice"), "me")}})
	refresh(t, co)
	bin := t.TempDir()
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho '{\"loggedIn\":false}'; exit 1\n"), 0o755)
	t.Setenv("PATH", bin)
	co.CheckAuth()
	accts := co.Accounts()
	i := slices.IndexFunc(accts, func(a core.Account) bool { return a.Key == "claude" })
	if i < 0 || accts[i].Auth.LoggedIn || !accts[i].Auth.Known {
		t.Fatalf("accounts: %+v", accts)
	}
	if _, err := co.Review("o/api#1", ""); err == nil || !strings.Contains(err.Error(), "isn't logged in") {
		t.Fatalf("want a refused review, got %v", err)
	}
	if err := co.CheckAgent("claude"); err == nil || !strings.Contains(err.Error(), "isn't logged in") {
		t.Fatalf("CheckAgent should say claude is logged out: %v", err)
	}
	if err := co.CheckAgent("codex"); err == nil || !strings.Contains(err.Error(), "not on your PATH") {
		t.Fatalf("CheckAgent should say codex is missing: %v", err)
	}
	if login, err := co.Login("claude"); err != nil || login.Args[0] != "claude" {
		t.Fatalf("login: %v %+v", err, login)
	}
}

// Update saves and applies a setting; an edit to the file made outside
// prowl is picked up by Run and reported.
func TestConfigChanges(t *testing.T) {
	co, _ := newCore(t, &ghtest.GitHub{})
	if _, err := co.Update(func(c *config.Config) error { c.GroupBy = "repo"; return nil }); err != nil {
		t.Fatal(err)
	}
	if saved, _ := config.Load(); saved.GroupBy != "repo" || co.Config().GroupBy != "repo" {
		t.Fatalf("not saved and applied: %q %q", saved.GroupBy, co.Config().GroupBy)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go co.Run(ctx)
	time.Sleep(1100 * time.Millisecond)
	edited, _ := config.Load()
	edited.Theme = "nord"
	if err := edited.Save(); err != nil {
		t.Fatal(err)
	}
	if e := waitEvent(t, co, core.ConfigChanged); e.Err != nil || co.Config().Theme != "nord" {
		t.Fatalf("the edit wasn't picked up: %v %q", e.Err, co.Config().Theme)
	}
}

// Detail carries the head commit's checks, only the latest run of each, and
// CheckLog the lines of a failed Actions job's log leading up to its error,
// without timestamps, group markers or the cleanup after it.
func TestChecks(t *testing.T) {
	var log strings.Builder
	log.WriteString("2026-11-11T10:00:00.0000000Z ##[group]Run go test ./...\n")
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&log, "2026-11-11T10:00:01.0000000Z line %d\n", i)
	}
	log.WriteString("2026-11-11T10:00:02.0000000Z ##[endgroup]\n2026-11-11T10:00:02.0000000Z ##[error]Process completed with exit code 1.\n")
	log.WriteString("2026-11-11T10:00:03.0000000Z Post job cleanup.\n2026-11-11T10:00:03.0000000Z Cleaning up orphan processes\n")
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 1, "fix: one", "alice"), "me")},
		Checks: map[int][]any{1: {ghtest.CheckRun("test", "FAILURE", true, 99), ghtest.CheckRun("lint", "", false, 98),
			ghtest.CheckRun("docs", "SKIPPED", false, 0), rerun(ghtest.CheckRun("docs", "FAILURE", false, 0), -time.Hour)}},
		JobLogs: map[int64]string{99: log.String()}, JobSteps: map[int64]string{99: "Run tests"}}
	co, _ := newCore(t, fake)
	refresh(t, co)
	det, err := co.Detail("o/api#1")
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, c := range det.Checks {
		states[c.Name] = c.State
	}
	if len(det.Checks) != 3 || states["test"] != "FAIL" || states["lint"] != "PENDING" || states["docs"] != "SKIPPED" || !det.Checks[0].Required || det.Checks[0].JobID != 99 {
		t.Fatalf("checks: %+v", det.Checks)
	}
	step, lines, err := co.CheckLog("o/api#1", 99)
	if err != nil {
		t.Fatal(err)
	}
	if step != "Run tests" || len(lines) != 40 || lines[len(lines)-1] != "Process completed with exit code 1." || lines[0] != "line 22" {
		t.Fatalf("step %q, %d lines: %q ... %q", step, len(lines), lines[0], lines[len(lines)-1])
	}
}

// rerun moves a check run's start by d, e.g. to make it an older run.
func rerun(c map[string]any, d time.Duration) map[string]any {
	c["startedAt"] = c["startedAt"].(time.Time).Add(d)
	return c
}

// PostReply sends a drafted reply as you edited it, marks it sent and keeps
// the draft beside what you sent.
func TestPostReply(t *testing.T) {
	fake := &ghtest.GitHub{}
	co, st := newCore(t, fake)
	id, _ := st.StartRun("o/api#7", "abc", "claude", "", 0)
	_ = st.FinishReview(id, "s1", "fine", "", nil)
	_ = st.SaveReplies(id, []store.ThreadReply{{ThreadID: "t1", Path: "a.go", Line: 3, Body: "Thanks, looks right."}})
	if err := co.PostReply(id, "t1", "Thanks, the wrap helps."); err != nil {
		t.Fatal(err)
	}
	if !fake.Wrote("mutation addPullRequestReviewThreadReply") {
		t.Fatal("the reply wasn't sent")
	}
	if r, _ := st.Run(id); !r.Replies[0].Posted {
		t.Fatal("the draft isn't marked sent")
	}
	posts, _ := st.Posts(time.Time{})
	if len(posts) != 1 || posts[0].Draft.Comments[0].Body != "Thanks, looks right." || posts[0].Posted.Comments[0].Body != "Thanks, the wrap helps." {
		t.Fatalf("posts: %+v", posts)
	}
	if err := co.PostReply(id, "t9", "x"); err == nil {
		t.Fatal("a reply with no draft should fail")
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// localRepo is a checkout of o/api on branch feat/x: one commit past main,
// a change not committed yet, and new files not added, one named like a
// git option.
func localRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "api")
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	gitIn(t, dir, "init", "--quiet", "--initial-branch=main")
	gitIn(t, dir, "remote", "add", "origin", "git@github.com:o/api.git")
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "--quiet", "-m", "init")
	gitIn(t, dir, "checkout", "--quiet", "-b", "feat/x")
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	gitIn(t, dir, "commit", "--quiet", "-am", "feat: add A")
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() { panic(1) }\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "sub", "new file.go"), []byte("package sub\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "-p.txt"), []byte("dash\n"), 0o644)
	return dir
}

// A branch with nothing since it left the default branch, committed or not,
// has nothing to review: no agent runs and no local item appears. A new file
// is a change, which goes on to the agent (here an unconfigured one). Once
// the change is gone again, re-running the local review from prowl stops too.
func TestNothingToReview(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "api")
	_ = os.MkdirAll(dir, 0o755)
	gitIn(t, dir, "init", "--quiet", "--initial-branch=main")
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "--quiet", "-m", "init")
	path := os.Getenv("PATH")
	co, _ := newCore(t, &ghtest.GitHub{})
	t.Setenv("PATH", path)
	if _, err := co.ReviewLocal(dir, "", true); !errors.Is(err, core.ErrNothingToReview) || !strings.Contains(err.Error(), "main has no changes since") {
		t.Fatalf("want nothing to review, got %v", err)
	}
	if _, ok := find(co.Inbox(), "local:api@main"); ok {
		t.Error("nothing to review should leave no local item")
	}
	_ = os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a\n"), 0o644)
	if _, err := co.ReviewLocal(dir, "command", true); err == nil || errors.Is(err, core.ErrNothingToReview) {
		t.Fatal("a new file is a change to review")
	}
	_ = os.Remove(filepath.Join(dir, "b.go"))
	bin := t.TempDir()
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncase \"$1\" in auth) echo '{\"loggedIn\":true}';; *) exit 1;; esac\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+path)
	if _, err := co.Review("local:api@main", ""); !errors.Is(err, core.ErrNothingToReview) {
		t.Fatalf("re-running an unchanged local review: %v", err)
	}
}

// prowl review in a checkout reviews the branch in place, committed or not,
// and shows it under Mine as a local item with its own overview and diff.
// Once a PR of yours opens for the branch, the review moves to the PR (chat
// and revise need a new review there). From a git worktree it reviews that
// worktree's branch, and a local review whose branch is gone is forgotten.
func TestLocalReview(t *testing.T) {
	dir := localRepo(t)
	path := os.Getenv("PATH")
	fake := &ghtest.GitHub{}
	co, st := newCore(t, fake)
	bin := t.TempDir()
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte(`#!/bin/sh
case "$1" in auth) echo '{"loggedIn":true}'; exit;; esac
d="$(dirname "$0")"
pwd >> "$d/dirs"
echo "$@" >> "$d/args"
cat >> "$d/prompts"
echo '{"type":"result","session_id":"s1","structured_output":{"summary":"A panics.","verdict":"request_changes","findings":[{"severity":"must-fix","path":"a.go","line":3,"title":"A panics","body":"Remove the panic.","rule":""}],"note":"","points":[],"thread_replies":[],"guidelines_read":[],"skills_used":[]}}'
`), 0o755)
	_ = os.WriteFile(filepath.Join(bin, "notify-send"), []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+path)
	if _, err := co.Update(func(c *config.Config) error { c.Agent.RunImages = map[string]string{"*": "golang:1.27"}; return nil }); err != nil {
		t.Fatal(err)
	}

	key, err := co.ReviewLocal(filepath.Join(dir, "sub"), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if key != "local:o/api@feat/x" {
		t.Fatalf("key %q", key)
	}
	if runs := co.Reviews()[key]; len(runs) != 1 || runs[0].Status != "done" || len(runs[0].Findings) != 1 {
		t.Fatalf("runs: %+v", runs)
	}
	prompt := read(t, filepath.Join(bin, "prompts"))
	for _, want := range []string{"local changes on branch feat/x in o/api", "git diff ", "git status --short", "the `run` tool"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
	if args := read(t, filepath.Join(bin, "args")); !strings.Contains(args, `"agent-tools","o/api"`) || !strings.Contains(args, `"--local"]`) {
		t.Errorf("a local review gets the run tool on your checkout, with its own cache: %s", args)
	}
	if strings.Contains(prompt, "Points raised") {
		t.Error("a local review has no points raised by others")
	}
	real, _ := filepath.EvalSymlinks(dir)
	if d := strings.TrimSpace(read(t, filepath.Join(bin, "dirs"))); !strings.HasPrefix(d, real) {
		t.Errorf("the agent ran in %q, not the checkout", d)
	}

	it, ok := find(co.Inbox(), key)
	if !ok || it.Bucket != inbox.Local || !it.PR.Local() || it.PR.Title != "feat: add A" {
		t.Fatalf("local item: %+v", it)
	}
	det, _ := co.Detail(key)
	for _, want := range []string{"feat/x", "feat: add A", "sub/new file.go"} {
		if !strings.Contains(det.Body, want) {
			t.Errorf("overview is missing %q", want)
		}
	}
	diff, _ := co.Diff(key, false)
	for _, want := range []string{"+func A() { panic(1) }", "sub/new file.go", "+package sub", "b/-p.txt"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff is missing %q", want)
		}
	}

	mine := ghtest.PR("api", 9, "feat: add A", "me")
	mine.HeadRefName = "feat/x"
	fake.PRs = []gh.PR{mine}
	if _, ok := find(refresh(t, co), key); ok {
		t.Fatal("once its PR is open, the local review should move to it")
	}
	runs := co.Reviews()["o/api#9"]
	if len(runs) != 1 || runs[0].Origin != "local" || len(runs[0].Findings) != 1 {
		t.Fatalf("the PR should hold the local review: %+v", runs)
	}
	if _, err := co.Chat("o/api#9", "claude"); err == nil || !strings.Contains(err.Error(), "before the PR") {
		t.Fatalf("chat on a moved review: %v", err)
	}
	if _, err := co.Revise("o/api#9", "claude", "again"); err == nil || !strings.Contains(err.Error(), "before the PR") {
		t.Fatalf("revise on a moved review: %v", err)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, dir, "worktree", "add", "--quiet", "-b", "feat/y", wt)
	wtKey, err := co.ReviewLocal(wt, "", true)
	if err != nil || wtKey != "local:o/api@feat/y" {
		t.Fatalf("from a worktree: %q %v", wtKey, err)
	}
	gitIn(t, dir, "worktree", "remove", "--force", wt)
	gitIn(t, dir, "branch", "-D", "--quiet", "feat/y")
	if _, ok := find(refresh(t, co), wtKey); ok {
		t.Fatal("the local review outlived its branch")
	}
	if runs, _ := st.Runs(wtKey); len(runs) != 0 {
		t.Fatal("the gone branch's review history was kept")
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// While another process is prowl watch, it sends the notifications and a
// second prowl watch is turned away. One that stopped (or ended, which drops
// its lock) doesn't count, nor does a stale process ID left in the store, and
// the watcher itself never counts. A lock held only for a moment is waited
// out.
func TestWatchRunning(t *testing.T) {
	co, st := newCore(t, &ghtest.GitHub{})
	if co.WatchRunning() {
		t.Fatal("no watcher has run yet")
	}
	other := core.New(config.Default(), nil, st)
	if err := other.Watching(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(co.StopWatching)
	if !co.WatchRunning() || other.WatchRunning() {
		t.Fatal("the other watcher should count, for everyone but itself")
	}
	if err := co.Watching(); !errors.Is(err, core.ErrWatching) {
		t.Fatalf("a second prowl watch should be turned away: %v", err)
	}
	other.StopWatching()
	if co.WatchRunning() || st.Watcher() != 0 {
		t.Fatal("a watcher restarting on an update should give up its claim")
	}
	_ = st.SetWatcher(1)
	if co.WatchRunning() {
		t.Fatal("a stale process ID shouldn't count")
	}
	if err := co.Watching(); err != nil || co.WatchRunning() {
		t.Fatalf("this process as the watcher: %v", err)
	}

	co.StopWatching()
	probe := core.New(config.Default(), nil, st)
	if err := probe.Watching(); err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(300*time.Millisecond, probe.StopWatching)
	if err := co.Watching(); err != nil {
		t.Fatalf("a lock held for a moment, as a UI checking for a watcher does, should be waited out: %v", err)
	}
}

// An agent draft carries the draft replies you kept; submitting it posts
// the review, then sends each one in its thread. One you left out stays.
func TestDraftWithReplies(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")}}
	_, st := newCore(t, fake)
	id, _ := st.StartRun("o/api#7", "abc1234", "claude", "", 0)
	_ = st.FinishReview(id, "s1", "fine", "", nil)
	_ = st.SaveReplies(id, []store.ThreadReply{
		{ThreadID: "t1", Path: "a.go", Line: 3, To: "alice", Body: "Thanks."},
		{ThreadID: "t2", Path: "b.go", Line: 9, To: "alice", Body: "Not this one."}})
	c, _ := fake.Client()
	co := core.New(config.Default(), c, st)
	refresh(t, co)
	if err := co.SetReplyExcluded(id, "t2", true); err != nil {
		t.Fatal(err)
	}
	d, err := co.AgentDraft("o/api#7")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Replies) != 1 || d.Replies[0].ThreadID != "t1" {
		t.Fatalf("draft replies: %+v", d.Replies)
	}
	if _, err := co.Submit("o/api#7", d); err != nil {
		t.Fatal(err)
	}
	if !fake.Wrote("POST /repos/o/api/pulls/7/reviews") || !fake.Wrote("mutation addPullRequestReviewThreadReply") {
		t.Fatal("the review and its reply weren't both sent")
	}
	r, _ := st.Run(id)
	if !r.Replies[0].Posted || r.Replies[1].Posted || !r.Replies[1].Excluded || r.Unsent() != 0 {
		t.Fatalf("replies after submit: %+v", r.Replies)
	}
}

// Only the process that notifies moves the snapshot changes are measured
// from: while prowl watch runs, a UI's sync or prowl list doesn't use up a
// change before the watcher has notified it.
func TestOnlyTheNotifierTakesChanges(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 1, "fix: one", "alice"), "me")}}
	ui, st := newCore(t, fake)
	refresh(t, ui)
	c, _ := fake.Client()
	list := core.New(config.Default(), c, st)
	list.Quiet()
	fake.PRs = append(fake.PRs, ghtest.Requested(ghtest.PR("api", 2, "fix: two", "alice"), "me"))
	if changes, _ := list.Refresh(); len(changes) != 0 {
		t.Fatalf("prowl list reported changes: %v", changes)
	}

	other := core.New(config.Default(), c, st)
	if err := other.Watching(); err != nil {
		t.Fatal(err)
	}
	if changes, _ := ui.Refresh(); len(changes) != 0 {
		t.Fatalf("the UI took changes while a watcher runs: %v", changes)
	}
	other.StopWatching()

	watch := core.New(config.Default(), c, st)
	if err := watch.Watching(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(watch.StopWatching)
	if changes, _ := watch.Refresh(); len(changes) != 1 || changes[0].PR.Key() != "o/api#2" {
		t.Fatalf("the watcher should still see the new PR: %v", changes)
	}
}

// Each pending comment stays on the commit it was written on, through
// edits. Submitting comments from before and after a push posts one review
// per commit, the earlier first, so every comment lands on its own line. A
// failed review can't be revised.
func TestPendingCommitAndFailedRevise(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")}}
	co, st := newCore(t, fake)
	refresh(t, co)
	co.SetPending("o/api#7", []gh.DraftComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "one"}})
	fake.PRs[0].HeadRefOid = "def5678"
	refresh(t, co)
	co.SetPending("o/api#7", []gh.DraftComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "one, edited"}, {Path: "a.go", Line: 9, Side: "RIGHT", Body: "two"}})
	d := co.Draft("o/api#7", "APPROVE")
	if d.Comments[0].Commit != "abc1234" || d.Comments[1].Commit != "def5678" {
		t.Fatalf("commits: %q %q", d.Comments[0].Commit, d.Comments[1].Commit)
	}
	if _, err := co.Submit("o/api#7", d); err != nil {
		t.Fatal(err)
	}
	rv := fake.Reviews()
	if len(rv) != 2 || rv[0]["commit_id"] != "abc1234" || rv[0]["event"] != "COMMENT" || rv[1]["commit_id"] != "def5678" || rv[1]["event"] != "APPROVE" {
		t.Fatalf("reviews: %v", rv)
	}
	if c := rv[0]["comments"].([]any); len(c) != 1 || c[0].(map[string]any)["body"] != "one, edited" {
		t.Fatalf("the earlier review's comments: %v", rv[0]["comments"])
	}

	id, _ := st.StartRun("o/api#7", "abc1234", "claude", "", 0)
	_ = st.FailReview(id, "boom")
	c, _ := fake.Client()
	co = core.New(config.Default(), c, st)
	refresh(t, co)
	fakeClaude(t)
	if _, err := co.Revise("o/api#7", "claude", "again"); err == nil || !strings.Contains(err.Error(), "nothing to revise") {
		t.Fatalf("revising a failed review: %v", err)
	}
}

// When the review after an earlier-commit one fails, the comments that went
// out aren't pending any more, so posting again doesn't send them twice.
// With no comment on the latest commit, the newest comments carry the
// review instead of an empty one.
func TestSplitReviewRetryAndNoEmptyReview(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")},
		RejectReview: map[int]bool{2: true}}
	co, _ := newCore(t, fake)
	refresh(t, co)
	co.SetPending("o/api#7", []gh.DraftComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "on A"}})
	fake.PRs[0].HeadRefOid = "bbb2222"
	refresh(t, co)
	co.SetPending("o/api#7", append(co.Pending("o/api#7"), gh.DraftComment{Path: "a.go", Line: 5, Side: "RIGHT", Body: "on B"}))
	fake.PRs[0].HeadRefOid = "ccc3333"
	refresh(t, co)

	if sent, err := co.Submit("o/api#7", co.Draft("o/api#7", "COMMENT")); err == nil || sent.Review {
		t.Fatalf("the second review should fail and not count as sent: %+v %v", sent, err)
	}
	if p := co.Pending("o/api#7"); len(p) != 1 || p[0].Body != "on B" {
		t.Fatalf("pending after the failure: %+v", p)
	}
	if _, err := co.Submit("o/api#7", co.Draft("o/api#7", "COMMENT")); err != nil {
		t.Fatal(err)
	}
	rv := fake.Reviews()
	if len(rv) != 2 || rv[0]["commit_id"] != "abc1234" || rv[1]["commit_id"] != "bbb2222" || len(rv[1]["comments"].([]any)) != 1 {
		t.Fatalf("reviews: %v", rv)
	}
}

// A review that failed to post reopens with its text when you post the same
// event again, and never as a different one: an approval doesn't come back
// as a change request.
func TestUnsentReopensForItsEvent(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")},
		RejectReview: map[int]bool{1: true}}
	co, _ := newCore(t, fake)
	refresh(t, co)
	d := co.Draft("o/api#7", "APPROVE")
	d.Body = "Ship it."
	if _, err := co.Submit("o/api#7", d); err == nil {
		t.Fatal("the first review should fail")
	}
	if d := co.Draft("o/api#7", "REQUEST_CHANGES"); d.Body != "" || d.Reopened {
		t.Fatalf("a change request reopened the approval: %+v", d)
	}
	if d := co.Draft("o/api#7", "APPROVE"); d.Body != "Ship it." || !d.Reopened {
		t.Fatalf("the approval didn't reopen: %+v", d)
	}
}

// A draft emptied down to its replies sends the replies and no review, says
// it posted, and leaves the agent's review unposted.
func TestRepliesOnly(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")}}
	_, st := newCore(t, fake)
	id, _ := st.StartRun("o/api#7", "abc1234", "claude", "", 0)
	_ = st.FinishReview(id, "s1", "", "", nil)
	_ = st.SaveReplies(id, []store.ThreadReply{{ThreadID: "t1", Path: "a.go", Line: 3, Body: "Thanks."}})
	c, _ := fake.Client()
	co := core.New(config.Default(), c, st)
	refresh(t, co)
	d, err := co.AgentDraft("o/api#7")
	if err != nil {
		t.Fatal(err)
	}
	d.Body = ""
	sent, err := co.Submit("o/api#7", d)
	if err != nil || !sent.Review || sent.URL != "" {
		t.Fatalf("submitted %+v, %v", sent, err)
	}
	if len(fake.Reviews()) != 0 || !fake.Wrote("mutation addPullRequestReviewThreadReply") {
		t.Fatal("want the reply and no review")
	}
	if r, _ := st.Run(id); r.Status != "done" || !r.Replies[0].Posted {
		t.Fatalf("run %s, replies %+v", r.Status, r.Replies)
	}
}

// Closing posts the text as a plain comment, then closes the PR; with
// inline comments it posts them as a comment review first. A draft can be
// made ready again.
func TestCloseAndDraft(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me"),
		ghtest.Requested(ghtest.PR("api", 8, "feat: wip", "alice"), "me")}}
	co, _ := newCore(t, fake)
	refresh(t, co)
	if _, err := co.Submit("o/api#7", core.Draft{Event: "CLOSE", Body: "Superseded by #9."}); err != nil {
		t.Fatal(err)
	}
	if !fake.Wrote("mutation addComment") || !fake.Wrote("mutation closePullRequest") || len(fake.Reviews()) != 0 {
		t.Fatalf("close: comment and close, no review; reviews %v", fake.Reviews())
	}
	d := core.Draft{Event: "DRAFT", Body: "Not ready yet.", Comments: []gh.DraftComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "missing test"}}}
	if _, err := co.Submit("o/api#8", d); err != nil {
		t.Fatal(err)
	}
	if rv := fake.Reviews(); len(rv) != 1 || rv[0]["event"] != "COMMENT" || !fake.Wrote("mutation convertPullRequestToDraft") {
		t.Fatalf("draft with comments: %v", rv)
	}
	if err := co.SetDraft("o/api#8", false, ""); err != nil || !fake.Wrote("mutation markPullRequestReadyForReview") {
		t.Fatalf("ready for review: %v", err)
	}
}

// A note is a plain comment in the conversation: no review, and your
// pending inline comments stay pending. Asking for reviews takes logins
// and org/team names.
func TestNoteAndRequestReviews(t *testing.T) {
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")}}
	co, _ := newCore(t, fake)
	refresh(t, co)
	co.SetPending("o/api#7", []gh.DraftComment{{Path: "a.go", Line: 1, Side: "RIGHT", Body: "later"}})
	if _, err := co.Submit("o/api#7", core.Draft{Event: "NOTE", Body: "Is this still needed?"}); err != nil {
		t.Fatal(err)
	}
	if !fake.Wrote("mutation addComment") || len(fake.Reviews()) != 0 || len(co.Pending("o/api#7")) != 1 {
		t.Fatalf("note: reviews %v, pending %v", fake.Reviews(), co.Pending("o/api#7"))
	}
	if err := co.RequestReviews("o/api#7", []string{"@bob", " o/backend "}); err != nil {
		t.Fatal(err)
	}
	if !fake.Wrote("POST /repos/o/api/pulls/7/requested_reviewers") {
		t.Fatal("no review request sent")
	}
	if err := co.RequestReviews("o/api#7", []string{" ", ""}); err == nil {
		t.Fatal("asking nobody should fail")
	}
}

// A change request someone dismissed, then asked you again, is a re-review:
// the dismissal event says what the review was, and it wasn't an approval.
func TestDismissedChangeRequest(t *testing.T) {
	pr := ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")
	at := time.Now().Add(-time.Hour)
	var ti gh.TimelineItem
	_ = json.Unmarshal([]byte(`{"__typename":"ReviewDismissedEvent","previousReviewState":"CHANGES_REQUESTED","review":{"id":"r1"}}`), &ti)
	pr.Reviews.Nodes = []gh.Review{{ID: "r1", Author: &gh.Actor{Login: "me"}, State: "DISMISSED", SubmittedAt: at}}
	pr.TimelineItems.Nodes = []gh.TimelineItem{ti}
	co, _ := newCore(t, &ghtest.GitHub{PRs: []gh.PR{pr}})
	it, ok := find(refresh(t, co), "o/api#7")
	if !ok || it.Bucket != inbox.ReReview || it.Reason != "re-requested" {
		t.Fatalf("dismissed change request: %v %q", it.Bucket, it.Reason)
	}
}

// A running review shows the agent's latest action, and once the agent has
// gone quiet to write, what the owl is up to in that step, or plain
// "writing" with the owl off. Preparing the worktree is never mistaken for
// writing.
func TestProgress(t *testing.T) {
	co, _ := newCore(t, &ghtest.GitHub{})
	now := time.Now()
	at := func(act string, ago time.Duration) *store.AgentReview {
		return &store.AgentReview{Activity: act, ActivityAt: now.Add(-ago)}
	}
	if got := co.Progress(&store.AgentReview{}, now); got != "starting" {
		t.Errorf("no activity yet: %q", got)
	}
	if got := co.Progress(at("reviewing: reading a.go", time.Second), now); got != "reviewing: reading a.go" {
		t.Errorf("busy: %q", got)
	}
	if got := co.Progress(at("verifying findings: reading a.go", time.Minute), now); !strings.HasPrefix(got, "verifying findings: ") || strings.Contains(got, "a.go") || !strings.HasSuffix(got, "…") {
		t.Errorf("quiet: %q", got)
	}
	if got := co.Progress(at("preparing worktree", time.Minute), now); got != "preparing worktree" {
		t.Errorf("cloning: %q", got)
	}
	if _, err := co.Update(func(c *config.Config) error { c.Playful = false; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := co.Progress(at("reviewing: reading a.go", time.Minute), now); got != "reviewing: writing…" {
		t.Errorf("owl off: %q", got)
	}
}
