package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// claudeBackend runs Claude Code (`claude -p`).
type claudeBackend struct{}

func (claudeBackend) Name() string     { return "claude" }
func (claudeBackend) Title() string    { return "Claude" }
func (claudeBackend) Resumes() bool    { return true }
func (claudeBackend) ChatQuit() string { return "/exit" }

// Models are Claude Code's model aliases, which always point at the latest
// model of each family. Claude Code has no command that lists models.
func (claudeBackend) Models() []string { return []string{"", "opus", "fable", "sonnet", "haiku"} }

func (claudeBackend) Check() error {
	return needBinary("claude", "https://claude.com/claude-code")
}

func (claudeBackend) SkillDirs() []string {
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".claude")
	}
	return []string{filepath.Join(base, "skills")}
}

func (claudeBackend) Chat(session, dir, model string) *exec.Cmd {
	cmd := exec.Command("claude", "--resume", session, "--setting-sources", claudeSettings)
	cmd.Dir = dir
	cmd.Env = CleanEnv()
	return cmd
}

// Read-only: the review can look at anything in the worktree and run git and
// gh read commands, but not change files or reach the web. git's --output
// would write a file, so it is denied anywhere in those commands. Only your own
// Claude settings load: a PR's .claude/settings.json could otherwise add
// hooks and permissions that run on your machine during the review. No MCP
// server loads either, so a tool your settings allow can't act on what a PR
// says while nobody is watching.
const (
	claudeSettings   = "user"
	claudeAllowed    = "Read,Grep,Glob,Skill,Bash(git diff:*),Bash(git log:*),Bash(git show:*),Bash(git blame:*),Bash(git range-diff:*),Bash(git status:*),Bash(git ls-files:*),Bash(git merge-base:*),Bash(gh pr view:*),Bash(gh pr diff:*),Bash(gh pr checks:*),Bash(gh repo view:*),Bash(gh release view:*)"
	claudeDisallowed = "Edit,Write,NotebookEdit,WebFetch,WebSearch," +
		"Bash(git diff *--output*),Bash(git log *--output*),Bash(git show *--output*),Bash(git range-diff *--output*)"
)

// commandRules allows each command with any arguments: "go test" allows
// `go test ./...`.
func commandRules(commands []string) string {
	var b strings.Builder
	for _, c := range commands {
		b.WriteString(",Bash(" + strings.TrimSuffix(c, ":*") + ":*)")
	}
	return b.String()
}

// Review runs one `claude -p` turn. The prompt goes on stdin: --allowedTools
// and friends are variadic and would swallow a positional prompt.
func (claudeBackend) Review(t Turn) (*result, string, error) {
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--setting-sources", claudeSettings,
		"--strict-mcp-config", "--json-schema", t.schema(),
		"--allowedTools", claudeAllowed + commandRules(t.Commands),
		"--disallowedTools", claudeDisallowed,
	}
	if t.Model != "" {
		args = append(args, "--model", t.Model)
	}
	for _, d := range t.ContextDirs {
		args = append(args, "--add-dir", d)
	}
	if t.Session != "" {
		args = append(args, "--resume", t.Session)
	}
	cmd := exec.Command("claude", args...)
	cmd.Dir = t.Dir
	cmd.Env = CleanEnv()
	cmd.Stdin = strings.NewReader(t.Prompt)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start claude: %w", err)
	}

	var res *result
	var sessionID, errMsg string
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
			IsError   bool   `json:"is_error"`
			Result    string `json:"result"`
			Message   struct {
				Content []struct {
					Type  string          `json:"type"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
			StructuredOutput *result `json:"structured_output"`
			Model            string  `json:"model"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" {
				t.setModel(ev.Model)
			}
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_use" {
					t.activity(describeClaudeTool(c.Name, c.Input))
				}
			}
		case "result":
			sessionID = ev.SessionID
			res = ev.StructuredOutput
			if ev.IsError {
				errMsg = ev.Subtype + ": " + ev.Result
			}
		}
	}
	if err := cmd.Wait(); err != nil && res == nil {
		return nil, sessionID, fmt.Errorf("claude exited: %v %s", err, errMsg)
	}
	if res == nil {
		return nil, sessionID, fmt.Errorf("no structured review returned %s", errMsg)
	}
	return res, sessionID, nil
}

func describeClaudeTool(name string, input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	s := func(k string) string { v, _ := in[k].(string); return v }
	switch name {
	case "Read":
		return "reading " + s("file_path")
	case "Grep":
		return "searching " + s("pattern")
	case "Glob":
		return "listing " + s("pattern")
	case "Bash":
		return "running " + oneLine(s("command"), 60)
	case "Skill":
		return "using skill " + s("skill")
	case "StructuredOutput":
		return "writing review"
	}
	return name
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

// sessionMarkers are set by a running Claude Code session for its own child
// processes. A review or chat started by prowl is its own session, so it must
// not inherit them (CLAUDE_CODE_CHILD_SESSION, for one, turns off transcript
// saving, which breaks resume and chat).
var sessionMarkers = []string{"CLAUDECODE", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ID",
	"CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_MESSAGING_", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH",
	"CLAUDE_PID", "CLAUDE_EFFORT"}

// CleanEnv is the environment for launching an agent from prowl, without
// the markers of any agent session prowl itself was started from.
func CleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		skip := false
		for _, mk := range sessionMarkers {
			if name == mk || strings.HasSuffix(mk, "_") && strings.HasPrefix(name, mk) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	return out
}
