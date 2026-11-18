package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/tmstorm/prowl/internal/core"
)

type lineKind int

const (
	lkFile lineKind = iota
	lkHunk
	lkAdd
	lkDel
	lkCtx
	lkMeta
)

type diffLine struct {
	kind    lineKind
	text    string
	path    string
	oldLine int
	newLine int
}

// commentable reports whether a GitHub inline comment can anchor here.
func (l diffLine) commentable() bool {
	return l.kind == lkAdd || l.kind == lkDel || l.kind == lkCtx
}

// anchor returns the (line, side) GitHub expects for an inline comment.
func (l diffLine) anchor() (int, string) {
	if l.kind == lkDel {
		return l.oldLine, "LEFT"
	}
	return l.newLine, "RIGHT"
}

func parseDiff(raw string) []diffLine {
	var out []diffLine
	var path string
	var oldN, newN int
	inHunk := false
	for _, l := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(l, "diff --git "):
			inHunk = false

			if i := strings.LastIndex(l, " b/"); i >= 0 {
				path = l[i+3:]
			}
			out = append(out, diffLine{kind: lkFile, text: path, path: path})
		case !inHunk && (strings.HasPrefix(l, "+++ ") || strings.HasPrefix(l, "--- ") ||
			strings.HasPrefix(l, "index ") || strings.HasPrefix(l, "new file") ||
			strings.HasPrefix(l, "deleted file") || strings.HasPrefix(l, "similarity") ||
			strings.HasPrefix(l, "rename ") || strings.HasPrefix(l, "old mode") || strings.HasPrefix(l, "new mode")):
			if strings.HasPrefix(l, "+++ b/") {
				path = l[6:]
			}
			if strings.HasPrefix(l, "new file") || strings.HasPrefix(l, "deleted file") || strings.HasPrefix(l, "rename ") {
				out = append(out, diffLine{kind: lkMeta, text: l, path: path})
			}
		case strings.HasPrefix(l, "@@"):
			inHunk = true
			oldN, newN = parseHunk(l)
			out = append(out, diffLine{kind: lkHunk, text: l, path: path})
		case strings.HasPrefix(l, "+"):
			out = append(out, diffLine{kind: lkAdd, text: l[1:], path: path, newLine: newN})
			newN++
		case strings.HasPrefix(l, "-"):
			out = append(out, diffLine{kind: lkDel, text: l[1:], path: path, oldLine: oldN})
			oldN++
		case strings.HasPrefix(l, " "):
			out = append(out, diffLine{kind: lkCtx, text: l[1:], path: path, oldLine: oldN, newLine: newN})
			oldN++
			newN++
		case strings.HasPrefix(l, `\`):
			out = append(out, diffLine{kind: lkMeta, text: l, path: path})
		}
	}
	return out
}

func parseHunk(h string) (int, int) {
	f := strings.Fields(h)
	if len(f) < 3 {
		return 0, 0
	}
	num := func(s string) int {
		s = strings.TrimLeft(s, "-+")
		s, _, _ = strings.Cut(s, ",")
		n, _ := strconv.Atoi(s)
		return n
	}
	return num(f[1]), num(f[2])
}

func renderDiffLine(l diffLine, width int) string {
	text := strings.ReplaceAll(l.text, "\t", "    ")
	var s string
	switch l.kind {
	case lkFile:
		s = sTitle.Render("━━ " + l.text + " " + strings.Repeat("━", max(width-len(l.text)-6, 0)))
	case lkHunk:
		s = sBlue.Render(l.text)
	case lkMeta:
		s = sDim.Render(l.text)
	case lkAdd:
		s = sDim.Render(lineNum(0)+" "+lineNum(l.newLine)) + " " + sGreen.Render("+"+text)
	case lkDel:
		s = sDim.Render(lineNum(l.oldLine)+" "+lineNum(0)) + " " + sRed.Render("-"+text)
	case lkCtx:
		s = sDim.Render(lineNum(l.oldLine)+" "+lineNum(l.newLine)) + "  " + text
	}
	return trunc(" "+s, width)
}

func (d *detail) buildDiff() []brow {
	lines := d.diffs[d.sinceReview]
	if lines == nil {
		return nil
	}
	threads := map[string][]*core.Thread{}
	if d.det != nil {
		for i := range d.det.Threads {
			t := &d.det.Threads[i]
			if t.Line > 0 && !t.Outdated {
				side := t.DiffSide
				if side == "" {
					side = "RIGHT"
				}
				k := fmt.Sprintf("%s:%s:%d", t.Path, side, t.Line)
				threads[k] = append(threads[k], t)
			}
		}
	}
	var rows []brow
	file := ""
	folded := false
	addThreads := func(l diffLine) {
		ln, side := l.anchor()
		for _, t := range threads[fmt.Sprintf("%s:%s:%d", l.path, side, ln)] {
			for _, r := range d.threadRows(t, d.w, false) {
				r.file = file
				rows = append(rows, r)
			}
		}
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if l.kind == lkFile {
			file = l.path
			folded = d.fileFolded(file)
			rows = append(rows, brow{diff: i, file: file, block: "file:" + file, head: true, finding: -1, pair: -1})
			continue
		}
		if folded {
			continue
		}
		if splitDiff && l.kind == lkDel {
			dels, adds := changeRun(lines, i)
			for k := 0; k < max(len(dels), len(adds)); k++ {
				r := brow{diff: -1, file: file, finding: -1, pair: -1}
				switch {
				case k < len(dels) && k < len(adds):
					r.diff, r.pair = dels[k], adds[k]
				case k < len(dels):
					r.diff = dels[k]
				default:
					r.diff = adds[k]
				}
				rows = append(rows, r)
				addThreads(lines[r.diff])
				if r.pair >= 0 {
					addThreads(lines[r.pair])
				}
			}
			i += len(dels) + len(adds) - 1
			continue
		}
		rows = append(rows, brow{diff: i, file: file, finding: -1, pair: -1})
		if l.commentable() {
			addThreads(l)
		}
	}
	return rows
}

// changeRun is the removed lines starting at i and the added lines right
// after them, the lines the split diff pairs up row by row.
func changeRun(lines []diffLine, i int) (dels, adds []int) {
	for ; i < len(lines) && lines[i].kind == lkDel; i++ {
		dels = append(dels, i)
	}
	for ; i < len(lines) && lines[i].kind == lkAdd; i++ {
		adds = append(adds, i)
	}
	return dels, adds
}

// splitRow draws a row of the split diff: the old line on the left and the
// new line on the right, either side blank when the row has none.
func (d *detail) splitRow(r brow, lines []diffLine) string {
	left := (d.w - 1) / 2
	right := d.w - 1 - left
	var old, cur *diffLine
	l := lines[r.diff]
	switch {
	case l.kind == lkCtx:
		old, cur = &l, &l
	case l.kind == lkDel:
		old = &l
		if r.pair >= 0 {
			cur = &lines[r.pair]
		}
	default:
		cur = &l
	}
	side := func(dl *diffLine, idx, width int, n int) string {
		if dl == nil {
			return strings.Repeat(" ", width)
		}
		return renderCode(*dl, d.highlighter().get(lines, idx), width, d.pendingIdx(*dl) >= 0, lineNum(n)+" ")
	}
	newIdx := r.diff
	if r.pair >= 0 {
		newIdx = r.pair
	}
	var oldN, newN int
	if old != nil {
		oldN = old.oldLine
	}
	if cur != nil {
		newN = cur.newLine
	}
	return side(old, r.diff, left, oldN) + sBorder.Render("│") + side(cur, newIdx, right, newN)
}

func (d *detail) pendingIdx(l diffLine) int {
	line, side := l.anchor()
	for i, p := range d.pending {
		if p.Path == l.path && p.Line == line && p.Side == side {
			return i
		}
	}
	return -1
}

func (d *detail) pendingAt(l diffLine) string {
	if i := d.pendingIdx(l); i >= 0 {
		return d.pending[i].Body
	}
	return ""
}

func (d *detail) setPending(sel []diffLine, body string) {
	last := sel[len(sel)-1]
	line, side := last.anchor()
	i := d.pendingIdx(last)
	d.dirty = true
	if strings.TrimSpace(body) == "" {
		if i >= 0 {
			d.pending = append(d.pending[:i], d.pending[i+1:]...)
		}
		return
	}
	c := core.DraftComment{Path: last.path, Line: line, Side: side, Body: body}
	if len(sel) > 1 {
		c.StartLine, c.StartSide = sel[0].anchor()
	}
	if i >= 0 {
		d.pending[i] = c
	} else {
		d.pending = append(d.pending, c)
	}
}

func (d *detail) removePending(l diffLine) bool {
	if i := d.pendingIdx(l); i >= 0 {
		d.pending = append(d.pending[:i], d.pending[i+1:]...)
		d.dirty = true
		return true
	}
	return false
}

func (d *detail) fileHeader(path string) string {
	folded := d.fileFolded(path)
	var stats, state string
	if d.det != nil {
		for _, f := range d.det.Files {
			if f.Path == path {
				stats = sGreen.Render(fmt.Sprintf("+%d", f.Additions)) + " " + sRed.Render(fmt.Sprintf("-%d", f.Deletions))
			}
		}
	}
	switch d.viewed[path] {
	case "VIEWED":
		state = sGreen.Render(" ✓ viewed")
	case "DISMISSED":
		state = sYellow.Render(" ~ changed since viewed")
	}
	left := " " + foldGlyph(folded) + sTitle.Render(path) + " "
	right := " " + stats + state + " "
	fill := d.w - lipgloss.Width(left) - lipgloss.Width(right)
	return trunc(left+sBorder.Render(strings.Repeat("─", max(fill, 1)))+right, d.w)
}
