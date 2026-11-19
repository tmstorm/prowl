package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
	"github.com/tmstorm/prowl/internal/logs"
	"github.com/tmstorm/prowl/internal/store"
)

// -h on any command prints its section of the prowl command's docs instead
// of running it, and help and the docs neither read nor write the config or
// the data directory, so they work with a broken config.
func TestHelp(t *testing.T) {
	home := t.TempDir()
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(v, filepath.Join(home, v))
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"update", "--help"}, "## prowl update"},
		{[]string{"review", "-h"}, "## prowl review"},
		{[]string{"findings", "123", "--help"}, "## prowl findings"},
		{[]string{"--help"}, "prowl docs cli explains every command"},
		{[]string{"docs", "stale_after"}, "`stale_after`"},
	} {
		if out := stdout(t, func() error { return run(c.args) }); !strings.Contains(out, c.want) {
			t.Errorf("prowl %s printed %q, want %q", strings.Join(c.args, " "), out, c.want)
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("help wrote %v", entries)
	}
}

// stdout is what f prints, failing the test if f fails.
func stdout(t *testing.T, f func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	ferr := f()
	os.Stdout = old
	w.Close()
	b, _ := io.ReadAll(r)
	if ferr != nil {
		t.Fatal(ferr)
	}
	return string(b)
}

// Every command reads its flags the same way: before or after its
// arguments, with the value after a space or an =.
func TestFlags(t *testing.T) {
	home := t.TempDir()
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(v, filepath.Join(home, v))
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"edits", "--since=12h", "--repo", "api"}, "no posted AI reviews in that time"},
		{[]string{"edits", "--since", "soon"}, "want e.g. 30d or 12h"},
		{[]string{"findings", "--agent=codex", "o/api#1", "--all"}, "no finished review for o/api#1 yet"},
		{[]string{"findings", "--agent"}, "flag needs an argument"},
		{[]string{"slack", "now"}, `unexpected argument "now"`},
		{[]string{"uninstall", "--force"}, "not defined: -force"},
	} {
		if err := run(c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("prowl %s: %v, want %q", strings.Join(c.args, " "), err, c.want)
		}
	}
}

// prowl list --json prints what needs you as one JSON array a status bar can
// read, and an empty array, not null, when nothing does.
func TestListJSON(t *testing.T) {
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME"} {
		t.Setenv(v, t.TempDir())
	}
	t.Setenv("PATH", t.TempDir())
	for _, c := range []struct {
		prs  []gh.PR
		want string
	}{
		{nil, "[]\n"},
		{[]gh.PR{ghtest.Requested(ghtest.PR("api", 1, "fix: retry on timeout", "bob"), "me"), ghtest.PR("web", 2, "feat: dark mode", "carol")}, ""},
	} {
		client, err := (&ghtest.GitHub{PRs: c.prs}).Client()
		if err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = list(context.Background(), config.Default(), client, st, []string{"--json"}, &out)
		st.Close()
		if err != nil {
			t.Fatal(err)
		}
		if c.want != "" {
			if out.String() != c.want {
				t.Errorf("no PRs: got %q, want %q", out.String(), c.want)
			}
			continue
		}
		var prs []listedPR
		if err := json.Unmarshal(out.Bytes(), &prs); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out.String())
		}
		if len(prs) != 1 {
			t.Fatalf("got %d PRs, want only the one requesting a review: %+v", len(prs), prs)
		}
		got := prs[0]
		if got.PR != "o/api#1" || got.Bucket != "needs review" || got.Author != "bob" || got.URL != "https://github.com/o/api/pull/1" || got.Since.IsZero() {
			t.Errorf("got %+v", got)
		}
	}
}

// prowl logs prints the end of the log, the lines of the last --since, or
// one run's lines with what its agent printed.
func TestLogs(t *testing.T) {
	home := t.TempDir()
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(v, filepath.Join(home, v))
	}
	at := func(ago time.Duration) string { return time.Now().Add(-ago).Format("2006-01-02T15:04:05") }
	log := "time=" + at(2*time.Hour) + ` level=INFO msg=synced proc=watch pid=1 prs=3` + "\n" +
		"time=" + at(10*time.Minute) + ` level=ERROR msg="review failed" proc=agent-run pid=2 run=412 pr=o/api#7` + "\n" +
		"time=" + at(5*time.Minute) + ` level=INFO msg="review done" proc=agent-run pid=3 run=4120 pr=o/api#8` + "\n"
	if err := os.MkdirAll(filepath.Dir(logs.RunPath("412")), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(logs.Path(), []byte(log), 0o600)
	_ = os.WriteFile(logs.RunPath("412"), []byte("claude: rate limited\n"), 0o600)
	for _, c := range []struct {
		args      []string
		want, not []string
	}{
		{[]string{"logs"}, []string{"prs=3", "run=412 ", "run=4120"}, nil},
		{[]string{"logs", "--since", "1h"}, []string{"run=412 ", "run=4120"}, []string{"prs=3"}},
		{[]string{"logs", "--since", "1d"}, []string{"prs=3", "run=412 ", "run=4120"}, nil},
		{[]string{"logs", "--run", "412"}, []string{`msg="review failed"`, "what run 412 printed", "claude: rate limited"}, []string{"prs=3", "run=4120"}},
	} {
		out := stdout(t, func() error { return run(c.args) })
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("prowl %s is missing %q:\n%s", strings.Join(c.args, " "), w, out)
			}
		}
		for _, n := range c.not {
			if strings.Contains(out, n) {
				t.Errorf("prowl %s printed %q:\n%s", strings.Join(c.args, " "), n, out)
			}
		}
	}
}

// The perf tip shows once per repo, and never for a repo perf runs on.
func TestPerfTip(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Default()
	cfg.Agent.Extensions = []config.Extension{{Name: "perf", Repos: []string{"acme/fast"}}}
	co := core.New(cfg, nil, st)
	if tip := perfTip(cfg, co, "local:acme/web@feat/x"); !strings.Contains(tip, "measure acme/web's benchmarks") {
		t.Fatalf("first tip: %q", tip)
	}
	if tip := perfTip(cfg, co, "local:acme/web@feat/y"); tip != "" {
		t.Fatalf("the tip showed twice: %q", tip)
	}
	if tip := perfTip(cfg, co, "local:acme/fast@main"); tip != "" {
		t.Fatalf("the tip showed where perf runs: %q", tip)
	}
}
