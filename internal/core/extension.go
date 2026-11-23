package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/store"
)

// OnDemandExtension is an extension you run on a PR when you ask. Key is
// the letter that runs it, "" for none, and Confirm says to ask first.
type OnDemandExtension struct {
	Name, Key string
	Confirm   bool
}

// ExtensionJob is an on-demand extension's newest run on a PR: running,
// done, or failed with what it printed before it failed. Added counts its
// findings that joined the AI review; the rest are in Findings.
type ExtensionJob struct {
	ID                    int64
	Name                  string
	Status                ReviewStatus
	StartedAt, FinishedAt time.Time
	Summary, Prompt       string
	Findings              []Finding
	Error                 string
	Added                 int
}

// OnDemandExtensions are the extensions you can run on the PR key.
func (co *Core) OnDemandExtensions(key string) []OnDemandExtension {
	co.mu.Lock()
	it, ok := co.item(key)
	co.mu.Unlock()
	if !ok {
		return nil
	}
	var out []OnDemandExtension
	for _, e := range co.Config().Agent.ExtensionsFor(it.PR.Repository.NameWithOwner, true) {
		out = append(out, OnDemandExtension{Name: e.Name, Key: e.Key, Confirm: e.Confirm})
	}
	return out
}

// RunExtension starts the on-demand extension name on the PR key, in the
// background. It keeps going when this frontend quits, and a notification
// says when it's done. It refuses while that extension is still running
// on the PR.
func (co *Core) RunExtension(key, name string) error {
	job, err := co.prJob(key)
	if err != nil {
		return fmt.Errorf("%s on %s: %w", name, key, err)
	}
	exts := co.Config().Agent.ExtensionsFor(job.Repo, true)
	i := slices.IndexFunc(exts, func(e config.Extension) bool { return e.Name == name })
	if i < 0 {
		return fmt.Errorf("%s isn't an on-demand extension for %s", name, job.Repo)
	}
	job.Extension = name
	if err := agent.LaunchExtension(co.st, job, exts[i].Limit()); errors.Is(err, store.ErrExtensionRunning) {
		return fmt.Errorf("%s is already running on %s", name, key)
	} else if err != nil {
		return err
	}
	co.loadRuns()
	return nil
}

// CancelExtension stops a running on-demand extension job within a second
// or so. The job fails with "cancelled".
func (co *Core) CancelExtension(jobID int64) error {
	ok, err := co.st.CancelExtensionJob(jobID)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("that extension isn't running")
	}
	return nil
}

// ExtensionJobs are the newest job of each on-demand extension run on the
// PR key, by extension name.
func (co *Core) ExtensionJobs(key string) []ExtensionJob {
	co.mu.Lock()
	defer co.mu.Unlock()
	return slices.Clone(co.extJobs[key])
}

func extensionJobOf(j *store.ExtensionJob) ExtensionJob {
	out := ExtensionJob{ID: j.ID, Name: j.Name, Status: ReviewStatus(j.Status), StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
		Error: j.Error, Added: j.Added}
	var o agent.ExtensionOutput
	_ = json.Unmarshal([]byte(j.Output), &o)
	out.Summary, out.Prompt = o.Summary, o.Prompt
	for _, f := range o.Findings {
		out.Findings = append(out.Findings, Finding{Severity: Severity(f.Severity), Rule: f.Rule, Path: f.Path, Line: f.Line, Title: f.Title, Body: f.Body})
	}
	return out
}
