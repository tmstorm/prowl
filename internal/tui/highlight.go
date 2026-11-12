package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	cstyles "github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
)

// seg is a run of code with one syntax color.
type seg struct {
	text         string
	fg           string // hex, "" = default
	bold, italic bool
}

// hlLine is a syntax highlighted diff line plus the rune range that changed
// relative to its paired line (word diff), if any.
type hlLine struct {
	segs []seg
	emph [][2]int // rune ranges [a,b) that changed vs the paired line
}

func (h *hlLine) emphAt(pos int) (in bool, next int) {
	next = 1 << 30
	for _, r := range h.emph {
		if pos >= r[0] && pos < r[1] {
			return true, r[1]
		}
		if r[0] > pos && r[0] < next {
			next = r[0]
		}
	}
	return false, next
}

// highlighter caches highlighted lines per diff line index.
type highlighter struct {
	theme string
	lines map[int]*hlLine
}

func (h *highlighter) reset(theme string) {
	h.theme = theme
	h.lines = map[int]*hlLine{}
}

func expandTabs(s string) string { return strings.ReplaceAll(s, "\t", "    ") }

func lexerFor(path, sample string) chroma.Lexer {
	l := lexers.Match(path)
	if l == nil {
		for _, suf := range []string{".tftpl", ".tmpl", ".tpl", ".j2", ".in"} {
			if strings.HasSuffix(path, suf) {
				l = lexers.Match(strings.TrimSuffix(path, suf))
				break
			}
		}
	}
	if l == nil {
		l = lexers.Analyse(sample)
	}
	if l == nil {
		l = lexers.Fallback
	}
	return chroma.Coalesce(l)
}

// get returns the highlighted line i, highlighting its whole hunk on first use
// so multi-line constructs (block comments, strings) lex correctly. Context
// lines take the new side's tokens; removed lines take the old side's.
func (h *highlighter) get(lines []diffLine, i int) *hlLine {
	if l, ok := h.lines[i]; ok {
		return l
	}

	start := i
	for start > 0 && lines[start].kind != lkHunk && lines[start].kind != lkFile {
		start--
	}
	end := i
	for end+1 < len(lines) && lines[end+1].kind != lkHunk && lines[end+1].kind != lkFile {
		end++
	}
	var oldIdx, newIdx []int
	var oldTxt, newTxt []string
	for j := start; j <= end; j++ {
		switch lines[j].kind {
		case lkCtx:
			oldIdx, newIdx = append(oldIdx, j), append(newIdx, j)
			t := expandTabs(lines[j].text)
			oldTxt, newTxt = append(oldTxt, t), append(newTxt, t)
		case lkDel:
			oldIdx = append(oldIdx, j)
			oldTxt = append(oldTxt, expandTabs(lines[j].text))
		case lkAdd:
			newIdx = append(newIdx, j)
			newTxt = append(newTxt, expandTabs(lines[j].text))
		}
	}
	lex := lexerFor(lines[i].path, strings.Join(newTxt, "\n"))
	style := cstyles.Get(chromaThemes[h.theme])

	for _, side := range []struct {
		idx []int
		txt []string
		del bool
	}{{oldIdx, oldTxt, true}, {newIdx, newTxt, false}} {
		segLines := tokenize(lex, style, side.txt)
		for k, j := range side.idx {
			if side.del && lines[j].kind != lkDel {
				continue
			}
			h.lines[j] = &hlLine{segs: segLines[k]}
		}
	}
	wordDiff(lines, start, end, h.lines)
	if l, ok := h.lines[i]; ok {
		return l
	}
	h.lines[i] = &hlLine{segs: []seg{{text: expandTabs(lines[i].text)}}}
	return h.lines[i]
}

func tokenize(lex chroma.Lexer, style *chroma.Style, txt []string) [][]seg {
	out := make([][]seg, len(txt))
	it, err := lex.Tokenise(nil, strings.Join(txt, "\n")+"\n")
	if err != nil {
		for k, t := range txt {
			out[k] = []seg{{text: t}}
		}
		return out
	}
	line := 0
	for tok := it(); tok != chroma.EOF; tok = it() {
		e := style.Get(tok.Type)
		s := seg{bold: e.Bold == chroma.Yes, italic: e.Italic == chroma.Yes}
		if e.Colour.IsSet() {
			s.fg = e.Colour.String()
		}
		parts := strings.Split(tok.Value, "\n")
		for p, part := range parts {
			if p > 0 {
				line++
			}
			if part != "" && line < len(out) {
				s.text = part
				out[line] = append(out[line], s)
			}
		}
	}
	return out
}

// wordDiff pairs each run of deleted lines with the following run of added
// lines and marks the words that differ in each pair, like GitHub's inline
// change highlighting.
func wordDiff(lines []diffLine, start, end int, hl map[int]*hlLine) {
	for j := start; j <= end; {
		if lines[j].kind != lkDel {
			j++
			continue
		}
		dStart := j
		for j <= end && lines[j].kind == lkDel {
			j++
		}
		aStart := j
		for j <= end && lines[j].kind == lkAdd {
			j++
		}
		for k := 0; k < min(aStart-dStart, j-aStart); k++ {
			oldL, newL := hl[dStart+k], hl[aStart+k]
			if oldL == nil || newL == nil {
				continue
			}
			oe, ne, ok := changedRanges(expandTabs(lines[dStart+k].text), expandTabs(lines[aStart+k].text))
			if ok {
				oldL.emph, newL.emph = oe, ne
			}
		}
	}
}

type wtok struct {
	text       string
	start, end int
}

// words splits a line into identifier runs, whitespace runs and single
// punctuation characters, with rune offsets.
func words(s string) []wtok {
	var out []wtok
	rs := []rune(s)
	class := func(r rune) int {
		switch {
		case r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127:
			return 1
		case r == ' ':
			return 2
		}
		return 3
	}
	for i := 0; i < len(rs); {
		c := class(rs[i])
		j := i + 1
		if c != 3 {
			for j < len(rs) && class(rs[j]) == c {
				j++
			}
		}
		out = append(out, wtok{string(rs[i:j]), i, j})
		i = j
	}
	return out
}

// changedRanges diffs two lines word by word (LCS) and returns the rune
// ranges that differ on each side. ok is false when the lines share too
// little for emphasis to help.
// lineNum is a right-aligned line number for the gutter, blank for 0.
func lineNum(n int) string {
	if n == 0 {
		return "    "
	}
	return fmt.Sprintf("%4d", n)
}

func changedRanges(a, b string) (ra, rb [][2]int, ok bool) {
	wa, wb := words(a), words(b)
	if len(wa)*len(wb) > 40000 {
		return nil, nil, false
	}

	dp := make([][]int, len(wa)+1)
	for i := range dp {
		dp[i] = make([]int, len(wb)+1)
	}
	for i := len(wa) - 1; i >= 0; i-- {
		for j := len(wb) - 1; j >= 0; j-- {
			if wa[i].text == wb[j].text {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	keepA, keepB := make([]bool, len(wa)), make([]bool, len(wb))
	same := 0
	for i, j := 0, 0; i < len(wa) && j < len(wb); {
		switch {
		case wa[i].text == wb[j].text:
			keepA[i], keepB[j] = true, true
			same += len([]rune(wa[i].text))
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	longest := max(len([]rune(a)), len([]rune(b)))
	if longest == 0 || float64(same) < 0.4*float64(longest) {
		return nil, nil, false
	}
	collect := func(ws []wtok, keep []bool) [][2]int {
		var out [][2]int
		for i, w := range ws {
			if keep[i] || strings.TrimSpace(w.text) == "" && (i == 0 || keep[i-1]) && (i+1 >= len(ws) || keep[i+1]) {
				continue
			}
			if n := len(out); n > 0 && out[n-1][1] == w.start {
				out[n-1][1] = w.end
			} else {
				out = append(out, [2]int{w.start, w.end})
			}
		}
		return out
	}
	return collect(wa, keepA), collect(wb, keepB), true
}

// ---- colors

func hexRGB(h string) (int, int, int) {
	h = strings.TrimPrefix(h, "#")
	v, _ := strconv.ParseUint(h, 16, 32)
	return int(v >> 16 & 0xff), int(v >> 8 & 0xff), int(v & 0xff)
}

// blend mixes color a into b by t (0..1).
func blend(a, b string, t float64) string {
	ar, ag, ab := hexRGB(a)
	br, bg, bb := hexRGB(b)
	mix := func(x, y int) int { return int(float64(x)*t + float64(y)*(1-t)) }
	return fmt.Sprintf("#%02x%02x%02x", mix(ar, br), mix(ag, bg), mix(ab, bb))
}

type diffTints struct{ add, addEmph, addGutter, del, delEmph, delGutter string }

func tints() diffTints {
	return diffTints{
		add: blend(pal.Green, pal.Bg, 0.13), addEmph: blend(pal.Green, pal.Bg, 0.35), addGutter: blend(pal.Green, pal.Bg, 0.22),
		del: blend(pal.Red, pal.Bg, 0.13), delEmph: blend(pal.Red, pal.Bg, 0.35), delGutter: blend(pal.Red, pal.Bg, 0.22),
	}
}

// renderCode renders a highlighted add/del/context line: marker, line number
// gutter, sign, code, with a GitHub-style tinted background for changes.
// renderCode draws a code line of the diff with syntax colors and change
// tints. gutter is the line number column: both numbers in the unified diff,
// one side's in the split diff.
func renderCode(l diffLine, hl *hlLine, width int, hasComment bool, gutter string) string {
	t := tints()
	var bg, emph, gutterBg, sign string
	signStyle := sDim
	switch l.kind {
	case lkAdd:
		bg, emph, gutterBg, sign = t.add, t.addEmph, t.addGutter, "+"
		signStyle = sGreen
	case lkDel:
		bg, emph, gutterBg, sign = t.del, t.delEmph, t.delGutter, "-"
		signStyle = sRed
	default:
		sign = " "
	}
	mark := " "
	if hasComment {
		mark = sYellow.Render("◆")
	}
	withBg := func(s lipgloss.Style, c string) lipgloss.Style {
		if c == "" {
			return s
		}
		return s.Background(lipgloss.Color(c))
	}
	var b strings.Builder
	b.WriteString(mark)
	b.WriteString(withBg(sDim, gutterBg).Render(gutter))
	b.WriteString(withBg(signStyle, bg).Render(sign + " "))
	used := 1 + lipgloss.Width(gutter) + 2

	pos := 0
	for _, sg := range hl.segs {
		runes := []rune(sg.text)
		for len(runes) > 0 {
			n := len(runes)
			inEmph, next := hl.emphAt(pos)
			if pos+n > next {
				n = next - pos
			}
			st := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Fg))
			if sg.fg != "" {
				st = st.Foreground(lipgloss.Color(sg.fg))
			}
			st = st.Bold(sg.bold).Italic(sg.italic)
			c := bg
			if inEmph {
				c = emph
			}
			st = withBg(st, c)
			b.WriteString(st.Render(string(runes[:n])))
			used += n
			pos += n
			runes = runes[n:]
		}
	}
	if pad := width - used; pad > 0 {
		b.WriteString(withBg(lipgloss.NewStyle(), bg).Render(strings.Repeat(" ", pad)))
	}
	return trunc(b.String(), width)
}
