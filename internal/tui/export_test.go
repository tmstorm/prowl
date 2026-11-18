package tui

// Owl draws the owl, so a test can pin its look.
var Owl = owl

// FloatingChat says whether the chat can open in a window, so a test can
// act as Windows.
var FloatingChat = &floatingChat

// RenderMarkdown renders Markdown as the panes do.
var RenderMarkdown = renderMarkdown

// LeaderKeys lists every leader binding as the docs write it: <leader>a, or
// <leader>tr for r in the t menu.
func LeaderKeys() []string {
	var out []string
	for _, k := range leaderKeys {
		if k.sub == nil {
			out = append(out, "<leader>"+k.key)
		}
		for _, s := range k.sub {
			out = append(out, "<leader>"+k.key+s.key)
		}
	}
	return out
}
