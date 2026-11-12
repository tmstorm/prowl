package tui

import (
	"fmt"
	"strconv"
	"strings"
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

func renderDiffLine(l diffLine, width int, hasComment bool) string {
	gutter := func(n int) string {
		if n == 0 {
			return "    "
		}
		return fmt.Sprintf("%4d", n)
	}
	mark := " "
	if hasComment {
		mark = sYellow.Render("◆")
	}
	text := strings.ReplaceAll(l.text, "\t", "    ")
	var s string
	switch l.kind {
	case lkFile:
		s = mark + sTitle.Render("━━ "+l.text+" "+strings.Repeat("━", max(width-len(l.text)-6, 0)))
	case lkHunk:
		s = mark + sBlue.Render(l.text)
	case lkMeta:
		s = mark + sDim.Render(l.text)
	case lkAdd:
		s = mark + sDim.Render(gutter(0)+" "+gutter(l.newLine)) + " " + sGreen.Render("+"+text)
	case lkDel:
		s = mark + sDim.Render(gutter(l.oldLine)+" "+gutter(0)) + " " + sRed.Render("-"+text)
	case lkCtx:
		s = mark + sDim.Render(gutter(l.oldLine)+" "+gutter(l.newLine)) + "  " + text
	}
	return trunc(s, width)
}
