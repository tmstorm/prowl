package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tmstorm/prowl/internal/config"
)

// Turn is one headless request to an agent.
type Turn struct {
	Dir         string   // the PR worktree; the agent runs here
	Prompt      string   // sent on stdin
	Session     string   // non-empty: continue this session
	Model       string   // "" = the agent's default
	ContextDirs []string // extra directories the agent may read
	Commands    []string // commands the agent may run, e.g. "go test"
	Schema      string   // the JSON Schema of the answer; "" is a review with every severity
	Activity    func(string)
	SetModel    func(string)
}

func (t Turn) schema() string {
	if t.Schema == "" {
		return Schema(config.Default().Agent.Severities())
	}
	return t.Schema
}

func (t Turn) activity(s string) {
	if t.Activity != nil {
		t.Activity(s)
	}
}

func (t Turn) setModel(s string) {
	if t.SetModel != nil && s != "" {
		t.SetModel(s)
	}
}

// Backend is a coding agent prowl can run reviews with.
type Backend interface {
	Name() string  // config name: claude, codex, command
	Title() string // shown in the UI: Claude, Codex, ...
	// Check reports why the agent can't run (not installed, not configured).
	Check() error
	// Review runs one turn and returns the structured review and the
	// session ID to resume.
	Review(t Turn) (*result, string, error)
	// Resumes is true when Review can continue a session. Without it prowl
	// resends the context with the earlier review on every turn.
	Resumes() bool
	// Chat is the interactive session for <leader>ci, or nil if the agent
	// has none.
	Chat(session, dir, model string) *exec.Cmd
	// ChatQuit is what ends the interactive session, e.g. "/exit".
	ChatQuit() string
	// Models are the models to pick from in the options menu, as the agent
	// itself lists them ("" = the agent's default comes first).
	Models() []string
	// SkillDirs are where the agent finds skills by itself. Empty: required
	// skills are put into the prompt instead.
	SkillDirs() []string
	// Auth reports whether the agent is logged in; Login is the interactive
	// command that logs it in (nil if prowl can't).
	Auth() Auth
	Login() *exec.Cmd
}

// Names lists the supported backends.
func Names() []string { return []string{"claude", "codex", "command"} }

// For returns the named backend ("" = the configured one).
func For(cfg config.Config, name string) (Backend, error) {
	if name == "" {
		name = cfg.Agent.Main()
	}
	switch name {
	case "", "claude":
		return claudeBackend{}, nil
	case "codex":
		return codexBackend{}, nil
	case "command":
		return commandBackend{command: cfg.Agent.Command, login: cfg.Agent.LoginCommand, status: cfg.Agent.StatusCommand}, nil
	}
	return nil, fmt.Errorf("unknown agent %q (want one of %s)", name, strings.Join(Names(), ", "))
}

// Title is the display name for a backend name, for reviews made by an agent
// that is no longer configured.
func Title(name string) string {
	switch name {
	case "", "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "command":
		return "Command"
	}
	return "Agent"
}

func needBinary(bin, url string) error {
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("`%s` is not on your PATH; install it from %s", bin, url)
	}
	return nil
}

// skillIn returns the directory holding the named skill's SKILL.md in one
// of dirs, or "".
func skillIn(name string, dirs []string) string {
	if _, short, ok := strings.Cut(name, ":"); ok {
		name = short
	}
	for _, d := range dirs {
		p := filepath.Join(d, name)
		if _, err := os.Stat(filepath.Join(p, "SKILL.md")); err == nil {
			return p
		}
	}
	return ""
}

func allSkillDirs() []string {
	return append(claudeBackend{}.SkillDirs(), codexBackend{}.SkillDirs()...)
}

// skillsSection tells the agent which skills to follow. A skill the agent
// can load itself is named; a skill installed only for another agent (say,
// a Claude skill when Codex reviews) is put into the prompt.
func skillsSection(be Backend, skills []string) string {
	native := be.SkillDirs()
	var b strings.Builder
	b.WriteString("## Skills\n\n")
	var use []string
	for _, s := range skills {
		if len(native) > 0 && (skillIn(s, native) != "" || skillIn(s, allSkillDirs()) == "") {
			use = append(use, s)
			continue
		}
		dir := skillIn(s, allSkillDirs())
		if dir == "" {
			fmt.Fprintf(&b, "(The skill %q is configured but was not found.)\n\n", s)
			continue
		}
		body, _ := os.ReadFile(filepath.Join(dir, "SKILL.md"))
		fmt.Fprintf(&b, "Follow the skill %q below. Files it refers to are in %s.\n\n<skill name=%q>\n%s\n</skill>\n\n",
			s, dir, s, strings.TrimSpace(stripFrontMatter(string(body))))
	}
	if len(use) > 0 {
		b.WriteString("Use these skills before writing the review and follow them: " + strings.Join(use, ", ") + ".\n")
	}
	if len(native) > 0 {
		b.WriteString("If any other skill available to you applies to this change (its language,\nframework, API design, testing, or the prose in docs and comments), use it.\n")
	}
	b.WriteString("List every skill you used in `skills_used`.\n\n")
	return b.String()
}

func stripFrontMatter(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return s
	}
	if i := strings.Index(s[4:], "\n---"); i >= 0 {
		return s[4+i+4:]
	}
	return s
}
