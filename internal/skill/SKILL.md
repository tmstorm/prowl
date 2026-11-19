---
name: prowl-findings
description: Get a prowl code review of the current branch and act on it. Use when the user asks to review the work before a pull request ("review my changes", "run prowl review"), or to check, fix, address, or go through review findings, "what the review said", or "the prowl findings" for a branch or PR they are working on, and when reviewers left comments on the user's own PR ("changes requested on my PR", "address the review comments"). Runs `prowl review` or `prowl findings`, verifies each finding against the code, fixes the ones that hold up, reports the ones it disagrees with, and drafts replies to review threads. Never commits, pushes, or posts to GitHub.
---

# Act on prowl review findings

prowl stores an agent review for each pull request, and for branches reviewed locally before they have one. This skill turns that review into verified fixes in the user's checkout.

## Review work that has no pull request yet

When the user asks for a review of their changes before opening a PR, run this in the repository:

```sh
prowl review
```

It reviews everything on the branch since it left the default branch, committed or not, and prints the findings when it is done (a few minutes). It exits with status 2 when a finding is must-fix. Then work through the findings as below.

## Get the findings

Run this in the repository:

```sh
prowl findings
```

It finds the pull request for the current branch, or the branch's local review when it has no PR. If the user named a PR, pass it: `prowl findings 123` or `prowl findings owner/repo#123`. Add `--all` only if the user asks for findings the reviewer excluded.

If the command says there is no review yet, run `prowl review` when the branch has no PR; otherwise tell the user to open the PR in prowl and start one with `<leader>cc`, and stop. If `prowl` is not installed, say so and stop.

If the output says the branch has moved since the review, expect some findings to be fixed already.

A "Note from" section after the findings is the reviewer talking to the user: what it held back as not blocking, what it couldn't check, or a question. It is not a finding, so don't change code for it.

## On the user's own PR

When the PR is the user's own, `prowl findings` also prints "On GitHub": each reviewer's latest verdict, then what was raised, in the order to work through it, whether the reviewers use prowl or not. Each item says where it is, who raised it, and its thread ID. An item marked "a finding prowl posted" came from a reviewer's AI review and has a severity. What a person wrote is never dropped: fix it, or draft a reply that says why not. What a bot wrote is checked like a finding: fix it when it holds up, and draft a reply when it's wrong. A failing check is fixed before anything else that isn't must-fix. A thread marked outdated is on code that has changed since, so check whether it still applies. A suggested change is the patch the reviewer would apply; apply it when it holds up. `prowl findings --json` prints the same as JSON.

For each review thread, draft the reply the user would post, in their voice, and list it with its thread ID: what was fixed and where ("Fixed in the next push: the map is now made in New."), or why it wasn't. The user posts the replies.

## Work through them

For each finding, in order:

1. Read the code at the cited location and around it. Confirm the problem is real on this branch as it is now. Do not trust the finding's wording on its own.
2. If it holds up, make the smallest change that fixes it, following the repo's own guidelines (AGENTS.md, CLAUDE.md, CONTRIBUTING.md). Add or update a test when the finding is about behavior.
3. If it does not hold up, or is already fixed, change nothing and note why.
4. Findings about "the PR description" cannot be fixed in code. Draft the corrected text for the user instead of editing the PR.

Run the repo's tests or checks for what you changed if they are quick.

## Rules

- Do not commit, push, or post anything to GitHub (no `gh pr review`, `gh pr comment`, or `gh pr edit`). The user reviews your changes first.
- Do not widen the scope: fix what the findings describe, nothing else.

## Report

Finish with one line per finding: its number and title, then fixed, already fixed, or not changed with the reason. Then list any drafted PR description text and thread replies, each reply with its thread ID, and pass on anything in a reviewer's note the user should act on.
