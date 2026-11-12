package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/platform"
)

// authEvery is how often Run checks the logins.
const authEvery = 30 * time.Minute

// Account is one login prowl depends on: GitHub, and each agent that can log
// in.
type Account struct {
	Key, Title string
	Missing    error      // why it can't be used, e.g. not installed
	Auth       agent.Auth // as of the last check; Known is false before one
}

// Accounts are GitHub and every agent prowl can log in to, as of the last
// CheckAuth.
func (co *Core) Accounts() []Account {
	co.mu.Lock()
	defer co.mu.Unlock()
	return append([]Account(nil), co.accounts...)
}

// CheckAuth asks GitHub and every installed agent whether you're logged in.
// It runs their status commands, so it takes a moment.
func (co *Core) CheckAuth() {
	accts := accountList(co.Config())
	for i, a := range accts {
		switch {
		case a.Missing != nil:
		case a.Key == "github":
			out, err := exec.Command("gh", "auth", "status", "--active").CombinedOutput()
			accts[i].Auth = agent.Auth{Known: true, LoggedIn: err == nil}
			if _, rest, ok := strings.Cut(string(out), "account "); ok {
				accts[i].Auth.Account, _, _ = strings.Cut(rest, " ")
			}
		default:
			if be, err := agent.For(co.Config(), a.Key); err == nil {
				accts[i].Auth = be.Auth()
			}
		}
	}
	co.mu.Lock()
	co.accounts = accts
	co.mu.Unlock()
}

// Login is the interactive command that logs an account in.
func (co *Core) Login(key string) (Command, error) {
	if key == "github" {
		return Command{Args: []string{"gh", "auth", "login"}}, nil
	}
	be, err := agent.For(co.Config(), key)
	if err == nil {
		err = be.Check()
	}
	if err != nil {
		return Command{}, err
	}
	cmd := be.Login()
	if cmd == nil {
		return Command{}, fmt.Errorf("prowl can't log %s in", be.Title())
	}
	return commandOf(cmd, ""), nil
}

// loggedIn fails when the last check found the agent logged out.
func (co *Core) loggedIn(name string) error {
	co.mu.Lock()
	defer co.mu.Unlock()
	return co.authErr(name)
}

// authErr is loggedIn with co.mu held.
func (co *Core) authErr(name string) error {
	for _, a := range co.accounts {
		if a.Key == name && a.Auth.Known && !a.Auth.LoggedIn {
			return fmt.Errorf("%s isn't logged in", a.Title)
		}
	}
	return nil
}

// accountList is every account for cfg, not yet checked.
func accountList(cfg config.Config) []Account {
	out := []Account{{Key: "github", Title: "GitHub"}}
	if _, err := exec.LookPath("gh"); err != nil {
		out[0].Missing = fmt.Errorf("the GitHub CLI isn't installed; get it from https://cli.github.com")
	}
	for _, n := range agent.Names() {
		be, err := agent.For(cfg, n)
		if err != nil || be.Login() == nil {
			continue
		}
		out = append(out, Account{Key: n, Title: be.Title(), Missing: be.Check()})
	}
	return out
}

// Update changes the config, saves it and applies it.
func (co *Core) Update(fn func(*config.Config) error) (config.Config, error) {
	next, err := config.Update(fn)
	if err != nil {
		return co.Config(), fmt.Errorf("save config: %w", err)
	}
	co.mu.Lock()
	co.cfgMod = config.ModTime()
	co.mu.Unlock()
	co.SetConfig(next)
	return next, nil
}

// reloadConfig picks up an edit to the config file made outside prowl and
// reports whether there was one.
func (co *Core) reloadConfig() (bool, error) {
	mt := config.ModTime()
	co.mu.Lock()
	changed := !mt.IsZero() && !mt.Equal(co.cfgMod)
	if changed {
		co.cfgMod = mt
	}
	co.mu.Unlock()
	if !changed {
		return false, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return true, fmt.Errorf("%s: %w", config.Path(), err)
	}
	co.SetConfig(cfg)
	return true, nil
}

// CheckAgent reports why an agent can't review (unknown, not installed, not
// configured, logged out), or nil.
func (co *Core) CheckAgent(name string) error {
	be, err := agent.For(co.Config(), name)
	if err == nil {
		err = be.Check()
	}
	if err != nil {
		return err
	}
	return co.loggedIn(be.Name())
}

// Models are the models an agent offers, as it lists them ("" is its own
// default). The model set in cfg stays in the list even when the agent
// doesn't offer it, so picking from the list never loses it.
func Models(cfg config.Config, name string) []string {
	be, err := agent.For(cfg, name)
	if err != nil {
		return []string{""}
	}
	models := be.Models()
	if cur := cfg.Agent.Model(be.Name()); !slices.Contains(models, cur) {
		models = append(models, cur)
	}
	return models
}

// Service reports whether `prowl watch` runs at login, sending notifications
// while prowl is closed. The first call asks the OS; later ones remember.
func (co *Core) Service() bool {
	co.serviceOnce.Do(func() { co.service = platform.ServiceInstalled() })
	co.mu.Lock()
	defer co.mu.Unlock()
	return co.service
}

// OfferService reports whether to suggest background notifications: the
// first time prowl asks, and only while they're off.
func (co *Core) OfferService() bool {
	return co.st.FirstTime("service") && !co.Service()
}

// AskNerdFont reports whether to ask if the terminal shows Nerd Font icons:
// the first time prowl asks, and only while they're off.
func (co *Core) AskNerdFont() bool {
	return !co.Config().NerdFont && co.st.FirstTime("nerd font")
}

// SetService sets up `prowl watch` to run at login and starts it, or stops
// and removes it. It returns what it set up, for people.
func (co *Core) SetService(on bool) (string, error) {
	what, err := SetService(on)
	if err == nil {
		co.serviceOnce.Do(func() {})
		co.mu.Lock()
		co.service = on
		co.mu.Unlock()
	}
	return what, err
}

// SetService sets up this prowl's `prowl watch` to run at login and starts
// it, or stops and removes it.
func SetService(on bool) (string, error) {
	if !on {
		return "", platform.UninstallService()
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("service: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", fmt.Errorf("service: %w", err)
	}
	return platform.InstallService(exe)
}
