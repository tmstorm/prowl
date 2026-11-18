package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/platform"
)

// docsPopup reads prowl's built-in docs: one page at a time with a cursor
// line, links to follow, and a search over every page.
type docsPopup struct {
	prev           popup // what esc goes back to
	pages          []core.DocPage
	page, cur, top int
	visual         bool // v: lines from anchor to cur are selected
	anchor         int
	yanking        bool       // y pressed once: a second y copies the line
	back           []docsSpot // where links were followed from, for backspace
	searching      bool
	input          filterInput
	hits           []docsSpot
	hit            int
}

type docsSpot struct{ page, line int }

// docLink is a link within the docs in a page's Markdown, e.g.
// [Running tests](ai-reviews.md#running-tests) or [Updating](#updating).
// The viewer shows it as its label in brackets, like vim help's |tag|: the
// renderer would drop the target of a link within a page, move a table's
// links under the table, and wrap long targets. The label's spaces don't
// break, so a label in a table cell stays on one line.
var docLink = regexp.MustCompile(`\[([^\[\]\n]+)\]\(([a-z][\w-]*\.md(?:#[\w-]+)?|#[\w-]+)\)`)

// linkLabel is a link's label as the viewer shows it, without code marks.
func linkLabel(md string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(md, "`", "")), " ")
}

func newDocsPopup() *docsPopup {
	return &docsPopup{pages: core.Docs(), input: newFilterInput("search the docs", 50)}
}

// openDocs opens the docs at a topic's tag. A tag with several places lists
// them to pick from, and a topic with no tag is searched for.
func (m *Model) openDocs(topic string) tea.Cmd {
	p := newDocsPopup()
	p.prev, m.popup = m.popup, p
	if topic == "" {
		return nil
	}
	tags := core.FindDocTag(topic)
	if len(tags) == 1 {
		s := p.spot(m, tags[0])
		p.show(s.page, s.line)
		return nil
	}
	p.searching = true
	p.input.SetValue(topic)
	p.search(m)
	if len(tags) > 1 {
		p.hits = nil
		for _, t := range tags {
			p.hits = append(p.hits, p.spot(m, t))
		}
	}
	return p.input.Focus()
}

// spot is where a tag is in the viewer: its heading, or the first line of its
// table row under that heading.
func (p *docsPopup) spot(m *Model, t core.DocTag) docsSpot {
	page := slices.IndexFunc(p.pages, func(d core.DocPage) bool { return d.Name == t.Page })
	lines := p.lines(m, page)
	at := headingLine(lines, t.Heading)
	row := strings.Split(p.pages[page].Markdown, "\n")[t.Line]
	if !strings.HasPrefix(row, "|") {
		return docsSpot{page, at}
	}
	first := strings.Fields(linkLabel(strings.Split(row, "|")[1]))[0]
	for i := at; i < len(lines); i++ {
		l := strings.TrimSpace(xansi.Strip(lines[i]))
		if strings.HasPrefix(l, first+" ") && strings.Contains(l, "│") {
			return docsSpot{page, i}
		}
	}
	return docsSpot{page, at}
}

// headingLine is the line of a rendered page that shows a heading, or 0.
func headingLine(lines []string, heading string) int {
	return max(slices.IndexFunc(lines, func(r string) bool { return strings.TrimLeft(strings.TrimSpace(xansi.Strip(r)), "# ") == heading }), 0)
}

func (p *docsPopup) width(m *Model) int  { return max(min(m.w-6, 110), 40) }
func (p *docsPopup) height(m *Model) int { return max(m.h-11, 5) }

func (p *docsPopup) lines(m *Model, page int) []string {
	md := docLink.ReplaceAllStringFunc(p.pages[page].Markdown, func(l string) string {
		return "[" + strings.ReplaceAll(docLink.FindStringSubmatch(l)[1], " ", "\u00a0") + "]"
	})
	return gutter(slices.Clone(renderMarkdown(md, p.width(m)-5, m.cfg.Theme)))
}

func (p *docsPopup) mode() mode {
	switch {
	case p.searching:
		return modeInsert
	case p.visual:
		return modeVisual
	}
	return modeNormal
}

func (p *docsPopup) show(page, line int) {
	p.page, p.cur, p.top, p.visual = page, line, max(line-2, 0), false
}

// selection is the lines v covers, or the cursor's line alone.
func (p *docsPopup) selection() (int, int) {
	if p.visual {
		return min(p.anchor, p.cur), max(p.anchor, p.cur)
	}
	return p.cur, p.cur
}

// yank copies the selected lines as they read on screen. A wider terminal
// makes a page shorter, so the selection may run past its end.
func (p *docsPopup) yank(m *Model) {
	lines := p.lines(m, p.page)
	a, b := p.selection()
	b = min(b, len(lines)-1)
	a = min(a, b)
	p.visual = false
	var out []string
	for _, l := range lines[a : b+1] {
		out = append(out, strings.TrimRight(strings.ReplaceAll(strings.TrimPrefix(xansi.Strip(l), " "), "\u00a0", " "), " "))
	}
	if err := platform.Copy(strings.Join(out, "\n")); err != nil {
		m.fail(err.Error())
		return
	}
	if len(out) == 1 {
		m.info("copied 1 line")
	} else {
		m.info(fmt.Sprintf("copied %d lines", len(out)))
	}
}

func (p *docsPopup) forward(m *Model, msg tea.Msg) tea.Cmd {
	if !p.searching {
		return nil
	}
	return p.input.update(msg, func() { p.search(m) })
}

func (p *docsPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	if p.searching {
		return p.updateSearch(m, k)
	}
	n := len(p.lines(m, p.page))
	yanking := p.yanking
	p.yanking = false
	switch k.String() {
	case "j", "down":
		p.cur = min(p.cur+1, n-1)
	case "k", "up":
		p.cur = max(p.cur-1, 0)
	case "ctrl+d":
		p.cur = min(p.cur+p.height(m)/2, n-1)
	case "ctrl+u":
		p.cur = max(p.cur-p.height(m)/2, 0)
	case "g", "home":
		p.cur = 0
	case "G", "end":
		p.cur = n - 1
	case "tab", "l", "right":
		p.show((p.page+1)%len(p.pages), 0)
	case "shift+tab", "h", "left":
		p.show((p.page+len(p.pages)-1)%len(p.pages), 0)
	case "enter":
		p.follow(m)
	case "backspace":
		if len(p.back) > 0 {
			s := p.back[len(p.back)-1]
			p.back = p.back[:len(p.back)-1]
			p.show(s.page, s.line)
		}
	case "/":
		p.searching, p.hits, p.hit, p.visual = true, nil, 0, false
		p.input.SetValue("")
		return p.input.Focus()
	case "v":
		p.visual, p.anchor = !p.visual, p.cur
	case "y":
		if p.visual || yanking {
			p.yank(m)
		} else {
			p.yanking = true
		}
	case "esc", "q":
		if p.visual {
			p.visual = false
			return nil
		}
		m.popup = p.prev
	}
	return nil
}

// follow opens what the first link on the cursor line points to, at its
// heading when it names one. The page is read as one text, so a label
// wrapped across lines counts, and a label used for several links is told
// apart by how many came before it.
func (p *docsPopup) follow(m *Model) {
	var b strings.Builder
	start, end := 0, 0
	for i, l := range p.lines(m, p.page) {
		if i == p.cur {
			start = b.Len()
		}
		b.WriteString(strings.Join(strings.Fields(xansi.Strip(l)), " "))
		if i == p.cur {
			end = b.Len()
		}
		b.WriteString(" ")
	}
	text := b.String()
	first, label, nth := -1, "", 0
	for _, l := range docLink.FindAllStringSubmatch(p.pages[p.page].Markdown, -1) {
		shown := "[" + linkLabel(l[1]) + "]"
		for n, from := 0, 0; ; n++ {
			i := strings.Index(text[from:], shown)
			if i < 0 {
				break
			}
			i += from
			from = i + 1
			if i < end && i+len(shown) > start && (first < 0 || i < first) {
				first, label, nth = i, linkLabel(l[1]), n
			}
		}
	}
	name, anchor, ok := p.target(label, nth)
	page := slices.IndexFunc(p.pages, func(d core.DocPage) bool { return d.Name == name })
	if !ok || page < 0 {
		return
	}
	at := 0
	if section, err := core.DocSection(name, anchor); anchor != "" && err == nil {
		top, _, _ := strings.Cut(section, "\n")
		at = headingLine(p.lines(m, page), strings.TrimLeft(top, "# "))
	}
	p.back = append(p.back, docsSpot{p.page, p.cur})
	p.show(page, at)
}

// target is the page and anchor the nth link labelled label on the current
// page points to, counting from 0.
func (p *docsPopup) target(label string, nth int) (page, anchor string, ok bool) {
	for _, l := range docLink.FindAllStringSubmatch(p.pages[p.page].Markdown, -1) {
		if linkLabel(l[1]) != label {
			continue
		}
		if nth > 0 {
			nth--
			continue
		}
		page, anchor, _ = strings.Cut(l[2], "#")
		if page == "" {
			return p.pages[p.page].Name, anchor, true
		}
		return strings.TrimSuffix(page, ".md"), anchor, true
	}
	return "", "", false
}

func (p *docsPopup) updateSearch(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		p.searching = false
		p.input.Blur()
	case "down", "ctrl+n", "ctrl+j":
		p.hit = min(p.hit+1, max(len(p.hits)-1, 0))
	case "up", "ctrl+p", "ctrl+k":
		p.hit = max(p.hit-1, 0)
	case "enter":
		p.searching = false
		p.input.Blur()
		if p.hit < len(p.hits) {
			p.back = append(p.back, docsSpot{p.page, p.cur})
			p.show(p.hits[p.hit].page, p.hits[p.hit].line)
		}
	default:
		return p.forward(m, k)
	}
	return nil
}

// search finds the lines, on every page, that hold every word typed.
func (p *docsPopup) search(m *Model) {
	words := strings.Fields(strings.ToLower(p.input.Value()))
	p.hits, p.hit = nil, 0
	if len(words) == 0 {
		return
	}
	for page := range p.pages {
		for i, l := range p.lines(m, page) {
			low := strings.ToLower(xansi.Strip(l))
			if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(low, w) }) {
				p.hits = append(p.hits, docsSpot{page, i})
			}
		}
	}
}

// bodyTop is the popup line, from the top border, of the first page or
// search-result line: below the title, the page names and their rule, and
// in a search the input.
func (p *docsPopup) bodyTop() int {
	if p.searching {
		return 6
	}
	return 5
}

// click opens the page whose name is clicked, selects a line, or follows the
// selected line's link when it is clicked again (as enter does). In a search
// it does the same for a result. x and y count from the popup's border.
func (p *docsPopup) click(m *Model, x, y int) tea.Cmd {
	if y == 3 {
		at := 3
		for i, d := range p.pages {
			if x >= at && x < at+len(d.Name) {
				p.searching = false
				p.input.Blur()
				p.show(i, 0)
				return nil
			}
			at += len(d.Name) + 3
		}
		return nil
	}
	if p.searching {
		i := max(p.hit-p.height(m)+2, 0) + y - p.bodyTop()
		if i < 0 || i >= len(p.hits) {
			return nil
		}
		if i == p.hit {
			return p.updateSearch(m, tea.KeyMsg{Type: tea.KeyEnter})
		}
		p.hit = i
		return nil
	}
	i := p.top + y - p.bodyTop()
	if i < p.top || i >= min(p.top+p.height(m), len(p.lines(m, p.page))) {
		return nil
	}
	if i == p.cur {
		p.follow(m)
		return nil
	}
	p.cur = i
	return nil
}

// wheel scrolls the page three lines at a time, taking the cursor along
// only when it would leave the window. In a search it moves the selection.
func (p *docsPopup) wheel(m *Model, d int) {
	if p.searching {
		p.hit = min(max(p.hit+d, 0), max(len(p.hits)-1, 0))
		return
	}
	h, n := p.height(m), len(p.lines(m, p.page))
	p.top = min(max(p.top+3*d, 0), max(n-h, 0))
	p.cur = min(max(p.cur, p.top), p.top+h-1, n-1)
}

func (p *docsPopup) view(m *Model) string {
	w, h := p.width(m), p.height(m)
	var tabs []string
	for i, d := range p.pages {
		if i == p.page {
			tabs = append(tabs, sTitle.Render(d.Name))
		} else {
			tabs = append(tabs, sDim.Render(d.Name))
		}
	}
	head := fit(" "+strings.Join(tabs, sDim.Render(" · ")), w-4) + "\n" + sBorder.Render(strings.Repeat("─", w-4))
	if p.searching {
		var rows []string
		for i := max(p.hit-h+2, 0); i < len(p.hits) && len(rows) < h-1; i++ {
			s := p.hits[i]
			row := fit(" "+sDim.Render(fit(p.pages[s.page].Name, 12))+strings.TrimSpace(xansi.Strip(p.lines(m, s.page)[s.line])), w-4)
			if i == p.hit {
				row = highlight(row, w-4, sTitle)
			}
			rows = append(rows, row)
		}
		if len(rows) == 0 && p.input.Value() != "" {
			rows = append(rows, sDim.Render(" nothing found"))
		}
		foot := sDim.Render(" ↑↓ move · enter go there · esc back to the page")
		return popBox("prowl docs", head+"\n"+p.input.View()+"\n"+strings.Join(rows, "\n")+"\n\n"+foot, w, pal.Accent)
	}
	lines := slices.Clone(p.lines(m, p.page))
	p.cur = min(p.cur, len(lines)-1)
	p.top = min(max(p.top, p.cur-h+1), p.cur)
	p.top = max(min(p.top, len(lines)-h), 0)
	shown := lines[p.top:min(p.top+h, len(lines))]
	a, b := p.selection()
	for i := range shown {
		shown[i] = fit(shown[i], w-4)
		switch {
		case p.visual && p.top+i >= a && p.top+i <= b:
			shown[i] = highlight(shown[i], w-4, sPink)
		case p.top+i == p.cur:
			shown[i] = highlight(shown[i], w-4, sTitle)
		}
	}
	foot := sDim.Render(fmt.Sprintf(" j/k move · tab page · enter follow link · backspace back · / search · v y copy · esc close   %d/%d", p.cur+1, len(lines)))
	return popBox("prowl docs · "+p.pages[p.page].Title, head+"\n"+strings.Join(shown, "\n")+"\n\n"+foot, w, pal.Accent)
}
