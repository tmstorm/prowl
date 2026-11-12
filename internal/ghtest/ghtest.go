// Package ghtest is a fake GitHub for tests: an http.RoundTripper that
// answers prowl's queries from fixtures.
package ghtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	"github.com/tmstorm/prowl/internal/gh"
)

// GitHub answers prowl's GitHub queries from fixtures and records writes.
// Tests may change the fixtures between syncs.
type GitHub struct {
	mu           sync.Mutex
	Viewer       string
	PRs          []gh.PR
	Bodies       map[int]string
	Comments     map[int][]gh.Comment
	Threads      map[int][]gh.Thread
	PRReviews    map[int][]gh.Review // the reviews on a PR's page
	Timeline     map[int][]any       // PR -> timeline items, as GraphQL returns them
	Bins         map[string]string   // fake programs a test puts on PATH: name -> shell script; unused here
	Others       []gh.PR             // open PRs that don't involve you: only repo searches find them
	Slow         bool                // searches hang, so prowl stays on its first sync
	Diffs        map[int]string
	Compares     map[string]string // "from...to" -> diff
	Checks       map[int][]any     // PR -> check contexts, as GraphQL returns them (see CheckRun)
	JobLogs      map[int64]string  // Actions job -> its raw log
	JobSteps     map[int64]string  // Actions job -> the step that failed
	RejectReview map[int]bool      // review submissions (1 is the first) that fail with a server error
	Repos        []string
	Closed       map[string]bool
	Release      *gh.Release      // the latest release of any repo; nil: none
	Assets       map[int64][]byte // release asset -> its bytes
	OnAsset      func(id int64)   // called as each release asset downloads
	releaseCalls int
	writes       []string
	reviewCalls  int
	reviews      []map[string]any
}

func (f *GitHub) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	reply := func(code int, v any) (*http.Response, error) {
		b, _ := json.Marshal(v)
		if s, ok := v.(string); ok {
			b = []byte(s)
		}
		return &http.Response{StatusCode: code, Request: r, Body: io.NopCloser(strings.NewReader(string(b))),
			Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	}
	if strings.HasSuffix(r.URL.Path, "/graphql") {
		var q struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &q)
		data := f.graphql(q.Query, q.Variables)
		return reply(200, map[string]any{"data": data})
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v3")
	if r.Method != http.MethodGet {
		f.writes = append(f.writes, r.Method+" "+path)
		if strings.HasSuffix(path, "/reviews") {
			f.reviewCalls++
			if f.RejectReview[f.reviewCalls] {
				return reply(500, map[string]any{"message": "server error"})
			}
			var rv map[string]any
			_ = json.Unmarshal(body, &rv)
			f.reviews = append(f.reviews, rv)
			if rv["event"] == "APPROVE" {
				for i := range f.PRs {
					if strings.HasSuffix(path, fmt.Sprintf("/%s/pulls/%d/reviews", f.PRs[i].Repository.Name, f.PRs[i].Number)) {
						f.PRs[i].ReviewRequests.Nodes = nil
						f.PRs[i].Reviews.Nodes = append(f.PRs[i].Reviews.Nodes, gh.Review{Author: &gh.Actor{Login: f.Viewer}, State: "APPROVED", SubmittedAt: time.Now()})
					}
				}
			}
			return reply(200, map[string]any{"html_url": "https://github.com/review/1"})
		}
		return reply(200, map[string]any{})
	}
	if m := regexp.MustCompile(`/actions/jobs/(\d+)(/logs)?$`).FindStringSubmatch(path); m != nil {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		if m[2] != "" {
			return reply(200, f.JobLogs[id])
		}
		return reply(200, map[string]any{"steps": []map[string]string{{"name": "Set up job", "conclusion": "success"}, {"name": f.JobSteps[id], "conclusion": "failure"}}})
	}
	if strings.HasSuffix(path, "/releases/latest") {
		f.releaseCalls++
		if f.Release == nil {
			return reply(404, map[string]any{"message": "not found"})
		}
		return reply(200, f.Release)
	}
	if m := regexp.MustCompile(`/releases/assets/(\d+)$`).FindStringSubmatch(path); m != nil {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		if f.OnAsset != nil {
			f.OnAsset(id)
		}
		return &http.Response{StatusCode: 200, Request: r, Body: io.NopCloser(bytes.NewReader(f.Assets[id])),
			Header: http.Header{"Content-Type": []string{"application/octet-stream"}}}, nil
	}
	if m := regexp.MustCompile(`/compare/(.+)$`).FindStringSubmatch(path); m != nil {
		return reply(200, f.Compares[m[1]])
	}
	if m := regexp.MustCompile(`/pulls/(\d+)$`).FindStringSubmatch(path); m != nil {
		n, _ := strconv.Atoi(m[1])
		return reply(200, f.Diffs[n])
	}
	return reply(404, map[string]any{"message": "not found"})
}

// ReleaseCalls is how often prowl asked for the latest release.
func (f *GitHub) ReleaseCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.releaseCalls
}

func (f *GitHub) graphql(q string, v map[string]any) any {
	switch {
	case strings.Contains(q, "mutation"):
		f.writes = append(f.writes, "mutation "+strings.Fields(strings.SplitN(q, "{", 3)[1])[0])
		return map[string]any{}
	case strings.HasPrefix(strings.TrimSpace(q), "{ viewer"):
		return map[string]any{"viewer": map[string]any{"login": f.Viewer}}
	case strings.Contains(q, "teams(first"):
		return map[string]any{"organization": map[string]any{"teams": map[string]any{"nodes": []any{}}}}
	case strings.Contains(q, "repositories(first"):
		var nodes []any
		for _, r := range f.Repos {
			nodes = append(nodes, map[string]any{"nameWithOwner": r})
		}
		return map[string]any{"organization": map[string]any{"repositories": map[string]any{"nodes": nodes, "pageInfo": map[string]any{}}}}
	case strings.Contains(q, "search(query"):
		if f.Slow {
			f.mu.Unlock()
			time.Sleep(time.Minute)
			f.mu.Lock()
		}
		query, _ := v["q"].(string)
		var nodes []gh.PR
		_, repo, inRepo := strings.Cut(query, "repo:")
		for _, pr := range append(append([]gh.PR{}, f.PRs...), f.Others...) {
			switch {
			case inRepo && pr.Repository.NameWithOwner == repo:
				nodes = append(nodes, pr)
			case !inRepo && strings.Contains(query, "involves:@me") && slices.ContainsFunc(f.PRs, func(p gh.PR) bool { return p.ID == pr.ID }):
				nodes = append(nodes, pr)
			}
		}
		var ids []map[string]string
		for _, pr := range nodes {
			ids = append(ids, map[string]string{"id": pr.ID})
		}
		return map[string]any{"search": map[string]any{"nodes": ids, "pageInfo": map[string]any{}}}
	case strings.Contains(q, "nodes(ids:"):
		var nodes []gh.PR
		for _, id := range v["ids"].([]any) {
			for _, pr := range append(append([]gh.PR{}, f.PRs...), f.Others...) {
				if pr.ID == id {
					nodes = append(nodes, pr)
				}
			}
		}
		return map[string]any{"nodes": nodes}
	case strings.Contains(q, "pullRequest(number") && strings.Contains(q, "files(first"):
		n := int(v["n"].(float64))
		return map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"body": f.Bodies[n], "reviews": map[string]any{"nodes": f.PRReviews[n]}, "comments": map[string]any{"nodes": f.Comments[n]},
			"reviewThreads": map[string]any{"nodes": f.Threads[n]}, "files": map[string]any{"nodes": []any{}},
			"timelineItems": map[string]any{"nodes": f.Timeline[n]},
			"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{
				"statusCheckRollup": map[string]any{"contexts": map[string]any{"nodes": f.Checks[n]}}}}}},
		}}}
	case strings.Contains(q, "pullRequest(number"):
		repo := fmt.Sprintf("%s/%s", v["owner"], v["name"])
		n := int(v["n"].(float64))
		for _, pr := range append(append([]gh.PR{}, f.PRs...), f.Others...) {
			if pr.Repository.NameWithOwner == repo && pr.Number == n {
				pr.State = "OPEN"
				if f.Closed[pr.Key()] {
					pr.State = "MERGED"
				}
				return map[string]any{"repository": map[string]any{"pullRequest": pr}}
			}
		}
		return map[string]any{"repository": map[string]any{"pullRequest": nil}}
	}
	return map[string]any{}
}

// PR builds an open PR in owner o.
func PR(repo string, n int, title, author string) gh.PR {
	p := gh.PR{ID: fmt.Sprintf("%s-%d", repo, n), Number: n, Title: title, State: "OPEN",
		Author: &gh.Actor{Login: author}, CreatedAt: time.Now().Add(-2 * time.Hour), UpdatedAt: time.Now().Add(-time.Hour),
		URL: fmt.Sprintf("https://github.com/o/%s/pull/%d", repo, n), HeadRefOid: "abc1234", BaseRefName: "main"}
	p.Repository.Name, p.Repository.NameWithOwner = repo, "o/"+repo
	return p
}

// Requested adds who as a requested reviewer.
func Requested(p gh.PR, who string) gh.PR {
	var rr gh.ReviewRequest
	rr.RequestedReviewer.Login = who
	p.ReviewRequests.Nodes = append(p.ReviewRequests.Nodes, rr)
	return p
}

// Client is a GitHub client talking to f, as the viewer (default "me").
func (f *GitHub) Client() (*gh.Client, error) {
	f.mu.Lock()
	if f.Viewer == "" {
		f.Viewer = "me"
	}
	f.mu.Unlock()
	return gh.NewWith(api.ClientOptions{Host: "github.com", AuthToken: "test", Transport: f})
}

// Wrote reports whether prowl sent a write starting with prefix, such as
// "POST /repos/o/api/pulls/7/reviews" or "mutation addPullRequestReview".
func (f *GitHub) Wrote(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.writes, func(w string) bool { return strings.HasPrefix(w, prefix) })
}

// Reviews are the reviews prowl submitted, as sent.
func (f *GitHub) Reviews() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reviews)
}

// CheckRun is a check run context for Checks: an Actions job when job is
// not 0. conclusion "" means it is still running.
func CheckRun(name, conclusion string, required bool, job int64) map[string]any {
	c := map[string]any{"__typename": "CheckRun", "name": name, "status": "COMPLETED", "conclusion": conclusion,
		"isRequired": required, "databaseId": job, "detailsUrl": "https://github.com/o/checks/" + name,
		"startedAt": time.Now().Add(-3 * time.Minute), "completedAt": time.Now().Add(-time.Minute)}
	if conclusion == "" {
		c["status"], c["completedAt"] = "IN_PROGRESS", nil
	}
	if job != 0 {
		c["checkSuite"] = map[string]any{"workflowRun": map[string]any{"workflow": map[string]any{"name": "ci"}}}
	}
	return c
}
