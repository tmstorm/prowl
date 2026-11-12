package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// codexBackend runs OpenAI's Codex CLI (`codex exec`).
type codexBackend struct{}

func (codexBackend) Name() string     { return "codex" }
func (codexBackend) Title() string    { return "Codex" }
func (codexBackend) Resumes() bool    { return true }
func (codexBackend) ChatQuit() string { return "/quit" }

// Models are the models Codex offers in its own picker, from its catalog.
func (codexBackend) Models() []string {
	out, err := exec.Command("codex", "debug", "models").Output()
	var cat struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	models := []string{""}
	if err != nil || json.Unmarshal(out, &cat) != nil {
		return models
	}
	for _, m := range cat.Models {
		if m.Visibility == "list" {
			models = append(models, m.Slug)
		}
	}
	return models
}

func (codexBackend) Check() error {
	return needBinary("codex", "https://github.com/openai/codex")
}

func codexHome() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

func (codexBackend) SkillDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(codexHome(), "skills"), filepath.Join(home, ".agents", "skills")}
}

func (codexBackend) Chat(session, dir, model string) *exec.Cmd {
	args := []string{"resume"}
	if model != "" {
		args = append(args, "-m", model)
	}
	cmd := exec.Command("codex", append(args, session)...)
	cmd.Dir = dir
	cmd.Env = CleanEnv()
	return cmd
}

// codexModel is the model Codex uses when none is given: the one in its
// config, if set.
func codexModel() string {
	var c struct {
		Model string `toml:"model"`
	}
	_, _ = toml.DecodeFile(filepath.Join(codexHome(), "config.toml"), &c)
	return c.Model
}

// Review runs one `codex exec` turn in the read-only sandbox, never asking
// for approval. The sandbox has no network, which is why prowl puts the PR's
// history in the prompt.
func (codexBackend) Review(t Turn) (*result, string, error) {
	tmp, err := os.MkdirTemp("", "prowl-codex-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(tmp)
	schemaPath, lastPath := filepath.Join(tmp, "schema.json"), filepath.Join(tmp, "last.json")
	if err := os.WriteFile(schemaPath, []byte(t.schema()), 0o600); err != nil {
		return nil, "", err
	}

	args := []string{"exec"}
	if t.Session != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--output-schema", schemaPath, "-o", lastPath,
		"-c", `sandbox_mode="read-only"`, "-c", `approval_policy="never"`)
	model := t.Model
	if model != "" {
		args = append(args, "-m", model)
	} else {
		model = codexModel()
	}
	if t.Session != "" {
		args = append(args, t.Session)
	}
	args = append(args, "-")
	t.setModel(model)

	cmd := exec.Command("codex", args...)
	cmd.Dir = t.Dir
	cmd.Env = CleanEnv()
	cmd.Stdin = strings.NewReader(t.Prompt)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start codex: %w", err)
	}
	var sessionID, last, errMsg string
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Message  string `json:"message"`
			Error    struct {
				Message string `json:"message"`
			} `json:"error"`
			Item struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Command string `json:"command"`
				Server  string `json:"server"`
				Tool    string `json:"tool"`
				Query   string `json:"query"`
			} `json:"item"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			sessionID = ev.ThreadID
		case "item.started":
			switch ev.Item.Type {
			case "command_execution":
				t.activity("running " + oneLine(stripShell(ev.Item.Command), 60))
			case "mcp_tool_call":
				t.activity("using " + ev.Item.Server + " " + ev.Item.Tool)
			case "web_search":
				t.activity("searching " + oneLine(ev.Item.Query, 60))
			}
		case "item.completed":
			switch ev.Item.Type {
			case "agent_message":
				last = ev.Item.Text
			case "reasoning":
				t.activity("thinking")
			}
		case "turn.failed":
			errMsg = ev.Error.Message
		case "error":
			errMsg = ev.Message
		}
	}
	waitErr := cmd.Wait()
	if b, err := os.ReadFile(lastPath); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		last = string(b)
	}
	if t.Session != "" && sessionID == "" {
		sessionID = t.Session
	}
	if last == "" {
		if errMsg == "" && waitErr != nil {
			errMsg = waitErr.Error()
		}
		return nil, sessionID, fmt.Errorf("codex returned no review: %s", errMsg)
	}
	res, err := parseResult(last)
	if err != nil {
		return nil, sessionID, fmt.Errorf("codex: %w", err)
	}
	return res, sessionID, nil
}

// stripShell shows the command inside a `bash -lc '…'` wrapper.
func stripShell(cmd string) string {
	for _, p := range []string{"bash -lc ", "/bin/bash -lc ", "sh -c ", "zsh -lc "} {
		if rest, ok := strings.CutPrefix(cmd, p); ok {
			return strings.Trim(rest, `'"`)
		}
	}
	return cmd
}

// parseResult reads a review from an agent's final message: plain JSON, or
// JSON in a code fence or after some prose.
func parseResult(s string) (*result, error) {
	s = strings.TrimSpace(s)
	var r result
	if json.Unmarshal([]byte(s), &r) == nil {
		return &r, nil
	}
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		if err := json.Unmarshal([]byte(s[i:j+1]), &r); err == nil {
			return &r, nil
		}
	}
	return nil, fmt.Errorf("the final message is not a review in prowl's JSON format: %s", oneLine(s, 200))
}
