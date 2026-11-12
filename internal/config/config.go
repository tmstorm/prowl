package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	// GitHub orgs to pull PRs from.
	Orgs []string `toml:"orgs"`
	// Repos (owner/name) to show every open PR of, not only the ones that
	// involve you. Those go in All, so you can drain a repo before a release.
	WatchRepos []string `toml:"watch_repos"`
	// Directories that contain local clones, searched in order for <dir>/<repo>.
	RepoRoots []string `toml:"repo_roots"`
	// How often `prowl watch` (and the TUI) refresh.
	PollInterval Duration `toml:"poll_interval"`
	// Treat a PR as stale after it waits on you this long.
	StaleAfter Duration `toml:"stale_after"`
	// How long desktop notifications stay up.
	NotifyTimeout Duration `toml:"notify_timeout"`

	// UI
	// The key that opens the action menu: space, ",", "\" or ";" (see Leaders).
	Leader   string `toml:"leader"`
	Theme    string `toml:"theme"`
	NerdFont bool   `toml:"nerd_font"`
	Mouse    bool   `toml:"mouse"`
	Editor   string `toml:"editor"` // empty: $VISUAL, $EDITOR, then vi (notepad on Windows)
	// Explain the inbox icons at the bottom of the inbox pane.
	ShowLegend bool `toml:"show_legend"`
	// The owl: asleep on an empty inbox, waking up while prowl syncs.
	Playful bool `toml:"playful"`
	// Stripped from repo names on screen, e.g. "acme-" shows acme-api as api.
	RepoPrefix string `toml:"repo_prefix"`
	// Give each repo its own color in the inbox (default: one color for all).
	ColorRepos bool `toml:"color_repos"`
	// Sort PRs inside each inbox section by Conventional Commit type.
	SortByType bool `toml:"sort_by_type"`
	// Type order for that sort. Empty uses DefaultTypeOrder.
	TypeOrder []string `toml:"type_order"`
	// Color per commit type, added to or replacing the built-in colors.
	TypeColors map[string]string `toml:"type_colors"`
	// A PR pushed to after you approved needs you again (as re-check), for
	// repos where GitHub doesn't dismiss stale approvals.
	RecheckAfterPush bool `toml:"recheck_after_push"`
	// Inbox grouping: "status" (what each PR needs from you) or "repo".
	GroupBy string `toml:"group_by"`
	// How <leader>ci shows the agent chat: "popup" (floating window) or "fullscreen".
	ChatMode string `toml:"chat_mode"`

	// Accounts treated as bots: dimmed and folded in the activity timeline.
	// Logins ending in [bot] always count.
	Bots []string `toml:"bots"`

	// The coding agents that review PRs, and how.
	Agent Agent `toml:"agent"`

	// GitHub login -> name used in Slack lines (e.g. "@alice").
	SlackNames map[string]string `toml:"slack_names"`
}

// Agent configures the coding agents that review PRs and write summaries.
type Agent struct {
	// The agents that review a PR together (claude, codex, command), the
	// main one first. <leader>cc runs all of them.
	Reviewers []string `toml:"reviewers"`
	// Model per agent, e.g. claude = "opus". Missing: the agent's own default.
	Models map[string]string `toml:"models"`
	// For the command agent: a shell command that reads the review prompt on
	// stdin and prints the review JSON on stdout.
	Command string `toml:"command"`
	// Skills the agent must use on every review.
	Skills []string `toml:"skills"`
	// Extra directories the agent may read, e.g. a clone of the org's
	// standards repo that AGENTS.md / CONTRIBUTING.md link to.
	ContextDirs []string `toml:"context_dirs"`
	// Commands a review of your own checkout (prowl review) may run, e.g.
	// "go test". PR reviews never run them.
	LocalCommands []string `toml:"local_commands"`
	// How to review: what to look for and how to write it. Empty uses
	// DefaultReviewInstructions.
	ReviewInstructions string `toml:"review_instructions"`
	// Report should-fix findings and nits. Must-fix findings always count.
	ShouldFix bool `toml:"should_fix"`
	Nits      bool `toml:"nits"`
	// For the command agent: how to log in, and how to check you're logged in
	// (exit 0 = yes). Both optional.
	LoginCommand  string `toml:"login_command"`
	StatusCommand string `toml:"status_command"`
	// Models for AI summaries, per agent; a missing one uses the agent's
	// review model. A small, fast model is plenty.
	SummaryModels map[string]string `toml:"summary_models"`
	// Write an AI summary of each PR as it first needs you.
	AutoSummary bool `toml:"auto_summary"`
	// How many of your recent edits to agent drafts go into the review
	// prompt as examples of how you write. 0 is off.
	EditExamples int `toml:"edit_examples"`
	// Named review profiles: a focus for a review, run with <leader>cp.
	Profiles []Profile `toml:"profiles"`
}

// Profile is a named way to review: extra instructions, and optionally its
// own reviewers and skills. "adversarial" is built in.
type Profile struct {
	Name      string   `toml:"name"`
	Prompt    string   `toml:"prompt"`
	Reviewers []string `toml:"reviewers"`
	Skills    []string `toml:"skills"`
}

// builtinProfiles ship with prowl; a profile in the config with the same
// name replaces one.
var builtinProfiles = []Profile{{
	Name: "adversarial",
	Prompt: "Review this change as an adversary trying to break it. Look for inputs, states and timings the author didn't plan for: " +
		"empty and huge values, concurrent calls, partial failures, retries, malicious input, and what happens when a dependency is down. " +
		"Every finding needs a concrete way to trigger it.",
}}

// AllProfiles are the built-in profiles and yours, yours winning by name.
func (a Agent) AllProfiles() []Profile {
	out := append([]Profile{}, a.Profiles...)
	for _, b := range builtinProfiles {
		if !slices.ContainsFunc(a.Profiles, func(p Profile) bool { return p.Name == b.Name }) {
			out = append(out, b)
		}
	}
	return out
}

// Profile looks up one of your profiles, then the built-in ones.
func (a Agent) Profile(name string) (Profile, bool) {
	for _, p := range a.Profiles {
		if p.Name == name {
			return p, true
		}
	}
	for _, p := range builtinProfiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// Main is the main review agent: the first reviewer.
func (a Agent) Main() string {
	if len(a.Reviewers) == 0 {
		return "claude"
	}
	return a.Reviewers[0]
}

// Model is the configured model for the named agent ("" = its default).
func (a Agent) Model(name string) string { return a.Models[name] }

// SummaryModel is the model for the named agent's PR summaries.
func (a Agent) SummaryModel(name string) string {
	if m := a.SummaryModels[name]; m != "" {
		return m
	}
	return a.Model(name)
}

// SetSummaryModel sets the named agent's summary model ("" = its review model).
func (a *Agent) SetSummaryModel(name, model string) {
	if model == "" {
		delete(a.SummaryModels, name)
		return
	}
	if a.SummaryModels == nil {
		a.SummaryModels = map[string]string{}
	}
	a.SummaryModels[name] = model
}

// BuiltinProfiles are the profiles prowl ships, for showing as examples.
func BuiltinProfiles() []Profile { return append([]Profile{}, builtinProfiles...) }

// SetModel sets the model for the named agent ("" = its default).
func (a *Agent) SetModel(name, model string) {
	if model == "" {
		delete(a.Models, name)
		return
	}
	if a.Models == nil {
		a.Models = map[string]string{}
	}
	a.Models[name] = model
}

// DefaultTypeOrder puts fixes first and chores last. "other" stands for any
// title that isn't a conventional commit and any type not in the list.
var DefaultTypeOrder = []string{"fix", "revert", "feat", "perf", "refactor", "test", "docs", "build", "ci", "style", "other", "chore"}

// Types is the commit type order in effect.
func (c Config) Types() []string {
	if len(c.TypeOrder) == 0 {
		return DefaultTypeOrder
	}
	return c.TypeOrder
}

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func (d Duration) String() string {
	s := d.Duration.String()
	if strings.HasSuffix(s, "m0s") {
		s = s[:len(s)-2]
	}
	if strings.HasSuffix(s, "h0m") {
		s = s[:len(s)-2]
	}
	return s
}

// Default is a configuration that works for anyone with an authenticated gh:
// all your open PRs across GitHub, no Nerd Font assumed.
func Default() Config {
	return Config{
		PollInterval:     Duration{3 * time.Minute},
		StaleAfter:       Duration{24 * time.Hour},
		NotifyTimeout:    Duration{8 * time.Second},
		Leader:           "space",
		Theme:            "dracula",
		Mouse:            true,
		ShowLegend:       true,
		Playful:          true,
		SortByType:       true,
		RecheckAfterPush: true,
		GroupBy:          "status",
		ChatMode:         "popup",
		Bots:             []string{"github-actions"},
		Agent:            Agent{Reviewers: []string{"claude"}, ShouldFix: true, Nits: true},
	}
}

// DefaultReviewInstructions say how prowl reviews unless review_instructions
// replaces them.
const DefaultReviewInstructions = `Review it like a senior engineer. Read surrounding code where needed to judge
correctness. Focus on: bugs, data races, security, error handling, migrations
and backwards compatibility, API contract changes, missing tests for new
behavior, and the repo guidelines. Check that the PR description matches what
the code does; if it doesn't, say so in the summary.

Be concise. Plain words, no filler, no hedging. The summary is two to four
sentences and leads with the verdict's reason. Each finding body is at most
three short sentences saying what is wrong, why it matters, and the fix. No
preamble, no praise, no restating the diff.

Report what matters, not everything you noticed. Nits only when cheap and
clearly worth the author's time, two at most. No style nits a linter would
catch.`

// Leaders are the keys the action menu can be on. Each is free in normal
// mode, so none shadows a motion.
var Leaders = []string{"space", ",", "\\", ";"}

// LeaderKey is the leader as a key press reads it.
func (c Config) LeaderKey() string {
	if c.Leader == "space" || c.Leader == "" {
		return " "
	}
	return c.Leader
}

// LeaderLabel is the leader as prowl shows it in hints: "<space>", or the
// key itself.
func (c Config) LeaderLabel() string {
	if c.Leader == "space" || c.Leader == "" {
		return "<space>"
	}
	return c.Leader
}

// Instructions are the review instructions in use.
func (a Agent) Instructions() string {
	if strings.TrimSpace(a.ReviewInstructions) == "" {
		return DefaultReviewInstructions
	}
	return a.ReviewInstructions
}

// Severities are the finding severities reviews report, most severe first.
func (a Agent) Severities() []string {
	out := []string{"must-fix"}
	if a.ShouldFix {
		out = append(out, "should-fix")
	}
	if a.Nits {
		out = append(out, "nit")
	}
	return out
}

// Dir is where the config lives: $XDG_CONFIG_HOME/prowl, else
// %APPDATA%\prowl on Windows and ~/.config/prowl elsewhere.
func Dir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "prowl")
	}
	if d := os.Getenv("APPDATA"); runtime.GOOS == "windows" && d != "" {
		return filepath.Join(d, "prowl")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "prowl")
}

func Path() string { return filepath.Join(Dir(), "config.toml") }

// DataDir is where state lives: $XDG_DATA_HOME/prowl, else
// %LOCALAPPDATA%\prowl on Windows and ~/.local/share/prowl elsewhere.
func DataDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "prowl")
	}
	if d := os.Getenv("LOCALAPPDATA"); runtime.GOOS == "windows" && d != "" {
		return filepath.Join(d, "prowl")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "prowl")
}

// Load reads the config, writing a commented default file on first run.
// review_instructions are trimmed: a multi-line TOML string keeps the
// newline before its closing quotes.
func Load() (Config, error) {
	cfg := Default()
	if _, err := os.Stat(Path()); errors.Is(err, os.ErrNotExist) {
		return cfg, cfg.Save()
	}
	if _, err := toml.DecodeFile(Path(), &cfg); err != nil {
		return cfg, err
	}

	cfg.Agent.ReviewInstructions = strings.Trim(cfg.Agent.ReviewInstructions, "\n")
	if !slices.Contains(Leaders, cfg.Leader) {
		return cfg, fmt.Errorf(`leader = %q: use "space", ",", "\\" or ";"`, cfg.Leader)
	}
	if len(cfg.Agent.Reviewers) == 0 {
		cfg.Agent.Reviewers = []string{"claude"}
	}
	return cfg, nil
}

// ExpandedRepoRoots returns repo_roots with ~ expanded.
func (c Config) ExpandedRepoRoots() []string { return expand(c.RepoRoots) }

// ExpandedContextDirs returns the agent's context_dirs with ~ expanded.
func (c Config) ExpandedContextDirs() []string { return expand(c.Agent.ContextDirs) }

func expand(dirs []string) []string {
	home, _ := os.UserHomeDir()
	out := make([]string, len(dirs))
	for i, r := range dirs {
		if strings.HasPrefix(r, "~/") {
			r = filepath.Join(home, r[2:])
		}
		out[i] = r
	}
	return out
}

// EditorCmd returns the editor to launch for long-form text.
func (c Config) EditorCmd() string {
	for _, e := range []string{c.Editor, os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if e != "" {
			return e
		}
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "vi"
}

// Update changes settings without clobbering edits made to the file by
// someone else (a hand edit, another prowl): it re-reads the file, applies
// fn, and saves.
func Update(fn func(c *Config) error) (Config, error) {
	c, err := Load()
	if err != nil {
		return c, err
	}
	if err := fn(&c); err != nil {
		return c, err
	}
	return c, c.Save()
}

// ModTime is the config file's modification time (zero if missing).
func ModTime() time.Time {
	st, err := os.Stat(Path())
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// Save writes the config as a commented TOML file. A setting at its default
// is written commented out, so the file lists every option without pinning
// the defaults; a later prowl with better defaults still applies them.
// Comments are regenerated, so hand-written comments are not preserved.
func (c Config) Save() error {
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	d := Default()
	q := func(s string) string { return fmt.Sprintf("%q", s) }
	list := func(xs []string) string {
		parts := make([]string, len(xs))
		for i, x := range xs {
			parts[i] = q(x)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }

	set := func(help, key, val, def string) {
		w("%s", help)
		if val == def {
			w("# %s = %s\n\n", key, def)
		} else {
			w("%s = %s\n\n", key, val)
		}
	}
	str := func(help, key, val, def string) { set(help, key, q(val), q(def)) }
	boolean := func(help, key string, val, def bool) { set(help, key, fmt.Sprint(val), fmt.Sprint(def)) }
	strs := func(help, key string, val, def []string) { set(help, key, list(val), list(def)) }
	dur := func(help, key string, val, def Duration) { set(help, key, q(val.String()), q(def.String())) }

	w("# prowl config. Also editable in the app: <leader>, (options).\n")
	w("# A commented setting is at its default; uncomment it to change it.\n\n")
	strs("# GitHub orgs to pull pull requests from. Empty: your open PRs across all of GitHub.\n", "orgs", c.Orgs, d.Orgs)
	strs("# Repos (owner/name) to show every open PR of, not only the ones that involve\n# you, e.g. to drain a repo before a release. They show in All: unclaimed when\n# nobody was asked to review, watching when someone else has it. <leader>w toggles.\n", "watch_repos", c.WatchRepos, d.WatchRepos)
	strs("# Where your local clones live. Agent reviews reuse <root>/<repo> when it\n# exists, and otherwise clone into prowl's data directory.\n", "repo_roots", c.RepoRoots, d.RepoRoots)
	dur("# How often to check GitHub, in the TUI and in `prowl watch`.\n", "poll_interval", c.PollInterval, d.PollInterval)
	dur("# A PR waiting on you longer than this is flagged.\n", "stale_after", c.StaleAfter, d.StaleAfter)
	dur("# How long desktop notifications stay on screen.\n", "notify_timeout", c.NotifyTimeout, d.NotifyTimeout)

	w("# UI ------------------------------------------------------------------\n\n")
	str("# The leader: the key that opens prowl's action menu, written <leader> in the\n# docs. \"space\", \",\", \"\\\\\" or \";\".\n", "leader", c.Leader, d.Leader)
	str("# dracula, catppuccin, tokyonight, gruvbox, nord, rose-pine, kanagawa\n", "theme", c.Theme, d.Theme)
	boolean("# Powerline separators and icons (needs a Nerd Font).\n", "nerd_font", c.NerdFont, d.NerdFont)
	boolean("", "mouse", c.Mouse, d.Mouse)
	boolean("# Explain the inbox icons at the bottom of the inbox pane.\n", "show_legend", c.ShowLegend, d.ShowLegend)
	boolean("# The owl: asleep on an empty inbox, waking up while prowl syncs.\n", "playful", c.Playful, d.Playful)
	str("# Stripped from repo names on screen to save room, e.g. \"acme-\" shows\n# acme-api as api. Empty shows full names.\n", "repo_prefix", c.RepoPrefix, d.RepoPrefix)
	boolean("# Give every repo its own color in the inbox instead of one shared color.\n", "color_repos", c.ColorRepos, d.ColorRepos)
	boolean("# Sort PRs inside each inbox section by Conventional Commit type (fix first,\n# chore last), then by how long they have waited. Off: by wait time only.\n", "sort_by_type", c.SortByType, d.SortByType)
	strs("# The type order for that sort. \"other\" is any title that isn't a\n# conventional commit, and any type not listed. To add a type, put it in\n# the list where it belongs and give it a color under [type_colors].\n", "type_order", c.Types(), DefaultTypeOrder)
	boolean("# A PR pushed to after you approved needs you again, as re-check. For repos\n# where GitHub keeps an approval through new pushes; off trusts your approval.\n", "recheck_after_push", c.RecheckAfterPush, d.RecheckAfterPush)
	str("# Inbox grouping: status (what each PR needs from you) or repo. <leader>g toggles.\n", "group_by", c.GroupBy, d.GroupBy)
	str("# How <leader>ci shows the agent chat: popup (floating window over prowl)\n# or fullscreen (prowl steps aside until you quit the chat). Windows always\n# uses fullscreen.\n", "chat_mode", c.ChatMode, d.ChatMode)
	str("# Editor for reviews. Empty uses $VISUAL, then $EDITOR, then vi (notepad on Windows).\n", "editor", c.Editor, d.Editor)
	strs("# Accounts shown as bots (dimmed and folded) in a PR's activity. Logins\n# ending in [bot] always count.\n", "bots", c.Bots, d.Bots)

	w("# Agent ----------------------------------------------------------------\n\n")
	w("[agent]\n")
	strs("# The agents that review PRs: claude (Claude Code), codex (Codex CLI), or\n# command (any program; see `command`). <leader>cc runs all of them together and\n# their findings post as one review. The first is the main agent.\n", "reviewers", c.Agent.Reviewers, d.Agent.Reviewers)
	str("# For the command agent: a shell command that reads the review prompt on\n# stdin and prints the review as JSON (`prowl schema` shows the format).\n", "command", c.Agent.Command, d.Agent.Command)
	str("# For the command agent: what <leader>l runs to log in, e.g. \"my-agent login\".\n", "login_command", c.Agent.LoginCommand, d.Agent.LoginCommand)
	str("# For the command agent: checks the login; exit 0 means logged in. It may print\n# {\"loggedIn\": true, \"account\": \"...\", \"expiresAt\": \"<RFC 3339>\"} to say more.\n", "status_command", c.Agent.StatusCommand, d.Agent.StatusCommand)
	strs("# Skills the agent must use on every review. Its other skills stay available.\n", "skills", c.Agent.Skills, d.Agent.Skills)
	strs("# Directories the agent may read besides the PR, e.g. the org standards repo\n# that AGENTS.md and CONTRIBUTING.md link to. GitHub links into these repos\n# are resolved to the local clone.\n", "context_dirs", c.Agent.ContextDirs, d.Agent.ContextDirs)
	strs("# Commands a review of your own checkout (`prowl review`) may run to check the\n# change and its findings, e.g. \"go test\" or \"pnpm test\". Claude Code only. PR\n# reviews never run them: a PR's tests are someone else's code.\n", "local_commands", c.Agent.LocalCommands, d.Agent.LocalCommands)
	boolean("# Report should-fix findings: real problems that don't block the merge.\n# Must-fix findings are always reported.\n", "should_fix", c.Agent.ShouldFix, d.Agent.ShouldFix)
	boolean("# Report nits: minor, optional findings.\n", "nits", c.Agent.Nits, d.Agent.Nits)
	w("# How to review: what to look for and how to write it. Severities and the\n# review format are fixed. Commented out: prowl's built-in instructions, below.\n")
	if instr := strings.Trim(c.Agent.ReviewInstructions, "\n"); strings.TrimSpace(instr) == "" {
		w("# review_instructions = '''\n# %s\n# '''\n\n", strings.ReplaceAll(strings.ReplaceAll(DefaultReviewInstructions, "\n", "\n# "), "# \n", "#\n"))
	} else if strings.Contains(instr, "'''") {
		w("review_instructions = %q\n\n", instr)
	} else {
		w("review_instructions = '''\n%s\n'''\n\n", instr)
	}
	boolean("# Write an AI summary of each PR as it first needs you (<leader>ca writes one\n# on demand either way).\n", "auto_summary", c.Agent.AutoSummary, d.Agent.AutoSummary)
	set("# How many of your recent edits to agent drafts go into the review prompt\n# as examples of how you write (`prowl edits` shows them). 0 is off.\n", "edit_examples", fmt.Sprint(c.Agent.EditExamples), fmt.Sprint(d.Agent.EditExamples))
	w("# Model per agent. Missing: the agent's own default. Claude takes opus,\n# fable, sonnet, haiku or a model ID; Codex takes an OpenAI model name.\n[agent.models]\n")
	mapLines(&b, c.Agent.Models, "# claude = \"opus\"\n")
	w("\n# Model per agent for AI summaries. Missing: the agent's review model. A small,\n# fast model is plenty, e.g. claude = \"haiku\".\n[agent.summary_models]\n")
	mapLines(&b, c.Agent.SummaryModels, "# claude = \"haiku\"\n")
	w("\n# Review profiles, run with <leader>cp: a focus for a review, and optionally\n# its own reviewers and skills. \"adversarial\" is built in; a profile with\n# its name replaces it.\n")
	if len(c.Agent.Profiles) == 0 {
		w("# [[agent.profiles]]\n# name = \"security\"\n# prompt = \"Look only for security problems: injection, auth, secrets, unsafe input.\"\n# reviewers = [\"codex\"]\n")
	}
	for _, p := range c.Agent.Profiles {
		w("[[agent.profiles]]\nname = %q\nprompt = %q\n", p.Name, p.Prompt)
		if len(p.Reviewers) > 0 {
			w("reviewers = %s\n", list(p.Reviewers))
		}
		if len(p.Skills) > 0 {
			w("skills = %s\n", list(p.Skills))
		}
	}
	w("\n# Color per Conventional Commit type, added to or replacing the built-in\n# ones: red, green, orange, pink, yellow, purple, cyan, dim or \"#rrggbb\".\n[type_colors]\n")
	mapLines(&b, c.TypeColors, "# wip = \"orange\"\n")

	w("\n# Slack ----------------------------------------------------------------\n\n")
	w("# GitHub login -> how to refer to them in copied Slack lines.\n[slack_names]\n")
	mapLines(&b, c.SlackNames, "# octocat = \"@Mona\"\n")

	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	return os.Rename(tmp, Path())
}

// mapLines writes a TOML table's entries sorted by key, or example when empty.
func mapLines(b *strings.Builder, m map[string]string, example string) {
	if len(m) == 0 {
		b.WriteString(example)
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%q = %q\n", k, m[k])
	}
}
