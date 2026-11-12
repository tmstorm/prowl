# Records the README's GIF and screenshots from the demo data.
# From the repo root: vhs docs/demo/demo.tape

Output docs/images/demo.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 15
Set Width 1600
Set Height 960
Set Padding 16
Set Theme "Dracula"
Set TypingSpeed 120ms
Set CursorBlink false

Hide
Type "go build -o /tmp/prowl-demo ./docs/demo && clear"
Enter
Wait@120s
Type "/tmp/prowl-demo"
Enter
Sleep 300ms
Show

# Starting up: the demo's GitHub answers after a few seconds, like a real one.
Wait+Screen@30s /NEEDS REVIEW/

# The inbox: what needs you, sorted by what you owe.
Sleep 2.5s
Screenshot docs/images/inbox.png
Down
Sleep 700ms
Down
Sleep 700ms
Up
Sleep 700ms
Up
Sleep 1s

# A PR: AI summary, reviewers, CI, activity.
Enter
Sleep 3s

# The diff, side by side and full width. The last Down comes after the
# side-by-side status message expires, so the screenshot has the statusline.
Type "3"
Sleep 1s
Type "s"
Sleep 1.2s
Type "+"
Sleep 1.2s
Down 17
Sleep 1.8s
Down
Sleep 1.5s
Screenshot docs/images/diff.png
Sleep 1s

# The AI review: findings, notes, points raised.
Type "4"
Sleep 3s
Screenshot docs/images/ai-review.png
Sleep 1s

# Every action behind the leader key.
Space
Sleep 3s
Escape
Sleep 500ms
Type "+"
Sleep 2s

Hide
Type "q"
