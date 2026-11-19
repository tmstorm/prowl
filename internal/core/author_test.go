package core_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
)

func thread(id, path string, line int, resolved bool, comments ...gh.Comment) gh.Thread {
	t := gh.Thread{ID: id, Path: path, Line: line, IsResolved: resolved}
	t.Comments.Nodes = comments
	return t
}

func comment(who, body string) gh.Comment {
	typ := "User"
	if strings.HasSuffix(who, "[bot]") {
		typ = "Bot"
	}
	return gh.Comment{Author: &gh.Actor{Login: who, Typename: typ}, Body: body, DiffHunk: "@@ -1,2 +1,2 @@\n-old\n+new"}
}

// A finding prowl posts carries a hidden marker with its severity and rule.
// On your own PR, prowl reads the reviews back from GitHub: each reviewer's
// latest verdict, changes requested first, and what to work through in
// order: prowl's must-fix findings, by marker or the older bold severity,
// then what a person asked for in a review requesting changes, should-fix
// findings, other comments, nits. Bots and resolved threads stay out
// unless all is set.
func TestReviewsOnGitHub(t *testing.T) {
	posted := core.ExportFindingBody(core.Finding{Severity: core.ShouldFix, Title: "Wrap the error", Body: "Add context.", Rule: `AGENTS.md: "errors"`})
	if !strings.HasSuffix(posted, `<!-- prowl:finding v=1 severity=should-fix rule="AGENTS.md: \"errors\"" -->`) {
		t.Fatalf("posted: %q", posted)
	}
	now := time.Now()
	review := func(who, state, body string, ago time.Duration) gh.Review {
		return gh.Review{Author: &gh.Actor{Login: who, Typename: "User"}, State: state, Body: body, SubmittedAt: now.Add(-ago)}
	}
	conv := func(who, body string, ago time.Duration) gh.Comment {
		c := comment(who, body)
		c.CreatedAt = now.Add(-ago)
		return c
	}
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.PR("api", 7, "fix: crash", "me"), ghtest.PR("api", 8, "fix: other", "alice")},
		PRReviews: map[int][]gh.Review{7: {
			review("bob", "COMMENTED", "First pass.", 3*time.Hour),
			review("bob", "CHANGES_REQUESTED", "Please split this.\n- "+strings.ReplaceAll(core.ExportFindingBody(core.Finding{Severity: core.Nit, Title: "Typo", Body: "In the title."}), "\n\n", " "), time.Hour),
			review("carol", "APPROVED", "", 2*time.Hour),
			{Author: &gh.Actor{Login: "ci[bot]", Typename: "Bot"}, State: "CHANGES_REQUESTED", Body: "Lint failed.", SubmittedAt: now.Add(-time.Hour)},
		}},
		Comments: map[int][]gh.Comment{7: {
			conv("deploy[bot]", "Preview building.", 3*time.Hour),
			conv("alice", "Looks close.", 2*time.Hour),
			conv("me", "Pushed a fix.", 90*time.Minute),
			conv("deploy[bot]", "Preview ready.", time.Hour),
		}},
		Checks: map[int][]any{7: {
			map[string]any{"__typename": "CheckRun", "name": "test", "status": "COMPLETED", "conclusion": "FAILURE", "title": "2 tests failed", "summary": "TestCache failed."},
			map[string]any{"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "SUCCESS",
				"annotations": map[string]any{"nodes": []any{map[string]any{"path": "a.go", "location": map[string]any{"start": map[string]any{"line": 4}}, "annotationLevel": "WARNING", "message": "unused variable"}}}},
			map[string]any{"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"},
		}},
		Threads: map[int][]gh.Thread{7: {
			thread("T1", "a.go", 3, false, comment("carol", "Rename this?")),
			thread("T2", "b.go", 9, false, comment("bob", posted)),
			thread("T3", "c.go", 1, false, comment("bob", "**must-fix:** Nil map\n\nIt panics."), comment("me", "Fixed in abc123.")),
			thread("T4", "d.go", 5, false, comment("bob", "Use this:\n```suggestion\nreturn nil\n```")),
			thread("T5", "e.go", 2, true, comment("bob", "Old point.")),
			thread("T6", "f.go", 2, false, comment("lint[bot]", "Style.")),
		}}}
	c, err := fake.Client()
	if err != nil {
		t.Fatal(err)
	}
	r, err := core.ReviewsOnGitHub(t.Context(), c, config.Default(), "o/api#7", false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, v := range r.Verdicts {
		got = append(got, fmt.Sprintf("%s %s %v", v.Reviewer, v.State, v.Bot))
	}
	if strings.Join(got, ", ") != "bob CHANGES_REQUESTED false, ci[bot] CHANGES_REQUESTED true, carol APPROVED false" {
		t.Fatalf("verdicts: %v", got)
	}
	got = nil
	for _, it := range r.Items {
		got = append(got, fmt.Sprintf("%s %s %s %s %s:%d %s", it.Source, it.Kind, it.Severity, it.Author, it.Path, it.Line, it.ThreadID))
	}
	want := []string{
		"thread finding must-fix bob c.go:1 T3",
		"check check  test :0 ",
		"review comment  bob :0 ",
		"review bot  ci[bot] :0 ",
		"thread comment  bob d.go:5 T4",
		"thread finding should-fix bob b.go:9 T2",
		"comment comment  alice :0 ",
		"thread comment  carol a.go:3 T1",
		"comment bot  deploy[bot] :0 ",
		"check check  lint :0 ",
		"thread bot  lint[bot] f.go:2 T6",
		"review finding nit bob :0 ",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("items:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if it := r.Items[5]; it.Rule != `AGENTS.md: "errors"` || strings.Contains(it.Body, "prowl:finding") || !strings.HasPrefix(it.Body, "**should-fix:** Wrap the error") {
		t.Errorf("the marked finding: %+v", it)
	}
	if it := r.Items[0]; len(it.Replies) != 1 || it.Replies[0] != "me: Fixed in abc123." || it.Hunk == "" {
		t.Errorf("replies and hunk: %+v", it)
	}
	if it := r.Items[4]; it.Suggestion != "return nil\n" {
		t.Errorf("suggestion: %q", it.Suggestion)
	}
	if it := r.Items[2]; it.Body != "Please split this." {
		t.Errorf("review body: %+v", it)
	}
	if it := r.Items[1]; !it.Failing || it.Body != "2 tests failed\n\nTestCache failed." {
		t.Errorf("failing check: %+v", it)
	}
	if it := r.Items[9]; it.Failing || !strings.Contains(it.Body, "a.go:4 warning: unused variable") {
		t.Errorf("check with warnings: %+v", it)
	}
	if it := r.Items[8]; it.Body != "Preview ready." {
		t.Errorf("a bot's older comment should be settled: %+v", it)
	}
	md := core.AuthorMarkdown(r)
	for _, w := range []string{"- bob: changes requested", "- ci[bot] (bot): changes requested", "## 1. must-fix, a finding prowl posted for bob, on `c.go:1`", "_thread T3_", "> me: Fixed in abc123.",
		"Suggested change, to replace `d.go:5`:", "## 2. the check test failed", "## 11. lint[bot], a bot, wrote on `f.go:2`"} {
		if !strings.Contains(md, w) {
			t.Errorf("markdown is missing %q:\n%s", w, md)
		}
	}
	b, _ := json.Marshal(r)
	for _, w := range []string{`"verdicts":[{"reviewer":"bob","state":"CHANGES_REQUESTED"}`, `"source":"thread","kind":"finding","thread_id":"T3","path":"c.go","line":1,"severity":"must-fix","author":"bob"`} {
		if !strings.Contains(string(b), w) {
			t.Errorf("json is missing %s:\n%s", w, b)
		}
	}

	if all, _ := core.ReviewsOnGitHub(t.Context(), c, config.Default(), "o/api#7", true); len(all.Items) != len(r.Items)+1 {
		t.Errorf("all should bring back the resolved thread: %d items", len(all.Items))
	}
	if theirs, err := core.ReviewsOnGitHub(t.Context(), c, config.Default(), "o/api#8", false); err != nil || theirs != nil {
		t.Errorf("someone else's PR: %+v %v", theirs, err)
	}
}

// A reviewer's verdict is their latest approval or changes request, as
// GitHub decides it: a later comment, such as the empty review a thread
// reply makes, doesn't undo it, and what they wrote since still counts. A
// finding prowl posted in a review body can run over several lines, a list
// in it included, while a bullet the reviewer wrote above it stays theirs.
func TestReviewsOnGitHubVerdict(t *testing.T) {
	now := time.Now()
	review := func(state, body string, ago time.Duration) gh.Review {
		return gh.Review{Author: &gh.Actor{Login: "bob", Typename: "User"}, State: state, Body: body, SubmittedAt: now.Add(-ago)}
	}
	finding := "- **must-fix:** Leak It never closes:\n```go\nf, _ := os.Open(p)\n```\n" + `<!-- prowl:finding v=1 severity=must-fix rule="AGENTS.md: close files" -->`
	listed := "- " + strings.ReplaceAll(core.ExportFindingBody(core.Finding{Severity: core.ShouldFix, Title: "Leaks", Body: "Both never close:\n- the file\n- the socket", Rule: "a --->b"}), "\n\n", " ")
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.PR("api", 7, "fix: crash", "me")},
		PRReviews: map[int][]gh.Review{7: {
			review("CHANGES_REQUESTED", "Two problems.\n- also rename Foo to Bar\n"+finding+"\n"+listed, 3*time.Hour),
			review("COMMENTED", "", 2*time.Hour),
			review("COMMENTED", "Also check the cache.", time.Hour),
		}}}
	c, err := fake.Client()
	if err != nil {
		t.Fatal(err)
	}
	r, err := core.ReviewsOnGitHub(t.Context(), c, config.Default(), "o/api#7", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Verdicts) != 1 || r.Verdicts[0].State != "CHANGES_REQUESTED" {
		t.Fatalf("verdicts: %+v", r.Verdicts)
	}
	var got []string
	for _, it := range r.Items {
		got = append(got, fmt.Sprintf("%s %s %q", it.Kind, it.Severity, it.Body))
	}
	want := []string{
		"finding must-fix \"**must-fix:** Leak It never closes:\\n```go\\nf, _ := os.Open(p)\\n```\"",
		"comment  \"Two problems.\\n- also rename Foo to Bar\"",
		"comment  \"Also check the cache.\"",
		"finding should-fix \"**should-fix:** Leaks Both never close:\\n- the file\\n- the socket _Per a --->b_\"",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("items:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if r.Items[0].Rule != "AGENTS.md: close files" || r.Items[3].Rule != "a ->b" {
		t.Errorf("rules: %q, %q", r.Items[0].Rule, r.Items[3].Rule)
	}
}
