// Package slack formats one-line PR status messages for pasting into Slack.
package slack

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/tmstorm/prowl/internal/inbox"
)

// Line builds e.g.
//
//	https://github.com/org/repo/pull/123 Approved · alice · Fix search pagination
func Line(it inbox.Item, names map[string]string) string {
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
	who := pr.AuthorLogin()
	if n, ok := names[who]; ok {
		who = n
	}
	return strings.Join([]string{pr.URL + " " + status, who, pr.Title}, " · ")
}

// Copy puts text on the clipboard (Wayland, then X11, then macOS).
func Copy(text string) error {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
			"[Console]::InputEncoding = [Text.Encoding]::UTF8; Set-Clipboard -Value ([Console]::In.ReadToEnd())")
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	for _, c := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "-ib"}, {"pbcopy"}} {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		return cmd.Run()
	}
	return fmt.Errorf("no clipboard tool found (install wl-clipboard or xclip)")
}
