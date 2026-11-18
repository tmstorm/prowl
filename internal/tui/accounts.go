package tui

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/platform"
)

type authCheckedMsg struct{ err error }

type loginDoneMsg struct {
	key, name string
	err       error
}

// checkAuth has core check every login.
func (m *Model) checkAuth() tea.Cmd {
	co := m.core
	return func() tea.Msg {
		co.CheckAuth(context.Background())
		return authCheckedMsg{}
	}
}

// slackSignedIn has core use the token `prowl slack` saved, then checks
// every login.
func (m *Model) slackSignedIn() tea.Cmd {
	co := m.core
	return func() tea.Msg {
		err := co.LoadSlack()
		co.CheckAuth(context.Background())
		return authCheckedMsg{err}
	}
}

// authWarning is the statusline note for the first login that needs you:
// logged out, or running out soon.
func (m *Model) authWarning() string {
	for _, a := range m.core.LoginsDue() {
		if !a.Auth.LoggedIn {
			return sRed.Render(a.Title+" logged out") + sDim.Render(" "+leader+"l")
		}
		return sYellow.Render(a.Title+" relogin in "+until(a.Auth.Expires)) + sDim.Render(" "+leader+"l")
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
	nav
	loading bool
}

func (p *accountsPopup) mode() mode { return modeNormal }

func (p *accountsPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	accts := m.core.Accounts()
	switch k.String() {
	case "esc", "q", "ctrl+c":
		m.popup = nil
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
		return tea.Exec(platform.TerminalCmd{Cmd: cmd}, func(err error) tea.Msg { return loginDoneMsg{a.Key, a.Title, err} })
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
				if st.ExpiresSoon(core.RenewWithin) {
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

func (p *accountsPopup) list(m *Model) (*nav, int) {
	n := len(m.core.Accounts())
	p.setLen(n, n)
	return &p.nav, 3
}
