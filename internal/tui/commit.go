package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/tmstorm/prowl/internal/inbox"
)

// colorRepos gives each repo its own color; off, all repos share one.
var colorRepos = false

// typeColors are the user's [type_colors]: palette names or "#rrggbb".
var typeColors map[string]string

func paletteColor(name string) string {
	switch strings.ToLower(name) {
	case "red":
		return pal.Red
	case "green":
		return pal.Green
	case "orange":
		return pal.Orange
	case "pink":
		return pal.Pink
	case "yellow":
		return pal.Yellow
	case "purple", "accent":
		return pal.Accent
	case "cyan", "blue":
		return pal.Cyan
	case "dim", "muted", "":
		return ""
	}
	if strings.HasPrefix(name, "#") {
		return name
	}
	return ""
}

// titleType is a PR title's conventional commit type, or "other".
func titleType(title string) string {
	t := inbox.ParseTitle(title)
	if !t.OK {
		return "other"
	}
	return t.Type
}

func typeStyle(typ string) lipgloss.Style {
	if name, ok := typeColors[typ]; ok {
		if c := paletteColor(name); c != "" {
			return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
		}
		return sDim
	}
	c := map[string]string{
		"fix": pal.Red, "feat": pal.Green, "perf": pal.Orange, "refactor": pal.Pink, "revert": pal.Pink,
		"test": pal.Yellow, "docs": pal.Accent, "ci": pal.Cyan, "build": pal.Cyan,
	}[typ]
	if c == "" {
		return sDim
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c))
}

// styledTitle colors a conventional commit title: the type in its color,
// the scope dimmed, a breaking "!" in bold red, and the subject plain.
func styledTitle(title string, bold bool) string {
	subject := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Fg)).Bold(bold)
	t := inbox.ParseTitle(title)
	if !t.OK {
		return subject.Render(title)
	}
	s := typeStyle(t.Type).Bold(bold).Render(t.Type)
	if t.Scope != "" {
		s += sDim.Render("(" + t.Scope + ")")
	}
	if t.Breaking {
		s += sRed.Bold(true).Render("!")
	}
	return s + sDim.Render(":") + " " + subject.Render(t.Subject)
}
