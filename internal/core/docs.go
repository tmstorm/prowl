package core

import (
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"unicode"

	"github.com/tmstorm/prowl/docs"
)

// DocPage is one page of prowl's docs.
type DocPage struct {
	Name     string // e.g. "slack", from slack.md
	Title    string // its first heading
	Markdown string
}

// docOrder is the reading order; a page not in it comes after, by name.
var docOrder = []string{"inbox", "reviewing", "ai-reviews", "slack", "running", "config"}

// Docs are the pages of prowl's docs, built into the binary, in reading
// order.
func Docs() []DocPage {
	files, _ := fs.Glob(docs.Pages, "*.md")
	var out []DocPage
	for _, f := range files {
		b, err := fs.ReadFile(docs.Pages, f)
		if err != nil {
			continue
		}
		p := DocPage{Name: strings.TrimSuffix(f, ".md"), Markdown: string(b)}
		first, _, _ := strings.Cut(p.Markdown, "\n")
		p.Title = strings.TrimSpace(strings.TrimLeft(first, "#"))
		out = append(out, p)
	}
	rank := func(name string) int {
		if i := slices.Index(docOrder, name); i >= 0 {
			return i
		}
		return len(docOrder)
	}
	slices.SortStableFunc(out, func(a, b DocPage) int {
		if rank(a.Name) != rank(b.Name) {
			return rank(a.Name) - rank(b.Name)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// Doc is one page by name, e.g. "slack".
func Doc(name string) (DocPage, bool) {
	i := slices.IndexFunc(Docs(), func(p DocPage) bool { return p.Name == strings.TrimSuffix(name, ".md") })
	if i < 0 {
		return DocPage{}, false
	}
	return Docs()[i], true
}

// docHeading is a heading line in a page, outside code blocks.
type docHeading struct {
	line, level int
	text        string
}

func headings(markdown string) []docHeading {
	var out []docHeading
	fenced := false
	for i, l := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(l, "```") {
			fenced = !fenced
		}
		level := len(l) - len(strings.TrimLeft(l, "#"))
		if !fenced && level > 0 && strings.HasPrefix(l[level:], " ") {
			out = append(out, docHeading{line: i, level: level, text: strings.TrimSpace(l[level:])})
		}
	}
	return out
}

// DocAnchor is the anchor GitHub gives a heading, e.g. "running-tests" for
// "Running tests".
func DocAnchor(heading string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == ' ':
			return '-'
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			return unicode.ToLower(r)
		}
		return -1
	}, heading)
}

// DocSection is the part of a page under a heading, given by its text or
// its anchor, down to the next heading of the same level or higher.
func DocSection(name, heading string) (string, error) {
	p, ok := Doc(name)
	if !ok {
		return "", fmt.Errorf("no docs page %q", name)
	}
	lines := strings.Split(p.Markdown, "\n")
	hs := headings(p.Markdown)
	for i, h := range hs {
		if !strings.EqualFold(h.text, heading) && h.text != heading && DocAnchor(h.text) != DocAnchor(heading) {
			continue
		}
		end := len(lines)
		for _, next := range hs[i+1:] {
			if next.level <= h.level {
				end = next.line
				break
			}
		}
		return strings.TrimSpace(strings.Join(lines[h.line:end], "\n")), nil
	}
	return "", fmt.Errorf("no section %q in %s", heading, p.Name)
}

// DocHit is a line of the docs that matches a search.
type DocHit struct {
	Page    string // its page's name
	Heading string // the section it is in, "" before the first
	Line    string
}

// SearchDocs finds the lines that hold every word of q, ignoring case.
func SearchDocs(q string) []DocHit {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return nil
	}
	var out []DocHit
	for _, p := range Docs() {
		heading := ""
		hs := headings(p.Markdown)
		for i, l := range strings.Split(p.Markdown, "\n") {
			if j := slices.IndexFunc(hs, func(h docHeading) bool { return h.line == i }); j >= 0 {
				heading = hs[j].text
			}
			low := strings.ToLower(l)
			if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(low, w) }) {
				out = append(out, DocHit{Page: p.Name, Heading: heading, Line: strings.TrimSpace(l)})
			}
		}
	}
	return out
}
