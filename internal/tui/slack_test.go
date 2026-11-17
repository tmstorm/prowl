package tui_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/slack"
)

// openNames opens Options → Slack names, the setting before the two
// update settings at the bottom.
func openNames(a *app) {
	a.t.Helper()
	a.keys("space", ",", "G", "k", "k")
	a.want("slack_names")
	a.keys("enter")
}

func slackNamesApp(t *testing.T) (*app, *fakeSlack) {
	sl := &fakeSlack{users: strings.Join([]string{
		`{"id":"U0BEN654321","name":"ben","real_name":"Ben Quill","profile":{"display_name":"Ben Quill"}}`,
		`{"id":"U0LO1234567","name":"lortiz","real_name":"Lena Ortiz","profile":{"display_name":"LO"}}`,
		`{"id":"U0MIRA12345","name":"mira.v","real_name":"Mira Vance","profile":{"display_name":"Mira"}}`,
		`{"id":"U0CARA12345","name":"cara","real_name":"Cara Fox"}`,
		`{"id":"U0ZED123456","name":"zed","real_name":"Zed Zulu"}`,
	}, ",")}
	fake := &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}, Members: []gh.Person{
		{Login: "bquill", Name: "Ben Quill"}, {Login: "lortiz-acme"}, {Login: "MiraJVance"}, {Login: "carafox"},
	}}
	a := newSlackApp(t, fake, sl.client(t), func(c *config.Config) {
		c.Orgs, c.SlackChannels = []string{"o"}, []string{"C1"}
		c.SlackNames = map[string]string{"carafox": "U0CARA12345"}
	})
	return a, sl
}

// The first sync with Slack says how many teammates it matched, once.
func TestSlackNamesTip(t *testing.T) {
	a, _ := slackNamesApp(t)
	a.waitFor("Slack: matched 3 teammates to their Slack users")
	a = reopen(a)
	a.wantNot("Slack: matched")
}

// Slack names lists the proposals, the logins nothing matched and the ones
// mapped already. You accept proposals, pick a Slack user for a login,
// remove one, and save; closing without saving keeps the config as it was
// and goes back to the options.
func TestSlackNamesPopup(t *testing.T) {
	a, _ := slackNamesApp(t)
	openNames(a)
	a.want("Proposed", "bquill", "Ben Quill · same name", "lortiz-acme", "LO (Lena Ortiz) · login matches their Slack name",
		"MiraJVance", "Mira (Mira Vance) · login matches their Slack name", "No match", "alice", "no Slack user · e to pick",
		"Mapped", "carafox", "Cara Fox")

	a.keys("esc")
	a.wantNot("Slack names not saved")
	a.want("Options")
	a.keys("esc")
	openNames(a)
	a.keys("a", "esc")
	a.want("Slack names not saved")
	a.keys("esc")
	if b, _ := os.ReadFile(config.Path()); strings.Contains(string(b), "U0BEN654321") {
		t.Fatal("saved on esc")
	}

	openNames(a)
	a.keys("a", "j", "j", "j", "e")
	a.want("Slack user for alice")
	a.keys("zed", "enter")
	a.want("Zed Zulu · picked")
	a.keys("j", "x")
	a.want("remove: prints carafox")
	a.keys("enter")
	a.want("saved 5 Slack names")
	b, _ := os.ReadFile(config.Path())
	for _, line := range []string{`"bquill" = "U0BEN654321"`, `"lortiz-acme" = "U0LO1234567"`, `"MiraJVance" = "U0MIRA12345"`, `"alice" = "U0ZED123456"`} {
		if !strings.Contains(string(b), line) {
			t.Errorf("config lacks %s:\n%s", line, b)
		}
	}
	if strings.Contains(string(b), "carafox") {
		t.Errorf("carafox wasn't removed:\n%s", b)
	}
}

// A list taller than the popup scrolls only to keep the cursor in view,
// and the popup keeps its size.
func TestSlackNamesScroll(t *testing.T) {
	var members []gh.Person
	for i := range 30 {
		members = append(members, gh.Person{Login: fmt.Sprintf("person%02d", i)})
	}
	sl := &fakeSlack{}
	a := newSlackApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}, Members: members}, sl.client(t),
		func(c *config.Config) { c.Orgs = []string{"o"} })
	a.resize(160, 30)
	openNames(a)
	a.want("No match", "person00")
	help := func() int {
		return slices.IndexFunc(a.lines(), func(l string) bool { return strings.Contains(l, "space accept") })
	}
	before := help()
	a.keys("j", "j", "j")
	a.want("No match", "person00")
	for range 40 {
		a.keys("j")
	}
	a.want("alice")
	a.wantNot("No match", "person00")
	if after := help(); after != before {
		t.Fatalf("the popup changed size: help on line %d, then %d", before, after)
	}
}

// With no channels picked, the first sync points to the picker. It lists
// DMs and your channels, busiest first with their recent PR links of your
// orgs, recommends the busy ones without checking them, and filters as you
// type; enter saves what's checked.
func TestSlackChannels(t *testing.T) {
	ts := fmt.Sprintf("%d.000100", time.Now().Unix())
	var matches []string
	for i := range 6 {
		matches = append(matches, fmt.Sprintf(`{"ts":%q,"user":"U2","username":"bob","text":"<https://github.com/o/api/pull/%d>",
"permalink":"https://x.slack.com/archives/C1/p1","channel":{"id":"C1","name":"reviews"}}`, ts, i))
	}
	matches = append(matches, fmt.Sprintf(`{"ts":%q,"user":"U2","username":"bob","text":"<https://github.com/o/api/pull/9>",
"permalink":"https://x.slack.com/archives/C2/p1","channel":{"id":"C2","name":"random"}}`, ts))
	sl := &fakeSlack{matches: strings.Join(matches, ","),
		rooms: `{"id":"C3","name":"quiet"},{"id":"C2","name":"random"},{"id":"C1","name":"reviews","is_private":true}`}
	a := newSlackApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}}, sl.client(t),
		func(c *config.Config) { c.Orgs = []string{"o"} })
	a.waitFor("Slack: pick the channels whose PR links you want in the inbox")

	a.keys("space", ",", "G", "k", "k", "k", "k", "k", "k", "k", "k")
	a.want("slack_channels", "0 channels + DMs")
	a.keys("enter")
	a.want("Slack channels", "signed in as sam in Acme", "[x] Direct messages",
		"#reviews private", "6 PR links in 14 days · recommended", "#random", "1 PR link in 14 days", "#quiet", "no PR links lately")
	a.before("#reviews", "#random", "#quiet")
	a.wantNot("[x] #reviews")

	a.keys("rev")
	a.wantNot("#random", "#quiet")
	a.keys("down", " ", "up", " ", "enter")
	a.want("watching 1 Slack channel")
	a.wantNot("and your DMs")
	b, _ := os.ReadFile(config.Path())
	if !strings.Contains(string(b), `slack_channels = ["C1"]`) || !strings.Contains(string(b), "slack_dms = false") {
		t.Fatalf("not saved:\n%s", b)
	}
}

// A PR shared in Slack links its reason to the message, and a click on it or
// <leader>O opens the message.
func TestOpenShareInSlack(t *testing.T) {
	opened := filepath.Join(t.TempDir(), "opened")
	t.Setenv("OPENED", opened)
	link := "https://acme.slack.com/archives/C1/p1794869000000100"
	sl := &fakeSlack{matches: fmt.Sprintf(`{"ts":"%d.000100","user":"U2","username":"bob","text":"<https://github.com/o/api/pull/7>",
"permalink":%q,"channel":{"id":"C1","name":"reviews"}}`, time.Now().Unix(), link)}
	opener := `echo "$1" >> "$OPENED"`
	fake := &fakeGitHub{Others: []gh.PR{pr("api", 7, "fix: crash", "alice")}, Bins: map[string]string{"xdg-open": opener, "open": opener}}
	a := newSlackApp(t, fake, sl.client(t), func(c *config.Config) { c.SlackChannels = []string{"C1"} })
	a.refresh()
	a.waitFor("shared: shared in #reviews by bob")
	if !strings.Contains(a.m.View(), ansi.SetHyperlink(link)) {
		t.Fatal("the reason doesn't link to the Slack message")
	}
	wantOpened := func(n int) {
		t.Helper()
		for range 50 {
			if b, _ := os.ReadFile(opened); strings.Count(string(b), link) == n {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		b, _ := os.ReadFile(opened)
		t.Fatalf("opened %q, want the Slack message %d times", b, n)
	}
	a.keys("space", "O")
	wantOpened(1)
	a.clickFirst("shared in #reviews by bob")
	wantOpened(2)
}

// A sync that can't read Slack says why in the status line.
func TestSlackSyncError(t *testing.T) {
	a := newSlackApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}}, slack.NewWith("xoxp-test", "http://127.0.0.1:1/"))
	a.waitFor("slack auth.test")
	a.want("fix: crash")
}
