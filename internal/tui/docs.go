package tui

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/tmstorm/prowl/internal/core"
)

// docsPopup reads prowl's built-in docs: one page at a time with a cursor
// line, links to follow, and a search over every page.
type docsPopup struct {
	pages          []core.DocPage
	page, cur, top int
	back           []docsSpot // where links were followed from, for backspace
	searching      bool
	input          textinput.Model
	hits           []docsSpot
	hit            int
}

type docsSpot struct{ page, line int }

// docLink is a link to another page as the renderer writes it, e.g.
// "/slack.md" or "/ai-reviews.md#running-tests".
var docLink = regexp.MustCompile(`/([a-z][\w-]*)\.md(?:#([\w-]+))?`)

func newDocsPopup() *docsPopup {
	p := &docsPopup{pages: core.Docs()}
	p.input = textinput.New()
	p.input.Prompt = sTitle.Render(" ") + " "
	p.input.Placeholder = "search the docs"
	p.input.Width = 50
	return p
}

func (p *docsPopup) width(m *Model) int  { return max(min(m.w-6, 110), 40) }
func (p *docsPopup) height(m *Model) int { return max(m.h-11, 5) }

func (p *docsPopup) lines(m *Model, page int) []string {
	return renderMarkdown(p.pages[page].Markdown, p.width(m)-4, m.cfg.Theme)
}

func (p *docsPopup) mode() mode {
	if p.searching {
		return modeInsert
	}
	return modeNormal
}

func (p *docsPopup) show(page, line int) {
	p.page, p.cur, p.top = page, line, max(line-2, 0)
}

func (p *docsPopup) forward(_ *Model, msg tea.Msg) tea.Cmd {
	if !p.searching {
		return nil
	}
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	return cmd
}

func (p *docsPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	if p.searching {
		return p.updateSearch(m, k)
	}
	n := len(p.lines(m, p.page))
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
		p.searching = true
		return p.input.Focus()
	case "esc", "q":
		m.popup = nil
	}
	return nil
}

// follow opens the page a link on the cursor line points to, at its
// heading when it names one.
func (p *docsPopup) follow(m *Model) {
	for _, l := range docLink.FindAllStringSubmatch(xansi.Strip(p.lines(m, p.page)[p.cur]), -1) {
		page := slices.IndexFunc(p.pages, func(d core.DocPage) bool { return d.Name == l[1] })
		if page < 0 {
			continue
		}
		line := 0
		if section, err := core.DocSection(l[1], l[2]); l[2] != "" && err == nil {
			first, _, _ := strings.Cut(section, "\n")
			heading := strings.TrimSpace(strings.TrimLeft(first, "#"))
			line = max(slices.IndexFunc(p.lines(m, page), func(r string) bool { return strings.TrimSpace(xansi.Strip(r)) == heading }), 0)
		}
		p.back = append(p.back, docsSpot{p.page, p.cur})
		p.show(page, line)
		return
	}
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
		before := p.input.Value()
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(k)
		if p.input.Value() != before {
			p.search(m)
		}
		return cmd
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
	for i := range shown {
		shown[i] = fit(shown[i], w-4)
		if p.top+i == p.cur {
			shown[i] = highlight(shown[i], w-4, sTitle)
		}
	}
	foot := sDim.Render(fmt.Sprintf(" j/k move · tab page · enter follow link · backspace back · / search · esc close   %d/%d", p.cur+1, len(lines)))
	return popBox("prowl docs · "+p.pages[p.page].Title, head+"\n"+strings.Join(shown, "\n")+"\n\n"+foot, w, pal.Accent)
}
