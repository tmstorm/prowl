package docs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// manPages are the man pages built from the docs: the prowl command and
// its config. The concept pages and the TUI read better in prowl docs.
var manPages = []struct {
	page, name, section, summary, synopsis, seeAlso string
}{
	{"cli", "prowl", "1", "review GitHub pull requests from the terminal", "prowl [COMMAND] [ARGS]", "prowl-config(5); prowl docs for the inbox, reviews and the TUI"},
	{"config", "prowl-config", "5", "prowl's settings", "~/.config/prowl/config.toml", "prowl(1)"},
}

// ManPaths are where WriteMan puts the man pages under dir.
func ManPaths(dir string) []string {
	var out []string
	for _, m := range manPages {
		out = append(out, filepath.Join(dir, "man"+m.section, m.name+"."+m.section))
	}
	return out
}

// WriteMan writes the man pages under dir, as dir/man1/prowl.1 and
// dir/man5/prowl-config.5, for prowl version.
func WriteMan(dir, version string) error {
	for i, m := range manPages {
		p, ok := Get(m.page)
		if !ok {
			return fmt.Errorf("man: no docs page %q", m.page)
		}
		var b strings.Builder
		fmt.Fprintf(&b, ".TH %s %s \"\" \"prowl %s\" \"prowl manual\"\n", strings.ToUpper(m.name), m.section, roffText(version))
		fmt.Fprintf(&b, ".SH NAME\n%s \\- %s\n.SH SYNOPSIS\n%s\n.SH DESCRIPTION\n", roffText(m.name), roffText(m.summary), roffText(m.synopsis))
		b.WriteString(roff(p.Markdown))
		fmt.Fprintf(&b, ".SH SEE ALSO\n%s\n", roffText(m.seeAlso))
		path := ManPaths(dir)[i]
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("man: %w", err)
		}
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			return fmt.Errorf("man: %w", err)
		}
	}
	return nil
}

// roff renders a page's Markdown as man(7) roff, without its title: ##
// headings become sections, ### ones subsections, and tables go through tbl.
func roff(md string) string {
	src := []byte(md)
	doc := goldmark.New(goldmark.WithExtensions(extension.Table)).Parser().Parse(text.NewReader(src))
	var b strings.Builder
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		block(&b, n, src)
	}
	return b.String()
}

func block(b *strings.Builder, n ast.Node, src []byte) {
	switch n := n.(type) {
	case *ast.Heading:
		switch n.Level {
		case 1:
		case 2:
			fmt.Fprintf(b, ".SH %s\n", strings.ToUpper(inline(n, src)))
		default:
			fmt.Fprintf(b, ".SS %s\n", inline(n, src))
		}
	case *ast.Paragraph:
		fmt.Fprintf(b, ".PP\n%s\n", inline(n, src))
	case *ast.List:
		for item := n.FirstChild(); item != nil; item = item.NextSibling() {
			b.WriteString(".IP \\(bu 2\n")
			for c := item.FirstChild(); c != nil; c = c.NextSibling() {
				b.WriteString(inline(c, src) + "\n")
			}
		}
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		var code bytes.Buffer
		lines := n.Lines()
		for i := range lines.Len() {
			seg := lines.At(i)
			code.Write(seg.Value(src))
		}
		b.WriteString(".PP\n.RS 4\n.nf\n")
		for _, l := range strings.Split(strings.TrimRight(code.String(), "\n"), "\n") {
			b.WriteString(roffLine(roffText(l)) + "\n")
		}
		b.WriteString(".fi\n.RE\n")
	case *east.Table:
		table(b, n, src)
	}
}

// table renders a table with tbl: the first column, the key or setting, is
// as wide as its widest cell, and the others share the rest and wrap.
func table(b *strings.Builder, t *east.Table, src []byte) {
	cols := len(t.Alignments)
	b.WriteString(".PP\n.TS\nallbox tab(\t);\n")
	b.WriteString(strings.TrimSpace(strings.Repeat("lb ", cols)) + "\n")
	b.WriteString("l" + strings.Repeat(" lx", cols-1) + ".\n")
	for row := t.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		for c := row.FirstChild(); c != nil; c = c.NextSibling() {
			if c == row.FirstChild() {
				cells = append(cells, inline(c, src))
				continue
			}
			cells = append(cells, "T{\n.na\n"+inline(c, src)+"\nT}")
		}
		b.WriteString(strings.Join(cells, "\t") + "\n")
	}
	b.WriteString(".TE\n")
}

// inline renders a block's inline content as one line of roff.
func inline(n ast.Node, src []byte) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			b.WriteString(roffText(string(c.Segment.Value(src))))
			if c.SoftLineBreak() || c.HardLineBreak() {
				b.WriteString(" ")
			}
		case *ast.String:
			b.WriteString(roffText(string(c.Value)))
		case *ast.CodeSpan:
			b.WriteString(`\fB` + inline(c, src) + `\fR`)
		case *ast.Emphasis:
			font := `\fI`
			if c.Level == 2 {
				font = `\fB`
			}
			b.WriteString(font + inline(c, src) + `\fR`)
		case *ast.Link:
			b.WriteString(inline(c, src))
			if dest := string(c.Destination); strings.Contains(dest, "://") {
				b.WriteString(" (" + roffText(dest) + ")")
			}
		case *ast.AutoLink:
			b.WriteString(roffText(string(c.URL(src))))
		default:
			b.WriteString(inline(c, src))
		}
	}
	return strings.TrimSpace(roffLine(b.String()))
}

// roffText escapes text for roff: backslashes, hyphens as the ASCII
// hyphen-minus that commands and options need to copy and paste, and
// anything outside ASCII as a Unicode escape.
func roffText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\e`)
		case r == '-':
			b.WriteString(`\-`)
		case r > 127:
			fmt.Fprintf(&b, `\[u%04X]`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// roffLine keeps a line that starts with . or ' from reading as a request.
func roffLine(s string) string {
	if strings.HasPrefix(s, ".") || strings.HasPrefix(s, "'") {
		return `\&` + s
	}
	return s
}
