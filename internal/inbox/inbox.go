// Package inbox classifies PRs by what they need from the viewer.
package inbox

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tmstorm/prowl/internal/gh"
)

type Bucket int

// Values are stored, so new buckets go at the end; urgency order is rank().
const (
	NeedsReview Bucket = iota
	ReReview
	Addressed
	Activity
	TeamRequest
	Mine
	WaitingOnAuthor
	Approved
	Involved
	ReApprove // your approval was dismissed, or you were asked again after approving
	Unclaimed // a watched repo's PR nobody was asked to review
	Watching  // a watched repo's PR someone else has, or a draft
	Recheck   // pushed to since you approved, and your approval still counts
	Local     // your branch, reviewed locally before it has a pull request
)

func (b Bucket) String() string {
	return [...]string{
		"needs review", "re-review", "addressed", "new activity", "team request",
		"mine", "waiting on author", "approved", "involved", "re-approve",
		"unclaimed", "watching", "re-check", "local",
	}[b]
}

var urgency = []Bucket{NeedsReview, ReApprove, ReReview, Addressed, Activity, Recheck, TeamRequest, Mine, Local, Approved, WaitingOnAuthor, Involved, Unclaimed, Watching}

// Rank orders buckets by urgency; lower sorts first.
func (b Bucket) Rank() int {
	for i, x := range urgency {
		if x == b {
			return i
		}
	}
	return len(urgency)
}

// NeedsMe reports whether the bucket is something the viewer should act on.
func (b Bucket) NeedsMe() bool {
	return b.Rank() <= Recheck.Rank()
}

type Item struct {
	PR     gh.PR
	Bucket Bucket
	// When the ball landed in the viewer's court (or when the state began).
	Since time.Time
	// Short human explanation, e.g. "3 commits since your review".
	Reason string
	// You approved and the PR changed since, but your approval still counts.
	ChangedSinceApproval bool
	// The commit the viewer last reviewed, if any (for "since my review" diffs).
	LastReviewedOid string
	MyLastReview    *gh.Review
	UnresolvedCount int
	ThreadCount     int    // review threads, resolved or not
	ReviewStatus    string // approved | changes requested | needs review | needs rebase | draft
}

func Classify(in *gh.Inbox) []Item {
	teams := map[string]bool{}
	for _, t := range in.Teams {
		teams[t] = true
	}
	items := make([]Item, 0, len(in.PRs))
	for _, pr := range in.PRs {
		items = append(items, classify(pr, in.Viewer, teams, in.Bots, in.Watched[pr.Repository.NameWithOwner], in.RecheckAfterPush))
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Bucket != items[j].Bucket {
			return items[i].Bucket.Rank() < items[j].Bucket.Rank()
		}
		return items[i].Since.Before(items[j].Since)
	})
	return items
}

// watched classifies a PR from a watched repo that you aren't part of:
// unclaimed when nobody was asked to review it and nobody has, otherwise
// watching (someone else has it, or it's a draft).
func watched(pr gh.PR, bots map[string]bool) (Bucket, time.Time, string) {
	if pr.IsDraft {
		return Watching, pr.UpdatedAt, "draft"
	}
	var who []string
	for _, rr := range pr.ReviewRequests.Nodes {
		if n := rr.RequestedReviewer.Login; n != "" {
			who = append(who, n)
		} else if rr.RequestedReviewer.Slug != "" {
			who = append(who, "@"+rr.RequestedReviewer.Slug)
		}
	}
	for _, r := range pr.Reviews.Nodes {
		if a := login(r.Author); a != "" && a != login(pr.Author) && !r.Author.IsBot(bots) && !slices.Contains(who, a) {
			who = append(who, a)
		}
	}
	if len(who) == 0 {
		return Unclaimed, pr.CreatedAt, "nobody asked to review"
	}
	return Watching, pr.UpdatedAt, "with " + strings.Join(who, ", ")
}

// latest is the most recent thing someone did on a PR.
type latest struct {
	at        time.Time
	who, what string
}

func (l *latest) see(at time.Time, who, what string) {
	if at.After(l.at) {
		*l = latest{at, who, what}
	}
}

// String is the Activity reason, e.g. "carol commented".
func (l latest) String() string {
	if l.who == "" {
		return "new replies since you last looked"
	}
	return l.who + " " + l.what
}

func login(a *gh.Actor) string {
	if a == nil {
		return ""
	}
	return a.Login
}

// classify puts a PR in one bucket from the viewer's history with it.
// myLastDecisive is their latest approve or request-changes; myVerdict also
// counts DISMISSED, which is what those turn into when GitHub or a bot
// dismisses them; a dismissed review counts as the approval or change
// request it was. Bots' comments and reviews never count as activity, and on
// someone else's PR neither do other reviewers' reviews: only conversation
// comments and replies in your threads are for you. Your review is feedback
// (the author owes you a response) while your change request stands, or when
// you reviewed with comments (a review body or a thread you started) and
// haven't approved or requested changes. Replying in someone else's thread is
// not feedback: GitHub records it as an empty comment review.
func classify(pr gh.PR, me string, myTeams, bots map[string]bool, watchedRepo, recheck bool) Item {
	it := Item{PR: pr, Since: pr.CreatedAt}

	var myLast, myLastDecisive, myVerdict *gh.Review
	for i := range pr.Reviews.Nodes {
		r := &pr.Reviews.Nodes[i]
		if login(r.Author) != me || r.State == "PENDING" {
			continue
		}
		if myLast == nil || r.SubmittedAt.After(myLast.SubmittedAt) {
			myLast = r
		}
		if r.State == "APPROVED" || r.State == "CHANGES_REQUESTED" {
			if myLastDecisive == nil || r.SubmittedAt.After(myLastDecisive.SubmittedAt) {
				myLastDecisive = r
			}
		}
		if r.State == "APPROVED" || r.State == "CHANGES_REQUESTED" || r.State == "DISMISSED" {
			if myVerdict == nil || r.SubmittedAt.After(myVerdict.SubmittedAt) {
				myVerdict = r
			}
		}
	}
	it.MyLastReview = myLast
	if myLast != nil && myLast.Commit != nil {
		it.LastReviewedOid = myLast.Commit.Oid
	}

	myLastActivity := time.Time{}
	if myLast != nil {
		myLastActivity = myLast.SubmittedAt
	}
	var others, threads latest
	for _, c := range pr.Comments.Nodes {
		switch {
		case login(c.Author) == me:
			myLastActivity = maxT(myLastActivity, c.CreatedAt)
		case !c.Author.IsBot(bots):
			others.see(c.CreatedAt, login(c.Author), "commented")
		}
	}
	for _, t := range pr.ReviewThreads.Nodes {
		it.ThreadCount++
		if !t.IsResolved {
			it.UnresolvedCount++
		}
		mine := false
		var reply latest
		for _, c := range t.Comments.Nodes {
			switch {
			case login(c.Author) == me:
				mine = true
				myLastActivity = maxT(myLastActivity, c.CreatedAt)
			case !c.Author.IsBot(bots):
				reply.see(c.CreatedAt, login(c.Author), "replied in your thread")
			}
		}
		if mine {
			threads.see(reply.at, reply.who, reply.what)
		}
	}
	reviewers := others
	for _, r := range pr.Reviews.Nodes {
		if a := login(r.Author); a != me && a != "" && !r.Author.IsBot(bots) {
			reviewers.see(r.SubmittedAt, a, "reviewed")
		}
	}
	othersLatest, threadActivity := others.at, threads.at

	var lastPush, requestedAt time.Time
	commitsAfterReview := 0
	for _, ti := range pr.TimelineItems.Nodes {
		switch ti.Typename {
		case "PullRequestCommit":
			if ti.Commit != nil {
				lastPush = maxT(lastPush, ti.Commit.CommittedDate)
				if myLast != nil && ti.Commit.CommittedDate.After(myLast.SubmittedAt) {
					commitsAfterReview++
				}
			}
		case "HeadRefForcePushedEvent":
			lastPush = maxT(lastPush, ti.CreatedAt)
		case "ReviewRequestedEvent":
			if rr := ti.RequestedReviewer; rr != nil && (rr.Login == me || myTeams[rr.Slug]) {
				requestedAt = maxT(requestedAt, ti.CreatedAt)
			}
		}
	}

	requestedDirect, requestedTeam := false, false
	for _, rr := range pr.ReviewRequests.Nodes {
		if rr.RequestedReviewer.Login == me {
			requestedDirect = true
		}
		if myTeams[rr.RequestedReviewer.Slug] {
			requestedTeam = true
		}
	}

	asked := false
	for _, r := range pr.Reviews.Nodes {
		if login(r.Author) == me && r.State == "COMMENTED" && strings.TrimSpace(r.Body) != "" {
			asked = true
		}
	}
	for _, t := range pr.ReviewThreads.Nodes {
		if t.OpenedBy(me) {
			asked = true
		}
	}
	feedback := myLastDecisive != nil && myLastDecisive.State == "CHANGES_REQUESTED" ||
		myLast != nil && myLastDecisive == nil && asked

	it.ReviewStatus = reviewStatus(pr)
	approved := myVerdict != nil && (myVerdict.State == "APPROVED" ||
		myVerdict.State == "DISMISSED" && myVerdict.PreviousState != "CHANGES_REQUESTED")

	switch {
	case login(pr.Author) == me:
		it.Bucket = Mine
		it.Since = maxT(reviewers.at, pr.CreatedAt)
		it.Reason = mineReason(pr, reviewers.at, myLastActivity)

	case requestedDirect && myLast == nil:
		it.Bucket = NeedsReview
		it.Since = orT(requestedAt, pr.CreatedAt)
		it.Reason = "review requested"

	case requestedDirect && approved:
		it.Bucket = ReApprove
		it.Since = orT(requestedAt, lastPush)
		it.Reason = "asked again after you approved" + commitsNote(commitsAfterReview)

	case requestedDirect:
		it.Bucket = ReReview
		it.Since = orT(requestedAt, lastPush)
		it.Reason = "re-requested" + commitsNote(commitsAfterReview)

	case login(pr.Author) != me && !pr.IsDraft && myVerdict != nil && myVerdict.State == "DISMISSED":
		it.Bucket = ReApprove
		it.Since = maxT(lastPush, myVerdict.SubmittedAt)
		it.Reason = "your review was dismissed" + commitsNote(commitsAfterReview)
		if !approved {
			it.Bucket = ReReview
			it.Reason = "your change request was dismissed" + commitsNote(commitsAfterReview)
		}

	case feedback && (lastPush.After(myLastActivity) || threadActivity.After(myLastActivity)):
		it.Bucket = Addressed
		it.Since = maxT(lastPush, threadActivity)
		if lastPush.After(myLastActivity) {
			it.Reason = "author pushed" + commitsNote(commitsAfterReview)
		} else {
			it.Reason = "author replied to your threads"
		}

	case threadActivity.After(myLastActivity) || (myLast != nil && othersLatest.After(myLastActivity)):
		it.Bucket = Activity
		it.Since = maxT(threadActivity, othersLatest)
		it.Reason = others.String()
		if threadActivity.After(othersLatest) {
			it.Reason = threads.String()
		}

	case myLastDecisive != nil && myLastDecisive.State == "APPROVED" && lastPush.After(myLastDecisive.SubmittedAt):
		it.Bucket = Approved
		it.Since = myLastDecisive.SubmittedAt
		it.ChangedSinceApproval = true
		it.Reason = "you approved; changed since" + commitsNote(commitsAfterReview) + ", approval still counts"
		if recheck {
			it.Bucket, it.Since = Recheck, lastPush
			it.Reason = "pushed since you approved" + commitsNote(commitsAfterReview) + "; your approval still counts"
		}

	case requestedTeam && myLast == nil:
		it.Bucket = TeamRequest
		it.Since = orT(requestedAt, pr.CreatedAt)
		it.Reason = "team review requested"

	case feedback:
		it.Bucket = WaitingOnAuthor
		it.Since = myLast.SubmittedAt
		it.Reason = "you requested changes"
		if myLastDecisive == nil {
			it.Reason = "you reviewed with comments"
		}

	case myLastDecisive != nil && myLastDecisive.State == "APPROVED":
		it.Bucket = Approved
		it.Since = myLastDecisive.SubmittedAt
		it.Reason = "you approved"

	case watchedRepo && myLastActivity.IsZero() && !requestedDirect:
		it.Bucket, it.Since, it.Reason = watched(pr, bots)

	default:
		it.Bucket = Involved
		it.Since = pr.UpdatedAt
		it.Reason = "mentioned / commented"
	}
	if it.Since.IsZero() {
		it.Since = pr.UpdatedAt
	}
	return it
}

func reviewStatus(pr gh.PR) string {
	switch {
	case pr.IsDraft:
		return "draft"
	case pr.NeedsRebase():
		return "needs rebase"
	case pr.ReviewDecision == "APPROVED":
		return "approved"
	case pr.ReviewDecision == "CHANGES_REQUESTED":
		return "changes requested"
	default:
		return "needs review"
	}
}

func mineReason(pr gh.PR, othersLatest, myLast time.Time) string {
	switch {
	case othersLatest.After(myLast):
		return "new feedback on your PR"
	case pr.ReviewDecision == "APPROVED":
		return "approved, ready to merge"
	case pr.ReviewDecision == "CHANGES_REQUESTED":
		return "changes requested"
	default:
		return "awaiting reviewers"
	}
}

func commitsNote(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return ", 1 commit since your review"
	default:
		return ", " + strconv.Itoa(n) + " commits since your review"
	}
}

func maxT(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func orT(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	return a
}
