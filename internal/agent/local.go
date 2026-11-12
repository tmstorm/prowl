package agent

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/store"
)

// localHistory is what a local review starts from: where the branch left
// the default branch, and the commit this reviewer last reviewed, if any.
func localHistory(dir string, j Job, runs []*store.AgentReview, current int64) history {
	h := history{base: j.Base}
	if out, err := exec.Command("git", "-C", dir, "merge-base", "origin/"+j.Base, "HEAD").Output(); err == nil {
		h.base = strings.TrimSpace(string(out))
	} else if out, err := exec.Command("git", "-C", dir, "merge-base", j.Base, "HEAD").Output(); err == nil {
		h.base = strings.TrimSpace(string(out))
	}
	h.lastOid, _, _ = lastRun(runs, store.Reviewer(j.Agent, j.Profile), current)
	return h
}

// lastRun is the commit and time of reviewer's latest finished run other
// than current, and whether there is one.
func lastRun(runs []*store.AgentReview, reviewer string, current int64) (string, time.Time, bool) {
	for _, r := range runs {
		if r.ID != current && r.Reviewer() == reviewer && (r.Status == "done" || r.Status == "posted") {
			return r.HeadOid, r.FinishedAt, true
		}
	}
	return "", time.Time{}, false
}

// localChanges are the files changed since base, committed or not, and the
// untracked ones.
func localChanges(dir, base string) []string {
	out, _ := exec.Command("git", "-C", dir, "diff", "--name-only", base).Output()
	untracked, _ := exec.Command("git", "-C", dir, "ls-files", "--others", "--exclude-standard").Output()
	var files []string
	for _, l := range strings.Split(string(out)+string(untracked), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files
}

// localHeader opens the prompt of a local review.
func localHeader(j Job, reader string, h history, commands []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `You are reviewing local changes on branch %s in %s for %s, before a pull request exists. Nothing you write is posted: it goes back to %s, or to the coding agent working in this checkout.

The checkout is the current directory, and some of the work may not be committed yet. Do not change any file. See the change with:
  git diff %s          (everything since the branch left %s, committed or not)
  git status --short   (untracked files are new; read them)
  git log %s..HEAD

`, j.Branch, j.Repo, reader, reader, gh.Short(h.base), j.Base, gh.Short(h.base))
	if len(commands) > 0 {
		fmt.Fprintf(&b, "You may also run `%s`, with any arguments. Run them to check that the change works and that your findings hold. A failure the change causes is a finding. Say in `note` what you ran and what it showed.\n\n", strings.Join(commands, "`, `"))
	}
	if h.lastOid != "" {
		fmt.Fprintf(&b, `You reviewed this branch before, at commit %s. Start from what changed since then (git diff %s), and only raise problems in code you already reviewed when they are must-fix.

`, gh.Short(h.lastOid), gh.Short(h.lastOid))
	}
	return b.String()
}
