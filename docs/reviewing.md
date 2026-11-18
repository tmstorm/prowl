# Reviewing a PR

A PR in prowl has three views: the overview, the diff, and its AI reviews (see [AI reviews](ai-reviews.md)). The keys that move through them are in [The PR pane](tui.md#the-pr-pane).

## Overview

The overview shows the PR's description, where each reviewer stands, its checks, its threads, and an Activity timeline: reviews, comments, thread replies, pushes, force-pushes and review requests, newest first. A "you last looked" line separates activity you haven't seen from what you have, and opening a PR starts at the newest item you haven't seen.

New items are shown in full, rendered as Markdown. Anything you have already seen (older than your last visit or your last review) is folded to one line, as is activity from accounts listed in `bots`.

## Checks

Checks lists the CI on the PR's head commit, with only the latest run of each check. A failed check shows what it reported: its title, its summary and its annotations (`a.go:42  want 2, got 3`). Checks that branch protection requires are marked `required` and come first, since those block the merge. Running checks show how long they have been going, and passed and skipped ones fold into one line. For a failed GitHub Actions check, prowl can fetch the lines of its log leading up to the error and the step that failed.

## Threads

Review threads appear in the overview with the code they refer to, and in the diff under the line they were left on. You can resolve a thread, unresolve it, or reply in it.

## The diff

The diff shows the changes since your last review when you have one, or the full diff. It's unified, or side by side.

Marking a file viewed is GitHub's own "Viewed" checkbox, so it syncs with the web UI, and a file that changes after you viewed it is flagged again.

The diff is syntax highlighted with your theme's colors. Added and removed lines get a tinted background, and when a removed line pairs with an added one the words that changed are tinted more strongly. Each hunk is highlighted as a whole, so block comments and multi-line strings come out right.

## Inline comments and suggestions

A comment goes on a line of the diff or on a range of lines. A suggested change is a comment that starts with a `suggestion` block holding those lines, so the author can apply your edit with a click.

A comment is kept as pending and goes out with your next review. Each comment stays on the commit you wrote it on: if the author pushes in between, the earlier comments go out as their own review on that commit, so they're on the right lines.

## Posting a review

Approving posts with no comment, after you confirm. Requesting changes and commenting open the review in your editor: saving submits it, and quitting without saving (`:cq` in vim) or leaving it empty cancels it. The review's `event:` line takes `approve`, `request_changes`, `comment`, `close` or `draft`, where the last two post your text and then close the PR or make it a draft. Pending inline comments go out with the review.

A plain comment in the conversation isn't a review: pending inline comments stay pending.

You can also:

- ask people or teams for a review. The list shows everyone who can review the PR, with GitHub's suggestions near the top, and someone who reviewed already starts checked, for another look.
- request yourself as a reviewer
- close the PR with a comment
- make it a draft, or ready for review

After a review is posted, a status line for it (`https://github.com/acme/api/pull/12 Approved · @bob · fix: retry on timeout`) is copied to your clipboard, to paste wherever your team asks for reviews. Signed in to [Slack](slack.md), a PR someone shared there gets your emoji on their message instead.
