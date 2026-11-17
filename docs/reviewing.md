# Reading and reviewing a PR

## Moving around

Not a vim user? The arrow keys work: `↑`/`↓` move, `→` opens the PR pane and `←` goes back. `?` lists the keys, the line under the inbox legend reminds you of the basics, and the `<leader> menu` button at the bottom right opens every action with a click.

prowl has two panes: the inbox on the left and the PR pane on the right. Under 100 columns it shows one at a time. Numbers work like lazygit's: `0` focuses the inbox and `1` the PR pane, and `2`–`9` pick a tab inside whichever pane has focus (the inbox tabs on the left; Overview, Diff and the agent's review on the right). The pane titles show every number in brackets and counts in parentheses, e.g. `[0] [2] Needs me (18)` and `[1] repo#305 [2] Overview [3] Diff [4] AI review (3)`.

| Key | Does |
| --- | --- |
| `j`/`k`, `↑`/`↓` | move |
| `l`, `enter`, `→` | open the PR pane |
| `h`, `esc`, `←` | back to the inbox (`q` too; in the inbox `q` quits) |
| `+` | give the PR pane the whole width; `+`, `0` or `h` brings the inbox back |
| `tab`, `shift+tab` | next or previous tab |
| `gg`/`G`, `home`/`end` | top and bottom |
| `ctrl+d`/`ctrl+u` | half a page down or up |
| `ctrl+f`/`ctrl+b`, `pgdown`/`pgup` | a page down or up |
| `ctrl+e`/`ctrl+y` | scroll a line without moving the cursor |
| `H`/`M`/`L` | top, middle or bottom of the screen |
| `zz`/`zt`/`zb` | put the cursor line in the middle, top or bottom |
| `/`, `n`/`N` | search, next and previous (`N` searches backwards); `:noh` clears the highlight |
| `:42` | go to line 42 |
| `:help`, `:h` | open these docs; `:help stale_after` opens them at a key, option or heading, and `tab` completes it |
| `ctrl+p`, `<leader><leader>`, `<leader>ff` | fuzzy-find a PR, or paste a link to one |
| `o` | open the PR on GitHub |
| `R`, `<leader>r` | refresh now |
| `:q`, `<leader>q` | quit |

## Copying text

`yy` copies the cursor's line. `v` starts a selection, the motion keys extend it, and `y` copies it; dragging the mouse over lines selects them the same way. A diff line is copied as its code, without the line numbers or the `+`/`-`. Hold Shift while dragging to select with your terminal instead, for part of a line.

## Mouse

Everything on screen also works with the mouse (`mouse = false` turns it off). Click a PR, tab or section header; click a fold head in the PR pane to open or close it; scroll either pane, and drag over lines to select them (see [Copying text](#copying-text)). In menus and pickers, a click selects a row and a second click chooses it, and a click outside closes the menu. Clicking a finding again includes or excludes it, and clicking a diff line again starts a comment on it. The leader menu's entries, confirm prompts' `[ yes ]`/`[ no ]`, the statusline's sync text (refresh) and login warning (Accounts) are clickable too. Clicks and the wheel inside the chat window go to the agent. Nothing posts to GitHub on a single click: approving still asks `[y/N]`.

## The leader key

Anything that posts to GitHub sits behind the leader key, written `<leader>` in these docs: `<leader>a` means press the leader, then `a`. The leader is `space` unless you change it with `leader` in the config or in the options (`<leader>,`) to `,`, `\` or `;`; prowl's own hints always show the key you chose. Press it and a menu shows what comes next.

| Key | Does |
| --- | --- |
| `<leader>a` | approve, after a `y` |
| `<leader>x` | request changes, in your editor |
| `<leader>m` | comment review, in your editor |
| `<leader>n` | a plain comment in the conversation, not a review; pending inline comments stay pending |
| `<leader>t` | the thread under the cursor: `r` resolve, `u` unresolve, `c` reply |
| `<leader>A` | ask people or teams for a review |
| `<leader>R` | request yourself as a reviewer |
| `<leader>X` | close with a comment |
| `<leader>D` | make it a draft, or ready for review |
| `<leader>c` | AI reviews (see [AI reviews](ai-reviews.md)) |
| `<leader>p` | post the AI review |
| `<leader>l` | accounts and logins (see [Accounts](running.md#accounts)) |
| `<leader>fr`, `<leader>ft` | focus one repo or one commit type (see [Grouping and focusing](inbox.md#grouping-and-focusing)); `<leader>ff` finds a PR |
| `<leader>g`, `<leader>s`, `<leader>S`, `<leader>z`, `<leader>w` | group, snooze, unsnooze, show snoozed, watch a repo (see [The inbox](inbox.md)) |
| `<leader>y`, `<leader>Y`, `<leader>O` | Slack: post the review line, DM the author, open the share (see [Slack](slack.md#keys)) |
| `<leader>o` | open on GitHub |
| `<leader>,` | options |
| `<leader>U`, `<leader>N` | update prowl, what's new (see [Updating](running.md#updating)) |

`<leader>A` lists everyone who can review the PR as you type, with GitHub's suggestions near the top. Someone who reviewed already starts checked, for another look.

## Overview

The overview shows where each reviewer stands, then an Activity timeline: reviews, comments, thread replies, pushes, force-pushes and review requests, newest first, with a time column that is brighter the newer it is. A "you last looked" line separates unread activity above from what you have seen below, and opening a PR puts the cursor on the newest unread item; `]n`/`[n` jump between new items.

New items are expanded and rendered as markdown. Anything you have already seen (older than your last visit or your last review) starts folded to one line, as do accounts listed in `bots`. Folds work like vim: `za` toggles, `zo`/`zc` open and close, `zR`/`zM` open or close everything.

## Checks

Below the reviewers, Checks lists the CI on the PR's head commit, with only the latest run of each check. A failed check stays open with what it reported: its title, its summary and its annotations (`a.go:42  want 2, got 3`). Checks that branch protection requires are marked `required` and come first, since those block the merge. Running checks show how long they have been going, and passed and skipped ones fold into one line.

On a failed GitHub Actions check, `gl` shows the lines of its log leading up to the error and the step that failed. `o` on a check opens it on GitHub.

## Threads

Review threads appear in the overview with the code they refer to, and in the diff directly under the line they were left on. `]t`/`[t` jump between them. With the cursor on a thread, `<leader>t` opens the thread menu: `r` resolve, `u` unresolve, `c` reply.

## The diff

The diff tab shows the changes since your last review when you have one, and `D` switches to the full diff. `s` switches between unified and side by side.

| Key | Does |
| --- | --- |
| `]c`/`[c` | next or previous hunk |
| `]f`/`[f` | next or previous file |
| `za` | fold a file |
| `V` | mark a file as viewed, fold it and move to the next |
| `c` | comment on the line, or on the lines selected with `v` |
| `S` | suggest a change to the line or selection |
| `dd` | drop a pending comment |

`V` is GitHub's own "Viewed" checkbox, so it syncs with the web UI, and a file that changes after you viewed it is flagged again.

The diff is syntax highlighted with your theme's colors. Added and removed lines get a tinted background, and when a removed line pairs with an added one the words that changed are tinted more strongly. Each hunk is highlighted as a whole, so block comments and multi-line strings come out right.

## Inline comments and suggestions

`c` on a line, or on lines selected with `v`, opens a comment. `S` does the same as a suggested change: the comment starts with a `suggestion` block holding those lines, so the author can apply your edit with a click. In the comment box, `ctrl+s` saves it, `ctrl+e` opens it in your editor, `esc` keeps what you wrote, and `ctrl+c` throws it away.

A comment is kept as pending and goes out with your next review; `◆3` in the statusline counts them. Each comment stays on the commit you wrote it on: if the author pushes in between, the earlier comments go out as their own review on that commit, so they're on the right lines.

## Posting a review

Approving (`<leader>a`) asks for a `y` and posts with no comment. Request changes (`<leader>x`) and comment (`<leader>m`) open in your editor; `:wq` submits and `:cq` cancels. The review's `event:` line takes `approve`, `request_changes`, `comment`, `close` or `draft`, where the last two post your text and then close the PR or make it a draft.

After a review is posted, a Slack status line for it is copied to your clipboard; `ys` copies one at any time and `yu` copies the PR's link. Signed in to [Slack](slack.md), a PR someone shared there gets your emoji on their message instead, and `<leader>y` posts the line in its thread.
