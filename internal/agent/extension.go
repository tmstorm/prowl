package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/logs"
	"github.com/tmstorm/prowl/internal/platform"
	"github.com/tmstorm/prowl/internal/store"
)

// Extension is a review step prowl runs beside the reviewer: an external
// program, or one built into prowl behind the same protocol.
type Extension interface {
	Name() string
	Run(ctx context.Context, in ExtensionInput) (ExtensionOutput, error)
}

// ExtensionInput is what an extension gets on stdin. Since, ChangedSince and
// Previous come from its own last run on the PR, and are empty on its first.
// Local says HeadDir is your own checkout, in a review before a PR exists,
// rather than a worktree of a PR.
type ExtensionInput struct {
	Repo         string             `json:"repo"`
	HeadDir      string             `json:"head_dir"`
	BaseDir      string             `json:"base_dir,omitempty"`
	Head         string             `json:"head"`
	Base         string             `json:"base"`
	ChangedFiles []string           `json:"changed_files"`
	RunImage     string             `json:"run_image,omitempty"`
	Local        bool               `json:"local,omitempty"`
	Since        string             `json:"since,omitempty"`
	ChangedSince []string           `json:"changed_since,omitempty"`
	Previous     []ExtensionFinding `json:"previous,omitempty"`
}

// ExtensionOutput is what an extension prints on stdout. Prompt becomes a
// section of the review prompt, and Summary a line of the review summary.
type ExtensionOutput struct {
	Prompt   string             `json:"prompt"`
	Findings []ExtensionFinding `json:"findings"`
	Summary  string             `json:"summary"`
}

// ExtensionFinding is a finding in the extension protocol. One with no Path
// goes to the reviewer to attach to the line that causes it.
type ExtensionFinding struct {
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Rule     string `json:"rule,omitempty"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
}

var extensionSeverities = []string{"must-fix", "should-fix", "nit"}

// ExtensionFor is the extension that e configures: one prowl ships, or the
// program e names.
func ExtensionFor(cfg config.Config, e config.Extension) Extension {
	if e.Builtin() && e.Name == "perf" {
		return perfExtension{cfg: cfg, ext: e}
	}
	return execExtension{e}
}

// execExtension runs the configured command in the PR's worktree, the input
// as JSON on stdin and the output as JSON on stdout.
type execExtension struct{ cfg config.Extension }

func (e execExtension) Name() string { return e.cfg.Name }

// Run returns what the extension printed even when it fails, so a run that
// failed partway shows how far it got.
func (e execExtension) Run(ctx context.Context, in ExtensionInput) (ExtensionOutput, error) {
	var out ExtensionOutput
	stdin, err := json.Marshal(in)
	if err != nil {
		return out, fmt.Errorf("%s: %w", e.cfg.Name, err)
	}
	ctx, cancel := context.WithTimeout(ctx, e.cfg.Limit())
	defer cancel()
	cmd := shell(ctx, e.cfg.Command)
	platform.KillGroup(cmd)
	cmd.Dir = in.HeadDir
	cmd.Env = CleanEnv()
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout bytes.Buffer
	var stderr tail
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("timed out after %s", e.cfg.Limit())
		}
		_ = json.Unmarshal(stdout.Bytes(), &out)
		return out, fmt.Errorf("%s: %w%s", e.cfg.Name, err, lastLines(stderr.String(), 20))
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return out, fmt.Errorf("%s: its output isn't the extension JSON: %w", e.cfg.Name, err)
	}
	for i, f := range out.Findings {
		if !slices.Contains(extensionSeverities, f.Severity) {
			return out, fmt.Errorf("%s: finding %d has severity %q; use %s", e.cfg.Name, i+1, f.Severity, strings.Join(extensionSeverities, ", "))
		}
	}
	return out, nil
}

// lastLines is the end of s, n lines at most, on lines of its own after the
// error it explains, or "" when s is empty.
func lastLines(s string, n int) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return "\n" + strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// extensionGrace is how long past its timeout an extension's holder may take
// to record the result before another review takes the run over.
const extensionGrace = 30 * time.Second

// reviewExtension is what one extension found on the head a review is on.
// Its findings carry their IDs (perf-1); note says how it went, for the
// reader.
type reviewExtension struct {
	name     string
	prompt   string
	findings []store.Finding
	note     string
}

// runExtensions runs the extensions enabled for the repo in parallel, before
// the review prompt is built, and notes how each went. An extension that
// another review of this head is running, or already ran, isn't run again.
func (r *jobRun) runExtensions(ctx context.Context, changed []string) {
	exts := r.cfg.Agent.ExtensionsFor(r.j.Repo, false)
	if len(exts) == 0 {
		return
	}
	names := make([]string, len(exts))
	for i, e := range exts {
		names[i] = e.Name
	}
	_ = r.st.SetRunActivity(r.id, "running extensions: "+strings.Join(names, ", "))
	in := ExtensionInput{Repo: r.j.Repo, HeadDir: r.wt, Head: r.extensionHead(), Base: MergeBase(r.wt, r.j.Base),
		ChangedFiles: changed, RunImage: r.cfg.Agent.RunImage(r.j.Repo), Local: r.j.Dir != ""}
	started := time.Now()
	if run, _ := r.st.Run(r.id); run != nil {
		started = run.StartedAt
	}
	r.exts = make([]reviewExtension, len(exts))
	var wg sync.WaitGroup
	for i, e := range exts {
		wg.Go(func() { r.exts[i] = r.runExtension(ctx, e, in, started) })
	}
	wg.Wait()
	notes := make([]string, len(r.exts))
	for i, x := range r.exts {
		notes[i] = x.name + ": " + x.note
	}
	r.note("note", "extensions: "+strings.Join(notes, " · "))
}

// runExtension claims extension e on the head and runs it, or waits for the
// review that holds it and uses its result.
func (r *jobRun) runExtension(ctx context.Context, e config.Extension, in ExtensionInput, started time.Time) reviewExtension {
	log := r.log.With("extension", e.Name)
	holder := ""
	for {
		run, claimed, err := r.st.ClaimExtension(r.key, e.Name, in.Head, r.reviewer(), os.Getpid(), e.Limit()+extensionGrace, started)
		switch {
		case err != nil:
			log.Warn("extension claim failed", "err", err)
			return reviewExtension{name: e.Name, note: "failed: " + err.Error()}
		case claimed:
			if holder != "" {
				log.Info("extension taken over", "from", holder)
			}
			return r.execute(ctx, e, run, in, log)
		case run.Status != "running":
			log.Info("extension result reused", "from", run.Holder, "status", run.Status)
			return fromExtensionRun(run)
		}
		if holder != run.Holder {
			holder = run.Holder
			log.Info("waiting for extension", "holder", holder)
		}
		_ = r.st.SetRunActivity(r.id, fmt.Sprintf("waiting for %s, running in the %s review", e.Name, run.Holder))
		select {
		case <-ctx.Done():
			return reviewExtension{name: e.Name, note: "failed: " + ctx.Err().Error()}
		case <-time.After(time.Second):
		}
	}
}

// execute runs the extension the review claimed and records its result. On
// a re-review it gets its own last run's findings and the files changed
// since: those outside them are kept as they were, and nothing changed
// means the last run stands.
func (r *jobRun) execute(ctx context.Context, e config.Extension, run *store.ExtensionRun, in ExtensionInput, log *slog.Logger) reviewExtension {
	start := time.Now()
	finish := func(out ExtensionOutput, errMsg string) reviewExtension {
		if errMsg != "" && ctx.Err() != nil {
			log.Info("extension released", "why", context.Cause(ctx))
			_ = r.st.ReleaseExtension(run)
			return reviewExtension{name: e.Name, note: "failed: " + context.Cause(ctx).Error()}
		}
		b, _ := json.Marshal(out)
		switch {
		case errMsg != "":
			b = nil
			fmt.Fprintln(os.Stderr, errMsg)
			log.Warn("extension failed", "took", since(start), "err", firstLine(errMsg))
		case strings.HasPrefix(out.Summary, "unchanged since "):
			log.Info("extension unchanged", "since", gh.Short(in.Since))
		default:
			log.Info("extension done", "took", since(start), "findings", len(out.Findings), "re_review", in.Since != "")
		}
		if ok, _ := r.st.FinishExtension(run, string(b), errMsg); !ok {
			log.Warn("extension result dropped", "why", "another review took the run over after its deadline")
		}
		run.Status, run.Output, run.Error = "done", string(b), errMsg
		if errMsg != "" {
			run.Status = "failed"
		}
		return fromExtensionRun(run)
	}
	var prev ExtensionOutput
	if last, _ := r.st.LastExtensionRun(r.key, e.Name, in.Head); last != nil && json.Unmarshal([]byte(last.Output), &prev) == nil {
		_ = git(r.wt, "fetch", "--quiet", "origin", last.HeadOid)
		if out, err := gitOutput(r.wt, "diff", "--name-only", last.HeadOid); err == nil {
			in.Since, in.ChangedSince, in.Previous = last.HeadOid, strings.Fields(string(out)), prev.Findings
			if len(in.ChangedSince) == 0 {
				prev.Summary = "unchanged since " + gh.Short(last.HeadOid)
				return finish(prev, "")
			}
		}
	}
	if e.NeedsBaseDir() {
		dir, remove, err := baseWorktree(r.wt, in.Base)
		if err != nil {
			return finish(ExtensionOutput{}, err.Error())
		}
		defer remove()
		in.BaseDir = dir
	}
	out, err := ExtensionFor(r.cfg, e).Run(ctx, in)
	if err != nil {
		return finish(ExtensionOutput{}, err.Error())
	}
	if in.Since != "" {
		out.Findings = carryForward(in.Previous, out.Findings, in.ChangedSince)
	}
	return finish(out, "")
}

// carryForward keeps the previous findings in files that didn't change
// since, unless the extension reported them again. A finding with no path is
// about the whole PR, so it stays only when reported again.
func carryForward(prev, next []ExtensionFinding, changedSince []string) []ExtensionFinding {
	out := slices.Clone(next)
	for _, p := range prev {
		if p.Path == "" || slices.Contains(changedSince, p.Path) {
			continue
		}
		if !slices.ContainsFunc(next, func(n ExtensionFinding) bool { return n.Path == p.Path && n.Title == p.Title }) {
			out = append(out, p)
		}
	}
	return out
}

// storedExtensions loads what the extensions found on the head, for a revise:
// it uses the review's results and never runs an extension.
func (r *jobRun) storedExtensions() {
	r.exts = nil
	head := r.extensionHead()
	for _, e := range r.cfg.Agent.ExtensionsFor(r.j.Repo, false) {
		if run, _ := r.st.ExtensionRun(r.key, e.Name, head); run != nil && run.Status == "done" {
			r.exts = append(r.exts, fromExtensionRun(run))
		}
	}
}

// extensionHead is the commit extension runs are kept by: the PR head, or in
// a review of your checkout with changes not yet committed, a commit of the
// working tree (git stash create), so changing it counts as a new head.
func (r *jobRun) extensionHead() string {
	if r.j.Dir != "" {
		if out, err := gitOutput(r.wt, "stash", "create"); err == nil && strings.TrimSpace(string(out)) != "" {
			return strings.TrimSpace(string(out))
		}
	}
	return r.j.Head
}

func fromExtensionRun(run *store.ExtensionRun) reviewExtension {
	x := reviewExtension{name: run.Name}
	if run.Status == "failed" {
		x.note = "failed: " + strings.TrimPrefix(firstLine(run.Error), run.Name+": ")
		return x
	}
	var out ExtensionOutput
	_ = json.Unmarshal([]byte(run.Output), &out)
	x.prompt, x.note = out.Prompt, cmp.Or(out.Summary, "done")
	for i, f := range out.Findings {
		x.findings = append(x.findings, store.Finding{Severity: f.Severity, Title: f.Title, Body: f.Body, Rule: f.Rule,
			Path: f.Path, Line: f.Line, ExtensionID: fmt.Sprintf("%s-%d", run.Name, i+1)})
	}
	return x
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// extensionSection gives the reviewer what the extensions found: each one's
// prompt section, and the findings with no path, which it must attach to
// the line that causes them. Findings with a path prowl adds itself.
func extensionSection(exts []reviewExtension) string {
	var b strings.Builder
	for _, x := range exts {
		if x.prompt == "" && len(x.findings) == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", x.name)
		if x.prompt != "" {
			b.WriteString(strings.TrimSpace(x.prompt) + "\n\n")
		}
		for _, f := range x.findings {
			where := "attach it"
			if f.Path != "" {
				where = fmt.Sprintf("already at %s:%d, prowl adds it", f.Path, f.Line)
			}
			fmt.Fprintf(&b, "- %s (%s, %s): %s. %s\n", f.ExtensionID, f.Severity, where, f.Title, oneLine(f.Body, 300))
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return "## Extensions\n\nThese ran on the PR before you, and are part of the review. For each finding to attach, add a finding on the line in the diff that causes it, with its id in `extension_id`, at its severity or higher. Don't repeat the ones prowl adds.\n\n" + b.String()
}

// withExtensions holds the review to what the extensions found: a finding
// the reviewer dropped goes back in as the extension reported it, and one
// it lowered gets its severity back.
func withExtensions(fs []store.Finding, exts []reviewExtension, log *slog.Logger) []store.Finding {
	for _, x := range exts {
		for _, ef := range x.findings {
			i := slices.IndexFunc(fs, func(f store.Finding) bool { return f.ExtensionID == ef.ExtensionID })
			if i < 0 {
				if ef.Path == "" {
					log.Info("extension finding put back", "id", ef.ExtensionID, "why", "the reviewer dropped it")
				}
				fs = append(fs, ef)
				continue
			}
			if slices.Index(extensionSeverities, ef.Severity) < slices.Index(extensionSeverities, fs[i].Severity) {
				log.Info("extension finding severity restored", "id", ef.ExtensionID, "from", fs[i].Severity, "to", ef.Severity)
				fs[i].Severity = ef.Severity
			}
		}
	}
	return fs
}

// baseWorktree checks base out next to the worktree wt, for an extension
// with needs_base, and returns how to remove it.
func baseWorktree(wt, base string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "prowl-base-")
	if err == nil {
		err = git(wt, "worktree", "add", "--quiet", "--detach", dir, base)
	}
	if err != nil {
		return "", nil, fmt.Errorf("base worktree: %w", err)
	}
	return dir, func() {
		_ = git(wt, "worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
	}, nil
}

// runExtensionJob runs an on-demand extension on the PR, in the detached
// process LaunchExtension started: in a worktree of the PR's head as a
// review would, or in your checkout for a local review. Its deadline starts
// again when the extension starts, since a clone can take a while. When it
// succeeds, findings with a path join the newest AI review from the main
// reviewer, as drafts. A notification says how it went.
func runExtensionJob(ctx context.Context, cfg config.Config, st *store.Store, j Job) error {
	_ = st.SetExtensionJobPID(j.ExtensionJob, os.Getpid())
	ctx, stop := cancellable(ctx, func() bool { return st.ExtensionJobCancelled(j.ExtensionJob) })
	defer stop()
	log := slog.With("job", j.ExtensionJob, "pr", j.Key(), "extension", j.Extension)
	log.Info("extension job started", "head", gh.Short(j.Head))
	start := time.Now()
	out, added, err := onDemand(ctx, cfg, st, j)
	if err != nil && ctx.Err() != nil {
		err = context.Cause(ctx)
	}
	b, _ := json.Marshal(out)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
		fmt.Fprintln(os.Stderr, errMsg)
		log.Error("extension job failed", "took", since(start), "err", firstLine(errMsg), "output", logs.RunPath(fmt.Sprintf("ext-%d", j.ExtensionJob)))
	} else {
		log.Info("extension job done", "took", since(start), "findings", len(out.Findings), "added", added)
	}
	if ferr := st.FinishExtensionJob(j.ExtensionJob, string(b), errMsg, added); ferr != nil && err == nil {
		err = ferr
	}
	title, body := j.Extension+" finished", j.Key()+": "+cmp.Or(out.Summary, "done")
	if err != nil {
		title, body = j.Extension+" failed", j.Key()+": "+strings.TrimPrefix(firstLine(errMsg), j.Extension+": ")
	} else if added > 0 {
		body += fmt.Sprintf(" · %d added to the AI review", added)
	}
	if !errors.Is(err, errCancelled) {
		platform.Notify(title, body, cfg.NotifyTimeout.Duration)
	}
	return err
}

func onDemand(ctx context.Context, cfg config.Config, st *store.Store, j Job) (ExtensionOutput, int, error) {
	exts := cfg.Agent.ExtensionsFor(j.Repo, true)
	i := slices.IndexFunc(exts, func(e config.Extension) bool { return e.Name == j.Extension })
	if i < 0 {
		return ExtensionOutput{}, 0, fmt.Errorf("%s: no on-demand extension by that name for %s", j.Extension, j.Repo)
	}
	e := exts[i]
	wt := j.Dir
	if wt == "" {
		j.Agent, j.Profile = "ext", e.Name
		var err error
		if wt, err = prepareWorktree(cfg, j); err != nil {
			return ExtensionOutput{}, 0, fmt.Errorf("%s: %w", e.Name, err)
		}
	}
	in := ExtensionInput{Repo: j.Repo, HeadDir: wt, Head: j.Head, Base: MergeBase(wt, j.Base), RunImage: cfg.Agent.RunImage(j.Repo), Local: j.Dir != ""}
	if j.Dir != "" {
		in.ChangedFiles = localChanges(wt, in.Base)
	} else {
		out, _ := gitOutput(wt, "diff", "--name-only", "origin/"+j.Base+"...HEAD")
		in.ChangedFiles = strings.Fields(string(out))
	}
	if e.NeedsBaseDir() {
		dir, remove, err := baseWorktree(wt, in.Base)
		if err != nil {
			return ExtensionOutput{}, 0, fmt.Errorf("%s: %w", e.Name, err)
		}
		defer remove()
		in.BaseDir = dir
	}
	_ = st.ExtendExtensionJob(j.ExtensionJob, e.Limit()+extensionGrace)
	out, err := ExtensionFor(cfg, e).Run(ctx, in)
	if err != nil {
		return out, 0, err
	}
	return out, addToReview(cfg, st, j.Key(), j.Head, e.Name, out.Findings), nil
}

// addToReview adds an on-demand extension's findings with a path to the
// main reviewer's current review, leaving out any it already has, and
// returns how many it added. The review must be finished, not yet posted,
// and of the head the extension ran on, so the lines match its diff.
// Otherwise the findings stay in the extension's result.
func addToReview(cfg config.Config, st *store.Store, key, head, name string, fs []ExtensionFinding) int {
	runs, _ := st.Runs(key)
	i := slices.IndexFunc(runs, func(r *store.Run) bool { return r.Agent == cfg.Agent.Main() && r.Profile == "" })
	if i < 0 || runs[i].Status != "done" || runs[i].HeadOid != head {
		return 0
	}
	var add []store.Finding
	for n, f := range fs {
		has := func(g store.Finding) bool { return g.Path == f.Path && g.Line == f.Line && g.Title == f.Title }
		if f.Path == "" || slices.ContainsFunc(runs[i].Findings, has) {
			continue
		}
		add = append(add, store.Finding{Severity: f.Severity, Title: f.Title, Body: f.Body, Rule: f.Rule, Path: f.Path, Line: f.Line,
			ExtensionID: fmt.Sprintf("%s-%d", name, n+1)})
	}
	if st.AddFindings(runs[i].ID, add) != nil {
		return 0
	}
	return len(add)
}
