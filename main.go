package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"golang.org/x/term"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/platform"
	"github.com/tmstorm/prowl/internal/skill"
	"github.com/tmstorm/prowl/internal/store"
	"github.com/tmstorm/prowl/internal/tui"
)

const usage = `prowl — PR review queue

usage:
  prowl            open the TUI
  prowl watch      poll GitHub and send desktop notifications (run as a user service)
  prowl list       print what needs you (for scripts / status bars)
  prowl service install | uninstall
                   run prowl watch at login, in the background: a systemd
                   user service on Linux, a launchd agent on macOS, a
                   startup entry on Windows
  prowl review [--detach] [--agent NAME[:PROFILE]]
                   review the branch checked out here, committed or not, before
                   it has a pull request, and print the findings. Exits 2 when
                   a finding is must-fix (for agent hooks). --detach runs it in
                   the background; it shows under Mine in prowl either way.
  prowl findings [PR] [--all] [--prompt] [--agent NAME[:PROFILE]]
                   print the AI reviews' findings, combined, and each reviewer's
                   note, as markdown, for you or the coding agent in your checkout. PR is a number, owner/repo#N,
                   or omitted for the current branch's PR (its local review
                   when it has none). --prompt wraps them in
                   instructions to verify and fix. --agent limits it to one agent
                   (with its profiles), or to one profile of it.
  prowl skill install [--agent claude|codex] [DIR]
                   install the prowl-findings skill (default: Claude Code's,
                   ~/.claude/skills/prowl-findings), so the agent in your
                   checkout can act on review findings when you ask it to.
  prowl skill print
                   show the skill
  prowl edits [--since 30d] [--repo NAME]
                   show how you edited agent reviews before posting them:
                   each rewrite as draft and posted, what you dropped and
                   added. Feed it to a style guide or a writing skill.
  prowl version    print the version, and the newer release when one is out
  prowl update     install the newest release over this prowl and print
                   what's new. A prowl from a package manager says which
                   command updates it instead.
  prowl uninstall [--purge] [--yes]
                   remove what prowl put on this machine: the background
                   service, its worktrees and refs in your checkouts, the
                   prowl-findings skill and, when prowl installed itself, the
                   binary. --purge also removes the config and data. A
                   package manager's prowl says which command removes it.
  prowl schema     print the JSON Schema a review must match (for
                   [agent] reviewers = ["command"])
`

func main() {
	err := run(os.Args[1:])
	var code exitCode
	switch {
	case errors.As(err, &code):
		os.Exit(int(code))
	case err != nil:
		fmt.Fprintln(os.Stderr, "prowl:", err)
		os.Exit(1)
	}
}

// exitCode ends prowl with that status and no message, e.g. 2 for a review
// with must-fix findings.
type exitCode int

func (c exitCode) Error() string { return fmt.Sprintf("exit status %d", int(c)) }

func run(args []string) error {
	if runtime.GOOS == "windows" {
		_ = os.Remove(core.OldBinary(core.Executable()))
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	st, err := store.Open(config.DataDir())
	if err != nil {
		return err
	}
	defer st.Close()

	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "agent-run":
		if len(args) < 2 {
			return fmt.Errorf("agent-run needs a job")
		}
		job, err := agent.ReadJob(args[1])
		if err != nil {
			return err
		}
		c, _ := gh.New()
		return agent.Run(cfg, st, c, job)
	case "version", "--version":
		fmt.Println("prowl", core.BuildVersion())
		if c, err := gh.New(); err == nil {
			co := core.New(cfg, c, st)
			_ = co.CheckRelease(false)
			if r := co.Release(core.Executable()); r.Newer() && !r.Installed {
				fmt.Printf("prowl %s is out: %s\n", r.Latest, cmp.Or(r.Command, "prowl update"))
			}
		}
		return nil
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "findings":
		return findings(cfg, st, args[1:])
	case "service":
		return service(args[1:])
	case "uninstall":
		return uninstall(cfg, st, args[1:])
	case "edits":
		return edits(cfg, st, args[1:])
	case "schema":
		fmt.Println(agent.Schema(cfg.Agent.Severities()))
		return nil
	case "skill":
		sub := ""
		if len(args) > 1 {
			sub = args[1]
		}
		switch sub {
		case "install":
			who, dir := "claude", ""
			for i := 2; i < len(args); i++ {
				switch {
				case args[i] == "--agent" && i+1 < len(args):
					who = args[i+1]
					i++
				case strings.HasPrefix(args[i], "--agent="):
					who = strings.TrimPrefix(args[i], "--agent=")
				default:
					dir = args[i]
				}
			}
			if who != "claude" && who != "codex" {
				return fmt.Errorf("--agent must be claude or codex")
			}
			if dir == "" {
				dir = skill.Dir(who)
			}
			path, err := skill.Install(dir)
			if err != nil {
				return err
			}
			fmt.Println("installed", path)
			fmt.Println(`In a checkout, ask your agent something like "fix the prowl review findings".`)
			return nil
		case "print":
			_, err := os.Stdout.Write(skill.Content)
			return err
		}
		return fmt.Errorf("usage: prowl skill install [--agent claude|codex] [DIR] | prowl skill print")
	}

	c, err := gh.New()
	if err != nil && cmd == "" && isTerminal() {
		c, err = loginGitHub(err)
	}
	if err != nil {
		return noGitHub()
	}
	switch cmd {
	case "review":
		return review(cfg, c, st, args[1:])
	case "update":
		return update(core.New(cfg, c, st))
	case "":
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		co := core.New(cfg, c, st)
		go co.Run(ctx)
		final, err := tea.NewProgram(tui.New(co), progOpts(cfg)...).Run()
		if m, ok := final.(*tui.Model); ok && err == nil && m.Restart() {
			stop()
			return platform.Reopen(core.Executable())
		}
		return err
	case "watch":
		return watch(cfg, c, st)
	case "list":
		co := core.New(cfg, c, st)
		co.Quiet()
		if _, err := co.Refresh(); err != nil {
			return err
		}
		in := co.Inbox()
		for _, it := range in.Items {
			if !it.Bucket.NeedsMe() {
				continue
			}
			if _, ok := in.Snoozed[it.PR.Key()]; ok {
				continue
			}
			fmt.Printf("%-14s %-28s %s (%s) — %s\n", it.Bucket, it.PR.Key(), it.PR.Title, it.PR.AuthorLogin(), it.Reason)
		}
		return nil
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func watch(cfg config.Config, c *gh.Client, st *store.Store) error {
	interval := cfg.PollInterval.Duration
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	co := core.New(cfg, c, st)
	if err := co.Watching(); errors.Is(err, core.ErrWatching) {
		fmt.Println(err)
		return nil
	} else if err != nil {
		return err
	}
	exe := core.Executable()
	built, _ := os.Stat(exe)
	var authAt time.Time
	reminded := ""
	for {
		if updated(exe, built) {
			fmt.Println(time.Now().Format(time.TimeOnly), "prowl was updated; restarting")
			co.StopWatching()
			if err := platform.Restart(exe); err != nil {
				fmt.Fprintln(os.Stderr, "restart failed:", err)
				_ = co.Watching()
			} else {
				return nil
			}
		}
		if time.Since(authAt) > time.Hour {
			authAt = time.Now()
			co.CheckAuth()
			reminded = remindLogin(co, reminded)
		}
		if v, err := co.AutoUpgrade(exe); err != nil {
			fmt.Fprintln(os.Stderr, time.Now().Format(time.TimeOnly), "update failed:", err)
		} else if v != "" {
			fmt.Println(time.Now().Format(time.TimeOnly), "installed prowl", v)
		}
		changes, err := co.Refresh()
		if err != nil {
			fmt.Fprintln(os.Stderr, time.Now().Format(time.TimeOnly), "refresh failed:", err)
		} else {
			core.Notify(changes, cfg.NotifyTimeout.Duration)
			fmt.Printf("%s synced %d PRs, %d changes\n", time.Now().Format(time.TimeOnly), len(co.Inbox().Items), len(changes))
		}
		time.Sleep(interval)
	}
}

// updated reports whether the executable at exe has been replaced since
// built was taken, and has stayed put for a few seconds so it isn't still
// being written.
func updated(exe string, built os.FileInfo) bool {
	now, err := os.Stat(exe)
	if err != nil || built == nil {
		return false
	}
	return (now.ModTime() != built.ModTime() || now.Size() != built.Size()) && time.Since(now.ModTime()) > 10*time.Second
}

// remindLogin sends a desktop notification when the review agent is logged
// out or its login runs out within a day. last is the state notified before;
// the new one is returned.
func remindLogin(co *core.Core, last string) string {
	cfg := co.Config()
	var states, msgs []string
	for _, acct := range co.Accounts() {
		if !slices.Contains(cfg.Agent.Reviewers, acct.Key) || acct.Missing != nil {
			continue
		}
		switch a := acct.Auth; {
		case a.Known && !a.LoggedIn:
			states = append(states, acct.Key+":out")
			msgs = append(msgs, acct.Title+" is logged out.")
		case a.ExpiresSoon(24 * time.Hour):
			states = append(states, acct.Key+":"+a.Expires.String())
			msgs = append(msgs, acct.Title+"'s login runs out "+a.Expires.Local().Format("Mon 15:04")+".")
		}
	}
	state := strings.Join(states, ",")
	if state != "" && state != last {
		platform.Notify("prowl", strings.Join(msgs, " ")+" Press "+cfg.LeaderLabel()+"l in prowl to log in.", cfg.NotifyTimeout.Duration)
	}
	return state
}

func progOpts(cfg config.Config) []tea.ProgramOption {
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if cfg.Mouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	return opts
}

// edits prints how you edited agent drafts before posting them.
func edits(cfg config.Config, st *store.Store, args []string) error {
	since, repo := 30*24*time.Hour, ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--since" && i+1 < len(args):
			d, err := parseAge(args[i+1])
			if err != nil {
				return err
			}
			since = d
			i++
		case args[i] == "--repo" && i+1 < len(args):
			repo = args[i+1]
			i++
		default:
			return fmt.Errorf("usage: prowl edits [--since 30d] [--repo NAME]")
		}
	}
	posts, err := st.Posts(time.Now().Add(-since))
	if err != nil {
		return err
	}
	if repo != "" {
		posts = slices.DeleteFunc(posts, func(p store.Post) bool {
			r, _, _ := strings.Cut(p.Key, "#")
			return r != repo && !strings.HasSuffix(r, "/"+repo)
		})
	}
	if len(posts) == 0 {
		return fmt.Errorf("no posted AI reviews in that time; post one with %sp in prowl", cfg.LeaderLabel())
	}
	fmt.Print(agent.EditsMarkdown(posts))
	return nil
}

// parseAge reads a duration that may also be in days ("30d").
func parseAge(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("--since %q: want e.g. 30d or 12h", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("--since %q: want e.g. 30d or 12h", s)
	}
	return d, nil
}

// findings prints a stored agent review for a PR. It works offline except
// when the PR has to be worked out from the current directory.
func findings(cfg config.Config, st *store.Store, args []string) error {
	all, asPrompt := false, false
	target, only := "", ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--all":
			all = true
		case a == "--prompt":
			asPrompt = true
		case a == "--agent" && i+1 < len(args):
			only = args[i+1]
			i++
		case strings.HasPrefix(a, "--agent="):
			only = strings.TrimPrefix(a, "--agent=")
		case a == "-h" || a == "--help":
			fmt.Print(usage)
			return nil
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("prowl findings: unknown argument %q", a)
		default:
			target = a
		}
	}
	key, head, err := resolvePR(target)
	if target == "" {
		if local, lerr := core.LocalKey("."); lerr == nil {
			if runs, _ := st.Runs(key); err != nil || len(runs) == 0 {
				key, head, err = local, "", nil
			}
		}
	}
	if err != nil {
		return err
	}
	runs, err := st.Runs(key)
	if err != nil {
		return err
	}
	var cur []*store.AgentReview
	for _, r := range store.Current(runs) {
		if only != "" && r.Agent != only && r.Reviewer() != only {
			continue
		}
		if r.Status == "running" {
			fmt.Fprintf(os.Stderr, "the %s review of %s is still running; leaving it out\n", agent.Title(r.Agent), key)
			continue
		}
		cur = append(cur, r)
	}
	if len(cur) == 0 {
		if strings.HasPrefix(key, "local:") {
			return fmt.Errorf("no finished review of %s yet; run prowl review", strings.TrimPrefix(key, "local:"))
		}
		return fmt.Errorf("no finished review for %s yet; open it in prowl and press %scc", key, cfg.LeaderLabel())
	}
	notes := map[string][]store.LogEntry{}
	for _, r := range cur {
		notes[r.Reviewer()], _ = st.ReviewLog(key, r.Reviewer())
	}
	md := agent.FindingsMarkdown(cur, notes, key, head, all)
	if asPrompt {
		md = agent.HandoffPrompt(md)
	}
	if asPrompt {
		fmt.Print(md)
		return nil
	}
	printMarkdown(md)
	return nil
}

// printMarkdown prints md, rendered when stdout is a terminal.
func printMarkdown(md string) {
	if fd := int(os.Stdout.Fd()); term.IsTerminal(fd) {
		w, _, _ := term.GetSize(fd)
		if r, err := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(min(max(w-4, 40), 100))); err == nil {
			if out, err := r.Render(md); err == nil {
				md = out
			}
		}
	}
	fmt.Print(md)
}

// update installs the newest release over this prowl, or says which command
// does when a package manager owns it.
func update(co *core.Core) error {
	exe := core.Executable()
	if err := co.CheckRelease(true); err != nil {
		return err
	}
	r := co.Release(exe)
	switch {
	case r.Running == "":
		return fmt.Errorf("this prowl (%s) was built from source; update it with git pull and go build", core.BuildVersion())
	case r.Installed:
		fmt.Printf("prowl %s is installed; restart prowl to run it\n", r.Latest)
		return nil
	case !r.Newer():
		fmt.Printf("prowl %s is the newest release\n", r.Running)
		return nil
	case r.Command != "":
		fmt.Printf("prowl %s is out; this prowl is updated with: %s\n", r.Latest, r.Command)
		return nil
	}
	stop := showProgress(co, time.Now(), func(time.Time) string { return co.Updating() })
	_, err := co.Upgrade(exe)
	stop()
	if err != nil {
		return err
	}
	r = co.Release(exe)
	w := 100
	if tw, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		w = min(tw, w)
	}
	fmt.Println(tui.Installed(co.Config(), r, exe, co.WatchRunning(), w))
	return nil
}

// resolvePR turns "", "123" or "owner/repo#123" into a PR key, using gh in the
// current directory to fill in what's missing. It also returns the PR's
// current head commit when gh was consulted.
func resolvePR(target string) (key, head string, err error) {
	if strings.HasPrefix(target, "local:") || strings.Contains(target, "#") && strings.Contains(target, "/") {
		return target, "", nil
	}
	ghArgs := []string{"pr", "view", "--json", "number,url,headRefOid"}
	if target != "" {
		ghArgs = []string{"pr", "view", target, "--json", "number,url,headRefOid"}
	}
	out, err := exec.Command("gh", ghArgs...).Output()
	if err != nil {
		return "", "", fmt.Errorf("couldn't find a PR for %q here (gh pr view failed); pass owner/repo#N", target)
	}
	var pr struct {
		Number     int    `json:"number"`
		URL        string `json:"url"`
		HeadRefOid string `json:"headRefOid"`
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return "", "", err
	}

	parts := strings.Split(strings.TrimPrefix(pr.URL, "https://github.com/"), "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("unexpected PR URL %s", pr.URL)
	}
	return fmt.Sprintf("%s/%s#%d", parts[0], parts[1], pr.Number), pr.HeadRefOid, nil
}

func isTerminal() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// loginGitHub offers `gh auth login` when prowl has no GitHub access.
func loginGitHub(cause error) (*gh.Client, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, cause
	}
	fmt.Println("prowl reads GitHub through the GitHub CLI, which isn't logged in.")
	fmt.Print("Log in now with `gh auth login`? [Y/n] ")
	var answer string
	_, _ = fmt.Scanln(&answer)
	if a := strings.ToLower(strings.TrimSpace(answer)); a != "" && a != "y" && a != "yes" {
		return nil, cause
	}
	login := exec.Command("gh", "auth", "login")
	login.Stdin, login.Stdout, login.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := login.Run(); err != nil {
		return nil, fmt.Errorf("gh auth login: %w", err)
	}
	return gh.New()
}

// noGitHub says what prowl needs to reach GitHub: the GitHub CLI installed,
// or logged in.
func noGitHub() error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("the GitHub CLI isn't installed: get it from https://cli.github.com, then run `gh auth login`")
	}
	return fmt.Errorf("the GitHub CLI isn't logged in: run `gh auth login`")
}

// review runs a local review of the branch checked out in the current
// directory and prints its findings.
func review(cfg config.Config, c *gh.Client, st *store.Store, args []string) error {
	detach, reviewer := false, ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--detach":
			detach = true
		case a == "--agent" && i+1 < len(args):
			reviewer = args[i+1]
			i++
		case strings.HasPrefix(a, "--agent="):
			reviewer = strings.TrimPrefix(a, "--agent=")
		default:
			return fmt.Errorf("prowl review: unknown argument %q", a)
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	co := core.New(cfg, c, st)
	if detach {
		key, err := co.ReviewLocal(dir, reviewer, false)
		if errors.Is(err, core.ErrNothingToReview) {
			fmt.Fprintln(os.Stderr, err)
			return nil
		}
		if err == nil {
			fmt.Fprintf(os.Stderr, "reviewing %s in the background; it shows under Mine in prowl\n", strings.TrimPrefix(key, "local:"))
		}
		return err
	}
	key, err := core.LocalKey(dir)
	if err != nil {
		return err
	}
	start := time.Now()
	stop := showProgress(co, start, func(now time.Time) string {
		runs, _ := st.Runs(key)
		if i := slices.IndexFunc(runs, func(r *store.AgentReview) bool { return r.Status == "running" }); i >= 0 {
			return co.Progress(runs[i], now)
		}
		return ""
	})
	_, err = co.ReviewLocal(dir, reviewer, true)
	stop()
	if errors.Is(err, core.ErrNothingToReview) {
		fmt.Fprintln(os.Stderr, err)
		return nil
	}
	if err != nil {
		return err
	}
	if err := findings(cfg, st, []string{key}); err != nil {
		return err
	}
	total, mustFix := 0, 0
	for _, r := range store.Current(co.Reviews()[key]) {
		total += len(r.Findings)
		mustFix += r.MustFix()
	}
	noun := "findings"
	if total == 1 {
		noun = "finding"
	}
	fmt.Fprintf(os.Stderr, "done in %s · %d %s, %d must-fix\n", time.Since(start).Round(time.Second), total, noun, mustFix)
	if mustFix > 0 {
		return exitCode(2)
	}
	return nil
}

// showProgress keeps one line on stderr saying what prowl is doing, from
// step ("" shows nothing), redrawn until stop is called: led by the blinking
// owl, or a plain spinner with the owl turned off. When stderr isn't a
// terminal (a hook, a log) it prints a line per step instead, with no
// control characters.
func showProgress(co *core.Core, start time.Time, step func(now time.Time) string) (stop func()) {
	fd := int(os.Stderr.Fd())
	live := term.IsTerminal(fd)
	done, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		shown, lastStep := 0, ""
		for frame := 1; ; frame++ {
			select {
			case <-done:
				if shown > 0 {
					fmt.Fprint(os.Stderr, "\r"+strings.Repeat(" ", shown)+"\r")
				}
				return
			case <-tick.C:
			}
			now := time.Now()
			progress := step(now)
			if progress == "" {
				continue
			}
			if !live {
				if step, _, _ := strings.Cut(progress, ": "); step != lastStep {
					lastStep = step
					fmt.Fprintln(os.Stderr, step)
				}
				continue
			}
			mark := `|/-\`[frame%4 : frame%4+1]
			if co.Config().Playful {
				mark = "(o,o)"
				if frame%15 == 0 {
					mark = "(-,-)"
				}
			}
			line := fmt.Sprintf("%s %s · %s", mark, now.Sub(start).Round(time.Second), progress)
			if w, _, err := term.GetSize(fd); err == nil && w > 1 && utf8.RuneCountInString(line) >= w {
				line = string([]rune(line)[:w-2]) + "…"
			}
			n := utf8.RuneCountInString(line)
			fmt.Fprint(os.Stderr, "\r"+line+strings.Repeat(" ", max(shown-n, 0)))
			shown = n
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

// uninstall removes what prowl put on this machine, after asking.
func uninstall(cfg config.Config, st *store.Store, args []string) error {
	purge, yes := false, false
	for _, a := range args {
		switch a {
		case "--purge":
			purge = true
		case "--yes", "-y":
			yes = true
		case "-h", "--help":
			fmt.Print(usage)
			return nil
		default:
			return fmt.Errorf("prowl uninstall: unknown argument %q", a)
		}
	}
	f := core.FindFootprint(cfg, core.Executable(), purge)
	if !yes && !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Print(tui.Uninstalling(cfg, f, false))
		return errors.New("prowl uninstall: run it in a terminal to confirm, or pass --yes")
	}
	fmt.Print(tui.Uninstalling(cfg, f, !yes))
	if !f.Service && len(f.Checkouts)+len(f.Skills)+len(f.Dirs) == 0 && f.Binary == "" {
		return nil
	}
	if !yes {
		var answer string
		_, _ = fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" && answer != "yes" {
			fmt.Println("nothing removed")
			return nil
		}
	}
	_ = st.Close()
	if err := core.Uninstall(f); err != nil {
		return err
	}
	fmt.Println("prowl is uninstalled")
	return nil
}

// service installs or removes `prowl watch` as a login service.
func service(args []string) error {
	if len(args) != 1 || args[0] != "install" && args[0] != "uninstall" {
		return fmt.Errorf("usage: prowl service install | uninstall")
	}
	what, err := core.SetService(args[0] == "install")
	if err != nil {
		return err
	}
	if args[0] == "uninstall" {
		fmt.Println("prowl watch no longer runs at login")
		return nil
	}
	fmt.Println("prowl watch runs at login and is running now:", what)
	return nil
}
