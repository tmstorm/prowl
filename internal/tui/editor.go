package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tmstorm/prowl/internal/config"
	"github.com/tmstorm/prowl/internal/core"
	"github.com/tmstorm/prowl/internal/inbox"
	"github.com/tmstorm/prowl/internal/store"
)

type editorDoneMsg struct {
	path   string
	mtime  time.Time
	err    error
	onDone func(m *Model, text string, saved bool) tea.Cmd
}

// openEditor suspends the TUI, runs the user's editor on content, and calls
// onDone with the file's contents afterwards. saved is false when the editor
// exited non-zero (:cq) or the file was never written. On Windows it runs
// through PowerShell piped to Out-Null, which waits for a windowed editor
// such as Notepad to close; cmd /C returns at once.
func (m *Model) openEditor(name, content string, onDone func(m *Model, text string, saved bool) tea.Cmd) tea.Cmd {
	dir := filepath.Join(config.DataDir(), "drafts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.fail(err.Error())
		return nil
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		m.fail(err.Error())
		return nil
	}
	st, _ := os.Stat(path)
	mtime := st.ModTime()
	cmd := exec.Command("sh", "-c", m.cfg.EditorCmd()+` "$1"`, "sh", path)
	if runtime.GOOS == "windows" {
		cmd = exec.Command("powershell", "-NoProfile", "-Command",
			"& { "+m.cfg.EditorCmd()+" '"+strings.ReplaceAll(path, "'", "''")+"' | Out-Null }")
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{path: path, mtime: mtime, err: err, onDone: onDone}
	})
}

func (m *Model) editorDone(msg editorDoneMsg) tea.Cmd {
	b, _ := os.ReadFile(msg.path)
	saved := msg.err == nil
	if st, err := os.Stat(msg.path); err == nil && !st.ModTime().After(msg.mtime) {
		saved = false
	}
	cmd := msg.onDone(m, string(b), saved)
	_ = os.Remove(msg.path)
	return tea.Batch(cmd, m.mouseBack())
}

// mouseBack turns mouse reporting on again after another program (the
// editor, a login, a fullscreen chat) had the terminal: Bubble Tea turns it
// off when it hands the terminal over and doesn't turn it back on.
func (m *Model) mouseBack() tea.Cmd {
	if !m.cfg.Mouse {
		return nil
	}
	return tea.EnableMouseCellMotion
}

// editText edits free text (from a popup) in the editor.
func (m *Model) editText(value string, done func(m *Model, text string)) tea.Cmd {
	keep := m.popup
	m.popup = nil
	return m.openEditor(fmt.Sprintf("text-%d.md", time.Now().UnixNano()), value, func(m *Model, text string, saved bool) tea.Cmd {
		if !saved {
			m.popup = keep
			return nil
		}
		done(m, strings.TrimRight(text, "\n"))
		return nil
	})
}

var eventLine = regexp.MustCompile(`(?mi)^event:\s*(\S+)`)

// editReview opens a review draft in the editor and submits it on :wq.
func (m *Model) editReview(it inbox.Item, d core.Draft) tea.Cmd {
	pr := it.PR
	if d.Reopened {
		m.info("this is the review you wrote that didn't go out")
	}
	event, body, comments, fromAgent := d.Event, d.Body, d.Comments, len(d.Runs) > 0
	var b strings.Builder
	fmt.Fprintf(&b, "%s#%d  %s  (%s)\n\n", pr.Repository.NameWithOwner, pr.Number, pr.Title, pr.AuthorLogin())
	b.WriteString("Write the review below this block. :wq submits it.\n")
	b.WriteString(":q! or :cq cancels. An empty comment or request-changes review cancels\n")
	b.WriteString("too, unless it has inline comments or replies: they post with no body.\n\n")
	fmt.Fprintf(&b, "event: %s\n", strings.ToLower(event))
	b.WriteString("       (approve | request_changes | comment | close | draft | note)\n")
	b.WriteString("       close and draft post this as a comment, then close the PR or make it a draft.\n")
	b.WriteString("       note posts a plain comment in the conversation, not a review; pending\n")
	b.WriteString("       inline comments stay pending.\n")
	if action, reason := m.suggestedAction(pr.Key()); action != "" && event != strings.ToUpper(action) {
		first, _, _ := strings.Cut(reason, "\n")
		fmt.Fprintf(&b, "       The agent suggests %s: %s\n", map[string]string{"close": "closing", "draft": "a draft"}[action], first)
	}
	if fromAgent && event == "COMMENT" {
		b.WriteString("       Posting as a comment: no finding is must-fix. To block the merge\n")
		b.WriteString("       until the author responds, change it to request_changes.\n")
	}
	if len(comments) > 0 {
		fmt.Fprintf(&b, "\n%d inline comment(s) will be attached:\n", len(comments))
		for _, c := range comments {
			first, _, _ := strings.Cut(c.Body, "\n")
			if r := []rune(first); len(r) > 70 {
				first = string(r[:70]) + "…"
			}
			fmt.Fprintf(&b, "  %s:%d  %s\n", c.Path, c.Line, first)
		}
		if fromAgent {
			b.WriteString("(toggle findings in the review tab to change this list)\n")
		}
		if spansCommits(d) {
			b.WriteString("Some were written before the author pushed. They go out first as their own\n")
			b.WriteString("review on the commit they were written on, so each stays on its line.\n")
		}
	}
	if len(d.Replies) > 0 {
		fmt.Fprintf(&b, "\n%d draft repl%s will be sent after the review, each in its thread:\n", len(d.Replies), map[bool]string{true: "y", false: "ies"}[len(d.Replies) == 1])
		for _, r := range d.Replies {
			first, _, _ := strings.Cut(r.Body, "\n")
			if rs := []rune(first); len(rs) > 60 {
				first = string(rs[:60]) + "…"
			}
			fmt.Fprintf(&b, "  %s:%d  to %s: %s\n", r.Path, r.Line, r.To, first)
		}
		b.WriteString("(x on a draft reply in the review tab leaves it out; c sends one now)\n")
	}
	header := "<!--\n" + strings.ReplaceAll(b.String(), "-->", "-- >") + "-->\n\n"
	b.Reset()
	b.WriteString(header)
	b.WriteString(body)
	if body != "" {
		b.WriteString("\n")
	}
	name := fmt.Sprintf("review-%s-%d.md", pr.Repository.Name, pr.Number)
	return m.openEditor(name, b.String(), func(m *Model, text string, saved bool) tea.Cmd {
		if !saved {
			m.info("review cancelled")
			return nil
		}
		header, rest := splitHeader(text)
		ev := event
		if mm := eventLine.FindStringSubmatch(header); mm != nil {
			ev = strings.ToUpper(mm[1])
		}
		if ev != "APPROVE" && ev != "REQUEST_CHANGES" && ev != "COMMENT" && ev != "CLOSE" && ev != "DRAFT" && ev != "NOTE" {
			m.fail("unknown event " + strings.ToLower(ev))
			return nil
		}
		rest = strings.TrimSpace(rest)
		if rest == "" && (ev == "NOTE" || (ev == "COMMENT" || ev == "REQUEST_CHANGES") && len(comments) == 0 && len(d.Replies) == 0) {
			m.info("empty review, cancelled")
			return nil
		}
		d.Event, d.Body = ev, rest
		return m.submit(it, d)
	})
}

// splitHeader splits a review draft at the line that closes its header
// block: a line that is only "-->". The header escapes any "-->" in what it
// quotes (a PR title, say), so it can't end early.
func splitHeader(text string) (header, body string) {
	if !strings.HasPrefix(text, "<!--") {
		return "", text
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "-->" {
			return strings.Join(lines[:i], "\n"), strings.Join(lines[i+1:], "\n")
		}
	}
	return "", text
}

// spansCommits reports whether a draft's inline comments are on more than
// one commit (some written before the author pushed).
func spansCommits(d core.Draft) bool {
	seen := ""
	for _, c := range d.Comments {
		at := c.Commit
		if at == "" {
			at = d.Commit
		}
		if seen != "" && at != seen {
			return true
		}
		seen = at
	}
	return false
}

// suggestedAction is what a current agent review suggests doing with the PR
// ("close", "draft" or "") and why.
func (m *Model) suggestedAction(key string) (string, string) {
	for _, r := range store.Current(m.review[key]) {
		if r.Action != "" && (r.Status == "done" || r.Status == "posted") {
			return r.Action, r.ActionReason
		}
	}
	return "", ""
}
