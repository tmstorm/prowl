package tui

import "strconv"

// nav is a vim-style cursor over n rows displayed in a window of h rows.
// Rows for which skip returns true (section headers) are never selected.
type nav struct {
	cur, top  int
	n, h      int
	skip      func(int) bool
	scrolloff int
}

func (v *nav) selectable(i int) bool {
	return i >= 0 && i < v.n && (v.skip == nil || !v.skip(i))
}

// snap moves the cursor to the nearest selectable row, preferring direction dir.
func (v *nav) snap(dir int) {
	if v.n == 0 {
		v.cur, v.top = 0, 0
		return
	}
	if v.cur < 0 {
		v.cur = 0
	}
	if v.cur >= v.n {
		v.cur = v.n - 1
	}
	if dir == 0 {
		dir = 1
	}
	for _, d := range []int{dir, -dir} {
		for i := v.cur; i >= 0 && i < v.n; i += d {
			if v.selectable(i) {
				v.cur = i
				v.follow()
				return
			}
		}
	}
}

func (v *nav) setLen(n, h int) {
	v.n, v.h = n, h
	v.snap(1)
}

// move by count selectable rows.
func (v *nav) move(count int) {
	dir := 1
	if count < 0 {
		dir, count = -1, -count
	}
	for ; count > 0; count-- {
		next := v.cur + dir
		for next >= 0 && next < v.n && !v.selectable(next) {
			next += dir
		}
		if next < 0 || next >= v.n {
			break
		}
		v.cur = next
	}
	v.follow()
}

func (v *nav) jump(i int) {
	dir := 1
	if i < v.cur {
		dir = -1
	}
	v.cur = i
	v.snap(dir)
}

func (v *nav) first()            { v.cur = 0; v.snap(1) }
func (v *nav) last()             { v.cur = v.n - 1; v.snap(-1) }
func (v *nav) gotoLine(line int) { v.cur = line - 1; v.snap(1) }

// scrollBy scrolls the view and drags the cursor along (ctrl+d/u/f/b).
func (v *nav) scrollBy(d int) {
	v.top += d
	v.cur += d
	v.clampTop()
	v.snap(sign(d))
}

// scrollView scrolls without moving the cursor unless it leaves the window (ctrl+e/y, mouse wheel).
func (v *nav) scrollView(d int) {
	v.top += d
	v.clampTop()
	if v.cur < v.top {
		v.cur = v.top
		v.snap(1)
	} else if v.cur >= v.top+v.h {
		v.cur = v.top + v.h - 1
		v.snap(-1)
	}
}

func (v *nav) screen(pos byte) {
	switch pos {
	case 'H':
		v.cur = v.top
		v.snap(1)
	case 'M':
		end := min(v.top+v.h, v.n)
		v.cur = (v.top + end - 1) / 2
		v.snap(1)
	case 'L':
		v.cur = min(v.top+v.h, v.n) - 1
		v.snap(-1)
	}
}

func (v *nav) align(pos byte) {
	switch pos {
	case 't':
		v.top = v.cur
	case 'z':
		v.top = v.cur - v.h/2
	case 'b':
		v.top = v.cur - v.h + 1
	}
	v.clampTop()
}

func (v *nav) clampTop() {
	if v.top > v.n-v.h {
		v.top = v.n - v.h
	}
	if v.top < 0 {
		v.top = 0
	}
}

// follow keeps the cursor inside the window with scrolloff rows of context.
func (v *nav) follow() {
	so := v.scrolloff
	if so*2 >= v.h {
		so = (v.h - 1) / 2
	}
	if v.cur < v.top+so {
		v.top = v.cur - so
	}
	if v.cur > v.top+v.h-1-so {
		v.top = v.cur - v.h + 1 + so
	}
	v.clampTop()
}

// percent returns a lualine-style progress label.
func (v *nav) percent() string {
	switch {
	case v.n <= v.h:
		return "All"
	case v.top == 0:
		return "Top"
	case v.top+v.h >= v.n:
		return "Bot"
	}
	return strconv.Itoa(v.top*100/(v.n-v.h)) + "%"
}

func sign(d int) int {
	if d < 0 {
		return -1
	}
	return 1
}
