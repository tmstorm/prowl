package core_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
)

func slackUser(id, handle, real, display, email string) string {
	return fmt.Sprintf(`{"id":%q,"name":%q,"real_name":%q,"profile":{"display_name":%q,"email":%q}}`, id, handle, real, display, email)
}

// Each signal proposes a Slack user, surest first, and none is proposed
// twice. You match from your Slack sign-in. A login mapped to a Slack user
// and bots get none; one mapped to a plain name does, and a login nothing
// matches is listed without one.
func TestProposeNames(t *testing.T) {
	ts := fmt.Sprintf("%d.000100", time.Now().Unix())
	fake := &ghtest.GitHub{
		Members: []gh.Person{
			{Login: "cara-x", Name: "Someone Else", Email: "cara@acme.dev"},
			{Login: "bquill", Name: "Ben Quill"},
			{Login: "imoreno", Name: "Inés Moreno"},
			{Login: "theo-acme"},
			{Login: "XYZ-NoahG"},
			{Login: "MiraJVance"},
			{Login: "oliveracme"},
			{Login: "nobody-here"},
			{Login: "mapped", Name: "Mapped Person"},
			{Login: "named", Name: "Named Person"},
			{Login: "me"},
		},
		Others: []gh.PR{ghtest.PR("api", 7, "fix: chat", "XYZ-NoahG")},
		PRs:    []gh.PR{ghtest.Requested(ghtest.PR("api", 9, "chore: release", "release-bot[bot]"), "me")},
	}
	co, _ := newCore(t, fake)
	cfg := config.Default()
	cfg.Orgs, cfg.SlackChannels = []string{"o"}, []string{"C1"}
	cfg.SlackNames = map[string]string{"mapped": "U0MAPPED12", "named": "@Named"}
	co.SetConfig(cfg)
	sl := &fakeSlack{users: strings.Join([]string{
		slackUser("U1", "sam", "Sam Rivera", "Sam", ""),
		slackUser("U2", "nlind", "Noah Lind", "", ""),
		slackUser("U3", "cara", "Cara Fox", "Cara", "cara@acme.dev"),
		slackUser("U4", "ben", "Ben Quill", "Ben Quill", ""),
		slackUser("U5", "ines", "Ines Moreno", "Ines", ""),
		slackUser("U6", "theo", "Theo Park", "Theo", ""),
		slackUser("U7", "mira.v", "Mira Vance", "Mira", ""),
		slackUser("U10", "oliver", "Oliver Grant", "Ollie", ""),
		slackUser("U8", "mapped", "Mapped Person", "", ""),
		slackUser("U9", "named", "Named Person", "", ""),
	}, ",")}
	co.UseSlack(sl.client(t))
	sl.set(match(ts, "U2", "nlind", "C1", "reviews", "o/api/pull/7"))
	refresh(t, co)

	got, _, err := co.ProposeNames(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, m := range got {
		lines = append(lines, m.Login+" "+m.User.ID+" "+m.Why)
	}
	want := []string{
		"cara-x U3 same email",
		"bquill U4 same name",
		"imoreno U5 same name",
		"theo-acme U6 login matches their Slack name",
		"XYZ-NoahG U2 posts their PRs",
		"MiraJVance U7 login matches their Slack name",
		"oliveracme U10 login matches their Slack name",
		"nobody-here  ",
		"named U9 same name",
		"me U1 you, from your Slack sign-in",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("proposals:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// A post mentions an author mapped to a Slack user and a copy names them by
// display name; <leader>Y DMs them a mention, and an author with no Slack user can't be.
func TestSlackLineAndDM(t *testing.T) {
	fake := &ghtest.GitHub{Others: []gh.PR{ghtest.PR("api", 7, "fix: chat", "ben-gh"), ghtest.PR("api", 8, "fix: more", "nobody")},
		PRs: []gh.PR{ghtest.Requested(ghtest.PR("api", 9, "fix: x", "ben-gh"), "me")}}
	co, _ := newCore(t, fake)
	cfg := config.Default()
	cfg.SlackNames = map[string]string{"ben-gh": "U0BEN123456"}
	co.SetConfig(cfg)
	sl := &fakeSlack{users: slackUser("U0BEN123456", "ben", "Ben Quill", "BQ", "")}
	co.UseSlack(sl.client(t))
	refresh(t, co)

	if got := co.SlackLine("o/api#9", "", true); got != "https://github.com/o/api/pull/9 Needs review · <@U0BEN123456> · fix: x" {
		t.Errorf("posted line = %q", got)
	}
	if got := co.SlackLine("o/api#9", "", false); got != "https://github.com/o/api/pull/9 Needs review · @BQ · fix: x" {
		t.Errorf("copied line = %q", got)
	}
	if got := co.SlackLine("o/api#9", core.EventApprove, false); got != "https://github.com/o/api/pull/9 Approved · @BQ · fix: x" {
		t.Errorf("line after approving = %q", got)
	}
	if err := co.DMAuthor(t.Context(), "o/api#9"); err != nil {
		t.Fatal(err)
	}
	if calls := sl.took(); len(calls) != 1 || calls[0] != "chat.postMessage D9  https://github.com/o/api/pull/9 Needs review · <@U0BEN123456> · fix: x" {
		t.Fatalf("calls = %q", calls)
	}
	if _, err := co.Open(t.Context(), "o/api", 8); err != nil {
		t.Fatal(err)
	}
	if err := co.DMAuthor(t.Context(), "o/api#8"); !errors.Is(err, core.ErrNoSlackUser) {
		t.Fatalf("unmapped author: %v", err)
	}
}
