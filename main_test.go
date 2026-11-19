package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/logs"
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
