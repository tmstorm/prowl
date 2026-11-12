package tui

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tmstorm/prowl/internal/agent"
	"github.com/tmstorm/prowl/internal/core"
)

type authCheckedMsg struct{}

type loginDoneMsg struct {
	name string
	err  error
}

// checkAuth has core check every login.
func (m *Model) checkAuth() tea.Cmd {
	co := m.core
	return func() tea.Msg {
		co.CheckAuth()
		return authCheckedMsg{}
	}
}

// account is the login of the given key, as of the last check.
func (m *Model) account(key string) (core.Account, bool) {
	for _, a := range m.core.Accounts() {
		if a.Key == key {
			return a, true
		}
	}
	return core.Account{}, false
}

// authWarning is the statusline note for the configured agent: logged out,
// or a login that runs out within three days.
func (m *Model) authWarning() string {
	for _, name := range m.cfg.Agent.Reviewers {
		acct, ok := m.account(name)
		a := acct.Auth
		switch {
		case !ok || !a.Known:
		case !a.LoggedIn:
			return sRed.Render(acct.Title+" logged out") + sDim.Render(" "+leader+"l")
		case a.ExpiresSoon(agent.RenewWithin):
			return sYellow.Render(acct.Title+" relogin in "+until(a.Expires)) + sDim.Render(" "+leader+"l")
		}
	}
	return ""
}

func until(t time.Time) string {
	d := time.Until(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 0))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// ---- popup

type accountsPopup struct {
	cur     int
	loading bool
}

func (p *accountsPopup) mode() mode { return modeInsert }

func (p *accountsPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	accts := m.core.Accounts()
	switch k.String() {
	case "esc", "q", "ctrl+c":
		m.popup = nil
	case "j", "down":
		p.cur = min(p.cur+1, len(accts)-1)
	case "k", "up":
		p.cur = max(p.cur-1, 0)
	case "r":
		p.loading = true
		return m.checkAuth()
	case "enter", "l":
		a := accts[p.cur]
		if a.Missing != nil {
			m.fail(a.Missing.Error())
			return nil
		}
		login, err := m.core.Login(a.Key)
		if err != nil {
			m.fail(err.Error())
			return nil
		}
		cmd := exec.Command(login.Args[0], login.Args[1:]...)
		cmd.Dir, cmd.Env = login.Dir, login.Env
		return tea.ExecProcess(cmd, func(err error) tea.Msg { return loginDoneMsg{a.Title, err} })
	}
	return nil
}

func (p *accountsPopup) view(m *Model) string {
	w := min(popWidth(m), 72)
	var rows []string
	for i, a := range m.core.Accounts() {
		var state string
		st := a.Auth
		switch {
		case a.Missing != nil:
			state = sDim.Render("not installed")
		case p.loading && !st.Known:
			state = spinner(m) + sDim.Render(" checking")
		case !st.Known:
			state = sDim.Render("unknown")
		case !st.LoggedIn:
			state = sRed.Render("logged out")
		default:
			state = sGreen.Render("logged in")
			if st.Account != "" {
				state += sDim.Render(" as " + st.Account)
			}
			if !st.Expires.IsZero() {
				exp := sDim.Render(" · ends in " + until(st.Expires))
				if st.ExpiresSoon(agent.RenewWithin) {
					exp = sYellow.Render(" · ends in " + until(st.Expires))
				}
				state += exp
			}
		}
		name := a.Title
		if slices.Contains(m.cfg.Agent.Reviewers, a.Key) {
			name += sDim.Render(" ·")
		}
		row := fit(" "+sBold.Render(fit(name, 9)), 11) + state
		row = fit(row, w-4)
		if i == p.cur {
			row = highlight(row, w-4, sTitle)
		}
		rows = append(rows, row)
	}
	help := sDim.Render(" enter log in · r recheck · esc close")
	note := sDim.Render(" · marks your reviewers (options: Reviewers)")
	return popBox("Accounts", strings.Join(rows, "\n")+"\n\n"+note+"\n"+help, w, pal.Accent)
}

func (p *accountsPopup) rows(m *Model) (int, int, int) { return 3, 0, len(m.core.Accounts()) }
func (p *accountsPopup) cursor() *int                  { return &p.cur }
