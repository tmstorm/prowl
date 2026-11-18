package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/store"
)

// localHistory is what a local review starts from: where the branch left
// the default branch, and the commit this reviewer last reviewed, if any.
func localHistory(dir string, j Job, runs []*store.Run, current int64) history {
	h := history{base: j.Base}
	if out, err := gitOutput(dir, "merge-base", "origin/"+j.Base, "HEAD"); err == nil {
		h.base = strings.TrimSpace(string(out))
	} else if out, err := gitOutput(dir, "merge-base", j.Base, "HEAD"); err == nil {
		h.base = strings.TrimSpace(string(out))
	}
	h.lastOid, _, _ = lastRun(runs, store.Reviewer(j.Agent, j.Profile), current)
	return h
}

// lastRun is the commit and time of reviewer's latest finished run other
// than current, and whether there is one.
func lastRun(runs []*store.Run, reviewer string, current int64) (string, time.Time, bool) {
	for _, r := range runs {
		if r.ID != current && r.Reviewer() == reviewer && r.Finished() {
			return r.HeadOid, r.FinishedAt, true
		}
	}
	return "", time.Time{}, false
}

// localChanges are the files changed since base, committed or not, and the
// untracked ones.
func localChanges(dir, base string) []string {
	out, _ := gitOutput(dir, "diff", "--name-only", base)
	untracked, _ := gitOutput(dir, "ls-files", "--others", "--exclude-standard")
	var files []string
	for _, l := range strings.Split(string(out)+string(untracked), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files
}

// localHeader opens the prompt of a local review. On a re-review, an agent
// that can run commands tests only what changed, as on a PR.
func localHeader(j Job, reader string, h history, canRun bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `You are reviewing local changes on branch %s in %s for %s, before a pull request exists. Nothing you write is posted: it goes back to %s, or to the coding agent working in this checkout.

The checkout is the current directory, and some of the work may not be committed yet. Do not change any file. See the change with:
  git diff %s          (everything since the branch left %s, committed or not)
  git status --short   (untracked files are new; read them)
  git log %s..HEAD

`, j.Branch, j.Repo, reader, reader, gh.Short(h.base), j.Base, gh.Short(h.base))
	if h.lastOid != "" {
		fmt.Fprintf(&b, "You reviewed this branch before, at commit %s. Start from what changed since then (git diff %s), and only raise problems in code you already reviewed when they are must-fix.", gh.Short(h.lastOid), gh.Short(h.lastOid))
		if canRun {
			b.WriteString(" " + testWhatChanged)
		}
		b.WriteString("\n\n")
	}
	return b.String()
}
