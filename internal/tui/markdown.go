package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/glamour"
	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	xansi "github.com/charmbracelet/x/ansi"
)

var chromaThemes = map[string]string{
	"dracula": "dracula", "catppuccin": "catppuccin-mocha", "tokyonight": "tokyonight-night",
	"gruvbox": "gruvbox", "nord": "nord", "rose-pine": "rose-pine", "kanagawa": "kanagawa-wave",
}

var md struct {
	theme string
	w     int
	r     *glamour.TermRenderer
	cache map[string][]string
}

var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// mdStyle is glamour's dark style recolored with the active palette.
func mdStyle(theme string) gansi.StyleConfig {
	s := styles.DarkStyleConfig
	str := func(v string) *string { return &v }
	u := func(v uint) *uint { return &v }
	t := func() *bool { b := true; return &b }

	s.Document.Margin = u(0)
	s.Document.BlockPrefix, s.Document.BlockSuffix = "", ""
	s.Document.Color = str(pal.Fg)
	s.Heading.Color = str(pal.Accent)
	s.Heading.Bold = t()
	s.H1.Color, s.H1.BackgroundColor = str(pal.Bg), str(pal.Accent)
	s.H1.Prefix, s.H1.Suffix = " ", " "
	s.H2.Prefix, s.H3.Prefix, s.H4.Prefix, s.H5.Prefix, s.H6.Prefix = "", "", "", "", ""
	s.H2.Color = str(pal.Pink)
	s.H3.Color = str(pal.Accent)
	s.Link.Color = str(pal.Cyan)
	s.LinkText.Color = str(pal.Pink)
	s.Code.Color = str(pal.Green)
	s.Code.BackgroundColor = nil
	s.Strong.Color = str(pal.Orange)
	s.Emph.Color = str(pal.Yellow)
	s.BlockQuote.Color = str(pal.Muted)
	s.Item.BlockPrefix = "• "
	s.HorizontalRule.Color = str(pal.Muted)
	s.CodeBlock.Chroma = nil
	s.CodeBlock.Theme = chromaThemes[theme]
	s.CodeBlock.Margin = u(1)
	return s
}

// renderMarkdown renders GitHub markdown to styled lines of at most w cells.
func renderMarkdown(src string, w int, theme string) []string {
	if md.r == nil || md.w != w || md.theme != theme {
		r, err := glamour.NewTermRenderer(
			glamour.WithStyles(mdStyle(theme)),
			glamour.WithWordWrap(w),
			glamour.WithEmoji(),
		)
		if err != nil {
			return wrap(src, w)
		}
		md.r, md.w, md.theme, md.cache = r, w, theme, map[string][]string{}
	}
	if out, ok := md.cache[src]; ok {
		return out
	}
	clean := strings.TrimSpace(htmlComment.ReplaceAllString(src, ""))
	if clean == "" {
		return nil
	}
	out, err := md.r.Render(clean)
	if err != nil {
		return wrap(clean, w)
	}
	lines := strings.Split(strings.TrimRight(out, "\n "), "\n")
	for len(lines) > 0 && strings.TrimSpace(xansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	md.cache[src] = lines
	return lines
}
