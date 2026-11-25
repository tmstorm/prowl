package core

import (
	"context"
	"time"

	"github.com/tmstorm/prowl/internal/config"
)

// SetConfig replaces the config without saving it.
func (co *Core) SetConfig(cfg config.Config) { co.setConfig(cfg) }

// RetryNow lets the next AutoUpgrade try a failed install again without
// waiting an hour.
func (co *Core) RetryNow() {
	co.mu.Lock()
	defer co.mu.Unlock()
	co.updateRetry = time.Time{}
}

// ExportFindingBody is a finding as prowl posts it.
func ExportFindingBody(f Finding) string { return findingBody(f) }

// TakeRole has this prowl take the role, as Run does when no prowl watch
// runs, and reports whether it holds it.
func (co *Core) TakeRole() bool {
	co.claimRole()
	return co.holdsRole()
}

// ReleaseRole gives the role up.
func (co *Core) ReleaseRole() { co.releaseRole() }

// Follow takes up the inbox the prowl with the role saved, as Run does.
func (co *Core) Follow(ctx context.Context) bool { return co.follow(ctx) }
