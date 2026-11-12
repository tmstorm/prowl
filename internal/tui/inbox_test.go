package tui_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/store"
	"github.com/tmstorm/prowl/internal/tui"
)

func needsReview(repo string, n int, title string) gh.PR {
	return requested(pr(repo, n, title, "alice"), "me")
}

// Inside a section fixes come first and chores last, ties in wait order;
// <space>ft narrows the inbox to one type.
func TestSortAndFocusByCommitType(t *testing.T) {
	var prs []gh.PR
	for i, title := range []string{"chore: bump deps", "docs: readme", "feat: new thing", "fix: crash", "Update stuff", "fix: other crash"} {
		prs = append(prs, needsReview("api", i+1, title))
	}
	a := newApp(t, &fakeGitHub{PRs: prs})
	a.before("fix: crash", "fix: other crash", "feat: new thing", "docs: readme", "Update stuff", "chore: bump deps")
	a.keys("space", "f", "t")
	a.paste("fix")
	a.keys("enter")
	a.want("fix: crash", "fix: other crash")
	a.wantNot("feat: new thing", "chore: bump deps")
	a.keys("esc")
	a.want("chore: bump deps")
}

// za folds the section under the cursor down to its header, the cursor
// stays on it through a sync, and enter opens it again.
func TestFoldInboxSection(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one"), needsReview("api", 2, "fix: two")}})
	a.keys("z", "a")
	a.want("▸ NEEDS REVIEW 2")
	a.wantNot("fix: one")
	a.refresh()
	if !strings.Contains(a.selected(), "NEEDS REVIEW") {
		t.Fatalf("a sync moved the cursor off the folded header: %q", a.selected())
	}
	a.keys("enter")
	a.want("▾ NEEDS REVIEW 2", "fix: one", "fix: two")
}

// A sync with fewer PRs (one merged) keeps the PR you're on selected.
func TestSyncWithFewerPRs(t *testing.T) {
	fake := &fakeGitHub{}
	for i := 1; i <= 6; i++ {
		fake.PRs = append(fake.PRs, needsReview("api", i, fmt.Sprintf("fix: number %d", i)))
	}
	a := newApp(t, fake)
	a.keys("G", "k")
	want := a.selected()
	fake.PRs = append(fake.PRs[:1], fake.PRs[3:]...)
	a.refresh()
	if got := a.selected(); got != want {
		t.Fatalf("selection moved from %q to %q", want, got)
	}
	fake.PRs = fake.PRs[:1]
	a.refresh()
	if a.selected() == "" {
		t.Fatal("no selection after the inbox shrank")
	}
}

// When a sync moves the PR you're on out of the tab (you approved it), it
// stays under the cursor until you move off it.
func TestSelectedPRStaysThroughSync(t *testing.T) {
	fake := &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one"), needsReview("api", 2, "fix: two")}}
	a := newApp(t, fake)
	if !strings.Contains(a.selected(), "fix: one") {
		t.Fatalf("setup: %q", a.selected())
	}
	fake.PRs[0].ReviewRequests.Nodes = nil
	fake.PRs[0].Reviews.Nodes = []gh.Review{{Author: &gh.Actor{Login: "me"}, State: "APPROVED", SubmittedAt: time.Now()}}
	a.refresh()
	if !strings.Contains(a.selected(), "fix: one") {
		t.Fatalf("the approved PR left the cursor: %q", a.selected())
	}
	a.keys("k")
	if strings.Contains(a.list(), "fix: one") {
		t.Fatal("once you move off it, the approved PR leaves Needs me")
	}
}

// 0 and 1 pick the pane; 2-9 pick a tab inside the focused pane. Counts read
// focused/total while a repo is in focus.
func TestPanesTabsAndCounts(t *testing.T) {
	mine := pr("web", 9, "feat: mine", "me")
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one"), mine}})
	a.resize(230, 44)
	a.want("[2] Needs me (1)", "[4] Mine (1)")
	a.keys("4")
	a.want("feat: mine")
	a.wantNot("fix: one")
	a.keys("1", "4")
	a.want("No AI review yet")
	a.keys("0", "3")
	a.keys("space", "f", "r")
	a.paste("api")
	a.keys("enter")
	a.want("[3] All (1/2)")
}

// The screen fills the terminal: panes, then the statusline on the last row,
// which a message replaces only while it shows.
func TestFullHeight(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}})
	ls := a.lines()
	if len(ls) != a.h || !strings.Contains(ls[len(ls)-1], "NORMAL") {
		t.Fatalf("%d lines, last %q", len(ls), ls[len(ls)-1])
	}
}

// The statusline owl says how things stand, and a click on it shows what
// needs you. The tab title carries the count.
func TestStatusOwlAndTitle(t *testing.T) {
	old := needsReview("api", 1, "fix: old")
	old.CreatedAt = time.Now().Add(-72 * time.Hour)
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{old, needsReview("api", 2, "fix: new")}})
	last := func() string { ls := a.lines(); return ls[len(ls)-1] }
	if !strings.HasPrefix(last(), " (O,O) ") || a.title != "prowl · 2 need you" {
		t.Fatalf("owl %q, title %q", last(), a.title)
	}
	a.resize(230, 44)
	a.keys("3")
	a.click("(O,O)")
	a.want("[2] Needs me (2)")
	empty := newApp(t, &fakeGitHub{PRs: []gh.PR{pr("web", 9, "feat: mine", "me")}})
	if ls := empty.lines(); !strings.HasPrefix(ls[len(ls)-1], " (-,-) ") || empty.title != "prowl" {
		t.Fatalf("nothing needs you: %q, %q", ls[len(ls)-1], empty.title)
	}
}

// <space>fr lists every repo in your orgs. Focusing one you don't watch
// fetches all its open PRs, with a summary of where they stand; esc stops.
func TestFocusAnyRepo(t *testing.T) {
	other := pr("infra", 5, "chore: release", "bob")
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}, Others: []gh.PR{other},
		Repos: []string{"o/api", "o/infra"}}, func(c *config.Config) { c.Orgs = []string{"o"} })
	a.wantNot("chore: release")
	a.keys("space", "f", "r")
	a.want("infra")
	a.paste("infra")
	a.keys("enter")
	a.want("chore: release", "UNCLAIMED", "infra · 1 open · 1 unclaimed", "<space>w keeps watching it")
	a.keys("esc")
	a.want("fix: one")
}

// A fork and its upstream share a name: the repo picker tells them apart,
// focusing one shows only its PRs, and api#N is ambiguous between them.
func TestForkAndUpstream(t *testing.T) {
	fork := needsReview("api", 2, "fix: in the fork")
	fork.Repository.NameWithOwner = "bob/api"
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: upstream"), fork}})
	a.keys("space", "f", "r")
	a.want("o/api", "bob/api")
	a.paste("bob/api")
	a.keys("enter")
	a.want("fix: in the fork", "api · 1 open")
	a.wantNot("fix: upstream")
	a.keys("space", "w")
	a.want("watching bob/api")
	a.keys("esc", "ctrl+p")
	a.paste("api#7")
	a.wantNot("from GitHub")
}

// ctrl+p searches the inbox; a pasted PR link it can't find is fetched and
// opened. An empty box shows its whole hint.
func TestFinder(t *testing.T) {
	other := pr("infra", 5, "chore: release", "bob")
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}, Others: []gh.PR{other}})
	a.keys("ctrl+p")
	a.want("repo, title, author, number, or paste a PR link")
	a.paste("https://github.com/o/infra/pull/5")
	a.want("open o/infra#5 from GitHub")
	a.keys("enter")
	a.want("infra#5", "chore: release")
}

// An empty inbox has the owl asleep, the little one in a short window, and
// plain text with playful off. The first sync wakes it up.
func TestOwls(t *testing.T) {
	a := newApp(t, &fakeGitHub{})
	a.want("(-)   (-)", "no open pull requests")
	a.resize(160, 18)
	a.want("( -,- )")
	plain := newApp(t, &fakeGitHub{}, func(c *config.Config) { c.Playful = false })
	plain.want("no open pull requests")
	plain.wantNot("(-)   (-)")
	waking := newApp(t, &fakeGitHub{Slow: true})
	waking.want("/\\       /\\", "fetching your pull requests")
}

// The owl is prowl's mascot and its drawings don't change: every eye (asleep,
// stirring, awake, wide awake), every way it looks, both sizes, and its
// colors: a muted body, bold yellow eyes, an orange beak and talons.
func TestOwlDrawing(t *testing.T) {
	big := func(face string) []string {
		return []string{
			"   /\\       /\\",
			"  /  `-----'  \\",
			face,
			"  \\     v     /",
			"   )  ' ' '  (",
			"  (  ' ' ' '  )",
			"   `-._____.-'",
			"  ======^=^======",
		}
	}
	small := func(face string) []string {
		return []string{
			"    ,_____,",
			face,
			"    /)   (\\",
			"   ((  \"  ))",
			"  ===^===^===",
		}
	}
	type pose struct {
		eye  string
		look int
	}
	want := map[pose][2][]string{}
	for _, e := range []string{"-", ".", "o", "O"} {
		want[pose{e, 0}] = [2][]string{big(" |  (" + e + ")   (" + e + ")  |"), small("    ( " + e + "," + e + " )")}
	}
	want[pose{"O", -1}] = [2][]string{big(" | (O)   (O)   |"), small("    (O,O  )")}
	want[pose{"O", 1}] = [2][]string{big(" |   (O)   (O) |"), small("    (  O,O)")}
	for p, w := range want {
		for i, isSmall := range []bool{false, true} {
			got := tui.Owl(p.eye, p.look, isSmall)
			for j := range got {
				got[j] = ansi.Strip(got[j])
			}
			if !slices.Equal(got, w[i]) {
				t.Errorf("owl %q looking %d (small %v):\n%s\nwant:\n%s", p.eye, p.look, isSmall, strings.Join(got, "\n"), strings.Join(w[i], "\n"))
			}
		}
	}

	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })
	art := strings.Join(tui.Owl("O", 0, false), "\n")
	for what, seq := range map[string]string{
		"muted body":       "\x1b[38;2;97;113;163m   /\\",
		"bold yellow eyes": "\x1b[1;38;2;241;250;140mO",
		"orange beak":      "\x1b[38;2;255;184;108mv",
		"orange talons":    "\x1b[38;2;255;184;108m^",
	} {
		if !strings.Contains(art, seq) {
			t.Errorf("the owl lost its %s:\n%q", what, art)
		}
	}
}

// The logo is the same owl in Dracula colors, perched on the accent
// branch. It is pinned: change it only on purpose, and update the sum.
func TestOwlLogo(t *testing.T) {
	b, err := os.ReadFile("../../docs/images/owl.svg")
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != "f2ddeb0d758e81d8f231ba8adacdcd5b2684d577e9e1d4b40a86024e32bb4d95" {
		t.Fatalf("docs/images/owl.svg changed: sha256 %x", sum)
	}
}

// The legend is labelled, and the key help never wraps at any width.
func TestLegendAndHelp(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}})
	a.want("┄┄ legend ┄")
	for _, w := range []int{80, 120, 160} {
		a.resize(w, 50)
		a.keys("space", "?")
		a.want("Keys", "any key to close", "hide it")
		if n := len(a.lines()); n != a.h {
			t.Fatalf("width %d: screen has %d lines, help wrapped", w, n)
		}
		a.keys("esc")
	}
}

// A profile run is its own section beside the plain review, and <space>cp
// lists the profiles, adversarial built in.
func TestReviewProfiles(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}})
	for _, profile := range []string{"", "adversarial"} {
		id, _ := a.st.StartRun("o/api#7", "abc1234", "claude", profile, 0)
		_ = a.st.SetReviewModel(id, "claude-opus-5-5")
		_ = a.st.FinishReview(id, "s", "summary "+profile, "", []store.Finding{{Severity: "must-fix", Path: "a.go", Line: 3, Title: "bad " + profile}})
	}
	a = reopen(a)
	a.keys("enter", "4")
	a.want("▾ Claude · ready · Opus 5.5", "▾ Claude · adversarial · ready", "bad adversarial", "also found by Claude · adversarial")
	a.keys("space", "c", "p")
	a.want("Run a review profile on o/api#7", "as an adversary")
	a.keys("enter")
	a.want("`claude` is not on your PATH")
}

// prowl draws the inbox it saw last time at once, while the first sync runs,
// with the waking owl in the PR pane until you touch something.
func TestStartsFromLastInbox(t *testing.T) {
	fake := &fakeGitHub{PRs: []gh.PR{needsReview("api", 1, "fix: one")}}
	a := newApp(t, fake)
	fake.Slow = true
	a = reopen(a)
	a.want("fix: one", "syncing", "/\\       /\\", "fetching your pull requests")
	a.wantNot("Reviewers")
	if title := a.lines()[0]; !strings.ContainsAny(title, "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏") {
		t.Fatalf("the inbox title should show the sync spinner: %q", title)
	}
	a.keys("j")
	a.waitFor("Reviewers")
	a.wantNot("fetching your pull requests")
}

// A snoozed PR that needs you is still mentioned in the summary line, so it
// isn't forgotten.
func TestSnoozedInSummary(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 855, "chore: promote")}})
	a.keys("space", "s", "1")
	a.wantNot("chore: promote")
	a.want("nothing needs you", "1 snoozed until")
}

// The leader is configurable: with "," it opens the action menu, space
// doesn't, hints show ",", and ",," still opens the options.
func TestLeaderKey(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}}, func(c *config.Config) { c.Leader = "," })
	a.keys("enter", "4")
	a.want(",cc starts one")
	a.wantNot("<space>")
	a.keys("space", "a")
	a.wantNot("approve api#7?")
	a.keys(",", "a")
	a.want("approve api#7?")
	a.keys("esc", ",", ",")
	a.want("Options")
}

// Repo names show in full unless repo_prefix strips one; a name too long
// for the column is cut in the middle so repos sharing a prefix still
// differ.
func TestRepoNames(t *testing.T) {
	prs := []gh.PR{needsReview("acme-api", 1, "fix: one"), needsReview("acme-sdk-unreal-chat-demo", 2, "fix: two")}
	a := newApp(t, &fakeGitHub{PRs: prs})
	a.want("acme-api", "acme-sdk-…-chat-demo")
	trimmed := newApp(t, &fakeGitHub{PRs: prs}, func(c *config.Config) { c.RepoPrefix = "acme-" })
	trimmed.want("api ", "sdk-unreal-chat-demo")
	trimmed.wantNot("acme-")
}

// Someone new to vim keys gets in: the legend lists the basic keys, ? opens
// the help, the arrows move between panes, and the statusline's menu button
// opens the leader menu with a click.
func TestNewcomerKeys(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}})
	a.want("↑↓ move · ←→ panes · enter open · ? keys", "<space> menu")
	a.keys("?")
	a.want("Keys", "any key to close", "suggest a change")
	a.keys("esc")
	a.keys("right", "4")
	a.want("No AI review yet")
	a.keys("left")
	a.clickFirst("<space> menu")
	a.want("approve")
}

// The first launch suggests background notifications, once. The switch in
// the options sets up prowl watch as a login service.
func TestBackgroundNotifications(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the service is a systemd unit on Linux")
	}
	f := &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}, Bins: map[string]string{"systemctl": "exit 0"}}
	a := newApp(t, f)
	a.want("tip: <space>, → Background notifications")
	a = reopen(a)
	a.wantNot("tip: <space>,")
	a.keys("space", ",")
	a.want("Background notifications", "○ off")
	a.clickFirst("Background notifications")
	a.clickFirst("Background notifications")
	a.want("background notifications on: systemd user service")
}

// The first start of a new version offers its notes under prowl's own
// divider, with the legend off too, until <leader>N opens them.
func TestWhatsNewTip(t *testing.T) {
	old := core.Version
	core.Version = "0.2.0"
	t.Cleanup(func() { core.Version = old })
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}}, func(c *config.Config) { c.ShowLegend = false })
	_ = a.st.SetRelease(store.Release{Checked: time.Now(), Tag: "v0.2.0", Notes: "- reviews run faster", URL: "https://github.com/tmstorm/prowl/releases/tag/v0.2.0"})
	a = reopen(a)
	a.want("┄┄ prowl", "you're on prowl v0.2.0 · <space>N what's new")
	a.wantNot("┄┄ legend")
	a.keys("space", "N")
	a.want("What's new in prowl v0.2.0", "reviews run faster")
	a.keys("esc")
	a.wantNot("you're on prowl v0.2.0", "┄┄ prowl")
}

// The options list the update settings under their own heading.
func TestUpdateOptions(t *testing.T) {
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}})
	a.keys("space", ",", "G")
	a.want("Updates", "Check for updates", "Update in the background")
}

// Once prowl has installed a newer release of itself, the legend says so,
// <leader>N shows what's new, each change as a commit title without its
// commit link, and <leader>U quits to start the new prowl. The keys popup
// shows the running version.
func TestUpdateInstalled(t *testing.T) {
	old := core.Version
	core.Version = "0.1.0"
	t.Cleanup(func() { core.Version = old })
	a := newApp(t, &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")}})
	notes := "## [0.2.0](https://github.com/tmstorm/prowl/compare/v0.1.0...v0.2.0) (2026-11-20)\n\n\n### Features\n\n" +
		"* **update:** retry failed installs an hour later ([#9](https://github.com/tmstorm/prowl/issues/9)) ([e2449d5](https://github.com/tmstorm/prowl/commit/e2449d5))\n\n" +
		"### Bug Fixes\n\n* show the reviewer's note ([4f1c2aa](https://github.com/tmstorm/prowl/commit/4f1c2aa))\n"
	_ = a.st.SetRelease(store.Release{Checked: time.Now(), Tag: "v0.2.0", Notes: notes, URL: "https://github.com/tmstorm/prowl/releases/tag/v0.2.0"})
	_ = a.st.SetInstalled("v0.2.0")
	a = reopen(a)
	a.want("prowl v0.2.0 is installed · <space>U restarts into it")
	a.keys("?")
	a.want("prowl 0.1.0 · any key to close")
	a.keys("esc", "space", "N")
	a.want("What's new in prowl v0.2.0", "released 2026-11-20", "feat(update): retry failed installs an hour later #9",
		"fix: show the reviewer's note", "o opens the release on GitHub")
	a.wantNot("e2449d5", "## [0.2.0]")
	a.keys("esc")
	a.want("prowl v0.2.0 is installed · <space>U restarts into it")
	a.keys("space", "U")
	if !a.m.(*tui.Model).Restart() {
		t.Fatal("<leader>U should quit to restart into the installed release")
	}
}

// `prowl update` says where it installed the release and what's new in it,
// including text added to the notes by hand.
func TestInstalled(t *testing.T) {
	notes := "## [0.2.0](https://github.com/tmstorm/prowl/compare/v0.1.0...v0.2.0) (2026-11-20)\n\n" +
		"Run `prowl service install` again after this one.\n\n### Features\n\n* **update:** show the step ([e2449d5](https://github.com/tmstorm/prowl/commit/e2449d5))\n\n" +
		"### Upgrading\n\nNothing else to do.\n"
	out := ansi.Strip(tui.Installed(config.Default(), core.Release{Latest: "v0.2.0", Notes: notes}, "/opt/bin/prowl", true, 80))
	for _, want := range []string{"✓ prowl v0.2.0 is installed at /opt/bin/prowl", "the background service restarts into it",
		"What's new in prowl v0.2.0", "released 2026-11-20", "Run prowl service install again after this one.",
		"feat(update): show the step", "Upgrading", "Nothing else to do."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "e2449d5") {
		t.Errorf("commit link left in:\n%s", out)
	}
	out = ansi.Strip(tui.Installed(config.Default(), core.Release{Latest: "v0.2.0"}, "/opt/bin/prowl", false, 80))
	if strings.Contains(out, "What's new") || strings.Contains(out, "restarts into it") {
		t.Errorf("no notes and no service, but:\n%s", out)
	}
}

// While prowl updates itself the statusline shows the step it is on.
func TestUpdateStep(t *testing.T) {
	old := core.Version
	core.Version = "0.1.0"
	t.Cleanup(func() { core.Version = old })
	fake := &fakeGitHub{PRs: []gh.PR{needsReview("api", 7, "fix: crash")},
		Release: &gh.Release{Tag: "v0.2.0", Assets: []gh.Asset{{ID: 1, Name: "checksums.txt"}, {ID: 2, Name: "checksums.txt.minisig"}}},
		Assets:  map[int64][]byte{1: []byte("not signed"), 2: []byte("no")}}
	reached, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fake.OnAsset = func(int64) {
		once.Do(func() {
			close(reached)
			<-done
		})
	}
	a := newApp(t, fake)
	a.keys("space", "U")
	<-reached
	a.want("verifying prowl v0.2.0")
	close(done)
	a.waitFor("unverified release")
	a.wantNot("verifying prowl v0.2.0")
}
