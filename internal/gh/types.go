package gh

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Actor struct {
	Login    string `json:"login"`
	Typename string `json:"__typename"`
}

// IsBot is true for GitHub Apps and Actions, logins ending in [bot], and the
// accounts in bots (the bots setting).
func (a *Actor) IsBot(bots map[string]bool) bool {
	if a == nil {
		return true
	}
	return a.Typename == "Bot" || strings.HasSuffix(a.Login, "[bot]") || bots[a.Login]
}

type Review struct {
	ID     string `json:"id"`
	Author *Actor `json:"author"`
	State  string `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING
	// PreviousState is what a DISMISSED review was before it was dismissed,
	// when its dismissal was fetched.
	PreviousState string    `json:"-"`
	SubmittedAt   time.Time `json:"submittedAt"`
	Body          string    `json:"body"`
	Commit        *struct {
		Oid string `json:"oid"`
	} `json:"commit"`
}

type Comment struct {
	ID        string    `json:"id"`
	Author    *Actor    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
	Body      string    `json:"body"`
	Path      string    `json:"path"`
	Line      int       `json:"line"`
	URL       string    `json:"url"`
	DiffHunk  string    `json:"diffHunk"`
}

type Thread struct {
	ID                 string `json:"id"`
	IsResolved         bool   `json:"isResolved"`
	IsOutdated         bool   `json:"isOutdated"`
	Path               string `json:"path"`
	Line               int    `json:"line"`
	StartLine          int    `json:"startLine"`
	OriginalLine       int    `json:"originalLine"`
	DiffSide           string `json:"diffSide"`
	ViewerCanResolve   bool   `json:"viewerCanResolve"`
	ViewerCanUnresolve bool   `json:"viewerCanUnresolve"`
	Comments           struct {
		Nodes []Comment `json:"nodes"`
	} `json:"comments"`
	// Opener is the thread's first comment, in the inbox query: whose
	// thread it is.
	Opener struct {
		Nodes []Comment `json:"nodes"`
	} `json:"opener"`
}

// OpenedBy is true when login started the thread.
func (t Thread) OpenedBy(login string) bool {
	return len(t.Opener.Nodes) > 0 && t.Opener.Nodes[0].Author != nil && t.Opener.Nodes[0].Author.Login == login
}

type ReviewRequest struct {
	RequestedReviewer struct {
		Login string `json:"login"` // User
		Slug  string `json:"slug"`  // Team
		Name  string `json:"name"`
	} `json:"requestedReviewer"`
}

type TimelineItem struct {
	Typename  string    `json:"__typename"`
	CreatedAt time.Time `json:"createdAt"`
	Actor     *Actor    `json:"actor"`
	// ReviewRequestedEvent
	RequestedReviewer *struct {
		Login string `json:"login"`
		Slug  string `json:"slug"`
	} `json:"requestedReviewer"`
	// PullRequestCommit
	Commit *struct {
		Oid           string    `json:"oid"`
		CommittedDate time.Time `json:"committedDate"`
	} `json:"commit"`
	// ReviewDismissedEvent
	PreviousReviewState string `json:"previousReviewState"`
	Review              *struct {
		ID string `json:"id"`
	} `json:"review"`
}

type PR struct {
	ID               string    `json:"id"`
	Number           int       `json:"number"`
	Title            string    `json:"title"`
	URL              string    `json:"url"`
	Body             string    `json:"body"`
	IsDraft          bool      `json:"isDraft"`
	State            string    `json:"state"` // OPEN, CLOSED, MERGED
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	Author           *Actor    `json:"author"`
	HeadRefName      string    `json:"headRefName"`
	BaseRefName      string    `json:"baseRefName"`
	HeadRefOid       string    `json:"headRefOid"`
	Additions        int       `json:"additions"`
	Deletions        int       `json:"deletions"`
	ChangedFiles     int       `json:"changedFiles"`
	Mergeable        string    `json:"mergeable"`        // MERGEABLE, CONFLICTING, UNKNOWN
	MergeStateStatus string    `json:"mergeStateStatus"` // BEHIND, BLOCKED, CLEAN, DIRTY, ...
	ReviewDecision   string    `json:"reviewDecision"`   // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED
	Repository       struct {
		Name          string `json:"name"`
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				Oid               string `json:"oid"`
				StatusCheckRollup *struct {
					State string `json:"state"` // SUCCESS, FAILURE, PENDING, ERROR, EXPECTED
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	ReviewRequests struct {
		Nodes []ReviewRequest `json:"nodes"`
	} `json:"reviewRequests"`
	Reviews struct {
		Nodes []Review `json:"nodes"`
	} `json:"reviews"`
	// Mine is the viewer's own latest reviews, merged into Reviews: on a busy
	// PR they can fall outside the latest reviews by everyone.
	Mine struct {
		Nodes []Review `json:"nodes"`
	} `json:"mine"`
	Comments struct {
		Nodes []Comment `json:"nodes"`
	} `json:"comments"`
	ReviewThreads struct {
		Nodes []Thread `json:"nodes"`
	} `json:"reviewThreads"`
	TimelineItems struct {
		Nodes []TimelineItem `json:"nodes"`
	} `json:"timelineItems"`
}

func (p *PR) AuthorLogin() string {
	if p.Author == nil {
		return "ghost"
	}
	return p.Author.Login
}

func (p *PR) CIState() string {
	if len(p.Commits.Nodes) == 0 || p.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
		return ""
	}
	return p.Commits.Nodes[0].Commit.StatusCheckRollup.State
}

func (p *PR) NeedsRebase() bool {
	return p.Mergeable == "CONFLICTING" || p.MergeStateStatus == "DIRTY" || p.MergeStateStatus == "BEHIND"
}

// Key uniquely identifies a PR across repos.
// Key names the PR: owner/name#number. A PR with no number is a local
// branch reviewed before it has a pull request: local:owner/name@branch.
func (p *PR) Key() string {
	if p.Number == 0 && p.HeadRefName != "" {
		return "local:" + p.Repository.NameWithOwner + "@" + p.HeadRefName
	}
	return p.Repository.NameWithOwner + "#" + strconv.Itoa(p.Number)
}

// Local reports whether this is a local branch, not a pull request.
func (p *PR) Local() bool { return p.Number == 0 && p.HeadRefName != "" }

// Short is a commit ID cut to seven characters, as GitHub shows it.
func Short(oid string) string {
	if len(oid) > 7 {
		return oid[:7]
	}
	return oid
}

// mergeReviews adds the viewer's own reviews to Reviews, oldest first, and
// marks what each dismissed review was.
func (p *PR) mergeReviews() {
	for _, r := range p.Mine.Nodes {
		if !slices.ContainsFunc(p.Reviews.Nodes, func(x Review) bool { return x.ID == r.ID }) {
			p.Reviews.Nodes = append(p.Reviews.Nodes, r)
		}
	}
	p.Mine.Nodes = nil
	slices.SortStableFunc(p.Reviews.Nodes, func(a, b Review) int { return a.SubmittedAt.Compare(b.SubmittedAt) })
	for _, ti := range p.TimelineItems.Nodes {
		if ti.Typename == "ReviewDismissedEvent" && ti.Review != nil {
			setPreviousState(p.Reviews.Nodes, ti.Review.ID, ti.PreviousReviewState)
		}
	}
}

func setPreviousState(reviews []Review, id, state string) {
	for i := range reviews {
		if reviews[i].ID == id {
			reviews[i].PreviousState = state
		}
	}
}

// githubRemote matches owner/name in a GitHub remote URL.
var githubRemote = regexp.MustCompile(`github\.com[:/]([^/]+/[^/]+?)(\.git)?/?$`)

// RemoteRepo is the owner/name a GitHub remote URL points at, or "".
func RemoteRepo(url string) string {
	if m := githubRemote.FindStringSubmatch(strings.TrimSpace(url)); m != nil {
		return m[1]
	}
	return ""
}
