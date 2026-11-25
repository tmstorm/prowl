package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	cstyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/tmstorm/prowl/internal/core"
)

type palette struct {
	Bg, Surface, Fg, Muted                         string
	Accent, Pink, Green, Yellow, Red, Orange, Cyan string
}

// Official palettes (dark variants), and bebop.
var palettes = map[string]palette{
	"dracula":    {"#282A36", "#44475A", "#F8F8F2", "#6272A4", "#BD93F9", "#FF79C6", "#50FA7B", "#F1FA8C", "#FF5555", "#FFB86C", "#8BE9FD"},
	"catppuccin": {"#1E1E2E", "#45475A", "#CDD6F4", "#7F849C", "#CBA6F7", "#F5C2E7", "#A6E3A1", "#F9E2AF", "#F38BA8", "#FAB387", "#89DCEB"},
	"tokyonight": {"#1A1B26", "#283457", "#C0CAF5", "#565F89", "#7AA2F7", "#BB9AF7", "#9ECE6A", "#E0AF68", "#F7768E", "#FF9E64", "#7DCFFF"},
	"gruvbox":    {"#282828", "#3C3836", "#EBDBB2", "#928374", "#83A598", "#D3869B", "#B8BB26", "#FABD2F", "#FB4934", "#FE8019", "#8EC07C"},
	"nord":       {"#2E3440", "#434C5E", "#ECEFF4", "#616E88", "#88C0D0", "#B48EAD", "#A3BE8C", "#EBCB8B", "#BF616A", "#D08770", "#8FBCBB"},
	"rose-pine":  {"#191724", "#403D52", "#E0DEF4", "#6E6A86", "#C4A7E7", "#EBBCBA", "#9CCFD8", "#F6C177", "#EB6F92", "#EBBCBA", "#31748F"},
	"kanagawa":   {"#1F1F28", "#2D4F67", "#DCD7BA", "#727169", "#957FB8", "#D27E99", "#98BB6C", "#E6C384", "#FF5D62", "#FFA066", "#7FB4CA"},
	"bebop":      {"#0A0D16", "#1B2238", "#E9DCC5", "#7C8297", "#D9A45B", "#B08CD2", "#6EAA82", "#D9A45B", "#F25C05", "#BC5B3A", "#7A96E0"},
}

var (
	pal       palette
	themeName string
	nerdIcons = true

	sTitle, sDim, sBold, sRed, sGreen, sYellow, sBlue, sPink, sOrange lipgloss.Style
	sSection, sErr, sSelRow, sBorder, sBorderOn, sSkel, sSkelHi       lipgloss.Style
)

func applyTheme(name string) {
	p, ok := palettes[name]
	if !ok {
		p = palettes["dracula"]
	}
	pal = p
	themeName = name
	c := func(h string) lipgloss.Color { return lipgloss.Color(h) }
	fg := lipgloss.NewStyle().Foreground
	sTitle = fg(c(p.Accent)).Bold(true)
	sDim = fg(c(p.Muted))
	sBold = fg(c(p.Fg)).Bold(true)
	sRed = fg(c(p.Red))
	sGreen = fg(c(p.Green))
	sYellow = fg(c(p.Yellow))
	sBlue = fg(c(p.Cyan))
	sPink = fg(c(p.Pink))
	sOrange = fg(c(p.Orange))
	sSection = fg(c(p.Accent)).Bold(true)
	sErr = fg(c(p.Red)).Bold(true)
	sSelRow = lipgloss.NewStyle().Background(c(p.Surface))
	sBorder = fg(c(p.Muted))
	sBorderOn = fg(c(p.Accent))
	sSkel = fg(c(p.Surface))
	sSkelHi = fg(c(p.Muted))
}

func init() {
	cstyles.Register(chroma.MustNewStyle("bebop", chroma.StyleEntries{
		chroma.Background:      "#E9DCC5 bg:#0A0D16",
		chroma.Comment:         "italic #7C8297",
		chroma.Keyword:         "#D9A45B",
		chroma.KeywordType:     "#7A96E0",
		chroma.Name:            "#E9DCC5",
		chroma.NameFunction:    "#7A96E0",
		chroma.NameBuiltin:     "#B08CD2",
		chroma.NameTag:         "#D9A45B",
		chroma.NameAttribute:   "#7A96E0",
		chroma.LiteralString:   "#6EAA82",
		chroma.LiteralNumber:   "#F25C05",
		chroma.Operator:        "#BC5B3A",
		chroma.GenericInserted: "#6EAA82",
		chroma.GenericDeleted:  "#F25C05",
		chroma.GenericHeading:  "bold #D9A45B",
		chroma.Error:           "#F25C05",
	}))
	applyTheme("dracula")
}

func bucketStyle(b core.Bucket) lipgloss.Style {
	switch b {
	case core.NeedsReview, core.ReReview, core.Shared:
		return sRed
	case core.ReApprove, core.Recheck:
		return sOrange
	case core.Addressed:
		return sYellow
	case core.Activity:
		return sBlue
	case core.Approved:
		return sGreen
	case core.Mine, core.Local:
		return sPink
	case core.Unclaimed:
		return sOrange
	}
	return sDim
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "–"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func ciBadge(state string) string {
	ok, bad, run := "✓", "✗", "●"
	if nerdIcons {
		ok, bad, run = "\uf00c", "\uf00d", "\uf017"
	}
	switch state {
	case "SUCCESS":
		return sGreen.Render(ok)
	case "FAILURE", "ERROR":
		return sRed.Render(bad)
	case "PENDING", "EXPECTED":
		return sYellow.Render(run)
	}
	return " "
}

func statusBadge(s string) string {
	switch s {
	case "approved":
		return sGreen.Render("approved")
	case "changes requested":
		return sRed.Render("changes requested")
	case "needs rebase":
		return sYellow.Render("needs rebase")
	}
	return sDim.Render(s)
}

func trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// count is n of noun, plural unless n is 1: "1 line", "3 replies".
func count(n int, noun string) string {
	switch {
	case n == 1:
	case strings.HasSuffix(noun, "y"):
		noun = strings.TrimSuffix(noun, "y") + "ies"
	default:
		noun += "s"
	}
	return fmt.Sprintf("%d %s", n, noun)
}

// fit truncates or pads a styled string to exactly w cells.
func fit(s string, w int) string {
	s = trunc(s, w)
	if n := lipgloss.Width(s); n < w {
		s += strings.Repeat(" ", w-n)
	}
	return s
}

func wrap(s string, w int) []string {
	if w < 10 {
		w = 10
	}
	return strings.Split(lipgloss.NewStyle().Width(w).Render(s), "\n")
}
