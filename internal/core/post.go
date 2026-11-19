package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/inbox"
	"github.com/tmstorm/prowl/internal/store"
)

// Draft is a review on its way to GitHub. A UI shows it, lets you change the
// event and body, and passes it to Submit.
type Draft struct {
	Event    ReviewEvent
	Body     string
	Comments []DraftComment // inline comments: the agents' findings, then your pending ones
	Runs     []int64        // the agent runs it was made from; empty for your own review
	Replies  []DraftReply   // drafted thread replies sent after the review, each in its thread
	Commit   string         // the commit the comments' lines are on; "" is the PR's head
	Reopened bool           // Body is the review you wrote for this event that failed to post
}

// ReviewEvent is what a Draft does: GitHub's review events, or prowl's own.
type ReviewEvent string

const (
	EventApprove        ReviewEvent = "APPROVE"
	EventRequestChanges ReviewEvent = "REQUEST_CHANGES"
	EventComment        ReviewEvent = "COMMENT"
	EventClose          ReviewEvent = "CLOSE" // a comment, then close the PR
	EventDraft          ReviewEvent = "DRAFT" // a comment, then make the PR a draft
	EventNote           ReviewEvent = "NOTE"  // a plain comment in the conversation, not a review
)

// DraftComment is an inline comment on its way to GitHub.
type DraftComment struct {
	Path      string
	Line      int
	Side      string // RIGHT (new) or LEFT (old)
	StartLine int
	StartSide string
	Body      string
	Commit    string // the commit the line is on; "" is the review's
}

// DraftReply is a drafted thread reply that goes out with an agent draft.
type DraftReply struct {
	RunID    int64
	ThreadID string
	Path     string
	Line     int
	To, Body string
}

// Pending are your inline comments on a PR that go out with your next
// review. They live until then, or until prowl quits.
func (co *Core) Pending(key string) []DraftComment {
	co.mu.Lock()
	defer co.mu.Unlock()
	return append([]DraftComment(nil), co.pending[key]...)
}

// SetPending replaces your pending inline comments on a PR. Each is on the
// commit that was the PR's head when you wrote it; editing a comment keeps
// its commit.
func (co *Core) SetPending(key string, comments []DraftComment) {
	co.mu.Lock()
	defer co.mu.Unlock()
	if co.pending == nil {
		co.pending = map[string][]DraftComment{}
	}
	head := ""
	if it, ok := co.item(key); ok {
		head = it.PR.HeadRefOid
	}
	next := make([]DraftComment, len(comments))
	for i, c := range comments {
		if c.Commit == "" {
			c.Commit = head
			for _, old := range co.pending[key] {
				if old.Path == c.Path && old.Line == c.Line && old.StartLine == c.StartLine && old.Side == c.Side {
					c.Commit = old.Commit
				}
			}
		}
		next[i] = c
	}
	co.pending[key] = next
}

// Draft is your own review of a PR: the event and your pending comments.
func (co *Core) Draft(key string, event ReviewEvent) Draft {
	d := Draft{Event: event, Comments: co.Pending(key)}
	co.reopen(key, &d)
	return d
}

// reopen puts back the text of a review that failed to post, when it was for
// the same event: a review you wrote to approve never reopens as a change
// request.
func (co *Core) reopen(key string, d *Draft) {
	co.mu.Lock()
	defer co.mu.Unlock()
	if u, ok := co.unsent[key]; ok && u.Event == d.Event {
		d.Body, d.Reopened = u.Body, true
		delete(co.unsent, key)
	}
}

// AgentDraft is one review made from every current agent review's included
// findings that aren't posted yet. The main reviewer's summary is the body,
// findings about the PR itself are listed under it, and any must-fix
// finding makes it a change request.
func (co *Core) AgentDraft(key string) (Draft, error) {
	cfg := co.Config()
	co.mu.Lock()
	var done []*Review
	for _, r := range Current(co.runs[key]) {
		if r.Finished() {
			done = append(done, r)
		}
	}
	co.mu.Unlock()
	if len(done) == 0 {
		return Draft{}, fmt.Errorf("no finished AI review to post")
	}
	d := Draft{Event: EventComment}
	summary := done[0]
	for _, r := range done {
		d.Runs = append(d.Runs, r.ID)
		if r.Agent == cfg.Agent.Main() {
			summary = r
		}
	}
	var prLevel []string
	for _, f := range Combine(done, false) {
		if f.Posted {
			continue
		}
		if f.Severity == MustFix {
			d.Event = EventRequestChanges
		}
		if f.Path == "" || f.Line == 0 {
			prLevel = append(prLevel, "- "+strings.ReplaceAll(findingBody(f), "\n\n", " "))
			continue
		}
		d.Comments = append(d.Comments, DraftComment{Path: f.Path, Line: f.Line, Side: "RIGHT", Body: findingBody(f)})
	}
	d.Body = stripVerdict(summary.Summary)
	d.Commit = summary.HeadOid
	if len(prLevel) > 0 {
		d.Body += "\n\n" + strings.Join(prLevel, "\n")
	}
	for _, r := range done {
		for _, rp := range r.Replies {
			if !rp.Posted && !rp.Excluded {
				d.Replies = append(d.Replies, DraftReply{RunID: r.ID, ThreadID: rp.ThreadID, Path: rp.Path, Line: rp.Line, To: rp.To, Body: rp.Body})
			}
		}
	}
	co.mu.Lock()
	if co.drafted == nil {
		co.drafted = map[string]store.Review{}
	}
	co.drafted[key] = reviewText(d.Body, d.Comments)
	co.mu.Unlock()
	d.Comments = append(d.Comments, co.Pending(key)...)
	co.reopen(key, &d)
	return d, nil
}

// Submit posts a review, then sends the draft's replies, each in its
// thread. A NOTE posts only its body, as a plain comment in the
// conversation, and leaves your pending comments pending. A CLOSE or DRAFT
// draft posts its text (a plain comment, or a
// comment review when it has inline comments), then closes the PR or
// converts it to a draft. A review has one commit, so inline comments written on an earlier
// commit (before the author pushed) go out first as a comment-only review on
// that commit, and each lands on the line it was written on. An approval goes
// on the head, the code you are approving, so an agent draft from before a
// push that you approve sends its inline comments first, on their commit. A comment with
// no body and no inline comments (replies only) sends no review. When
// something fails, Submitted says whether your review went out.
// Your pending comments are cleared, and an approval or change request moves
// the PR to its new section at once; RefreshPR then gets what GitHub made of
// it. For an agent draft that posted a review, the
// findings that went out are marked posted, and the draft and what you
// posted are kept so `prowl edits` and later reviews can learn from your
// changes. A review on a PR shared in Slack then shows there, as
// slack_after_review says.
func (co *Core) Submit(ctx context.Context, key string, d Draft) (Submitted, error) {
	out, err := co.submit(ctx, key, d)
	if err != nil && !out.Review {
		co.mu.Lock()
		if co.unsent == nil {
			co.unsent = map[string]Draft{}
		}
		co.unsent[key] = d
		co.mu.Unlock()
	}
	return out, err
}

func (co *Core) submit(ctx context.Context, key string, d Draft) (Submitted, error) {
	var out Submitted
	co.mu.Lock()
	it, ok := co.item(key)
	co.mu.Unlock()
	if !ok {
		return out, fmt.Errorf("submit %s: not in the inbox", key)
	}
	pr := it.PR
	if d.Event == EventNote {
		if err := co.gh.Comment(ctx, pr.ID, d.Body); err != nil {
			return out, fmt.Errorf("%s: %w", key, err)
		}
		out.Review = true
		return out, nil
	}
	event, then := d.Event, ReviewEvent("")
	if event == EventClose || event == EventDraft {
		event, then = EventComment, d.Event
	}
	var reviewed ReviewEvent
	main, earlier := byCommit(d, pr.HeadRefOid)
	if event == EventApprove && main.commit != pr.HeadRefOid {
		if len(main.comments) > 0 {
			earlier = append(earlier, main)
		}
		main = commitComments{commit: pr.HeadRefOid}
	}
	for _, g := range earlier {
		u, err := co.gh.SubmitReview(ctx, pr.Repository.NameWithOwner, pr.Number, g.commit, string(EventComment), "", ghComments(g.comments))
		if err != nil {
			return out, fmt.Errorf("comments on %s: %w", gh.Short(g.commit), err)
		}
		out.URL = u
		co.dropPending(key, g.comments)
		if len(d.Runs) > 0 {
			co.markSent(d.Runs, g.comments)
		}
	}
	switch {
	case then != "" && len(main.comments) == 0 && len(earlier) == 0:
		if strings.TrimSpace(d.Body) != "" {
			if err := co.gh.Comment(ctx, pr.ID, d.Body); err != nil {
				return out, fmt.Errorf("%s: %w", key, err)
			}
		}
	case event != EventComment || strings.TrimSpace(d.Body) != "" || len(main.comments) > 0:
		u, err := co.gh.SubmitReview(ctx, pr.Repository.NameWithOwner, pr.Number, main.commit, string(event), d.Body, ghComments(main.comments))
		if err != nil {
			return out, err
		}
		out.URL = u
		co.reviewed(key, event, main.commit)
		if then == "" {
			reviewed = event
		}
	}
	out.Review = true
	co.mu.Lock()
	delete(co.pending, key)
	drafted := co.drafted[key]
	delete(co.drafted, key)
	co.mu.Unlock()
	if len(d.Runs) > 0 && out.URL != "" {
		logErr("record the post", co.st.AddPost(key, out.URL, drafted, reviewText(d.Body, d.Comments)))
		for _, id := range d.Runs {
			if r, _ := co.st.Run(id); r != nil {
				for i := range r.Findings {
					if !r.Findings[i].Excluded {
						r.Findings[i].Posted = true
					}
				}
				logErr("save posted findings", co.st.SaveFindings(id, r.Findings))
				logErr("mark the review posted", co.st.MarkPosted(id, out.URL))
			}
		}
		co.loadRuns()
	}
	failed := make([]error, len(d.Replies))
	var sending sync.WaitGroup
	for i, r := range d.Replies {
		sending.Go(func() { failed[i] = co.sendReply(ctx, r.RunID, r.ThreadID, r.Body) })
	}
	sending.Wait()
	for i, r := range d.Replies {
		if failed[i] == nil {
			failed[i] = co.markReplied(r.RunID, r.ThreadID, r.Body)
		}
	}
	if reviewed != "" {
		note, err := co.afterReview(ctx, key, reviewed)
		out.Slack = note
		if err != nil {
			failed = append(failed, fmt.Errorf("slack: %w", err))
		}
	}
	switch then {
	case EventClose:
		if err := co.gh.ClosePR(ctx, pr.ID); err != nil {
			failed = append(failed, err)
		}
	case EventDraft:
		if err := co.gh.SetDraft(ctx, pr.ID, true); err != nil {
			failed = append(failed, err)
		}
	}
	if err := errors.Join(failed...); err != nil {
		return out, fmt.Errorf("the review went out, but: %w", err)
	}
	return out, nil
}

// reviewed puts the approval or change request you just posted on the PR as
// prowl holds it, so it changes section before RefreshPR confirms it. A
// comment review waits for RefreshPR: where it lands depends on its threads.
func (co *Core) reviewed(key string, event ReviewEvent, commit string) {
	state := map[ReviewEvent]string{EventApprove: "APPROVED", EventRequestChanges: "CHANGES_REQUESTED"}[event]
	co.mu.Lock()
	defer co.mu.Unlock()
	i := slices.IndexFunc(co.inbox.Items, func(it inbox.Item) bool { return it.PR.Key() == key })
	if state == "" || i < 0 {
		return
	}
	pr, me := co.inbox.Items[i].PR, co.inbox.Viewer
	review := gh.Review{Author: &gh.Actor{Login: me}, State: state, SubmittedAt: time.Now()}
	review.Commit = &struct {
		Oid string `json:"oid"`
	}{commit}
	pr.Reviews.Nodes = append(slices.Clone(pr.Reviews.Nodes), review)
	pr.ReviewRequests.Nodes = slices.DeleteFunc(slices.Clone(pr.ReviewRequests.Nodes), func(r gh.ReviewRequest) bool {
		return r.RequestedReviewer.Login == me
	})
	co.inbox.Items[i] = co.reclassify(pr)
}

// MarkReady marks a draft PR ready for review.
func (co *Core) MarkReady(ctx context.Context, key string) error {
	co.mu.Lock()
	it, ok := co.item(key)
	co.mu.Unlock()
	if !ok || it.PR.Local() {
		return fmt.Errorf("%s is not a pull request in the inbox", key)
	}
	if err := co.gh.SetDraft(ctx, it.PR.ID, false); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

// Submitted is what Submit sent. Review is true once your review went out,
// or when the draft had none to send (replies only), even if an error says
// a reply didn't; URL is the last review posted, if any.
type Submitted struct {
	URL    string
	Review bool
	Slack  string // what prowl did on the PR's Slack share, e.g. "reacted :white_check_mark: in #reviews"
}

// SetReplyExcluded leaves a drafted thread reply out of what gets sent, or
// puts it back.
func (co *Core) SetReplyExcluded(runID int64, threadID string, excluded bool) error {
	run, i, err := co.draftedReply(runID, threadID)
	if err != nil {
		return err
	}
	run.Replies[i].Excluded = excluded
	if err := co.st.SaveReplies(runID, run.Replies); err != nil {
		return fmt.Errorf("reply: %w", err)
	}
	co.loadRuns()
	return nil
}

// reviewText is a review's body and inline comments, as the posts table
// keeps them.
func ghComments(comments []DraftComment) []gh.DraftComment {
	var out []gh.DraftComment
	for _, c := range comments {
		out = append(out, gh.DraftComment(c))
	}
	return out
}

func reviewText(body string, comments []DraftComment) store.Review {
	r := store.Review{Body: body}
	for _, c := range comments {
		r.Comments = append(r.Comments, store.Comment{Path: c.Path, Line: c.Line, Body: c.Body})
	}
	return r
}

func findingBody(f Finding) string {
	b := "**" + string(f.Severity) + ":** " + f.Title + "\n\n" + f.Body
	if f.Rule != "" {
		b += "\n\n_Per " + f.Rule + "_"
	}
	return b
}

func stripVerdict(s string) string {
	if strings.HasPrefix(s, "**Verdict:") {
		if _, rest, ok := strings.Cut(s, "\n\n"); ok {
			return rest
		}
	}
	return s
}

// PostReply sends a reply that an agent drafted for a review thread, as
// you edited it. The draft is marked sent, and kept beside what you sent so
// `prowl edits` and later reviews learn from your edits.
func (co *Core) PostReply(ctx context.Context, runID int64, threadID, body string) error {
	if err := co.sendReply(ctx, runID, threadID, body); err != nil {
		return err
	}
	return co.markReplied(runID, threadID, body)
}

// draftedReply is the run that drafted a reply to a thread, and the reply's
// index in it.
func (co *Core) draftedReply(runID int64, threadID string) (*store.Run, int, error) {
	run, err := co.st.Run(runID)
	if err != nil {
		return nil, 0, fmt.Errorf("reply: %w", err)
	}
	if run == nil {
		return nil, 0, fmt.Errorf("reply: no review run %d", runID)
	}
	i := slices.IndexFunc(run.Replies, func(r store.ThreadReply) bool { return r.ThreadID == threadID })
	if i < 0 {
		return nil, 0, fmt.Errorf("reply: no drafted reply to thread %s", threadID)
	}
	return run, i, nil
}

func (co *Core) sendReply(ctx context.Context, runID int64, threadID, body string) error {
	if _, _, err := co.draftedReply(runID, threadID); err != nil {
		return err
	}
	return co.gh.ReplyToThread(ctx, threadID, body)
}

// markReplied records that a drafted reply went out as body. Submit sends
// replies at once and marks them one at a time: each rewrites its run's
// replies.
func (co *Core) markReplied(runID int64, threadID, body string) error {
	run, i, err := co.draftedReply(runID, threadID)
	if err != nil {
		return err
	}
	r := run.Replies[i]
	run.Replies[i].Posted = true
	if err := co.st.SaveReplies(runID, run.Replies); err != nil {
		return fmt.Errorf("reply: %w", err)
	}
	comment := func(text string) store.Review {
		return store.Review{Comments: []store.Comment{{Path: r.Path, Line: r.Line, Body: text}}}
	}
	logErr("record the reply", co.st.AddPost(run.Key, "", comment(r.Body), comment(body)))
	co.loadRuns()
	return nil
}

// commitComments are the inline comments of a draft on one commit.
type commitComments struct {
	commit   string
	comments []DraftComment
}

// markSent marks the findings that went out as inline comments posted, so a
// retry after a later call fails doesn't post them twice.
func (co *Core) markSent(runs []int64, sent []DraftComment) {
	for _, id := range runs {
		r, _ := co.st.Run(id)
		if r == nil {
			continue
		}
		for i, f := range r.Findings {
			if !f.Excluded && slices.ContainsFunc(sent, func(c DraftComment) bool { return c.Path == f.Path && c.Line == f.Line }) {
				r.Findings[i].Posted = true
			}
		}
		logErr("save sent findings", co.st.SaveFindings(id, r.Findings))
	}
	co.loadRuns()
}

// dropPending forgets pending comments that went out, so a retry after a
// later call fails doesn't post them twice.
func (co *Core) dropPending(key string, sent []DraftComment) {
	co.mu.Lock()
	defer co.mu.Unlock()
	if len(co.pending[key]) == 0 {
		return
	}
	co.pending[key] = slices.DeleteFunc(co.pending[key], func(c DraftComment) bool {
		return slices.ContainsFunc(sent, func(s DraftComment) bool {
			return s.Path == c.Path && s.Line == c.Line && s.StartLine == c.StartLine && s.Side == c.Side && s.Commit == c.Commit
		})
	})
}

// byCommit splits a draft's comments by the commit they are on. main is the
// draft's own commit (head when it has none); earlier are the others, in the
// order first seen. When no comment is on main's commit and the draft has
// none of its own, the newest group becomes main, so the review keeps its
// comments and there is one review fewer.
func byCommit(d Draft, head string) (main commitComments, earlier []commitComments) {
	main.commit = d.Commit
	if main.commit == "" {
		main.commit = head
	}
	for _, c := range d.Comments {
		at := c.Commit
		if at == "" {
			at = main.commit
		}
		if at == main.commit {
			main.comments = append(main.comments, c)
			continue
		}
		i := slices.IndexFunc(earlier, func(g commitComments) bool { return g.commit == at })
		if i < 0 {
			earlier = append(earlier, commitComments{commit: at})
			i = len(earlier) - 1
		}
		earlier[i].comments = append(earlier[i].comments, c)
	}
	if len(main.comments) == 0 && d.Commit == "" && len(earlier) > 0 {
		main, earlier = earlier[len(earlier)-1], earlier[:len(earlier)-1]
	}
	return main, earlier
}
