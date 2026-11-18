package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
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
type ExtensionInput struct {
	Repo         string             `json:"repo"`
	HeadDir      string             `json:"head_dir"`
	BaseDir      string             `json:"base_dir,omitempty"`
	Head         string             `json:"head"`
	Base         string             `json:"base"`
	ChangedFiles []string           `json:"changed_files"`
	RunImage     string             `json:"run_image,omitempty"`
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

// ExtensionFor is the extension that e configures.
func ExtensionFor(e config.Extension) Extension { return execExtension{e} }

// execExtension runs the configured command in the PR's worktree, the input
// as JSON on stdin and the output as JSON on stdout.
type execExtension struct{ cfg config.Extension }

func (e execExtension) Name() string { return e.cfg.Name }

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
		ChangedFiles: changed, RunImage: r.cfg.Agent.RunImage(r.j.Repo)}
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
	_ = r.st.AddNote(r.key, r.reviewer(), "note", "extensions: "+strings.Join(notes, " · "))
}

// runExtension claims extension e on the head and runs it, or waits for the
// review that holds it and uses its result.
func (r *jobRun) runExtension(ctx context.Context, e config.Extension, in ExtensionInput, started time.Time) reviewExtension {
	for {
		run, claimed, err := r.st.ClaimExtension(r.key, e.Name, in.Head, r.reviewer(), os.Getpid(), e.Limit()+extensionGrace, started)
		switch {
		case err != nil:
			return reviewExtension{name: e.Name, note: "failed: " + err.Error()}
		case claimed:
			return r.execute(ctx, e, run, in)
		case run.Status != "running":
			return fromExtensionRun(run)
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
func (r *jobRun) execute(ctx context.Context, e config.Extension, run *store.ExtensionRun, in ExtensionInput) reviewExtension {
	finish := func(out ExtensionOutput, errMsg string) reviewExtension {
		b, _ := json.Marshal(out)
		if errMsg != "" {
			b = nil
		}
		_, _ = r.st.FinishExtension(run, string(b), errMsg)
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
	if e.NeedsBase {
		dir, err := os.MkdirTemp("", "prowl-base-")
		if err == nil {
			err = git(r.wt, "worktree", "add", "--quiet", "--detach", dir, in.Base)
		}
		if err != nil {
			return finish(ExtensionOutput{}, "base worktree: "+err.Error())
		}
		defer func() {
			_ = git(r.wt, "worktree", "remove", "--force", dir)
			_ = os.RemoveAll(dir)
		}()
		in.BaseDir = dir
	}
	out, err := ExtensionFor(e).Run(ctx, in)
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
func withExtensions(fs []store.Finding, exts []reviewExtension) []store.Finding {
	for _, x := range exts {
		for _, ef := range x.findings {
			i := slices.IndexFunc(fs, func(f store.Finding) bool { return f.ExtensionID == ef.ExtensionID })
			if i < 0 {
				fs = append(fs, ef)
				continue
			}
			if slices.Index(extensionSeverities, ef.Severity) < slices.Index(extensionSeverities, fs[i].Severity) {
				fs[i].Severity = ef.Severity
			}
		}
	}
	return fs
}
