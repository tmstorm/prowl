package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tmstorm/prowl/docs"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/platform"
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

type DocsInput struct {
	Topic string `json:"topic,omitempty" jsonschema:"a page, a heading, a key, a setting or a bucket, e.g. config, Running tests, ]f, stale_after, re-check; other words return the lines that mention them; empty lists the pages"`
}

type DocsOutput struct {
	Text string `json:"text" jsonschema:"Markdown from prowl's docs"`
}

// ToolServer is the MCP server a review agent starts as `prowl agent-tools`. Its
// run tool runs a command in a container of the repo's image, on a copy of
// dir and nothing else of yours: no home, no environment, no credentials. It
// has the network, which module downloads need. The command stops itself at
// runTimeout, so a container left by a killed tool server ends too. dir
// is mounted read-only, so a PR's tests can't change files that git on your
// machine reads later, such as the worktree's .git. local is a review of your
// own checkout: it gets a cache of its own, so a PR can't plant code in a
// module that your next local review runs. chat is the review's chat, which
// also gets a docs tool that reads prowl's own docs.
func ToolServer(cfg config.Config, repo, dir string, local, chat bool) (*mcp.Server, error) {
	if !canRun(cfg, repo) && !chat {
		return nil, fmt.Errorf("agent tools: no run tool for %s: no image in [agent.run_images], or no docker", repo)
	}
	image := cfg.Agent.RunImage(repo)
	trust := "pr"
	if local {
		trust = "local"
	}
	cache := filepath.Join(runCacheRoot(), filepath.FromSlash(repo), trust)
	s := mcp.NewServer(&mcp.Implementation{Name: "prowl"}, nil)
	if chat {
		mcp.AddTool(s, &mcp.Tool{
			Name: "docs",
			Description: "Read prowl's own docs, for the version you run in: what its keys, settings, inbox buckets and review features do. " +
				"Use it when asked how prowl works, rather than answering from memory.",
		}, func(_ context.Context, _ *mcp.CallToolRequest, in DocsInput) (*mcp.CallToolResult, DocsOutput, error) {
			return nil, DocsOutput{Text: docs.Text(in.Topic)}, nil
		})
	}
	if !canRun(cfg, repo) {
		return s, nil
	}
	if err := markUsed(cache); err != nil {
		return nil, fmt.Errorf("agent tools: %w", err)
	}
	go pruneRunCaches(cfg)
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
	if err := markUsed(cache); err != nil {
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

func runCacheRoot() string { return filepath.Join(config.DataDir(), "cache") }

// runCacheInUse is how long after its last run a cache may still belong to a
// review that is running, so pruning leaves it.
const runCacheInUse = time.Hour

type runCache struct {
	dir  string
	size int64
	used time.Time
}

// pruneRunCaches keeps the run tool's caches (cache/owner/repo/{pr,local})
// under run_cache_gb, deleting whole caches, least recently used first. It
// first deletes anything else under cache/, such as an earlier layout's
// caches. A cache's last use is the mtime of its .used file, which sits beside
// the cache so a PR's commands can't change it. A cache from before .used
// files goes by its own mtime and its entries'. Each cache's last use is read
// again just before it goes, since a review may have started on it meanwhile.
func pruneRunCaches(cfg config.Config) {
	root := runCacheRoot()
	_ = platform.RemoveAll(filepath.Join(root, ".trash"))
	var caches []runCache
	var total int64
	owners, _ := os.ReadDir(root)
	for _, o := range owners {
		op := filepath.Join(root, o.Name())
		if o.Name() == ".trash" {
			continue
		}
		if !o.IsDir() {
			removeCache(op)
			continue
		}
		repos, _ := os.ReadDir(op)
		for _, r := range repos {
			rp := filepath.Join(op, r.Name())
			if !r.IsDir() {
				removeCache(rp)
				continue
			}
			entries, _ := os.ReadDir(rp)
			for _, e := range entries {
				p := filepath.Join(rp, e.Name())
				switch name := strings.TrimSuffix(e.Name(), ".used"); {
				case name != "pr" && name != "local":
					removeCache(p)
				case e.IsDir():
					c := runCache{dir: p, size: dirSize(p), used: lastUse(p)}
					caches = append(caches, c)
					total += c.size
				}
			}
			_ = os.Remove(rp)
		}
		_ = os.Remove(op)
	}
	limit := int64(cfg.Agent.RunCacheGB) << 30
	if limit <= 0 {
		return
	}
	slices.SortFunc(caches, func(a, b runCache) int { return a.used.Compare(b.used) })
	for _, c := range caches {
		if total <= limit {
			return
		}
		if time.Since(lastUse(c.dir)) < runCacheInUse {
			continue
		}
		removeCache(c.dir)
		_ = os.Remove(c.dir + ".used")
		total -= c.size
	}
}

// markUsed creates cache and records that a review uses it now.
func markUsed(cache string) error {
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(cache+".used", nil, 0o600); err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(cache+".used", now, now)
}

func lastUse(dir string) time.Time {
	if fi, err := os.Stat(dir + ".used"); err == nil {
		return fi.ModTime()
	}
	var t time.Time
	if fi, err := os.Stat(dir); err == nil {
		t = fi.ModTime()
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// removeCache moves path into cache/.trash before deleting it, so a prowl
// killed partway leaves no half-deleted cache for the next run to build from.
func removeCache(path string) {
	trash := filepath.Join(runCacheRoot(), ".trash", strconv.FormatInt(time.Now().UnixNano(), 36))
	if os.MkdirAll(filepath.Dir(trash), 0o700) != nil || os.Rename(path, trash) != nil {
		return
	}
	_ = platform.RemoveAll(trash)
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
