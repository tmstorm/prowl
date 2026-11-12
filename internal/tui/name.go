package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const appName = "prowl"

// font is a 3-row box-drawing alphabet (figlet "future" style).
var font = map[rune][3]string{
	'a': {"┏━┓", "┣━┫", "╹ ╹"}, 'b': {"┏┓ ", "┣┻┓", "┗━┛"}, 'c': {"┏━╸", "┃  ", "┗━╸"},
	'd': {"╺┳┓", " ┃┃", "╺┻┛"}, 'e': {"┏━╸", "┣╸ ", "┗━╸"}, 'f': {"┏━╸", "┣╸ ", "╹  "},
	'g': {"┏━╸", "┃╺┓", "┗━┛"}, 'h': {"╻ ╻", "┣━┫", "╹ ╹"}, 'i': {"╻", "┃", "╹"},
	'j': {"  ╻", "  ┃", "┗━┛"}, 'k': {"╻┏ ", "┣┻┓", "╹ ╹"}, 'l': {"╻  ", "┃  ", "┗━╸"},
	'm': {"┏┳┓", "┃┃┃", "╹ ╹"}, 'n': {"┏┓╻", "┃┗┫", "╹ ╹"}, 'o': {"┏━┓", "┃ ┃", "┗━┛"},
	'p': {"┏━┓", "┣━┛", "╹  "}, 'q': {"┏━┓", "┃┓┃", "┗┻┛"}, 'r': {"┏━┓", "┣┳┛", "╹┗╸"},
	's': {"┏━┓", "┗━┓", "┗━┛"}, 't': {"╺┳╸", " ┃ ", " ╹ "}, 'u': {"╻ ╻", "┃ ┃", "┗━┛"},
	'v': {"╻ ╻", "┃┏┛", "┗┛ "}, 'w': {"╻ ╻", "┃╻┃", "┗┻┛"}, 'x': {"╻ ╻", "┏╋┛", "╹ ╹"},
	'y': {"╻ ╻", "┗┳┛", " ╹ "}, 'z': {"━━┓", "┏━┛", "┗━╸"}, '-': {"   ", "╺━╸", "   "},
}

func bigName() []string {
	var rows [3]strings.Builder
	for i, r := range appName {
		g, ok := font[r]
		if !ok {
			continue
		}
		for j := range rows {
			if i > 0 {
				rows[j].WriteString(" ")
			}
			rows[j].WriteString(g[j])
		}
	}
	return []string{rows[0].String(), rows[1].String(), rows[2].String()}
}

// The owl is prowl's mascot (p-r-owl). It sleeps on an empty inbox and wakes
// up while the first sync runs. Turned off with playful = false.

// playful mirrors the playful setting for code without the config at hand.
var playful = true

// beside puts a drawing to the left of text lines.
func beside(art []string, artW int, text []string) []string {
	n := max(len(art), len(text))
	out := make([]string, n)
	for i := range out {
		a, t := "", ""
		if i < len(art) {
			a = art[i]
		}
		if i < len(text) {
			t = text[i]
		}
		out[i] = fit(a, artW) + t
	}
	return out
}

// owl draws the owl with the given eye ("-" asleep, "." stirring, "o"/"O"
// awake), looking left (-1), ahead (0) or right (1). small is the five-row
// owl for short panes; the full one is a tufted great horned owl.
func owl(eye string, look int, small bool) []string {
	o := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Muted)).Render
	e := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Yellow)).Bold(true).Render(eye)
	v, talon := sOrange.Render("v"), sOrange.Render("^")
	pad := func(n int) string { return strings.Repeat(" ", n) }
	left, right := 1+look, 1-look
	if small {
		return []string{
			o("    ,_____,"),
			"    " + o("("+pad(left)) + e + o(",") + e + o(pad(right)+")"),
			o("    /)   (\\"),
			o("   ((  \"  ))"),
			o("  ===") + talon + o("===") + talon + o("==="),
		}
	}
	return []string{
		o("   /\\       /\\"),
		o("  /  `-----'  \\"),
		" " + o("|"+pad(left+1)+"(") + e + o(")   (") + e + o(")"+pad(right+1)+"|"),
		o("  \\     ") + v + o("     /"),
		o("   )  ' ' '  ("),
		o("  (  ' ' ' '  )"),
		o("   `-._____.-'"),
		o("  ======") + talon + o("=") + talon + o("======"),
	}
}

// owlWidth is the width of the drawing, for centring it as a block.
func owlWidth(small bool) int {
	if small {
		return 13
	}
	return 17
}

// sleepingOwl is the owl asleep with z's drifting up, step advancing once a
// tick.
func sleepingOwl(step int, small bool) []string {
	z := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Accent))
	start := 15
	if small {
		start = 12
	}
	rows := []string{"", "", ""}

	for _, age := range []int{step % 4, (step + 2) % 4} {
		if age < 3 {
			ch := "z"
			if age == 2 {
				ch = "Z"
			}
			rows[2-age] = strings.Repeat(" ", start+2*age) + z.Render(ch)
		}
	}
	return append(rows, owl("-", 0, small)...)
}

// owlPose is a frame of the waking owl.
type owlPose struct {
	eye  string
	look int
}

// wakingOwl wakes up, then keeps looking around and blinking.
func wakingOwl(step int, small bool) []string {
	wake := []owlPose{{"-", 0}, {"-", 0}, {".", 0}, {"o", 0}, {"O", 0}}
	watch := []owlPose{{"O", 0}, {"O", 0}, {"O", -1}, {"O", -1}, {"O", 0}, {"O", 1}, {"O", 1}, {"O", 0}, {"-", 0}, {"O", 0}}
	p := watch[0]
	if step < len(wake) {
		p = wake[step]
	} else {
		p = watch[(step-len(wake))%len(watch)]
	}
	return owl(p.eye, p.look, small)
}
