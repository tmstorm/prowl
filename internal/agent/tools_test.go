package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
)

func runTool(t *testing.T, ctx context.Context, repo, dir string, local bool, command string) (agent.RunOutput, error) {
	t.Helper()
	cfg := config.Default()
	cfg.Agent.RunImages = map[string]string{"*": "busybox:1.37"}
	s, err := agent.ToolServer(cfg, repo, dir, local)
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = s.Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "run", Arguments: map[string]any{"command": command}})
	if err != nil {
		return agent.RunOutput{}, err
	}
	var out agent.RunOutput
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

// The run tool runs in a container on a copy of the repo: the exit code and
// output come back, the host's environment stays out, nothing it does reaches
// the repo on disk, and neither a review of your own checkout nor another
// repo shares a cache with a PR review.
func TestRunTool(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil || runtime.GOOS == "windows" {
		t.Skip("needs docker with Linux containers")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "host-secret")
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}

	out, err := runTool(t, ctx, "acme/api", dir, false, `test -e .git && echo "copied .git"; cat go.mod; echo "token=$GH_TOKEN home=$HOME"; ls ~/.ssh 2>&1; `+
		`echo "gitdir: evil" > .git && rm go.mod && touch made /cache/from-pr && echo changed; exit 3`)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode != 3 || !strings.Contains(out.Output, "module x") || strings.Contains(out.Output, "host-secret") ||
		!strings.Contains(out.Output, "home=/cache/home") || !strings.Contains(out.Output, "changed") || strings.Contains(out.Output, "copied .git") {
		t.Fatalf("run: %+v", out)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 || entries[0].Name() != ".git" || entries[1].Name() != "go.mod" {
		t.Fatalf("the run changed the repo on disk: %v", entries)
	}

	out, err = runTool(t, ctx, "acme/api", dir, true, `test -e /cache/from-pr`)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode == 0 {
		t.Fatalf("a local review sees what a PR review left in the cache: %+v", out)
	}

	if _, err := runTool(t, ctx, "acme-web/api", dir, false, `touch /cache/from-acme-web`); err != nil {
		t.Fatal(err)
	}
	out, err = runTool(t, ctx, "acme/web-api", dir, false, `test -e /cache/from-acme-web`)
	if err != nil {
		t.Fatal(err)
	}
	if out.ExitCode == 0 {
		t.Fatalf("acme/web-api shares a cache with acme-web/api: %+v", out)
	}

	out, err = runTool(t, ctx, "acme/api", dir, false, `yes | head -c 100000`)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Output) > 17<<10 || !strings.HasPrefix(out.Output, "[first ") {
		t.Fatalf("output not cut to its end: %d bytes, %q", len(out.Output), out.Output[:40])
	}
}

// A call the agent gives up on stops its container.
func TestRunToolCancel(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil || runtime.GOOS == "windows" {
		t.Skip("needs docker with Linux containers")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := runTool(t, ctx, "acme/api", t.TempDir(), false, `sleep 60`); err == nil {
		t.Fatal("a cancelled call succeeded")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		ps, _ := exec.Command("docker", "ps", "-q", "--filter", "ancestor=busybox:1.37", "--filter", "name=prowl-run-").Output()
		if len(strings.TrimSpace(string(ps))) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("container still running: %s", ps)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// A run container left past its timeout, by a `prowl agent-tools` that was killed,
// goes at the next prune; one still in its time stays.
func TestPruneRunContainers(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil || runtime.GOOS == "windows" {
		t.Skip("needs docker with Linux containers")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	start := func(until int64) string {
		id, err := exec.Command("docker", "run", "-d", "--rm", "--label", fmt.Sprintf("prowl.run.until=%d", until), "busybox:1.37", "sleep", "60").Output()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", strings.TrimSpace(string(id))).Run() })
		return strings.TrimSpace(string(id))
	}
	expired, live := start(time.Now().Add(-time.Minute).Unix()), start(time.Now().Add(time.Hour).Unix())
	agent.PruneOrphans(config.Default(), func(string, int) bool { return true })
	ps, _ := exec.Command("docker", "ps", "-q", "--no-trunc").Output()
	if strings.Contains(string(ps), expired) || !strings.Contains(string(ps), live) {
		t.Fatalf("after prune: %s", ps)
	}
}
