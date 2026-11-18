package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
