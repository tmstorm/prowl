package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Orgs          []string `toml:"orgs"`
	RepoRoots     []string `toml:"repo_roots"`
	PollInterval  Duration `toml:"poll_interval"`
	NotifyTimeout Duration `toml:"notify_timeout"`
	CheckUpdates  bool     `toml:"check_updates"`
	AutoUpdate    bool     `toml:"auto_update"`
	Bots          []string `toml:"bots"`

	WatchRepos       []string          `toml:"watch_repos"`
	StaleAfter       Duration          `toml:"stale_after"`
	SortByType       bool              `toml:"sort_by_type"`
	TypeOrder        []string          `toml:"type_order"`
	TypeColors       map[string]string `toml:"type_colors"`
	RecheckAfterPush bool              `toml:"recheck_after_push"`
	GroupBy          string            `toml:"group_by"`

	SlackClientID    string            `toml:"slack_client_id"`
	SlackChannels    []string          `toml:"slack_channels"`
	SlackDMs         bool              `toml:"slack_dms"`
	SlackAfterReview string            `toml:"slack_after_review"`
	SlackReactions   SlackReactions    `toml:"slack_reactions"`
	SlackNames       map[string]string `toml:"slack_names"`

	Agent Agent `toml:"agent"`

	Leader     string `toml:"leader"`
	Mouse      bool   `toml:"mouse"`
	Editor     string `toml:"editor"`
	ChatMode   string `toml:"chat_mode"`
	Theme      string `toml:"theme"`
	NerdFont   bool   `toml:"nerd_font"`
	ShowLegend bool   `toml:"show_legend"`
	Playful    bool   `toml:"playful"`
	RepoPrefix string `toml:"repo_prefix"`
	ColorRepos bool   `toml:"color_repos"`
}

// SlackReactions are the emoji for where your review of a shared PR stands.
type SlackReactions struct {
	Reviewing        string `toml:"reviewing"`
	Approved         string `toml:"approved"`
	ChangesRequested string `toml:"changes_requested"`
	Commented        string `toml:"commented"`
}

// Agent configures the coding agents that review PRs and write summaries.
type Agent struct {
	Reviewers          []string          `toml:"reviewers"`
	Command            string            `toml:"command"`
	LoginCommand       string            `toml:"login_command"`
	StatusCommand      string            `toml:"status_command"`
	Skills             []string          `toml:"skills"`
	ContextDirs        []string          `toml:"context_dirs"`
	ShouldFix          bool              `toml:"should_fix"`
	Nits               bool              `toml:"nits"`
	ReviewInstructions string            `toml:"review_instructions"`
	AutoSummary        bool              `toml:"auto_summary"`
	EditExamples       int               `toml:"edit_examples"`
	Models             map[string]string `toml:"models"`
	SummaryModels      map[string]string `toml:"summary_models"`
	RunImages          map[string]string `toml:"run_images"`
	RunCacheGB         int               `toml:"run_cache_gb"`
	Profiles           []Profile         `toml:"profiles"`
	Extensions         []Extension       `toml:"extensions"`
}

// Extension is a program prowl runs during reviews of the repos it is
// enabled for, or on demand under <leader>e.
type Extension struct {
	Name      string   `toml:"name"`
	Command   string   `toml:"command"`
	Repos     []string `toml:"repos"`
	Timeout   Duration `toml:"timeout"`
	NeedsBase bool     `toml:"needs_base"`
	OnDemand  bool     `toml:"on_demand"`
	Key       string   `toml:"key"`
	Confirm   bool     `toml:"confirm"`
}

// DefaultExtensionTimeout is how long an extension runs without a timeout set.
const DefaultExtensionTimeout = 10 * time.Minute

// Limit is how long the extension may run.
func (e Extension) Limit() time.Duration {
	if e.Timeout.Duration <= 0 {
		return DefaultExtensionTimeout
	}
	return e.Timeout.Duration
}

// For is whether the extension is enabled for repo ("*" is every repo).
func (e Extension) For(repo string) bool {
	return slices.Contains(e.Repos, repo) || slices.Contains(e.Repos, "*")
}

// ExtensionsFor are the extensions enabled for repo: the ones that run on
// every review, or with onDemand the ones under <leader>e.
func (a Agent) ExtensionsFor(repo string, onDemand bool) []Extension {
	var out []Extension
	for _, e := range a.Extensions {
		if e.OnDemand == onDemand && e.For(repo) {
			out = append(out, e)
		}
	}
	return out
}

// extensionListKeys are the letters the list of on-demand extensions keeps
// for itself, so none can be an extension's key.
const extensionListKeys = "jkgGHMLqv"

func (a Agent) checkExtensions() error {
	names := map[string]bool{}
	keys := map[string]string{}
	for _, e := range a.Extensions {
		switch {
		case e.Name == "":
			return fmt.Errorf("[[agent.extensions]]: every extension needs a name")
		case strings.TrimSpace(e.Command) == "":
			return fmt.Errorf("extension %q: set command", e.Name)
		case len(e.Repos) == 0:
			return fmt.Errorf("extension %q: set repos, e.g. [\"acme/web\"] or [\"*\"]", e.Name)
		case e.Key != "" && !e.OnDemand:
			return fmt.Errorf("extension %q: key is only for on_demand extensions", e.Name)
		case e.Key != "" && (len(e.Key) != 1 || !unicode.IsLetter(rune(e.Key[0]))):
			return fmt.Errorf("extension %q: key = %q: use one letter", e.Name, e.Key)
		case e.Key != "" && strings.Contains(extensionListKeys, e.Key):
			return fmt.Errorf("extension %q: key = %q: the list of extensions uses %s to move, show a result and close; pick another letter", e.Name, e.Key, extensionListKeys)
		}
		k := fmt.Sprint(e.Name, e.OnDemand)
		if names[k] {
			return fmt.Errorf("extension %q is listed twice", e.Name)
		}
		names[k] = true
		if other, ok := keys[e.Key]; ok && e.Key != "" {
			return fmt.Errorf("extensions %q and %q both use key %q", other, e.Name, e.Key)
		}
		keys[e.Key] = e.Name
	}
	return nil
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
		a = Default().Agent
	}
	return a.Reviewers[0]
}

// Model is the configured model for the named agent ("" = its default).
func (a Agent) Model(name string) string { return a.Models[name] }

// BotSet is the bots setting as a set, for gh.Actor.IsBot.
func (c Config) BotSet() map[string]bool {
	out := map[string]bool{}
	for _, b := range c.Bots {
		out[b] = true
	}
	return out
}

// RunImage is the container image the agent's run tool uses for repo ("" = none).
func (a Agent) RunImage(repo string) string {
	if img := a.RunImages[repo]; img != "" {
		return img
	}
	return a.RunImages["*"]
}

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

var slackClientID = regexp.MustCompile(`^\d+\.\d+$`)

// IsSlackClientID is whether s looks like a Slack app's client ID: two
// numbers joined by a dot, under Basic Information, App Credentials.
func IsSlackClientID(s string) bool { return slackClientID.MatchString(s) }

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
		SlackDMs:         true,
		SlackAfterReview: "react",
		SlackReactions:   SlackReactions{Reviewing: "eyes", Approved: "white_check_mark", ChangesRequested: "x", Commented: "speech_balloon"},
		StaleAfter:       Duration{24 * time.Hour},
		NotifyTimeout:    Duration{8 * time.Second},
		CheckUpdates:     true,
		AutoUpdate:       true,
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
		Agent:            Agent{Reviewers: []string{"claude"}, ShouldFix: true, Nits: true, RunCacheGB: 20},
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

// Themes are the TUI's color themes.
var Themes = []string{"dracula", "catppuccin", "tokyonight", "gruvbox", "nord", "rose-pine", "kanagawa"}

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
	for _, e := range []struct {
		key, val string
		want     []string
	}{
		{"leader", cfg.Leader, Leaders},
		{"group_by", cfg.GroupBy, []string{"status", "repo"}},
		{"slack_after_review", cfg.SlackAfterReview, []string{"react", "reply", "off"}},
		{"chat_mode", cfg.ChatMode, []string{"popup", "fullscreen"}},
		{"theme", cfg.Theme, Themes},
	} {
		if !slices.Contains(e.want, e.val) {
			q := make([]string, len(e.want))
			for i, w := range e.want {
				q[i] = strconv.Quote(w)
			}
			return cfg, fmt.Errorf("%s = %q: use %s or %s", e.key, e.val, strings.Join(q[:len(q)-1], ", "), q[len(q)-1])
		}
	}
	if err := cfg.Agent.checkExtensions(); err != nil {
		return cfg, err
	}
	if len(cfg.Agent.Reviewers) == 0 {
		cfg.Agent.Reviewers = Default().Agent.Reviewers
	}
	return cfg, nil
}

// PollEvery is how often to sync: poll_interval, but never more often than
// every 30 seconds.
func (c Config) PollEvery() time.Duration { return max(c.PollInterval.Duration, 30*time.Second) }

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
	strs("# Where your local clones live. Agent reviews reuse <root>/<repo> when it\n# exists, and otherwise clone into prowl's data directory.\n", "repo_roots", c.RepoRoots, d.RepoRoots)
	dur("# How often to check GitHub, in the TUI and in `prowl watch`.\n", "poll_interval", c.PollInterval, d.PollInterval)
	dur("# How long desktop notifications stay on screen.\n", "notify_timeout", c.NotifyTimeout, d.NotifyTimeout)
	boolean("# Ask GitHub once a day whether a newer prowl is out, and say so.\n", "check_updates", c.CheckUpdates, d.CheckUpdates)
	boolean("# When the install script installed prowl, install a newer prowl in the\n# background; a restart (<leader>U) runs it. A package manager's prowl is never\n# replaced: prowl says which command updates it.\n", "auto_update", c.AutoUpdate, d.AutoUpdate)
	strs("# Accounts shown as bots (dimmed and folded) in a PR's activity. Logins\n# ending in [bot] always count.\n", "bots", c.Bots, d.Bots)

	w("# Inbox ----------------------------------------------------------------\n\n")
	strs("# Repos (owner/name) to show every open PR of, not only the ones that involve\n# you, e.g. to drain a repo before a release. They show in All: unclaimed when\n# nobody was asked to review, watching when someone else has it. <leader>w toggles.\n", "watch_repos", c.WatchRepos, d.WatchRepos)
	dur("# A PR waiting on you longer than this is flagged.\n", "stale_after", c.StaleAfter, d.StaleAfter)
	boolean("# Sort PRs inside each inbox section by Conventional Commit type (fix first,\n# chore last), then by how long they have waited. Off: by wait time only.\n", "sort_by_type", c.SortByType, d.SortByType)
	strs("# The type order for that sort. \"other\" is any title that isn't a\n# conventional commit, and any type not listed. To add a type, put it in\n# the list where it belongs and give it a color under [type_colors].\n", "type_order", c.Types(), DefaultTypeOrder)
	boolean("# A PR pushed to after you approved needs you again, as re-check. For repos\n# where GitHub keeps an approval through new pushes; off trusts your approval.\n", "recheck_after_push", c.RecheckAfterPush, d.RecheckAfterPush)
	str("# Inbox grouping: status (what each PR needs from you) or repo. <leader>g toggles.\n", "group_by", c.GroupBy, d.GroupBy)

	w("# Slack ----------------------------------------------------------------\n\n")
	str("# Client ID of your team's prowl Slack app; `prowl slack` signs you in to\n# it. Not a secret. Empty: `prowl slack` shows how to make the app.\n", "slack_client_id", c.SlackClientID, d.SlackClientID)
	strs("# Slack channel IDs whose PR links show in the inbox, as shared, until you\n# review them. Only channels you're in.\n", "slack_channels", c.SlackChannels, d.SlackChannels)
	boolean("# PR links sent to you in Slack DMs and group DMs show in the inbox too.\n", "slack_dms", c.SlackDMs, d.SlackDMs)
	str("# Once you review a PR someone shared in Slack: react (keep an emoji on their\n# message in step with your review, wherever you reviewed; see [slack_reactions]),\n# reply (post the Slack line in its thread when you submit from prowl), or off.\n", "slack_after_review", c.SlackAfterReview, d.SlackAfterReview)

	w("# TUI ------------------------------------------------------------------\n\n")
	str("# The leader: the key that opens prowl's action menu, written <leader> in the\n# docs. \"space\", \",\", \"\\\\\" or \";\".\n", "leader", c.Leader, d.Leader)
	boolean("", "mouse", c.Mouse, d.Mouse)
	str("# Editor for reviews. Empty uses $VISUAL, then $EDITOR, then vi (notepad on Windows).\n", "editor", c.Editor, d.Editor)
	str("# How <leader>ci shows the agent chat: popup (floating window over prowl)\n# or fullscreen (prowl steps aside until you quit the chat). Windows always\n# uses fullscreen.\n", "chat_mode", c.ChatMode, d.ChatMode)
	str("# dracula, catppuccin, tokyonight, gruvbox, nord, rose-pine, kanagawa\n", "theme", c.Theme, d.Theme)
	boolean("# Powerline separators and icons (needs a Nerd Font).\n", "nerd_font", c.NerdFont, d.NerdFont)
	boolean("# Explain the inbox icons at the bottom of the inbox pane.\n", "show_legend", c.ShowLegend, d.ShowLegend)
	boolean("# The owl: asleep on an empty inbox, waking up while prowl syncs.\n", "playful", c.Playful, d.Playful)
	str("# Stripped from repo names on screen to save room, e.g. \"acme-\" shows\n# acme-api as api. Empty shows full names.\n", "repo_prefix", c.RepoPrefix, d.RepoPrefix)
	boolean("# Give every repo its own color in the inbox instead of one shared color.\n", "color_repos", c.ColorRepos, d.ColorRepos)

	w("# Inbox and Slack tables -----------------------------------------------\n\n")
	w("# Color per Conventional Commit type, added to or replacing the built-in\n# ones: red, green, orange, pink, yellow, purple, cyan, dim or \"#rrggbb\".\n[type_colors]\n")
	mapLines(&b, c.TypeColors, "# wip = \"orange\"\n")

	w("\n# The emoji prowl reacts with on a shared PR's message, by Slack name, without\n# colons. prowl only removes the ones it added.\n[slack_reactions]\n")
	r, dr := c.SlackReactions, d.SlackReactions
	str("", "reviewing", r.Reviewing, dr.Reviewing)
	str("", "approved", r.Approved, dr.Approved)
	str("", "changes_requested", r.ChangesRequested, dr.ChangesRequested)
	str("", "commented", r.Commented, dr.Commented)

	w("# GitHub login -> how to refer to them in copied Slack lines.\n[slack_names]\n")
	mapLines(&b, c.SlackNames, "# octocat = \"@Mona\"\n")

	w("\n# Agent ----------------------------------------------------------------\n\n")
	w("[agent]\n")
	strs("# The agents that review PRs: claude (Claude Code), codex (Codex CLI), or\n# command (any program; see `command`). <leader>cc runs all of them together and\n# their findings post as one review. The first is the main agent.\n", "reviewers", c.Agent.Reviewers, d.Agent.Reviewers)
	str("# For the command agent: a shell command that reads the review prompt on\n# stdin and prints the review as JSON (`prowl schema` shows the format).\n", "command", c.Agent.Command, d.Agent.Command)
	str("# For the command agent: what <leader>l runs to log in, e.g. \"my-agent login\".\n", "login_command", c.Agent.LoginCommand, d.Agent.LoginCommand)
	str("# For the command agent: checks the login; exit 0 means logged in. It may print\n# {\"loggedIn\": true, \"account\": \"...\", \"expiresAt\": \"<RFC 3339>\"} to say more.\n", "status_command", c.Agent.StatusCommand, d.Agent.StatusCommand)
	strs("# Skills the agent must use on every review. Its other skills stay available.\n", "skills", c.Agent.Skills, d.Agent.Skills)
	strs("# Directories the agent may read besides the PR, e.g. the org standards repo\n# that AGENTS.md and CONTRIBUTING.md link to. GitHub links into these repos\n# are resolved to the local clone.\n", "context_dirs", c.Agent.ContextDirs, d.Agent.ContextDirs)
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
	set("# Most disk the run tool's caches may take, in GB (see [agent.run_images]).\n# Past it, prowl deletes the caches used longest ago. 0 is no limit.\n", "run_cache_gb", fmt.Sprint(c.Agent.RunCacheGB), fmt.Sprint(d.Agent.RunCacheGB))
	w("# Model per agent. Missing: the agent's own default. Claude takes opus,\n# fable, sonnet, haiku or a model ID; Codex takes an OpenAI model name.\n[agent.models]\n")
	mapLines(&b, c.Agent.Models, "# claude = \"opus\"\n")
	w("\n# Model per agent for AI summaries. Missing: the agent's review model. A small,\n# fast model is plenty, e.g. claude = \"haiku\".\n[agent.summary_models]\n")
	mapLines(&b, c.Agent.SummaryModels, "# claude = \"haiku\"\n")
	w("\n# Container image per repo (owner/name) for the agent's run tool, which runs\n# commands like go test in Docker on a copy of the code. \"*\" is the\n# fallback. No image, or no docker: reviews only read.\n[agent.run_images]\n")
	mapLines(&b, c.Agent.RunImages, "# \"*\" = \"golang:1.27\"\n# \"acme/web\" = \"node:22\"\n")
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
	w("\n# Extensions: programs prowl runs during reviews of the repos listed, whose\n# output goes to the reviewer and into the findings, or with on_demand = true\n# when you pick them under <leader>e.\n")
	if len(c.Agent.Extensions) == 0 {
		w("# [[agent.extensions]]\n# name = \"licenses\"\n# command = \"acme-license-check\"\n# repos = [\"*\"]\n# timeout = \"2m\"\n")
	}
	for _, e := range c.Agent.Extensions {
		w("[[agent.extensions]]\nname = %q\ncommand = %q\nrepos = %s\n", e.Name, e.Command, list(e.Repos))
		if e.Timeout.Duration > 0 {
			w("timeout = %q\n", e.Timeout.String())
		}
		for _, f := range []struct {
			key string
			on  bool
		}{{"needs_base", e.NeedsBase}, {"on_demand", e.OnDemand}, {"confirm", e.Confirm}} {
			if f.on {
				w("%s = true\n", f.key)
			}
		}
		if e.Key != "" {
			w("key = %q\n", e.Key)
		}
	}

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
