package tui

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tmstorm/prowl/docs"
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
	section   string                      // heading in the menu
	title     string                      // what the menu calls it
	key, help string                      // key is the config key
	show      func(c *config.Config) bool // nil: always shown
	empty     string                      // what an empty value means, if not "none"
	kind      optKind
	choices   []string
	choicesFn func(c *config.Config) []string // choices worked out when cycled
	summary   func(c *config.Config) string   // an editor option's value in the menu; nil: its first line
	get       func(c *config.Config) string
	set       func(c *config.Config, v string) error
	// open opens a popup for the option instead of the editor.
	open func(m *Model, p *optionsPopup, o option) tea.Cmd
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

func boolOpt(section, title, key, help string, field func(c *config.Config) *bool) option {
	return option{section: section, title: title, key: key, help: help, kind: optBool,
		get: func(c *config.Config) string { return strconv.FormatBool(*field(c)) },
		set: func(c *config.Config, v string) error { *field(c) = v == "true"; return nil }}
}

// listOpt is a comma separated list.
func listOpt(section, title, key, help string, field func(c *config.Config) *[]string) option {
	return option{section: section, title: title, key: key, help: help, kind: optText,
		get: func(c *config.Config) string { return strings.Join(*field(c), ", ") },
		set: func(c *config.Config, v string) error { *field(c) = splitList(v); return nil }}
}

func durationOpt(section, title, key, help string, field func(c *config.Config) *config.Duration) option {
	return option{section: section, title: title, key: key, help: help, kind: optText,
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

func isCommandAgent(c *config.Config) bool { return c.Agent.Main() == "command" }

var options = []option{
	{section: "Inbox", title: "Group by", key: "group_by", help: "Sections by what each PR needs from you, or by repo. <leader>g toggles it too.",
		kind: optChoice, choices: []string{"status", "repo"},
		get: func(c *config.Config) string { return c.GroupBy },
		set: func(c *config.Config, v string) error { c.GroupBy = v; return nil }},
	boolOpt("Inbox", "Sort by commit type", "sort_by_type", "Inside each section: fixes first, chores last, then by wait time.", func(c *config.Config) *bool { return &c.SortByType }),
	boolOpt("Inbox", "Re-check after push", "recheck_after_push", "A PR pushed to after you approved needs you again. Off trusts your approval.", func(c *config.Config) *bool { return &c.RecheckAfterPush }),
	{section: "Inbox", title: "Commit type order", key: "type_order", help: "Comma separated. \"other\" is any title that isn't a conventional commit.", kind: optText,
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
	durationOpt("Inbox", "Due after", "stale_after", "A PR waiting on you longer than this is due and turns yellow; at twice that it's overdue and turns red.", func(c *config.Config) *config.Duration { return &c.StaleAfter }),
	{section: "Inbox", title: "Background notifications", key: "prowl service", kind: optBool,
		help:  "Runs prowl watch at login, so you hear about PRs while prowl is closed: a systemd user service, launchd agent or Windows startup entry.",
		state: func(m *Model) bool { return m.core.Service() },
		turn: func(m *Model, on bool) tea.Cmd {
			co := m.core
			m.info("setting up background notifications…")
			return func() tea.Msg {
				what, err := co.SetService(on)
				return serviceMsg{on: on, what: what, err: err}
			}
		}},
	listOpt("Inbox", "Bot accounts", "bots", "Dimmed and folded in a PR's activity. Comma separated; logins ending in [bot] always count.", func(c *config.Config) *[]string { return &c.Bots }),
	{section: "Appearance", title: "Leader key", key: "leader", help: "The key that opens the action menu; the docs call it the leader. Takes effect at once.", kind: optChoice, choices: config.Leaders,
		get: func(c *config.Config) string { return c.Leader },
		set: func(c *config.Config, v string) error { c.Leader = v; return nil }},
	{section: "Appearance", title: "Theme", key: "theme", help: "h/l previews each theme as you go.", kind: optChoice, choices: themeNames,
		get: func(c *config.Config) string { return c.Theme },
		set: func(c *config.Config, v string) error { c.Theme = v; return nil }},
	boolOpt("Appearance", "Nerd Font icons", "nerd_font", "Powerline separators and icons. Needs a Nerd Font in your terminal.", func(c *config.Config) *bool { return &c.NerdFont }),
	boolOpt("Appearance", "Icon legend", "show_legend", "Explains the inbox icons at the bottom of the inbox.", func(c *config.Config) *bool { return &c.ShowLegend }),
	boolOpt("Appearance", "The owl", "playful", "Sleeps on an empty inbox and wakes up while prowl syncs. Off: plain text.", func(c *config.Config) *bool { return &c.Playful }),
	{section: "Appearance", title: "Repo prefix", key: "repo_prefix", empty: "full names", help: "Stripped from repo names on screen to save room, e.g. acme- shows acme-api as api.", kind: optText,
		get: func(c *config.Config) string { return c.RepoPrefix },
		set: func(c *config.Config, v string) error { c.RepoPrefix = strings.TrimSpace(v); return nil }},
	boolOpt("Appearance", "Color per repo", "color_repos", "Give every repo its own color instead of one shared color.", func(c *config.Config) *bool { return &c.ColorRepos }),
	boolOpt("Appearance", "Mouse", "mouse", "Click and scroll anywhere. Keys keep working either way.", func(c *config.Config) *bool { return &c.Mouse }),
	{section: "Review agent", title: "Reviewers", key: "reviewers", help: "Who reviews PRs: Claude Code, Codex, or a command of your own. <leader>cc runs them all; findings post as one review.", kind: optPick,
		get: func(c *config.Config) string { return strings.Join(c.Agent.Reviewers, ", ") },
		set: func(c *config.Config, v string) error {
			if c.Agent.Reviewers = splitList(v); len(c.Agent.Reviewers) == 0 {
				return fmt.Errorf("pick at least one reviewer")
			}
			return nil
		}},
	{section: "Review agent", title: "Command", key: "command", help: "Reads the review prompt on stdin and prints the review as JSON (prowl schema).", kind: optText, show: isCommandAgent,
		get: func(c *config.Config) string { return c.Agent.Command },
		set: func(c *config.Config, v string) error { c.Agent.Command = strings.TrimSpace(v); return nil }},
	{section: "Review agent", title: "Login command", key: "login_command", help: "What <leader>l runs to log the command agent in.", kind: optText, show: isCommandAgent,
		get: func(c *config.Config) string { return c.Agent.LoginCommand },
		set: func(c *config.Config, v string) error { c.Agent.LoginCommand = strings.TrimSpace(v); return nil }},
	{section: "Review agent", title: "Login check", key: "status_command", help: "Exits 0 when the command agent is logged in.", kind: optText, show: isCommandAgent,
		get: func(c *config.Config) string { return c.Agent.StatusCommand },
		set: func(c *config.Config, v string) error { c.Agent.StatusCommand = strings.TrimSpace(v); return nil }},
	listOpt("Review agent", "Required skills", "skills", "Skills every review must use, comma separated. The agent's other skills stay available.", func(c *config.Config) *[]string { return &c.Agent.Skills }),
	listOpt("Review agent", "Extra directories", "context_dirs", "Directories the agent may read besides the PR, e.g. your org's standards repo. Comma separated.", func(c *config.Config) *[]string { return &c.Agent.ContextDirs }),
	mapOpt("Review agent", "Run images", "run_images", "# owner/name = container image for the agent's run tool, which runs commands\n# like go test in Docker on a copy of the code. \"*\" is the fallback.\n# No image, or no docker: reviews only read.\n# \"*\" = \"golang:1.27\"\n# \"acme/web\" = \"node:22\"\n",
		func(c *config.Config) *map[string]string { return &c.Agent.RunImages }),
	{section: "Review agent", title: "Review instructions", key: "review_instructions", help: "What the agent looks for and how it writes. Opens your editor with the current text; save it unchanged or empty to keep prowl's.", kind: optEditor,
		get: func(c *config.Config) string { return c.Agent.Instructions() },
		set: func(c *config.Config, v string) error {
			if v = strings.TrimSpace(v); v == config.DefaultReviewInstructions {
				v = ""
			}
			c.Agent.ReviewInstructions = v
			return nil
		}},
	boolOpt("Review agent", "Should-fix findings", "should_fix", "Report real problems that don't block the merge. Must-fix findings are always reported.", func(c *config.Config) *bool { return &c.Agent.ShouldFix }),
	boolOpt("Review agent", "Nits", "nits", "Report minor, optional findings.", func(c *config.Config) *bool { return &c.Agent.Nits }),
	{section: "Review agent", title: "Review profiles", key: "profiles", help: "Named ways to review, run with <leader>cp. Opens your editor.", kind: optEditor,
		summary: func(c *config.Config) string {
			var names []string
			for _, p := range c.Agent.AllProfiles() {
				names = append(names, p.Name)
			}
			return strings.Join(names, ", ")
		},
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
	boolOpt("Review agent", "AI summary for new PRs", "auto_summary", "Write an AI summary of each PR as it first needs you. <leader>ca writes one on demand either way.", func(c *config.Config) *bool { return &c.Agent.AutoSummary }),
	{section: "Review agent", title: "Learn from my edits", key: "edit_examples", help: "How many of your recent rewrites of agent drafts go into review prompts as examples (prowl edits shows them). 0 is off.", kind: optChoice, choices: []string{"0", "1", "2", "3", "5"},
		get: func(c *config.Config) string { return fmt.Sprint(c.Agent.EditExamples) },
		set: func(c *config.Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("edit_examples: %w", err)
			}
			c.Agent.EditExamples = n
			return nil
		}},
	{section: "Review agent", title: "Chat window", key: "chat_mode", help: "How <leader>ci shows the agent: a floating popup, or fullscreen while prowl steps aside.", kind: optChoice, choices: []string{"popup", "fullscreen"},
		show: func(*config.Config) bool { return floatingChat },
		get:  func(c *config.Config) string { return c.ChatMode },
		set:  func(c *config.Config, v string) error { c.ChatMode = v; return nil }},
	{section: "GitHub", title: "Orgs", key: "orgs", empty: "all of GitHub", help: "Orgs to pull PRs from, comma separated. Empty: your PRs across all of GitHub.", kind: optText,
		get: func(c *config.Config) string { return strings.Join(c.Orgs, ", ") },
		set: func(c *config.Config, v string) error { c.Orgs = splitList(v); return nil }},
	listOpt("GitHub", "Watched repos", "watch_repos", "owner/name, comma separated. Every open PR shows in All, not only yours. <leader>w toggles the current repo.", func(c *config.Config) *[]string { return &c.WatchRepos }),
	listOpt("GitHub", "Local clones", "repo_roots", "Directories holding your clones; reviews reuse <dir>/<repo>. Comma separated.", func(c *config.Config) *[]string { return &c.RepoRoots }),
	durationOpt("GitHub", "Check every", "poll_interval", "How often to check GitHub, here and in prowl watch.", func(c *config.Config) *config.Duration { return &c.PollInterval }),
	durationOpt("GitHub", "Notifications stay", "notify_timeout", "How long desktop notifications stay on screen.", func(c *config.Config) *config.Duration { return &c.NotifyTimeout }),
	{section: "Editor", title: "Editor", key: "editor", empty: "$VISUAL, $EDITOR or vi", help: "For reviews and long text. Empty uses $VISUAL, then $EDITOR, then vi (notepad on Windows).", kind: optText,
		get: func(c *config.Config) string { return c.Editor },
		set: func(c *config.Config, v string) error { c.Editor = strings.TrimSpace(v); return nil }},
	{section: "Slack", title: "Channels", key: "slack_channels", help: "The channels and DMs whose PR links show in the inbox as shared.", kind: optEditor,
		open: func(m *Model, p *optionsPopup, _ option) tea.Cmd { return m.openChannels(p) },
		summary: func(c *config.Config) string {
			if c.SlackDMs {
				return fmt.Sprintf("%d channels + DMs", len(c.SlackChannels))
			}
			return fmt.Sprintf("%d channels", len(c.SlackChannels))
		},
		get: func(c *config.Config) string { return strings.Join(c.SlackChannels, ", ") },
		set: func(c *config.Config, v string) error { c.SlackChannels = splitList(v); return nil }},
	{section: "Slack", title: "After review", key: "slack_after_review", help: "react keeps an emoji on the share in step with your review; reply posts the Slack line in its thread when you submit from prowl.",
		kind: optChoice, choices: []string{"react", "reply", "off"},
		get: func(c *config.Config) string { return c.SlackAfterReview },
		set: func(c *config.Config, v string) error { c.SlackAfterReview = v; return nil }},
	emojiOpt("Reviewing", "reviewing", "While prowl holds an AI review or your inline comments for it.", func(r *config.SlackReactions) *string { return &r.Reviewing }),
	emojiOpt("Approved", "approved", "Once you approve.", func(r *config.SlackReactions) *string { return &r.Approved }),
	emojiOpt("Changes requested", "changes_requested", "Once you request changes.", func(r *config.SlackReactions) *string { return &r.ChangesRequested }),
	emojiOpt("Commented", "commented", "Once you review with comments.", func(r *config.SlackReactions) *string { return &r.Commented }),
	func() option {
		o := mapOpt("Slack", "Slack names", "slack_names", "# GitHub login = \"their Slack user ID\" (a mention), or a name to print as it is\n# some-login = \"U0123ABCD\"\n",
			func(c *config.Config) *map[string]string { return &c.SlackNames })
		o.open = func(m *Model, p *optionsPopup, o option) tea.Cmd {
			if !m.core.SlackConnected() {
				return m.editOption(p, o)
			}
			return m.matchSlackNames(namesProposedMsg{back: p})
		}
		return o
	}(),
	boolOpt("Updates", "Check for updates", "check_updates", "Asks GitHub once a day whether a newer prowl is out, and says so under the legend.", func(c *config.Config) *bool { return &c.CheckUpdates }),
	boolOpt("Updates", "Update in the background", "auto_update", "Installs a newer prowl when the install script installed it; <leader>U restarts into it. A package manager's prowl is never replaced.", func(c *config.Config) *bool { return &c.AutoUpdate }),
}

// optionTopic is the docs topic for an option: its config key as the config
// docs write it, e.g. [agent.models] for models.claude.
func optionTopic(key string) string {
	table, _, _ := strings.Cut(key, ".")
	for _, t := range []string{table, "[" + table + "]", "[agent." + table + "]", "[[agent." + table + "]]"} {
		if tags := docs.Find(t); len(tags) > 0 && tags[0].Tag == t {
			return t
		}
	}
	return key
}

// modelOption is the model setting for one reviewer, listed per agent.
func modelOption(name string) option {
	return option{section: "Review agent", title: agent.Title(name) + " model", key: "models." + name, kind: optChoice,
		help:      "The models " + agent.Title(name) + " itself offers. Default follows its own setting.",
		choicesFn: func(c *config.Config) []string { return core.Models(*c, name) },
		get:       func(c *config.Config) string { return c.Agent.Model(name) },
		set:       func(c *config.Config, v string) error { c.Agent.SetModel(name, v); return nil }}
}

// summaryModelOption is the AI summary model for one reviewer.
func summaryModelOption(name string) option {
	return option{section: "Review agent", title: agent.Title(name) + " summary model", key: "summary_models." + name, kind: optChoice,
		help:      "Model for AI summaries (<leader>ca). Default is the review model; a small, fast one is plenty.",
		choicesFn: func(c *config.Config) []string { return core.Models(*c, name) },
		get:       func(c *config.Config) string { return c.Agent.SummaryModels[name] },
		set:       func(c *config.Config, v string) error { c.Agent.SetSummaryModel(name, v); return nil }}
}

// profilesText is the review profiles as TOML for your editor, with the
// built-in ones shown commented out.
func profilesText(c *config.Config) string {
	var b strings.Builder
	b.WriteString("# Review profiles, run with " + leader + "cp. A profile named like a built-in one replaces it.\n")
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

// emojiOpt is the Slack emoji prowl reacts with for one review state.
func emojiOpt(title, key, help string, field func(r *config.SlackReactions) *string) option {
	return option{section: "Slack", title: title + " emoji", key: "slack_reactions." + key, help: help + " A Slack emoji name, without colons.", kind: optText,
		get: func(c *config.Config) string { return *field(&c.SlackReactions) },
		set: func(c *config.Config, v string) error {
			v = strings.Trim(strings.TrimSpace(v), ":")
			if v == "" {
				return fmt.Errorf("%s: name an emoji", title)
			}
			*field(&c.SlackReactions) = v
			return nil
		}}
}

// mapOpt edits a string-to-string table in your editor.
func mapOpt(section, title, key, header string, field func(c *config.Config) *map[string]string) option {
	return option{section: section, title: title, key: key, help: "Opens your editor.", kind: optEditor,
		summary: func(c *config.Config) string { return fmt.Sprintf("%d set", len(*field(c))) },
		get: func(c *config.Config) string {
			m := *field(c)
			var b strings.Builder
			b.WriteString(header)
			for _, k := range slices.Sorted(maps.Keys(m)) {
				fmt.Fprintf(&b, "%q = %q\n", k, m[k])
			}
			return b.String()
		},
		set: func(c *config.Config, v string) error {
			out := map[string]string{}
			if _, err := toml.Decode(v, &out); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			*field(c) = out
			return nil
		}}
}

type optionsPopup struct {
	nav           // over menu lines
	lineOpt []int // per menu line: option index, or -1 for headings
	editing bool
	input   textinput.Model
	err     string
	errAt   int // the menu line err is about
}

func newOptions(m *Model) *optionsPopup {
	ti := textinput.New()
	ti.Prompt = ""
	p := &optionsPopup{input: ti}
	p.skip = func(i int) bool { return p.lineOpt[i] < 0 }
	p.scrolloff = 2
	return p
}

// list lays the menu out: each section's heading, after a blank line but
// the first, then its options. While you edit one the menu stays put.
func (p *optionsPopup) list(m *Model) (*nav, int) {
	p.lineOpt = p.lineOpt[:0]
	sec := ""
	for i, o := range visibleOptions(&m.cfg) {
		if o.section != sec {
			if sec != "" {
				p.lineOpt = append(p.lineOpt, -1)
			}
			sec = o.section
			p.lineOpt = append(p.lineOpt, -1)
		}
		p.lineOpt = append(p.lineOpt, i)
	}
	p.setLen(len(p.lineOpt), max(m.h-4-optionsChrome, 5))
	if p.editing {
		return &nav{}, 0
	}
	return &p.nav, 3
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
	if err := saveOption(m, o, v); err != nil {
		p.err, p.errAt = err.Error(), p.cur
		return nil
	}
	p.err = ""
	m.info("saved " + config.Path())
	return m.configChanged()
}

// editOption edits an option's value in your editor, then saves it and
// returns to the options.
func (m *Model) editOption(p *optionsPopup, o option) tea.Cmd {
	m.popup = nil
	return m.openEditor(o.key+".toml", o.get(&m.cfg), func(m *Model, text string, saved bool) tea.Cmd {
		if p != nil {
			m.popup = p
		}
		if !saved {
			return nil
		}
		if p == nil {
			if err := saveOption(m, o, text); err != nil {
				m.fail(err.Error())
				return nil
			}
			m.info("saved " + config.Path())
			return m.configChanged()
		}
		return p.apply(m, o, text)
	})
}

// saveOption sets an option to v in the config file, if v is valid.
func saveOption(m *Model, o option, v string) error {
	if err := o.set(&config.Config{}, v); err != nil {
		return err
	}
	_, err := m.core.Update(func(c *config.Config) error { return o.set(c, v) })
	return err
}

func (p *optionsPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	o := visibleOptions(&m.cfg)[p.lineOpt[p.cur]]
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
	case "esc", "q", "ctrl+c":
		m.popup = nil
	case "K":
		return m.openDocs(optionTopic(o.key))
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
			if o.open != nil {
				return o.open(m, p, o)
			}
			return m.editOption(p, o)
		}
	}
	return nil
}

// visibleOptions are the options in menu order, minus those that don't
// apply (the command agent's settings when another agent reviews).
func visibleOptions(c *config.Config) []option {
	var out []option
	for _, o := range options {
		if o.show != nil && !o.show(c) {
			continue
		}
		out = append(out, o)
		if o.key == "reviewers" {
			for _, r := range c.Agent.Reviewers {
				out = append(out, modelOption(r))
			}
			for _, r := range c.Agent.Reviewers {
				out = append(out, summaryModelOption(r))
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
		if o.summary != nil {
			first = o.summary(&m.cfg)
		} else if i := strings.IndexByte(first, '\n'); i >= 0 {
			first = first[:i] + " …"
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
	p.list(m)
	cur := p.lineOpt[p.cur]

	nameW := 0
	for _, o := range vis {
		nameW = max(nameW, lipgloss.Width(o.title))
	}
	nameW += 6
	var lines []string
	for i, oi := range p.lineOpt[p.top:min(p.top+p.h, p.n)] {
		if oi < 0 {
			line := ""
			if next := p.lineOpt[p.top+i+1]; next >= 0 {
				head := " " + sSection.Render(vis[next].section) + " "
				line = head + sBorder.Render(strings.Repeat("─", max(inner-lipgloss.Width(head)-1, 0)))
			}
			lines = append(lines, line)
			continue
		}
		o := vis[oi]
		sel := oi == cur
		val := p.value(m, o, sel)
		if sel && p.editing {
			val = p.input.View()
		}
		name := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Fg)).Render(o.title)
		row := fit("   "+name, nameW) + val
		if sel {
			row = highlight(row, inner, sTitle)
		}
		lines = append(lines, fit(row, inner))
	}

	o := vis[cur]
	foot := []string{sBorder.Render(strings.Repeat("─", inner))}
	help := wrap(strings.ReplaceAll(o.help, "<leader>", leader), inner-2)
	for len(help) < 2 {
		help = append(help, "")
	}
	foot = append(foot, " "+sBold.Render(o.title)+sDim.Render("  "+o.key), " "+sDim.Render(help[0]))
	if len(help) > 1 && help[1] != "" {
		foot[len(foot)-1] += "\n " + sDim.Render(help[1])
	}
	if p.err != "" && p.errAt == p.cur {
		foot = append(foot, " "+sErr.Render(p.err))
	}
	keys := "j/k move · h/l change · enter edit · click to select, again to change · esc close"
	if p.editing {
		keys = "enter save · esc cancel"
	}
	foot = append(foot, " "+sDim.Render(keys))
	scroll := ""
	if p.n > p.h {
		scroll = sDim.Render(fmt.Sprintf("  %d/%d", cur+1, len(vis)))
	}
	return popBox("Options"+sDim.Render("  prowl "+core.BuildVersion())+scroll, strings.Join(lines, "\n")+"\n"+strings.Join(foot, "\n"), w, pal.Accent)
}

// ---- reviewers: which agents review, in order (the first is the main one)

type reviewersPopup struct {
	back  *optionsPopup
	o     option
	names []string // every agent, reviewers first in their order
	on    map[string]bool
	nav
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
	case "esc", "q", "ctrl+c", "h":
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
		sDim.Render(" "+leader+"cc runs every checked agent. The main one is first;") + "\n" +
		sDim.Render(" it reviews alone with "+leader+"cC and its summary is posted.")
	return popBox("Reviewers", strings.Join(rows, "\n")+"\n\n"+help, w, pal.Accent)
}

func (p *reviewersPopup) list(*Model) (*nav, int) {
	p.setLen(len(p.names), len(p.names))
	return &p.nav, 3
}

type serviceMsg struct {
	on   bool
	what string
	err  error
}
