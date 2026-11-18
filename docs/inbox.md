# The inbox

The inbox is every open PR that involves you, sorted by what each needs from you. With no configuration, that's across all of GitHub; `orgs` limits it to your work. prowl checks GitHub every `poll_interval` and sends a desktop notification when a PR moves into a state that needs you.

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

If a change needs your approval again, the PR moves back to re-approve and you get a notification. Each PR also says why it's in its bucket, such as `review requested`.

## Tabs

The buckets are grouped into tabs:

| Tab | Holds |
| --- | --- |
| Needs me | needs review, shared, re-approve, re-review, addressed, new activity, re-check |
| All | every PR, including unclaimed and watching |
| Mine | PRs you opened, and local reviews of your branches |
| Approved | PRs you approved, and your own approved PRs, until they merge |
| Waiting | waiting on author, team request, involved |

## Overdue

prowl counts how long each PR has waited on you. Once that's longer than `stale_after` (a day by default) the PR is due, and at twice that it's overdue. How many PRs need you, and how long the oldest has waited, is what the TUI's summary line and its owl report.

## Sorting by type

Titles that follow [Conventional Commits](https://www.conventionalcommits.org) are colored by type (`fix` red, `feat` green, `perf` orange, `refactor` pink, `test` yellow, `docs` purple, `ci` and `build` cyan, `chore` and `style` dim), with the scope dimmed and a breaking `!` in bold red.

Inside each bucket PRs are sorted by type, fixes first and chores last, then by how long they have waited. This works without any configuration. To change it, set `type_order`; a type you add there (say `wip`) can get a color under `[type_colors]`, which also overrides the built-in colors. `sort_by_type = false` sorts by wait time only, longest first.

## Grouping and focusing

The inbox groups PRs by status (their bucket) or by repo (`group_by`). Grouped by repo, each repo says how many of its PRs need you, and each PR shows its bucket instead of its repo.

Focusing narrows the inbox to one repo, for when you're trying to get one repo merged, or to one commit type, and the two combine. With a repo in focus, the summary says where all its PRs stand: `api · 9 open · 2 need you · 1 unclaimed · 3 waiting on author · 2 approved, not merged · 1 draft`.

You can focus any repo in your orgs, not only those with PRs in your inbox. Focusing a repo you don't watch fetches all its open PRs until you clear the focus, so release day works without setting anything up.

## Watching a repo

prowl normally shows only PRs that involve you. Add a repo to `watch_repos` to see every open PR in it, for instance to drain a repo before a release. PRs you aren't part of go in the All tab, not Needs me: unclaimed when nobody was asked to review, watching when someone else has it or it's a draft.

## PRs from Slack or a link

Signed in to Slack, prowl finds the PR links people post in your channels and DMs, and those PRs wait under shared until you review them; see [Slack](slack.md).

Without Slack, open a PR by its link (a link, `owner/repo#N` or `repo#N` all work). prowl fetches it, and it stays in your inbox for the session. Requesting yourself as a reviewer puts you on the PR on GitHub, so the author sees it and it stays for good.

## Snoozing

Snoozing a PR takes it out of the inbox for 1 hour, 4 hours, 1 day or 1 week. It comes back then, or early if the author pushes. The summary says how many snoozed PRs need you and when they come back, and `prowl list` leaves them out. Snoozed PRs can be shown among the rest, and unsnoozed to bring them back now.

The keys for all of this are in [The inbox screen](tui.md#the-inbox-screen).
