package agent_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/store"
)

// perfTree writes a checkout for perf: files by path, bench.sh made
// executable.
func perfTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// benchScript prints benchmark lines for run n of a side; values is shell
// that sets them from $n.
const benchScript = `c=/tmp/n-$(basename "$PWD"); n=$(cat $c 2>/dev/null || echo 0); n=$((n+1)); echo $n > $c
echo "$@" >> /cache/args
echo "$PROWL_CHANGED_FILES" > /cache/files
`

func perf(t *testing.T, cfg config.Config, in agent.ExtensionInput) agent.ExtensionOutput {
	t.Helper()
	out, err := perfRun(t, cfg, config.Extension{Name: "perf", Repos: []string{"*"}}, in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func perfRun(t *testing.T, cfg config.Config, ext config.Extension, in agent.ExtensionInput) (agent.ExtensionOutput, error) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil || runtime.GOOS == "windows" {
		t.Skip("needs docker with Linux containers")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if cfg.Agent.RunImages == nil {
		cfg.Agent.RunImages = map[string]string{"*": "busybox:1.37"}
	}
	in.Repo, in.RunImage = "acme/web", cfg.Agent.RunImage("acme/web")
	return agent.ExtensionFor(cfg, ext).Run(context.Background(), in)
}

// prowl measures the benchmarks on both sides and decides what is a
// regression: significant and at least 5% worse, must-fix past must_fix.
// A small but steady change and a big but noisy one aren't reported. It
// reads Go's, Rust's and the C++ library's lines, and a Go benchmark's
// finding names its directory.
func TestPerfFindsRegressions(t *testing.T) {
	side := func(parse, small, noisy, rust string) string {
		return benchScript + `noisy="100 900 300 700 500 200 800 400 600 1000"; set -- $noisy; shift $(( (n-1) % 10 )); nv=$1
echo "pkg: example.com/acme/internal/tui"
i=0; while [ $i -lt 1500 ]; do echo "a log line a benchmark printed, long enough to fill the output"; i=$((i+1)); done
echo "BenchmarkParse-8 1000 $((` + parse + ` + n % 3)) ns/op"
echo "BenchmarkSmall-8 1000 $((` + small + ` + n % 3)) ns/op"
echo "BenchmarkNoisy-8 1000 $((nv` + noisy + `)) ns/op"
echo "test parse::big ... bench: $((` + rust + ` + n % 3)) ns/iter (+/- 5)"
echo "BM_Sort       $((500 + n % 3)) ns        498 ns      1000"
`
	}
	base := perfTree(t, map[string]string{"bench.sh": side("100", "1000", "", "1000"), "internal/tui/a.go": "package tui\n"})
	head := perfTree(t, map[string]string{"bench.sh": side("130", "1020", " * 3 / 2", "1100") + `echo "BenchmarkNew-8 10 5 ns/op"` + "\n",
		"internal/tui/a.go": "package tui\n", "go.mod": "module example.com/acme\n"})
	cfg := config.Default()
	cfg.Agent.Perf.BenchCommands = map[string]string{"acme/web": "sh bench.sh"}
	out := perf(t, cfg, agent.ExtensionInput{BaseDir: base, HeadDir: head, ChangedFiles: []string{"internal/tui/a.go"}})

	var got []string
	for _, f := range out.Findings {
		got = append(got, f.Severity+" "+f.Rule+": "+f.Title)
	}
	want := []string{"must-fix perf: BenchmarkParse in ./internal/tui: BenchmarkParse is 30% slower", "should-fix perf: parse::big: parse::big is 10% slower"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, w := range []string{"| BenchmarkSmall | sec/op |", "| BenchmarkNoisy | sec/op |", "| BM_Sort | sec/op |", "New on the head, with nothing to compare to: BenchmarkNew.",
		"Look in the changed code for work that grows faster"} {
		if !strings.Contains(out.Prompt, w) {
			t.Errorf("the prompt is missing %q:\n%s", w, out.Prompt)
		}
	}
	if !strings.Contains(out.Summary, "5 compared, 2 regressions") {
		t.Errorf("summary: %s", out.Summary)
	}
}

// {dirs} becomes the changed files' directories that exist on each side,
// and on a re-review those changed since plus the ones behind the previous
// regressions. Scripts get the changed files. A command that prints no
// benchmark lines is timed whole only when both sides ran the same command.
// A review of your own checkout measures with its own cache.
func TestPerfDirs(t *testing.T) {
	base := perfTree(t, map[string]string{"bench.sh": benchScript, "gone/b.go": "", "internal/tui/a.go": ""})
	head := perfTree(t, map[string]string{"bench.sh": benchScript, "new/a.go": "", "internal/tui/a.go": "", "go.mod": "module example.com/acme\n"})
	cfg := config.Default()
	cfg.Agent.Perf.BenchCommands = map[string]string{"*": "sh bench.sh {dirs}"}
	cache := func(name string) []string {
		return strings.Split(strings.TrimSpace(read(filepath.Join(config.DataDir(), "cache", "acme", "web", "pr", name))), "\n")
	}
	out := perf(t, cfg, agent.ExtensionInput{BaseDir: base, HeadDir: head, ChangedFiles: []string{"new/a.go", "gone/b.go"}})
	if !strings.HasPrefix(out.Summary, "no benchmarks in the changed code") || strings.Contains(out.Prompt, "the whole bench command") {
		t.Errorf("the two sides ran different commands, yet were timed whole: %s", out.Summary)
	}
	if args := cache("args"); len(args) != 20 || !slices.Contains(args, "./gone") || !slices.Contains(args, "./new") {
		t.Fatalf("bench args: %q", args)
	}
	if files := cache("files"); strings.Join(files, ",") != "new/a.go,gone/b.go" {
		t.Fatalf("PROWL_CHANGED_FILES: %q", files)
	}

	for _, c := range []struct {
		previous string
		want     string
	}{
		{"perf: BenchmarkParse in ./internal/tui", "./new ./internal/tui"},
		{"perf: parse::big", "./new ./internal/tui"},
	} {
		_ = os.Remove(filepath.Join(config.DataDir(), "cache", "acme", "web", "pr", "args"))
		changed := []string{"new/a.go"}
		if !strings.Contains(c.previous, " in ") {
			changed = []string{"new/a.go", "internal/tui/a.go"}
		}
		perf(t, cfg, agent.ExtensionInput{BaseDir: base, HeadDir: head, ChangedFiles: changed, Since: "abc",
			ChangedSince: []string{"new/a.go"}, Previous: []agent.ExtensionFinding{{Severity: "must-fix", Title: "slower", Rule: c.previous}}})
		if args := cache("args"); !slices.Contains(args, c.want) {
			t.Errorf("re-review after %q: bench args %q, want %q", c.previous, args, c.want)
		}
	}

	cfg.Agent.Perf.BenchCommands = map[string]string{"*": "sh bench.sh"}
	if out := perf(t, cfg, agent.ExtensionInput{BaseDir: base, HeadDir: head, ChangedFiles: []string{"new/a.go"}}); !strings.Contains(out.Prompt, "| the whole bench command | sec/op |") {
		t.Errorf("the same command on both sides wasn't timed whole:\n%s", out.Prompt)
	}

	_ = perf(t, cfg, agent.ExtensionInput{BaseDir: base, HeadDir: head, ChangedFiles: []string{"new/a.go"}, Local: true})
	if _, err := os.Stat(filepath.Join(config.DataDir(), "cache", "acme", "web", "local", "args")); err != nil {
		t.Error("a review of your checkout measured with the PR cache")
	}
}

// perf stops at its timeout, like any extension.
func TestPerfTimeout(t *testing.T) {
	tree := perfTree(t, map[string]string{"bench.sh": "sleep 5", "a/a.go": ""})
	cfg := config.Default()
	cfg.Agent.Perf.BenchCommands = map[string]string{"*": "sh bench.sh"}
	start := time.Now()
	_, err := perfRun(t, cfg, config.Extension{Name: "perf", Repos: []string{"*"}, Timeout: config.Duration{Duration: 2 * time.Second}},
		agent.ExtensionInput{BaseDir: tree, HeadDir: tree, ChangedFiles: []string{"a/a.go"}})
	if err == nil || !strings.Contains(err.Error(), "perf: timed out after 2s") || time.Since(start) > 4*time.Second {
		t.Fatalf("after %s: %v", time.Since(start), err)
	}
}

// Without anything to measure with, perf still asks the reviewer for
// proposals, and says why it measured nothing.
func TestPerfProposalsOnly(t *testing.T) {
	plain := perfTree(t, map[string]string{"main.py": ""})
	goRepo := perfTree(t, map[string]string{"go.mod": "module example.com/acme\n", "README.md": ""})
	for _, c := range []struct {
		name string
		cfg  func(*config.Config)
		in   agent.ExtensionInput
		want string
	}{
		{"no bench command", nil, agent.ExtensionInput{HeadDir: plain, BaseDir: plain, ChangedFiles: []string{"main.py"}}, "proposals only: no bench command for acme/web"},
		{"no image", func(c *config.Config) { c.Agent.RunImages = map[string]string{"other/repo": "busybox:1.37"} },
			agent.ExtensionInput{HeadDir: goRepo, BaseDir: goRepo, ChangedFiles: []string{"x.go"}}, "proposals only: measuring needs a run image"},
		{"no Go package changed", nil, agent.ExtensionInput{HeadDir: goRepo, BaseDir: goRepo, ChangedFiles: []string{"README.md"}}, "nothing to measure: no changed packages"},
	} {
		cfg := config.Default()
		if c.cfg != nil {
			c.cfg(&cfg)
		}
		out := perf(t, cfg, c.in)
		if !strings.HasPrefix(out.Summary, c.want) || len(out.Findings) != 0 || !strings.Contains(out.Prompt, "perf: unmeasured") {
			t.Errorf("%s: %+v", c.name, out)
		}
	}
}

// perf runs in a review like any extension, with the base checked out
// beside the head. A proposal it didn't measure can't be must-fix.
func TestPerfInReview(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a shell script")
	}
	st, roots, bin, head := setupRun(t)
	unmeasured := `{"summary":"ok","verdict":"comment","findings":[{"severity":"must-fix","path":"a.go","line":1,"title":"Quadratic search","body":"b","rule":"perf: unmeasured","extension_id":""}],"note":"","guidelines_read":[],"skills_used":[]}`
	_ = os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat >> \"$(dirname \"$0\")/prompts\"\necho '{\"type\":\"result\",\"session_id\":\"s1\",\"structured_output\":"+unmeasured+"}'\n"), 0o755)
	cfg := config.Default()
	cfg.RepoRoots = []string{roots}
	cfg.Agent.Extensions = []config.Extension{{Name: "perf", Repos: []string{"o/api"}}}
	if err := agent.Run(t.Context(), cfg, st, nil, reviewJob(head)); err != nil {
		t.Fatal(err)
	}
	if p := read(filepath.Join(bin, "prompts")); !strings.Contains(p, "### perf") || !strings.Contains(p, "perf: unmeasured") {
		t.Errorf("the prompt has no perf section:\n%s", p)
	}
	run := latestRun(t, st, "claude")
	if len(run.Findings) != 1 || run.Findings[0].Severity != "should-fix" {
		t.Fatalf("findings: %+v", run.Findings)
	}
	notes, _ := st.Notes("o/api#7", "claude")
	if !slices.ContainsFunc(notes, func(n store.Note) bool {
		return n.Text == "extensions: perf: proposals only: no bench command for o/api"
	}) {
		t.Errorf("notes: %+v", notes)
	}
}
