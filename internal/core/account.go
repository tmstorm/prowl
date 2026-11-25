package core

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/platform"
	"github.com/tmstorm/prowl/internal/slack"
)

// AuthEvery is how often Run and `prowl watch` check the logins, and
// RenewWithin how early prowl asks you to log in again before a login runs
// out.
const (
	AuthEvery   = 30 * time.Minute
	RenewWithin = 72 * time.Hour
)

// Account is one login prowl depends on: GitHub, and each agent that can log
// in.
type Account struct {
	Key, Title string
	Missing    error // why it can't be used, e.g. not installed
	Auth       Auth  // as of the last check; Known is false before one
}

// Auth is whether an account is logged in.
type Auth = agent.Auth

// Agents are the review agents prowl can run.
func Agents() []string { return agent.Names() }

// Accounts are GitHub, every agent prowl can log in to, and Slack, as of the
// last CheckAuth.
func (co *Core) Accounts() []Account {
	co.mu.Lock()
	defer co.mu.Unlock()
	return append([]Account(nil), co.accounts...)
}

// LoginsDue are the logins that need you, as of the last CheckAuth: each
// reviewer that is logged out or whose login runs out within RenewWithin,
// and Slack when it refused the token you signed in with.
func (co *Core) LoginsDue() []Account {
	reviewers, slackOn := co.Config().Agent.Reviewers, co.SlackConnected()
	var due []Account
	for _, a := range co.Accounts() {
		mine := slices.Contains(reviewers, a.Key) || a.Key == "slack" && slackOn
		if mine && a.Auth.Known && (!a.Auth.LoggedIn || a.Auth.ExpiresSoon(RenewWithin)) {
			due = append(due, a)
		}
	}
	return due
}

// remindLogins sends a desktop notification when the logins due change and
// some are due.
func (co *Core) remindLogins() {
	var states, msgs []string
	for _, a := range co.LoginsDue() {
		if !a.Auth.LoggedIn {
			states = append(states, a.Key+":out")
			msgs = append(msgs, a.Title+" is logged out.")
			continue
		}
		states = append(states, a.Key+":"+a.Auth.Expires.String())
		msgs = append(msgs, a.Title+"'s login runs out "+a.Auth.Expires.Local().Format("Mon 15:04")+".")
	}
	state := strings.Join(states, ",")
	co.mu.Lock()
	last := co.reminded
	co.reminded = state
	co.mu.Unlock()
	if state != "" && state != last {
		platform.Notify("prowl", strings.Join(msgs, " ")+" Log in again from prowl's accounts.", co.Config().NotifyTimeout.Duration)
	}
}

// CheckAuth asks GitHub, every installed agent and Slack whether you're
// logged in. It runs their status commands, so it takes a moment.
func (co *Core) CheckAuth(ctx context.Context) {
	accts := accountList(co.Config())
	for i, a := range accts {
		switch {
		case a.Missing != nil:
		case a.Key == "slack":
			accts[i].Auth = co.checkSlack(ctx)
		case a.Key == "github":
			out, err := exec.CommandContext(ctx, "gh", "auth", "status", "--active").CombinedOutput()
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

// checkSlack asks Slack whose token prowl has. No token, or Slack refusing
// it, means you're logged out; failing to reach Slack says nothing.
func (co *Core) checkSlack(ctx context.Context) agent.Auth {
	co.mu.Lock()
	sl := co.slack
	co.mu.Unlock()
	if sl == nil {
		return agent.Auth{Known: true}
	}
	id, err := sl.AuthTest(ctx)
	if slack.LoggedOut(err) {
		return agent.Auth{Known: true}
	}
	if err != nil {
		return agent.Auth{}
	}
	return agent.Auth{Known: true, LoggedIn: true, Account: id.User + " in " + id.Team}
}

// Login is the interactive command that logs an account in.
func (co *Core) Login(key string) (Command, error) {
	switch key {
	case "github":
		return Command{Args: []string{"gh", "auth", "login"}}, nil
	case "slack":
		return Command{Args: []string{Executable(), "slack"}}, nil
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
		return Command{}, fmt.Errorf("prowl can't log %s in", agent.Title(be.Name()))
	}
	return commandOf(cmd, ""), nil
}

// setLoggedOut records that a call found the account logged out, ahead of
// the next check.
func (co *Core) setLoggedOut(key string) {
	co.mu.Lock()
	defer co.mu.Unlock()
	for i := range co.accounts {
		if co.accounts[i].Key == key {
			co.accounts[i].Auth = agent.Auth{Known: true}
		}
	}
}

// slackFor is the Slack client to act with, or why there's none: you never
// signed in, or the last check found you logged out. co.mu is held.
func (co *Core) slackFor() (*slack.Client, error) {
	if co.slack == nil {
		return nil, errors.New("Slack isn't logged in")
	}
	if err := co.authErr("slack"); err != nil {
		return nil, err
	}
	return co.slack, nil
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
		out = append(out, Account{Key: n, Title: agent.Title(be.Name()), Missing: be.Check()})
	}
	return append(out, Account{Key: "slack", Title: "Slack"})
}

// UpdateConfig changes the config, saves it and applies it.
func (co *Core) UpdateConfig(fn func(*config.Config) error) (config.Config, error) {
	next, err := config.Update(fn)
	if err != nil {
		return co.Config(), fmt.Errorf("save config: %w", err)
	}
	co.mu.Lock()
	co.cfgMod = config.ModTime()
	co.mu.Unlock()
	co.setConfig(next)
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
	co.setConfig(cfg)
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

// FirstTime reports whether this is the first time prowl does name, such as
// offering a tip, and remembers that it now has.
func (co *Core) FirstTime(name string) bool { return co.st.FirstTime(name) }

// Shown reports whether prowl has done name before, without marking it.
func (co *Core) Shown(name string) bool { return co.st.Shown(name) }

// SetService sets up this prowl's `prowl watch` to run at login and starts
// it, or stops and removes it. It returns what it set up, for people.
func (co *Core) SetService(on bool) (string, error) {
	var what string
	var err error
	if on {
		exe := Executable()
		if what, err = platform.InstallService(exe); err == nil {
			logErr("remember the background service", co.st.SetServiceSum(platform.ServiceSum(exe)))
		}
	} else {
		err = platform.UninstallService()
	}
	if err == nil {
		co.serviceOnce.Do(func() {})
		co.mu.Lock()
		co.service = on
		co.mu.Unlock()
	}
	return what, err
}
