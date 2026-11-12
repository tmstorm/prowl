package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
)

type optKind int

const (
	optChoice optKind = iota
	optBool
	optText
	optEditor
	optPick // opens a checklist popup
)

type option struct {
	section     string                      // heading in the menu
	title       string                      // what the menu calls it
	label, help string                      // label is the config key
	show        func(c *config.Config) bool // nil: always shown
	empty       string                      // what an empty value means, if not "none"
	kind        optKind
	choices     []string
	choicesFn   func(c *config.Config) []string // choices worked out when cycled
	get         func(c *config.Config) string
	set         func(c *config.Config, v string) error
	// state and turn make a switch of something that isn't a config value.
	state func(m *Model) bool
	turn  func(m *Model, on bool) tea.Cmd
}

// switchTo turns a bool option on or off.
func (p *optionsPopup) switchTo(m *Model, o option, on bool) tea.Cmd {
	if o.turn != nil {
		return o.turn(m, on)
	}
	return p.apply(m, o, strconv.FormatBool(on))
}

func isOn(m *Model, o option) bool {
	if o.state != nil {
		return o.state(m)
	}
	return o.get(&m.cfg) == "true"
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func durationOpt(section, title, label, help string, field func(c *config.Config) *config.Duration) option {
	return option{section: section, title: title, label: label, help: help, kind: optText,
		get: func(c *config.Config) string { return field(c).String() },
		set: func(c *config.Config, v string) error {
			d, err := time.ParseDuration(strings.TrimSpace(v))
			if err != nil {
				return fmt.Errorf("not a duration (try 90s, 5m, 2h): %v", v)
			}
			field(c).Duration = d
			return nil
		}}
}

// optSections are the headings of the options menu, in order.
var optSections = []string{"Inbox", "Appearance", "Review agent", "GitHub", "Editor & Slack"}

func isCommandAgent(c *config.Config) bool { return c.Agent.Main() == "command" }

var options = []option{
	// Inbox
	{section: "Inbox", title: "Group by", label: "group_by", help: "Sections by what each PR needs from you, or by repo. <leader>g toggles it too.",
		kind: optChoice, choices: []string{"status", "repo"},
		get: func(c *config.Config) string { return c.GroupBy },
		set: func(c *config.Config, v string) error { c.GroupBy = v; return nil }},
	{section: "Inbox", title: "Sort by commit type", label: "sort_by_type", help: "Inside each section: fixes first, chores last, then by wait time.", kind: optBool,
		get: func(c *config.Config) string { return fmt.Sprint(c.SortByType) },
		set: func(c *config.Config, v string) error { c.SortByType = v == "true"; return nil }},
	{section: "Inbox", title: "Re-check after push", label: "recheck_after_push", help: "A PR pushed to after you approved needs you again. Off trusts your approval.", kind: optBool,
		get: func(c *config.Config) string { return fmt.Sprint(c.RecheckAfterPush) },
		set: func(c *config.Config, v string) error { c.RecheckAfterPush = v == "true"; return nil }},
	{section: "Inbox", title: "Commit type order", label: "type_order", help: "Comma separated. \"other\" is any title that isn't a conventional commit.", kind: optText,
		get: func(c *config.Config) string { return strings.Join(c.Types(), ", ") },
		set: func(c *config.Config, v string) error {
			c.TypeOrder = splitList(v)
			if strings.Join(c.TypeOrder, ",") == strings.Join(config.DefaultTypeOrder, ",") {
				c.TypeOrder = nil
			}
			return nil
		}},
	mapOpt("Inbox", "Commit type colors", "type_colors", "# commit type = color: red, green, orange, pink, yellow, purple, cyan, dim or \"#rrggbb\"\n# wip = \"orange\"\n",
		func(c *config.Config) *map[string]string { return &c.TypeColors }),
	durationOpt("Inbox", "Overdue after", "stale_after", "A PR waiting on you longer than this turns yellow, and red at twice that.", func(c *config.Config) *config.Duration { return &c.StaleAfter }),
	{section: "Inbox", title: "Background notifications", label: "prowl service", kind: optBool,
		help:  "Runs prowl watch at login, so you hear about PRs while prowl is closed: a systemd user service, launchd agent or scheduled task.",
		state: func(m *Model) bool { return m.core.Service() },
		turn: func(m *Model, on bool) tea.Cmd {
			co := m.core
			m.info("setting up background notifications…")
			return func() tea.Msg {
				what, err := co.SetService(on)
				return serviceMsg{on: on, what: what, err: err}
			}
		}},
	{section: "Inbox", title: "Bot accounts", label: "bots", help: "Dimmed and folded in a PR's activity. Comma separated; logins ending in [bot] always count.", kind: optText,
		get: func(c *config.Config) string { return strings.Join(c.Bots, ", ") },
		set: func(c *config.Config, v string) error { c.Bots = splitList(v); return nil }},

	// Appearance
	{section: "Appearance", title: "Leader key", label: "leader", help: "The key that opens the action menu; the docs call it the leader. Takes effect at once.", kind: optChoice, choices: config.Leaders,
		get: func(c *config.Config) string { return c.Leader },
		set: func(c *config.Config, v string) error { c.Leader = v; return nil }},
	{section: "Appearance", title: "Theme", label: "theme", help: "h/l previews each theme as you go.", kind: optChoice, choices: themeNames,
		get: func(c *config.Config) string { return c.Theme },
		set: func(c *config.Config, v string) error { c.Theme = v; return nil }},
	{section: "Appearance", title: "Nerd Font icons", label: "nerd_font", help: "Powerline separators and icons. Needs a Nerd Font in your terminal.", kind: optBool,
		get: func(c *config.Config) string { return fmt.Sprint(c.NerdFont) },
		set: func(c *config.Config, v string) error { c.NerdFont = v == "true"; return nil }},
	{section: "Appearance", title: "Icon legend", label: "show_legend", help: "Explains the inbox icons at the bottom of the inbox.", kind: optBool,
		get: func(c *config.Config) string { return fmt.Sprint(c.ShowLegend) },
		set: func(c *config.Config, v string) error { c.ShowLegend = v == "true"; return nil }},
	{section: "Appearance", title: "The owl", label: "playful", help: "Sleeps on an empty inbox and wakes up while prowl syncs. Off: plain text.", kind: optBool,
		get: func(c *config.Config) string { return fmt.Sprint(c.Playful) },
		set: func(c *config.Config, v string) error { c.Playful = v == "true"; return nil }},
	{section: "Appearance", title: "Repo prefix", label: "repo_prefix", empty: "full names", help: "Stripped from repo names on screen to save room, e.g. acme- shows acme-api as api.", kind: optText,
		get: func(c *config.Config) string { return c.RepoPrefix },
		set: func(c *config.Config, v string) error { c.RepoPrefix = strings.TrimSpace(v); return nil }},
	{section: "Appearance", title: "Color per repo", label: "color_repos", help: "Give every repo its own color instead of one shared color.", kind: optBool,
		get: func(c *config.Config) string { return fmt.Sprint(c.ColorRepos) },
		set: func(c *config.Config, v string) error { c.ColorRepos = v == "true"; return nil }},
	{section: "Appearance", title: "Mouse", label: "mouse", help: "Click and scroll anywhere. Keys keep working either way.", kind: optBool,
		get: func(c *config.Config) string { return fmt.Sprint(c.Mouse) },
		set: func(c *config.Config, v string) error { c.Mouse = v == "true"; return nil }},

	// Review agent
	{section: "Review agent", title: "Reviewers", label: "reviewers", help: "Who reviews PRs: Claude Code, Codex, or a command of your own. <leader>cc runs them all; findings post as one review.", kind: optPick,
		get: func(c *config.Config) string { return strings.Join(c.Agent.Reviewers, ", ") },
		set: func(c *config.Config, v string) error {
			if c.Agent.Reviewers = splitList(v); len(c.Agent.Reviewers) == 0 {
				return fmt.Errorf("pick at least one reviewer")
			}
			return nil
		}},
	{section: "Review agent", title: "Command", label: "command", help: "Reads the review prompt on stdin and prints the review as JSON (prowl schema).", kind: optText, show: isCommandAgent,
		get: func(c *config.Config) string { return c.Agent.Command },
		set: func(c *config.Config, v string) error { c.Agent.Command = strings.TrimSpace(v); return nil }},
	{section: "Review agent", title: "Login command", label: "login_command", help: "What <leader>l runs to log the command agent in.", kind: optText, show: isCommandAgent,
		get: func(c *config.Config) string { return c.Agent.LoginCommand },
		set: func(c *config.Config, v string) error { c.Agent.LoginCommand = strings.TrimSpace(v); return nil }},
	{section: "Review agent", title: "Login check", label: "status_command", help: "Exits 0 when the command agent is logged in.", kind: optText, show: isCommandAgent,
		get: func(c *config.Config) string { return c.Agent.StatusCommand },
		set: func(c *config.Config, v string) error { c.Agent.StatusCommand = strings.TrimSpace(v); return nil }},
	{section: "Review agent", title: "Required skills", label: "skills", help: "Skills every review must use, comma separated. The agent's other skills stay available.", kind: optText,
		get: func(c *config.Config) string { return strings.Join(c.Agent.Skills, ", ") },
		set: func(c *config.Config, v string) error { c.Agent.Skills = splitList(v); return nil }},
	{section: "Review agent", title: "Extra directories", label: "context_dirs", help: "Directories the agent may read besides the PR, e.g. your org's standards repo. Comma separated.", kind: optText,
		get: func(c *config.Config) string { return strings.Join(c.Agent.ContextDirs, ", ") },
		set: func(c *config.Config, v string) error { c.Agent.ContextDirs = splitList(v); return nil }},
	{section: "Review agent", title: "Local review commands", label: "local_commands", help: "Commands prowl review may run in your own checkout, e.g. go test. Claude Code only; PR reviews never run them. Comma separated.", kind: optText,
		get: func(c *config.Config) string { return strings.Join(c.Agent.LocalCommands, ", ") },
		set: func(c *config.Config, v string) error { c.Agent.LocalCommands = splitList(v); return nil }},
	{section: "Review agent", title: "Review instructions", label: "review_instructions", help: "What the agent looks for and how it writes. Opens your editor with the current text; save it unchanged or empty to keep prowl's.", kind: optEditor,
		get: func(c *config.Config) string { return c.Agent.Instructions() },
		set: func(c *config.Config, v string) error {
			if v = strings.TrimSpace(v); v == config.DefaultReviewInstructions {
				v = ""
			}
			c.Agent.ReviewInstructions = v
			return nil
		}},
	{section: "Review agent", title: "Should-fix findings", label: "should_fix", help: "Report real problems that don't block the merge. Must-fix findings are always reported.", kind: optBool,
		get: func(c *config.Config) string { return fmt.Sprint(c.Agent.ShouldFix) },
		set: func(c *config.Config, v string) error { c.Agent.ShouldFix = v == "true"; return nil }},
	{section: "Review agent", title: "Nits", label: "nits", help: "Report minor, optional findings.", kind: optBool,
		get: func(c *config.Config) string { return fmt.Sprint(c.Agent.Nits) },
		set: func(c *config.Config, v string) error { c.Agent.Nits = v == "true"; return nil }},
	{section: "Review agent", title: "Review profiles", label: "profiles", help: "Named ways to review, run with <leader>cp. Opens your editor.", kind: optEditor,
		get: profilesText,
		set: func(c *config.Config, v string) error {
			var in struct {
				Profiles []config.Profile `toml:"profiles"`
			}
			if _, err := toml.Decode(v, &in); err != nil {
				return fmt.Errorf("profiles: %w", err)
			}
			for _, p := range in.Profiles {
				if p.Name == "" || strings.TrimSpace(p.Prompt) == "" {
					return fmt.Errorf("profiles: every profile needs a name and a prompt")
				}
			}
			c.Agent.Profiles = in.Profiles
			return nil
		}},
	{section: "Review agent", title: "AI summary for new PRs", label: "auto_summary", help: "Write an AI summary of each PR as it first needs you. <leader>ca writes one on demand either way.", kind: optBool,
		get: func(c *config.Config) string { return fmt.Sprint(c.Agent.AutoSummary) },
		set: func(c *config.Config, v string) error { c.Agent.AutoSummary = v == "true"; return nil }},
	{section: "Review agent", title: "Learn from my edits", label: "edit_examples", help: "How many of your recent rewrites of agent drafts go into review prompts as examples (prowl edits shows them). 0 is off.", kind: optChoice, choices: []string{"0", "1", "2", "3", "5"},
		get: func(c *config.Config) string { return fmt.Sprint(c.Agent.EditExamples) },
		set: func(c *config.Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("edit_examples: %w", err)
			}
			c.Agent.EditExamples = n
			return nil
		}},
	{section: "Review agent", title: "Chat window", label: "chat_mode", help: "How <leader>ci shows the agent: a floating popup, or fullscreen while prowl steps aside.", kind: optChoice, choices: []string{"popup", "fullscreen"},
		get: func(c *config.Config) string { return c.ChatMode },
		set: func(c *config.Config, v string) error { c.ChatMode = v; return nil }},

	// GitHub
	{section: "GitHub", title: "Orgs", label: "orgs", empty: "all of GitHub", help: "Orgs to pull PRs from, comma separated. Empty: your PRs across all of GitHub.", kind: optText,
		get: func(c *config.Config) string { return strings.Join(c.Orgs, ", ") },
		set: func(c *config.Config, v string) error { c.Orgs = splitList(v); return nil }},
	{section: "GitHub", title: "Watched repos", label: "watch_repos", help: "owner/name, comma separated. Every open PR shows in All, not only yours. <leader>w toggles the current repo.", kind: optText,
		get: func(c *config.Config) string { return strings.Join(c.WatchRepos, ", ") },
		set: func(c *config.Config, v string) error { c.WatchRepos = splitList(v); return nil }},
	{section: "GitHub", title: "Local clones", label: "repo_roots", help: "Directories holding your clones; reviews reuse <dir>/<repo>. Comma separated.", kind: optText,
		get: func(c *config.Config) string { return strings.Join(c.RepoRoots, ", ") },
		set: func(c *config.Config, v string) error { c.RepoRoots = splitList(v); return nil }},
	durationOpt("GitHub", "Check every", "poll_interval", "How often to check GitHub, here and in prowl watch.", func(c *config.Config) *config.Duration { return &c.PollInterval }),
	durationOpt("GitHub", "Notifications stay", "notify_timeout", "How long desktop notifications stay on screen.", func(c *config.Config) *config.Duration { return &c.NotifyTimeout }),

	// Editor & Slack
	{section: "Editor & Slack", title: "Editor", label: "editor", empty: "$VISUAL, $EDITOR or vi", help: "For reviews and long text. Empty uses $VISUAL, then $EDITOR, then vi (notepad on Windows).", kind: optText,
		get: func(c *config.Config) string { return c.Editor },
		set: func(c *config.Config, v string) error { c.Editor = strings.TrimSpace(v); return nil }},
	mapOpt("Editor & Slack", "Slack names", "slack_names", "# GitHub login = \"name used in Slack lines\"\n# some-login = \"@alice\"\n",
		func(c *config.Config) *map[string]string { return &c.SlackNames }),
}

// modelOption is the model setting for one reviewer, listed per agent.
func modelOption(name string) option {
	return option{section: "Review agent", title: agent.Title(name) + " model", label: "models." + name, kind: optChoice,
		help:      "The models " + agent.Title(name) + " itself offers. Default follows its own setting.",
		choicesFn: func(c *config.Config) []string { return core.Models(*c, name) },
		get:       func(c *config.Config) string { return c.Agent.Model(name) },
		set:       func(c *config.Config, v string) error { c.Agent.SetModel(name, v); return nil }}
}

// summaryModelOption is the AI summary model for one reviewer.
func summaryModelOption(name string) option {
	return option{section: "Review agent", title: agent.Title(name) + " summary model", label: "summary_models." + name, kind: optChoice,
		help:      "Model for AI summaries (<leader>ca). Default is the review model; a small, fast one is plenty.",
		choicesFn: func(c *config.Config) []string { return core.Models(*c, name) },
		get:       func(c *config.Config) string { return c.Agent.SummaryModels[name] },
		set:       func(c *config.Config, v string) error { c.Agent.SetSummaryModel(name, v); return nil }}
}

// profilesText is the review profiles as TOML for your editor, with the
// built-in ones shown commented out.
func profilesText(c *config.Config) string {
	var b strings.Builder
	b.WriteString("# Review profiles, run with <leader>cp. A profile named like a built-in one replaces it.\n")
	b.WriteString("# reviewers and skills are optional: by default the main agent runs it with your skills.\n#\n")
	for _, p := range config.BuiltinProfiles() {
		fmt.Fprintf(&b, "# built in:\n# [[profiles]]\n# name = %q\n# prompt = %q\n\n", p.Name, p.Prompt)
	}
	for _, p := range c.Agent.Profiles {
		fmt.Fprintf(&b, "[[profiles]]\nname = %q\nprompt = %q\n", p.Name, p.Prompt)
		if len(p.Reviewers) > 0 {
			fmt.Fprintf(&b, "reviewers = [%s]\n", quoteList(p.Reviewers))
		}
		if len(p.Skills) > 0 {
			fmt.Fprintf(&b, "skills = [%s]\n", quoteList(p.Skills))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func quoteList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = fmt.Sprintf("%q", x)
	}
	return strings.Join(q, ", ")
}

// mapOpt edits a string-to-string table in your editor.
func mapOpt(section, title, label, header string, field func(c *config.Config) *map[string]string) option {
	return option{section: section, title: title, label: label, help: "Opens your editor.", kind: optEditor,
		get: func(c *config.Config) string {
			m := *field(c)
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			var b strings.Builder
			b.WriteString(header)
			for _, k := range keys {
				fmt.Fprintf(&b, "%q = %q\n", k, m[k])
			}
			return b.String()
		},
		set: func(c *config.Config, v string) error {
			out := map[string]string{}
			if _, err := toml.Decode(v, &out); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
			*field(c) = out
			return nil
		}}
}

type optionsPopup struct {
	cur     int   // index into visibleOptions
	top     int   // first menu line shown
	lineOpt []int // per drawn menu line: option index, or -1 for headings
	editing bool
	input   textinput.Model
	err     string
}

func newOptions(m *Model) *optionsPopup {
	ti := textinput.New()
	ti.Prompt = ""
	return &optionsPopup{input: ti}
}

func (p *optionsPopup) mode() mode {
	if p.editing {
		return modeInsert
	}
	return modeNormal
}

func (p *optionsPopup) forward(_ *Model, msg tea.Msg) tea.Cmd {
	if !p.editing {
		return nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

// apply sets an option, saves the TOML file, and applies live settings.
func (p *optionsPopup) apply(m *Model, o option, v string) tea.Cmd {
	if err := o.set(&config.Config{}, v); err != nil {
		p.err = err.Error()
		return nil
	}
	if _, err := m.core.Update(func(c *config.Config) error { return o.set(c, v) }); err != nil {
		p.err = err.Error()
		return nil
	}
	p.err = ""
	m.info("saved " + config.Path())
	return m.configChanged()
}

func (p *optionsPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	vis := visibleOptions(&m.cfg)
	p.cur = min(p.cur, len(vis)-1)
	o := vis[p.cur]
	key := k.String()
	if p.editing {
		switch key {
		case "enter":
			p.editing = false
			p.input.Blur()
			return p.apply(m, o, p.input.Value())
		case "esc":
			p.editing = false
			p.input.Blur()
			return nil
		}
		return p.forward(m, k)
	}
	cycle := func(step int) tea.Cmd {
		choices := o.choices
		if o.choicesFn != nil {
			choices = o.choicesFn(&m.cfg)
		}
		cur := o.get(&m.cfg)
		i := 0
		for j, c := range choices {
			if c == cur {
				i = j
			}
		}
		i = (i + step + len(choices)) % len(choices)
		return p.apply(m, o, choices[i])
	}
	switch key {
	case "esc", "q":
		m.popup = nil
	case "j", "down":
		p.cur = min(p.cur+1, len(vis)-1)
		p.err = ""
	case "k", "up":
		p.cur = max(p.cur-1, 0)
		p.err = ""
	case "gg", "g":
		p.cur = 0
	case "G":
		p.cur = len(vis) - 1
	case "h", "left":
		switch o.kind {
		case optChoice:
			return cycle(-1)
		case optBool:
			return p.switchTo(m, o, false)
		}
	case "l", "right":
		switch o.kind {
		case optChoice:
			return cycle(1)
		case optBool:
			return p.switchTo(m, o, true)
		}
	case "enter", " ", "i":
		switch o.kind {
		case optChoice:
			return cycle(1)
		case optBool:
			return p.switchTo(m, o, !isOn(m, o))
		case optText:
			p.editing = true
			p.input.SetValue(o.get(&m.cfg))
			p.input.CursorEnd()
			return p.input.Focus()
		case optPick:
			m.popup = newReviewersPopup(m, p, o)
			return nil
		case optEditor:
			m.popup = nil
			return m.openEditor(o.label+".toml", o.get(&m.cfg), func(m *Model, text string, saved bool) tea.Cmd {
				m.popup = p
				if !saved {
					return nil
				}
				return p.apply(m, o, text)
			})
		}
	}
	return nil
}

// visibleOptions are the options in menu order, minus those that don't
// apply (the command agent's settings when another agent reviews).
func visibleOptions(c *config.Config) []option {
	var out []option
	for _, sec := range optSections {
		for _, o := range options {
			if o.section == sec && (o.show == nil || o.show(c)) {
				out = append(out, o)
				if o.label == "reviewers" {
					for _, r := range c.Agent.Reviewers {
						out = append(out, modelOption(r))
					}
					for _, r := range c.Agent.Reviewers {
						out = append(out, summaryModelOption(r))
					}
				}
			}
		}
	}
	return out
}

func (p *optionsPopup) value(m *Model, o option, sel bool) string {
	if o.kind == optBool {
		if isOn(m, o) {
			return sGreen.Render("● on")
		}
		return sDim.Render("○ off")
	}
	val := o.get(&m.cfg)
	switch o.kind {
	case optChoice:
		if val == "" {
			val = "default"
		}
		if sel {
			return sDim.Render("‹ ") + sBold.Render(val) + sDim.Render(" ›")
		}
		return "  " + val
	case optPick:
		var names []string
		for _, n := range splitList(val) {
			names = append(names, agent.Title(n))
		}
		return "  " + strings.Join(names, sDim.Render(" + ")) + sDim.Render(" ✎")
	case optEditor:
		first := strings.TrimSpace(val)
		switch o.label {
		case "slack_names":
			first = fmt.Sprintf("%d mapped", len(m.cfg.SlackNames))
		case "type_colors":
			first = fmt.Sprintf("%d set", len(m.cfg.TypeColors))
		case "profiles":
			var names []string
			for _, p := range m.cfg.Agent.AllProfiles() {
				names = append(names, p.Name)
			}
			first = strings.Join(names, ", ")
		default:
			if i := strings.IndexByte(first, '\n'); i >= 0 {
				first = first[:i] + " …"
			}
		}
		if first == "" {
			first = "none"
		}
		return "  " + sDim.Render(first+" ✎")
	}
	if val == "" {
		if o.empty != "" {
			return "  " + sDim.Render(o.empty)
		}
		return "  " + sDim.Render("none")
	}
	return "  " + val
}

// optionsChrome is the popup's lines around the menu: border, title and
// blank line above; rule, help (2), error, keys and border below.
const optionsChrome = 3 + 6

func (p *optionsPopup) view(m *Model) string {
	w := min(popWidth(m), 84)
	inner := w - 2
	vis := visibleOptions(&m.cfg)
	p.cur = min(p.cur, len(vis)-1)

	nameW := 0
	for _, o := range vis {
		nameW = max(nameW, lipgloss.Width(o.title))
	}
	nameW += 6
	var lines []string
	p.lineOpt = p.lineOpt[:0]
	curLine, sec := 0, ""
	for i, o := range vis {
		if o.section != sec {
			sec = o.section
			if len(lines) > 0 {
				lines = append(lines, "")
				p.lineOpt = append(p.lineOpt, -1)
			}
			head := " " + sSection.Render(sec) + " "
			lines = append(lines, head+sBorder.Render(strings.Repeat("─", max(inner-lipgloss.Width(head)-1, 0))))
			p.lineOpt = append(p.lineOpt, -1)
		}
		sel := i == p.cur
		val := p.value(m, o, sel)
		if sel && p.editing {
			val = p.input.View()
		}
		name := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Fg)).Render(o.title)
		row := fit("   "+name, nameW) + val
		if sel {
			curLine = len(lines)
			row = highlight(row, inner, sTitle)
		}
		lines = append(lines, fit(row, inner))
		p.lineOpt = append(p.lineOpt, i)
	}

	h := max(m.h-4-optionsChrome, 5)
	if curLine < p.top+1 {
		p.top = max(curLine-2, 0)
	}
	if curLine >= p.top+h {
		p.top = curLine - h + 1
	}
	p.top = min(p.top, max(len(lines)-h, 0))
	shown := lines[p.top:min(p.top+h, len(lines))]

	o := vis[p.cur]
	foot := []string{sBorder.Render(strings.Repeat("─", inner))}
	help := wrap(strings.ReplaceAll(o.help, "<leader>", leader), inner-2)
	for len(help) < 2 {
		help = append(help, "")
	}
	foot = append(foot, " "+sBold.Render(o.title)+sDim.Render("  "+o.label), " "+sDim.Render(help[0]))
	if len(help) > 1 && help[1] != "" {
		foot[len(foot)-1] += "\n " + sDim.Render(help[1])
	}
	if p.err != "" {
		foot = append(foot, " "+sErr.Render(p.err))
	}
	keys := "j/k move · h/l change · enter edit · click to select, again to change · esc close"
	if p.editing {
		keys = "enter save · esc cancel"
	}
	foot = append(foot, " "+sDim.Render(keys))
	scroll := ""
	if len(lines) > h {
		scroll = sDim.Render(fmt.Sprintf("  %d/%d", p.cur+1, len(vis)))
	}
	return popBox("Options"+scroll, strings.Join(shown, "\n")+"\n"+strings.Join(foot, "\n"), w, pal.Accent)
}

// click selects a setting, or changes it when it is already selected (as
// enter does). y counts from the popup's top border.
func (p *optionsPopup) click(m *Model, _, y int) tea.Cmd {
	i := p.top + y - 3
	if p.editing || i < 0 || i >= len(p.lineOpt) || p.lineOpt[i] < 0 {
		return nil
	}
	if p.lineOpt[i] == p.cur {
		return p.update(m, tea.KeyMsg{Type: tea.KeyEnter})
	}
	p.cur, p.err = p.lineOpt[i], ""
	return nil
}

func (p *optionsPopup) wheel(m *Model, d int) {
	if !p.editing {
		p.cur = min(max(p.cur+d, 0), len(visibleOptions(&m.cfg))-1)
	}
}

// ---- reviewers: which agents review, in order (the first is the main one)

type reviewersPopup struct {
	back  *optionsPopup
	o     option
	names []string // every agent, reviewers first in their order
	on    map[string]bool
	cur   int
}

func newReviewersPopup(m *Model, back *optionsPopup, o option) *reviewersPopup {
	p := &reviewersPopup{back: back, o: o, on: map[string]bool{}}
	for _, n := range m.cfg.Agent.Reviewers {
		p.names = append(p.names, n)
		p.on[n] = true
	}
	for _, n := range agent.Names() {
		if !p.on[n] {
			p.names = append(p.names, n)
		}
	}
	return p
}

func (p *reviewersPopup) mode() mode { return modeNormal }

func (p *reviewersPopup) chosen() []string {
	var out []string
	for _, n := range p.names {
		if p.on[n] {
			out = append(out, n)
		}
	}
	return out
}

func (p *reviewersPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "j", "down":
		p.cur = min(p.cur+1, len(p.names)-1)
	case "k", "up":
		p.cur = max(p.cur-1, 0)
	case "enter", " ", "x":
		n := p.names[p.cur]
		if p.on[n] && len(p.chosen()) == 1 {
			return nil
		}
		p.on[n] = !p.on[n]
		return p.back.apply(m, p.o, strings.Join(p.chosen(), ","))
	case "K", "J":
		to := p.cur - 1
		if k.String() == "J" {
			to = p.cur + 1
		}
		if to < 0 || to >= len(p.names) {
			return nil
		}
		p.names[p.cur], p.names[to] = p.names[to], p.names[p.cur]
		p.cur = to
		return p.back.apply(m, p.o, strings.Join(p.chosen(), ","))
	case "esc", "q", "h":
		m.popup = p.back
	}
	return nil
}

func (p *reviewersPopup) view(m *Model) string {
	w := min(popWidth(m), 64)
	main := ""
	if c := p.chosen(); len(c) > 0 {
		main = c[0]
	}
	var rows []string
	for i, n := range p.names {
		box := sDim.Render("[ ]")
		if p.on[n] {
			box = sGreen.Render("[x]")
		}
		row := " " + box + " " + fit(sBold.Render(agent.Title(n)), 9) + sDim.Render(n)
		if n == main {
			row += sTitle.Render("  main")
		}
		row = fit(row, w-4)
		if i == p.cur {
			row = highlight(row, w-4, sTitle)
		}
		rows = append(rows, row)
	}
	help := sDim.Render(" space toggle · J/K move down/up · esc back") + "\n" +
		sDim.Render(" <leader>cc runs every checked agent. The main one is first;") + "\n" +
		sDim.Render(" it reviews alone with <leader>cC and its summary is posted.")
	return popBox("Reviewers", strings.Join(rows, "\n")+"\n\n"+help, w, pal.Accent)
}

func (p *reviewersPopup) rows(*Model) (int, int, int) { return 3, 0, len(p.names) }
func (p *reviewersPopup) cursor() *int                { return &p.cur }

type serviceMsg struct {
	on   bool
	what string
	err  error
}
