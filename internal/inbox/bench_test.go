package inbox_test

import (
	"fmt"
	"testing"

	"github.com/tmstorm/prowl/internal/gh"
	"github.com/tmstorm/prowl/internal/ghtest"
	"github.com/tmstorm/prowl/internal/inbox"
)

// BenchmarkClassify sorts a made-up inbox of 500 PRs.
func BenchmarkClassify(b *testing.B) {
	in := gh.Inbox{Viewer: "me"}
	for i := range 500 {
		p := ghtest.PR(fmt.Sprintf("repo%d", i%25), i, fmt.Sprintf("fix: change %d", i), fmt.Sprintf("dev%d", i%40))
		if i%3 == 0 {
			p = ghtest.Requested(p, "me")
		}
		in.PRs = append(in.PRs, p)
	}
	for b.Loop() {
		inbox.Classify(&in, inbox.Options{})
	}
}
