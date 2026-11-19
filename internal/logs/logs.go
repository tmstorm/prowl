// Package logs keeps prowl's log: one file that every prowl process appends
// to, the TUI, prowl watch and the review processes alike, so what happened
// across them reads in one place. It holds IDs, counts, durations and
// errors, never a token, a PR's text, a prompt or review text, since people
// paste it into issues.
package logs

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tmstorm/prowl/internal/config"
)

const (
	maxSize = 5 << 20
	keepRun = 30 * 24 * time.Hour
)

func Dir() string { return filepath.Join(config.DataDir(), "logs") }

// Path is the log every process appends to.
func Path() string { return filepath.Join(Dir(), "prowl.log") }

// RunPath is where a review's own output goes: what its agent printed,
// which stays out of the shared log.
func RunPath(name string) string { return filepath.Join(Dir(), "runs", name+".log") }

// Open makes the log slog's default for this process, each line naming it
// with proc (tui, watch, agent-run) and its pid. First it moves a log past
// 5 MB to prowl.log.1, deletes run logs older than 30 days, and deletes the
// per-PR logs of prowl versions before this one. PROWL_DEBUG=1 adds debug
// lines. When the log can't be written, logging is off, so nothing is ever
// written over the TUI.
func Open(proc string) {
	if err := prepare(); err != nil {
		slog.SetDefault(slog.New(slog.DiscardHandler))
		return
	}
	level := slog.LevelInfo
	if os.Getenv("PROWL_DEBUG") != "" {
		level = slog.LevelDebug
	}
	slog.SetDefault(newLogger(appender(Path()), level).With("proc", proc, "pid", os.Getpid()))
}

// newLogger writes prowl's log format: key=value lines with the time to the
// second.
func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey && len(groups) == 0 {
			a.Value = slog.StringValue(a.Value.Time().Format("2006-01-02T15:04:05"))
		}
		return a
	}}))
}

// appender opens the log for each line rather than holding it open, so a
// TUI or prowl watch that runs for days keeps writing to prowl.log after
// another process moves it aside, and on Windows doesn't stop that move.
// Each line is one append.
type appender string

func (a appender) Write(p []byte) (int, error) {
	f, err := os.OpenFile(string(a), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := f.Write(p)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}

func prepare() error {
	if err := os.MkdirAll(filepath.Join(Dir(), "runs"), 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(Path()); err == nil && fi.Size() > maxSize {
		_ = os.Rename(Path(), Path()+".1")
	}
	prune()
	f, err := os.OpenFile(Path(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func prune() {
	runs, _ := os.ReadDir(filepath.Join(Dir(), "runs"))
	for _, e := range runs {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > keepRun {
			_ = os.Remove(filepath.Join(Dir(), "runs", e.Name()))
		}
	}
	old, _ := os.ReadDir(Dir())
	for _, e := range old {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), "prowl.log") {
			_ = os.Remove(filepath.Join(Dir(), e.Name()))
		}
	}
}
