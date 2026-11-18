// Package docs holds prowl's docs pages, built into the binary so prowl can
// show the ones for its own version, and finds topics, sections and lines
// in them for :help, K, prowl docs and the chat's docs tool.
package docs

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// pages are the docs pages. install.md isn't one: it's for before prowl is
// installed, so it lives on GitHub only.
//
//go:embed inbox.md reviewing.md ai-reviews.md slack.md tui.md cli.md config.md
var pages embed.FS

// Page is one page of prowl's docs.
type Page struct {
	Name     string // e.g. "slack", from slack.md
	Title    string // its first heading
	Markdown string
}

// order is the reading order; a page not in it comes after, by name.
var order = []string{"inbox", "reviewing", "ai-reviews", "slack", "tui", "cli", "config"}

// All are the pages of prowl's docs, built into the binary, in reading
// order.
func All() []Page {
	files, _ := fs.Glob(pages, "*.md")
	var out []Page
	for _, f := range files {
		b, err := fs.ReadFile(pages, f)
		if err != nil {
			continue
		}
		p := Page{Name: strings.TrimSuffix(f, ".md"), Markdown: string(b)}
		first, _, _ := strings.Cut(p.Markdown, "\n")
		p.Title = strings.TrimSpace(strings.TrimLeft(first, "#"))
		out = append(out, p)
	}
	rank := func(name string) int {
		if i := slices.Index(order, name); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(out, func(a, b Page) int {
		if rank(a.Name) != rank(b.Name) {
			return rank(a.Name) - rank(b.Name)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// Get is one page by name, e.g. "slack".
func Get(name string) (Page, bool) {
	i := slices.IndexFunc(All(), func(p Page) bool { return p.Name == strings.TrimSuffix(name, ".md") })
	if i < 0 {
		return Page{}, false
	}
	return All()[i], true
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

// Anchor is the anchor GitHub gives a heading, e.g. "running-tests" for
// "Running tests".
func Anchor(heading string) string {
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

// Section is the part of a page under a heading, given by its text or
// its anchor, down to the next heading of the same level or higher.
func Section(name, heading string) (string, error) {
	p, ok := Get(name)
	if !ok {
		return "", fmt.Errorf("no docs page %q", name)
	}
	lines := strings.Split(p.Markdown, "\n")
	hs := headings(p.Markdown)
	for i, h := range hs {
		if !strings.EqualFold(h.text, heading) && h.text != heading && Anchor(h.text) != Anchor(heading) {
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

// Tag is a place in the docs a topic leads to, like a tag in vim's help:
// a page by its name, a heading by its text or anchor, or a table row by a
// code span in its first column.
type Tag struct {
	Tag     string // e.g. "slack", "Running tests", "running-tests", "stale_after"
	Page    string
	Heading string // the section it is in; a heading's is itself
	Line    int    // its line in the page's Markdown
}

var codeSpan = regexp.MustCompile("`([^`]+)`")

// Tags are every tag in the docs, in reading order.
func Tags() []Tag {
	var out []Tag
	for _, p := range All() {
		out = append(out, Tag{Tag: p.Name, Page: p.Name, Heading: p.Title})
		hs := headings(p.Markdown)
		heading, fenced, rows := p.Title, false, false
		for i, l := range strings.Split(p.Markdown, "\n") {
			if strings.HasPrefix(l, "```") {
				fenced = !fenced
			}
			if j := slices.IndexFunc(hs, func(h docHeading) bool { return h.line == i }); j >= 0 {
				heading = hs[j].text
				out = append(out, Tag{Tag: heading, Page: p.Name, Heading: heading, Line: i})
				if a := Anchor(heading); a != strings.ToLower(heading) {
					out = append(out, Tag{Tag: a, Page: p.Name, Heading: heading, Line: i})
				}
			}
			if fenced || !strings.HasPrefix(l, "|") {
				rows = false
				continue
			}
			if strings.HasPrefix(l, "| ---") {
				rows = true
				continue
			}
			if !rows {
				continue
			}
			first := strings.Split(l, "|")[1]
			spans := codeSpan.FindAllStringSubmatch(first, -1)
			if len(spans) == 0 {
				spans = [][]string{{"", strings.TrimSpace(first)}}
			}
			for _, s := range spans {
				out = append(out, Tag{Tag: s[1], Page: p.Name, Heading: heading, Line: i})
			}
		}
	}
	return out
}

// Markdown is what a tag leads to: its heading's section, or its table row
// under the table's header and the heading it is in.
func (t Tag) Markdown() string {
	p, _ := Get(t.Page)
	lines := strings.Split(p.Markdown, "\n")
	if !strings.HasPrefix(lines[t.Line], "|") {
		s, _ := Section(t.Page, t.Heading)
		return s
	}
	start := t.Line
	for start > 0 && strings.HasPrefix(lines[start-1], "|") {
		start--
	}
	return "## " + t.Heading + "\n\n" + lines[start] + "\n" + lines[start+1] + "\n" + lines[t.Line]
}

// Find is where a topic leads: the places of the tag that is the topic,
// in the same case when one is and in any case otherwise, or else of the
// shortest tag that starts with it. Nothing means no tag fits.
func Find(topic string) []Tag {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return nil
	}
	tags := Tags()
	var exact, folded []Tag
	best := ""
	for _, t := range tags {
		switch {
		case t.Tag == topic:
			exact = append(exact, t)
		case strings.EqualFold(t.Tag, topic):
			folded = append(folded, t)
		case strings.HasPrefix(strings.ToLower(t.Tag), strings.ToLower(topic)) && (best == "" || len(t.Tag) < len(best)):
			best = t.Tag
		}
	}
	out := exact
	if len(out) == 0 {
		out = folded
	}
	if len(out) == 0 && best != "" {
		out = slices.DeleteFunc(tags, func(t Tag) bool { return t.Tag != best })
	}
	return slices.CompactFunc(out, func(a, b Tag) bool { return a.Page == b.Page && a.Line == b.Line })
}

// Topic is the Markdown a topic leads to, each place of its tag in turn,
// or "" when no tag fits.
func Topic(topic string) string {
	var parts []string
	for _, t := range Find(topic) {
		parts = append(parts, t.Markdown())
	}
	return strings.Join(parts, "\n\n")
}

// Text is what the docs say about a topic, for an agent to read: what
// its tag leads to, or else the lines that mention it and their sections. An
// empty topic lists the pages.
func Text(topic string) string {
	if strings.TrimSpace(topic) == "" {
		var b strings.Builder
		for _, p := range All() {
			fmt.Fprintf(&b, "%s: %s\n", p.Name, p.Title)
		}
		return b.String()
	}
	if md := Topic(topic); md != "" {
		return md
	}
	hits := Search(topic)
	if len(hits) == 0 {
		return fmt.Sprintf("Nothing in prowl's docs mentions %q.", topic)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "No topic %q. Lines that mention it, as page › section: line:\n\n", topic)
	for _, h := range hits[:min(len(hits), 30)] {
		fmt.Fprintf(&b, "%s › %s: %s\n", h.Page, h.Heading, h.Line)
	}
	return b.String()
}

// Hit is a line of the docs that matches a search.
type Hit struct {
	Page    string // its page's name
	Heading string // the section it is in, "" before the first
	Line    string
}

// Search finds the lines that hold every word of q, ignoring case.
func Search(q string) []Hit {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return nil
	}
	var out []Hit
	for _, p := range All() {
		heading := ""
		hs := headings(p.Markdown)
		for i, l := range strings.Split(p.Markdown, "\n") {
			if j := slices.IndexFunc(hs, func(h docHeading) bool { return h.line == i }); j >= 0 {
				heading = hs[j].text
			}
			low := strings.ToLower(l)
			if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(low, w) }) {
				out = append(out, Hit{Page: p.Name, Heading: heading, Line: strings.TrimSpace(l)})
			}
		}
	}
	return out
}
