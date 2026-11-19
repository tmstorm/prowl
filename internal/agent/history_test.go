package agent_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// richPR is a made-up PR page with every kind of activity: people's and
// bots' reviews, a bot's superseded review and comment, a dismissed and a
// pending review, the reader's own review and later comment, threads
// waiting on the reader, unresolved, outdated and resolved threads, and
// failing, annotated and passing checks.
func richPR() *ghtest.GitHub {
	at := func(h int) time.Time { return time.Date(2026, 11, 10, h, 0, 0, 0, time.UTC) }
	who := func(login string) *gh.Actor {
		if strings.HasSuffix(login, "[bot]") {
			return &gh.Actor{Login: login, Typename: "Bot"}
		}
		return &gh.Actor{Login: login, Typename: "User"}
	}
	commit := func(oid string) *struct {
		Oid string `json:"oid"`
	} {
		return &struct {
			Oid string `json:"oid"`
		}{oid}
	}
	thread := func(id, path string, line int, resolved, outdated bool, cs ...gh.Comment) gh.Thread {
		t := gh.Thread{ID: id, Path: path, Line: line, IsResolved: resolved, IsOutdated: outdated}
		t.Comments.Nodes = cs
		return t
	}
	return &ghtest.GitHub{PRs: []gh.PR{ghtest.PR("api", 7, "fix: a", "alice")},
		PRReviews: map[int][]gh.Review{7: {
			{ID: "r1", Author: who("me"), State: "CHANGES_REQUESTED", SubmittedAt: at(1), Body: "Please wrap the errors.", Commit: commit("1111111aaaa")},
			{ID: "r2", Author: who("ci[bot]"), State: "COMMENTED", SubmittedAt: at(2), Body: "Coverage dropped 2%."},
			{ID: "r3", Author: who("bob"), State: "DISMISSED", SubmittedAt: at(3), Body: "Fine by me.", Commit: commit("2222222bbbb")},
			{ID: "r4", Author: who("ci[bot]"), State: "COMMENTED", SubmittedAt: at(5), Body: "Coverage is back."},
			{ID: "r5", Author: who("carol"), State: "COMMENTED", SubmittedAt: at(6)},
			{ID: "r6", Author: who("dave"), State: "PENDING", SubmittedAt: at(7), Body: "draft"},
			{ID: "r7", Author: who("carol"), State: "CHANGES_REQUESTED", SubmittedAt: at(8), Body: "The cache never empties.<!-- hidden -->", Commit: commit("3333333cccc")},
		}},
		Timeline: map[int][]any{7: {map[string]any{"__typename": "ReviewDismissedEvent", "createdAt": at(4),
			"actor": map[string]any{"login": "alice"}, "previousReviewState": "APPROVED", "review": map[string]any{"id": "r3"}}}},
		Comments: map[int][]gh.Comment{7: {
			{Author: who("alice"), CreatedAt: at(2), Body: "Wrapped them in 3333333."},
			{Author: who("lint[bot]"), CreatedAt: at(3), Body: "2 warnings."},
			{Author: who("me"), CreatedAt: at(4), Body: "Still missing one in store.go."},
			{Author: who("lint[bot]"), CreatedAt: at(9), Body: "0 warnings."},
		}},
		Checks: map[int][]any{7: {
			map[string]any{"__typename": "CheckRun", "name": "test", "status": "COMPLETED", "conclusion": "FAILURE", "title": "2 tests failed", "summary": "TestCache and TestRun failed."},
			map[string]any{"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "SUCCESS",
				"annotations": map[string]any{"nodes": []any{map[string]any{"path": "a.go", "location": map[string]any{"start": map[string]any{"line": 4}}, "annotationLevel": "WARNING", "message": "unused variable"}}}},
			map[string]any{"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"},
		}},
		Threads: map[int][]gh.Thread{7: {
			thread("T1", "a.go", 3, false, false, gh.Comment{Author: who("me"), CreatedAt: at(1), Body: "Wrap this."}, gh.Comment{Author: who("alice"), CreatedAt: at(2), Body: "Done."}),
			thread("T2", "b.go", 7, false, true, gh.Comment{Author: who("carol"), CreatedAt: at(8), Body: "Why a map here?"}),
			thread("T3", "c.go", 1, true, false, gh.Comment{Author: who("bob"), CreatedAt: at(3), Body: "Nit: name."}),
			thread("T4", "d.go", 9, false, false, gh.Comment{Author: who("me"), CreatedAt: at(1), Body: "Test this."}, gh.Comment{Author: who("ci[bot]"), CreatedAt: at(2), Body: "Covered."}),
			thread("T5", "e.go", 2, true, false, gh.Comment{Author: who("me"), CreatedAt: at(1), Body: "Rename."}, gh.Comment{Author: who("alice"), CreatedAt: at(2), Body: "Renamed."}),
		}}}
}

// The review history an AI reviewer reads is pinned, so a change to how
// prowl gathers a PR's activity, which the author's side reads too, can't
// change review prompts by accident.
func TestHistoryText(t *testing.T) {
	st, roots, bin, head := setupRun(t)
	c, err := richPR().Client()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	if err := agent.Run(t.Context(), cfg, st, c, agent.Job{Repo: "o/api", Number: 7, Base: "main", Head: head, Viewer: "me", Agent: "claude"}); err != nil {
		t.Fatal(err)
	}
	const intro = "## Review history\n\nThis is what is already on the PR page. Read it before reviewing, the way a reviewer would.\n\n"
	prompt := read(filepath.Join(bin, "prompts"))
	start := strings.Index(prompt, intro)
	if start < 0 {
		t.Fatalf("no review history in the prompt:\n%s", prompt)
	}
	section := prompt[start+len(intro):]
	section = section[:strings.Index(section, "\n## ")]
	golden := filepath.Join("testdata", "history.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(section), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if section != string(want) {
		t.Fatalf("history text changed:\n%s\nwant:\n%s", section, want)
	}
}
