package logs_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tmstorm/prowl/internal/logs"
)

// Every line says when, how bad, which process and what, as key=value
// pairs, appended to one file.
func TestOpen(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", os.Getenv("XDG_DATA_HOME"))
	defer slog.SetDefault(slog.Default())
	logs.Open("watch")
	slog.Info("synced", "prs", 12, "took", 1500*time.Millisecond)
	slog.Debug("hidden without PROWL_DEBUG")
	b, err := os.ReadFile(logs.Path())
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`^time=\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d level=INFO msg=synced proc=watch pid=\d+ prs=12 took=1.5s\n$`)
	if !line.Match(b) {
		t.Fatalf("log: %q", b)
	}
}

// Opening the log moves one past 5 MB aside, and clears run logs older
// than 30 days and the per-PR logs older prowls left.
func TestOpenPrunes(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", os.Getenv("XDG_DATA_HOME"))
	defer slog.SetDefault(slog.Default())
	write := func(path string, size int64, age time.Duration) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Truncate(path, size)
		_ = os.Chtimes(path, time.Now().Add(-age), time.Now().Add(-age))
	}
	write(logs.Path(), 6<<20, 0)
	write(logs.RunPath("41"), 10, 31*24*time.Hour)
	write(logs.RunPath("42"), 10, time.Hour)
	write(filepath.Join(logs.Dir(), "o_api_7-claude.log"), 10, 0)
	logs.Open("tui")
	for path, want := range map[string]bool{
		logs.Path() + ".1": true, logs.RunPath("42"): true,
		logs.RunPath("41"): false, filepath.Join(logs.Dir(), "o_api_7-claude.log"): false,
	} {
		if _, err := os.Stat(path); (err == nil) != want {
			t.Errorf("%s: exists %v, want %v", path, err == nil, want)
		}
	}
	if fi, _ := os.Stat(logs.Path()); fi.Size() > 1<<20 {
		t.Errorf("the log wasn't moved aside: %d bytes", fi.Size())
	}
}

// A process that keeps running, like the TUI, goes on writing to prowl.log
// after another process moved it aside.
func TestOpenFollowsRotation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", os.Getenv("XDG_DATA_HOME"))
	defer slog.SetDefault(slog.Default())
	logs.Open("tui")
	slog.Info("before")
	if err := os.Rename(logs.Path(), logs.Path()+".1"); err != nil {
		t.Fatal(err)
	}
	slog.Info("after")
	if b, _ := os.ReadFile(logs.Path()); !strings.Contains(string(b), "msg=after") {
		t.Fatalf("the line after the move went elsewhere: %q", b)
	}
}
