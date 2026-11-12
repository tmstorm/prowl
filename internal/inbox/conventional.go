package inbox

import (
	"regexp"
	"strings"
)

// Title is a PR title split into its Conventional Commits parts
// ("fix(api)!: handle nil" -> type fix, scope api, breaking, subject).
type Title struct {
	Type, Scope, Subject string
	Breaking             bool
	OK                   bool // the title follows the convention
}

var conventional = regexp.MustCompile(`^([a-zA-Z]+)(?:\(([^)]*)\))?(!)?:\s*(.*)$`)

func ParseTitle(s string) Title {
	m := conventional.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Title{Subject: s}
	}
	return Title{Type: strings.ToLower(m[1]), Scope: m[2], Breaking: m[3] == "!", Subject: m[4], OK: true}
}

// TypeRank orders a title's type by order (lower first); breaking changes go
// ahead of others of the same type.
func TypeRank(t Title, order []string) int {
	typ := t.Type
	if !t.OK {
		typ = "other"
	}
	rank := len(order)
	for i, o := range order {
		if o == typ {
			rank = i
			break
		}
	}
	if rank == len(order) {
		for i, o := range order {
			if o == "other" {
				rank = i
			}
		}
	}
	rank *= 2
	if !t.Breaking {
		rank++
	}
	return rank
}
