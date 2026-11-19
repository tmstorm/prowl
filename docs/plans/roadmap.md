# Roadmap

The PRs after built-in docs (`builtin-docs.md`), in order. Each is one PR, split into commits.

1. Findings for the author
2. Re-reviews that check the fixes
3. Before 1.0: the VS Code extension, a stable core, then packages

## 1. Findings for the author

When someone reviews your PR on GitHub, `prowl findings` in your checkout shows that review, so you or your agent can work through it like a local AI review. Today it only reads the local store and says "no finished review" on your own PR even after changes were requested.

### Decisions

- No new command or skill. `prowl findings` gets a GitHub source, and the `prowl-findings` skill learns the author side.
- Read-only. The agent drafts thread replies and the author posts them.
- prowl tags each finding it posts with a hidden HTML comment, so the author's prowl can tell AI findings from hand-written comments and sort by severity.

### The marker

`findingBody` (`internal/core/post.go`) appends `<!-- prowl:finding v=1 severity=must-fix rule="AGENTS.md: wrap errors" -->`, and each PR-level finding in the review body gets one at the end of its bullet. Your own pending comments get none. Reviews posted before the marker start with `**must-fix:** Title`, and the parser falls back to that.

### `prowl findings` on your own PR

When the current branch's PR (`resolvePR`) is yours, prowl fetches it with `FetchDetail` and prints, after any local AI review:

- Each reviewer's latest verdict, changes requested first.
- Their latest review bodies, PR-level findings split out.
- Unresolved threads: path, line, hunk, who opened it, replies, whether it's outdated, thread ID.
- Sorted: tagged must-fix, hand-written comments in a changes-requested review, tagged should-fix, other hand-written comments, nits. `suggestion` blocks as the patch they'd apply.

Bots and resolved threads are left out; `--all` brings resolved threads back. `--json` prints `verdicts` and `items` (`source`, `kind`, `thread_id`, `path`, `line`, `severity`, `author`, `outdated`, `body`, `suggestion`). A PR someone else wrote keeps today's behaviour.

### The skill

`internal/skill/SKILL.md` adds the author-side triggers ("changes requested on my PR", "address the review comments"). A hand-written comment is a person asking, so the agent fixes it or drafts a reply saying why not, never drops it. Each thread gets a drafted reply listed with its thread ID. The no-posting rule stays.

### Code

- `internal/core/post.go`: the marker.
- `internal/core`: parsing it, the GitHub items beside `FindingsMarkdown`, the sort.
- `main.go`: `findings` fetches the detail on your own PR, `--json`.
- `internal/skill/SKILL.md`, `skill_test.go` if it pins the text. Docs: `docs/ai-reviews.md`, the `prowl findings` usage text.
- Tests: marker round trip, the `**must-fix:**` fallback, sort order, `--json` fields, someone else's PR unchanged.

Open: sorting and colouring tagged threads by severity in the TUI's PR view. Cheap once parsing exists, but UI scope beyond `findings`.

## 2. Re-reviews that check the fixes

On a re-review the agent is told to check what the reader asked for and review what changed (`reReviewSection` in `internal/agent/history.go`), but it still does a full review turn and a verify turn. When a commit claims to fix a finding, the work is checking that claim, as extensions already do.

- The agent gets the previous findings, posted and not, with the commits since, and returns a verdict on each: fixed, not fixed or partly fixed, with the evidence.
- Then it reviews only the diff since the last review, at the same must-fix bar for code already reviewed as today.
- The verdicts show in the PR pane beside each finding, and a fixed one drops out of the next post.

Open: the schema change, and whether the verify turn is still needed when the review is only verdicts and a small diff.

## 3. Before 1.0

Last, once the features above have settled:

- The VS Code extension, released for people to try. What it needs from `internal/core` goes into the core before the API is settled.
- A stable core: settle `internal/core`'s API, and the extension protocol with it, so other interfaces and extensions can rely on it.
- Packages: AUR (`prowl-bin`), then Homebrew and Scoop.

## Being considered

Not scheduled. Each needs a reason from using prowl before it gets a place above.

- Ask prowl: a chat about prowl itself, opened from anywhere, with only the `docs` tool and a read-only view of the inbox (each PR's bucket and why, its CI and review state). It would answer "why is api#12 under re-check?" or "what's blocking the oldest PR waiting on me?", which the review chat can't: it only exists after a review, and can't see the inbox. It needs an agent session without a checkout, an inbox tool for the tool server, and a place in the UI.
- Old file contents for Codex's re-reviews: review checkouts are partial clones, and Codex's sandbox has no network, so `git range-diff` against the last reviewed commit fails on missing blobs. prowl could fetch what a re-review needs before the agent starts, or put the range-diff in Codex's prompt as it does the PR's history.
- CI logs for the review agents: their allowed `gh` commands include `gh pr checks` but not `gh run view --log-failed`, so a failing check shows only its annotations. prowl could put the failed job's log tail in the prompt, as `gl` shows it in the TUI.
