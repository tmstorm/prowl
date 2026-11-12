package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/inbox"
	"github.com/tmstorm/prowl/internal/store"
)

// git runs a read-only git command in dir and returns its trimmed output,
// or "" when it fails.
func git(dir string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// checkout describes the git checkout dir is in: its top directory (a git
// worktree's own), branch, repo (owner/name from the origin remote) and the
// default branch it is compared with.
func checkout(dir string) (store.Local, error) {
	top := git(dir, "rev-parse", "--show-toplevel")
	if top == "" {
		return store.Local{}, fmt.Errorf("%s is not in a git checkout", dir)
	}
	branch := git(top, "rev-parse", "--abbrev-ref", "HEAD")
	if branch == "" || branch == "HEAD" {
		return store.Local{}, fmt.Errorf("%s has no branch checked out; check one out to review it", top)
	}
	repo := filepath.Base(top)
	if r := gh.RemoteRepo(git(top, "config", "--get", "remote.origin.url")); r != "" {
		repo = r
	}
	base := strings.TrimPrefix(git(top, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"), "origin/")
	if base == "" {
		base = "main"
		if git(top, "rev-parse", "--verify", "--quiet", "refs/heads/main") == "" && git(top, "rev-parse", "--verify", "--quiet", "refs/heads/master") != "" {
			base = "master"
		}
	}
	return store.Local{Key: store.LocalKey(repo, branch), Repo: repo, Branch: branch, Dir: top, Base: base, At: time.Now()}, nil
}

// ErrNothingToReview is ReviewLocal's answer for a branch with no changes,
// committed or not, since it left the default branch.
var ErrNothingToReview = errors.New("nothing to review")

// ReviewLocal reviews the branch checked out in dir, committed or not,
// before it has a pull request, with reviewer ("" is the main agent, or
// agent:profile). With wait it runs in the foreground and returns when the
// review is done; otherwise it runs in the background. It returns the
// review's key.
func (co *Core) ReviewLocal(dir, reviewer string, wait bool) (string, error) {
	l, err := checkout(dir)
	if err != nil {
		return "", err
	}
	if err := unchanged(l); err != nil {
		return "", err
	}
	if err := co.st.SaveLocal(l); err != nil {
		return "", err
	}
	co.mu.Lock()
	if co.inbox.Viewer == "" && co.gh != nil {
		co.mu.Unlock()
		viewer, _ := co.gh.Viewer()
		co.mu.Lock()
		co.inbox.Viewer = viewer
	}
	co.inbox.Items = co.withLocals(co.inbox.Items)
	co.mu.Unlock()
	job, err := co.job(l.Key, reviewer, "", false)
	if err != nil {
		return "", err
	}
	if wait {
		err = agent.Run(co.Config(), co.st, nil, job)
	} else {
		err = agent.Launch(co.st, job)
	}
	co.loadRuns()
	return l.Key, err
}

// local is the stored local review for key.
func (co *Core) local(key string) (store.Local, error) {
	locals, err := co.st.Locals()
	if err != nil {
		return store.Local{}, err
	}
	for _, l := range locals {
		if l.Key == key {
			return l, nil
		}
	}
	return store.Local{}, fmt.Errorf("%s: no such local review", key)
}

// gone reports whether a local review's checkout or branch no longer exists.
func gone(l store.Local) bool {
	if _, err := os.Stat(l.Dir); err != nil {
		return true
	}
	return git(l.Dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+l.Branch) == ""
}

// withLocals replaces the local reviews in items with one item each for the
// local reviews whose checkout and branch still exist. co.mu is held.
func (co *Core) withLocals(items []inbox.Item) []inbox.Item {
	items = slices.DeleteFunc(slices.Clone(items), func(it inbox.Item) bool { return it.PR.Local() })
	locals, _ := co.st.Locals()
	for _, l := range locals {
		if gone(l) {
			continue
		}
		pr := gh.PR{Title: git(l.Dir, "log", "-1", "--format=%s"), HeadRefName: l.Branch, BaseRefName: l.Base,
			HeadRefOid: git(l.Dir, "rev-parse", "HEAD"), CreatedAt: l.At, UpdatedAt: l.At, State: "OPEN",
			Author: &gh.Actor{Login: co.inbox.Viewer}}
		pr.Repository.NameWithOwner = l.Repo
		_, pr.Repository.Name, _ = strings.Cut(l.Repo, "/")
		if pr.Repository.Name == "" {
			pr.Repository.Name = l.Repo
		}
		items = append(items, inbox.Item{PR: pr, Bucket: inbox.Local, Since: l.At, Reason: "local review of " + l.Branch})
	}
	return items
}

// foldLocals moves each local review whose branch now has an open PR of
// yours to that PR: its runs become the PR's earlier reviews, and the next
// review of the PR starts from what changed since.
func (co *Core) foldLocals(prs []inbox.Item, viewer string) {
	locals, _ := co.st.Locals()
	moved := false
	for _, l := range locals {
		for _, it := range prs {
			pr := it.PR
			if pr.Local() || pr.State != "OPEN" || pr.AuthorLogin() != viewer ||
				!strings.EqualFold(pr.Repository.NameWithOwner, l.Repo) || pr.HeadRefName != l.Branch {
				continue
			}
			if co.st.MoveLocal(l.Key, pr.Key()) == nil {
				moved = true
			}
			break
		}
	}
	if moved {
		co.loadRuns()
	}
}

// pruneLocals forgets local reviews whose checkout or branch is gone.
func (co *Core) pruneLocals() {
	locals, _ := co.st.Locals()
	for _, l := range locals {
		if gone(l) {
			_ = co.st.DeleteLocal(l.Key)
		}
	}
}

// unchanged is ErrNothingToReview when the branch has no changes since it
// left the default branch, committed or not.
func unchanged(l store.Local) error {
	if base := mergeBase(l); base != "" && localDiff(l) == "" {
		return fmt.Errorf("%w: %s has no changes since %s, committed or not", ErrNothingToReview, l.Branch, gh.Short(base))
	}
	return nil
}

// mergeBase is where the branch left the default branch, preferring the
// remote's copy of it.
func mergeBase(l store.Local) string {
	if base := git(l.Dir, "merge-base", "origin/"+l.Base, "HEAD"); base != "" {
		return base
	}
	return git(l.Dir, "merge-base", l.Base, "HEAD")
}

// localDetail is the Overview of a local review: the branch, what it is
// compared with, its commits and its changed files.
func localDetail(l store.Local) *gh.Detail {
	base := mergeBase(l)
	var b strings.Builder
	fmt.Fprintf(&b, "Local review of `%s` in `%s`, compared with `%s` (%s). Nothing here is on GitHub.\n\n", l.Branch, l.Dir, l.Base, gh.Short(base))
	if log := git(l.Dir, "log", "--format=- `%h` %s", base+"..HEAD"); log != "" {
		b.WriteString("Commits:\n\n" + log + "\n\n")
	}
	if stat := git(l.Dir, "diff", "--stat=100", base); stat != "" {
		b.WriteString("Changed, committed or not:\n\n```\n" + stat + "\n```\n\n")
	}
	if untracked := git(l.Dir, "ls-files", "--others", "--exclude-standard"); untracked != "" {
		b.WriteString("New files, not added yet:\n\n")
		for _, f := range lines(untracked) {
			b.WriteString("- `" + f + "`\n")
		}
	}
	return &gh.Detail{Body: b.String()}
}

// localDiff is a local review's diff: everything since the branch left the
// default branch, committed or not, with untracked files as new files.
func localDiff(l store.Local) string {
	base := mergeBase(l)
	out, _ := exec.Command("git", "-C", l.Dir, "diff", base).Output()
	diff := string(out)
	for _, f := range lines(git(l.Dir, "ls-files", "--others", "--exclude-standard")) {
		out, _ := exec.Command("git", "-C", l.Dir, "diff", "--no-index", "--", "/dev/null", f).Output()
		diff += string(out)
	}
	return diff
}

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// LocalKey is the key of the local review of the branch checked out in dir.
func LocalKey(dir string) (string, error) {
	l, err := checkout(dir)
	return l.Key, err
}
