package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/store"
)

const review = `{"summary":"ok","verdict":"comment","findings":[{"severity":"nit","path":"a.go","line":3,"title":"t","body":"b","rule":""}],"note":"I could not check the repo settings.","guidelines_read":["AGENTS.md"],"skills_used":[]}`

// fakeBin puts an executable script named name first on PATH. The script
// records its arguments and stdin next to itself.
func fakeBin(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$(dirname \"$0\")/args\"\ncat > \"$(dirname \"$0\")/stdin\"\n" + script
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func read(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func backend(t *testing.T, name string, configure ...func(*config.Config)) agent.Backend {
	t.Helper()
	cfg := config.Default()
	for _, f := range configure {
		f(&cfg)
	}
	be, err := agent.For(cfg, name)
	if err != nil {
		t.Fatal(err)
	}
	return be
}

// Codex: JSONL events, the review from the final agent message, the thread
// ID as the session, read-only sandbox, and resume by ID.
func TestCodexReview(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	_ = os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.docs]\ncommand = \"docs-server\"\n[mcp_servers.prowl]\ncommand = \"prowl\"\nargs = [\"mcp\"]\n"), 0o600)
	dir := fakeBin(t, "codex", `
echo '{"type":"thread.started","thread_id":"th-1"}'
echo '{"type":"item.started","item":{"type":"command_execution","command":"bash -lc '"'"'git diff origin/main...HEAD'"'"'"}}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"`+strings.ReplaceAll(review, `"`, `\"`)+`"}}'
echo '{"type":"turn.completed"}'
`)
	be := backend(t, "codex")
	var acts []string
	res, sid, err := be.Review(agent.Turn{Dir: t.TempDir(), Prompt: "review it", Model: "gpt-x",
		Activity: func(s string) { acts = append(acts, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if sid != "th-1" || len(res.Findings) != 1 || res.Findings[0].Path != "a.go" {
		t.Fatalf("got session %q, review %+v", sid, res)
	}
	args := read(filepath.Join(dir, "args"))
	for _, want := range []string{"exec", "--json", "--output-schema", `sandbox_mode="read-only"`, "-m\ngpt-x", "\n-\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("codex args missing %q:\n%s", want, args)
		}
	}
	if read(filepath.Join(dir, "stdin")) != "review it" {
		t.Error("prompt not sent on stdin")
	}
	if len(acts) == 0 || acts[0] != "running git diff origin/main...HEAD" {
		t.Errorf("activity = %q", acts)
	}
	if strings.Contains(args, "mcp_servers.prowl.command") || !strings.Contains(args, "mcp_servers.docs.enabled=false") || !strings.Contains(args, "mcp_servers.prowl.enabled=false") {
		t.Errorf("a review without tools loads no MCP server, yours included:\n%s", args)
	}
	if _, _, err := be.Review(agent.Turn{Dir: t.TempDir(), Prompt: "verify", Session: "th-1",
		ToolServer: []string{"/bin/prowl", "agent-tools", "o/api", `C:\wt`}}); err != nil {
		t.Fatal(err)
	}
	if args := read(filepath.Join(dir, "args")); !strings.HasPrefix(args, "exec\nresume\n") || !strings.Contains(args, "\nth-1\n-\n") {
		t.Errorf("resume args wrong:\n%s", args)
	}
	if args := read(filepath.Join(dir, "args")); !strings.Contains(args, `mcp_servers.prowl.args=["agent-tools","o/api","C:\\wt"]`) || !strings.Contains(args, `mcp_servers.prowl.tools.run.approval_mode="approve"`) || !strings.Contains(args, "mcp_servers.prowl.tool_timeout_sec=660") ||
		strings.Contains(args, "mcp_servers.prowl.enabled=false") || !strings.Contains(args, "mcp_servers.prowl.enabled=true") {
		t.Errorf("the tool server and its approved run tool should reach codex:\n%s", args)
	}
}

// A failed Codex turn reports its error instead of an empty review.
func TestCodexError(t *testing.T) {
	fakeBin(t, "codex", `
echo '{"type":"thread.started","thread_id":"th-2"}'
echo '{"type":"turn.failed","error":{"message":"401 Unauthorized"}}'
exit 1
`)
	_, _, err := backend(t, "codex").Review(agent.Turn{Dir: t.TempDir(), Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("want the turn's error, got %v", err)
	}
}

// Claude: stream-json with structured_output, model from init, resume flag.
func TestClaudeReview(t *testing.T) {
	dir := fakeBin(t, "claude", `
echo '{"type":"system","subtype":"init","model":"claude-test-1"}'
echo '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}'
echo '{"type":"result","session_id":"s-1","structured_output":`+review+`}'
`)
	var model string
	res, sid, err := backend(t, "claude").Review(agent.Turn{Dir: t.TempDir(), Prompt: "p", Session: "s-0", SetModel: func(m string) { model = m }})
	if err != nil {
		t.Fatal(err)
	}
	if sid != "s-1" || model != "claude-test-1" || len(res.Findings) != 1 {
		t.Fatalf("sid %q model %q review %+v", sid, model, res)
	}
	if args := read(filepath.Join(dir, "args")); !strings.Contains(args, "--resume\ns-0") || !strings.Contains(args, "--disallowedTools") {
		t.Errorf("claude args:\n%s", args)
	}
}

// Command: any program, prompt on stdin, JSON on stdout (fenced is fine),
// and its login through status_command.
func TestCommandAgent(t *testing.T) {
	be := backend(t, "command", func(c *config.Config) {
		c.Agent.Command = "cat > /dev/null; echo 'Here you go:'; echo '```json'; echo '" + review + "'; echo '```'"
		c.Agent.StatusCommand = `echo '{"loggedIn":true,"account":"me","expiresAt":"2030-01-01T00:00:00Z"}'`
		c.Agent.LoginCommand = "true"
	})
	res, sid, err := be.Review(agent.Turn{Dir: t.TempDir(), Prompt: "p"})
	if err != nil || sid != "" || len(res.Findings) != 1 {
		t.Fatalf("sid %q review %+v err %v", sid, res, err)
	}
	if a := be.Auth(); !a.LoggedIn || a.Account != "me" || a.Expires.Year() != 2030 || be.Login() == nil {
		t.Fatalf("auth %+v", a)
	}
	if err := backend(t, "command").Check(); err == nil {
		t.Error("an empty command must be reported")
	}
	if a := backend(t, "command", func(c *config.Config) { c.Agent.StatusCommand = "exit 1" }).Auth(); !a.Known || a.LoggedIn {
		t.Fatalf("exit 1 means logged out: %+v", a)
	}
}

func TestFor(t *testing.T) {
	for _, n := range agent.Names() {
		if be := backend(t, n); be.Name() != n {
			t.Fatalf("For(%q) = %s", n, be.Name())
		}
	}
	if be := backend(t, ""); be.Name() != "claude" {
		t.Error("the default agent is claude")
	}
	if _, err := agent.For(config.Default(), "nope"); err == nil {
		t.Error("unknown agents must be an error")
	}
}

func TestAuthStatus(t *testing.T) {
	fakeBin(t, "claude", `echo '{"loggedIn":true,"email":"a@b.c","orgName":"Org","loginExpiresAt":"2030-01-02T03:04:05.000Z"}'`)
	if a := backend(t, "claude").Auth(); !a.Known || !a.LoggedIn || a.Account != "a@b.c (Org)" || a.Expires.Year() != 2030 {
		t.Fatalf("claude auth = %+v", a)
	}
	fakeBin(t, "claude", `echo '{"loggedIn":false}'; exit 1`)
	if a := backend(t, "claude").Auth(); !a.Known || a.LoggedIn {
		t.Fatalf("logged-out claude = %+v", a)
	}
	fakeBin(t, "codex", `echo 'Not logged in'; exit 1`)
	if a := backend(t, "codex").Auth(); !a.Known || a.LoggedIn {
		t.Fatalf("logged-out codex = %+v", a)
	}
	fakeBin(t, "codex", `echo 'Logged in using ChatGPT'`)
	if a := backend(t, "codex").Auth(); !a.LoggedIn || a.Account != "ChatGPT" {
		t.Fatalf("codex = %+v", a)
	}
}

// Codex's models come from its own catalog, minus the ones it hides.
func TestCodexModels(t *testing.T) {
	fakeBin(t, "codex", `echo '{"models":[{"slug":"gpt-a","visibility":"list"},{"slug":"hidden","visibility":"hide"},{"slug":"gpt-b","visibility":"list"}]}'`)
	if got := strings.Join(backend(t, "codex").Models(), ","); got != ",gpt-a,gpt-b" {
		t.Fatalf("models = %q", got)
	}
	fakeBin(t, "codex", `exit 1`)
	if got := backend(t, "codex").Models(); len(got) != 1 || got[0] != "" {
		t.Fatalf("a failing catalog should leave only the default, got %q", got)
	}
}

// Posting combines agents' findings: excluded ones out, a problem two agents
// raised kept once at the higher severity, one agent's findings never merged.
func TestCombine(t *testing.T) {
	runs := []*store.AgentReview{
		{Status: "done", Findings: []store.Finding{{Severity: "should-fix", Path: "a.go", Line: 11, Title: "x"}, {Path: "c.go", Line: 1, Excluded: true}}},
		{Status: "done", Findings: []store.Finding{{Severity: "must-fix", Path: "a.go", Line: 9, Title: "y"}, {Severity: "nit", Path: "b.go", Line: 1}}},
		{Status: "running", Findings: []store.Finding{{Path: "z.go", Line: 1}}},
	}
	got := agent.Combine(runs, false)
	if len(got) != 2 || got[0].Path != "a.go" || got[0].Severity != "must-fix" || got[1].Path != "b.go" {
		t.Fatalf("combined = %+v", got)
	}
	if len(agent.Combine(runs, true)) != 3 {
		t.Fatal("all includes excluded findings")
	}
	one := []*store.AgentReview{{Status: "done", Findings: []store.Finding{{Path: "a.go", Line: 10}, {Path: "a.go", Line: 12}}}}
	if len(agent.Combine(one, false)) != 2 {
		t.Fatal("two findings from one agent are two findings")
	}
}

// prowl's own Claude session markers never leak into the agents it starts.
func TestCleanEnv(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("CLAUDE_CODE_MESSAGING_X", "1")
	t.Setenv("KEEP_ME", "1")
	env := strings.Join(agent.CleanEnv(), "\n")
	if strings.Contains(env, "CLAUDECODE=") || strings.Contains(env, "CLAUDE_CODE_CHILD_SESSION=") || strings.Contains(env, "CLAUDE_CODE_MESSAGING_X=") || !strings.Contains(env, "KEEP_ME=1") {
		t.Fatalf("env:\n%s", env)
	}
}
