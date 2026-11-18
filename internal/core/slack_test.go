package core_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
	"github.com/tmstorm/prowl/internal/slack"
	"github.com/zalando/go-keyring"
)

// fakeSlack answers auth.test as U1 and search.messages with matches, which
// a test may change between syncs, and records every other call.
type fakeSlack struct {
	mu      sync.Mutex
	matches string
	users   string // users.list members, as JSON
	emoji   string // reactions.get reactions, as JSON
	rooms   string // users.conversations channels, as JSON
	authErr string // what auth.test fails with, if set
	calls   []string
}

// took returns the calls since the last took.
func (f *fakeSlack) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func (f *fakeSlack) set(matches string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.matches = matches
}

func (f *fakeSlack) react(emoji string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emoji = emoji
}

func (f *fakeSlack) client(t *testing.T) *slack.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/auth.test":
			if f.authErr != "" {
				fmt.Fprintf(w, `{"ok":false,"error":%q}`, f.authErr)
				return
			}
			fmt.Fprint(w, `{"ok":true,"user":"me","user_id":"U1","team":"Acme","team_id":"T1"}`)
		case "/search.messages":
			fmt.Fprintf(w, `{"ok":true,"messages":{"paging":{"pages":1},"matches":[%s]}}`, f.matches)
		case "/users.list":
			fmt.Fprintf(w, `{"ok":true,"members":[%s]}`, f.users)
		case "/conversations.open":
			fmt.Fprint(w, `{"ok":true,"channel":{"id":"D9"}}`)
		case "/users.conversations":
			fmt.Fprintf(w, `{"ok":true,"channels":[%s]}`, f.rooms)
		case "/reactions.get":
			fmt.Fprintf(w, `{"ok":true,"message":{"reactions":[%s]}}`, f.emoji)
		default:
			_ = r.ParseForm()
			f.calls = append(f.calls, strings.TrimSpace(r.URL.Path[1:]+" "+r.Form.Get("channel")+" "+r.Form.Get("timestamp")+
				r.Form.Get("thread_ts")+" "+r.Form.Get("name")+r.Form.Get("text")))
			fmt.Fprint(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(srv.Close)
	return slack.NewWith(slack.Token{Access: "xoxp-test"}, srv.URL+"/")
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

	changes, err := co.Refresh(t.Context())
	if err != nil || co.SlackErr() != nil {
		t.Fatal(err, co.SlackErr())
	}
	if len(changes) != 0 {
		t.Fatalf("shares notify: %v", changes)
	}
	in := co.Inbox()
	if it, ok := find(in, "o/api#7"); !ok || it.Bucket != core.Shared || it.Reason != "shared in #reviews by bob" ||
		it.SlackLink != "https://x.slack.com/archives/C1/p1" {
		t.Fatalf("o/api#7 = %v %+v", ok, it)
	}
	if it, ok := find(in, "o/api#10"); !ok || it.Bucket != core.Shared || it.Reason != "sent to you by carol" {
		t.Fatalf("o/api#10 = %v %+v", ok, it)
	}
	for _, key := range []string{"o/api#8", "o/api#9"} {
		if _, ok := find(in, key); ok {
			t.Errorf("%s is in the inbox", key)
		}
	}
	c, _ := fake.Client()
	if it, ok := find(core.New(cfg, c, st).Inbox(), "o/api#7"); !ok || it.Bucket != core.Shared {
		t.Fatalf("a new core should start with the shares: %v %+v", ok, it)
	}

	sl.set("")
	in = refresh(t, co)
	if it, ok := find(in, "o/api#7"); !ok || it.Bucket != core.Shared {
		t.Fatalf("after the search moved on, o/api#7 = %v %+v", ok, it)
	}

	fake.Others[0].State = "MERGED"
	fake.Closed["o/api#7"] = true
	in = refresh(t, co)
	if _, ok := find(in, "o/api#7"); ok {
		t.Fatal("merged o/api#7 is still in the inbox")
	}
	co.PruneClosed(t.Context())
	shares, _ := st.SlackShares()
	for _, s := range shares {
		if s.PRKey == "o/api#7" {
			t.Fatal("merged o/api#7's share is still kept")
		}
	}
}

// Your emoji on a shared PR's message follows your review: reviewing while
// you have inline comments pending, then your review once GitHub has it,
// wherever you made it. A sync that changes nothing calls Slack for nothing,
// and submitting from prowl reacts at once.
func TestSlackReactions(t *testing.T) {
	now := time.Now()
	ts := fmt.Sprintf("%d.000100", now.Unix())
	pr := ghtest.PR("api", 7, "feat: shared", "alice")
	fake := &ghtest.GitHub{Others: []gh.PR{pr}, Closed: map[string]bool{}}
	co, _ := newCore(t, fake)
	cfg := config.Default()
	cfg.SlackChannels = []string{"C1"}
	co.SetConfig(cfg)
	sl := &fakeSlack{}
	co.UseSlack(sl.client(t))
	sl.set(match(ts, "U2", "bob", "C1", "reviews", "o/api/pull/7"))

	refresh(t, co)
	if calls := sl.took(); len(calls) != 0 {
		t.Fatalf("before any review: %q", calls)
	}
	co.SetPending("o/api#7", []core.DraftComment{{Path: "a.go", Line: 1, Body: "why?"}})
	refresh(t, co)
	if calls := sl.took(); fmt.Sprint(calls) != fmt.Sprint([]string{"reactions.add C1 " + ts + " eyes"}) {
		t.Fatalf("reviewing: %q", calls)
	}

	co.SetPending("o/api#7", nil)
	fake.Others[0].Reviews.Nodes = []gh.Review{{Author: &gh.Actor{Login: "me"}, State: "CHANGES_REQUESTED", SubmittedAt: now.Add(time.Minute)}}
	refresh(t, co)
	want := []string{"reactions.remove C1 " + ts + " eyes", "reactions.add C1 " + ts + " x"}
	if calls := sl.took(); fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("changes requested on GitHub: %q", calls)
	}
	refresh(t, co)
	if calls := sl.took(); len(calls) != 0 {
		t.Fatalf("a sync with nothing new: %q", calls)
	}

	sent, err := co.Submit(t.Context(), "o/api#7", core.Draft{Event: "APPROVE"})
	if err != nil || sent.Slack != "reacted :white_check_mark: in #reviews" {
		t.Fatalf("submit: %+v, %v", sent, err)
	}
	want = []string{"reactions.remove C1 " + ts + " x", "reactions.add C1 " + ts + " white_check_mark"}
	if calls := sl.took(); fmt.Sprint(calls) != fmt.Sprint(want) {
		t.Fatalf("approving from prowl: %q", calls)
	}
}

// With reply, submitting from prowl posts the Slack line in the share's
// thread, and a sync never posts.
func TestSlackReply(t *testing.T) {
	ts := fmt.Sprintf("%d.000100", time.Now().Unix())
	fake := &ghtest.GitHub{Others: []gh.PR{ghtest.PR("api", 7, "feat: shared", "alice")}, Closed: map[string]bool{}}
	co, _ := newCore(t, fake)
	cfg := config.Default()
	cfg.SlackChannels, cfg.SlackAfterReview = []string{"C1"}, "reply"
	co.SetConfig(cfg)
	sl := &fakeSlack{}
	co.UseSlack(sl.client(t))
	sl.set(match(ts, "U2", "bob", "C1", "reviews", "o/api/pull/7"))
	refresh(t, co)

	sent, err := co.Submit(t.Context(), "o/api#7", core.Draft{Event: "APPROVE"})
	if err != nil || sent.Slack != "replied in #reviews" {
		t.Fatalf("submit: %+v, %v", sent, err)
	}
	calls := sl.took()
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "chat.postMessage C1 "+ts+" https://github.com/o/api/pull/7 Approved") {
		t.Fatalf("calls = %q", calls)
	}
	refresh(t, co)
	if calls := sl.took(); len(calls) != 0 {
		t.Fatalf("a sync posted: %q", calls)
	}
}

// Reactions on the share of your PR, yours aside, show in its reason as
// each person's strongest emoji, and a change notifies. A share posted by
// you counts.
func TestReactionsOnYours(t *testing.T) {
	ts := fmt.Sprintf("%d.000100", time.Now().Unix())
	fake := &ghtest.GitHub{PRs: []gh.PR{ghtest.PR("api", 3, "feat: mine", "me")}}
	co, _ := newCore(t, fake)
	cfg := config.Default()
	cfg.SlackChannels = []string{"C1"}
	co.SetConfig(cfg)
	sl := &fakeSlack{users: slackUser("U2", "bob", "Bob Ames", "Bob", "") + "," + slackUser("U3", "carol", "Carol Diaz", "", "")}
	co.UseSlack(sl.client(t))
	sl.set(match(ts, "U1", "me", "C1", "reviews", "o/api/pull/3"))
	sl.react(`{"name":"eyes","users":["U3","U1","U2"]},{"name":"white_check_mark","users":["U2"]},{"name":"tada","users":["U3"]}`)

	changes, err := co.Refresh(t.Context())
	if err != nil || co.SlackErr() != nil {
		t.Fatal(err, co.SlackErr())
	}
	it, _ := find(co.Inbox(), "o/api#3")
	if it.Reason != "awaiting reviewers · #reviews: Bob ✅, Carol Diaz 👀" || it.SlackLink != "https://x.slack.com/archives/C1/p1" {
		t.Fatalf("reason = %q, link %q", it.Reason, it.SlackLink)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %v", changes)
	}
	if changes, _ := co.Refresh(t.Context()); len(changes) != 0 {
		t.Fatalf("nothing new, but changes = %v", changes)
	}
	sl.react(`{"name":"x","users":["U3"]},{"name":"white_check_mark","users":["U2"]}`)
	changes, _ = co.Refresh(t.Context())
	if it, _ := find(co.Inbox(), "o/api#3"); len(changes) != 1 || it.Reason != "awaiting reviewers · #reviews: Carol Diaz ❌, Bob ✅" {
		t.Fatalf("after carol's change: %q, %v", it.Reason, changes)
	}
}

// The channels you're in come busiest first by links to your orgs' PRs,
// and the busy ones are recommended. DM links are counted on their own.
func TestSlackActivity(t *testing.T) {
	ts := fmt.Sprintf("%d.000100", time.Now().Unix())
	co, _ := newCore(t, &ghtest.GitHub{})
	cfg := config.Default()
	cfg.Orgs = []string{"o"}
	co.SetConfig(cfg)
	sl := &fakeSlack{rooms: `{"id":"C3","name":"quiet"},{"id":"C2","name":"random"},{"id":"C1","name":"reviews","is_private":true}`}
	co.UseSlack(sl.client(t))
	var matches []string
	for i := range 6 {
		matches = append(matches, match(ts, "U2", "bob", "C1", "reviews", fmt.Sprintf("o/api/pull/%d", i)))
	}
	matches = append(matches,
		match(ts, "U2", "bob", "C2", "random", "o/api/pull/9"), match(ts, "U2", "bob", "C2", "random", "o/web/pull/9"),
		match(ts, "U2", "bob", "C2", "random", "elsewhere/lib/pull/1"), match(ts, "U2", "bob", "C2", "random", "elsewhere/lib/pull/2"),
		match(ts, "U3", "carol", "D1", "", "o/api/pull/7"))
	sl.set(strings.Join(matches, ","))

	channels, dms, err := co.SlackActivity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range channels {
		got = append(got, fmt.Sprintf("%s %d %v", c.Name, c.Links, c.Recommended))
	}
	if strings.Join(got, ", ") != "reviews 6 true, random 2 false, quiet 0 false" || dms != 1 {
		t.Fatalf("activity = %v, DMs %d", got, dms)
	}
}

// With the Slack app installed, a share links to its message in the app,
// which skips the browser.
func TestSlackAppLink(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the Slack app is found through xdg-mime on Linux")
	}
	ts := fmt.Sprintf("%d.000100", time.Now().Unix())
	co, _ := newCore(t, &ghtest.GitHub{Others: []gh.PR{ghtest.PR("api", 7, "feat: shared", "alice")}})
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "xdg-mime"), []byte("#!/bin/sh\necho slack.desktop\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	cfg := config.Default()
	cfg.SlackChannels = []string{"C1"}
	co.SetConfig(cfg)
	sl := &fakeSlack{}
	co.UseSlack(sl.client(t))
	sl.set(match(ts, "U2", "bob", "C1", "reviews", "o/api/pull/7"))
	it, _ := find(refresh(t, co), "o/api#7")
	if want := "slack://channel?id=C1&message=" + ts + "&team=T1"; it.SlackLink != want {
		t.Fatalf("link = %q, want %q", it.SlackLink, want)
	}
}

// A PR you were asked to review notifies once; a share of it in Slack later
// joins its reason without notifying again.
func TestShareOfRequestedPRDoesNotNotify(t *testing.T) {
	ts := fmt.Sprintf("%d.000100", time.Now().Unix())
	co, _ := newCore(t, &ghtest.GitHub{PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 7, "fix: crash", "alice"), "me")}})
	cfg := config.Default()
	cfg.SlackChannels = []string{"C1"}
	co.SetConfig(cfg)
	sl := &fakeSlack{}
	co.UseSlack(sl.client(t))
	if changes, _ := co.Refresh(t.Context()); len(changes) != 1 {
		t.Fatalf("first sync: %v", changes)
	}
	sl.set(match(ts, "U2", "bob", "C1", "reviews", "o/api/pull/7"))
	changes, err := co.Refresh(t.Context())
	if err != nil || len(changes) != 0 {
		t.Fatalf("after the share: %v, %v", changes, err)
	}
	if it, _ := find(co.Inbox(), "o/api#7"); it.Reason != "review requested · shared in #reviews by bob" {
		t.Fatalf("reason = %q", it.Reason)
	}
}

// Slack is an account: logged out before you sign in, logged in as you
// after, and logged out once Slack refuses the token. Login signs in with
// `prowl slack`.
func TestSlackAccount(t *testing.T) {
	co, _ := newCore(t, &ghtest.GitHub{})
	slackAuth := func() agent.Auth {
		co.CheckAuth(t.Context())
		accts := co.Accounts()
		i := slices.IndexFunc(accts, func(a core.Account) bool { return a.Key == "slack" })
		if i < 0 {
			t.Fatalf("no Slack account: %+v", accts)
		}
		return accts[i].Auth
	}
	if a := slackAuth(); !a.Known || a.LoggedIn {
		t.Fatalf("before signing in: %+v", a)
	}
	sl := &fakeSlack{}
	co.UseSlack(sl.client(t))
	if a := slackAuth(); !a.Known || !a.LoggedIn || a.Account != "me in Acme" {
		t.Fatalf("signed in: %+v", a)
	}
	sl.mu.Lock()
	sl.authErr = "token_revoked"
	sl.mu.Unlock()
	if a := slackAuth(); !a.Known || a.LoggedIn {
		t.Fatalf("revoked: %+v", a)
	}
	if login, err := co.Login("slack"); err != nil || len(login.Args) != 2 || login.Args[1] != "slack" {
		t.Fatalf("login: %v %+v", err, login)
	}
}

// A sync that finds the token refused marks Slack logged out instead of
// failing, as a check would. Slack actions are then refused until a check
// finds you logged in again.
func TestSlackLoggedOutInSync(t *testing.T) {
	co, _ := newCore(t, &ghtest.GitHub{})
	sl := &fakeSlack{authErr: "token_revoked"}
	co.UseSlack(sl.client(t))
	if _, err := co.Refresh(t.Context()); err != nil || co.SlackErr() != nil {
		t.Fatalf("sync: %v, Slack: %v", err, co.SlackErr())
	}
	if err := co.DMAuthor(t.Context(), ""); err == nil || err.Error() != "Slack isn't logged in" {
		t.Fatalf("DM while logged out: %v", err)
	}
	if _, _, err := co.SlackActivity(t.Context()); err == nil || err.Error() != "Slack isn't logged in" {
		t.Fatalf("channels while logged out: %v", err)
	}
	sl.mu.Lock()
	sl.authErr = ""
	sl.mu.Unlock()
	co.CheckAuth(t.Context())
	if _, _, err := co.SlackActivity(t.Context()); err != nil {
		t.Fatalf("channels after logging in: %v", err)
	}
}

// With slack_after_review off, a review on a shared PR doesn't mind Slack
// being logged out.
func TestAfterReviewOffLoggedOut(t *testing.T) {
	ts := fmt.Sprintf("%d.000100", time.Now().Unix())
	fake := &ghtest.GitHub{Others: []gh.PR{ghtest.PR("api", 7, "feat: shared", "alice")}, Closed: map[string]bool{}}
	co, _ := newCore(t, fake)
	cfg := config.Default()
	cfg.SlackChannels, cfg.SlackAfterReview = []string{"C1"}, "off"
	co.SetConfig(cfg)
	sl := &fakeSlack{}
	co.UseSlack(sl.client(t))
	sl.set(match(ts, "U2", "bob", "C1", "reviews", "o/api/pull/7"))
	refresh(t, co)
	sl.mu.Lock()
	sl.authErr = "token_revoked"
	sl.mu.Unlock()
	co.CheckAuth(t.Context())

	if sent, err := co.Submit(t.Context(), "o/api#7", core.Draft{Event: "APPROVE"}); err != nil || sent.Slack != "" {
		t.Fatalf("submit: %+v, %v", sent, err)
	}
}

// LoadSlack signs in with the token `prowl slack` saved, so a sign-in from
// Accounts needs no restart.
func TestLoadSlack(t *testing.T) {
	keyring.MockInit()
	t.Setenv("PROWL_SLACK_TOKEN", "")
	co, st := newCore(t, &ghtest.GitHub{})
	if err := co.LoadSlack(); err != nil || co.SlackConnected() {
		t.Fatalf("before signing in: %v, connected %v", err, co.SlackConnected())
	}
	if err := slack.SaveToken(slack.Token{Access: "xoxp-saved"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSlackSignedIn(true); err != nil {
		t.Fatal(err)
	}
	if err := co.LoadSlack(); err != nil || !co.SlackConnected() {
		t.Fatalf("after signing in: %v, connected %v", err, co.SlackConnected())
	}
}
