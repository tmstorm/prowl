package core

import (
	"errors"
	"fmt"
	"os"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/platform"
	"github.com/tmstorm/prowl/internal/skill"
)

// Footprint is what prowl put on this machine, for `prowl uninstall`.
type Footprint struct {
	Service   bool     // prowl watch runs at login
	Checkouts []string // your checkouts holding prowl's review worktrees or refs
	Skills    []string // prowl-findings skills as prowl installed them
	Binary    string   // the prowl binary, when prowl may remove it
	Command   string   // what removes prowl when something else owns it
	Dirs      []string // the config and data dirs, with purge
}

// FindFootprint lists what uninstalling the prowl at exe removes. purge adds
// the config and data dirs; without it they stay for a reinstall.
func FindFootprint(cfg config.Config, exe string, purge bool) Footprint {
	f := Footprint{
		Service:   platform.ServiceInstalled(),
		Checkouts: agent.Checkouts(cfg.ExpandedRepoRoots()),
		Skills:    skill.Installed(),
	}
	switch o := owner(exe); o {
	case "":
		f.Binary = exe
	case "readonly":
		f.Command = "remove " + exe + " by hand"
	default:
		f.Command = map[string]string{"brew": "brew uninstall prowl", "scoop": "scoop uninstall prowl", "aur": "sudo pacman -R prowl-bin"}[o]
	}
	if purge {
		f.Dirs = []string{config.Dir(), config.DataDir()}
	}
	return f
}

// Uninstall removes everything in f, the binary last, and returns what it
// couldn't remove.
func Uninstall(f Footprint) error {
	var errs []error
	if f.Service {
		errs = append(errs, platform.UninstallService())
	}
	for _, dir := range f.Checkouts {
		errs = append(errs, agent.CleanCheckout(dir))
	}
	for _, dir := range f.Skills {
		errs = append(errs, skill.Remove(dir))
	}
	for _, dir := range f.Dirs {
		if err := os.RemoveAll(dir); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", dir, err))
		}
	}
	if f.Binary != "" {
		errs = append(errs, platform.RemoveBinary(f.Binary))
	}
	return errors.Join(errs...)
}
