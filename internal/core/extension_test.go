package core_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
)

// A PR lists the on-demand extensions enabled for its repo. One already
// running on it isn't started again, and its job shows up when the detached
// process finishes it, with what it printed.
func TestOnDemandExtensions(t *testing.T) {
	co, st := newCore(t, &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 1, "fix: one", "alice"), "me")}})
	if _, err := co.UpdateConfig(func(c *config.Config) error {
		c.Agent.Extensions = []config.Extension{
			{Name: "factory", Command: "acme-factory", Repos: []string{"o/api"}, OnDemand: true, Key: "f", Confirm: true},
			{Name: "lint", Command: "acme-lint", Repos: []string{"*"}},
			{Name: "web", Command: "acme-web", Repos: []string{"o/web"}, OnDemand: true},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	refresh(t, co)
	if got := co.OnDemandExtensions("o/api#1"); len(got) != 1 || got[0] != (core.OnDemandExtension{Name: "factory", Key: "f", Confirm: true}) {
		t.Fatalf("on demand: %+v", got)
	}
	if err := co.RunExtension("o/api#1", "lint"); err == nil || !strings.Contains(err.Error(), "lint isn't an on-demand extension for o/api") {
		t.Fatalf("lint: %v", err)
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go co.Run(ctx)
	id, err := st.StartExtensionJob("o/api#1", "factory", "abc", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetExtensionJobPID(id, os.Getpid())
	if err := co.RunExtension("o/api#1", "factory"); err == nil || err.Error() != "factory is already running on o/api#1" {
		t.Fatalf("second run: %v", err)
	}
	_ = st.FinishExtensionJob(id, `{"summary":"pushed","findings":[{"severity":"nit","title":"Slow job","body":"b"}]}`, "factory: exit status 2", 0)
	var jobs []core.ExtensionJob
	for range 3 {
		waitEvent(t, co, core.RunsChanged)
		if jobs = co.ExtensionJobs("o/api#1"); len(jobs) == 1 && jobs[0].Status != core.ReviewRunning {
			break
		}
	}
	if len(jobs) != 1 || jobs[0].Status != core.ReviewFailed || jobs[0].Summary != "pushed" || len(jobs[0].Findings) != 1 || jobs[0].Error != "factory: exit status 2" {
		t.Fatalf("jobs: %+v", jobs)
	}
}
