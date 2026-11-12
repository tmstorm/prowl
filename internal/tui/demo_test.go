package tui_test

import (
	"testing"

	"github.com/tmstorm/prowl/internal/ghtest"
)

// The demo the README's screenshots are recorded from sorts every PR, and
// its auth PR has a diff on both sides and the stored AI review.
func TestDemo(t *testing.T) {
	a := newApp(t, ghtest.Demo())
	if err := ghtest.SeedDemo(a.st); err != nil {
		t.Fatal(err)
	}
	a = reopen(a)
	a.want("NEEDS REVIEW 3", "ADDRESSED 1", "RE-CHECK 1", "rotate refresh tokens")
	a.keys("enter")
	a.want("AI summary · haiku", "Checks · 3 passed", "bob")
	a.keys("3", "+")
	a.want("- // Rotate issues a new access token", "+ // Rotate swaps a refresh token")
	a.keys("4")
	a.want("opus · 3 findings · 1 still open", "A reused token leaves the rest of its family valid", "internal/auth/refresh.go:28", "Notes")
}
