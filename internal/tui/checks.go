package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tmstorm/prowl/internal/gh"
)

// ciLog is the end of a failed check's log, as gl loads it.
type ciLog struct {
	step    string
	lines   []string
	err     error
	loading bool
}

type ciLogMsg struct {
	key   string
	job   int64
	step  string
	lines []string
	err   error
}

// summaryLines is how much of a failed check's own summary shows.
const summaryLines = 8

// checksRows draws the PR's CI: failed checks open with their reason
// (required ones first), running ones a line each, the rest folded into one
// line.
func (d *detail) checksRows(a *rowAdder, w int, section func(string)) {
	var failed, running, rest []gh.Check
	for _, c := range d.det.Checks {
		switch c.State {
		case "FAIL":
			failed = append(failed, c)
		case "PENDING":
			running = append(running, c)
		default:
			rest = append(rest, c)
		}
	}
	if len(d.det.Checks) == 0 {
		return
	}
	slices.SortStableFunc(failed, func(x, y gh.Check) int {
		switch {
		case x.Required == y.Required:
			return 0
		case x.Required:
			return -1
		}
		return 1
	})
	var counts []string
	if len(failed) > 0 {
		counts = append(counts, sRed.Render(fmt.Sprintf("%d failed", len(failed))))
	}
	if len(running) > 0 {
		counts = append(counts, sYellow.Render(fmt.Sprintf("%d running", len(running))))
	}
	passed := 0
	for _, c := range rest {
		if c.State == "PASS" {
			passed++
		}
	}
	if passed > 0 {
		counts = append(counts, sGreen.Render(fmt.Sprintf("%d passed", passed)))
	}
	if n := len(rest) - passed; n > 0 {
		counts = append(counts, sDim.Render(fmt.Sprintf("%d skipped", n)))
	}
	section("Checks" + sDim.Render(" · ") + strings.Join(counts, sDim.Render(" · ")))

	for _, c := range failed {
		id := "check:" + c.URL
		folded := d.folded(id, false)
		a.end()
		a.begin(id, "", folded)
		a.add(" " + foldGlyph(folded) + sRed.Render("✗ ") + checkLine(c))
		if folded {
			continue
		}
		if c.Title != "" {
			a.add("    " + sBold.Render(fit(c.Title, w-6)))
		}
		summary := renderMarkdown(c.Summary, w-6, themeName)
		for i, l := range summary {
			if i == summaryLines {
				a.add("    " + sDim.Render(fmt.Sprintf("… %d more lines · o opens the check", len(summary)-summaryLines)))
				break
			}
			a.add("    " + l)
		}
		for _, an := range c.Annotations {
			style := sYellow
			if an.Level == "FAILURE" {
				style = sRed
			}
			msg, _, _ := strings.Cut(an.Message, "\n")
			a.add("    " + style.Render(fmt.Sprintf("%s:%d", an.Path, an.Line)) + "  " + fit(msg, w-lenLoc(an)-8))
		}
		d.logRows(a, c, w)
	}
	a.end()
	for _, c := range running {
		a.begin("check:"+c.URL, "", false)
		a.add("   " + sYellow.Render("● ") + checkLine(c))
		a.end()
	}
	if len(rest) == 0 {
		return
	}
	folded := d.folded("checks:rest", true)
	a.begin("checks:rest", "", folded)
	head := sGreen.Render(fmt.Sprintf("✓ %d passed", passed))
	if n := len(rest) - passed; n > 0 {
		head += sDim.Render(fmt.Sprintf(" · %d skipped", n))
	}
	a.add(" " + foldGlyph(folded) + head)
	if !folded {
		for _, c := range rest {
			mark := sGreen.Render("✓ ")
			if c.State != "PASS" {
				mark = sDim.Render("– ")
			}
			a.add("     " + mark + checkLine(c))
		}
	}
	a.end()
}

// logRows shows a failed Actions check's log once gl has loaded it.
func (d *detail) logRows(a *rowAdder, c gh.Check, w int) {
	if c.JobID == 0 {
		return
	}
	l, ok := d.ciLogs[c.JobID]
	switch {
	case !ok:
		a.add("    " + sDim.Render("gl shows the end of the log"))
	case l.loading:
		a.add("    " + sDim.Render("loading the log…"))
	case l.err != nil:
		a.add("    " + sErr.Render(fit(l.err.Error(), w-6)))
	default:
		head := "Log"
		if l.step != "" {
			head += " · failed at " + l.step
		}
		a.add("    " + sSection.Render(head))
		for _, line := range l.lines {
			a.add("      " + sDim.Render(fit(line, w-8)))
		}
	}
}

func lenLoc(an gh.Annotation) int { return len(fmt.Sprintf("%s:%d", an.Path, an.Line)) }

// checkLine is a check's name, workflow, whether it is required, and how
// long it ran or has been running.
func checkLine(c gh.Check) string {
	s := sBold.Render(c.Name)
	if c.Workflow != "" && c.Workflow != c.Name {
		s += sDim.Render(" · " + c.Workflow)
	}
	if c.Required {
		s += sYellow.Render(" · required")
	}
	switch {
	case c.Started.IsZero():
	case c.Finished.IsZero():
		s += sDim.Render(" · running " + span(time.Since(c.Started)))
	default:
		s += sDim.Render(" · " + span(c.Finished.Sub(c.Started)))
	}
	return s
}

func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// checkAtCursor is the check whose rows the overview cursor is on.
func (d *detail) checkAtCursor() *gh.Check {
	v := &d.navs[tabOverview]
	rows := d.rows[tabOverview]
	if d.det == nil || v.cur >= len(rows) {
		return nil
	}
	url, ok := strings.CutPrefix(rows[v.cur].block, "check:")
	if !ok {
		return nil
	}
	for i := range d.det.Checks {
		if d.det.Checks[i].URL == url {
			return &d.det.Checks[i]
		}
	}
	return nil
}

// loadCheckLog fetches the end of the failed check's log under the cursor.
func (m *Model) loadCheckLog(d *detail) tea.Cmd {
	c := d.checkAtCursor()
	if c == nil || c.State != "FAIL" || c.JobID == 0 {
		m.info("put the cursor on a failed GitHub Actions check")
		return nil
	}
	if d.ciLogs == nil {
		d.ciLogs = map[int64]ciLog{}
	}
	d.ciLogs[c.JobID] = ciLog{loading: true}
	d.dirty = true
	co, key, job := m.core, d.item.PR.Key(), c.JobID
	return func() tea.Msg {
		step, lines, err := co.CheckLog(key, job)
		return ciLogMsg{key, job, step, lines, err}
	}
}
