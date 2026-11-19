package tui

import (
	"fmt"
	"strings"
	"testing"
)

// bigDiff is a made-up diff of about 5,000 lines over 20 Go files.
func bigDiff() string {
	var b strings.Builder
	for f := range 20 {
		fmt.Fprintf(&b, "diff --git a/pkg/f%d.go b/pkg/f%d.go\n--- a/pkg/f%d.go\n+++ b/pkg/f%d.go\n@@ -1,250 +1,250 @@\n", f, f, f, f)
		for i := range 250 {
			switch i % 5 {
			case 0:
				fmt.Fprintf(&b, "-func old%d(ctx context.Context, n int) (string, error) { return fmt.Sprint(n), nil }\n", i)
			case 1:
				fmt.Fprintf(&b, "+func new%d(ctx context.Context, n int) (string, error) {\n", i)
			default:
				fmt.Fprintf(&b, " \tif err := check(ctx, %d); err != nil { // keep going\n", i)
			}
		}
	}
	return b.String()
}

func BenchmarkParseDiff(b *testing.B) {
	raw := bigDiff()
	for b.Loop() {
		parseDiff(raw)
	}
}

func BenchmarkRenderDiffLines(b *testing.B) {
	lines := parseDiff(bigDiff())
	for b.Loop() {
		for _, l := range lines {
			renderDiffLine(l, 120)
		}
	}
}

func BenchmarkHighlightDiff(b *testing.B) {
	lines := parseDiff(bigDiff())
	var h highlighter
	for b.Loop() {
		h.reset("dracula")
		for i := range lines {
			h.get(lines, i)
		}
	}
}

// BenchmarkRenderConversation renders a long made-up PR conversation.
func BenchmarkRenderConversation(b *testing.B) {
	var src strings.Builder
	for i := range 200 {
		fmt.Fprintf(&src, "### alice commented\n\nThe change in `pkg/f%d.go` looks right, but:\n\n- the loop allocates on every pass\n- **naming**: prefer `count` over `n`\n\n```go\nfor i := range items {\n\tout = append(out, convert(items[i]))\n}\n```\n\n> quoted reply from bob about item %d\n\n", i, i)
	}
	for b.Loop() {
		renderMarkdown(src.String(), 100)
	}
}
