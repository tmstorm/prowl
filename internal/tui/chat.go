package tui

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/inbox"
)

// agentTitle names the configured review agent in the UI ("Claude", "Codex").
var agentTitle = "Claude"

// chatSession is the review agent's own session, resumed interactively in a
// pseudo-terminal. It keeps running while hidden.
type chatSession struct {
	key   string // chatKey: the PR and the agent
	agent string
	title string // agent name for the popup title
	quit  string // what ends the session, e.g. /exit
	item  inbox.Item
	cmd   *exec.Cmd
	pty   *os.File
	emu   *vt.SafeEmulator
	w, h  int
	out   chan struct{} // redraw signal, coalesced
	done  chan error
}

type chatOutMsg struct{ key string }
type chatExitMsg struct {
	key string
	err error
}

func (cs *chatSession) listen() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-cs.out:
			return chatOutMsg{cs.key}
		case err := <-cs.done:
			return chatExitMsg{cs.key, err}
		}
	}
}

// chatSize is the emulator size for the popup at the current window size.
func (m *Model) chatSize() (int, int) {
	return max(m.w-8, 40), max(m.h-6, 10)
}

// chatKey names one chat: each agent has its own session on a PR.
func chatKey(pr, reviewer string) string { return pr + "/" + reviewer }

// openChat shows an agent's chat for a PR, starting it if needed.
func (m *Model) openChat(it inbox.Item, reviewer string) tea.Cmd {
	key := chatKey(it.PR.Key(), reviewer)
	if cs := m.chats[key]; cs != nil {
		m.popup = &chatPopup{cs: cs}
		return nil
	}
	spec, err := m.core.Chat(it.PR.Key(), reviewer)
	if err != nil {
		m.fail(err.Error())
		return nil
	}
	cmd := exec.Command(spec.Args[0], spec.Args[1:]...)
	cmd.Dir, cmd.Env = spec.Dir, spec.Env
	if m.cfg.ChatMode == "fullscreen" || runtime.GOOS == "windows" {
		return tea.ExecProcess(cmd, func(err error) tea.Msg { return chatExitMsg{key, err} })
	}

	w, h := m.chatSize()
	cs, err := startChat(key, it, cmd, w, h)
	if err != nil {
		m.fail("start " + core.ReviewerTitle(reviewer) + ": " + err.Error())
		return nil
	}
	cs.title, cs.quit, cs.agent = core.ReviewerTitle(reviewer), spec.Quit, reviewer
	if m.chats == nil {
		m.chats = map[string]*chatSession{}
	}
	m.chats[key] = cs
	m.popup = &chatPopup{cs: cs}
	return cs.listen()
}

// startChat runs cmd in a pseudo-terminal feeding a terminal emulator. The
// emulator is never closed: its Close races its Read in the vt library, so
// the goroutine copying its replies to the pty stays parked after the chat.
func startChat(key string, it inbox.Item, cmd *exec.Cmd, w, h int) (*chatSession, error) {
	cmd.Env = append(cmd.Env, "TERM=xterm-256color", "COLORTERM=truecolor")
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)})
	if err != nil {
		return nil, fmt.Errorf("start chat: %w", err)
	}
	cs := &chatSession{key: key, item: it, cmd: cmd, pty: f, emu: vt.NewSafeEmulator(w, h), w: w, h: h,
		out: make(chan struct{}, 1), done: make(chan error, 1)}
	read := make(chan struct{})
	go func() {
		defer close(read)
		buf := make([]byte, 32*1024)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				_, _ = cs.emu.Write(buf[:n])
				select {
				case cs.out <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { _, _ = io.Copy(f, cs.emu) }()
	go func() {
		err := cmd.Wait()
		select {
		case <-read:
		case <-time.After(2 * time.Second):
		}
		_ = f.Close()
		cs.done <- err
	}()
	return cs, nil
}

func (m *Model) resizeChats() {
	w, h := m.chatSize()
	for _, cs := range m.chats {
		if cs.w == w && cs.h == h {
			continue
		}
		cs.w, cs.h = w, h
		_ = pty.Setsize(cs.pty, &pty.Winsize{Rows: uint16(h), Cols: uint16(w)})
		cs.emu.Resize(w, h)
	}
}

// chatExited offers to sync the structured review from what was discussed.
func (m *Model) chatExited(msg chatExitMsg) tea.Cmd {
	cs := m.chats[msg.key]
	delete(m.chats, msg.key)
	if p, ok := m.popup.(*chatPopup); ok && p.cs.key == msg.key {
		m.popup = nil
	}
	i := strings.LastIndex(msg.key, "/")
	pr, name := msg.key[:i], msg.key[i+1:]
	var it inbox.Item
	if cs != nil {
		it = cs.item
	} else {
		found := false
		for _, x := range m.items {
			if x.PR.Key() == pr {
				it, found = x, true
			}
		}
		if !found {
			return nil
		}
	}
	m.mode = modeConfirm
	m.confirm = &confirm{prompt: "chat ended. Update " + core.ReviewerTitle(name) + "'s findings from the conversation? [y/N]", yes: func() tea.Cmd {
		return m.startReview(it, "", true, name)
	}}
	return nil
}

// ---- popup

type chatPopup struct{ cs *chatSession }

func (p *chatPopup) mode() mode { return modeInsert }

func (p *chatPopup) update(m *Model, k tea.KeyMsg) tea.Cmd {
	if k.String() == "ctrl+\\" {
		m.popup = nil
		m.info("chat hidden, still running · " + leader + "ci to reopen")
		return nil
	}
	if b := keyBytes(k); len(b) > 0 {
		_, _ = p.cs.pty.Write(b)
	}
	return nil
}

func (p *chatPopup) view(m *Model) string {
	cs := p.cs
	lines := strings.Split(cs.emu.Render(), "\n")
	for len(lines) < cs.h {
		lines = append(lines, "")
	}
	lines = lines[:cs.h]

	if !cs.emu.CursorHidden() {
		pos := cs.emu.CursorPosition()
		if pos.Y >= 0 && pos.Y < len(lines) {
			l := lines[pos.Y]
			ch := ansi.Strip(ansi.Cut(l, pos.X, pos.X+1))
			if ch == "" {
				ch = " "
			}
			lines[pos.Y] = ansi.Cut(l, 0, pos.X) + "\x1b[0m" + lipgloss.NewStyle().Reverse(true).Render(ch) + ansi.Cut(l, pos.X+1, cs.w)
		}
	}
	for i, l := range lines {
		lines[i] = fit(l, cs.w)
	}
	title := sTitle.Render(" "+cs.title+" ") + sDim.Render(fmt.Sprintf("· %s · ctrl+\\ hide · %s ends ", cs.key, cs.quit))
	border := lipgloss.Color(pal.Green)
	top := lipgloss.NewStyle().Foreground(border).Render("╭─") + title
	top += lipgloss.NewStyle().Foreground(border).Render(strings.Repeat("─", max(cs.w-lipgloss.Width(title)-1, 0)) + "╮")
	side := lipgloss.NewStyle().Foreground(border).Render("│")
	var b strings.Builder
	b.WriteString(top)
	for _, l := range lines {
		b.WriteString("\n" + side + l + "\x1b[0m" + side)
	}
	b.WriteString("\n" + lipgloss.NewStyle().Foreground(border).Render("╰"+strings.Repeat("─", cs.w)+"╯"))
	return b.String()
}

// keyBytes encodes a key press the way a terminal would send it. Control
// keys (ctrl+a…ctrl+z, enter, tab, esc, backspace) are their own byte.
func keyBytes(k tea.KeyMsg) []byte {
	var s string
	switch k.Type {
	case tea.KeyRunes:
		s = string(k.Runes)
		if k.Paste {
			s = "\x1b[200~" + s + "\x1b[201~"
		}
	case tea.KeySpace:
		s = " "
	case tea.KeyUp:
		s = "\x1b[A"
	case tea.KeyDown:
		s = "\x1b[B"
	case tea.KeyRight:
		s = "\x1b[C"
	case tea.KeyLeft:
		s = "\x1b[D"
	case tea.KeyHome:
		s = "\x1b[H"
	case tea.KeyEnd:
		s = "\x1b[F"
	case tea.KeyPgUp:
		s = "\x1b[5~"
	case tea.KeyPgDown:
		s = "\x1b[6~"
	case tea.KeyDelete:
		s = "\x1b[3~"
	case tea.KeyShiftTab:
		s = "\x1b[Z"
	case tea.KeyShiftUp:
		s = "\x1b[1;2A"
	case tea.KeyShiftDown:
		s = "\x1b[1;2B"
	case tea.KeyCtrlUp:
		s = "\x1b[1;5A"
	case tea.KeyCtrlDown:
		s = "\x1b[1;5B"
	case tea.KeyCtrlLeft:
		s = "\x1b[1;5D"
	case tea.KeyCtrlRight:
		s = "\x1b[1;5C"
	default:
		if k.Type >= 0 && (k.Type < 32 || k.Type == 127) {
			s = string(rune(k.Type))
		}
	}
	if s == "" {
		return nil
	}
	if k.Alt && utf8.RuneCountInString(s) >= 1 && !strings.HasPrefix(s, "\x1b[") {
		s = "\x1b" + s
	}
	return []byte(s)
}

// mouse passes clicks and the wheel to the agent's own TUI, at x, y inside
// the window. The emulator drops them unless the agent turned mouse input on.
func (p *chatPopup) mouse(msg tea.MouseMsg, x, y int, in bool) {
	cs := p.cs
	if !in || x < 0 || y < 0 || x >= cs.w || y >= cs.h {
		return
	}
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		cs.emu.SendMouse(vt.MouseWheel{X: x, Y: y, Button: vt.MouseWheelUp})
	case msg.Button == tea.MouseButtonWheelDown:
		cs.emu.SendMouse(vt.MouseWheel{X: x, Y: y, Button: vt.MouseWheelDown})
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		cs.emu.SendMouse(vt.MouseClick{X: x, Y: y, Button: vt.MouseLeft})
	case msg.Action == tea.MouseActionRelease:
		cs.emu.SendMouse(vt.MouseRelease{X: x, Y: y, Button: vt.MouseLeft})
	}
}
