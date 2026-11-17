package core_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
	"github.com/tmstorm/prowl/internal/inbox"
	"github.com/tmstorm/prowl/internal/slack"
)

// fakeSlack answers auth.test as U1 and search.messages with matches, which
// a test may change between syncs.
type fakeSlack struct {
	mu      sync.Mutex
	matches string
}

func (f *fakeSlack) set(matches string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.matches = matches
}

func (f *fakeSlack) client(t *testing.T) *slack.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/auth.test":
			fmt.Fprint(w, `{"ok":true,"user":"me","user_id":"U1"}`)
		case "/search.messages":
			fmt.Fprintf(w, `{"ok":true,"messages":{"paging":{"pages":1},"matches":[%s]}}`, f.matches)
		default:
			fmt.Fprint(w, `{"ok":false,"error":"unknown_method"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return slack.NewWith("xoxp-test", srv.URL+"/")
}

func match(ts, user, name, channel, channelName, pr string) string {
	return fmt.Sprintf(`{"ts":%q,"user":%q,"username":%q,"text":"<https://github.com/%s>","permalink":"https://x.slack.com/archives/%s/p1","channel":{"id":%q,"name":%q,"is_im":%v}}`,
		ts, user, name, pr, channel, channel, channelName, channelName == "")
}

// A PR shared in a channel you watch shows as shared, stays after the
// message leaves the search, and goes once it merges. Shares elsewhere, and
// your own, don't count, and a share never notifies. A link may spell the
// repo in other case than GitHub.
func TestSlackShares(t *testing.T) {
	now := fmt.Sprintf("%d.000100", time.Now().Unix())
	fake := &ghtest.GitHub{Others: []gh.PR{
		ghtest.PR("api", 7, "feat: shared", "alice"),
		ghtest.PR("api", 8, "fix: elsewhere", "alice"),
		ghtest.PR("api", 9, "fix: mine to share", "alice"),
		ghtest.PR("api", 10, "docs: sent to me", "carol"),
	}, Closed: map[string]bool{}}
	co, st := newCore(t, fake)
	cfg := config.Default()
	cfg.SlackChannels = []string{"C1"}
	co.SetConfig(cfg)
	sl := &fakeSlack{}
	co.UseSlack(sl.client(t))
	sl.set(match(now, "U2", "bob", "C1", "reviews", "o/API/pull/7") + "," +
		match(now, "U2", "bob", "C9", "random", "o/api/pull/8") + "," +
		match(now, "U1", "me", "C1", "reviews", "o/api/pull/9") + "," +
		match(now, "U3", "carol", "D1", "", "o/api/pull/10"))

	changes, err := co.Refresh()
	if err != nil || co.SlackErr() != nil {
		t.Fatal(err, co.SlackErr())
	}
	if len(changes) != 0 {
		t.Fatalf("shares notify: %v", changes)
	}
	in := co.Inbox()
	if it, ok := find(in, "o/api#7"); !ok || it.Bucket != inbox.Shared || it.Reason != "shared in #reviews by bob" {
		t.Fatalf("o/api#7 = %v %+v", ok, it)
	}
	if it, ok := find(in, "o/api#10"); !ok || it.Bucket != inbox.Shared || it.Reason != "sent to you by carol" {
		t.Fatalf("o/api#10 = %v %+v", ok, it)
	}
	for _, key := range []string{"o/api#8", "o/api#9"} {
		if _, ok := find(in, key); ok {
			t.Errorf("%s is in the inbox", key)
		}
	}

	sl.set("")
	in = refresh(t, co)
	if it, ok := find(in, "o/api#7"); !ok || it.Bucket != inbox.Shared {
		t.Fatalf("after the search moved on, o/api#7 = %v %+v", ok, it)
	}

	fake.Others[0].State = "MERGED"
	fake.Closed["o/api#7"] = true
	in = refresh(t, co)
	if _, ok := find(in, "o/api#7"); ok {
		t.Fatal("merged o/api#7 is still in the inbox")
	}
	co.PruneClosed()
	shares, _ := st.SlackShares()
	for _, s := range shares {
		if s.PRKey == "o/api#7" {
			t.Fatal("merged o/api#7's share is still kept")
		}
	}
}
