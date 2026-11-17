# The inbox

## Rows

Each row shows how long the PR has waited on you, the repo, the PR number and the title. On the right are its size (`+120 -30`, faded when small and orange when large), review state (green approved, red changes requested, yellow needs rebase) and CI. On PRs where the author is answering review feedback, `2/3` shows how many review threads are resolved.

The age turns yellow once a PR has waited on you longer than `stale_after` (a day by default) and bold red at twice that. A legend at the bottom of the inbox explains the icons on screen (`show_legend` turns it off).

## The summary line and the owl

The line above the list is for triage at a glance: how many PRs need you, how long the oldest has waited, when snoozed ones come back, and any of your own PRs with failing CI or merge conflicts.

The owl at the left of the statusline is a status you can read from anywhere: `(-,-)` nothing needs you, `(o,o)` something does, `(O,O)` something is overdue. Click it to see what needs you. The terminal tab's title says the same, e.g. `prowl · 4 need you`, so you can see it from another tab. When nothing needs you, prowl's owl (it's in the name: p-r-owl) sleeps on the empty inbox, and it wakes up while the first sync runs. `playful = false` drops the statusline owl and turns the sleeping one into text.

## Tabs

| Tab | Holds |
| --- | --- |
| Needs me | needs review, shared, re-approve, re-review, addressed, new activity, re-check |
| All | every PR, including unclaimed and watching |
| Mine | PRs you opened, and local reviews of your branches |
| Approved | PRs you approved, and your own approved PRs, until they merge |
| Waiting | waiting on author, team request, involved |

`2`–`6` or `tab` pick a tab. Inside a tab, PRs are grouped into sections by bucket. Sections fold like vim folds: `za` on any PR folds its section down to the header (`▸ NEEDS REVIEW 10`), `zo`/`zc` open and close, `zM`/`zR` fold or open them all, and `enter` or a click on a folded header opens it.

## Buckets

Each PR gets one bucket based on your own history with it. They're listed in the order they sort, most urgent first.

| Bucket | Meaning |
| --- | --- |
| needs review | you were asked and haven't reviewed |
| shared | linked in a Slack channel you watch, or sent to you in a DM, and you haven't reviewed it ([Slack](slack.md)) |
| re-approve | your approval was dismissed (for example by a push under stale-approval rules), or you were asked to review again after approving |
| re-review | you reviewed, then were asked again, or your change request was dismissed |
| addressed | you requested changes or left review comments, and since then the author pushed or someone replied in your threads |
| new activity | replies in your threads, or comments from others since your last review |
| re-check | pushed to since you approved, on a repo where GitHub keeps your approval through pushes (`recheck_after_push = false` leaves these under approved) |
| team request | a team you're on was asked, not you |
| mine | PRs you opened |
| local | your branch, reviewed with `prowl review` before it has a pull request |
| approved | you approved; if the PR changed since but your approval still counts, it stays here marked ◐ |
| waiting on author | you requested changes or left review comments, and nothing has happened since |
| involved | you commented or were mentioned, and nothing is waiting on you |
| unclaimed | in a watched repo, nobody was asked or has reviewed, and you haven't taken part |
| watching | in a watched repo, someone else has it, or it's a draft |

If a change needs your approval again, the PR moves back to Needs me under re-approve and you get a notification.

## Sorting by type

Titles that follow [Conventional Commits](https://www.conventionalcommits.org) are colored by type (`fix` red, `feat` green, `perf` orange, `refactor` pink, `test` yellow, `docs` purple, `ci` and `build` cyan, `chore` and `style` dim), with the scope dimmed and a breaking `!` in bold red.

Inside each section PRs are sorted by type, fixes first and chores last, then by how long they have waited. This works without any configuration. To change it, set `type_order`; a type you add there (say `wip`) can get a color under `[type_colors]`, which also overrides the built-in colors. `sort_by_type = false` sorts by wait time only, longest first.

## Grouping and focusing

`<leader>g` switches between grouping by status and grouping by repo. Grouped by repo, each repo header says how many of its PRs need you, and each row shows its bucket where the repo was.

`<leader>fr` focuses one repo, for when you are trying to get one repo merged, and `<leader>ft` focuses one commit type. The two combine, and `esc` in the inbox clears both. With a repo in focus, the summary line says where all its PRs stand: `api · 9 open · 2 need you · 1 unclaimed · 3 waiting on author · 2 approved, not merged · 1 draft`.

`<leader>fr` lists every repo in your orgs, not only those with PRs in your inbox. Focusing a repo you don't watch fetches all its open PRs for the rest of the session (`esc` stops), so `<leader>fr` → any repo works on release day without setting anything up.

## Watching a repo

prowl normally shows only PRs that involve you. Add a repo to `watch_repos` (or press `<leader>w` on one of its PRs) to see every open PR in it, for instance to drain a repo before a release. PRs you aren't part of go in the All tab, not Needs me: unclaimed when nobody was asked to review, watching when someone else has it or it's a draft.

## PRs from Slack or a link

Signed in to Slack, prowl finds the PR links people post in your channels and DMs, and those PRs wait under shared until you review them; see [Slack](slack.md).

Without Slack, paste a link into `ctrl+p` (a link, `owner/repo#N` or `repo#N` all work). prowl fetches the PR and opens it, and it stays in your inbox for the session. `<leader>R` requests you as a reviewer on GitHub, so the author sees it and it stays for good.

## Snoozing

`<leader>s` snoozes a PR for 1 hour, 4 hours, 1 day or 1 week (`1`–`4`, or move and `enter`). It leaves the inbox until then, and comes back early if the author pushes. The summary line says how many snoozed PRs need you and when they come back, and the bottom of the list counts them.

`<leader>z` shows snoozed PRs in the list, marked `z`, and hides them again. `<leader>S` on one brings it back now.
