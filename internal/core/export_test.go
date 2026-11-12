package core

import "time"

// RetryNow lets the next AutoUpgrade try a failed install again without
// waiting an hour.
func (co *Core) RetryNow() {
	co.mu.Lock()
	defer co.mu.Unlock()
	co.updateRetry = time.Time{}
}
