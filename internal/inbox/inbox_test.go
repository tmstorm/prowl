package inbox_test

import (
	"github.com/tmstorm/prowl/internal/inbox"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/gh"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func at(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }

func pr(author string) gh.PR {
	p := gh.PR{Number: 1, Author: &gh.Actor{Login: author}, CreatedAt: at(0), UpdatedAt: at(0)}
	p.Repository.NameWithOwner = "org/repo"
	return p
}

func review(who, state string, h int) gh.Review {
	return gh.Review{Author: &gh.Actor{Login: who}, State: state, SubmittedAt: at(h)}
}

func requestMe(p *gh.PR) {
	var rr gh.ReviewRequest
	rr.RequestedReviewer.Login = "me"
	p.ReviewRequests.Nodes = append(p.ReviewRequests.Nodes, rr)
}

func commit(p *gh.PR, h int) {
	ti := gh.TimelineItem{Typename: "PullRequestCommit"}
	ti.Commit = &struct {
		Oid           string    `json:"oid"`
		CommittedDate time.Time `json:"committedDate"`
	}{Oid: "abc", CommittedDate: at(h)}
	p.TimelineItems.Nodes = append(p.TimelineItems.Nodes, ti)
}

func threadReply(p *gh.PR, comments ...gh.Comment) {
	var t gh.Thread
	t.Comments.Nodes = comments
	p.ReviewThreads.Nodes = append(p.ReviewThreads.Nodes, t)
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name  string
		build func() gh.PR
		want  inbox.Bucket
	}{
		{"requested, never reviewed", func() gh.PR {
			p := pr("alice")
			requestMe(&p)
			return p
		}, inbox.NeedsReview},
		{"re-requested after my review", func() gh.PR {
			p := pr("alice")
			p.Reviews.Nodes = []gh.Review{review("me", "CHANGES_REQUESTED", 1)}
			requestMe(&p)
			return p
		}, inbox.ReReview},
		{"pushed after I requested changes", func() gh.PR {
			p := pr("alice")
			p.Reviews.Nodes = []gh.Review{review("me", "CHANGES_REQUESTED", 1)}
			commit(&p, 2)
			return p
		}, inbox.Addressed},
		{"replied in my thread after I requested changes", func() gh.PR {
			p := pr("alice")
			p.Reviews.Nodes = []gh.Review{review("me", "CHANGES_REQUESTED", 1)}
			threadReply(&p,
				gh.Comment{Author: &gh.Actor{Login: "me"}, CreatedAt: at(1)},
				gh.Comment{Author: &gh.Actor{Login: "alice"}, CreatedAt: at(3)})
			return p
		}, inbox.Addressed},
		{"nothing since I requested changes", func() gh.PR {
			p := pr("alice")
			commit(&p, 0)
			p.Reviews.Nodes = []gh.Review{review("me", "CHANGES_REQUESTED", 1)}
			return p
		}, inbox.WaitingOnAuthor},
		{"pushed after I approved, approval still counts", func() gh.PR {
			p := pr("alice")
			p.Reviews.Nodes = []gh.Review{review("me", "APPROVED", 1)}
			commit(&p, 2)
			return p
		}, inbox.Approved},
		{"my approval was dismissed by a push", func() gh.PR {
			p := pr("alice")
			p.Reviews.Nodes = []gh.Review{review("me", "DISMISSED", 1)}
			commit(&p, 2)
			return p
		}, inbox.ReApprove},
		{"dismissed review on a draft does not nag", func() gh.PR {
			p := pr("alice")
			p.IsDraft = true
			p.Reviews.Nodes = []gh.Review{review("me", "DISMISSED", 1)}
			return p
		}, inbox.Involved},
		{"re-requested after I approved", func() gh.PR {
			p := pr("alice")
			p.Reviews.Nodes = []gh.Review{review("me", "APPROVED", 1)}
			requestMe(&p)
			return p
		}, inbox.ReApprove},
		{"approved, quiet", func() gh.PR {
			p := pr("alice")
			p.Reviews.Nodes = []gh.Review{review("me", "APPROVED", 1)}
			return p
		}, inbox.Approved},
		{"my own PR", func() gh.PR { return pr("me") }, inbox.Mine},
		{"team request only", func() gh.PR {
			p := pr("alice")
			var rr gh.ReviewRequest
			rr.RequestedReviewer.Slug = "backend"
			p.ReviewRequests.Nodes = append(p.ReviewRequests.Nodes, rr)
			return p
		}, inbox.TeamRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items := inbox.Classify(&gh.Inbox{Viewer: "me", Teams: []string{"backend"}, PRs: []gh.PR{c.build()}})
			if got := items[0].Bucket; got != c.want {
				t.Fatalf("got %s (%s), want %s", got, items[0].Reason, c.want)
			}
		})
	}
}

// PRs from a watched repo that you have nothing to do with: unclaimed when
// nobody was asked, watching when someone was (or it's a draft). Outside a
// watched repo they stay involved. None of them need you.
func TestWatchedRepoBuckets(t *testing.T) {
	mk := func(draft bool, requested string) gh.PR {
		pr := gh.PR{ID: "1", Number: 1, Author: &gh.Actor{Login: "alice"}, IsDraft: draft, CreatedAt: time.Now()}
		pr.Repository.NameWithOwner = "o/api"
		if requested != "" {
			var rr gh.ReviewRequest
			rr.RequestedReviewer.Login = requested
			pr.ReviewRequests.Nodes = []gh.ReviewRequest{rr}
		}
		return pr
	}
	watch := map[string]bool{"o/api": true}
	for _, c := range []struct {
		pr      gh.PR
		watched bool
		want    inbox.Bucket
	}{
		{mk(false, ""), true, inbox.Unclaimed},
		{mk(false, "bob"), true, inbox.Watching},
		{mk(true, ""), true, inbox.Watching},
		{mk(false, ""), false, inbox.Involved},
	} {
		w := map[string]bool{}
		if c.watched {
			w = watch
		}
		got := inbox.Classify(&gh.Inbox{Viewer: "me", PRs: []gh.PR{c.pr}, Watched: w})[0]
		if got.Bucket != c.want || got.Bucket.NeedsMe() {
			t.Errorf("draft=%v requested=%v watched=%v: got %v (%s), want %v", c.pr.IsDraft, len(c.pr.ReviewRequests.Nodes), c.watched, got.Bucket, got.Reason, c.want)
		}
	}
}

// You approve, a bot comments seconds later. That is not
// activity for you; a person commenting is, and the reason names them.
func TestBotCommentAfterApproval(t *testing.T) {
	approved := time.Now().Add(-time.Hour)
	pr := gh.PR{ID: "1", Number: 27, Author: &gh.Actor{Login: "carol"}, CreatedAt: approved.Add(-48 * time.Hour)}
	pr.Repository.NameWithOwner = "o/tools"
	pr.Reviews.Nodes = []gh.Review{{Author: &gh.Actor{Login: "me"}, State: "APPROVED", SubmittedAt: approved}}
	pr.Comments.Nodes = []gh.Comment{{Author: &gh.Actor{Login: "github-actions", Typename: "Bot"}, CreatedAt: approved.Add(12 * time.Second)}}
	in := &gh.Inbox{Viewer: "me", PRs: []gh.PR{pr}}
	if it := inbox.Classify(in)[0]; it.Bucket != inbox.Approved {
		t.Fatalf("a bot comment after approving moved it to %v (%s)", it.Bucket, it.Reason)
	}
	pr.Comments.Nodes = append(pr.Comments.Nodes, gh.Comment{Author: &gh.Actor{Login: "carol"}, CreatedAt: approved.Add(time.Minute)})
	in.PRs = []gh.PR{pr}
	if it := inbox.Classify(in)[0]; it.Bucket != inbox.Activity || it.Reason != "carol commented" {
		t.Fatalf("a person commenting is activity: %v (%s)", it.Bucket, it.Reason)
	}
}

// You request changes, the author pushes, you look again
// and leave a comment review on the new head. It's back with the author.
func TestCommentAfterPushIsWaiting(t *testing.T) {
	asked := time.Now().Add(-48 * time.Hour)
	pushed := asked.Add(22 * time.Hour)
	looked := pushed.Add(2 * time.Hour)
	pr := gh.PR{ID: "1", Number: 307, Author: &gh.Actor{Login: "carol"}, CreatedAt: asked.Add(-time.Hour)}
	pr.Repository.NameWithOwner = "o/infra"
	pr.Reviews.Nodes = []gh.Review{{Author: &gh.Actor{Login: "me"}, State: "CHANGES_REQUESTED", SubmittedAt: asked}}
	pr.TimelineItems.Nodes = []gh.TimelineItem{{Typename: "HeadRefForcePushedEvent", CreatedAt: pushed}}
	in := &gh.Inbox{Viewer: "me", PRs: []gh.PR{pr}}
	if it := inbox.Classify(in)[0]; it.Bucket != inbox.Addressed {
		t.Fatalf("after the push and before you look: %v (%s)", it.Bucket, it.Reason)
	}
	pr.Reviews.Nodes = append(pr.Reviews.Nodes, gh.Review{Author: &gh.Actor{Login: "me"}, State: "COMMENTED", SubmittedAt: looked})
	in.PRs = []gh.PR{pr}
	if it := inbox.Classify(in)[0]; it.Bucket != inbox.WaitingOnAuthor {
		t.Fatalf("after you commented on the push: %v (%s)", it.Bucket, it.Reason)
	}
}

// You approved, then another reviewer approved with
// inline comments of their own. Nothing there is for you.
func TestOtherReviewerAfterYouApproved(t *testing.T) {
	approved := time.Now().Add(-21 * 24 * time.Hour)
	pr := gh.PR{ID: "1", Number: 200, Author: &gh.Actor{Login: "carol"}, CreatedAt: approved.Add(-5 * 24 * time.Hour)}
	pr.Repository.NameWithOwner = "o/fpe"
	pr.Reviews.Nodes = []gh.Review{
		{Author: &gh.Actor{Login: "me"}, State: "APPROVED", SubmittedAt: approved},
		{Author: &gh.Actor{Login: "pickuperast"}, State: "APPROVED", SubmittedAt: approved.Add(4 * 24 * time.Hour)},
	}
	var th gh.Thread
	th.Comments.Nodes = []gh.Comment{{Author: &gh.Actor{Login: "pickuperast"}, CreatedAt: approved.Add(4 * 24 * time.Hour)}}
	pr.ReviewThreads.Nodes = []gh.Thread{th}
	if it := inbox.Classify(&gh.Inbox{Viewer: "me", PRs: []gh.PR{pr}})[0]; it.Bucket != inbox.Approved {
		t.Fatalf("another reviewer's review pulled it back: %v (%s)", it.Bucket, it.Reason)
	}
}

// On a repo that keeps approvals through pushes, a push after you approved
// needs you again as re-check, unless recheck_after_push is off.
func TestRecheckAfterPush(t *testing.T) {
	approved := time.Now().Add(-48 * time.Hour)
	pr := gh.PR{ID: "1", Number: 9, Author: &gh.Actor{Login: "alice"}, CreatedAt: approved.Add(-time.Hour)}
	pr.Repository.NameWithOwner = "o/r"
	pr.Reviews.Nodes = []gh.Review{{Author: &gh.Actor{Login: "me"}, State: "APPROVED", SubmittedAt: approved}}
	pr.TimelineItems.Nodes = []gh.TimelineItem{{Typename: "HeadRefForcePushedEvent", CreatedAt: approved.Add(time.Hour)}}
	in := &gh.Inbox{Viewer: "me", PRs: []gh.PR{pr}, RecheckAfterPush: true}
	if it := inbox.Classify(in)[0]; it.Bucket != inbox.Recheck || !it.Bucket.NeedsMe() {
		t.Fatalf("with recheck on: %v (%s)", it.Bucket, it.Reason)
	}
	in.RecheckAfterPush = false
	if it := inbox.Classify(in)[0]; it.Bucket != inbox.Approved || !it.ChangedSinceApproval {
		t.Fatalf("with recheck off: %v (%s)", it.Bucket, it.Reason)
	}
}

// A comment review is feedback too. The author pushing
// after it brings the PR back to you; until then it waits on them. On top of
// an approval, a comment doesn't take the approval back.
func TestCommentReviewIsFeedback(t *testing.T) {
	reviewed := time.Now().Add(-6 * time.Hour)
	mk := func(reviews ...gh.Review) gh.PR {
		pr := gh.PR{ID: "1", Number: 11, Author: &gh.Actor{Login: "carol"}, CreatedAt: reviewed.Add(-24 * time.Hour)}
		pr.Repository.NameWithOwner = "o/sdk-unreal"
		pr.Reviews.Nodes = reviews
		return pr
	}
	commented := gh.Review{Author: &gh.Actor{Login: "me"}, State: "COMMENTED", SubmittedAt: reviewed, Body: "Two things to fix."}
	classify := func(pr gh.PR) inbox.Item { return inbox.Classify(&gh.Inbox{Viewer: "me", PRs: []gh.PR{pr}})[0] }

	pr := mk(commented)
	if it := classify(pr); it.Bucket != inbox.WaitingOnAuthor {
		t.Fatalf("after your comment review: %v (%s)", it.Bucket, it.Reason)
	}
	pr.TimelineItems.Nodes = []gh.TimelineItem{{Typename: "HeadRefForcePushedEvent", CreatedAt: reviewed.Add(16 * time.Minute)}}
	if it := classify(pr); it.Bucket != inbox.Addressed {
		t.Fatalf("after the author pushed: %v (%s)", it.Bucket, it.Reason)
	}
	reply := commented
	reply.Body = ""
	pr = mk(reply)
	var th gh.Thread
	th.Opener.Nodes = []gh.Comment{{Author: &gh.Actor{Login: "bob"}}}
	pr.ReviewThreads.Nodes = []gh.Thread{th}
	pr.TimelineItems.Nodes = []gh.TimelineItem{{Typename: "HeadRefForcePushedEvent", CreatedAt: reviewed.Add(time.Hour)}}
	if it := classify(pr); it.Bucket == inbox.Addressed || it.Bucket == inbox.WaitingOnAuthor {
		t.Fatalf("a reply in someone else's thread isn't feedback: %v (%s)", it.Bucket, it.Reason)
	}
	th.Opener.Nodes = []gh.Comment{{Author: &gh.Actor{Login: "me"}}}
	pr.ReviewThreads.Nodes = []gh.Thread{th}
	if it := classify(pr); it.Bucket != inbox.Addressed {
		t.Fatalf("a thread you started is feedback: %v (%s)", it.Bucket, it.Reason)
	}
	approved := gh.Review{Author: &gh.Actor{Login: "me"}, State: "APPROVED", SubmittedAt: reviewed.Add(-time.Hour)}
	pr = mk(approved, commented)
	if it := classify(pr); it.Bucket != inbox.Approved {
		t.Fatalf("a comment on top of your approval: %v (%s)", it.Bucket, it.Reason)
	}
}
