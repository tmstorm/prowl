package core

import (
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
