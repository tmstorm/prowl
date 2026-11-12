# The inbox

Each row shows how long the PR has waited on you, the repo, the PR number and the title. On the right are its size (`+120 -30`, faded when small and orange when large), review state (green approved, red changes requested, yellow needs rebase) and CI. On PRs where the author is answering review feedback, `2/3` shows how many review threads are resolved.

The line above the list is for triage at a glance: how many PRs need you, how long the oldest has waited, and any of your own PRs with failing CI or merge conflicts.

When nothing needs you, prowl's owl (it's in the name: p-r-owl) sleeps on the empty inbox, and it wakes up while the first sync runs. The owl at the left of the statusline is a status you can read from anywhere: `(-,-)` nothing needs you, `(o,o)` something does, `(O,O)` something is overdue. Click it to see what needs you. The terminal tab's title says the same, e.g. `prowl · 4 need you`, so you can see it from another tab. `playful = false` swaps the owls for plain text.

Sections fold like vim folds: `za` on any PR folds its section down to the header (`▸ NEEDS REVIEW 10`), `zo`/`zc` open and close, `zM`/`zR` fold or open them all, and `enter` or a click on a folded header opens it. The Approved tab lists PRs you approved, and your own approved PRs, until they merge, so you can see what is ready to go in. If a change needs your approval again, the PR moves back to Needs me under re-approve and you get a notification. A legend at the bottom of the inbox explains the icons on screen (`show_legend` turns it off).

Titles that follow [Conventional Commits](https://www.conventionalcommits.org) are colored by type (`fix` red, `feat` green, `perf` orange, `refactor` pink, `test` yellow, `docs` purple, `ci` and `build` cyan, `chore` and `style` dim), with the scope dimmed and a breaking `!` in bold red. Inside each section PRs are sorted by type, fixes first and chores last, then by how long they have waited. This works without any configuration. To change it, set `type_order`; a type you add there (say `wip`) can get a color under `[type_colors]`, which also overrides the built-in colors. `sort_by_type = false` sorts by wait time only. `<leader>ft` focuses one type, the same way `<leader>fr` focuses one repo, and the two combine. `esc` clears both.

**Watching a repo.** prowl normally shows only PRs that involve you. Add a repo to `watch_repos` (or press `<leader>w` on one of its PRs) to see every open PR in it, for instance to drain a repo before a release. PRs you aren't part of go in the All tab, not Needs me: **unclaimed** when nobody was asked to review, **watching** when someone else has it or it's a draft. `<leader>fr` lists every repo in your orgs, not only those with PRs in your inbox. Focusing a repo you don't watch fetches all its open PRs for the rest of the session (`esc` stops), so `<leader>fr` → any repo works on release day without setting anything up. With a repo in focus, the summary line says where all its PRs stand: `api · 9 open · 2 need you · 1 unclaimed · 3 waiting on author · 2 approved, not merged · 1 draft`.

**A PR shared in Slack.** When someone posts a link instead of requesting you, paste it into `ctrl+p` (a link, `owner/repo#N` or `repo#N` all work). prowl fetches the PR and opens it, and it stays in your inbox for the session. `<leader>R` requests you as a reviewer on GitHub, so the author sees it and it stays for good.

`<leader>g` switches between grouping by status (below) and grouping by repo, where each repo header says how many of its PRs need you. `<leader>fr` picks one repo to focus on, for when you are trying to get one repo merged up; `esc` in the inbox shows all repos again.

Each PR gets one bucket based on your own history with it:

| Bucket | Meaning |
| --- | --- |
| needs review | you were asked and haven't reviewed |
| re-review | you reviewed, then were asked again |
| addressed | you requested changes, and the author has since pushed or replied in your threads |
| new activity | replies since your last comment, or new commits after you approved |
| team request | a team you're on was asked, not you |
| mine | PRs you opened |
| waiting on author | you requested changes and nothing has happened since |
| re-approve | your approval was dismissed (for example by a push under stale-approval rules), or you were asked to review again after approving |
| approved | you approved; if the PR changed since but your approval still counts, it stays here marked ◐ |
| re-check | pushed to since you approved, on a repo where GitHub keeps your approval through pushes (`recheck_after_push = false` leaves these under approved) |
| unclaimed | in a watched repo, nobody asked to review (shows in All only) |
| watching | in a watched repo, someone else has it, or a draft (All only) |
| involved | you commented or were mentioned, and nothing is waiting on you |
| local | your branch, reviewed with `prowl review` before it has a pull request (Mine) |

Inside a bucket, the PR that has waited longest comes first. The age turns yellow once a PR has waited on you longer than `stale_after` (a day by default) and bold red at twice that.
