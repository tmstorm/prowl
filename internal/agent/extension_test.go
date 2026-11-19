package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
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
	e := agent.ExtensionFor(config.Default(), config.Extension{Name: "lint", Command: command, Repos: []string{"*"}, Timeout: config.Duration{Duration: timeout}})
	return e.Run(context.Background(), in)
}

// An extension runs in the PR's worktree with the input on stdin, and its
// output comes back as it printed it.
func TestExtensionRuns(t *testing.T) {
	head := t.TempDir()
	in := agent.ExtensionInput{Repo: "acme/web", HeadDir: head, Head: "abc", Base: "def", ChangedFiles: []string{"a.go"},
		Since: "fed", ChangedSince: []string{"a.go"}, Previous: []agent.ExtensionFinding{{Severity: "nit", Title: "old", Path: "a.go", Line: 2}}}
	finding := `{"severity":"should-fix","title":"Slow","body":"b","rule":"speed","path":"","line":0}`
	out, err := runExtension(t, `cat > input.json; pwd > pwd; printf '{"prompt":"p","summary":"s","findings":[`+finding+`]}'`, time.Minute, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Prompt != "p" || out.Summary != "s" || len(out.Findings) != 1 || out.Findings[0].Title != "Slow" || out.Findings[0].Rule != "speed" {
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
		{Name: "speed", Repos: []string{"o/api"}, Command: fakeExtension(t, perf, `{"prompt":"| benchmark | delta |","summary":"2 benchmarks compared",
"findings":[{"severity":"should-fix","title":"Parse is 30% slower","body":"b"},{"severity":"nit","title":"Allocates per call","body":"b","path":"a.go","line":1}]}`)},
		{Name: "broken", Repos: []string{"*"}, Command: "echo boom >&2; exit 1"},
		{Name: "web-only", Repos: []string{"o/web"}, Command: "echo should not run; exit 1"},
	}
	var logged bytes.Buffer
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	if err := agent.Run(t.Context(), cfg, st, nil, reviewJob(head)); err != nil {
		t.Fatal(err)
	}
	run := latestRun(t, st, "claude")
	if run == nil || run.Status != "done" {
		t.Fatalf("run: %+v", run)
	}
	for _, want := range []string{`msg="extension done"`, "extension=speed", "findings=2", `msg="extension failed"`, "extension=broken",
		`msg="extension finding put back" run=`, "id=speed-1"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the log is missing %q:\n%s", want, logged.String())
		}
	}
	var got []string
	for _, f := range run.Findings {
		got = append(got, fmt.Sprintf("%s %s %s:%d %s", f.ExtensionID, f.Severity, f.Path, f.Line, f.Title))
	}
	want := []string{" nit a.go:3 t", "speed-1 should-fix :0 Parse is 30% slower", "speed-2 nit a.go:1 Allocates per call"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	prompts := read(filepath.Join(filepath.Dir(roots), "bin", "prompts"))
	for _, want := range []string{"## Extensions", "### speed", "| benchmark | delta |", "speed-1 (should-fix, attach it): Parse is 30% slower",
		"speed-2 (nit, already at a.go:1, prowl adds it)"} {
		if !strings.Contains(prompts, want) {
			t.Errorf("the prompt is missing %q", want)
		}
	}
	if strings.Contains(prompts, "### broken") || strings.Contains(prompts, "web-only") {
		t.Error("a failed extension, or one not enabled for the repo, reached the prompt")
	}
	notes, _ := st.Notes("o/api#7", "claude")
	if !slices.ContainsFunc(notes, func(n store.Note) bool {
		return n.Text == "extensions: speed: 2 benchmarks compared · broken: failed: exit status 1"
	}) {
		t.Errorf("notes: %+v", notes)
	}

	revise := reviewJob(head)
	revise.Refine, revise.RunID = "drop the nit", run.ID
	if err := agent.Run(t.Context(), cfg, st, nil, revise); err != nil {
		t.Fatal(err)
	}
	if n := extensionRuns(perf); n != 1 {
		t.Fatalf("speed ran %d times", n)
	}
	if run := latestRun(t, st, "claude"); !slices.ContainsFunc(run.Findings, func(f store.Finding) bool { return f.ExtensionID == "speed-1" }) {
		t.Fatalf("the revise lost speed-1: %+v", run.Findings)
	}
}

// A finding the reviewer attached to a line keeps the extension's severity
// even when the reviewer lowered it.
func TestExtensionSeverityKept(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the extensions are shell scripts")
	}
	st, roots, bin, head := setupRun(t)
	attached := `{"summary":"ok","verdict":"comment","findings":[{"severity":"nit","path":"a.go","line":1,"title":"Slow parse","body":"b","rule":"","extension_id":"speed-1"}],"note":"","guidelines_read":[],"skills_used":[]}`
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat > /dev/null\necho '{\"type\":\"result\",\"session_id\":\"s1\",\"structured_output\":"+attached+"}'\n"), 0o755)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.Extensions = []config.Extension{{Name: "speed", Repos: []string{"*"},
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
	cfg.Agent.Extensions = []config.Extension{{Name: "speed", Repos: []string{"*"}, Command: fakeExtension(t, perf, `{"summary":"mine"}`)}}
	held, claimed, err := st.ClaimExtension("o/api#7", "speed", head, "codex", os.Getpid(), time.Hour, time.Time{})
	if err != nil || !claimed {
		t.Fatal(err)
	}
	var logged syncBuffer
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	done := make(chan error, 1)
	go func() { done <- agent.Run(context.Background(), cfg, st, nil, reviewJob(head)) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if run := latestRun(t, st, "claude"); run != nil && run.Activity == "waiting for speed, running in the codex review" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the review never said it was waiting for codex's speed run")
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
		t.Fatalf("speed ran %d times beside codex's run", n)
	}
	if !strings.Contains(logged.String(), `msg="waiting for extension"`) || !strings.Contains(logged.String(), "holder=codex") ||
		!strings.Contains(logged.String(), `msg="extension result reused"`) {
		t.Errorf("log: %s", logged.String())
	}
	notes, _ := st.Notes("o/api#7", "claude")
	if !slices.ContainsFunc(notes, func(n store.Note) bool { return n.Text == "extensions: speed: from codex" }) {
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
	cfg.Agent.Extensions = []config.Extension{{Name: "speed", Repos: []string{"*"}, Command: fakeExtension(t, perf,
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
		t.Fatalf("speed ran %d times; nothing changed since the last run", n)
	}
	notes, _ := st.Notes("o/api#7", "claude")
	if !slices.ContainsFunc(notes, func(n store.Note) bool { return n.Text == "extensions: speed: unchanged since "+gh.Short(second) }) {
		t.Errorf("notes: %+v", notes)
	}
	if fs := latestRun(t, st, "claude").Findings; !slices.ContainsFunc(fs, func(f store.Finding) bool { return f.Title == "Slow a" }) {
		t.Fatalf("the unchanged run lost its findings: %+v", fs)
	}
}

// An on-demand extension stays out of reviews. Run as a job, its finding
// with a path joins the main reviewer's AI review, the rest stay in its
// result, and a notification says how it went. One that fails partway keeps
// what it printed beside the error.
func TestExtensionOnDemand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the extensions are shell scripts")
	}
	st, roots, bin, head := setupRun(t)
	factory := t.TempDir()
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.Extensions = []config.Extension{{Name: "factory", Repos: []string{"o/api"}, OnDemand: true, Key: "f",
		Command: fakeExtension(t, factory, `{"summary":"job 12 started","findings":[{"severity":"should-fix","title":"Flaky test","body":"b","path":"a.go","line":1},{"severity":"nit","title":"Slow job","body":"b"}]}`)}}
	if err := agent.Run(t.Context(), cfg, st, nil, reviewJob(head)); err != nil {
		t.Fatal(err)
	}
	if n := extensionRuns(factory); n != 0 {
		t.Fatalf("an on-demand extension ran in a review %d times", n)
	}

	runJob := func() *store.ExtensionJob {
		t.Helper()
		id, err := st.StartExtensionJob("o/api#7", "factory", head, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		j := reviewJob(head)
		j.Extension, j.ExtensionJob = "factory", id
		_ = agent.Run(t.Context(), cfg, st, nil, j)
		jobs, _ := st.ExtensionJobs()
		return jobs["o/api#7"][0]
	}
	var logged bytes.Buffer
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	job := runJob()
	if job.Status != "done" || job.Added != 1 || !strings.Contains(job.Output, "job 12 started") {
		t.Fatalf("job: %+v", job)
	}
	if !strings.Contains(logged.String(), `msg="extension job done"`) || !strings.Contains(logged.String(), "extension=factory") || !strings.Contains(logged.String(), "added=1") {
		t.Errorf("log: %s", logged.String())
	}
	if again := runJob(); again.Status != "done" || again.Added != 0 {
		t.Fatalf("a second run added its finding again: %+v", again)
	}
	reviewed := latestRun(t, st, "claude")
	_ = st.MarkPosted(reviewed.ID, "https://github.com/o/api/pull/7#review")
	_ = os.WriteFile(filepath.Join(factory, "out.json"), []byte(`{"findings":[{"severity":"nit","title":"After posting","body":"b","path":"a.go","line":1}]}`), 0o644)
	if late := runJob(); late.Added != 0 {
		t.Fatalf("findings joined a posted review: %+v", late)
	}
	if runs, _ := st.Runs("o/api#7"); slices.ContainsFunc(runs, func(r *store.Run) bool {
		return slices.ContainsFunc(r.Findings, func(f store.Finding) bool { return f.Title == "After posting" })
	}) {
		t.Fatal("findings joined an older review")
	}
	fs := latestRun(t, st, "claude").Findings
	if !slices.ContainsFunc(fs, func(f store.Finding) bool { return f.ExtensionID == "factory-1" && f.Path == "a.go" }) ||
		slices.ContainsFunc(fs, func(f store.Finding) bool { return f.Title == "Slow job" }) {
		t.Fatalf("review findings: %+v", fs)
	}
	if n := read(filepath.Join(bin, "notified")); !strings.Contains(n, "factory finished") || !strings.Contains(n, "o/api#7: job 12 started · 1 added to the AI review") {
		t.Errorf("notification: %q", n)
	}

	cfg.Agent.Extensions[0].Command = `printf '{"summary":"pushed the branch","findings":[{"severity":"nit","title":"Half done","body":"b","path":"a.go","line":2}]}'; echo "deploy failed" >&2; exit 2`
	job = runJob()
	if job.Status != "failed" || !strings.Contains(job.Output, "pushed the branch") || !strings.Contains(job.Error, "exit status 2\ndeploy failed") || job.Added != 0 {
		t.Fatalf("failed job: %+v", job)
	}
	if fs := latestRun(t, st, "claude").Findings; slices.ContainsFunc(fs, func(f store.Finding) bool { return f.Title == "Half done" }) {
		t.Fatalf("a failed run added its findings to the review: %+v", fs)
	}
	if n := read(filepath.Join(bin, "notified")); !strings.Contains(n, "factory failed") || !strings.Contains(n, "o/api#7: exit status 2") {
		t.Errorf("notification: %q", n)
	}
}

// An on-demand job's deadline counts from when the extension starts, not
// from launch, so a slow clone doesn't make a live job look timed out.
func TestExtensionJobDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the extensions are shell scripts")
	}
	st, roots, _, head := setupRun(t)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.Extensions = []config.Extension{{Name: "factory", Repos: []string{"*"}, OnDemand: true, Command: `sleep 2; printf '{}'`}}
	id, err := st.StartExtensionJob("o/api#7", "factory", head, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	j := reviewJob(head)
	j.Extension, j.ExtensionJob = "factory", id
	done := make(chan error, 1)
	go func() { done <- agent.Run(context.Background(), cfg, st, nil, j) }()
	for {
		if jobs, _ := st.ExtensionJobs(); jobs["o/api#7"][0].Status == "running" {
			break
		}
		select {
		case <-done:
			t.Fatal("the job read as timed out the whole time the extension ran")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// syncBuffer is a buffer a log can write to from another goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// An extension learns from local whether head_dir is a PR's worktree or your
// own checkout, in a review and when run on demand.
func TestExtensionInputLocal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the extensions are shell scripts")
	}
	st, roots, _, head := setupRun(t)
	speed, deploy := t.TempDir(), t.TempDir()
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.Extensions = []config.Extension{
		{Name: "speed", Repos: []string{"o/api"}, Command: fakeExtension(t, speed, `{}`)},
		{Name: "deploy", Repos: []string{"o/api"}, OnDemand: true, Command: fakeExtension(t, deploy, `{}`)},
	}
	local := agent.Job{Repo: "o/api", Dir: filepath.Join(roots, "api"), Branch: "main", Base: "main", Viewer: "me", Agent: "claude"}
	local.Head = git(t, local.Dir, "rev-parse", "HEAD")
	for _, j := range []agent.Job{reviewJob(head), local} {
		if err := agent.Run(t.Context(), cfg, st, nil, j); err != nil {
			t.Fatal(err)
		}
	}
	id, err := st.StartExtensionJob(local.Key(), "deploy", local.Head, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	onDemand := local
	onDemand.Extension, onDemand.ExtensionJob = "deploy", id
	if err := agent.Run(t.Context(), cfg, st, nil, onDemand); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		filepath.Join(speed, "in-1.json"): false, filepath.Join(speed, "in-2.json"): true, filepath.Join(deploy, "in-1.json"): true,
	} {
		var in agent.ExtensionInput
		if err := json.Unmarshal([]byte(read(path)), &in); err != nil || in.Local != want {
			t.Errorf("%s: local %v, want %v (%v)", path, in.Local, want, err)
		}
	}
}
