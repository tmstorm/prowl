package tui_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
	"github.com/tmstorm/prowl/internal/store"
)

func withComment(n int, who, body string) *fakeGitHub {
	f := &fakeGitHub{PRs: []gh.PR{needsReview("api", n, "fix: crash")}, Bodies: map[int]string{n: "the description"},
		Comments: map[int][]gh.Comment{n: {{ID: "c1", Author: &gh.Actor{Login: who}, CreatedAt: time.Now().Add(-48 * time.Hour), Body: body}}}}
	return f
}

// za toggles an activity item both ways, and so does a click on its head.
func TestFoldActivity(t *testing.T) {
	a := newApp(t, withComment(1, "bob", "first paragraph\n\nsecond paragraph"))
	a.keys("enter")
	a.want("second paragraph")
	a.search("commented")
	a.keys("z", "a")
	a.wantNot("second paragraph")
	a.keys("z", "a")
	a.want("second paragraph")
	a.click("▾ bob")
	a.wantNot("second paragraph")
}

// Scrolling through a long description survives a sync.
func TestCursorSurvivesSync(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 40; i++ {
		body.WriteString("- bullet number " + strings.Repeat("x", i%7) + "\n")
	}
	f := &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}, Bodies: map[int]string{1: body.String()}}
	a := newApp(t, f)
	a.keys("enter")
	for i := 0; i < 20; i++ {
		a.keys("j")
	}
	before := a.cursor()
	a.refresh()
	if got := a.cursor(); got != before {
		t.Fatalf("a sync moved the cursor from %q to %q", before, got)
	}
}

// A sync that shows the PR changed refetches it, so the pane shows the change.
func TestSyncRefetchesChangedPR(t *testing.T) {
	f := &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}, Bodies: map[int]string{1: "old description"}}
	a := newApp(t, f)
	a.keys("enter")
	a.want("old description")
	f.Bodies[1] = "new description"
	f.PRs[0].UpdatedAt = time.Now()
	a.refresh()
	a.want("new description")
}

// The diff side by side: a removed line and the line replacing it share a
// row. A removed line starting with "--" stays a removed line.
func TestSplitDiff(t *testing.T) {
	diff := "diff --git a/q.sql b/q.sql\n--- a/q.sql\n+++ b/q.sql\n@@ -1,4 +1,3 @@\n ctx\n-old one\n+new one\n--- old note\n tail\n"
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}, Diffs: map[int]string{1: diff}})
	a.keys("enter", "3")
	a.want("- -- old note", "+ new one")
	a.keys("s")
	for _, l := range a.lines() {
		if strings.Contains(l, "old one") && strings.Contains(l, "new one") {
			return
		}
	}
	t.Fatalf("no row pairs old one with new one:\n%s", a.screen())
}

// Several agents share the AI review tab: a section each with when it ran
// and how long it took, findings both raised marked, earlier runs under History, and <space>cd in a section
// discards that agent's review only.
func TestMultiAgentReviews(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}})
	run := func(agent string, f ...store.Finding) {
		id, _ := a.st.StartRun("o/api#7", "abc1234", agent, "", 0)
		_ = a.st.FinishReview(id, "s-"+agent, agent+" says", "", f)
	}
	run("claude", store.Finding{Severity: "nit", Title: "old one", Path: "z.go", Line: 1})
	run("claude", store.Finding{Severity: "must-fix", Path: "a.go", Line: 10, Title: "nil map write"})
	run("codex", store.Finding{Severity: "should-fix", Path: "a.go", Line: 11, Title: "nil map"})
	a.cfg.Agent.Reviewers = []string{"claude", "codex"}
	a = reopen(a)
	a.keys("enter", "4")
	a.want("[4] AI review (2)", "▾ Claude", "▾ Codex", "also found by Codex", "also found by Claude",
		"1 of 2 findings will be posted", "History · 1 earlier run")
	runs, _ := a.st.Runs("o/api#7")
	a.want(store.Latest(runs, "codex").StartedAt.Format("Jan 2 15:04") + ", took 0s")
	a.before("▾ Claude", "▾ Codex")
	a.search("Codex says")
	a.keys("space", "c", "d")
	a.wantNot("Codex says")
	a.want("claude says")
}

// The AI review tab before any review: the owl, and how to start one.
func TestEmptyReviewTab(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}})
	a.keys("enter", "4")
	a.want("No AI review yet", "( O,O )")
}

// Approving asks in a popup; a click outside doesn't answer it, the yes
// button approves on GitHub.
func TestApproveConfirm(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}})
	a.keys("space", "a")
	a.want("Confirm", "approve api#7?")
	a.send(mouseAt(1, 1))
	if a.wrote("POST") {
		t.Fatal("a click outside the popup approved")
	}
	a.click("[ y  yes ]")
	if !a.wrote("POST /repos/o/api/pulls/7/reviews") || a.gh.Reviews()[0]["event"] != "APPROVE" {
		t.Fatalf("not approved: %v %v", a.gh.Reviews(), a.gh.Reviews())
	}
	a.want("APPROVED 1")
	a.wantNot("NEEDS REVIEW")
}

// A logged-out agent shows in the statusline and stops a review from starting.
func TestLoggedOutAgent(t *testing.T) {
	f := &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")},
		Bins: map[string]string{"claude": `echo '{"loggedIn":false}'; exit 1`}}
	a := newApp(t, f)
	a.want("Claude logged out")
	a.keys("space", "c", "c")
	a.want("isn't logged in")
}

// <space>ci opens the review's own session in a window: its output shows,
// typed keys reach it, and when it ends prowl offers to sync the findings
// and turns the mouse back on.
func TestChat(t *testing.T) {
	f := &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}, Bins: map[string]string{
		"claude": `case "$1" in auth) echo '{"loggedIn":true}';; --resume) echo ready; read x; echo "got $x"; read y;; esac`}}
	a := newApp(t, f)
	id, _ := a.st.StartRun("o/api#7", "abc1234", "claude", "", 0)
	_ = a.st.FinishReview(id, "s1", "summary", "", nil)
	if err := os.MkdirAll(agent.WorktreePath("o/api", 7, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	a = reopen(a)
	a.keys("enter", "space", "c", "i")
	a.waitFor("ready")
	a.paste("hello")
	a.keys("enter")
	a.waitFor("got hello")
	a.keys("enter")
	a.waitFor("chat ended")
	if a.mouseOn == 0 {
		t.Fatal("the mouse should be turned back on after the agent had the terminal")
	}
}

// Folding the description from inside it puts the cursor on its head.
func TestFoldFromInside(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 30; i++ {
		body.WriteString("- a bullet point\n")
	}
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}, Bodies: map[int]string{1: body.String()}})
	a.keys("enter", "j", "j", "j", "j", "j", "z", "c")
	if !strings.Contains(a.cursor(), "▸ Description") {
		t.Fatalf("cursor after zc: %q", a.cursor())
	}
}

// While an agent drafts its first review, the owl keeps watch in its tab.
// Its eyes blink with the clock, so the test looks for its feet.
func TestReviewRunningOwl(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}})
	id, _ := a.st.StartRun("o/api#7", "abc1234", "claude", "", 0)
	_ = a.st.SetReviewActivity(id, "reading a.go")
	a = reopen(a)
	a.keys("enter", "4")
	a.want("reading the PR", "===^===^===")
}

// + gives the PR pane the full width; 0 brings the inbox back.
func TestFocusMode(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}, Bodies: map[int]string{7: "the description"}})
	a.keys("+")
	if top := a.lines()[0]; !strings.HasPrefix(top, "╭─[1]") || strings.Contains(top, "[0]") {
		t.Fatalf("focus mode should show only the PR pane: %q", top)
	}
	a.want("the description")
	a.keys("+")
	a.want("[0]", "the description")
	a.keys("+", "0")
	if top := a.lines()[0]; !strings.HasPrefix(top, "╭─[0]") {
		t.Fatalf("0 should bring the inbox back: %q", top)
	}
	a.keys("l")
	a.want("[0]")
}

// <space>ca has the agent summarize the PR; the summary shows at the top of
// the Overview, and says when the PR has moved on since.
func TestAISummary(t *testing.T) {
	f := &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}, Bodies: map[int]string{7: "the description"},
		Diffs: map[int]string{7: "diff --git a/a.go b/a.go\n"},
		Bins: map[string]string{"claude": `case "$1" in auth) echo '{"loggedIn":true}';; *) while read -r l; do :; done
echo '{"type":"system","subtype":"init","model":"claude-haiku-5-5"}'
echo '{"type":"result","session_id":"x","structured_output":{"summary":"Adds retries to the uploader."}}';; esac`}}
	a := newApp(t, f)
	a.keys("enter")
	a.want("<space>ca writes an AI summary")
	a.keys("space", "c", "a")
	a.waitFor("Adds retries to the uploader.")
	a.want("AI summary · Haiku 5.5 · for abc1234")
	a.before("AI summary", "the description")
	f.PRs[0].HeadRefOid = "def5678"
	f.PRs[0].UpdatedAt = time.Now()
	a.refresh()
	a.want("new commits since, <space>ca to refresh")
}

// With auto_summary on, a PR that needs you gets its summary without asking.
func TestAutoSummary(t *testing.T) {
	f := &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}, Diffs: map[int]string{7: "diff --git a/a.go b/a.go\n"},
		Bins: map[string]string{"claude": `case "$1" in auth) echo '{"loggedIn":true}';; *) while read -r l; do :; done
echo '{"type":"result","session_id":"x","structured_output":{"summary":"Written on its own."}}';; esac`}}
	a := newApp(t, f, func(c *config.Config) { c.Agent.AutoSummary = true })
	a.keys("enter")
	a.waitFor("Written on its own.")
}

// Activity reads newest first: what's new since you last looked sits above
// the "you last looked" line, what you've seen below it, and the cursor
// opens on the newest new item.
func TestActivityNewestFirst(t *testing.T) {
	f := withComment(1, "bob", "the old comment")
	a := newApp(t, f)
	a.keys("enter")
	a.want("the old comment")
	f.Comments[1] = append(f.Comments[1], gh.Comment{ID: "c2", Author: &gh.Actor{Login: "carol"}, CreatedAt: time.Now().Add(5 * time.Second), Body: "the new comment"})
	a = reopen(a)
	a.keys("enter")
	a.before("carol", "you last looked", "bob")
	if !strings.Contains(a.cursor(), "carol") {
		t.Fatalf("the cursor should open on the new comment: %q", a.cursor())
	}
}

// A review dismissed after a push shows what it was in the activity.
func TestDismissedReviewActivity(t *testing.T) {
	f := &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")},
		PRReviews: map[int][]gh.Review{1: {{ID: "r1", Author: &gh.Actor{Login: "carol"}, State: "DISMISSED", SubmittedAt: time.Now().Add(-time.Hour)}}},
		Timeline: map[int][]any{1: {map[string]any{"__typename": "ReviewDismissedEvent", "createdAt": time.Now(),
			"previousReviewState": "APPROVED", "review": map[string]any{"id": "r1"}}}}}
	a := newApp(t, f)
	a.keys("enter")
	a.want("approved, since dismissed")
}

// The Overview lists CI: a failed check open with its reason, required ones
// marked, running ones a line each, the rest folded. gl on a failed Actions
// check shows the end of its log.
func TestChecks(t *testing.T) {
	failed := ghtest.CheckRun("test", "FAILURE", true, 99)
	failed["title"] = "1 test failed"
	failed["annotations"] = map[string]any{"nodes": []any{map[string]any{"path": "a.go", "message": "want 2, got 3",
		"annotationLevel": "FAILURE", "location": map[string]any{"start": map[string]any{"line": 42}}}}}
	f := &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")},
		Checks:  map[int][]any{1: {ghtest.CheckRun("build", "SUCCESS", true, 97), failed, ghtest.CheckRun("lint", "", false, 98)}},
		JobLogs: map[int64]string{99: "2026-11-11T10:00:01.0000000Z --- FAIL: TestSum\n"}, JobSteps: map[int64]string{99: "Run tests"}}
	a := newApp(t, f)
	a.keys("enter")
	a.want("Checks · 1 failed · 1 running · 1 passed", "✗ test · ci · required", "1 test failed", "a.go:42  want 2, got 3",
		"● lint · ci · running", "▸ ✓ 1 passed", "gl shows the end of the log")
	a.wantNot("build · ci")
	a.search("1 test failed")
	a.keys("g", "l")
	a.waitFor("--- FAIL: TestSum")
	a.want("Log · failed at Run tests")
}

// A reply an agent drafted shows under its review, on its thread, in the
// tab's count and the legend; x leaves it out and puts it back, c opens it
// filled in, and sending it posts the reply and marks it sent.
func TestDraftReply(t *testing.T) {
	thread := gh.Thread{ID: "t1", Path: "a.go", Line: 3}
	thread.Comments.Nodes = []gh.Comment{{ID: "c1", Author: &gh.Actor{Login: "me"}, Body: "Return the error."},
		{ID: "c2", Author: &gh.Actor{Login: "alice"}, Body: "Done, wrapped too."}}
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}, Threads: map[int][]gh.Thread{7: {thread}}})
	id, _ := a.st.StartRun("o/api#7", "abc1234", "claude", "", 0)
	_ = a.st.FinishReview(id, "s1", "fine", "", nil)
	_ = a.st.SaveReplies(id, []store.ThreadReply{{ThreadID: "t1", Path: "a.go", Line: 3, To: "alice", Last: "Done, wrapped too.", Body: "Thanks, looks right."}})
	a = reopen(a)
	a.want("replies to send")
	a.keys("enter")
	a.want("↩ draft reply", "AI review (↩1)")
	a.keys("4")
	a.want("1 reply goes out with it, in its thread", "Draft replies", "[x] ↩ draft a.go:3 · to alice",
		"you: Return the error.", "alice, ", "┃ Done, wrapped too.", "your reply:", "Thanks, looks right.")
	a.before("you: Return the error.", "┃ Done, wrapped too.", "your reply:", "Thanks, looks right.")
	a.search("Thanks, looks right")
	a.keys("x")
	a.want("[ ] ↩ left out a.go:3")
	a.wantNot("1 reply goes out", "(↩1)")
	a.keys("x")
	a.want("[x] ↩ draft a.go:3")
	a.keys("c")
	a.want("Reply on a.go:3")
	a.keys("ctrl+s")
	if !a.wrote("mutation addPullRequestReviewThreadReply") {
		t.Fatalf("the reply wasn't sent: %v", a.gh.Reviews())
	}
	a.want("✓ sent a.go:3")
	a.wantNot("1 reply goes out", "(↩1)")
}

// Points raised show under the review, what still needs attention first, your
// own as "you", and the count of those still open in the section's head.
func TestPoints(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}})
	id, _ := a.st.StartRun("o/api#7", "abc1234", "claude", "", 0)
	_ = a.st.FinishReview(id, "s1", "fine", "", nil)
	_ = a.st.SavePoints(id, []store.Point{
		{Who: "me", Point: "add a test", Verdict: "addressed", Why: "a_test.go covers it."},
		{Who: "axes[bot]", Path: "a.go", Line: 40, Point: "nil map write", Verdict: "open", Why: "m is never made."}})
	a = reopen(a)
	a.keys("enter", "4")
	a.want("1 still open", "Points raised", "● open · axes[bot] a.go:40", "m is never made.", "✓ addressed · you")
	a.before("● open", "✓ addressed")
}

// A local review shows under Mine as ⌂ with its branch, its Overview is the
// branch, and actions that need GitHub say there's no PR yet. Discarding
// its runs one by one works to the last, and then it leaves the list.
func TestLocalReview(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := filepath.Join(t.TempDir(), "api")
	run := func(args ...string) {
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	_ = os.MkdirAll(dir, 0o755)
	run("init", "--quiet", "--initial-branch=main")
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	run("add", ".")
	run("commit", "--quiet", "-m", "init")
	run("checkout", "--quiet", "-b", "feat/x")
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644)
	run("commit", "--quiet", "-am", "feat: add A")

	a := newApp(t, &fakeGitHub{Bins: map[string]string{"git": `exec ` + gitBin + ` "$@"`}})
	key := store.LocalKey("o/api", "feat/x")
	_ = a.st.SaveLocal(store.Local{Key: key, Repo: "o/api", Branch: "feat/x", Dir: dir, Base: "main", At: time.Now()})
	id, _ := a.st.StartRun(key, "abc1234", "claude", "", 0)
	_ = a.st.FinishReview(id, "s1", "A is fine.", "", nil)
	a = reopen(a)
	a.keys("4")
	a.want("LOCAL", "⌂", "feat: add A")
	a.keys("enter")
	a.want("api ⌂ feat/x", "⌂ feat/x → main", "Branch", "Local review of  feat/x", "reviewed just now")
	a.wantNot("Reviewers", "Activity")
	a.keys("space", "a")
	a.want("isn't on GitHub")
	for i := 0; i < 3; i++ {
		id, _ := a.st.StartRun(key, "abc1234", "claude", "", 0)
		_ = a.st.FinishReview(id, "s1", fmt.Sprintf("run %d", i), "", nil)
	}
	a = reopen(a)
	a.keys("4", "enter", "4")
	for i := 0; i < 4; i++ {
		a.keys("space", "c", "d")
		a.want("discarded Claude's review")
	}
	a.wantNot("feat: add A")
}

// A review that suggests closing the PR says so in its section head and
// above its findings, with the keys that do it.
func TestSuggestsClosing(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}})
	id, _ := a.st.StartRun("o/api#7", "abc1234", "claude", "", 0)
	_ = a.st.FinishReview(id, "s1", "Superseded.", "", nil)
	_ = a.st.SaveAction(id, "close", "Closing in favor of #9, which does the same with a migration.")
	a = reopen(a)
	a.keys("enter", "4")
	a.want("suggests closing", "Suggests closing this PR", "<space>X, or event: close in <space>p", "Closing in favor of #9")
}

// S on a diff line starts a suggested change holding that line; <leader>A
// asks someone for a review.
func TestSuggestAndAskForReview(t *testing.T) {
	diff := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n ctx line\n+return nil\n"
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}, Diffs: map[int]string{7: diff}})
	a.keys("enter", "3")
	a.search("return nil")
	a.keys("S")
	a.want("Comment on a.go:2", "```suggestion")
	a.keys("esc")
	a.want("pending comment saved")
	a.keys("space", "A")
	a.want("Ask for reviews on api#7")
	a.paste("bob")
	a.keys("ctrl+s")
	if !a.wrote("POST /repos/o/api/pulls/7/requested_reviewers") {
		t.Fatal("no review request sent")
	}
}
