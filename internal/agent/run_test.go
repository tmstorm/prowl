package agent_test

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
	"github.com/tmstorm/prowl/internal/store"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// One review, start to finish: prowl checks the PR out from your clone into
// its own worktree, prompts the agent with the repo's guidelines, the
// profile's focus, your skills, what you dismissed before and how you edit,
// then verifies the draft in the same session and stores the result.
func TestRunReview(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.Skills = []string{"write-prose"}
	cfg.Agent.EditExamples = 1
	cfg.Agent.RunImages = map[string]string{"*": "golang:1.27"}
	job := agent.Job{Repo: "o/api", Number: 7, Title: "feat: a", Author: "alice", Base: "main", Head: head,
		Viewer: "me", Agent: "claude", Profile: "adversarial"}
	if err := agent.Run(t.Context(), cfg, st, nil, job); err != nil {
		t.Fatal(err)
	}

	run := latestRun(t, st, "claude:adversarial")
	if run == nil || run.Status != "done" || run.Model != "claude-test" || len(run.Findings) != 1 || run.SessionID != "s1" {
		t.Fatalf("stored run: %+v", run)
	}
	if log, _ := st.Notes("o/api#7", "claude:adversarial"); len(log) != 1 || log[0].Who != "agent" || log[0].Text != "I could not check the repo settings." {
		t.Errorf("the agent's note should reach Notes, never the summary: %+v", log)
	}
	if _, err := os.Stat(filepath.Join(agent.WorktreePath("o/api", 7, "claude:adversarial"), "a.go")); err != nil {
		t.Fatalf("the PR head wasn't checked out: %v", err)
	}
	turns := strings.Split(read(filepath.Join(bin, "prompts")), "=== turn")
	if len(turns) != 3 {
		t.Fatalf("want a review and a verification turn, got %d", len(turns)-1)
	}
	for _, want := range []string{"AGENTS.md", "Review focus: adversarial", "write-prose",
		"old dropped finding", "Anything for the reader alone goes in `note`", "This might possibly be a problem.", "This breaks on nil input."} {
		if !strings.Contains(turns[1], want) {
			t.Errorf("the review prompt is missing %q", want)
		}
	}
	if strings.Index(turns[1], "## How to review") > strings.Index(turns[1], "Review focus: adversarial") {
		t.Error("the profile's focus should come after the review instructions, so it wins")
	}
	if n := read(filepath.Join(bin, "notified")); !strings.Contains(n, "api#7: 1 finding\n") {
		t.Errorf("notification: %q", n)
	}
	if args := read(filepath.Join(bin, "args")); !strings.Contains(args, "--setting-sources\nuser\n--strict-mcp-config\n") || !strings.Contains(args, "Bash(git diff *--output*)") || !strings.Contains(args, "Bash(gh repo view:*)") {
		t.Error("a review must load only your Claude settings, no MCP server, and deny git --output")
	}
	wt := agent.WorktreePath("o/api", 7, "claude:adversarial")
	if args := read(filepath.Join(bin, "args")); !strings.Contains(args, `"args":["agent-tools","o/api",`+strconv.Quote(wt)+`]`) || !strings.Contains(args, "mcp__prowl__run") || !strings.Contains(turns[1], "the `run` tool") {
		t.Errorf("a PR review gets the run tool on its worktree, read-write:\n%s", args)
	}
	if !strings.Contains(turns[2], "verify your review") || !strings.Contains(read(filepath.Join(bin, "args")), "--resume\ns1") {
		t.Error("the second turn should verify the draft in the same session")
	}
}

// setupRun makes a PR (o/api#7) in a local origin with a clone under roots,
// and a fake claude in bin that records its arguments and prompts and
// answers with review. Desktop notifications are written to bin/notified.
func setupRun(t *testing.T) (st *store.Store, roots, bin, head string) {
	t.Helper()
	var err error
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(tmp, "claude"))
	t.Setenv("GH_CONFIG_DIR", filepath.Join(tmp, "gh"))
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	origin := filepath.Join(tmp, "origin.git")
	git(t, tmp, "init", "--quiet", "--bare", "--initial-branch=main", origin)
	work := filepath.Join(tmp, "work")
	git(t, tmp, "clone", "--quiet", origin, work)
	_ = os.WriteFile(filepath.Join(work, "AGENTS.md"), []byte("Every change needs a test.\n"), 0o644)
	git(t, work, "add", ".")
	git(t, work, "commit", "--quiet", "-m", "init")
	git(t, work, "push", "--quiet", "origin", "HEAD:main")
	_ = os.WriteFile(filepath.Join(work, "a.go"), []byte("package a\n"), 0o644)
	git(t, work, "add", ".")
	git(t, work, "commit", "--quiet", "-m", "change")
	git(t, work, "push", "--quiet", "origin", "HEAD:refs/pull/7/head")
	head = git(t, work, "rev-parse", "HEAD")
	roots = filepath.Join(tmp, "src")
	git(t, tmp, "clone", "--quiet", origin, filepath.Join(roots, "api"))
	git(t, filepath.Join(roots, "api"), "remote", "set-url", "origin", "https://github.com/o/api.git")
	git(t, filepath.Join(roots, "api"), "config", "url."+origin+".insteadOf", "https://github.com/o/api.git")

	skill := filepath.Join(tmp, "claude", "skills", "write-prose")
	_ = os.MkdirAll(skill, 0o755)
	_ = os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: write-prose\n---\nShort sentences.\n"), 0o644)

	bin = filepath.Join(tmp, "bin")
	_ = os.MkdirAll(bin, 0o755)
	_ = os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(filepath.Join(bin, "notify-send"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$(dirname \"$0\")/notified\"\n"), 0o755)
	_ = os.WriteFile(filepath.Join(bin, "osascript"), []byte(`#!/bin/sh
n="$(dirname "$0")/notified"
printf '%s\n' "$2" | sed -E 's/.* subtitle "(.*)"$/\1/' >> "$n"
printf '%s\n' "$2" | sed -E 's/^display notification "(.*)" with title .*/\1/' >> "$n"
`), 0o755)
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte(`#!/bin/sh
d="$(dirname "$0")"
printf '%s\n' "$@" >> "$d/args"
echo '=== turn' >> "$d/prompts"
cat >> "$d/prompts"
echo '{"type":"system","subtype":"init","model":"claude-test"}'
echo '{"type":"result","session_id":"s1","structured_output":`+review+`}'
`), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	st, err = store.Open(filepath.Join(tmp, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	old, _ := st.StartRun("o/api#7", "abc", "claude", "", 0)
	_ = st.FinishRun(old, "s0", "", "", []store.Finding{{Title: "old dropped finding", Path: "a.go", Line: 1, Excluded: true}})
	_ = st.AddPost("o/api#3", "u", store.Review{Comments: []store.Comment{{Path: "x.go", Line: 1, Body: "This might possibly be a problem."}}},
		store.Review{Comments: []store.Comment{{Path: "x.go", Line: 1, Body: "This breaks on nil input."}}})

	return st, roots, bin, head
}

// With nits off the schema doesn't offer them, the prompt doesn't define
// them, and a nit the agent returns anyway is dropped. Your review
// instructions replace prowl's.
func TestSeverityToggles(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.Nits = false
	cfg.Agent.ReviewInstructions = "Look only at error handling."
	if err := agent.Run(t.Context(), cfg, st, nil, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	if run := latestRun(t, st, "claude"); run == nil || len(run.Findings) != 0 {
		t.Fatalf("the nit should be dropped: %+v", run)
	}
	if args := read(filepath.Join(bin, "args")); !strings.Contains(args, `"enum": ["must-fix","should-fix"]`) {
		t.Errorf("the schema should offer only must-fix and should-fix:\n%s", args)
	}
	prompt := read(filepath.Join(bin, "prompts"))
	if !strings.Contains(prompt, "Look only at error handling.") || strings.Contains(prompt, "senior engineer") || strings.Contains(prompt, "- nit:") {
		t.Errorf("prompt:\n%s", prompt)
	}
}

// The run tool is opt-in: with no image for the repo the agent gets no tool
// server and the prompt doesn't offer one.
func TestRunToolOptIn(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.RunImages = map[string]string{"acme/web": "node:22"}
	if err := agent.Run(t.Context(), cfg, st, nil, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	if args := read(filepath.Join(bin, "args")); strings.Contains(args, "--mcp-config") || strings.Contains(args, "mcp__prowl__run") {
		t.Errorf("no image, yet the agent got the tool server:\n%s", args)
	}
	if strings.Contains(read(filepath.Join(bin, "prompts")), "`run` tool") {
		t.Error("no image, yet the prompt offers the run tool")
	}
}

// latestRun is reviewer's newest run on o/api#7, or nil.
func latestRun(t *testing.T, st *store.Store, reviewer string) *store.Run {
	t.Helper()
	runs, err := st.Runs("o/api#7")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.Reviewer() == reviewer {
			return r
		}
	}
	return nil
}

// A thread you are in where the author answered last is shown in full with
// its id, and the agent's draft reply to it is kept with where it is and
// what it answers. An unresolved thread you aren't in has an id too, so a
// sync can keep a reply you asked for there. A draft for a thread not
// listed is dropped.
func TestThreadReplies(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	thread := gh.Thread{ID: "t1", IsResolved: true, Path: "a.go", Line: 3}
	thread.Comments.Nodes = []gh.Comment{
		{Author: &gh.Actor{Login: "me"}, Body: "Return the error here.", CreatedAt: time.Now().Add(-time.Hour)},
		{Author: &gh.Actor{Login: "alice"}, Body: "Done, and I wrapped it with the path too.", CreatedAt: time.Now()}}
	other := gh.Thread{ID: "t2", Path: "b.go", Line: 9}
	other.Comments.Nodes = []gh.Comment{
		{Author: &gh.Actor{Login: "bob"}, Body: "Who approved this?", CreatedAt: time.Now().Add(-time.Hour)},
		{Author: &gh.Actor{Login: "alice"}, Body: "Nobody yet.", CreatedAt: time.Now()}}
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.PR("api", 7, "fix: a", "alice")}, Threads: map[int][]gh.Thread{7: {thread, other}}}
	c, err := fake.Client()
	if err != nil {
		t.Fatal(err)
	}
	answer := `{"summary":"ok","verdict":"request_changes","findings":[{"severity":"must-fix","path":"a.go","line":1,"title":"t","body":"b","rule":""}],"note":"","thread_replies":[` +
		`{"thread_id":"t1","body":"Thanks, the path helps."},{"thread_id":"nope","body":"made up"}],"guidelines_read":[],"skills_used":[]}`
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte(`#!/bin/sh
d="$(dirname "$0")"
echo '=== turn' >> "$d/prompts"
cat >> "$d/prompts"
echo '{"type":"result","session_id":"s1","structured_output":`+answer+`}'
`), 0o755)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	if err := agent.Run(t.Context(), cfg, st, c, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Viewer: "me", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	prompt := read(filepath.Join(bin, "prompts"))
	for _, want := range []string{"Threads waiting on me", "id `t1` · a.go:3 (resolved)", "wrapped it with the path too", "## Replies to threads", "id `t2` · b.go:9"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt is missing %q", want)
		}
	}
	run := latestRun(t, st, "claude")
	if run == nil || len(run.Replies) != 1 {
		t.Fatalf("replies: %+v", run)
	}
	if r := run.Replies[0]; r.ThreadID != "t1" || r.Path != "a.go" || r.Line != 3 || r.To != "alice" || r.Body != "Thanks, the path helps." {
		t.Fatalf("reply: %+v", r)
	}
	if n := read(filepath.Join(bin, "notified")); !strings.Contains(n, "api#7: 1 finding, 1 must-fix · 1 reply to send") {
		t.Errorf("notification: %q", n)
	}

	answer = strings.Replace(answer, `{"thread_id":"nope"`, `{"thread_id":"t2","body":"I approve it."},{"thread_id":"nope"`, 1)
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte(`#!/bin/sh
d="$(dirname "$0")"
echo '=== turn' >> "$d/prompts"
cat >> "$d/prompts"
echo '{"type":"result","session_id":"s1","structured_output":`+answer+`}'
`), 0o755)
	if err := agent.Run(t.Context(), cfg, st, c, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Viewer: "me", Agent: "claude",
		Refine: agent.SyncInstruction, Sync: true, RunID: run.ID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(filepath.Join(bin, "prompts")), "whether or not the thread is waiting on the reader") {
		t.Error("the sync doesn't say a reply can go to any listed thread")
	}
	run = latestRun(t, st, "claude")
	if len(run.Replies) != 2 {
		t.Fatalf("replies after the sync: %+v", run.Replies)
	}
	if r := run.Replies[1]; r.ThreadID != "t2" || r.Path != "b.go" || r.Line != 9 || r.To != "alice" || r.Last != "Nobody yet." || r.Body != "I approve it." {
		t.Fatalf("reply to a thread I'm not in: %+v", r)
	}
}

// What a check reported goes to the agent with everything else that was
// raised, and the points it checked are kept; one still open shows in the
// notification.
func TestPoints(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	failed := ghtest.CheckRun("axes", "FAILURE", false, 0)
	failed["title"] = "1 issue"
	failed["annotations"] = map[string]any{"nodes": []any{map[string]any{"path": "a.go", "message": "possible nil map write",
		"annotationLevel": "WARNING", "location": map[string]any{"start": map[string]any{"line": 1}}}}}
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.PR("api", 7, "fix: a", "alice")}, Checks: map[int][]any{7: {failed}}}
	c, err := fake.Client()
	if err != nil {
		t.Fatal(err)
	}
	answer := `{"summary":"ok","verdict":"comment","findings":[],"note":"","thread_replies":[],"pr_action":"close","pr_action_reason":"Superseded by #9.","points":[` +
		`{"who":"axes","path":"a.go","line":1,"point":"nil map write","verdict":"open","why":"m is never made."},` +
		`{"who":"me","path":"","line":0,"point":"add a test","verdict":"addressed","why":"a_test.go covers it."}],"guidelines_read":[],"skills_used":[]}`
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte(`#!/bin/sh
d="$(dirname "$0")"
cat >> "$d/prompts"
echo '{"type":"result","session_id":"s1","structured_output":`+answer+`}'
`), 0o755)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	if err := agent.Run(t.Context(), cfg, st, c, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Viewer: "me", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	prompt := read(filepath.Join(bin, "prompts"))
	for _, want := range []string{"### Checks", "- axes (fail): 1 issue", "a.go:1 warning: possible nil map write", "## Points raised"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt is missing %q", want)
		}
	}
	run := latestRun(t, st, "claude")
	if run == nil || len(run.Points) != 2 || run.Points[0].Verdict != "open" || run.Points[1].Why != "a_test.go covers it." {
		t.Fatalf("points: %+v", run)
	}
	if run.Action != "close" || run.ActionReason != "Superseded by #9." {
		t.Errorf("suggestion: %q %q", run.Action, run.ActionReason)
	}
	if n := read(filepath.Join(bin, "notified")); !strings.Contains(n, "api#7: no findings · 1 still open · suggests closing") {
		t.Errorf("notification: %q", n)
	}
}

// A revise that fails leaves the review as it was, still postable, with a
// note saying why.
func TestFailedReviseKeepsReview(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	id, _ := st.StartRun("o/api#7", head, "claude", "", 0)
	_ = st.FinishRun(id, "s1", "summary", "", []store.Finding{{Severity: "must-fix", Title: "keep me"}})
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho boom >&2\nexit 1\n"), 0o755)
	_ = st.RestartRun(id, 0)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	err := agent.Run(t.Context(), cfg, st, nil, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Agent: "claude", Refine: "drop it", RunID: id})
	if err == nil {
		t.Fatal("the revise should fail")
	}
	r, _ := st.Run(id)
	if r.Status != "done" || len(r.Findings) != 1 {
		t.Fatalf("after a failed revise: %s %+v", r.Status, r.Findings)
	}
	if log, _ := st.Notes("o/api#7", "claude"); len(log) == 0 || !strings.Contains(log[len(log)-1].Text, "revise failed") {
		t.Fatalf("no note about the failed revise: %+v", log)
	}
}

// Cancelling a review stops its agent and fails the run with "cancelled",
// with no notification, since you did it.
func TestCancelReview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the agent is a shell script")
	}
	st, roots, bin, head := setupRun(t)
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat >/dev/null\nsleep 60\n"), 0o755)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	id, _ := st.StartRun("o/api#7", head, "claude", "", 0)
	done := make(chan error, 1)
	go func() {
		done <- agent.Run(t.Context(), cfg, st, nil, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Agent: "claude", RunID: id})
	}()
	for r, _ := st.Run(id); r.Activity != "reviewing"; r, _ = st.Run(id) {
		time.Sleep(20 * time.Millisecond)
	}
	if ok, err := st.CancelRun(id); !ok || err != nil {
		t.Fatalf("cancel: %v, %v", ok, err)
	}
	select {
	case err := <-done:
		if err == nil || err.Error() != "cancelled" {
			t.Fatalf("run ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the review kept running")
	}
	if r, _ := st.Run(id); r.Status != "failed" || r.Error != "cancelled" {
		t.Fatalf("after cancelling: %s %q", r.Status, r.Error)
	}
	if n := read(filepath.Join(bin, "notified")); n != "" {
		t.Errorf("notified: %q", n)
	}
	if ok, _ := st.CancelRun(id); ok {
		t.Error("cancelled a run that had ended")
	}
}

// A detached job reads its job from the file Launch wrote, and the file
// goes away.
func TestReadJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "7.json")
	_ = os.WriteFile(path, []byte(`{"Repo":"o/api","Number":7,"Body":"`+strings.Repeat("x", 200_000)+`"}`), 0o600)
	j, err := agent.ReadJob(path)
	if err != nil || j.Repo != "o/api" || j.Number != 7 || len(j.Body) != 200_000 {
		t.Fatalf("job: %v %s#%d %d", err, j.Repo, j.Number, len(j.Body))
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the job file should be deleted once read")
	}
}

// A clone under repo_roots with the PR's repo name but another owner's
// origin is not used: prowl clones the PR's own repo instead.
func TestCloneMustMatchOrigin(t *testing.T) {
	st, roots, _, head := setupRun(t)
	git(t, filepath.Join(roots, "api"), "remote", "set-url", "origin", "https://github.com/someone-else/api.git")
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	err := agent.Run(t.Context(), cfg, st, nil, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Agent: "claude"})
	if err == nil || !strings.Contains(err.Error(), "gh repo clone o/api") {
		t.Fatalf("want prowl to clone o/api itself, got %v", err)
	}
}

// A review that can't be launched fails, so the next one isn't refused as
// already running.
func TestLaunchFailure(t *testing.T) {
	st, _, _, head := setupRun(t)
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config.DataDir(), "jobs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.Launch(st, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Agent: "claude"}); err == nil {
		t.Fatal("launch should fail when the job can't be written")
	}
	if run := latestRun(t, st, "claude"); run == nil || run.Status != "failed" {
		t.Fatalf("run: %+v", run)
	}
}

// Two reviewers starting at once both clone the repo; the one that finishes
// second uses the first one's clone instead of failing.
func TestCloneRace(t *testing.T) {
	st, _, bin, head := setupRun(t)
	_ = os.WriteFile(filepath.Join(bin, "gh"), []byte(`#!/bin/sh
o="$(dirname "$0")/../origin.git"
git clone --quiet "$o" "$4"
git clone --quiet "$o" "${4%.cloning-*}"
`), 0o755)
	if err := agent.Run(t.Context(), config.Default(), st, nil, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	if run := latestRun(t, st, "claude"); run == nil || run.Status != "done" {
		t.Fatalf("run: %+v", run)
	}
}

// When an automated reviewer ran again, the agent reads only its latest
// run: earlier ones are marked superseded and their points left out. An
// account in the bots setting counts as one too.
func TestSupersededBotRuns(t *testing.T) {
	for _, bot := range []*gh.Actor{{Login: "axes", Typename: "Bot"}, {Login: "ci-reviewer", Typename: "User"}} {
		st, roots, bin, head := setupRun(t)
		fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.PR("api", 7, "fix: a", "alice")}, Comments: map[int][]gh.Comment{7: {
			{ID: "c1", Author: bot, CreatedAt: time.Now().Add(-2 * time.Hour), Body: "OLD RUN: nil map write in a.go"},
			{ID: "c2", Author: bot, CreatedAt: time.Now().Add(-time.Hour), Body: "NEW RUN: no issues"},
		}}}
		c, err := fake.Client()
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.Default()
		cfg.RepoRoots = []string{roots}
		cfg.Bots = []string{"ci-reviewer"}
		if err := agent.Run(t.Context(), cfg, st, c, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Viewer: "me", Agent: "claude"}); err != nil {
			t.Fatal(err)
		}
		prompt := read(filepath.Join(bin, "prompts"))
		if strings.Contains(prompt, "OLD RUN") || !strings.Contains(prompt, "NEW RUN") || !strings.Contains(prompt, "superseded by its later comment") {
			t.Errorf("%s's old run should be marked superseded and left out:\n%s", bot.Login, prompt)
		}
	}
}

// On your own PR there is no review of yours on GitHub, so a re-review
// starts from the commit your last AI review saw, for instance the local
// review made before the PR was opened.
func TestOwnPRStartsFromLastAIReview(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	prev, _ := st.StartRun("o/api#7", head, "claude", "", 0)
	_ = st.FinishRun(prev, "s0", "earlier", "", nil)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	if err := agent.Run(t.Context(), cfg, st, nil, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Author: "me", Viewer: "me", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	p := read(filepath.Join(bin, "prompts"))
	if !strings.Contains(p, "This is me's own PR. An AI review for them last looked at it") {
		t.Errorf("the re-review should start from the last AI review:\n%s", p)
	}
	if strings.Contains(p, "Run only the tests") {
		t.Error("an agent that can't run commands shouldn't be told which tests to run")
	}
}

// An agent that can run commands tests only what changed on a re-review,
// with the repo's usual checks, rather than the whole suite again.
func TestReReviewTestsWhatChanged(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	prev, _ := st.StartRun("o/api#7", head, "claude", "", 0)
	_ = st.FinishRun(prev, "s0", "earlier", "", nil)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.RunImages = map[string]string{"*": "golang:1.27"}
	if err := agent.Run(t.Context(), cfg, st, nil, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Author: "me", Viewer: "me", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	if p := read(filepath.Join(bin, "prompts")); !strings.Contains(p, "Run only the tests for the code that changed since then, not the whole suite, with the checks the repo's own test command uses, such as the race detector.") {
		t.Errorf("the re-review should test only what changed:\n%s", p)
	}
}

// A re-review of a local branch tests only what changed too.
func TestLocalReReviewTestsWhatChanged(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	dir := filepath.Join(roots, "api")
	prev, _ := st.StartRun(gh.LocalKey("o/api", "main"), head, "claude", "", 0)
	_ = st.FinishRun(prev, "s0", "earlier", "", nil)
	cfg := config.Default()
	cfg.Agent.RunImages = map[string]string{"*": "golang:1.27"}
	if err := agent.Run(t.Context(), cfg, st, nil, agent.Job{Repo: "o/api", Dir: dir, Branch: "main", Base: "main", Viewer: "me", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	if p := read(filepath.Join(bin, "prompts")); !strings.Contains(p, "You reviewed this branch before") || !strings.Contains(p, "Run only the tests for the code that changed since then") {
		t.Errorf("the local re-review should test only what changed:\n%s", p)
	}
}

// A review GitHub dismissed after a push still counts as the reader's last
// review: the re-review starts from what they approved, not from an older
// request for changes.
func TestDismissedApproval(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	now := time.Now()
	requested := gh.Review{ID: "r1", Author: &gh.Actor{Login: "me"}, State: "CHANGES_REQUESTED", SubmittedAt: now.Add(-2 * time.Hour), Body: "Fix the reset."}
	requested.Commit = &struct {
		Oid string `json:"oid"`
	}{"1111111aaaa"}
	approved := gh.Review{ID: "r2", Author: &gh.Actor{Login: "me"}, State: "DISMISSED", SubmittedAt: now.Add(-time.Hour)}
	approved.Commit = &struct {
		Oid string `json:"oid"`
	}{head}
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.PR("api", 7, "fix: a", "alice")},
		PRReviews: map[int][]gh.Review{7: {requested, approved}},
		Timeline: map[int][]any{7: {map[string]any{"__typename": "ReviewDismissedEvent", "createdAt": now,
			"actor": map[string]any{"login": "alice"}, "previousReviewState": "APPROVED", "review": map[string]any{"id": "r2"}}}}}
	c, err := fake.Client()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	if err := agent.Run(t.Context(), cfg, st, c, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Viewer: "me", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	prompt := read(filepath.Join(bin, "prompts"))
	for _, want := range []string{"me, approved, later dismissed", "at commit " + gh.Short(head) + " (approved)", "GitHub has since dismissed that review"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt is missing %q", want)
		}
	}
}

// Removing a closed PR's review takes its worktrees and the refs its reviews
// fetched, and leaves every other PR's alone, including repo api-1's PR 2
// and PR 12, which a name pattern for api PR 1 could match.
func TestRemoveReview(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	gone := []string{agent.WorktreePath("o/api", 1, "claude"), agent.WorktreePath("o/api", 1, "claude:adversarial")}
	kept := []string{agent.WorktreePath("o/api-1", 2, "claude"), agent.WorktreePath("o/api", 12, "claude")}
	for _, d := range append(gone, kept...) {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	clone := filepath.Join(config.DataDir(), "clones", "o", "api")
	_ = os.MkdirAll(clone, 0o700)
	git(t, clone, "init", "-q")
	git(t, clone, "commit", "-q", "--allow-empty", "-m", "first")
	for _, ref := range []string{"pr-1-claude", "pr-1", "pr-12-claude"} {
		git(t, clone, "update-ref", "refs/prowl/"+ref, "HEAD")
	}
	agent.RemoveReview(config.Default(), "o/api", 1)
	for _, d := range gone {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("%s should be removed", d)
		}
	}
	for _, d := range kept {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s should stay: %v", d, err)
		}
	}
	if refs := strings.TrimSpace(git(t, clone, "for-each-ref", "--format=%(refname)", "refs/prowl/")); refs != "refs/prowl/pr-12-claude" {
		t.Errorf("refs left: %q", refs)
	}
}

// A review logs when it starts, each turn and how long it took, and how it
// ended: its counts, or the phase it failed in and why, with where its
// output is. Nothing from the PR's text reaches the log.
func TestRunLogs(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	var buf bytes.Buffer
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	job := agent.Job{Repo: "o/api", Number: 7, Title: "feat: secret title", Body: "a secret plan", Author: "alice", Base: "main", Head: head, Agent: "claude"}
	if err := agent.Run(t.Context(), cfg, st, nil, job); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`msg="review started" run=`, `pr=o/api#7 reviewer=claude head=` + gh.Short(head),
		`msg="turn done"`, "phase=reviewing", `phase="verifying findings"`, `msg="review done"`, "findings=1 must_fix=0"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the log is missing %q:\n%s", want, buf.String())
		}
	}

	buf.Reset()
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat > /dev/null\necho 'rate limited' >&2\nexit 1\n"), 0o755)
	if err := agent.Run(t.Context(), cfg, st, nil, job); err == nil {
		t.Fatal("the review should fail")
	}
	if !strings.Contains(buf.String(), `msg="review failed"`) || !strings.Contains(buf.String(), "phase=reviewing") ||
		!strings.Contains(buf.String(), "err=") || !strings.Contains(buf.String(), "output="+filepath.Join(config.DataDir(), "logs", "runs")) {
		t.Errorf("the failure isn't logged with its phase, cause and output:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "secret") {
		t.Errorf("the PR's text reached the log:\n%s", buf.String())
	}
}
