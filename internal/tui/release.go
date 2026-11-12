package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tmstorm/prowl/internal/core"
)

// Restart reports whether prowl quit to start again on the prowl it
// installed (<leader>U).
func (m *Model) Restart() bool { return m.restart }

// releaseTip puts news about prowl's own updates under the legend: a
// release installed and waiting for a restart, one to install, or what's new
// in the release now running.
func (m *Model) releaseTip() {
	r := m.core.Release(m.exe)
	tip := ""
	switch {
	case r.Installed:
		tip = fmt.Sprintf("prowl %s is installed · <leader>U restarts into it · <leader>N what's new", r.Latest)
	case r.Newer() && r.Command != "" && m.core.FirstNews(r.Latest):
		tip = fmt.Sprintf("prowl %s is out: %s · <leader>N what's new", r.Latest, r.Command)
	case r.Newer() && r.Command == "" && !m.cfg.AutoUpdate && m.core.FirstNews(r.Latest):
		tip = fmt.Sprintf("prowl %s is out · <leader>U installs it · <leader>N what's new", r.Latest)
	default:
		if v, _ := m.core.WhatsNew(); v != "" {
			tip = fmt.Sprintf("you're on prowl %s · <leader>N what's new", v)
		}
	}
	if tip != "" {
		m.tip = tip
		m.layoutNavs()
	}
}

type upgradeMsg struct {
	installed string // the release prowl installed
	note      string
	err       error
}

// upgrade restarts into an installed release, or checks for a newer one and
// installs it when prowl owns its binary.
func (m *Model) upgrade() tea.Cmd {
	if m.core.Release(m.exe).Installed {
		m.restart = true
		return tea.Quit
	}
	co, exe := m.core, m.exe
	m.info("checking for a newer prowl…")
	return func() tea.Msg {
		if err := co.CheckRelease(true); err != nil {
			return upgradeMsg{err: err}
		}
		r := co.Release(exe)
		switch {
		case r.Running == "":
			return upgradeMsg{note: "this prowl (" + core.BuildVersion() + ") was built from source; it doesn't update itself"}
		case r.Installed:
			return upgradeMsg{installed: r.Latest}
		case !r.Newer():
			return upgradeMsg{note: "prowl " + r.Running + " is the newest release"}
		case r.Command != "":
			return upgradeMsg{note: "prowl " + r.Latest + " is out: " + r.Command}
		}
		v, err := co.Upgrade(exe)
		return upgradeMsg{installed: v, err: err}
	}
}

func (m *Model) upgraded(msg upgradeMsg) {
	switch {
	case msg.err != nil:
		m.fail("update: " + msg.err.Error())
	case msg.installed != "":
		m.releaseTip()
		m.info("prowl " + msg.installed + " is installed · " + leader + "U restarts into it")
	default:
		m.info(msg.note)
	}
}

// notesPopup shows a release's notes.
type notesPopup struct {
	title, url string
	notes      string
	top        int
}

func (m *Model) whatsNew() {
	r := m.core.Release(m.exe)
	if strings.TrimSpace(r.Notes) == "" {
		m.fail("no release notes yet: prowl checks for a new release once a day (" + leader + "U checks now)")
		return
	}
	m.popup = &notesPopup{title: "What's new in prowl " + r.Latest, url: r.URL, notes: r.Notes}
}

func (p *notesPopup) mode() mode { return modeNormal }

func (p *notesPopup) lines(m *Model) []string {
	return renderMarkdown(p.notes, min(popWidth(m), 84)-4, themeName)
}

func (p *notesPopup) height(m *Model) int { return max(m.h-12, 5) }

func (p *notesPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "j", "down":
		p.top = min(p.top+1, max(len(p.lines(m))-p.height(m), 0))
	case "k", "up":
		p.top = max(p.top-1, 0)
	case "o":
		m.popup = nil
		return openURL(p.url)
	default:
		m.popup = nil
	}
	return nil
}

func (p *notesPopup) view(m *Model) string {
	ls := p.lines(m)
	shown := ls[min(p.top, len(ls)):min(p.top+p.height(m), len(ls))]
	foot := "o opens the release on GitHub · any other key closes"
	if len(ls) > p.height(m) {
		foot = "j/k scroll · " + foot
	}
	return popBox(p.title, strings.Join(shown, "\n")+"\n\n"+sDim.Render(foot), min(popWidth(m), 84), pal.Accent)
}
