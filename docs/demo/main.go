// Command demo runs prowl on made-up data, for the README's screenshots:
// a fake GitHub for the nimbus org, and a throwaway config and database.
// It never reads your config, your data or your GitHub login.
//
//	go run ./docs/demo
//	vhs docs/demo/demo.tape
package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
	"github.com/tmstorm/prowl/internal/store"
	"github.com/tmstorm/prowl/internal/tui"
)

// fakes stand in for the programs prowl calls, so nothing reaches your
// accounts or your desktop.
var fakes = map[string]string{
	"gh":          "echo 'Logged in to github.com account sam (keyring)'",
	"claude":      `echo '{"loggedIn":true,"authMethod":"claude.ai","email":"sam@nimbus.dev"}'`,
	"codex":       "exit 1",
	"notify-send": "exit 0",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run() error {
	tmp, err := os.MkdirTemp("", "prowl-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		return err
	}
	for name, script := range fakes {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
			return err
		}
	}
	for k, v := range map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(tmp, "config"),
		"XDG_DATA_HOME":   filepath.Join(tmp, "data"),
		"PATH":            bin + string(os.PathListSeparator) + os.Getenv("PATH"),
	} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}

	cfg := config.Default()
	cfg.Orgs = []string{"nimbus"}
	cfg.NerdFont = true
	if err := cfg.Save(); err != nil {
		return err
	}
	st, err := store.Open(config.DataDir())
	if err != nil {
		return err
	}
	defer st.Close()
	if err := ghtest.SeedDemo(st); err != nil {
		return err
	}
	if err := backdateReview(); err != nil {
		return err
	}

	c, err := gh.NewWith(api.ClientOptions{Host: "github.com", AuthToken: "demo",
		Transport: slowStart{ghtest.Demo(), time.Now().Add(3 * time.Second)}})
	if err != nil {
		return err
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	co := core.New(cfg, c, st)
	go co.Run(ctx)
	_, err = tea.NewProgram(tui.New(co), tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

// slowStart holds GitHub's answers until a moment after launch, so the
// recording shows prowl loading as it would over a real network.
type slowStart struct {
	next  http.RoundTripper
	until time.Time
}

func (s slowStart) RoundTrip(r *http.Request) (*http.Response, error) {
	time.Sleep(time.Until(s.until))
	return s.next.RoundTrip(r)
}

// backdateReview makes the seeded review look like it ran this morning and
// took a few minutes, as a real one would.
func backdateReview() error {
	db, err := sql.Open("sqlite", filepath.Join(config.DataDir(), "prowl.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE review_runs SET started_at = started_at - 3*3600 - 194, finished_at = finished_at - 3*3600`)
	return err
}
