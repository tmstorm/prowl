package tui_test

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/tmstorm/prowl/internal/tui"
)

// The same text rendered at widths in turn, as one Overview does, wraps to
// each width, not to whichever came first.
func TestRenderMarkdownWidths(t *testing.T) {
	src := strings.Repeat("word ", 40)
	for _, w := range []int{30, 60, 30, 60} {
		lines := tui.RenderMarkdown(src, w)
		widest := 0
		for _, l := range lines {
			widest = max(widest, lipgloss.Width(l))
		}
		if widest > w || widest < w-10 {
			t.Errorf("at width %d the widest line is %d", w, widest)
		}
	}
}
