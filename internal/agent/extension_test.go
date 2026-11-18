package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
)

func runExtension(t *testing.T, command string, timeout time.Duration, in agent.ExtensionInput) (agent.ExtensionOutput, error) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the extensions are shell scripts")
	}
	e := agent.ExtensionFor(config.Extension{Name: "lint", Command: command, Repos: []string{"*"}, Timeout: config.Duration{Duration: timeout}})
	return e.Run(context.Background(), in)
}

// An extension runs in the PR's worktree with the input on stdin, and its
// output comes back as it printed it.
func TestExtensionRuns(t *testing.T) {
	head := t.TempDir()
	in := agent.ExtensionInput{Repo: "acme/web", HeadDir: head, Head: "abc", Base: "def", ChangedFiles: []string{"a.go"},
		Since: "fed", ChangedSince: []string{"a.go"}, Previous: []agent.ExtensionFinding{{Severity: "nit", Title: "old", Path: "a.go", Line: 2}}}
	finding := `{"severity":"should-fix","title":"Slow","body":"b","rule":"perf","path":"","line":0}`
	out, err := runExtension(t, `cat > input.json; pwd > pwd; printf '{"prompt":"p","summary":"s","findings":[`+finding+`]}'`, time.Minute, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Prompt != "p" || out.Summary != "s" || len(out.Findings) != 1 || out.Findings[0].Title != "Slow" || out.Findings[0].Rule != "perf" {
		t.Fatalf("output: %+v", out)
	}
	var got agent.ExtensionInput
	if err := json.Unmarshal([]byte(read(filepath.Join(head, "input.json"))), &got); err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("stdin: %+v %v", got, err)
	}
	if wd, _ := filepath.EvalSymlinks(strings.TrimSpace(read(filepath.Join(head, "pwd")))); wd != mustEval(t, head) {
		t.Fatalf("ran in %s, not the worktree", wd)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A failing extension's error says which one, how it ended, and the end of
// what it printed on stderr.
func TestExtensionFails(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{`echo working >&2; echo boom >&2; exit 3`, "lint: exit status 3\nworking\nboom"},
		{`echo not json`, "lint: its output isn't the extension JSON"},
		{`printf '{"findings":[{"severity":"high","title":"t"}]}'`, `lint: finding 1 has severity "high"; use must-fix, should-fix, nit`},
	} {
		_, err := runExtension(t, tc.command, time.Minute, agent.ExtensionInput{HeadDir: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", tc.command, err, tc.want)
		}
	}
}

// An extension past its timeout stops with its children, even one holding
// its output open, and says it timed out.
func TestExtensionTimesOut(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "late")
	start := time.Now()
	_, err := runExtension(t, `(sleep 2; touch `+marker+`) | cat`, 200*time.Millisecond, agent.ExtensionInput{HeadDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "lint: timed out after 200ms") {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %s to stop", d)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the extension's child kept running")
	}
}
