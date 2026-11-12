package gh_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/tmstorm/prowl/internal/gh"

	"github.com/cli/go-gh/v2/pkg/api"
)

type roundTrip func(*http.Request) *http.Response

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	resp := f(r)
	resp.Request = r
	return resp, nil
}

func reply(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)),
		Header: http.Header{"Content-Type": []string{"application/json"}}}
}

func fake(t *testing.T, f roundTrip) *gh.Client {
	t.Helper()
	c, err := gh.NewWith(api.ClientOptions{Host: "github.com", AuthToken: "test", Transport: f})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A search with more results than one page follows the pages.
func TestFetchInboxPages(t *testing.T) {
	calls := 0
	c := fake(t, func(r *http.Request) *http.Response {
		b, _ := io.ReadAll(r.Body)
		q := string(b)
		switch {
		case strings.Contains(q, "{ viewer {"):
			return reply(200, `{"data":{"viewer":{"login":"me"}}}`)
		case strings.Contains(q, "organization(login"):
			return reply(200, `{"data":{"organization":{"teams":{"nodes":[]}}}}`)
		case strings.Contains(q, `"after":null`) && strings.Contains(q, "involves"):
			calls++
			return reply(200, `{"data":{"search":{"nodes":[{"id":"A"}],"pageInfo":{"hasNextPage":true,"endCursor":"c1"}}}}`)
		case strings.Contains(q, `"after":"c1"`):
			calls++
			return reply(200, `{"data":{"search":{"nodes":[{"id":"B"}],"pageInfo":{"hasNextPage":false}}}}`)
		case strings.Contains(q, "nodes(ids:"):
			return reply(200, `{"data":{"nodes":[{"id":"A","number":1},{"id":"B","number":2}]}}`)
		}
		return reply(200, `{"data":{"search":{"nodes":[],"pageInfo":{"hasNextPage":false}}}}`)
	})
	in, err := c.FetchInbox([]string{"o"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.PRs) != 2 || calls != 2 {
		t.Fatalf("got %d PRs from %d pages, want 2 from 2", len(in.PRs), calls)
	}
}

// A rejected inline comment (422) retries with the comments in the body; any
// other error does not, since the first post may have gone through.
func TestSubmitReviewFallback(t *testing.T) {
	for _, c := range []struct {
		first int
		posts int
	}{{422, 2}, {502, 1}} {
		posts := 0
		cl := fake(t, func(r *http.Request) *http.Response {
			posts++
			if posts == 1 {
				return reply(c.first, `{"message":"nope"}`)
			}
			return reply(200, `{"html_url":"u"}`)
		})
		_, _ = cl.SubmitReview("o/r", 1, "abc", "COMMENT", "body", []gh.DraftComment{{Path: "a.go", Line: 1, Body: "x"}})
		if posts != c.posts {
			t.Errorf("first response %d: %d posts, want %d", c.first, posts, c.posts)
		}
	}
}

func TestIsBot(t *testing.T) {
	for _, c := range []struct {
		a    *gh.Actor
		want bool
	}{
		{&gh.Actor{Login: "axis", Typename: "Bot"}, true},
		{&gh.Actor{Login: "dependabot[bot]"}, true},
		{&gh.Actor{Login: "sovereignify-agents"}, true},
		{&gh.Actor{Login: "alice", Typename: "User"}, false},
	} {
		if got := c.a.IsBot(map[string]bool{"sovereignify-agents": true}); got != c.want {
			t.Errorf("%+v: %v", c.a, got)
		}
	}
}

// Terminal control characters in GitHub's answers are replaced, in JSON
// (go-gh does that) and in diffs.
func TestCleansControlCharacters(t *testing.T) {
	c := fake(t, func(r *http.Request) *http.Response {
		if strings.HasSuffix(r.URL.Path, "/pulls/1") {
			resp := reply(200, "+evil \x1b]52;c;aGk=\x07 line\r\n+ok\n")
			resp.Header.Set("Content-Type", "application/vnd.github.diff")
			return resp
		}
		return reply(200, `{"data":{"repository":{"pullRequest":{"title":"a \u001b[2J b","state":"OPEN","number":1}}}}`)
	})
	pr, err := c.PR("o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(pr.Title, 0x1b) {
		t.Fatalf("title %q", pr.Title)
	}
	diff, err := c.PRDiff("o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(diff, "\x1b\x07\r") || !strings.Contains(diff, "+ok") {
		t.Fatalf("diff %q", diff)
	}
}

// Your own reviews are fetched on their own and merged in, so an old review
// of yours outside the latest reviews by everyone still counts.
func TestMyReviewsMergedIn(t *testing.T) {
	c := fake(t, func(r *http.Request) *http.Response {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "{ viewer { login } }") {
			return reply(200, `{"data":{"viewer":{"login":"me"}}}`)
		}
		if !strings.Contains(string(b), `"me":"me"`) {
			t.Errorf("the PR query should ask for my reviews: %s", b)
		}
		return reply(200, `{"data":{"repository":{"pullRequest":{"number":1,"state":"OPEN",
  "reviews":{"nodes":[{"id":"r2","author":{"login":"bob"},"state":"COMMENTED","submittedAt":"2026-11-02T00:00:00Z"},
                      {"id":"r1","author":{"login":"me"},"state":"APPROVED","submittedAt":"2026-11-03T00:00:00Z"}]},
  "mine":{"nodes":[{"id":"r0","author":{"login":"me"},"state":"CHANGES_REQUESTED","submittedAt":"2026-11-01T00:00:00Z"},
                   {"id":"r1","author":{"login":"me"},"state":"APPROVED","submittedAt":"2026-11-03T00:00:00Z"}]}}}}}`)
	})
	pr, err := c.PR("o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range pr.Reviews.Nodes {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "r0,r2,r1" {
		t.Fatalf("reviews %v, want r0,r2,r1", ids)
	}
}

// A PR with more than a page of threads loads the rest of them.
func TestDetailLoadsEveryPage(t *testing.T) {
	c := fake(t, func(r *http.Request) *http.Response {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `"after":"c1"`) {
			return reply(200, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"t2"}],"pageInfo":{"hasNextPage":false}}}}}}`)
		}
		return reply(200, `{"data":{"repository":{"pullRequest":{"body":"b",
  "reviewThreads":{"nodes":[{"id":"t1"}],"pageInfo":{"hasNextPage":true,"endCursor":"c1"}},
  "files":{"nodes":[{"path":"a.go"}],"pageInfo":{"hasNextPage":false}}}}}}`)
	})
	d, err := c.FetchDetail("o/r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Threads) != 2 || d.Threads[1].ID != "t2" || len(d.Files) != 1 {
		t.Fatalf("threads %+v files %+v", d.Threads, d.Files)
	}
}

// A failed lookup of your login is asked again next time; a good one is
// remembered.
func TestViewerRetriesAfterFailure(t *testing.T) {
	calls := 0
	c := fake(t, func(r *http.Request) *http.Response {
		calls++
		if calls == 1 {
			return reply(502, `{"message":"bad gateway"}`)
		}
		return reply(200, `{"data":{"viewer":{"login":"me"}}}`)
	})
	if _, err := c.Viewer(); err == nil {
		t.Fatal("the first lookup should fail")
	}
	if me, err := c.Viewer(); err != nil || me != "me" {
		t.Fatalf("second lookup: %q %v", me, err)
	}
	if _, _ = c.Viewer(); calls != 2 {
		t.Fatalf("a good login should be remembered: %d calls", calls)
	}
}

// A new login takes effect without a restart: a 401 makes prowl read the
// token again and retry once, and it keeps the new token after that.
func TestNewLoginWithoutRestart(t *testing.T) {
	t.Setenv("GH_TOKEN", "old")
	var seen []string
	c, err := gh.NewWith(api.ClientOptions{Host: "github.com", Transport: roundTrip(func(r *http.Request) *http.Response {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "token new" {
			return reply(401, `{"message":"Bad credentials"}`)
		}
		return reply(200, `{"data":{"viewer":{"login":"me"}}}`)
	})})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "new")
	if me, err := c.Viewer(); err != nil || me != "me" {
		t.Fatalf("viewer: %q %v (sent %v)", me, err, seen)
	}
	if strings.Join(seen, ",") != "token old,token new" {
		t.Fatalf("sent %v", seen)
	}
}
