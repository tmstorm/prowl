package core

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/tmstorm/prowl/internal/gh"
)

// Detail is one PR in full, as its page shows it.
type Detail struct {
	Body     string
	Reviews  []PRReview
	Comments []Comment // the conversation
	Threads  []Thread
	Files    []File
	Events   []PREvent // pushes, force pushes, review requests
	Checks   []Check   // CI on the head commit
}

// Comment is a comment in the conversation or in a review thread.
type Comment struct {
	ID        string
	Author    Actor
	CreatedAt time.Time
	Body      string
	DiffHunk  string // in a thread: the code it is on
}

// Thread is a review thread on a line of the diff.
type Thread struct {
	ID                 string
	Resolved, Outdated bool
	Path               string
	Line               int // 0 once the line left the diff; OriginalLine is where it was
	OriginalLine       int
	DiffSide           string // RIGHT (new) or LEFT (old)
	Comments           []Comment
}

// LineShown is the thread's line, or where it was once it left the diff.
func (t Thread) LineShown() int { return cmp.Or(t.Line, t.OriginalLine) }

type File struct {
	Path                 string
	Additions, Deletions int
	Viewed               string // VIEWED, UNVIEWED, or DISMISSED (viewed, then changed)
}

// PREvent is something that happened on a PR besides reviews and comments.
type PREvent struct {
	Kind          string    // GitHub's: PullRequestCommit, HeadRefForcePushedEvent, ReviewRequestedEvent, ReadyForReviewEvent
	At            time.Time // for a commit, when it was committed
	Actor         Actor     // for a commit, its author
	Requested     string    // ReviewRequestedEvent: who was asked, a login or @team
	Oid, Headline string    // PullRequestCommit
}

// Check is one CI check on a PR's head commit: a check run (GitHub Actions
// or an app) or a commit status.
type Check struct {
	Name        string
	Workflow    string // the Actions workflow it belongs to, if any
	State       string // PASS, FAIL, PENDING or SKIPPED
	Required    bool   // branch protection requires it to pass before merging
	URL         string
	Started     time.Time
	Finished    time.Time // zero while it runs, and for commit statuses
	Title       string    // what the check said about its result
	Summary     string    // markdown
	Annotations []Annotation
	JobID       int64 // the Actions job, whose log CheckLog reads; 0 otherwise
}

// Annotation is a check's note on a line, e.g. a compile error.
type Annotation struct {
	Path    string
	Line    int
	Level   string // NOTICE, WARNING or FAILURE
	Message string
}

// Detail fetches what a PR page shows: the description, activity, review
// threads and changed files.
func (co *Core) Detail(ctx context.Context, key string) (*Detail, error) {
	if l, err := co.local(key); err == nil {
		return &Detail{Body: localBody(l)}, nil
	}
	repo, n, err := parseKey(key)
	if err != nil {
		return nil, fmt.Errorf("detail: %w", err)
	}
	det, err := co.gh.FetchDetail(ctx, repo, n)
	if err != nil {
		return nil, fmt.Errorf("detail %s: %w", key, err)
	}
	return detailOf(det, co.Config().BotSet()), nil
}

func detailOf(det *gh.Detail, bots map[string]bool) *Detail {
	out := &Detail{Body: det.Body}
	for _, r := range det.Reviews {
		out.Reviews = append(out.Reviews, prReviewOf(r, bots))
	}
	comment := func(c gh.Comment) Comment {
		return Comment{ID: c.ID, Author: actorOf(c.Author, bots), CreatedAt: c.CreatedAt, Body: c.Body, DiffHunk: c.DiffHunk}
	}
	for _, c := range det.Comments {
		out.Comments = append(out.Comments, comment(c))
	}
	for _, t := range det.Threads {
		th := Thread{ID: t.ID, Resolved: t.IsResolved, Outdated: t.IsOutdated, Path: t.Path, Line: t.Line,
			OriginalLine: t.OriginalLine, DiffSide: t.DiffSide}
		for _, c := range t.Comments.Nodes {
			th.Comments = append(th.Comments, comment(c))
		}
		out.Threads = append(out.Threads, th)
	}
	for _, f := range det.Files {
		out.Files = append(out.Files, File{Path: f.Path, Additions: f.Additions, Deletions: f.Deletions, Viewed: f.ViewerViewedState})
	}
	for _, e := range det.Events {
		ev := PREvent{Kind: e.Typename, At: e.CreatedAt, Actor: actorOf(e.Actor, bots)}
		switch {
		case e.RequestedReviewer != nil && e.RequestedReviewer.Login != "":
			ev.Requested = e.RequestedReviewer.Login
		case e.RequestedReviewer != nil:
			ev.Requested = "@" + e.RequestedReviewer.Slug
		case e.Commit != nil:
			var who *gh.Actor
			if e.Commit.Author != nil {
				who = e.Commit.Author.User
			}
			ev.At, ev.Actor, ev.Oid, ev.Headline = e.Commit.CommittedDate, actorOf(who, bots), e.Commit.Oid, e.Commit.MessageHeadline
		}
		out.Events = append(out.Events, ev)
	}
	for _, c := range det.Checks {
		ch := Check{Name: c.Name, Workflow: c.Workflow, State: c.State, Required: c.Required, URL: c.URL, Started: c.Started,
			Finished: c.Finished, Title: c.Title, Summary: c.Summary, JobID: c.JobID}
		for _, a := range c.Annotations {
			ch.Annotations = append(ch.Annotations, Annotation(a))
		}
		out.Checks = append(out.Checks, ch)
	}
	return out
}
