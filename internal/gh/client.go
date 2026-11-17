package gh

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

type Client struct {
	gql   *api.GraphQLClient
	rest  *api.RESTClient
	diff  *api.RESTClient
	asset *api.RESTClient

	viewerMu sync.Mutex
	viewer   string // your login, once a lookup succeeded
}

func New() (*Client, error) {
	c, err := NewWith(api.ClientOptions{})
	if err != nil {
		return nil, fmt.Errorf("github auth (run `gh auth login`): %w", err)
	}
	return c, nil
}

// NewWith builds a client from explicit options (host, token, transport).
// Answers pass through cleanTransport, a request that takes longer than a
// minute fails rather than stalling a sync, and without a fixed token a new
// login takes effect without a restart (tokenTransport).
func NewWith(opts api.ClientOptions) (*Client, error) {
	next := opts.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	opts.Transport = cleanTransport{next}
	if opts.AuthToken == "" {
		host := opts.Host
		if host == "" {
			host = "github.com"
		}
		opts.Transport = &tokenTransport{next: opts.Transport, host: host}
	}
	if opts.Timeout == 0 {
		opts.Timeout = time.Minute
	}
	gql, err := api.NewGraphQLClient(opts)
	if err != nil {
		return nil, fmt.Errorf("graphql client: %w", err)
	}
	rest, err := api.NewRESTClient(opts)
	if err != nil {
		return nil, fmt.Errorf("rest client: %w", err)
	}
	opts.Headers = map[string]string{"Accept": "application/vnd.github.diff"}
	diff, err := api.NewRESTClient(opts)
	if err != nil {
		return nil, fmt.Errorf("diff client: %w", err)
	}
	opts.Headers = map[string]string{"Accept": "application/octet-stream"}
	opts.Timeout = 10 * time.Minute
	asset, err := api.NewRESTClient(opts)
	if err != nil {
		return nil, fmt.Errorf("asset client: %w", err)
	}
	return &Client{gql: gql, rest: rest, diff: diff, asset: asset}, nil
}

const prFields = `
fragment pr on PullRequest {
  id number title url body isDraft state createdAt updatedAt
  author { login __typename }
  headRefName baseRefName headRefOid
  additions deletions changedFiles
  mergeable mergeStateStatus reviewDecision
  repository { name nameWithOwner }
  commits(last: 1) { nodes { commit { oid statusCheckRollup { state } } } }
  reviewRequests(first: 20) { nodes { requestedReviewer {
    ... on User { login } ... on Team { slug name } } } }
  reviews(last: 40) { nodes { id author { login __typename } state submittedAt body commit { oid } } }
  mine: reviews(last: 20, author: $me) { nodes { id author { login __typename } state submittedAt body commit { oid } } }
  comments(last: 30) { nodes { author { login __typename } createdAt } }
  reviewThreads(last: 60) { nodes { id isResolved opener: comments(first: 1) { nodes { author { login } } }
    comments(last: 10) { nodes { author { login __typename } createdAt } } } }
  timelineItems(last: 40, itemTypes: [REVIEW_REQUESTED_EVENT, PULL_REQUEST_COMMIT, HEAD_REF_FORCE_PUSHED_EVENT, REVIEW_DISMISSED_EVENT]) {
    nodes {
      __typename
      ... on ReviewRequestedEvent { createdAt actor { login } requestedReviewer { ... on User { login } ... on Team { slug } } }
      ... on PullRequestCommit { commit { oid committedDate } }
      ... on HeadRefForcePushedEvent { createdAt actor { login } }
      ... on ReviewDismissedEvent { createdAt previousReviewState review { id } }
    }
  }
}`

// search runs a PR search, following pages up to limit results, and returns
// the PRs' node IDs.
func (c *Client) search(q string, limit int) ([]string, error) {
	var out []string
	var after *string
	for len(out) < limit {
		var resp struct {
			Search page[struct {
				ID string `json:"id"`
			}] `json:"search"`
		}
		err := c.gql.Do(`query($q: String!, $after: String) { search(query: $q, type: ISSUE, first: 100, after: $after) {
  nodes { ... on PullRequest { id } } pageInfo { hasNextPage endCursor } } }`, map[string]any{"q": q, "after": after}, &resp)
		if err != nil {
			return nil, fmt.Errorf("search %q: %w", q, err)
		}
		for _, n := range resp.Search.Nodes {
			if n.ID != "" {
				out = append(out, n.ID)
			}
		}
		if !resp.Search.PageInfo.HasNextPage {
			break
		}
		cursor := resp.Search.PageInfo.EndCursor
		after = &cursor
	}
	return out, nil
}

type Inbox struct {
	Watched map[string]bool // repos (owner/name) whose every open PR is fetched
	Bots    map[string]bool // logins treated as bots: their activity doesn't count
	// RecheckAfterPush puts a PR pushed to after you approved back in front
	// of you, for repos that don't dismiss stale approvals.
	RecheckAfterPush bool
	Viewer           string
	Teams            []string // team slugs the viewer belongs to
	PRs              []PR
	Shared           map[string]Share // PR key -> its newest share in Slack, by someone else
	// PR key -> who reacted on the Slack share of your PR, e.g. "#reviews: Bob ✅".
	Reactions map[string]string
	// PR key -> the Slack message that shared it: the newest by someone else,
	// or on your PR the newest by anyone.
	SlackLinks map[string]string
}

// Share is a PR link someone posted in Slack.
type Share struct {
	Channel string // e.g. "reviews"; empty for a DM
	From    string // who posted it
	At      time.Time
}

// searchLimit caps each inbox search; past it, PRs drop out of the inbox.
const searchLimit = 300

// maxBatches caps how many PR-loading queries a sync runs at once. Loading
// full PR data is the slow part of a sync, so it is split across queries of
// as few as two PRs each, but never more than this many, to stay clear of
// GitHub's secondary rate limits.
const maxBatches = 10

// FetchInbox gets the open PRs that involve you in orgs (all of GitHub when
// empty), plus every open PR in the watched repos (owner/name). The searches
// return only IDs and run at once; searching with the full PR data is several
// times slower, and the searches overlap. The PRs in known (node IDs, from
// the last sync) are loaded while the searches run, so only the PRs that are
// new wait for them. A known PR that no search finds any more is left out.
// The PRs in keep (node IDs, e.g. shared in Slack) are in the inbox whether a
// search finds them or not, while they are open.
func (c *Client) FetchInbox(orgs, watch, known, keep []string) (*Inbox, error) {
	inbox := &Inbox{Watched: map[string]bool{}}
	for _, r := range watch {
		inbox.Watched[r] = true
	}
	var queries []string
	scopes := orgs
	if len(scopes) == 0 {
		scopes = []string{""}
	}
	for _, org := range scopes {
		for _, filter := range []string{"review-requested:@me", "involves:@me", "reviewed-by:@me"} {
			q := "is:open is:pr archived:false " + filter
			if org != "" {
				q = fmt.Sprintf("is:open is:pr archived:false org:%s %s", org, filter)
			}
			queries = append(queries, q)
		}
	}
	for _, repo := range watch {
		queries = append(queries, "is:open is:pr archived:false repo:"+repo)
	}

	var wg sync.WaitGroup
	ids := make([][]string, len(queries))
	errs := make([]error, len(queries)+1)
	for i, q := range queries {
		wg.Go(func() { ids[i], errs[i] = c.search(q, searchLimit) })
	}
	wg.Go(func() { inbox.Viewer, inbox.Teams, errs[len(queries)] = c.viewerTeams(orgs) })
	var loaded []PR
	var loadErr error
	wg.Go(func() {
		loaded, loadErr = c.loadPRs(slices.Compact(slices.Sorted(slices.Values(slices.Concat(known, keep)))))
	})
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("fetch inbox: %w", err)
	}

	byID := map[string]PR{}
	if loadErr == nil {
		for _, pr := range loaded {
			byID[pr.ID] = pr
		}
	}
	var unique, missing []string
	seen := map[string]bool{}
	for _, list := range append(ids, keep) {
		for _, id := range list {
			if seen[id] {
				continue
			}
			seen[id] = true
			unique = append(unique, id)
			if _, ok := byID[id]; !ok {
				missing = append(missing, id)
			}
		}
	}
	more, err := c.loadPRs(missing)
	if err != nil {
		return nil, fmt.Errorf("fetch inbox: %w", err)
	}
	for _, pr := range more {
		byID[pr.ID] = pr
	}
	for _, id := range unique {
		if pr, ok := byID[id]; ok && (pr.State == "OPEN" || !slices.Contains(keep, id)) {
			inbox.PRs = append(inbox.PRs, pr)
		}
	}
	return inbox, nil
}

// loadPRs loads full PR data for the given node IDs, in batches run at once.
func (c *Client) loadPRs(ids []string) ([]PR, error) {
	size := max(2, (len(ids)+maxBatches-1)/maxBatches)
	batches := make([][]PR, (len(ids)+size-1)/size)
	errs := make([]error, len(batches))
	var wg sync.WaitGroup
	for i := range batches {
		chunk := ids[i*size : min((i+1)*size, len(ids))]
		wg.Go(func() { batches[i], errs[i] = c.prsByID(chunk) })
	}
	wg.Wait()
	return slices.Concat(batches...), errors.Join(errs...)
}

// viewerTeams is your login and the teams you're on in orgs.
func (c *Client) viewerTeams(orgs []string) (string, []string, error) {
	me, err := c.Viewer()
	if err != nil {
		return "", nil, err
	}
	var teams []string
	for _, org := range orgs {
		var resp struct {
			Organization *struct {
				Teams struct {
					Nodes []struct {
						Slug string `json:"slug"`
					} `json:"nodes"`
				} `json:"teams"`
			} `json:"organization"`
		}
		err := c.gql.Do(`query($org: String!, $me: String!) {
  organization(login: $org) { teams(first: 100, userLogins: [$me]) { nodes { slug } } } }`,
			map[string]any{"org": org, "me": me}, &resp)
		if err != nil {
			return "", nil, fmt.Errorf("teams in %s: %w", org, err)
		}
		if resp.Organization != nil {
			for _, t := range resp.Organization.Teams.Nodes {
				teams = append(teams, t.Slug)
			}
		}
	}
	return me, teams, nil
}

// prsByID loads full PR data for the given node IDs.
func (c *Client) prsByID(ids []string) ([]PR, error) {
	var resp struct {
		Nodes []*PR `json:"nodes"`
	}
	me, _ := c.Viewer()
	if err := c.gql.Do(`query($ids: [ID!]!, $me: String) { nodes(ids: $ids) { ...pr } }`+prFields, map[string]any{"ids": ids, "me": me}, &resp); err != nil {
		return nil, fmt.Errorf("load PRs: %w", err)
	}
	var out []PR
	for _, pr := range resp.Nodes {
		if pr != nil && pr.ID != "" {
			pr.mergeReviews()
			out = append(out, *pr)
		}
	}
	return out, nil
}

// Viewer returns the authenticated user's login. It is asked once and
// remembered; a failed lookup is asked again next time.
func (c *Client) Viewer() (string, error) {
	c.viewerMu.Lock()
	defer c.viewerMu.Unlock()
	if c.viewer != "" {
		return c.viewer, nil
	}
	var v struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}
	if err := c.gql.Do(`{ viewer { login } }`, nil, &v); err != nil {
		return "", fmt.Errorf("viewer: %w", err)
	}
	c.viewer = v.Viewer.Login
	return c.viewer, nil
}

// Detail is the full conversation for a single PR.
type Detail struct {
	Body     string
	Reviews  []Review
	Comments []Comment
	Threads  []Thread
	Files    []File
	Events   []Event // pushes, force pushes, review requests
	Checks   []Check // CI on the head commit
}

// Event is a timeline item that is not a review or comment.
type Event struct {
	Typename          string    `json:"__typename"`
	CreatedAt         time.Time `json:"createdAt"`
	Actor             *Actor    `json:"actor"`
	RequestedReviewer *struct {
		Login string `json:"login"`
		Slug  string `json:"slug"`
	} `json:"requestedReviewer"`
	PreviousReviewState string `json:"previousReviewState"`
	Review              *struct {
		ID string `json:"id"`
	} `json:"review"`
	Commit *struct {
		Oid             string    `json:"oid"`
		CommittedDate   time.Time `json:"committedDate"`
		MessageHeadline string    `json:"messageHeadline"`
		Author          *struct {
			User *Actor `json:"user"`
		} `json:"author"`
	} `json:"commit"`
}

type File struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// VIEWED, UNVIEWED, or DISMISSED (viewed, then changed).
	ViewerViewedState string `json:"viewerViewedState"`
}

func (c *Client) FetchDetail(repo string, number int) (*Detail, error) {
	owner, name, _ := strings.Cut(repo, "/")
	q := `query($owner: String!, $name: String!, $n: Int!) {
  repository(owner: $owner, name: $name) { pullRequest(number: $n) {
    body
    reviews(last: 50) { nodes { id author { login __typename } state submittedAt body commit { oid } } }
    comments(last: 100) { nodes { id author { login __typename } createdAt body url } }
    reviewThreads(first: 100) { ` + threadFields + ` }
    files(first: 100) { ` + fileFields + ` }
    commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) { ` + checkFields + ` } } } } }
    timelineItems(last: 100, itemTypes: [PULL_REQUEST_COMMIT, HEAD_REF_FORCE_PUSHED_EVENT, REVIEW_REQUESTED_EVENT, READY_FOR_REVIEW_EVENT, REVIEW_DISMISSED_EVENT]) {
      nodes {
        __typename
        ... on PullRequestCommit { commit { oid committedDate messageHeadline author { user { login } } } }
        ... on HeadRefForcePushedEvent { createdAt actor { login } }
        ... on ReviewRequestedEvent { createdAt actor { login } requestedReviewer { ... on User { login } ... on Team { slug } } }
        ... on ReadyForReviewEvent { createdAt actor { login } }
        ... on ReviewDismissedEvent { createdAt actor { login } previousReviewState review { id } }
      }
    }
  } }
}`
	var resp struct {
		Repository struct {
			PullRequest struct {
				Body    string `json:"body"`
				Reviews struct {
					Nodes []Review `json:"nodes"`
				} `json:"reviews"`
				Comments struct {
					Nodes []Comment `json:"nodes"`
				} `json:"comments"`
				prPage
				TimelineItems struct {
					Nodes []Event `json:"nodes"`
				} `json:"timelineItems"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": owner, "name": name, "n": number}
	if err := c.gql.Do(q, vars, &resp); err != nil {
		return nil, fmt.Errorf("fetch detail: %w", err)
	}
	p := resp.Repository.PullRequest
	threads, err := allPages(c, p.ReviewThreads, vars, `reviewThreads(first: 100, after: $after) { `+threadFields+` }`,
		func(pr prPage) page[Thread] { return pr.ReviewThreads })
	if err != nil {
		return nil, err
	}
	files, err := allPages(c, p.Files, vars, `files(first: 100, after: $after) { `+fileFields+` }`,
		func(pr prPage) page[File] { return pr.Files })
	if err != nil {
		return nil, err
	}
	det := &Detail{
		Body: p.Body, Reviews: p.Reviews.Nodes, Comments: p.Comments.Nodes,
		Threads: threads, Files: files, Events: p.TimelineItems.Nodes,
	}
	for _, e := range det.Events {
		if e.Typename == "ReviewDismissedEvent" && e.Review != nil {
			setPreviousState(det.Reviews, e.Review.ID, e.PreviousReviewState)
		}
	}
	for _, cm := range p.Commits.Nodes {
		if cm.Commit.StatusCheckRollup == nil {
			continue
		}
		checks, err := allPages(c, cm.Commit.StatusCheckRollup.Contexts, vars,
			`commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100, after: $after) { `+checkFields+` } } } } }`,
			func(pr prPage) page[checkNode] {
				if len(pr.Commits.Nodes) == 0 || pr.Commits.Nodes[0].Commit.StatusCheckRollup == nil {
					return page[checkNode]{}
				}
				return pr.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts
			})
		if err != nil {
			return nil, err
		}
		det.Checks = latestChecks(checks)
	}
	return det, nil
}

// The fields of a PR's threads, files and checks, shared by FetchDetail and
// the queries for the pages after the first.
const (
	threadFields = `nodes { id isResolved isOutdated path line startLine originalLine diffSide
      comments(first: 50) { nodes { id author { login __typename } createdAt body url diffHunk } } }
    pageInfo { hasNextPage endCursor }`
	fileFields  = `nodes { path additions deletions viewerViewedState } pageInfo { hasNextPage endCursor }`
	checkFields = `nodes {
      __typename
      ... on CheckRun { databaseId name status conclusion startedAt completedAt detailsUrl title summary
        isRequired(pullRequestNumber: $n)
        checkSuite { workflowRun { workflow { name } } }
        annotations(first: 10) { nodes { path message annotationLevel location { start { line } } } } }
      ... on StatusContext { context state description targetUrl createdAt isRequired(pullRequestNumber: $n) }
    }
    pageInfo { hasNextPage endCursor }`
)

// maxPages caps how many more pages of threads, files or checks a PR loads.
const maxPages = 10

// page is one page of a GraphQL connection.
type page[T any] struct {
	Nodes    []T `json:"nodes"`
	PageInfo struct {
		HasNextPage bool   `json:"hasNextPage"`
		EndCursor   string `json:"endCursor"`
	} `json:"pageInfo"`
}

// prPage is the paged connections of a PR: what FetchDetail's query and the
// queries for a later page of one of them return.
type prPage struct {
	ReviewThreads page[Thread] `json:"reviewThreads"`
	Files         page[File]   `json:"files"`
	Commits       struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts page[checkNode] `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// allPages returns the nodes of first and of the pages after it, which it loads
// with field, a connection taking $after, picked out of the answer by get.
func allPages[T any](c *Client, first page[T], vars map[string]any, field string, get func(prPage) page[T]) ([]T, error) {
	nodes := first.Nodes
	cur := first
	for i := 0; i < maxPages && cur.PageInfo.HasNextPage; i++ {
		var resp struct {
			Repository struct {
				PullRequest prPage `json:"pullRequest"`
			} `json:"repository"`
		}
		v := maps.Clone(vars)
		v["after"] = cur.PageInfo.EndCursor
		q := `query($owner: String!, $name: String!, $n: Int!, $after: String) {
  repository(owner: $owner, name: $name) { pullRequest(number: $n) { ` + field + ` } } }`
		if err := c.gql.Do(q, v, &resp); err != nil {
			return nil, fmt.Errorf("fetch detail: %w", err)
		}
		cur = get(resp.Repository.PullRequest)
		nodes = append(nodes, cur.Nodes...)
	}
	return nodes, nil
}

// PRDiff returns the full unified diff of the PR.
func (c *Client) PRDiff(repo string, number int) (string, error) {
	return c.rawDiff(fmt.Sprintf("repos/%s/pulls/%d", repo, number))
}

// CompareDiff returns the diff between two commits (used for "since my last review").
func (c *Client) CompareDiff(repo, from, to string) (string, error) {
	return c.rawDiff(fmt.Sprintf("repos/%s/compare/%s...%s", repo, from, to))
}

func (c *Client) rawDiff(path string) (string, error) {
	resp, err := c.diff.Request("GET", path, nil)
	if err != nil {
		return "", fmt.Errorf("raw diff: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

type DraftComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"` // RIGHT (new) or LEFT (old)
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
	Commit    string `json:"-"` // the commit the line is on; "" is the review's
}

// SubmitReview posts a review. event is APPROVE, REQUEST_CHANGES or COMMENT.
// If GitHub rejects the inline comments (e.g. a line outside the diff), the
// comments are folded into the review body and the review is retried.
func (c *Client) SubmitReview(repo string, number int, commitID, event, body string, comments []DraftComment) (string, error) {
	post := func(body string, comments []DraftComment) (string, error) {
		payload := map[string]any{"event": event, "body": body}
		if commitID != "" {
			payload["commit_id"] = commitID
		}
		if len(comments) > 0 {
			payload["comments"] = comments
		}
		b, _ := json.Marshal(payload)
		var out struct {
			HTMLURL string `json:"html_url"`
		}
		err := c.rest.Post(fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number), bytes.NewReader(b), &out)
		return out.HTMLURL, err
	}
	url, err := post(body, comments)
	var herr *api.HTTPError
	if err == nil || len(comments) == 0 || !errors.As(err, &herr) || herr.StatusCode != 422 {
		return url, err
	}
	var sb strings.Builder
	sb.WriteString(body)
	sb.WriteString("\n\n---\n")
	for _, cm := range comments {
		fmt.Fprintf(&sb, "\n**`%s:%d`**\n%s\n", cm.Path, cm.Line, cm.Body)
	}
	return post(sb.String(), nil)
}

// ReplyToThread adds a reply to an existing review thread.
func (c *Client) ReplyToThread(threadID, body string) error {
	q := `mutation($id: ID!, $body: String!) { addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $id, body: $body}) { comment { id } } }`
	return c.gql.Do(q, map[string]any{"id": threadID, "body": body}, nil)
}

// ResolveThread resolves a review thread, or reopens it.
func (c *Client) ResolveThread(threadID string, resolved bool) error {
	q := `mutation($id: ID!) { unresolveReviewThread(input: {threadId: $id}) { thread { id } } }`
	if resolved {
		q = `mutation($id: ID!) { resolveReviewThread(input: {threadId: $id}) { thread { id } } }`
	}
	return c.gql.Do(q, map[string]any{"id": threadID}, nil)
}

// Person is a GitHub user as their profile shows them.
type Person struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"` // public email, often empty
}

// OrgMembers lists an org's members.
func (c *Client) OrgMembers(org string) ([]Person, error) {
	var out []Person
	var after *string
	for range 10 {
		var resp struct {
			Organization *struct {
				MembersWithRole page[Person] `json:"membersWithRole"`
			} `json:"organization"`
		}
		err := c.gql.Do(`query($org: String!, $after: String) { organization(login: $org) {
  membersWithRole(first: 100, after: $after) { nodes { login name email } pageInfo { hasNextPage endCursor } } } }`,
			map[string]any{"org": org, "after": after}, &resp)
		if err != nil {
			return nil, fmt.Errorf("members of %s: %w", org, err)
		}
		if resp.Organization == nil {
			return nil, fmt.Errorf("org %s not found", org)
		}
		out = append(out, resp.Organization.MembersWithRole.Nodes...)
		pi := resp.Organization.MembersWithRole.PageInfo
		if !pi.HasNextPage {
			break
		}
		after = &pi.EndCursor
	}
	return out, nil
}

// OrgRepos lists an org's repos that aren't archived, as owner/name.
func (c *Client) OrgRepos(org string) ([]string, error) {
	var out []string
	var after *string
	for range 10 {
		var resp struct {
			Organization *struct {
				Repositories page[struct {
					NameWithOwner string `json:"nameWithOwner"`
					IsArchived    bool   `json:"isArchived"`
				}] `json:"repositories"`
			} `json:"organization"`
		}
		err := c.gql.Do(`query($org: String!, $after: String) { organization(login: $org) {
  repositories(first: 100, after: $after) { nodes { nameWithOwner isArchived } pageInfo { hasNextPage endCursor } } } }`,
			map[string]any{"org": org, "after": after}, &resp)
		if err != nil {
			return nil, fmt.Errorf("repos of %s: %w", org, err)
		}
		if resp.Organization == nil {
			return nil, fmt.Errorf("org %s not found", org)
		}
		for _, r := range resp.Organization.Repositories.Nodes {
			if !r.IsArchived {
				out = append(out, r.NameWithOwner)
			}
		}
		pi := resp.Organization.Repositories.PageInfo
		if !pi.HasNextPage {
			break
		}
		after = &pi.EndCursor
	}
	return out, nil
}

// PR fetches one pull request, e.g. one shared as a link.
func (c *Client) PR(repo string, number int) (*PR, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("repo %q is not owner/name", repo)
	}
	var resp struct {
		Repository *struct {
			PullRequest *PR `json:"pullRequest"`
		} `json:"repository"`
	}
	me, _ := c.Viewer()
	err := c.gql.Do(`query($owner: String!, $name: String!, $n: Int!, $me: String) {
  repository(owner: $owner, name: $name) { pullRequest(number: $n) { ...pr } } }`+prFields,
		map[string]any{"owner": owner, "name": name, "n": number, "me": me}, &resp)
	if err != nil {
		return nil, fmt.Errorf("pr: %w", err)
	}
	if resp.Repository == nil || resp.Repository.PullRequest == nil {
		return nil, fmt.Errorf("%s#%d not found", repo, number)
	}
	resp.Repository.PullRequest.mergeReviews()
	return resp.Repository.PullRequest, nil
}

// RequestReviewers asks people, and teams (by slug), to review the PR. For
// someone who already reviewed, it asks again.
func (c *Client) RequestReviewers(repo string, number int, logins, teams []string) error {
	b, _ := json.Marshal(map[string]any{"reviewers": append([]string{}, logins...), "team_reviewers": append([]string{}, teams...)})
	return c.rest.Post(fmt.Sprintf("repos/%s/pulls/%d/requested_reviewers", repo, number), bytes.NewReader(b), nil)
}

// SetFileViewed toggles GitHub's per-user "Viewed" checkbox for a file.
func (c *Client) SetFileViewed(prID, path string, viewed bool) error {
	q := `mutation($id: ID!, $path: String!) { markFileAsViewed(input: {pullRequestId: $id, path: $path}) { clientMutationId } }`
	if !viewed {
		q = `mutation($id: ID!, $path: String!) { unmarkFileAsViewed(input: {pullRequestId: $id, path: $path}) { clientMutationId } }`
	}
	return c.gql.Do(q, map[string]any{"id": prID, "path": path}, nil)
}

// Comment adds a comment to a PR's conversation. subjectID is the PR's node
// ID.
func (c *Client) Comment(subjectID, body string) error {
	q := `mutation($id: ID!, $body: String!) { addComment(input: {subjectId: $id, body: $body}) { clientMutationId } }`
	return c.gql.Do(q, map[string]any{"id": subjectID, "body": body}, nil)
}

// ClosePR closes a pull request without merging it.
func (c *Client) ClosePR(prID string) error {
	q := `mutation($id: ID!) { closePullRequest(input: {pullRequestId: $id}) { pullRequest { id } } }`
	return c.gql.Do(q, map[string]any{"id": prID}, nil)
}

// SetDraft converts a pull request to a draft, or marks it ready for review.
func (c *Client) SetDraft(prID string, draft bool) error {
	q := `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { id } } }`
	if draft {
		q = `mutation($id: ID!) { convertPullRequestToDraft(input: {pullRequestId: $id}) { pullRequest { id } } }`
	}
	return c.gql.Do(q, map[string]any{"id": prID}, nil)
}
