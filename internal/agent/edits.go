package agent

import (
	"fmt"
	"strings"

	"github.com/tmstorm/prowl/internal/store"
)

// edit is one piece of an agent draft and what you posted in its place.
type edit struct {
	where, draft, posted string
}

// diffPost lines a posted review up with its draft: the body, then each
// inline comment, matched by file and line, then by order within the file.
// Comments you deleted are dropped; ones you wrote yourself are added.
func diffPost(p store.Post) (changed []edit, dropped, added []store.Comment, unchanged int) {
	if strings.TrimSpace(p.Draft.Body) != strings.TrimSpace(p.Posted.Body) {
		changed = append(changed, edit{"review body", p.Draft.Body, p.Posted.Body})
	} else {
		unchanged++
	}
	posted := append([]store.Comment{}, p.Posted.Comments...)
	used := make([]bool, len(posted))
	match := func(d store.Comment, sameLine bool) int {
		for i, c := range posted {
			if !used[i] && c.Path == d.Path && (!sameLine || c.Line == d.Line) {
				return i
			}
		}
		return -1
	}
	var unmatched []store.Comment
	pair := func(d store.Comment, i int) {
		used[i] = true
		if strings.TrimSpace(d.Body) == strings.TrimSpace(posted[i].Body) {
			unchanged++
			return
		}
		changed = append(changed, edit{fmt.Sprintf("%s:%d", d.Path, d.Line), d.Body, posted[i].Body})
	}
	for _, d := range p.Draft.Comments {
		if i := match(d, true); i >= 0 {
			pair(d, i)
		} else {
			unmatched = append(unmatched, d)
		}
	}
	for _, d := range unmatched {
		if i := match(d, false); i >= 0 {
			pair(d, i)
		} else {
			dropped = append(dropped, d)
		}
	}
	for i, c := range posted {
		if !used[i] {
			added = append(added, c)
		}
	}
	return changed, dropped, added, unchanged
}

// EditsMarkdown shows how you edited agent drafts before posting them: each
// change as draft then posted, the comments you dropped and added, and how
// many pieces went out unchanged. It's meant for you, or an agent you ask to
// learn your style from it.
func EditsMarkdown(posts []store.Post) string {
	var b strings.Builder
	b.WriteString("# How I edit agent reviews\n\n")
	total := 0
	for _, p := range posts {
		changed, dropped, added, unchanged := diffPost(p)
		total += unchanged
		if len(changed)+len(dropped)+len(added) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s · %s\n\n%s\n\n", p.Key, p.At.Format("2006-01-02"), p.URL)
		for _, e := range changed {
			fmt.Fprintf(&b, "### %s\n\nDraft:\n\n%s\n\nPosted:\n\n%s\n\n", e.where, quote(e.draft), quote(e.posted))
		}
		for _, c := range dropped {
			fmt.Fprintf(&b, "### Dropped: %s:%d\n\n%s\n\n", c.Path, c.Line, quote(c.Body))
		}
		for _, c := range added {
			fmt.Fprintf(&b, "### Added: %s:%d\n\n%s\n\n", c.Path, c.Line, quote(c.Body))
		}
	}
	fmt.Fprintf(&b, "%d of the drafts' pieces (bodies and comments) went out unchanged across %d reviews.\n", total, len(posts))
	return b.String()
}

// editExamples is a prompt section with your n most recent rewrites of agent
// drafts, so the agent writes more the way you do.
func editExamples(posts []store.Post, n int) string {
	var ex []edit
	for i := len(posts) - 1; i >= 0 && len(ex) < n; i-- {
		changed, _, _, _ := diffPost(posts[i])
		for _, e := range changed {
			if len(ex) < n {
				ex = append(ex, e)
			}
		}
	}
	if len(ex) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## How the reviewer rewrites agent drafts\n\nRecent text an agent drafted, and what the reviewer posted instead. Write findings the way the reviewer does.\n\n")
	for _, e := range ex {
		fmt.Fprintf(&b, "Draft:\n%s\nPosted:\n%s\n\n", quote(oneLine(e.draft, 600)), quote(oneLine(e.posted, 600)))
	}
	return b.String()
}

func quote(s string) string { return indent(strings.TrimSpace(s), "> ") }
