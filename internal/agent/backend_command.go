package agent

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// commandBackend runs any program: the prompt goes in on stdin and the review
// JSON comes out on stdout. It has no sessions, so every turn carries the
// whole context, and there is no chat.
//
// The program gets PROWL_SCHEMA (a file with the JSON Schema), PROWL_MODEL
// (the configured model, may be empty) and runs in the PR's worktree.
//
// login and status are the optional login_command and status_command, so the
// Accounts popup and the logout warnings work for any agent.
type commandBackend struct{ command, login, status string }

func (commandBackend) Name() string                              { return "command" }
func (commandBackend) Title() string                             { return "Command" }
func (commandBackend) Resumes() bool                             { return false }
func (commandBackend) ChatQuit() string                          { return "" }
func (commandBackend) Models() []string                          { return []string{""} }
func (commandBackend) SkillDirs() []string                       { return nil }
func (commandBackend) Chat(_, _, _ string, _ []string) *exec.Cmd { return nil }

func (b commandBackend) Check() error {
	if strings.TrimSpace(b.command) == "" {
		return fmt.Errorf("the command reviewer needs [agent] command set")
	}
	return nil
}

func shell(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", command)
	}
	return exec.Command("sh", "-c", command)
}

func (b commandBackend) Review(t Turn) (*result, string, error) {
	if err := b.Check(); err != nil {
		return nil, "", err
	}
	tmp, err := os.MkdirTemp("", "prowl-agent-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmp)
	schemaPath := filepath.Join(tmp, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(t.schema()), 0o600); err != nil {
		return nil, "", err
	}
	t.setModel(t.Model)
	t.activity("running " + oneLine(b.command, 40))
	cmd := shell(b.command)
	cmd.Dir = t.Dir
	cmd.Env = append(CleanEnv(), "PROWL_SCHEMA="+schemaPath, "PROWL_MODEL="+t.Model)
	cmd.Stdin = strings.NewReader(t.Prompt + "\n\nReply with only JSON matching this schema, no other text:\n" + t.schema() + "\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("%s: %w", oneLine(b.command, 40), err)
	}
	res, err := parseResult(out.String())
	return res, "", err
}
