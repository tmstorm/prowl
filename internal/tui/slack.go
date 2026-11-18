package tui

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
)

// nameRow is one GitHub login in the Slack names popup.
type nameRow struct {
	login  string
	group  string         // Proposed, No match or Mapped, as it was when the popup opened
	user   core.SlackUser // the proposed, picked or mapped Slack user; zero for none
	why    string         // why it was proposed, "" once mapped
	mapped bool           // slack_names maps it to user already
	accept bool           // save user for it
	clear  bool           // save it without a Slack user
}

// namesPopup reviews the Slack user of each GitHub login: proposals to
// accept, logins nothing matched, and the ones mapped already.
type namesPopup struct {
	rows    []nameRow
	nav           // over lines: each group's heading, then its rows
	lineRow []int // per line: index into rows, or -1 for a heading
	users   []core.SlackUser
	picking bool // choosing a Slack user for the current row
	input   filterInput
	results []core.SlackUser
	pick    nav
	back    *optionsPopup        // the options to return to, if they were open
	then    func(*Model) tea.Cmd // what to do after saving, e.g. send a DM
	focus   string               // the login to start on
}

type namesProposedMsg struct {
	matches []core.NameMatch
	users   []core.SlackUser
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
		msg.matches, msg.users, msg.err = co.ProposeNames(context.Background())
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
		if n > 0 && m.core.FirstTime("slack names") {
			m.offerTip("slack names", fmt.Sprintf("Slack: matched %d teammates to their Slack users. Review them: <leader>, → Slack names", n))
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
			open = append(open, nameRow{login: nm.Login, group: "No match"})
			continue
		}
		proposed = append(proposed, nameRow{login: nm.Login, group: "Proposed", user: nm.User, why: nm.Why})
	}
	for _, login := range slices.Sorted(maps.Keys(m.cfg.SlackNames)) {
		id := m.cfg.SlackNames[login]
		i := slices.IndexFunc(msg.users, func(u core.SlackUser) bool { return u.ID == id })
		if !seen[login] && i >= 0 {
			mapped = append(mapped, nameRow{login: login, group: "Mapped", user: msg.users[i], mapped: true})
		}
	}
	p.rows = slices.Concat(proposed, open, mapped)
	for i, r := range p.rows {
		if i == 0 || r.group != p.rows[i-1].group {
			p.lineRow = append(p.lineRow, -1)
		}
		if r.login == p.focus {
			p.cur = len(p.lineRow)
		}
		p.lineRow = append(p.lineRow, i)
	}
	p.skip = func(i int) bool { return p.lineRow[i] < 0 }
	p.scrolloff = 1
	p.input = newFilterInput("Slack name", 40)
	return p
}

func (p *namesPopup) mode() mode {
	if p.picking {
		return modeInsert
	}
	return modeNormal
}

func (p *namesPopup) list(m *Model) (*nav, int) {
	if p.picking {
		p.pick.setLen(len(p.results), 12)
		return &p.pick, 6
	}
	p.setLen(len(p.lineRow), max(m.h-12, 6))
	return &p.nav, 3
}

// click picks the Slack user clicked, as the other pickers do, or checks or
// unchecks the login clicked.
func (p *namesPopup) click(m *Model, _, _, row int) tea.Cmd {
	if p.picking {
		return m.pick(&p.pick, row)
	}
	return m.check(&p.nav, row)
}

// row is the login under the cursor.
func (p *namesPopup) row() *nameRow { return &p.rows[p.lineRow[p.cur]] }

func (p *namesPopup) changed() bool {
	return slices.ContainsFunc(p.rows, func(r nameRow) bool { return r.accept || r.clear })
}

func (p *namesPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	if p.picking {
		return p.updatePick(m, k)
	}
	if len(p.rows) == 0 && slices.Contains([]string{" ", "e", "x"}, k.String()) {
		return nil
	}
	switch k.String() {
	case " ":
		r := p.row()
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
		r := p.row()
		r.accept, r.clear = false, r.mapped || r.user.ID != ""
	case "E":
		p.close(m)
		i := slices.IndexFunc(options, func(o option) bool { return o.key == "slack_names" })
		return m.editOption(p.back, options[i])
	case "enter":
		return p.save(m)
	case "esc", "q", "ctrl+c":
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
	case "enter":
		p.picking = false
		p.input.Blur()
		if p.pick.cur < len(p.results) {
			r := p.row()
			r.user, r.why, r.accept, r.clear = p.results[p.pick.cur], "picked", true, false
		}
	default:
		return p.input.key(&p.pick, k, p.filter)
	}
	return nil
}

func (p *namesPopup) forward(_ *Model, msg tea.Msg) tea.Cmd {
	if !p.picking {
		return nil
	}
	return p.input.update(msg, p.filter)
}

func (p *namesPopup) filter() {
	q := strings.ToLower(p.input.Value())
	p.results = p.results[:0]
	for _, u := range p.users {
		if _, ok := fuzzy(q, strings.ToLower(u.Display+" "+u.RealName+" "+u.Handle)); ok {
			p.results = append(p.results, u)
		}
	}
	p.pick.cur = 0
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
	m.dropTip("slack names")
	m.info("saved " + count(n, "Slack name"))
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
	v, _ := p.list(m)
	if p.picking {
		var rows []string
		for i := v.top; i < min(v.top+v.h, v.n); i++ {
			u := p.results[i]
			shown, more := cmp.Or(u.Display, u.RealName, u.Handle), " @"+u.Handle
			if u.RealName != "" && u.RealName != shown {
				more = " " + u.RealName + more
			}
			row := fit(" "+sBold.Render(shown)+sDim.Render(" "+more), w-4)
			if i == v.cur {
				row = highlight(row, w-4, sTitle)
			}
			rows = append(rows, row)
		}
		body := sDim.Render(" Slack user for ") + sBold.Render(p.row().login) + "\n" + p.input.View() + "\n" +
			sBorder.Render(strings.Repeat("─", w-4)) + "\n" + strings.Join(rows, "\n") + "\n\n" + sDim.Render(" enter pick · esc back")
		return popBox("Slack names", body, w, pal.Accent)
	}
	var lines []string
	for i := p.top; i < min(p.top+p.h, p.n); i++ {
		if p.lineRow[i] < 0 {
			lines = append(lines, sTitle.Render(" "+p.rows[p.lineRow[i+1]].group))
			continue
		}
		r := p.rows[p.lineRow[i]]
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
		}
		lines = append(lines, row)
	}
	if len(p.rows) == 0 {
		lines = append(lines, sDim.Render(" Everyone in your orgs and inbox has a Slack user."))
	}
	help := sDim.Render(" space accept · a accept all · e pick · x remove · enter save · esc cancel · E edit as text") + "\n" +
		sDim.Render(" A mapped teammate is @mentioned when prowl posts, and "+leader+"Y DMs them.")
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
	input   filterInput
	results []int // indexes into all; the DM row comes first and isn't one
	nav           // row 0 is the DM row
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
		channels, dms, err := co.SlackActivity(context.Background())
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
	p.input = newFilterInput("channel", 40)
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
}

func (p *channelsPopup) forward(_ *Model, msg tea.Msg) tea.Cmd { return p.input.update(msg, p.filter) }

func (p *channelsPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	switch k.String() {
	case " ", "tab":
		if p.cur == 0 {
			p.dms = !p.dms
		} else {
			id := p.all[p.results[p.cur-1]].ID
			p.on[id] = !p.on[id]
		}
	case "enter":
		return p.save(m)
	case "esc", "ctrl+c":
		m.popup = nil
		if p.back != nil {
			m.popup = p.back
		}
	default:
		return p.input.key(&p.nav, k, p.filter)
	}
	return nil
}

func (p *channelsPopup) list(m *Model) (*nav, int) {
	p.setLen(len(p.results)+1, max(m.h-14, 6))
	return &p.nav, 6
}

// click checks or unchecks the row clicked.
func (p *channelsPopup) click(m *Model, _, _, row int) tea.Cmd { return m.check(&p.nav, row) }

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
	m.dropTip("slack channels")
	note := "watching " + count(len(ids), "Slack channel")
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
		return sDim.Render(count(n, "PR link") + " in 14 days")
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
	p.list(m)
	lines[p.cur] = highlight(lines[p.cur], w-4, sTitle)
	lines = lines[p.top:min(p.top+p.h, p.n)]
	head := sDim.Render(" signed in as " + p.who)
	help := sDim.Render(" type to filter · ↑↓ move · space or click check · enter save · esc cancel") + "\n" +
		sDim.Render(" PR links in checked channels, and sent to you in DMs, show in the inbox as shared.")
	return popBox("Slack channels", head+"\n"+p.input.View()+"\n"+sBorder.Render(strings.Repeat("─", w-4))+"\n"+
		strings.Join(lines, "\n")+"\n\n"+help, w, pal.Accent)
}
