package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/term"

	"github.com/tmstorm/prowl/docs"
	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/platform"
	"github.com/tmstorm/prowl/internal/skill"
	"github.com/tmstorm/prowl/internal/slack"
	"github.com/tmstorm/prowl/internal/store"
	"github.com/tmstorm/prowl/internal/tui"
)

const usage = `prowl — PR review queue

usage:
  prowl                 open the TUI
  prowl watch           send desktop notifications as PRs need you
  prowl service install | uninstall
                        run prowl watch at login
  prowl list            print what needs you
  prowl review [--detach] [--agent NAME[:PROFILE]]
                        review the branch checked out here; exits 2 on a must-fix
  prowl findings [PR] [--all] [--prompt] [--agent NAME[:PROFILE]]
                        print a PR's AI review findings
  prowl skill install [--agent claude|codex] [DIR] | print
                        the prowl-findings skill for your coding agent
  prowl edits [--since 30d] [--repo NAME]
                        how you edited AI reviews before posting them
  prowl slack [--off]   sign in to Slack, or out
  prowl docs [TOPIC | PAGE SECTION]
                        read these docs
  prowl man DIR         write the man pages under DIR
  prowl schema          the JSON Schema a command reviewer's review matches
  prowl version         print the version, and the newer release when one is out
  prowl update          install the newest release
  prowl uninstall [--purge] [--yes]
                        remove what prowl put on this machine

prowl docs cli explains every command.
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
	ctx := context.Background()
	cmd, rest := "", []string(nil)
	if len(args) > 0 {
		cmd, rest = args[0], args[1:]
	}
	switch {
	case cmd == "-h" || cmd == "--help" || cmd == "help":
		fmt.Print(usage)
		return nil
	case slices.Contains(rest, "-h") || slices.Contains(rest, "--help"):
		return commandHelp(cmd)
	case cmd == "docs":
		return showDocs(args[1:])
	case cmd == "man":
		if len(rest) != 1 {
			return errors.New("usage: prowl man DIR")
		}
		return docs.WriteMan(rest[0], core.BuildVersion())
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
		return agent.Run(ctx, cfg, st, c, job)
	case "agent-tools":
		if len(args) < 3 {
			return fmt.Errorf("agent-tools needs a repo and a directory")
		}
		s, err := agent.ToolServer(cfg, args[1], args[2], slices.Contains(args[3:], "--local"), slices.Contains(args[3:], "--chat"))
		if err != nil {
			return err
		}
		return s.Run(ctx, &mcp.StdioTransport{})
	case "version", "--version":
		fmt.Println("prowl", core.BuildVersion())
		if c, err := gh.New(); err == nil {
			co := core.New(cfg, c, st)
			_ = co.CheckRelease(ctx, false)
			if r := co.Release(core.Executable()); r.Newer() && !r.Installed {
				fmt.Println(tui.UpdateNote(cfg, r, fmt.Sprintf("prowl %s is out: %s", r.Latest, cmp.Or(r.Command, "prowl update"))))
			}
		}
		return nil
	case "findings":
		return findings(cfg, st, args[1:])
	case "service":
		return service(core.New(cfg, nil, st), args[1:])
	case "slack":
		return connectSlack(ctx, core.New(cfg, nil, st), args[1:])
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
			fs := flagSet("skill install")
			who := fs.String("agent", "claude", "")
			dirs, err := parseFlags(fs, args[2:])
			if err != nil {
				return err
			}
			if *who != "claude" && *who != "codex" {
				return fmt.Errorf("--agent must be claude or codex")
			}
			dir := skill.Dir(*who)
			if len(dirs) > 0 {
				dir = dirs[0]
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
		return review(ctx, cfg, c, st, args[1:])
	case "update":
		return update(ctx, core.New(cfg, c, st))
	case "":
		ctx, stop := context.WithCancel(ctx)
		defer stop()
		co := withSlack(core.New(cfg, c, st))
		go co.Run(ctx)
		final, err := tea.NewProgram(tui.New(co), progOpts(cfg)...).Run()
		if m, ok := final.(*tui.Model); ok && err == nil && m.Restart() {
			stop()
			return platform.Reopen(core.Executable())
		}
		return err
	case "watch":
		return watch(ctx, cfg, c, st)
	case "list":
		co := withSlack(core.New(cfg, c, st))
		co.Quiet()
		if _, err := co.Refresh(ctx); err != nil {
			return err
		}
		in := co.Inbox()
		for _, it := range in.Items {
			if !it.Bucket.NeedsMe() {
				continue
			}
			if _, ok := in.Snoozed[it.Key]; ok {
				continue
			}
			fmt.Printf("%-14s %-28s %s (%s) — %s\n", it.Bucket, it.Key, it.Title, it.Author, it.Reason)
		}
		return nil
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func watch(ctx context.Context, cfg config.Config, c *gh.Client, st *store.Store) error {
	co := withSlack(core.New(cfg, c, st))
	if err := co.Watching(); errors.Is(err, core.ErrWatching) {
		fmt.Println(err)
		return nil
	} else if err != nil {
		return err
	}
	go co.PruneOrphans()
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
		if time.Since(authAt) > core.AuthEvery {
			authAt = time.Now()
			co.CheckAuth(ctx)
			reminded = remindLogin(co, reminded)
		}
		if v, err := co.AutoUpgrade(ctx, exe); err != nil {
			fmt.Fprintln(os.Stderr, time.Now().Format(time.TimeOnly), "update failed:", err)
		} else if v != "" {
			fmt.Println(time.Now().Format(time.TimeOnly), "installed prowl", v)
		}
		changes, err := co.Refresh(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, time.Now().Format(time.TimeOnly), "refresh failed:", err)
		} else {
			core.Notify(changes, cfg.NotifyTimeout.Duration)
			fmt.Printf("%s synced %d PRs, %d changes\n", time.Now().Format(time.TimeOnly), len(co.Inbox().Items), len(changes))
			if err := co.SlackErr(); err != nil {
				fmt.Fprintln(os.Stderr, time.Now().Format(time.TimeOnly), "slack:", err)
			}
			co.PruneClosed(ctx)
		}
		time.Sleep(cfg.PollEvery())
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

// remindLogin sends a desktop notification when a login needs you (see
// LoginsDue). last is the state notified before; the new one is returned.
func remindLogin(co *core.Core, last string) string {
	var states, msgs []string
	for _, a := range co.LoginsDue() {
		if !a.Auth.LoggedIn {
			states = append(states, a.Key+":out")
			msgs = append(msgs, a.Title+" is logged out.")
			continue
		}
		states = append(states, a.Key+":"+a.Auth.Expires.String())
		msgs = append(msgs, a.Title+"'s login runs out "+a.Auth.Expires.Local().Format("Mon 15:04")+".")
	}
	state := strings.Join(states, ",")
	if state != "" && state != last {
		platform.Notify("prowl", strings.Join(msgs, " ")+" Log in again from prowl's accounts.", co.Config().NotifyTimeout.Duration)
	}
	return state
}

func progOpts(cfg config.Config) []tea.ProgramOption {
	opts := []tea.ProgramOption{tea.WithAltScreen(), tea.WithOutput(platform.Terminal)}
	if cfg.Mouse {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	return opts
}

// edits prints how you edited agent drafts before posting them.
func edits(cfg config.Config, st *store.Store, args []string) error {
	since := 30 * 24 * time.Hour
	fs := flagSet("edits")
	fs.Func("since", "", func(s string) (err error) {
		since, err = parseAge(s)
		return err
	})
	repo := fs.String("repo", "", "")
	if err := parseNoArgs(fs, args); err != nil {
		return err
	}
	posts, err := st.Posts(time.Now().Add(-since))
	if err != nil {
		return err
	}
	if *repo != "" {
		posts = slices.DeleteFunc(posts, func(p store.Post) bool {
			r, _, _ := strings.Cut(p.Key, "#")
			return r != *repo && !strings.HasSuffix(r, "/"+*repo)
		})
	}
	if len(posts) == 0 {
		return errors.New("no posted AI reviews in that time; post one from prowl")
	}
	fmt.Print(agent.EditsMarkdown(posts))
	return nil
}

// parseAge reads a duration that may also be in days ("30d").
func parseAge(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, errors.New("want e.g. 30d or 12h")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, errors.New("want e.g. 30d or 12h")
	}
	return d, nil
}

// findings prints a stored agent review for a PR. It works offline except
// when the PR has to be worked out from the current directory.
func findings(cfg config.Config, st *store.Store, args []string) error {
	fs := flagSet("findings")
	all := fs.Bool("all", false, "")
	asPrompt := fs.Bool("prompt", false, "")
	only := fs.String("agent", "", "")
	targets, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	target := ""
	if len(targets) > 0 {
		target = targets[0]
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
	reviews, notes, err := core.ReviewsOf(st, key)
	if err != nil {
		return err
	}
	var cur []*core.Review
	for _, r := range core.Current(reviews) {
		if *only != "" && r.Agent != *only && r.Reviewer() != *only {
			continue
		}
		if r.Status == core.ReviewRunning {
			fmt.Fprintf(os.Stderr, "the %s review of %s is still running; leaving it out\n", agent.Title(r.Agent), key)
			continue
		}
		cur = append(cur, r)
	}
	if len(cur) == 0 {
		if strings.HasPrefix(key, "local:") {
			return fmt.Errorf("no finished review of %s yet; run prowl review", strings.TrimPrefix(key, "local:"))
		}
		return fmt.Errorf("no finished review for %s yet; open it in prowl and run an AI review", key)
	}
	md := core.FindingsMarkdown(cur, notes, key, head, *all)
	if *asPrompt {
		fmt.Print(core.HandoffPrompt(md))
		return nil
	}
	printMarkdown(md)
	return nil
}

// commandHelp prints a command's section of the prowl command's docs.
func commandHelp(cmd string) error {
	s, err := docs.Section("cli", "prowl "+cmd)
	if err != nil {
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
	printMarkdown(s + "\n")
	return nil
}

// showDocs lists the docs pages, or prints what a topic leads to, or a
// page's section. A topic with no tag lists the sections that mention it.
func showDocs(args []string) error {
	if len(args) == 0 {
		for _, p := range docs.All() {
			fmt.Printf("%-12s %s\n", p.Name, p.Title)
		}
		fmt.Println("\nprowl docs TOPIC prints a page, a section, a key or a setting, e.g. prowl docs stale_after.")
		return nil
	}
	if len(args) > 1 {
		if s, err := docs.Section(args[0], strings.Join(args[1:], " ")); err == nil {
			printMarkdown(s + "\n")
			return nil
		}
	}
	topic := strings.Join(args, " ")
	if md := docs.Topic(topic); md != "" {
		printMarkdown(md + "\n")
		return nil
	}
	var sections []string
	for _, h := range docs.Search(topic) {
		if s := "prowl docs " + h.Page + " " + docs.Anchor(h.Heading); !slices.Contains(sections, s) {
			sections = append(sections, s)
		}
	}
	if len(sections) == 0 {
		return fmt.Errorf("nothing in the docs about %q: `prowl docs` lists the pages", topic)
	}
	fmt.Printf("%q isn't a topic. These sections mention it:\n\n  %s\n", topic, strings.Join(sections, "\n  "))
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
func update(ctx context.Context, co *core.Core) error {
	exe := core.Executable()
	if err := co.CheckRelease(ctx, true); err != nil {
		return err
	}
	r := co.Release(exe)
	switch {
	case r.Running == "":
		return fmt.Errorf("this prowl (%s) was built from source; update it with git pull and go build", core.BuildVersion())
	case r.Installed:
		fmt.Println(tui.UpdateNote(co.Config(), r, fmt.Sprintf("prowl %s is installed; restart prowl to run it", r.Latest)))
		return nil
	case !r.Newer():
		fmt.Println(tui.UpdateNote(co.Config(), core.Release{}, fmt.Sprintf("prowl %s is the newest release", r.Running)))
		return nil
	case r.Command != "":
		fmt.Println(tui.UpdateNote(co.Config(), r, fmt.Sprintf("prowl %s is out; this prowl is updated with: %s", r.Latest, r.Command)))
		return nil
	}
	stop := showProgress(co, time.Now(), func(time.Time) string { return co.Updating() })
	_, err := co.Upgrade(ctx, exe)
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
	return gh.PRKey(parts[0]+"/"+parts[1], pr.Number), pr.HeadRefOid, nil
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
func review(ctx context.Context, cfg config.Config, c *gh.Client, st *store.Store, args []string) error {
	fs := flagSet("review")
	detach := fs.Bool("detach", false, "")
	reviewer := fs.String("agent", "", "")
	if err := parseNoArgs(fs, args); err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	co := core.New(cfg, c, st)
	if *detach {
		key, err := co.ReviewLocal(ctx, dir, *reviewer, false)
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
		if i := slices.IndexFunc(runs, func(r *store.Run) bool { return r.Status == string(core.ReviewRunning) }); i >= 0 {
			return tui.Progress(&core.Review{Activity: runs[i].Activity, ActivityAt: runs[i].ActivityAt}, now, cfg.Playful)
		}
		return ""
	})
	_, err = co.ReviewLocal(ctx, dir, *reviewer, true)
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
	for _, r := range core.Current(co.Runs()[key]) {
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
	var purge, yes bool
	fs := flagSet("uninstall")
	fs.BoolVar(&purge, "purge", false, "")
	fs.BoolVar(&yes, "yes", false, "")
	fs.BoolVar(&yes, "y", false, "")
	if err := parseNoArgs(fs, args); err != nil {
		return err
	}
	f := core.FindFootprint(cfg, core.Executable(), purge)
	if !yes && !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Print(tui.Uninstalling(cfg, f, false))
		return errors.New("run prowl uninstall in a terminal to confirm, or pass --yes")
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
	slackToken := st.SlackSignedIn()
	_ = st.Close()
	if err := core.Uninstall(f); err != nil {
		return err
	}
	if purge && slackToken {
		if err := slack.DeleteToken(); err != nil {
			fmt.Fprintln(os.Stderr, "prowl:", err)
		}
	}
	fmt.Println("prowl is uninstalled")
	return nil
}

// withSlack has co read the PR links shared in Slack, when you're signed in.
func withSlack(co *core.Core) *core.Core {
	_ = co.LoadSlack()
	return co
}

// connectSlack signs in to the team's Slack app, or shows how to make one.
func connectSlack(ctx context.Context, co *core.Core, args []string) error {
	fs := flagSet("slack")
	off := fs.Bool("off", false, "")
	if err := parseNoArgs(fs, args); err != nil {
		return err
	}
	if *off {
		if err := co.SignOutSlack(); err != nil {
			return err
		}
		fmt.Println("signed out of Slack")
		return nil
	}
	token := ""
	if co.Config().SlackClientID == "" {
		fmt.Print("prowl needs a Slack app to sign in to. Your team needs only one: if someone\n" +
			"already made it, enter its client ID. Otherwise create it at\n" +
			"https://api.slack.com/apps?new_app=1 (From a manifest) with this manifest,\n" +
			"then enter its client ID (Basic Information, App Credentials):\n\n" + slack.Manifest() + "\n" +
			"Client ID, or a user token (xoxp-...) from an app of your own: ")
		answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return fmt.Errorf("read the client ID: %w", err)
		}
		answer = strings.TrimSpace(answer)
		switch {
		case strings.HasPrefix(answer, "xoxp-"):
			token = answer
		case !config.IsSlackClientID(answer):
			return fmt.Errorf("%q isn't a client ID: it's two numbers joined by a dot, under Basic Information, App Credentials", answer)
		default:
			if _, err := co.UpdateConfig(func(c *config.Config) error { c.SlackClientID = answer; return nil }); err != nil {
				return err
			}
		}
	}
	who, err := co.SignInSlack(ctx, token, func(url string) error {
		fmt.Printf("Signing in through the Slack app %s. If Slack says the app is wrong, change it under Slack, Client ID in prowl's options.\n", co.Config().SlackClientID)
		fmt.Println("Opening Slack in your browser to sign in. If it doesn't open, go to:\n" + url)
		_ = platform.Open(url)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Println("signed in to Slack as", who)
	fmt.Println("Next, in prowl's options, under Slack: pick the channels to watch, and match\nyour teammates to their Slack users under Slack names.")
	return nil
}

// service installs or removes `prowl watch` as a login service.
func service(co *core.Core, args []string) error {
	if len(args) != 1 || args[0] != "install" && args[0] != "uninstall" {
		return fmt.Errorf("usage: prowl service install | uninstall")
	}
	what, err := co.SetService(args[0] == "install")
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

func flagSet(cmd string) *flag.FlagSet {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parseFlags parses args with fs, flags before or after the arguments, and
// returns the arguments. A flag's value follows it, or an =.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return rest, nil
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// parseNoArgs parses the flags of a command that takes no arguments.
func parseNoArgs(fs *flag.FlagSet, args []string) error {
	rest, err := parseFlags(fs, args)
	if err == nil && len(rest) > 0 {
		err = fmt.Errorf("unexpected argument %q", rest[0])
	}
	return err
}
