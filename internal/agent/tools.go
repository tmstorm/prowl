package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tmstorm/prowl/internal/config"
)

var runTimeout = 10 * time.Minute

const (
	runOutputLimit = 16 << 10
	runUntilLabel  = "prowl.run.until"
)

type RunInput struct {
	Command string `json:"command" jsonschema:"a shell command to run at the root of the repo, e.g. go test ./..."`
}

type RunOutput struct {
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out,omitempty"`
	Duration string `json:"duration"`
	Output   string `json:"output" jsonschema:"stdout and stderr together, only the end when it is long"`
}

// ToolServer is the MCP server a review agent starts as `prowl agent-tools`. Its
// run tool runs a command in a container of the repo's image, on a copy of
// dir and nothing else of yours: no home, no environment, no credentials. It
// has the network, which module downloads need. The command stops itself at
// runTimeout, so a container left by a killed tool server ends too. dir
// is mounted read-only, so a PR's tests can't change files that git on your
// machine reads later, such as the worktree's .git. local is a review of your
// own checkout: it gets a cache of its own, so a PR can't plant code in a
// module that your next local review runs.
func ToolServer(cfg config.Config, repo, dir string, local bool) (*mcp.Server, error) {
	image := cfg.Agent.RunImage(repo)
	if image == "" {
		return nil, fmt.Errorf("agent tools: no image for %s in [agent.run_images]", repo)
	}
	trust := "pr"
	if local {
		trust = "local"
	}
	cache := filepath.Join(config.DataDir(), "cache", filepath.FromSlash(repo), trust)
	s := mcp.NewServer(&mcp.Implementation{Name: "prowl"}, nil)
	mcp.AddTool(s, &mcp.Tool{
		Name: "run",
		Description: fmt.Sprintf("Run a shell command at the root of the repo, in a %s container with only a copy of the repo. "+
			"Use it to run the tests or build and check that the change works and that your findings hold. "+
			"git doesn't work in the container; use your own git commands for history and diffs. "+
			"Returns the exit code and the end of the output. Each call times out after %s.", image, runTimeout),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RunInput) (*mcp.CallToolResult, RunOutput, error) {
		out, err := runInContainer(ctx, image, dir, cache, in.Command)
		return nil, out, err
	})
	return s, nil
}

func runInContainer(ctx context.Context, image, dir, cache, command string) (RunOutput, error) {
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return RunOutput{}, fmt.Errorf("run: %w", err)
	}
	name := fmt.Sprintf("prowl-run-%d-%d", os.Getpid(), time.Now().UnixNano())
	until := strconv.FormatInt(time.Now().Add(runTimeout).Unix(), 10)
	args := []string{"run", "--rm", "--name", name, "--label", runUntilLabel + "=" + until,
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=4096",
		"-v", dir + ":/src:ro", "-v", cache + ":/cache",
		"-e", "HOME=/cache/home", "-e", "XDG_CACHE_HOME=/cache/xdg", "-e", "GOCACHE=/cache/go-build",
		"-e", "GOMODCACHE=/cache/go-mod", "-e", "GOFLAGS=-buildvcs=false", "-e", "npm_config_cache=/cache/npm"}
	if runtime.GOOS != "windows" {
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	}
	args = append(args, image, "sh", "-c", `mkdir -p /tmp/work && tar -C /src --exclude=./.git -cf - . | tar -C /tmp/work -xof - && cd /tmp/work && timeout "$1" sh -c "$2"`,
		"sh", strconv.Itoa(int(runTimeout.Seconds())), command)

	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	var out tail
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Cancel = func() error {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		return cmd.Process.Kill()
	}
	start := time.Now()
	err := cmd.Run()
	res := RunOutput{Duration: time.Since(start).Round(100 * time.Millisecond).String(), Output: out.String(),
		TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded)}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		res.ExitCode = exit.ExitCode()
	} else if err != nil {
		return res, fmt.Errorf("run %q: %w", command, err)
	}
	return res, nil
}

// pruneRunContainers removes run containers past their timeout. They are
// left when `prowl agent-tools` is killed before it can stop them.
func pruneRunContainers() {
	if _, err := exec.LookPath("docker"); err != nil {
		return
	}
	out, err := exec.Command("docker", "ps", "-a", "--filter", "label="+runUntilLabel, "--format", `{{.ID}} {{.Label "`+runUntilLabel+`"}}`).Output()
	if err != nil {
		return
	}
	for line := range strings.Lines(string(out)) {
		id, until, _ := strings.Cut(strings.TrimSpace(line), " ")
		if u, err := strconv.ParseInt(until, 10, 64); err == nil && time.Now().Unix() > u {
			_ = exec.Command("docker", "rm", "-f", id).Run()
		}
	}
}

// tail keeps the last runOutputLimit bytes written to it. exec calls Write
// from one goroutine when Stdout and Stderr are the same writer.
type tail struct {
	b   []byte
	cut int
}

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if over := len(t.b) - runOutputLimit; over > 0 {
		t.cut += over
		t.b = t.b[over:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	s := strings.ToValidUTF8(string(t.b), "")
	if t.cut > 0 {
		return fmt.Sprintf("[first %d bytes cut]\n%s", t.cut, s)
	}
	return s
}
