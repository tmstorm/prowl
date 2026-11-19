package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/perf/benchfmt"
	"golang.org/x/perf/benchmath"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/store"
)

const (
	perfRuns       = 10 // per side
	perfMinPercent = 5  // a smaller change isn't reported, however significant
	goBenchCommand = "go test -run '^$' -bench . -benchmem {dirs}"
)

// perfPrompt asks the reviewer to look for performance problems in the
// change, whether or not prowl measured anything.
const perfPrompt = `Look in the changed code for work that grows faster than it needs to: a call per item in a loop, a quadratic search, allocation in a hot loop, unbounded goroutines or buffers, a missing index. For each, propose the fix as a finding. A proposal prowl didn't measure is at most should-fix, has the rule ` + "`perf: unmeasured`" + `, and includes a benchmark in the repo's own style that would show it, so the next review measures it.`

// perfExtension is the built-in perf extension: it measures the repo's
// benchmarks on the base and the head and reports regressions as findings
// for the reviewer to attach, and asks the reviewer for proposals. prowl
// decides what is a regression, not the agent.
type perfExtension struct {
	cfg config.Config
	ext config.Extension
}

func (perfExtension) Name() string { return "perf" }

func (p perfExtension) Run(ctx context.Context, in ExtensionInput) (ExtensionOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, p.ext.Limit())
	defer cancel()
	out := ExtensionOutput{Prompt: perfPrompt}
	command := p.cfg.Agent.Perf.BenchCommand(in.Repo)
	if command == "" && fileExists(filepath.Join(in.HeadDir, "go.mod")) {
		command = goBenchCommand
	}
	_, docker := exec.LookPath("docker")
	switch {
	case command == "":
		out.Summary = "proposals only: no bench command for " + in.Repo
		return out, nil
	case in.RunImage == "" || docker != nil:
		out.Summary = "proposals only: measuring needs a run image for " + in.Repo + " and docker"
		return out, nil
	}
	files := perfFiles(in)
	dirs := dirsOf(files, in.Previous)
	cmds := [2]string{benchCommand(command, in.BaseDir, dirs), benchCommand(command, in.HeadDir, dirs)}
	if cmds[0] == "" && cmds[1] == "" {
		out.Summary = "nothing to measure: no changed packages"
		return out, nil
	}
	start := time.Now()
	runs, err := measure(ctx, in, cmds, files)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("timed out after %s", p.ext.Limit())
		}
		return out, fmt.Errorf("perf: %w", err)
	}
	took := time.Since(start).Round(time.Second)
	module := goModule(in.HeadDir)
	samples := [2]map[benchKey][]float64{parseRuns(runs[0], module), parseRuns(runs[1], module)}
	if len(samples[0]) == 0 && len(samples[1]) == 0 {
		if cmds[0] != cmds[1] || command == goBenchCommand {
			out.Summary = fmt.Sprintf("no benchmarks in the changed code (%s)", took)
			return out, nil
		}
		samples = [2]map[benchKey][]float64{wallTimes(runs[0]), wallTimes(runs[1])}
	}
	cmp := compare(samples, p.cfg.Agent.Perf.MustFix)
	out.Findings = cmp.findings()
	out.Prompt = cmp.table(took) + "\n" + perfPrompt
	out.Summary = fmt.Sprintf("benchmarks: %s, %d compared, %d %s", took, len(cmp.rows), len(out.Findings), plural(len(out.Findings), "regression"))
	return out, nil
}

// capUnmeasured holds a performance proposal prowl didn't measure to
// should-fix: without numbers it can't block a merge.
func capUnmeasured(fs []store.Finding) []store.Finding {
	for i := range fs {
		if fs[i].Rule == "perf: unmeasured" && fs[i].Severity == "must-fix" {
			fs[i].Severity = "should-fix"
		}
	}
	return fs
}

func plural(n int, s string) string {
	if n == 1 {
		return s
	}
	return s + "s"
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// perfFiles are the changed files to measure: the PR's, and on a re-review
// those changed since. When a previous regression doesn't say which
// directory it is in (Rust, C++, a whole command), it's the PR's again, so
// that regression is measured again rather than dropped.
func perfFiles(in ExtensionInput) []string {
	if in.Since == "" {
		return in.ChangedFiles
	}
	for _, f := range in.Previous {
		if strings.HasPrefix(f.Rule, "perf: ") && !strings.Contains(f.Rule, " in ") {
			return in.ChangedFiles
		}
	}
	return in.ChangedSince
}

// dirsOf are the directories of files, plus the ones behind the previous
// regressions, so a commit that claims to fix one is measured again.
func dirsOf(files []string, previous []ExtensionFinding) []string {
	var dirs []string
	add := func(d string) {
		if d = "./" + strings.TrimPrefix(path.Clean(d), "./"); d == "./." {
			d = "."
		}
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	for _, f := range files {
		add(path.Dir(f))
	}
	for _, f := range previous {
		if _, dir, ok := strings.Cut(f.Rule, " in "); ok && strings.HasPrefix(f.Rule, "perf: ") {
			add(dir)
		}
	}
	return dirs
}

// benchCommand is command for one side, its {dirs} replaced by the dirs that
// exist there; for Go's own command, only those with Go files. "" when the
// command needs dirs and none exist on that side.
func benchCommand(command, root string, dirs []string) string {
	if !strings.Contains(command, "{dirs}") {
		return command
	}
	var have []string
	for _, d := range dirs {
		full := filepath.Join(root, filepath.FromSlash(d))
		if command == goBenchCommand {
			if gos, _ := filepath.Glob(filepath.Join(full, "*.go")); len(gos) == 0 {
				continue
			}
		} else if !fileExists(full) {
			continue
		}
		have = append(have, "'"+strings.ReplaceAll(d, "'", `'\''`)+"'")
	}
	if len(have) == 0 {
		return ""
	}
	return strings.ReplaceAll(command, "{dirs}", strings.Join(have, " "))
}

// benchRun is what one run of the bench command printed, and how long it
// took, for when it printed no benchmark lines.
type benchRun struct {
	out  string
	wall time.Duration
}

// measure runs cmds (base, head) perfRuns times each, interleaved, so noise
// on the machine falls on both sides alike. It uses one container for all
// of them, of the repo's run image with the run tool's restrictions, with a
// copy of the base and the head, so the repo is copied once rather than per
// run. Like the run tool, a review of your own checkout gets the local
// cache, so a PR can't plant code that runs beside your files. A side with
// no command isn't run.
func measure(ctx context.Context, in ExtensionInput, cmds [2]string, files []string) ([2][]benchRun, error) {
	var runs [2][]benchRun
	trust := "pr"
	if in.Local {
		trust = "local"
	}
	cache := filepath.Join(runCacheRoot(), filepath.FromSlash(in.Repo), trust)
	if err := markUsed(cache); err != nil {
		return runs, err
	}
	limit := time.Hour
	if d, ok := ctx.Deadline(); ok {
		limit = time.Until(d)
	}
	name := containerName()
	args := append([]string{"run", "-d", "--rm"}, containerArgs(name, limit, cache, "-v", in.BaseDir+":/base:ro", "-v", in.HeadDir+":/head:ro")...)
	args = append(args, "-e", "PROWL_CHANGED_FILES="+strings.Join(files, "\n"), in.RunImage, "sleep", strconv.Itoa(int(limit.Seconds())+60))
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return runs, fmt.Errorf("start the container: %w: %s", err, strings.TrimSpace(string(out)))
	}
	defer func() { _ = exec.Command("docker", "rm", "-f", name).Run() }()
	copyBoth := `for s in base head; do mkdir -p /tmp/work/$s && tar -C /$s --exclude=./.git -cf - . | tar -C /tmp/work/$s -xof - || exit 1; done`
	if out, err := exec.CommandContext(ctx, "docker", "exec", name, "sh", "-c", copyBoth).CombinedOutput(); err != nil {
		return runs, fmt.Errorf("copy the code into the container: %w: %s", err, strings.TrimSpace(string(out)))
	}
	sides := [2]string{"base", "head"}
	for range perfRuns {
		for s, command := range cmds {
			if command == "" {
				continue
			}
			var out bytes.Buffer
			cmd := exec.CommandContext(ctx, "docker", "exec", "-w", "/tmp/work/"+sides[s], name, "sh", "-c", command)
			cmd.Stdout, cmd.Stderr = &out, &out
			start := time.Now()
			if err := cmd.Run(); err != nil {
				if ctx.Err() != nil {
					return runs, ctx.Err()
				}
				return runs, fmt.Errorf("the bench command failed on the %s: %w%s", sides[s], err, lastLines(out.String(), 20))
			}
			runs[s] = append(runs[s], benchRun{out: out.String(), wall: time.Since(start)})
		}
	}
	return runs, nil
}

// benchKey is one measurement of one benchmark: its name, the package
// directory it is in when known, and the unit.
type benchKey struct{ name, dir, unit string }

var (
	procSuffix = regexp.MustCompile(`-\d+$`)
	// Rust's bencher format, from libtest and criterion.
	rustLine = regexp.MustCompile(`^test (\S+)\s+\.\.\. bench:\s+([\d,]+(?:\.\d+)?) ns/iter`)
	// The C++ benchmark library's console format: name, time, CPU, iterations.
	cppLine = regexp.MustCompile(`^(BM_\S+)\s+([\d.]+) (ns|us|ms|s)\s+[\d.]+ (?:ns|us|ms|s)\s+\d+`)
)

// lowerIsBetter are the units prowl compares: a rise is a regression.
var lowerIsBetter = []string{"sec/op", "B/op", "allocs/op"}

// parseRuns reads each run's output into samples: Go benchmark lines, with
// golang.org/x/perf/benchfmt, and Rust's and the C++ library's lines by
// their own names. module is the head's Go module, for telling which
// directory a Go benchmark is in.
func parseRuns(runs []benchRun, module string) map[benchKey][]float64 {
	out := map[benchKey][]float64{}
	add := func(k benchKey, v float64) { out[k] = append(out[k], v) }
	scale := map[string]float64{"ns": 1e-9, "us": 1e-6, "ms": 1e-3, "s": 1}
	for _, run := range runs {
		var goLines strings.Builder
		for line := range strings.Lines(run.out) {
			if m := rustLine.FindStringSubmatch(line); m != nil {
				v, _ := strconv.ParseFloat(strings.ReplaceAll(m[2], ",", ""), 64)
				add(benchKey{m[1], "", "sec/op"}, v*1e-9)
			} else if m := cppLine.FindStringSubmatch(line); m != nil {
				v, _ := strconv.ParseFloat(m[2], 64)
				add(benchKey{m[1], "", "sec/op"}, v*scale[m[3]])
			} else {
				goLines.WriteString(line)
			}
		}
		r := benchfmt.NewReader(strings.NewReader(goLines.String()), "bench")
		for r.Scan() {
			res, ok := r.Result().(*benchfmt.Result)
			if !ok {
				continue
			}
			dir := ""
			if pkg := res.GetConfig("pkg"); module != "" && (pkg == module || strings.HasPrefix(pkg, module+"/")) {
				if dir = "./" + strings.TrimPrefix(strings.TrimPrefix(pkg, module), "/"); dir == "./" {
					dir = "."
				}
			}
			name := "Benchmark" + procSuffix.ReplaceAllString(res.Name.String(), "")
			for _, v := range res.Values {
				if slices.Contains(lowerIsBetter, v.Unit) {
					add(benchKey{name, dir, v.Unit}, v.Value)
				}
			}
		}
	}
	return out
}

// wallTimes are each run's wall time, for a command that prints no
// benchmark lines: it is timed whole. Only fair when both sides ran the same
// command.
func wallTimes(runs []benchRun) map[benchKey][]float64 {
	out := map[benchKey][]float64{}
	k := benchKey{"the whole bench command", "", "sec/op"}
	for _, run := range runs {
		out[k] = append(out[k], run.wall.Seconds())
	}
	return out
}

// goModule is the module path in dir's go.mod, or "".
func goModule(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for l := range strings.Lines(string(b)) {
		if m, ok := strings.CutPrefix(strings.TrimSpace(l), "module "); ok {
			return strings.Trim(strings.TrimSpace(m), `"`)
		}
	}
	return ""
}

// benchRow is one benchmark and unit compared between the base and the head.
type benchRow struct {
	benchKey
	base, head  float64 // medians
	delta       float64 // percent; positive is worse
	p           float64
	n1, n2      int
	significant bool
}

type comparison struct {
	rows    []benchRow
	newOnes []benchKey // on the head only
	mustFix int
}

// compare tests each benchmark and unit on both sides with a Mann-Whitney U
// test on the medians, as benchstat does.
func compare(samples [2]map[benchKey][]float64, mustFix int) comparison {
	c := comparison{mustFix: mustFix}
	thresholds := benchmath.DefaultThresholds
	for k, head := range samples[1] {
		base, ok := samples[0][k]
		if !ok {
			c.newOnes = append(c.newOnes, k)
			continue
		}
		s1, s2 := benchmath.NewSample(base, &thresholds), benchmath.NewSample(head, &thresholds)
		cmp := benchmath.AssumeNothing.Compare(s1, s2)
		m1, m2 := benchmath.AssumeNothing.Summary(s1, 0.95).Center, benchmath.AssumeNothing.Summary(s2, 0.95).Center
		delta := 0.0
		if m1 != 0 {
			delta = (m2 - m1) / m1 * 100
		}
		c.rows = append(c.rows, benchRow{benchKey: k, base: m1, head: m2, delta: delta, p: cmp.P, n1: cmp.N1, n2: cmp.N2,
			significant: cmp.P < cmp.Alpha})
	}
	slices.SortFunc(c.rows, func(a, b benchRow) int { return strings.Compare(a.name+a.unit, b.name+b.unit) })
	slices.SortFunc(c.newOnes, func(a, b benchKey) int { return strings.Compare(a.name+a.unit, b.name+b.unit) })
	return c
}

func (r benchRow) regression() bool  { return r.significant && r.delta >= perfMinPercent }
func (r benchRow) improvement() bool { return r.significant && r.delta <= -perfMinPercent }

// findings are the regressions, with no path: the reviewer attaches each to
// the line that causes it. The rule names the benchmark and its directory,
// so a re-review measures that directory again.
func (c comparison) findings() []ExtensionFinding {
	var out []ExtensionFinding
	for _, r := range c.rows {
		if !r.regression() {
			continue
		}
		sev := "should-fix"
		if c.mustFix > 0 && r.delta >= float64(c.mustFix) {
			sev = "must-fix"
		}
		what := map[string]string{"sec/op": "slower", "B/op": "more memory per op", "allocs/op": "more allocations per op"}[r.unit]
		rule := "perf: " + r.name
		if r.dir != "" {
			rule += " in " + r.dir
		}
		out = append(out, ExtensionFinding{Severity: sev, Rule: rule,
			Title: fmt.Sprintf("%s is %.0f%% %s", r.name, r.delta, what),
			Body: fmt.Sprintf("%s %s: %s on the base, %s on the head (+%.1f%%, p=%.3f, n=%d+%d), measured by prowl.",
				r.name, r.unit, formatUnit(r.base, r.unit), formatUnit(r.head, r.unit), r.delta, r.p, r.n1, r.n2)})
	}
	return out
}

// table is the comparison as Markdown for the reviewer's prompt.
func (c comparison) table(took time.Duration) string {
	var b strings.Builder
	fmt.Fprintf(&b, "prowl ran the benchmarks %d times on the base and %d on the head, interleaved, in %s.\n\n", perfRuns, perfRuns, took)
	if len(c.rows) > 0 {
		b.WriteString("| Benchmark | Unit | Base | Head | Change | p |\n| --- | --- | --- | --- | --- | --- |\n")
		for _, r := range c.rows {
			change := "~"
			if r.significant {
				change = fmt.Sprintf("%+.1f%%", r.delta)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %.3f |\n", r.name, r.unit, formatUnit(r.base, r.unit), formatUnit(r.head, r.unit), change, r.p)
		}
		b.WriteString("\n~ means no significant change. Each regression of 5% or more is a finding below for you to attach to the line that causes it.\n")
	}
	var better []string
	for _, r := range c.rows {
		if r.improvement() {
			better = append(better, fmt.Sprintf("%s %s %.0f%%", r.name, r.unit, r.delta))
		}
	}
	if len(better) > 0 {
		b.WriteString("\nImprovements worth a word in the summary: " + strings.Join(better, ", ") + ".\n")
	}
	if len(c.newOnes) > 0 {
		var names []string
		for _, k := range c.newOnes {
			if !slices.Contains(names, k.name) {
				names = append(names, k.name)
			}
		}
		b.WriteString("\nNew on the head, with nothing to compare to: " + strings.Join(names, ", ") + ".\n")
	}
	return b.String()
}

func formatUnit(v float64, unit string) string {
	switch unit {
	case "sec/op":
		return time.Duration(math.Round(v * 1e9)).String()
	case "B/op":
		return fmt.Sprintf("%.0f B", v)
	}
	return fmt.Sprintf("%.0f", v)
}
