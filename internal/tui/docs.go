package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/tmstorm/prowl/docs"
	"github.com/tmstorm/prowl/internal/platform"
)

// docsPopup reads prowl's built-in docs: one page at a time with a cursor
// line, links to follow, and a search over every page.
type docsPopup struct {
	prev      popup // what esc goes back to
	pages     []docs.Page
	page      int
	nav            // over the page's lines
	visual    bool // v: lines from anchor to cur are selected
	anchor    int
	yanking   bool // y pressed once on line yankAt: a second y there copies it
	yankAt    int
	back      []docsSpot // where links were followed from, for backspace
	searching bool
	input     filterInput
	hits      []docsSpot
	hit       nav
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
	p := &docsPopup{pages: docs.All(), input: newFilterInput("search the docs", 50)}
	p.scrolloff = 2
	return p
}

// openDocs opens the docs at a topic's tag. A tag with several places lists
// them to pick from, and a topic with no tag is searched for.
func (m *Model) openDocs(topic string) tea.Cmd {
	p := newDocsPopup()
	p.prev, m.popup = m.popup, p
	if topic == "" {
		return nil
	}
	tags := docs.Find(topic)
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
func (p *docsPopup) spot(m *Model, t docs.Tag) docsSpot {
	page := slices.IndexFunc(p.pages, func(d docs.Page) bool { return d.Name == t.Page })
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
	return gutter(slices.Clone(renderMarkdown(md, p.width(m)-5)))
}

// list is the page, or in a search its results, which start below the
// title, the page names and their rule, and the search's input.
func (p *docsPopup) list(m *Model) (*nav, int) {
	if p.searching {
		p.hit.setLen(len(p.hits), p.height(m)-1)
		return &p.hit, 6
	}
	p.setLen(len(p.lines(m, p.page)), p.height(m))
	return &p.nav, 5
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
	m.info("copied " + count(len(out), "line"))
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
	yanking := p.yanking && p.yankAt == p.cur
	p.yanking = false
	switch k.String() {
	case "tab", "l", "right":
		p.show((p.page+1)%len(p.pages), 0)
	case "shift+tab", "h", "left":
		p.show((p.page+len(p.pages)-1)%len(p.pages), 0)
	case "enter":
		p.followLink(m)
	case "backspace":
		if len(p.back) > 0 {
			s := p.back[len(p.back)-1]
			p.back = p.back[:len(p.back)-1]
			p.show(s.page, s.line)
		}
	case "/":
		p.searching, p.hits, p.hit.cur, p.visual = true, nil, 0, false
		p.input.SetValue("")
		return p.input.Focus()
	case "v":
		p.visual, p.anchor = !p.visual, p.cur
	case "y":
		if p.visual || yanking {
			p.yank(m)
		} else {
			p.yanking, p.yankAt = true, p.cur
		}
	case "esc", "q", "ctrl+c":
		if p.visual {
			p.visual = false
			return nil
		}
		m.popup = p.prev
	}
	return nil
}

// followLink opens what the first link on the cursor line points to, at its
// heading when it names one. The page is read as one text, so a label
// wrapped across lines counts, and a label used for several links is told
// apart by how many came before it.
func (p *docsPopup) followLink(m *Model) {
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
	page := slices.IndexFunc(p.pages, func(d docs.Page) bool { return d.Name == name })
	if !ok || page < 0 {
		return
	}
	at := 0
	if section, err := docs.Section(name, anchor); anchor != "" && err == nil {
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
	case "esc", "ctrl+c":
		p.searching = false
		p.input.Blur()
	case "enter":
		p.searching = false
		p.input.Blur()
		if p.hit.cur < len(p.hits) {
			p.back = append(p.back, docsSpot{p.page, p.cur})
			p.show(p.hits[p.hit.cur].page, p.hits[p.hit.cur].line)
		}
	default:
		return p.input.key(&p.hit, k, func() { p.search(m) })
	}
	return nil
}

// search finds the lines, on every page, that hold every word typed.
func (p *docsPopup) search(m *Model) {
	words := strings.Fields(strings.ToLower(p.input.Value()))
	p.hits, p.hit.cur = nil, 0
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

// click opens the page whose name is clicked, or selects a line or search
// result and goes to it when it is clicked again, as enter does. x and y
// count from the popup's border.
func (p *docsPopup) click(m *Model, x, y, row int) tea.Cmd {
	if y != 3 {
		v, _ := p.list(m)
		return m.pick(v, row)
	}
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

func (p *docsPopup) view(m *Model) string {
	w := p.width(m)
	v, _ := p.list(m)
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
		for i := v.top; i < min(v.top+v.h, v.n); i++ {
			s := p.hits[i]
			row := fit(" "+sDim.Render(fit(p.pages[s.page].Name, 12))+strings.TrimSpace(xansi.Strip(p.lines(m, s.page)[s.line])), w-4)
			if i == v.cur {
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
	lines := p.lines(m, p.page)
	shown := lines[p.top:min(p.top+p.h, p.n)]
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
