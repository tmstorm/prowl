package inbox_test

import (
	"github.com/tmstorm/prowl/internal/inbox"
	"testing"

	"github.com/tmstorm/prowl/internal/config"
)

func TestParseTitle(t *testing.T) {
	for in, want := range map[string]inbox.Title{
		"fix(api): handle nil":       {Type: "fix", Scope: "api", Subject: "handle nil", OK: true},
		"feat!: drop v1":             {Type: "feat", Breaking: true, Subject: "drop v1", OK: true},
		"Docs(ci): Check things":     {Type: "docs", Scope: "ci", Subject: "Check things", OK: true},
		"Merge branch main into dev": {Subject: "Merge branch main into dev"},
	} {
		if got := inbox.ParseTitle(in); got != want {
			t.Errorf("inbox.ParseTitle(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestTypeRank(t *testing.T) {
	order := config.DefaultTypeOrder
	rank := func(s string) int { return inbox.TypeRank(inbox.ParseTitle(s), order) }
	seq := []string{"fix!: breaking fix", "fix: a", "feat: b", "docs: c", "Update readme", "wip: unknown type", "chore: d"}
	for i := 1; i < len(seq); i++ {
		if rank(seq[i-1]) > rank(seq[i]) {
			t.Errorf("%q should sort before %q", seq[i-1], seq[i])
		}
	}
	if rank("Update readme") != rank("wip: unknown type") {
		t.Error("unlisted types should sort with titles that aren't conventional")
	}
}
