# The TUI

`prowl` with no command opens the TUI: the inbox and the PR you're reading, side by side. It uses vim's keys, and the arrow keys and the mouse work too. This page has the keys; what they act on is explained on the other pages.

## Panes

The inbox is on the left and the PR pane on the right. Under 100 columns prowl shows one at a time. Numbers work like lazygit's: `0` focuses the inbox and `1` the PR pane, and `2`–`9` pick a tab inside whichever pane has focus (the inbox tabs on the left; Overview, Diff and the AI review on the right). The pane titles show every number in brackets and counts in parentheses, e.g. `[0] [2] Needs me (18)` and `[1] repo#305 [2] Overview [3] Diff [4] AI review (3)`. `+` gives the PR pane the whole width, and `+`, `0` or `h` brings the inbox back.

## Moving around

Not a vim user? The arrow keys work: `↑`/`↓` move, `→` opens the PR pane and `←` goes back. `?` lists the keys, the line under the inbox legend reminds you of the basics, and the `<leader> menu` button at the bottom right opens every action with a click.

| Key | Does |
| --- | --- |
| `j`/`k`, `↑`/`↓` | move |
| `l`, `enter`, `→` | open the PR pane |
| `h`, `esc`, `←` | back to the inbox (`q` too; in the inbox `q` quits) |
| `tab`, `shift+tab` | next or previous tab |
| `gg`/`G`, `home`/`end` | top and bottom |
| `ctrl+d`/`ctrl+u` | half a page down or up |
| `ctrl+f`/`ctrl+b`, `pgdown`/`pgup` | a page down or up |
| `ctrl+e`/`ctrl+y` | scroll a line without moving the cursor |
| `H`/`M`/`L` | top, middle or bottom of the screen |
| `zz`/`zt`/`zb` | put the cursor line in the middle, top or bottom |
| `za`, `zo`/`zc`, `zR`/`zM` | toggle a fold, open or close it, open or close every fold |
| `/`, `n`/`N` | search, next and previous (`N` searches backwards); `:noh` clears the highlight |
| `:42` | go to line 42 |
| `K` | open these docs at what the cursor is on: a PR's bucket, a finding, a check, a thread, an option |
| `:help`, `:h` | open these docs; `:help stale_after` opens them at a key, option or heading, and `tab` completes it |
| `ctrl+p`, `<leader><leader>`, `<leader>ff` | fuzzy-find a PR, or paste a link to one |
| `o` | open the PR on GitHub |
| `R`, `<leader>r` | refresh now |
| `?`, `<leader>?` | list the keys |
| `:q`, `<leader>q` | quit |

## Mouse

Everything on screen also works with the mouse (`mouse = false` turns it off). Click a PR, tab or section header; click a fold head in the PR pane to open or close it; scroll either pane, and drag over lines to select them (see [Copying text](#copying-text)). In menus and pickers, a click selects a row and a second click chooses it, a click in a checklist checks the row, the wheel moves the selection, and a click outside closes the menu. The keys that move the panes move a menu too, with `g` for `gg`. Clicking a finding again includes or excludes it, and clicking a diff line again starts a comment on it. The leader menu's entries, confirm prompts' `[ yes ]`/`[ no ]`, the statusline's sync text (refresh) and login warning (accounts) are clickable too. Clicks and the wheel inside the chat window go to the agent. Nothing posts to GitHub on a single click: approving still asks `[y/N]`.

## The leader key

Anything that posts to GitHub sits behind the leader key, written `<leader>` in these docs: `<leader>a` means press the leader, then `a`. The leader is `space` unless you change it with `leader` in the config or in the options (`<leader>,`) to `,`, `\` or `;`; prowl's own hints always show the key you chose. Press it and a menu shows what comes next.

| Key | Does |
| --- | --- |
| `<leader>a` | approve, after a `y` |
| `<leader>x` | request changes, in your editor |
| `<leader>m` | comment review, in your editor |
| `<leader>n` | a plain comment in the conversation, not a review |
| `<leader>tr`, `<leader>tu`, `<leader>tc` | the thread under the cursor: resolve, unresolve, reply |
| `<leader>A` | ask people or teams for a review |
| `<leader>R` | request yourself as a reviewer |
| `<leader>X` | close with a comment |
| `<leader>D` | make it a draft, or ready for review |
| `<leader>c` | AI reviews (see [Agent keys](#agent-keys)) |
| `<leader>p` | post the AI review |
| `<leader>l` | accounts and logins (see [The accounts list](#the-accounts-list)) |
| `<leader>fr`, `<leader>ft` | focus one repo or one commit type; `<leader>ff` finds a PR |
| `<leader>g` | group by status or by repo |
| `<leader>s`, `<leader>S`, `<leader>z` | snooze, unsnooze, show or hide snoozed PRs |
| `<leader>w` | watch or unwatch the PR's repo |
| `<leader>y`, `<leader>Y`, `<leader>O` | Slack: post the review line, DM the author, open the share (see [Slack keys](#slack-keys)) |
| `<leader>o` | open on GitHub |
| `<leader>,` | options |
| `<leader>U`, `<leader>N` | update prowl, what's new (see [New versions](#new-versions)) |

## The inbox screen

Each row shows how long the PR has waited on you, the repo, the PR number and the title. On the right are its size (`+120 -30`, faded when small and orange when large), review state (green approved, red changes requested, yellow needs rebase) and CI. On PRs where the author is answering review feedback, `2/3` shows how many review threads are resolved. The age turns yellow once the PR is due (`stale_after`) and bold red when it's overdue. A legend at the bottom explains the icons on screen (`show_legend` turns it off).

The line above the list is for triage at a glance: how many PRs need you, how long the oldest has waited, when snoozed ones come back, and any of your own PRs with failing CI or merge conflicts.

The owl at the left of the statusline is a status you can read from anywhere: `(-,-)` nothing needs you, `(o,o)` something does, `(O,O)` something is due. Click it to see what needs you. The terminal tab's title says the same, e.g. `prowl · 4 need you`, so you can see it from another tab. When nothing needs you, prowl's owl (it's in the name: p-r-owl) sleeps on the empty inbox, and it wakes up while the first sync runs. `playful = false` drops the statusline owl and turns the sleeping one into text.

`2`–`6` or `tab` pick a tab. Inside a tab, PRs are grouped into sections by bucket, which fold: `za` on any PR folds its section down to the header (`▸ NEEDS REVIEW 10`), `zM`/`zR` fold or open them all, and `enter` or a click on a folded header opens it.

`<leader>fr` lists every repo in your orgs to focus one, and `<leader>ft` focuses a commit type; `esc` in the inbox clears both. `ctrl+p` takes a link to a PR that isn't in your inbox. `<leader>s` snoozes the PR for 1 hour, 4 hours, 1 day or 1 week (`1`–`4`, or move and `enter`), `<leader>z` shows snoozed PRs marked `z`, or hides them again, and `<leader>S` on one brings it back now. See [The inbox](inbox.md).

## The PR pane

The overview puts the cursor on the newest item you haven't seen, and `]n`/`[n` jump between new items. `]t`/`[t` jump between threads, here and in the diff, and `<leader>t` on one opens the thread menu: `r` resolve, `u` unresolve, `c` reply. On a failed GitHub Actions check, `gl` shows the end of its log, and `o` on a check opens it on GitHub. See [Reviewing a PR](reviewing.md).

## Diff keys

| Key | Does |
| --- | --- |
| `D` | switch between the changes since your review and the full diff |
| `s` | switch between unified and side by side |
| `]c`/`[c` | next or previous hunk |
| `]f`/`[f` | next or previous file |
| `za` | fold a file |
| `V` | mark a file as viewed, fold it and move to the next |
| `c` | comment on the line, or on the lines selected with `v` |
| `S` | suggest a change to the line or selection |
| `dd` | drop a pending comment |

In the comment box, `ctrl+s` saves the comment, `ctrl+e` opens it in your editor, `esc` keeps what you wrote, and `ctrl+c` throws it away. `◆3` in the statusline counts your pending comments.

## Reviewing keys

`<leader>a` approves after a `y`. `<leader>x` (request changes) and `<leader>m` (comment) open the review in your editor; `:wq` submits and `:cq` cancels. `<leader>A` lists everyone who can review the PR as you type. After a review is posted, its Slack line is on your clipboard; `ys` copies one at any time and `yu` copies the PR's link. See [Reviewing a PR](reviewing.md#posting-a-review).

## Agent keys

| Key | Does |
| --- | --- |
| `<leader>cc` | review with every reviewer |
| `<leader>cC` | review with one agent you pick |
| `<leader>cp` | run a review profile |
| `<leader>cr` | ask the reviewer to revise |
| `<leader>ci` | chat with the reviewer |
| `<leader>cs` | update the findings from a chat |
| `<leader>ca` | write an AI summary |
| `<leader>ch` | copy a fix-these-findings prompt |
| `<leader>cd` | discard the review |
| `<leader>p` | post the AI review, in your editor |
| `<leader>e` | the PR's on-demand extensions |

Revise, chat, update and discard act on the agent whose section the cursor is in, or the main agent. See [AI reviews](ai-reviews.md).

`<leader>e` lists the extensions you can run on the PR, with how each last went. An extension's `key` runs it (`<leader>ef`), as does `enter` on its line, and `v` shows its last result. One with `confirm = true` asks first. See [Extensions](ai-reviews.md#extensions).

## The review tab

| Key | Does |
| --- | --- |
| `x`, `enter` | include or exclude the finding or draft reply |
| `m` | keep only must-fix findings |
| `u` | include every finding again |
| `]f`/`[f` | next or previous finding |
| `c` | edit a draft reply and send it now |

`<leader>tc` on a thread also opens its draft reply, and `ctrl+s` sends it.

## The chat window

`<leader>ci` opens the reviewer's session in a window over prowl. `ctrl+\` hides the window and leaves the session running; `<leader>ci` brings it back. `chat_mode = "fullscreen"` has prowl step aside instead of drawing a window.

## Slack keys

`<leader>y` posts the PR's Slack line in the thread that shared it, `<leader>Y` sends it to the author in a DM, and `<leader>O` opens the message that shared the PR. In the PR pane, the reason ("shared in #reviews by bob") links to the message too: click it, or ctrl-click it in terminals that open links. `ys` copies the line and `yu` copies the PR's link. See [Slack](slack.md).

## The accounts list

`<leader>l` shows every login, with the account and, for Claude Code, when the login runs out. `enter` on one runs that tool's own login (`gh auth login`, `claude auth login`, `codex login`) and checks again when it finishes; on Slack it runs `prowl slack`, which also signs you in the first time. `r` checks them all again. The statusline says when your review agent is logged out or Slack refuses your sign-in, or `Claude relogin in 2d` when its login runs out within three days. See [Accounts](cli.md#accounts).

## Options

`<leader>,` opens the options, where you can change most settings without the file. `K` on one opens its row in [Config](config.md). Background notifications in the options switches `prowl service` on or off, and prowl suggests it once, the first time you open it.

In Slack names, `space` accepts a match (`a` accepts them all), `e` picks someone, `x` clears one, `enter` saves, and `E` edits them all as text.

## New versions

When prowl has installed a new version, the TUI you have open keeps the one it started with and says `prowl v0.2.0 is installed`; `<leader>U` restarts it on the new one. Without one installed, `<leader>U` checks for a new release and installs it, which is how you update with `auto_update = false`. The first time a new version starts, the legend offers its release notes and counts the features it adds, and `<leader>N` shows the notes at any time. See [Updating](cli.md#updating).

## Copying text

`yy` copies the cursor's line. `v` starts a selection, the motion keys extend it, and `y` copies it; dragging the mouse over lines selects them the same way. A diff line is copied as its code, without the line numbers or the `+`/`-`. Hold Shift while dragging to select with your terminal instead, for part of a line.

## Reading these docs

`:help` (or `?` then `d`) opens these docs, and `:help stale_after` opens them at that topic. `K` opens them at what the cursor is on. In the docs, the keys that move the panes move the cursor, `tab` moves between pages, `/` searches every page, `enter` follows a link and `backspace` comes back. `yy`, `v` and `y` copy lines as they read on screen. `esc` closes the docs, back to where you opened them.
