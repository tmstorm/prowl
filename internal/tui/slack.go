package tui

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/slack"
)

// nameRow is one GitHub login in the Slack names popup.
type nameRow struct {
	login  string
	user   slack.User // the proposed, picked or mapped Slack user; zero for none
	why    string     // why it was proposed, "" once mapped
	mapped bool       // slack_names maps it to user already
	accept bool       // save user for it
	clear  bool       // save it without a Slack user
}

// namesPopup reviews the Slack user of each GitHub login: proposals to
// accept, logins nothing matched, and the ones mapped already.
type namesPopup struct {
	rows    []nameRow
	cur     int
	top     int // the first line shown, once the list is taller than the popup
	users   []slack.User
	picking bool // choosing a Slack user for the current row
	input   textinput.Model
	results []slack.User
	pick    int
	back    *optionsPopup        // the options to return to, if they were open
	then    func(*Model) tea.Cmd // what to do after saving, e.g. send a DM
	focus   string               // the login to start on
}

type namesProposedMsg struct {
	matches []core.NameMatch
	users   []slack.User
	err     error
	back    *optionsPopup
	then    func(*Model) tea.Cmd
	focus   string
	tip     bool // only offer the popup with a tip, the first time
}

// matchSlackNames proposes a Slack user for each GitHub login not mapped to
// one yet, then opens the Slack names popup.
func (m *Model) matchSlackNames(msg namesProposedMsg) tea.Cmd {
	co := m.core
	if !msg.tip {
		m.info("matching GitHub logins to Slack users…")
	}
	return func() tea.Msg {
		msg.matches, msg.users, msg.err = co.ProposeNames()
		return msg
	}
}

// namesProposed opens the popup, or on the first sync offers it in a tip
// when there is something to review.
func (m *Model) namesProposed(msg namesProposedMsg) tea.Cmd {
	if msg.err != nil {
		if !msg.tip {
			m.fail(msg.err.Error())
		}
		return nil
	}
	if msg.tip {
		n := 0
		for _, nm := range msg.matches {
			if nm.User.ID != "" {
				n++
			}
		}
		if n > 0 && m.core.NamesOffered() {
			m.tip = fmt.Sprintf("Slack: matched %d teammates to their Slack users. Review them: <leader>, → Slack names", n)
		}
		return nil
	}
	m.popup = newNamesPopup(m, msg)
	return nil
}

func newNamesPopup(m *Model, msg namesProposedMsg) *namesPopup {
	p := &namesPopup{users: msg.users, back: msg.back, then: msg.then, focus: msg.focus}
	var proposed, open, mapped []nameRow
	seen := map[string]bool{}
	for _, nm := range msg.matches {
		seen[nm.Login] = true
		if nm.User.ID == "" {
			open = append(open, nameRow{login: nm.Login})
			continue
		}
		proposed = append(proposed, nameRow{login: nm.Login, user: nm.User, why: nm.Why})
	}
	for _, login := range slices.Sorted(maps.Keys(m.cfg.SlackNames)) {
		id := m.cfg.SlackNames[login]
		i := slices.IndexFunc(msg.users, func(u slack.User) bool { return u.ID == id })
		if !seen[login] && i >= 0 {
			mapped = append(mapped, nameRow{login: login, user: msg.users[i], mapped: true})
		}
	}
	p.rows = slices.Concat(proposed, open, mapped)
	if i := slices.IndexFunc(p.rows, func(r nameRow) bool { return r.login == p.focus }); i >= 0 {
		p.cur = i
	}
	p.input = textinput.New()
	p.input.Prompt = sTitle.Render(" ") + " "
	p.input.Placeholder = "Slack name"
	p.input.Width = 40
	return p
}

// window is the height lines of lines that keep line at in view, scrolling
// only when it would leave it, so a popup keeps its size as you move. The
// line above at, often its heading, stays in view too. top is the first
// line shown, kept between calls.
func window(lines []string, at, height int, top *int) []string {
	if len(lines) <= height {
		return lines
	}
	*top = min(*top, max(at-1, 0))
	*top = max(*top, at-height+1)
	*top = min(*top, len(lines)-height)
	return lines[*top : *top+height]
}

func (p *namesPopup) mode() mode {
	if p.picking {
		return modeInsert
	}
	return modeNormal
}

func (p *namesPopup) changed() bool {
	return slices.ContainsFunc(p.rows, func(r nameRow) bool { return r.accept || r.clear })
}

func (p *namesPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	if p.picking {
		return p.updatePick(m, k)
	}
	if len(p.rows) == 0 && k.String() != "esc" && k.String() != "E" {
		return nil
	}
	switch k.String() {
	case "j", "down":
		p.cur = min(p.cur+1, len(p.rows)-1)
	case "k", "up":
		p.cur = max(p.cur-1, 0)
	case " ":
		r := &p.rows[p.cur]
		if r.user.ID != "" && !r.mapped {
			r.accept, r.clear = !r.accept, false
		}
	case "a":
		for i := range p.rows {
			if p.rows[i].user.ID != "" && !p.rows[i].mapped {
				p.rows[i].accept, p.rows[i].clear = true, false
			}
		}
	case "e":
		p.picking = true
		p.input.SetValue("")
		p.filter()
		return p.input.Focus()
	case "x":
		r := &p.rows[p.cur]
		r.accept, r.clear = false, r.mapped || r.user.ID != ""
	case "E":
		p.close(m)
		i := slices.IndexFunc(options, func(o option) bool { return o.label == "slack_names" })
		return m.editOption(p.back, options[i])
	case "enter":
		return p.save(m)
	case "esc", "q":
		p.close(m)
		if p.changed() {
			m.info("Slack names not saved")
		}
	}
	return nil
}

func (p *namesPopup) updatePick(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		p.picking = false
		p.input.Blur()
	case "down", "ctrl+n", "ctrl+j":
		p.pick = min(p.pick+1, max(len(p.results)-1, 0))
	case "up", "ctrl+p", "ctrl+k":
		p.pick = max(p.pick-1, 0)
	case "enter":
		p.picking = false
		p.input.Blur()
		if p.pick < len(p.results) {
			r := &p.rows[p.cur]
			r.user, r.why, r.accept, r.clear = p.results[p.pick], "picked", true, false
		}
	default:
		return p.forward(m, k)
	}
	return nil
}

func (p *namesPopup) forward(_ *Model, msg tea.Msg) tea.Cmd {
	if !p.picking {
		return nil
	}
	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != before {
		p.filter()
	}
	return cmd
}

func (p *namesPopup) filter() {
	q := strings.ToLower(p.input.Value())
	p.results = p.results[:0]
	for _, u := range p.users {
		if _, ok := fuzzy(q, strings.ToLower(u.Display+" "+u.RealName+" "+u.Handle)); ok {
			p.results = append(p.results, u)
		}
	}
	p.pick = 0
}

// save writes the accepted and cleared rows to slack_names.
func (p *namesPopup) save(m *Model) tea.Cmd {
	p.close(m)
	if !p.changed() {
		return p.done(m)
	}
	n := 0
	_, err := m.core.Update(func(c *config.Config) error {
		if c.SlackNames == nil {
			c.SlackNames = map[string]string{}
		}
		for _, r := range p.rows {
			switch {
			case r.accept:
				c.SlackNames[r.login] = r.user.ID
				n++
			case r.clear:
				delete(c.SlackNames, r.login)
				n++
			}
		}
		return nil
	})
	if err != nil {
		m.fail(err.Error())
		return nil
	}
	m.tip = ""
	m.info(fmt.Sprintf("saved %d Slack name%s", n, map[bool]string{true: "", false: "s"}[n == 1]))
	return tea.Batch(m.configChanged(), p.done(m))
}

// close returns to the options, if the popup came from them. A nil
// *optionsPopup in m.popup would be a popup that isn't there.
func (p *namesPopup) close(m *Model) {
	m.popup = nil
	if p.back != nil {
		m.popup = p.back
	}
}

func (p *namesPopup) done(m *Model) tea.Cmd {
	if p.then == nil {
		return nil
	}
	return p.then(m)
}

func (p *namesPopup) view(m *Model) string {
	w := min(popWidth(m), 96)
	if p.picking {
		var rows []string
		for i := max(p.pick-11, 0); i < len(p.results) && len(rows) < 12; i++ {
			u := p.results[i]
			shown, more := cmp.Or(u.Display, u.RealName, u.Handle), " @"+u.Handle
			if u.RealName != "" && u.RealName != shown {
				more = " " + u.RealName + more
			}
			row := fit(" "+sBold.Render(shown)+sDim.Render(" "+more), w-4)
			if i == p.pick {
				row = highlight(row, w-4, sTitle)
			}
			rows = append(rows, row)
		}
		body := sDim.Render(" Slack user for ") + sBold.Render(p.rows[p.cur].login) + "\n" + p.input.View() + "\n" +
			sBorder.Render(strings.Repeat("─", w-4)) + "\n" + strings.Join(rows, "\n") + "\n\n" + sDim.Render(" enter pick · esc back")
		return popBox("Slack names", body, w, pal.Accent)
	}
	var lines []string
	heading, at := "", 0
	for i := range p.rows {
		r := p.rows[i]
		h := "Mapped"
		switch {
		case r.why != "":
			h = "Proposed"
		case !r.mapped:
			h = "No match"
		}
		if h != heading {
			heading = h
			lines = append(lines, sTitle.Render(" "+h))
		}
		box := sDim.Render("[ ]")
		switch {
		case r.clear:
			box = sRed.Render("[-]")
		case r.accept:
			box = sGreen.Render("[x]")
		case r.mapped:
			box = sDim.Render(" ✓ ")
		}
		who := sDim.Render("no Slack user · e to pick")
		if r.user.ID != "" && !r.clear {
			shown := cmp.Or(r.user.Display, r.user.RealName, r.user.Handle)
			who = sBold.Render(shown)
			if r.user.RealName != "" && r.user.RealName != shown {
				who += sDim.Render(" (" + r.user.RealName + ")")
			}
		}
		if r.clear {
			who = sRed.Render("remove: prints " + r.login)
		}
		row := " " + box + " " + fit(r.login, 20) + sDim.Render(" → ") + who
		if r.why != "" && !r.clear {
			row += sDim.Render(" · " + r.why)
		}
		row = fit(row, w-4)
		if i == p.cur {
			row = highlight(row, w-4, sTitle)
			at = len(lines)
		}
		lines = append(lines, row)
	}
	lines = window(lines, at, max(m.h-12, 6), &p.top)
	if len(p.rows) == 0 {
		lines = append(lines, sDim.Render(" Everyone in your orgs and inbox has a Slack user."))
	}
	help := sDim.Render(" space accept · a accept all · e pick · x remove · enter save · esc cancel · E edit as text") + "\n" +
		sDim.Render(" A mapped teammate is @mentioned when prowl posts, and <leader>Y DMs them.")
	return popBox("Slack names", strings.Join(lines, "\n")+"\n\n"+help, w, pal.Accent)
}

// channelsPopup picks the channels whose PR links show in the inbox, and
// whether DMs count, with each channel's recent PR links to go by.
type channelsPopup struct {
	all     []core.ChannelActivity
	dmLinks int
	who     string // who you're signed in as
	on      map[string]bool
	dms     bool
	input   textinput.Model
	results []int // indexes into all; the DM row comes first and isn't one
	cur     int   // 0 is the DM row
	top     int
	back    *optionsPopup
}

type channelsMsg struct {
	channels []core.ChannelActivity
	dmLinks  int
	err      error
	back     *optionsPopup
}

// openChannels reads your channels and their recent PR links, then opens
// the channel picker.
func (m *Model) openChannels(back *optionsPopup) tea.Cmd {
	co := m.core
	m.info("reading your Slack channels…")
	return func() tea.Msg {
		channels, dms, err := co.SlackActivity()
		return channelsMsg{channels: channels, dmLinks: dms, err: err, back: back}
	}
}

func (m *Model) channelsRead(msg channelsMsg) {
	if msg.err != nil {
		m.fail(msg.err.Error())
		return
	}
	p := &channelsPopup{all: msg.channels, dmLinks: msg.dmLinks, who: m.core.SlackSignedIn(), on: map[string]bool{}, dms: m.cfg.SlackDMs, back: msg.back}
	for _, id := range m.cfg.SlackChannels {
		p.on[id] = true
	}
	p.input = textinput.New()
	p.input.Prompt = sTitle.Render(" ") + " "
	p.input.Placeholder = "channel"
	p.input.Width = 40
	p.input.Focus()
	p.filter()
	m.popup = p
}

func (p *channelsPopup) mode() mode { return modeInsert }

func (p *channelsPopup) filter() {
	q := strings.ToLower(p.input.Value())
	p.results = p.results[:0]
	for i, ch := range p.all {
		if _, ok := fuzzy(q, strings.ToLower(ch.Name)); ok {
			p.results = append(p.results, i)
		}
	}
	p.cur = min(p.cur, len(p.results))
}

func (p *channelsPopup) forward(_ *Model, msg tea.Msg) tea.Cmd {
	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != before {
		p.filter()
	}
	return cmd
}

func (p *channelsPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case "down", "ctrl+n", "ctrl+j":
		p.cur = min(p.cur+1, len(p.results))
	case "up", "ctrl+p", "ctrl+k":
		p.cur = max(p.cur-1, 0)
	case " ", "tab":
		if p.cur == 0 {
			p.dms = !p.dms
		} else {
			id := p.all[p.results[p.cur-1]].ID
			p.on[id] = !p.on[id]
		}
	case "enter":
		return p.save(m)
	case "esc":
		m.popup = nil
		if p.back != nil {
			m.popup = p.back
		}
	default:
		return p.forward(m, k)
	}
	return nil
}

func (p *channelsPopup) save(m *Model) tea.Cmd {
	m.popup = nil
	if p.back != nil {
		m.popup = p.back
	}
	var ids []string
	for _, ch := range p.all {
		if p.on[ch.ID] {
			ids = append(ids, ch.ID)
		}
	}
	_, err := m.core.Update(func(c *config.Config) error {
		c.SlackChannels, c.SlackDMs = ids, p.dms
		return nil
	})
	if err != nil {
		m.fail(err.Error())
		return nil
	}
	m.tip = ""
	note := fmt.Sprintf("watching %d Slack channel%s", len(ids), map[bool]string{true: "", false: "s"}[len(ids) == 1])
	if p.dms {
		note += " and your DMs"
	}
	m.info(note)
	return m.configChanged()
}

func (p *channelsPopup) view(m *Model) string {
	w := min(popWidth(m), 80)
	box := func(on bool) string {
		if on {
			return sGreen.Render("[x]")
		}
		return sDim.Render("[ ]")
	}
	links := func(n int) string {
		if n == 0 {
			return sDim.Render("no PR links lately")
		}
		return sDim.Render(fmt.Sprintf("%d PR link%s in 14 days", n, map[bool]string{true: "", false: "s"}[n == 1]))
	}
	lines := []string{fit(" "+box(p.dms)+" "+fit(sBold.Render("Direct messages"), 28)+links(p.dmLinks), w-4)}
	for _, i := range p.results {
		ch := p.all[i]
		name := "#" + ch.Name
		if ch.Private {
			name += sDim.Render(" private")
		}
		row := " " + box(p.on[ch.ID]) + " " + fit(name, 28) + links(ch.Links)
		if ch.Recommended {
			row += sGreen.Render(" · recommended")
		}
		lines = append(lines, fit(row, w-4))
	}
	lines[p.cur] = highlight(lines[p.cur], w-4, sTitle)
	lines = window(lines, p.cur, max(m.h-14, 6), &p.top)
	head := sDim.Render(" signed in as " + p.who)
	help := sDim.Render(" type to filter · ↑↓ move · space check · enter save · esc cancel") + "\n" +
		sDim.Render(" PR links in checked channels, and sent to you in DMs, show in the inbox as shared.")
	return popBox("Slack channels", head+"\n"+p.input.View()+"\n"+sBorder.Render(strings.Repeat("─", w-4))+"\n"+
		strings.Join(lines, "\n")+"\n\n"+help, w, pal.Accent)
}
