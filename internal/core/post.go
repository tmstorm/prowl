package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/store"
)

// Draft is a review on its way to GitHub. A UI shows it, lets you change the
// event and body, and passes it to Submit.
type Draft struct {
	Event    string // APPROVE, REQUEST_CHANGES, COMMENT; CLOSE / DRAFT: a comment, then close the PR or make it a draft; NOTE: a plain comment, not a review
	Body     string
	Comments []gh.DraftComment // inline comments: the agents' findings, then your pending ones
	Runs     []int64           // the agent runs it was made from; empty for your own review
	Replies  []DraftReply      // drafted thread replies sent after the review, each in its thread
	Commit   string            // the commit the comments' lines are on; "" is the PR's head
	Reopened bool              // Body is the review you wrote for this event that failed to post
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
func (co *Core) Pending(key string) []gh.DraftComment {
	co.mu.Lock()
	defer co.mu.Unlock()
	return append([]gh.DraftComment(nil), co.pending[key]...)
}

// SetPending replaces your pending inline comments on a PR. Each is on the
// commit that was the PR's head when you wrote it; editing a comment keeps
// its commit.
func (co *Core) SetPending(key string, comments []gh.DraftComment) {
	co.mu.Lock()
	defer co.mu.Unlock()
	if co.pending == nil {
		co.pending = map[string][]gh.DraftComment{}
	}
	head := ""
	if it, ok := co.item(key); ok {
		head = it.PR.HeadRefOid
	}
	next := make([]gh.DraftComment, len(comments))
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
func (co *Core) Draft(key, event string) Draft {
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
	var done []*store.AgentReview
	for _, r := range store.Current(co.runs[key]) {
		if r.Status == "done" || r.Status == "posted" {
			done = append(done, r)
		}
	}
	co.mu.Unlock()
	if len(done) == 0 {
		return Draft{}, fmt.Errorf("no finished AI review to post")
	}
	d := Draft{Event: "COMMENT"}
	summary := done[0]
	for _, r := range done {
		d.Runs = append(d.Runs, r.ID)
		if r.Agent == cfg.Agent.Main() {
			summary = r
		}
	}
	var prLevel []string
	for _, f := range agent.Combine(done, false) {
		if f.Posted {
			continue
		}
		if f.Severity == "must-fix" {
			d.Event = "REQUEST_CHANGES"
		}
		if f.Path == "" || f.Line == 0 {
			prLevel = append(prLevel, "- "+strings.ReplaceAll(findingBody(f), "\n\n", " "))
			continue
		}
		d.Comments = append(d.Comments, gh.DraftComment{Path: f.Path, Line: f.Line, Side: "RIGHT", Body: findingBody(f)})
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
// that commit, and each lands on the line it was written on. A comment with
// no body and no inline comments (replies only) sends no review. When
// something fails, Submitted says whether your review went out.
// Your pending comments are cleared, and the PR is fetched again so it moves
// to its new section at once. For an agent draft that posted a review, the
// findings that went out are marked posted, and the draft and what you
// posted are kept so `prowl edits` and later reviews can learn from your
// changes.
func (co *Core) Submit(key string, d Draft) (Submitted, error) {
	out, err := co.submit(key, d)
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

func (co *Core) submit(key string, d Draft) (Submitted, error) {
	var out Submitted
	co.mu.Lock()
	it, ok := co.item(key)
	co.mu.Unlock()
	if !ok {
		return out, fmt.Errorf("submit %s: not in the inbox", key)
	}
	pr := it.PR
	if d.Event == "NOTE" {
		if err := co.gh.Comment(pr.ID, d.Body); err != nil {
			return out, fmt.Errorf("comment on %s: %w", key, err)
		}
		out.Review = true
		_ = co.RefreshPR(key)
		return out, nil
	}
	event, then := d.Event, ""
	if event == "CLOSE" || event == "DRAFT" {
		event, then = "COMMENT", d.Event
	}
	main, earlier := byCommit(d, pr.HeadRefOid)
	for _, g := range earlier {
		u, err := co.gh.SubmitReview(pr.Repository.NameWithOwner, pr.Number, g.commit, "COMMENT", "", g.comments)
		if err != nil {
			return out, fmt.Errorf("submit %s: comments on %s: %w", key, gh.Short(g.commit), err)
		}
		out.URL = u
		co.dropPending(key, g.comments)
	}
	switch {
	case then != "" && len(main.comments) == 0 && len(earlier) == 0:
		if strings.TrimSpace(d.Body) != "" {
			if err := co.gh.Comment(pr.ID, d.Body); err != nil {
				return out, fmt.Errorf("comment on %s: %w", key, err)
			}
		}
	case event != "COMMENT" || strings.TrimSpace(d.Body) != "" || len(main.comments) > 0:
		u, err := co.gh.SubmitReview(pr.Repository.NameWithOwner, pr.Number, main.commit, event, d.Body, main.comments)
		if err != nil {
			return out, fmt.Errorf("submit %s: %w", key, err)
		}
		out.URL = u
	}
	out.Review = true
	co.mu.Lock()
	delete(co.pending, key)
	drafted := co.drafted[key]
	delete(co.drafted, key)
	co.mu.Unlock()
	if len(d.Runs) > 0 && out.URL != "" {
		_ = co.st.AddPost(key, out.URL, drafted, reviewText(d.Body, d.Comments))
		for _, id := range d.Runs {
			if r, _ := co.st.Run(id); r != nil {
				for i := range r.Findings {
					if !r.Findings[i].Excluded {
						r.Findings[i].Posted = true
					}
				}
				_ = co.st.SaveFindings(id, r.Findings)
				_ = co.st.MarkPosted(id, out.URL)
			}
		}
		co.loadRuns()
	}
	var failed []error
	for _, r := range d.Replies {
		if err := co.PostReply(r.RunID, r.ThreadID, r.Body); err != nil {
			failed = append(failed, err)
		}
	}
	switch then {
	case "CLOSE":
		if err := co.gh.ClosePR(pr.ID); err != nil {
			failed = append(failed, fmt.Errorf("close: %w", err))
		}
	case "DRAFT":
		if err := co.gh.SetDraft(pr.ID, true); err != nil {
			failed = append(failed, fmt.Errorf("convert to draft: %w", err))
		}
	}
	_ = co.RefreshPR(key)
	if len(failed) > 0 {
		return out, fmt.Errorf("the review went out, but: %w", errors.Join(failed...))
	}
	return out, nil
}

// SetDraft marks a PR ready for review, or converts it to a draft after
// posting comment on it, when there is one.
func (co *Core) SetDraft(key string, draft bool, comment string) error {
	co.mu.Lock()
	it, ok := co.item(key)
	co.mu.Unlock()
	if !ok || it.PR.Local() {
		return fmt.Errorf("%s is not a pull request in the inbox", key)
	}
	if comment = strings.TrimSpace(comment); comment != "" {
		if err := co.gh.Comment(it.PR.ID, comment); err != nil {
			return fmt.Errorf("comment on %s: %w", key, err)
		}
	}
	if err := co.gh.SetDraft(it.PR.ID, draft); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	_ = co.RefreshPR(key)
	return nil
}

// Submitted is what Submit sent. Review is true once your review went out,
// or when the draft had none to send (replies only), even if an error says
// a reply didn't; URL is the last review posted, if any.
type Submitted struct {
	URL    string
	Review bool
}

// SetReplyExcluded leaves a drafted thread reply out of what gets sent, or
// puts it back.
func (co *Core) SetReplyExcluded(runID int64, threadID string, excluded bool) error {
	run, err := co.st.Run(runID)
	if err != nil {
		return fmt.Errorf("reply: %w", err)
	}
	i := slices.IndexFunc(run.Replies, func(r store.ThreadReply) bool { return r.ThreadID == threadID })
	if i < 0 {
		return fmt.Errorf("reply: no drafted reply to thread %s", threadID)
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
func reviewText(body string, comments []gh.DraftComment) store.Review {
	r := store.Review{Body: body}
	for _, c := range comments {
		r.Comments = append(r.Comments, store.Comment{Path: c.Path, Line: c.Line, Body: c.Body})
	}
	return r
}

func findingBody(f store.Finding) string {
	b := "**" + f.Severity + ":** " + f.Title + "\n\n" + f.Body
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
func (co *Core) PostReply(runID int64, threadID, body string) error {
	run, err := co.st.Run(runID)
	if err != nil {
		return fmt.Errorf("reply: %w", err)
	}
	i := slices.IndexFunc(run.Replies, func(r store.ThreadReply) bool { return r.ThreadID == threadID })
	if i < 0 {
		return fmt.Errorf("reply: no drafted reply to thread %s", threadID)
	}
	if err := co.gh.ReplyToThread(threadID, body); err != nil {
		return fmt.Errorf("reply: %w", err)
	}
	r := run.Replies[i]
	run.Replies[i].Posted = true
	if err := co.st.SaveReplies(runID, run.Replies); err != nil {
		return fmt.Errorf("reply: %w", err)
	}
	comment := func(text string) store.Review {
		return store.Review{Comments: []store.Comment{{Path: r.Path, Line: r.Line, Body: text}}}
	}
	_ = co.st.AddPost(run.Key, "", comment(r.Body), comment(body))
	co.loadRuns()
	return nil
}

// commitComments are the inline comments of a draft on one commit.
type commitComments struct {
	commit   string
	comments []gh.DraftComment
}

// dropPending forgets pending comments that went out, so a retry after a
// later call fails doesn't post them twice.
func (co *Core) dropPending(key string, sent []gh.DraftComment) {
	co.mu.Lock()
	defer co.mu.Unlock()
	co.pending[key] = slices.DeleteFunc(co.pending[key], func(c gh.DraftComment) bool {
		return slices.ContainsFunc(sent, func(s gh.DraftComment) bool {
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
