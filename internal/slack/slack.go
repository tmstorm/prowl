// Package slack formats one-line PR status messages for pasting into Slack.
package slack

import (
	"strings"

	"github.com/tmstorm/prowl/internal/inbox"
)

// Line builds e.g.
//
//	https://github.com/org/repo/pull/123 Approved · alice · Fix search pagination
//
// who is how to name the author.
func Line(it inbox.Item, who string) string {
	pr := it.PR
	var status string
	switch {
	case it.MyLastReview != nil && it.MyLastReview.State == "APPROVED":
		status = "Approved"
	case it.MyLastReview != nil && it.MyLastReview.State == "CHANGES_REQUESTED":
		status = "Changes requested"
	case pr.ReviewDecision == "APPROVED":
		status = "Approved"
	case pr.ReviewDecision == "CHANGES_REQUESTED":
		status = "Changes requested"
	default:
		status = "Needs review"
	}
	if pr.NeedsRebase() {
		status += ", needs rebase"
	}
	return strings.Join([]string{pr.URL + " " + status, who, pr.Title}, " · ")
}
