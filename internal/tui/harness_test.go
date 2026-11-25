package tui_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
	"github.com/tmstorm/prowl/internal/slack"
	"github.com/tmstorm/prowl/internal/store"
	"github.com/tmstorm/prowl/internal/tui"
)

type fakeGitHub = ghtest.GitHub

// askIcons lets the first sync ask about Nerd Font icons; the other tests
// have it answered already.
var askIcons = false

func pr(repo string, n int, title, author string) gh.PR { return ghtest.PR(repo, n, title, author) }
func requested(p gh.PR, who string) gh.PR               { return ghtest.Requested(p, who) }

// app drives a real tui.Model against a fake GitHub and a temporary store.
type app struct {
	t       *testing.T
	m       tea.Model
	gh      *fakeGitHub
	st      *store.Store
	slack   *slack.Client
	cfg     config.Config
	clip    string // the file the fake wl-copy writes
	w, h    int
	queue   []tea.Cmd
	late    chan tea.Msg // messages from commands, delivered in the next round
	title   string       // the terminal tab title prowl last set
	mouseOn int          // how often prowl turned mouse reporting on
}

func newApp(t *testing.T, fake *fakeGitHub, configure ...func(*config.Config)) *app {
	t.Helper()
	return newSlackApp(t, fake, nil, configure...)
}

// newSlackApp is newApp signed in to Slack through sl.
func newSlackApp(t *testing.T, fake *fakeGitHub, sl *slack.Client, configure ...func(*config.Config)) *app {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	bin := t.TempDir() // no real gh, claude or codex: only the fakes
	for name, script := range fake.Bins {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	clip := filepath.Join(t.TempDir(), "clipboard")
	if err := os.WriteFile(filepath.Join(bin, "wl-copy"), []byte("#!/bin/sh\nexec /bin/cat > "+clip+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WAYLAND_DISPLAY", "test")
	t.Setenv("PATH", bin)
	c, err := fake.Client()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if !askIcons {
		st.FirstTime("nerd font")
	}
	cfg := config.Default()
	for _, f := range configure {
		f(&cfg)
	}
	a := &app{t: t, gh: fake, st: st, slack: sl, cfg: cfg, clip: clip, w: 160, h: 44, late: make(chan tea.Msg, 1024)}
	a.m = tui.New(start(t, cfg, c, st, sl))
	a.send(tea.WindowSizeMsg{Width: a.w, Height: a.h})
	a.queue = append(a.queue, a.m.Init())
	a.settle()
	return a
}

// start runs a core for one TUI until the test ends.
func start(t *testing.T, cfg config.Config, c *gh.Client, st *store.Store, sl *slack.Client) *core.Core {
	co := core.New(cfg, c, st)
	if sl != nil {
		co.UseSlack(sl)
	}
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	co.Run(ctx)
	return co
}

// send delivers a message and runs what it starts.
func (a *app) send(msg tea.Msg) {
	a.t.Helper()
	var cmd tea.Cmd
	a.m, cmd = a.m.Update(msg)
	a.queue = append(a.queue, cmd)
	a.settle()
}

// settle runs queued commands, feeding their messages back. Each round runs
// the queue at once and waits briefly; a command still running after that
// (a timer, or a chat waiting for output) keeps going, and its message is
// delivered in a later round, as Bubble Tea would.
func (a *app) settle() {
	for round := 0; round < 60; round++ {
		launched := 0
		for _, cmd := range a.queue {
			if cmd != nil {
				launched++
				go func() { a.late <- cmd() }()
			}
		}
		a.queue = nil
		if launched == 0 {
			select {
			case msg := <-a.late:
				a.deliver(msg)
				continue
			default:
				return
			}
		}
		deadline := time.After(150 * time.Millisecond)
	collect:
		for got := 0; got < launched; got++ {
			select {
			case msg := <-a.late:
				a.deliver(msg)
			case <-deadline:
				break collect
			}
		}
	}
}

func (a *app) deliver(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		a.queue = append(a.queue, msg...)
	case tea.QuitMsg:
	default:
		if fmt.Sprintf("%T", msg) == "tea.enableMouseCellMotionMsg" {
			a.mouseOn++
		}
		if fmt.Sprintf("%T", msg) == "tea.setWindowTitleMsg" {
			a.title = fmt.Sprint(msg)
		}
		var next tea.Cmd
		a.m, next = a.m.Update(msg)
		a.queue = append(a.queue, next)
	}
}

var keyNames = map[string]tea.KeyType{
	"enter": tea.KeyEnter, "esc": tea.KeyEsc, "tab": tea.KeyTab, "space": tea.KeySpace, "ctrl+p": tea.KeyCtrlP,
	"ctrl+c": tea.KeyCtrlC, "ctrl+s": tea.KeyCtrlS, "left": tea.KeyLeft, "right": tea.KeyRight, "up": tea.KeyUp, "down": tea.KeyDown, "backspace": tea.KeyBackspace,
}

// keys presses keys: names like "enter" or "ctrl+p", else each rune.
func (a *app) keys(ks ...string) {
	a.t.Helper()
	for _, k := range ks {
		if t, ok := keyNames[k]; ok {
			a.send(tea.KeyMsg{Type: t})
			continue
		}
		for _, r := range k {
			a.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}
}

// paste delivers text the way a terminal paste does.
func (a *app) paste(s string) {
	a.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true})
}

func (a *app) screen() string { return ansi.Strip(a.m.View()) }

// lines is the screen as lines, the left pane only when left is set.
func (a *app) lines() []string { return strings.Split(a.screen(), "\n") }

func (a *app) listW() int { return min(max(a.w*42/100, 44), 92) }

// selected is the inbox row under the cursor.
func (a *app) selected() string {
	for _, l := range a.lines() {
		if left := ansi.Cut(l, 0, a.listW()); strings.Contains(left, "▌") {
			return left
		}
	}
	return ""
}

// cursor is the PR pane's line under the cursor.
func (a *app) cursor() string {
	for _, l := range a.lines() {
		if right := ansi.Cut(l, a.listW(), a.w); strings.Contains(right, "▌") {
			return right
		}
	}
	return ""
}

// list is the inbox pane's text.
func (a *app) list() string {
	var out []string
	for _, l := range a.lines() {
		out = append(out, ansi.Cut(l, 0, a.listW()))
	}
	return strings.Join(out, "\n")
}

// before checks that each text appears on screen above the next.
func (a *app) before(texts ...string) {
	a.t.Helper()
	s, at := a.screen(), -1
	for _, text := range texts {
		i := strings.Index(s, text)
		if i < 0 || i < at {
			a.t.Fatalf("want %q in this order:\n%s", texts, s)
		}
		at = i
	}
}

// search moves the focused pane's cursor to text, vim style.
func (a *app) search(text string) {
	a.keys("/")
	a.paste(text)
	a.keys("enter")
}

func (a *app) has(text string) bool { return strings.Contains(a.screen(), text) }

func (a *app) want(texts ...string) {
	a.t.Helper()
	s := a.screen()
	for _, text := range texts {
		if !strings.Contains(s, text) {
			a.t.Fatalf("%q is not on screen:\n%s", text, s)
		}
	}
}

func (a *app) wantNot(texts ...string) {
	a.t.Helper()
	s := a.screen()
	for _, text := range texts {
		if strings.Contains(s, text) {
			a.t.Fatalf("%q should not be on screen:\n%s", text, s)
		}
	}
}

// click clicks the mouse on the first cell of text (the last match when
// last is set, e.g. to reach a popup over the inbox).
func (a *app) click(text string) {
	a.t.Helper()
	x, y := a.find(text)
	a.send(mouseAt(x, y))
	a.send(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
}

// find is the screen cell where the lowest match of text starts.
func (a *app) find(text string) (x, y int) {
	a.t.Helper()
	ls := a.lines()
	for y := len(ls) - 1; y >= 0; y-- {
		if i := strings.Index(ls[y], text); i >= 0 {
			return lipgloss.Width(ls[y][:i]), y
		}
	}
	a.t.Fatalf("%q is not on screen:\n%s", text, a.screen())
	return 0, 0
}

// drag holds the left button down on from, moves to to and lets go.
func (a *app) drag(from, to string) {
	a.t.Helper()
	x, y := a.find(from)
	a.send(mouseAt(x, y))
	x, y = a.find(to)
	a.send(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	a.send(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
}

// copied is what prowl last put on the clipboard.
func (a *app) copied() string {
	b, _ := os.ReadFile(a.clip)
	return string(b)
}

func (a *app) resize(w, h int) {
	a.w, a.h = w, h
	a.send(tea.WindowSizeMsg{Width: w, Height: h})
}

// refresh syncs again (<space>r), as the poll would.
func (a *app) refresh() { a.keys("space", "r") }

func (a *app) wrote(prefix string) bool {
	return a.gh.Wrote(prefix)
}

// reopen starts prowl again on the same GitHub and data, as after a restart.
func reopen(a *app) *app {
	a.t.Helper()
	c, err := a.gh.Client()
	if err != nil {
		a.t.Fatal(err)
	}
	b := &app{t: a.t, gh: a.gh, st: a.st, slack: a.slack, cfg: a.cfg, w: a.w, h: a.h, late: make(chan tea.Msg, 1024)}
	b.m = tui.New(start(a.t, a.cfg, c, a.st, a.slack))
	b.send(tea.WindowSizeMsg{Width: b.w, Height: b.h})
	b.queue = append(b.queue, b.m.Init())
	b.settle()
	return b
}

func mouseAt(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

// waitFor keeps running prowl's background work (a chat's output, say)
// until text shows, for up to two seconds.
func (a *app) waitFor(text string) {
	a.t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); {
		if a.has(text) {
			return
		}
		a.send(nil)
	}
	a.t.Fatalf("%q never showed:\n%s", text, a.screen())
}

// clickFirst is click on the topmost match instead of the lowest.
func (a *app) clickFirst(text string) {
	a.t.Helper()
	for y, l := range a.lines() {
		if i := strings.Index(l, text); i >= 0 {
			x := lipgloss.Width(l[:i])
			a.send(mouseAt(x, y))
			a.send(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
			return
		}
	}
	a.t.Fatalf("%q is not on screen:\n%s", text, a.screen())
}

// fakeSlack is a Slack workspace for the TUI: search finds matches and
// users.list lists users (both as JSON), and every post is recorded.
type fakeSlack struct {
	mu                    sync.Mutex
	matches, users, rooms string // rooms: users.conversations channels
	posted                []string
}

func (f *fakeSlack) client(t *testing.T) *slack.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/auth.test":
			fmt.Fprint(w, `{"ok":true,"user":"sam","team":"Acme","user_id":"U1"}`)
		case "/users.conversations":
			fmt.Fprintf(w, `{"ok":true,"channels":[%s]}`, f.rooms)
		case "/search.messages":
			fmt.Fprintf(w, `{"ok":true,"messages":{"paging":{"pages":1},"matches":[%s]}}`, f.matches)
		case "/users.list":
			fmt.Fprintf(w, `{"ok":true,"members":[%s]}`, f.users)
		case "/conversations.open":
			fmt.Fprint(w, `{"ok":true,"channel":{"id":"D9"}}`)
		default:
			f.posted = append(f.posted, strings.TrimSpace(r.URL.Path+" "+r.Form.Get("channel")+" "+r.Form.Get("thread_ts")+" "+r.Form.Get("text")))
			fmt.Fprint(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(srv.Close)
	return slack.NewWith(slack.Token{Access: "xoxp-test"}, srv.URL+"/")
}

func (f *fakeSlack) posts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.posted)
}
