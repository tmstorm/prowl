package tui_test

import (
	"strings"
	"testing"

	cstyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/tui"
)

// Every theme has its own palette and a registered code style: a missing
// one falls back to dracula's or chroma's default without a word.
func TestThemesComplete(t *testing.T) {
	for _, name := range config.Themes {
		if _, ok := tui.Palettes[name]; !ok {
			t.Errorf("%s: no palette", name)
		}
		if style := tui.ChromaThemes[name]; cstyles.Get(style).Name != style {
			t.Errorf("%s: chroma style %q not registered", name, style)
		}
	}
}

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
