package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/store"
)

func runExtension(t *testing.T, command string, timeout time.Duration, in agent.ExtensionInput) (agent.ExtensionOutput, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the extensions are shell scripts")
	}
	e := agent.ExtensionFor(config.Extension{Name: "lint", Command: command, Repos: []string{"*"}, Timeout: config.Duration{Duration: timeout}})
	return e.Run(context.Background(), in)
}

// An extension runs in the PR's worktree with the input on stdin, and its
// output comes back as it printed it.
func TestExtensionRuns(t *testing.T) {
	head := t.TempDir()
	in := agent.ExtensionInput{Repo: "acme/web", HeadDir: head, Head: "abc", Base: "def", ChangedFiles: []string{"a.go"},
		Since: "fed", ChangedSince: []string{"a.go"}, Previous: []agent.ExtensionFinding{{Severity: "nit", Title: "old", Path: "a.go", Line: 2}}}
	finding := `{"severity":"should-fix","title":"Slow","body":"b","rule":"perf","path":"","line":0}`
	out, err := runExtension(t, `cat > input.json; pwd > pwd; printf '{"prompt":"p","summary":"s","findings":[`+finding+`]}'`, time.Minute, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Prompt != "p" || out.Summary != "s" || len(out.Findings) != 1 || out.Findings[0].Title != "Slow" || out.Findings[0].Rule != "perf" {
		t.Fatalf("output: %+v", out)
	}
	var got agent.ExtensionInput
	if err := json.Unmarshal([]byte(read(filepath.Join(head, "input.json"))), &got); err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("stdin: %+v %v", got, err)
	}
	if wd, _ := filepath.EvalSymlinks(strings.TrimSpace(read(filepath.Join(head, "pwd")))); wd != mustEval(t, head) {
		t.Fatalf("ran in %s, not the worktree", wd)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A failing extension's error says which one, how it ended, and the end of
// what it printed on stderr.
func TestExtensionFails(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{`echo working >&2; echo boom >&2; exit 3`, "lint: exit status 3\nworking\nboom"},
		{`echo not json`, "lint: its output isn't the extension JSON"},
		{`printf '{"findings":[{"severity":"high","title":"t"}]}'`, `lint: finding 1 has severity "high"; use must-fix, should-fix, nit`},
	} {
		_, err := runExtension(t, tc.command, time.Minute, agent.ExtensionInput{HeadDir: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", tc.command, err, tc.want)
		}
	}
}

// An extension past its timeout stops with its children, even one holding
// its output open, and says it timed out.
func TestExtensionTimesOut(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "late")
	start := time.Now()
	_, err := runExtension(t, `(sleep 2; touch `+marker+`) | cat`, 200*time.Millisecond, agent.ExtensionInput{HeadDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "lint: timed out after 200ms") {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %s to stop", d)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the extension's child kept running")
	}
}

// fakeExtension is a shell command for an extension that counts its runs in
// dir/count, keeps each run's input in dir/in-N.json, and prints dir/out.json.
func fakeExtension(t *testing.T, dir, out string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "out.json"), []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	return `d=` + dir + `; n=$(($(cat $d/count 2>/dev/null || echo 0) + 1)); echo $n > $d/count; cat > $d/in-$n.json; cat $d/out.json`
}

func extensionRuns(dir string) int {
	n := 0
	_, _ = fmt.Sscan(read(filepath.Join(dir, "count")), &n)
	return n
}

func reviewJob(head string) agent.Job {
	return agent.Job{Repo: "o/api", Number: 7, Title: "feat: a", Author: "alice", Base: "main", Head: head, Viewer: "me", Agent: "claude"}
}

// The extensions enabled for the repo run before the reviewer, and what they
// found is part of the review: their prompt sections, a finding with no path
// for the reviewer to attach, which prowl puts back when the reviewer drops
// it, and one with a path, which prowl adds as is. A failing extension leaves
// the review going, with a note saying why. A revise uses what the review
// got, and runs nothing.
func TestExtensionsInReview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the extensions are shell scripts")
	}
	st, roots, _, head := setupRun(t)
	perf := t.TempDir()
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.Extensions = []config.Extension{
		{Name: "perf", Repos: []string{"o/api"}, Command: fakeExtension(t, perf, `{"prompt":"| benchmark | delta |","summary":"2 benchmarks compared",
"findings":[{"severity":"should-fix","title":"Parse is 30% slower","body":"b"},{"severity":"nit","title":"Allocates per call","body":"b","path":"a.go","line":1}]}`)},
		{Name: "broken", Repos: []string{"*"}, Command: "echo boom >&2; exit 1"},
		{Name: "web-only", Repos: []string{"o/web"}, Command: "echo should not run; exit 1"},
	}
	if err := agent.Run(t.Context(), cfg, st, nil, reviewJob(head)); err != nil {
		t.Fatal(err)
	}
	run := latestRun(t, st, "claude")
	if run == nil || run.Status != "done" {
		t.Fatalf("run: %+v", run)
	}
	var got []string
	for _, f := range run.Findings {
		got = append(got, fmt.Sprintf("%s %s %s:%d %s", f.ExtensionID, f.Severity, f.Path, f.Line, f.Title))
	}
	want := []string{" nit a.go:3 t", "perf-1 should-fix :0 Parse is 30% slower", "perf-2 nit a.go:1 Allocates per call"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	prompts := read(filepath.Join(filepath.Dir(roots), "bin", "prompts"))
	for _, want := range []string{"## Extensions", "### perf", "| benchmark | delta |", "perf-1 (should-fix, attach it): Parse is 30% slower",
		"perf-2 (nit, already at a.go:1, prowl adds it)"} {
		if !strings.Contains(prompts, want) {
			t.Errorf("the prompt is missing %q", want)
		}
	}
	if strings.Contains(prompts, "### broken") || strings.Contains(prompts, "web-only") {
		t.Error("a failed extension, or one not enabled for the repo, reached the prompt")
	}
	notes, _ := st.Notes("o/api#7", "claude")
	if !slices.ContainsFunc(notes, func(n store.Note) bool {
		return n.Text == "extensions: perf: 2 benchmarks compared · broken: failed: exit status 1"
	}) {
		t.Errorf("notes: %+v", notes)
	}

	revise := reviewJob(head)
	revise.Refine, revise.RunID = "drop the nit", run.ID
	if err := agent.Run(t.Context(), cfg, st, nil, revise); err != nil {
		t.Fatal(err)
	}
	if n := extensionRuns(perf); n != 1 {
		t.Fatalf("perf ran %d times", n)
	}
	if run := latestRun(t, st, "claude"); !slices.ContainsFunc(run.Findings, func(f store.Finding) bool { return f.ExtensionID == "perf-1" }) {
		t.Fatalf("the revise lost perf-1: %+v", run.Findings)
	}
}

// A finding the reviewer attached to a line keeps the extension's severity
// even when the reviewer lowered it.
func TestExtensionSeverityKept(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the extensions are shell scripts")
	}
	st, roots, bin, head := setupRun(t)
	attached := `{"summary":"ok","verdict":"comment","findings":[{"severity":"nit","path":"a.go","line":1,"title":"Slow parse","body":"b","rule":"","extension_id":"perf-1"}],"note":"","guidelines_read":[],"skills_used":[]}`
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat > /dev/null\necho '{\"type\":\"result\",\"session_id\":\"s1\",\"structured_output\":"+attached+"}'\n"), 0o755)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.Extensions = []config.Extension{{Name: "perf", Repos: []string{"*"},
		Command: fakeExtension(t, t.TempDir(), `{"findings":[{"severity":"must-fix","title":"Parse is 3x slower","body":"b"}]}`)}}
	if err := agent.Run(t.Context(), cfg, st, nil, reviewJob(head)); err != nil {
		t.Fatal(err)
	}
	run := latestRun(t, st, "claude")
	if len(run.Findings) != 1 || run.Findings[0].Severity != "must-fix" || run.Findings[0].Path != "a.go" {
		t.Fatalf("findings: %+v", run.Findings)
	}
}

// A review of a head another review is already running an extension on
// waits for it, says whose review runs it, and uses its result.
func TestExtensionWaitsForHolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the extensions are shell scripts")
	}
	st, roots, _, head := setupRun(t)
	perf := t.TempDir()
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.Extensions = []config.Extension{{Name: "perf", Repos: []string{"*"}, Command: fakeExtension(t, perf, `{"summary":"mine"}`)}}
	held, claimed, err := st.ClaimExtension("o/api#7", "perf", head, "codex", os.Getpid(), time.Hour, time.Time{})
	if err != nil || !claimed {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- agent.Run(context.Background(), cfg, st, nil, reviewJob(head)) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if run := latestRun(t, st, "claude"); run != nil && run.Activity == "waiting for perf, running in the codex review" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the review never said it was waiting for codex's perf run")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ok, _ := st.FinishExtension(held, `{"summary":"from codex"}`, ""); !ok {
		t.Fatal("couldn't finish the held run")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := extensionRuns(perf); n != 0 {
		t.Fatalf("perf ran %d times beside codex's run", n)
	}
	notes, _ := st.Notes("o/api#7", "claude")
	if !slices.ContainsFunc(notes, func(n store.Note) bool { return n.Text == "extensions: perf: from codex" }) {
		t.Errorf("notes: %+v", notes)
	}
}

// A re-review gives the extension its last run's findings and the files
// changed since. Its findings in files that didn't change are kept, and one
// with no path goes unless reported again. With nothing changed, the last
// run stands and the extension doesn't run.
func TestExtensionReReview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the extensions are shell scripts")
	}
	st, roots, _, head := setupRun(t)
	work := filepath.Join(filepath.Dir(roots), "work")
	perf := t.TempDir()
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.Extensions = []config.Extension{{Name: "perf", Repos: []string{"*"}, Command: fakeExtension(t, perf,
		`{"findings":[{"severity":"should-fix","title":"Slow a","body":"b","path":"a.go","line":1},{"severity":"should-fix","title":"Slower overall","body":"b"}]}`)}}
	if err := agent.Run(t.Context(), cfg, st, nil, reviewJob(head)); err != nil {
		t.Fatal(err)
	}
	push := func(file string) string {
		t.Helper()
		if file != "" {
			_ = os.WriteFile(filepath.Join(work, file), []byte("package a\n"), 0o644)
			git(t, work, "add", ".")
		}
		git(t, work, "commit", "--quiet", "--allow-empty", "-m", "more")
		git(t, work, "push", "--quiet", "-f", "origin", "HEAD:refs/pull/7/head")
		return git(t, work, "rev-parse", "HEAD")
	}
	_ = os.WriteFile(filepath.Join(perf, "out.json"), []byte(`{"findings":[]}`), 0o644)
	second := push("b.go")
	if err := agent.Run(t.Context(), cfg, st, nil, reviewJob(second)); err != nil {
		t.Fatal(err)
	}
	var in agent.ExtensionInput
	if err := json.Unmarshal([]byte(read(filepath.Join(perf, "in-2.json"))), &in); err != nil {
		t.Fatal(err)
	}
	if in.Since != head || strings.Join(in.ChangedSince, ",") != "b.go" || len(in.Previous) != 2 {
		t.Fatalf("re-review input: since %s, changed %v, previous %+v", in.Since, in.ChangedSince, in.Previous)
	}
	if fs := latestRun(t, st, "claude").Findings; !slices.ContainsFunc(fs, func(f store.Finding) bool { return f.Title == "Slow a" }) ||
		slices.ContainsFunc(fs, func(f store.Finding) bool { return f.Title == "Slower overall" }) {
		t.Fatalf("findings: %+v", fs)
	}

	third := push("")
	if err := agent.Run(t.Context(), cfg, st, nil, reviewJob(third)); err != nil {
		t.Fatal(err)
	}
	if n := extensionRuns(perf); n != 2 {
		t.Fatalf("perf ran %d times; nothing changed since the last run", n)
	}
	notes, _ := st.Notes("o/api#7", "claude")
	if !slices.ContainsFunc(notes, func(n store.Note) bool { return n.Text == "extensions: perf: unchanged since "+gh.Short(second) }) {
		t.Errorf("notes: %+v", notes)
	}
	if fs := latestRun(t, st, "claude").Findings; !slices.ContainsFunc(fs, func(f store.Finding) bool { return f.Title == "Slow a" }) {
		t.Fatalf("the unchanged run lost its findings: %+v", fs)
	}
}
