package tui

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
)

// Restart reports whether prowl quit to start again on the prowl it
// installed (<leader>U).
func (m *Model) Restart() bool { return m.restart }

// onTip starts the tip offering the running release's notes, which opening
// them clears.
const onTip = "you're on prowl "

// breakingTip leads news of a release whose notes list breaking changes, in
// red where the rest is pink.
const breakingTip = "⚠ breaking changes"

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
			tip = fmt.Sprintf(onTip+"%s · <leader>N what's new", v)
		}
	}
	if tip != "" {
		if r.Breaking && !strings.HasPrefix(tip, onTip) {
			tip = breakingTip + " · " + tip
		}
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
		line := sGreen.Render("✓") + " "
		if m.core.Release(m.exe).Breaking {
			line += sRed.Bold(true).Render(breakingTip) + sPink.Render(" · ")
		}
		m.info(line + sPink.Render("prowl "+msg.installed+" is installed · "+leader+"U restarts into it · "+leader+"N what's new"))
	default:
		m.info(sPink.Render(msg.note))
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
	if strings.HasPrefix(m.tip, onTip) {
		m.tip = ""
		m.layoutNavs()
	}
}

func (p *notesPopup) mode() mode { return modeNormal }

func (p *notesPopup) lines(m *Model) []string {
	return noteLines(p.notes, min(popWidth(m), 84)-4)
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

// Installed is what `prowl update` prints once it installed r.Latest at exe:
// a line saying so, then the release's notes, in cfg's theme and at most w
// cells wide.
func Installed(cfg config.Config, r core.Release, exe string, restarting bool, w int) string {
	applyConfig(cfg)
	out := sGreen.Render("✓") + " " + sBold.Render("prowl "+r.Latest) + " is installed at " + sDim.Render(exe)
	if r.Breaking {
		out += "\n  " + sRed.Bold(true).Render(breakingTip) + sPink.Render(": read what's new below before you go on")
	}
	if restarting {
		out += "\n  " + sDim.Render("the background service restarts into it in a few seconds")
	}
	if ls := noteLines(r.Notes, w); len(ls) > 0 {
		out += "\n\n" + sTitle.Render("What's new in prowl "+r.Latest) + "\n\n" + strings.Join(ls, "\n")
	}
	return out
}

// Uninstalling is what `prowl uninstall` shows first: what it removes, what
// it keeps, and what removes prowl when a package manager owns it, then the
// question when ask is set, in cfg's theme.
func Uninstalling(cfg config.Config, f core.Footprint, ask bool) string {
	applyConfig(cfg)
	home, _ := os.UserHomeDir()
	short := func(p string) string {
		if rest, ok := strings.CutPrefix(p, home); ok && home != "" {
			return "~" + rest
		}
		return p
	}
	var items []string
	if f.Service {
		items = append(items, "background notifications: prowl watch at login")
	}
	for _, dir := range f.Checkouts {
		items = append(items, "prowl's review worktrees and refs in "+short(dir))
	}
	for _, dir := range f.Skills {
		items = append(items, "the prowl-findings skill in "+short(dir))
	}
	for _, dir := range f.Dirs {
		items = append(items, short(dir))
	}
	if f.Binary != "" {
		items = append(items, short(f.Binary))
	}
	var b strings.Builder
	if len(items) == 0 {
		b.WriteString(sTitle.Render("prowl left nothing to remove") + "\n")
	} else {
		b.WriteString(sTitle.Render("prowl uninstall removes") + "\n")
		for _, it := range items {
			b.WriteString("  " + sPink.Render("•") + " " + it + "\n")
		}
	}
	for _, dir := range f.Kept {
		b.WriteString("  " + sDim.Render("It keeps the prowl-findings skill in "+short(dir)+": it differs from this prowl's, from an edit or an older prowl.") + "\n")
	}
	if len(f.Dirs) == 0 {
		b.WriteString("  " + sDim.Render("It keeps your config and data in "+short(config.Dir())+" and "+short(config.DataDir())+"; --purge removes them too.") + "\n")
	}
	if f.Command != "" {
		b.WriteString("  " + sDim.Render("A package manager owns prowl itself: "+f.Command) + "\n")
	}
	if ask && len(items) > 0 {
		b.WriteString("\n" + sYellow.Bold(true).Render("?") + " Remove these? " + sDim.Render("[y/N]") + " ")
	}
	return b.String()
}

var (
	notesVersion = regexp.MustCompile(`^##?\s+\[?v?\d+\.\d+\.\d+.*?(\((\d{4}-\d{2}-\d{2})\))?\s*$`)
	notesSection = regexp.MustCompile(`^###\s+(.+?)\s*$`)
	notesItem    = regexp.MustCompile(`^[*-]\s+(?:\*\*([^*]+):\*\*\s+)?(.+)$`)
	notesCommit  = regexp.MustCompile(`\s*\(\[[0-9a-f]{7,40}\]\([^)]*\)\)`)
	notesPR      = regexp.MustCompile(`\s*\(\[#(\d+)\]\([^)]*\)\)`)
)

// noteTypes are release-please's changelog sections and the commit type
// each holds.
var noteTypes = map[string]string{
	"Features": "feat", "Bug Fixes": "fix", "Performance Improvements": "perf", "Reverts": "revert",
	"Documentation": "docs", "Styles": "style", "Miscellaneous Chores": "chore", "Code Refactoring": "refactor",
	"Tests": "test", "Build System": "build", "Continuous Integration": "ci", "Dependencies": "deps",
}

// noteLines renders release-please's notes like the inbox: each change as a
// conventional commit title in its type's color, with the PR it came from,
// and without the version heading or commit links. Anything added to the
// notes by hand shows as text. Notes with no changes listed render as
// markdown.
func noteLines(notes string, w int) []string {
	var out []string
	typ, items := "", 0
	for l := range strings.Lines(notes) {
		l = strings.TrimSpace(l)
		if l == "" || l == "---" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		if m := notesVersion.FindStringSubmatch(l); m != nil {
			if m[2] != "" {
				out = append(out, sDim.Render("released "+m[2]), "")
			}
			continue
		}
		if m := notesSection.FindStringSubmatch(l); m != nil {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			typ = noteTypes[m[1]]
			switch {
			case strings.Contains(m[1], "BREAKING"):
				typ = "!"
			case typ == "":
				out = append(out, " "+sTitle.Render(m[1]))
			}
			continue
		}
		m := notesItem.FindStringSubmatch(l)
		if m == nil {
			text := codeSpans(notesCommit.ReplaceAllString(l, ""))
			if strings.HasPrefix(l, "#") {
				text = sTitle.Render(strings.TrimLeft(l, "# "))
			}
			for _, wl := range wrap(text, w-1) {
				out = append(out, " "+strings.TrimRight(wl, " "))
			}
			continue
		}
		items++
		subject, pr := notesCommit.ReplaceAllString(m[2], ""), ""
		if p := notesPR.FindStringSubmatch(subject); p != nil {
			subject, pr = notesPR.ReplaceAllString(subject, ""), " "+sDim.Render("#"+p[1])
		}
		label := typeStyle(typ).Render(typ)
		switch typ {
		case "!":
			label = sRed.Bold(true).Render("breaking")
		case "":
			label = sDim.Render("•")
		}
		if m[1] != "" {
			label += sDim.Render("(" + m[1] + ")")
		}
		if typ != "" {
			label += sDim.Render(":")
		}
		title := label + " " + codeSpans(subject)
		for i, wl := range wrap(title+pr, w-3) {
			if i > 0 {
				wl = "  " + wl
			}
			out = append(out, " "+strings.TrimRight(wl, " "))
		}
	}
	if items == 0 {
		return renderMarkdown(notesCommit.ReplaceAllString(notes, ""), w, themeName)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// codeSpans colors the `code` in a line of notes as the markdown views do.
func codeSpans(s string) string {
	fg := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Fg))
	var b strings.Builder
	for i, part := range strings.Split(s, "`") {
		if i%2 == 1 {
			b.WriteString(sGreen.Render(part))
		} else {
			b.WriteString(fg.Render(part))
		}
	}
	return b.String()
}
