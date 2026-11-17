# AI reviews

prowl runs reviews with the coding agents you already use, one or several. A review reads the PR and its repo's guidelines in a throwaway checkout, verifies its own findings, and leaves you findings to include or leave out. Nothing reaches GitHub until you post it from your editor.

## Agents

List the agents in `[agent] reviewers` (or Reviewers in `<leader>,`), main agent first:

```toml
[agent]
reviewers = ["claude", "codex"]
```

| Agent | Runs | Notes |
| --- | --- | --- |
| `claude` (default) | `claude -p` with read-only tools, no MCP servers but prowl's own `run` tool, and only your user settings | Needs [Claude Code](https://claude.com/claude-code). A PR's own `.claude/settings.json` doesn't apply. |
| `codex` | `codex exec` in the read-only sandbox, with your MCP servers off | Needs the [Codex CLI](https://github.com/openai/codex), logged in. The sandbox has no network, which is fine: prowl puts the PR's history in the prompt. |
| `command` | any shell command | Reads the prompt on stdin and prints the review as JSON (`prowl schema`). Gets `PROWL_SCHEMA` and `PROWL_MODEL` in its environment. Use it for Gemini, opencode, a local model, or a script. |

## Starting a review

`<leader>cc` starts every reviewer at once, in the background. prowl checks the PR out into a worktree under `~/.local/share/prowl/worktrees`, using your existing clone from `repo_roots` when there is one. The review tab shows what each agent is doing while it runs, and you get a desktop notification when it finishes.

Agent actions live under `<leader>c`:

| Key | Does |
| --- | --- |
| `cc` | review with every reviewer |
| `cC` | review with one agent you pick |
| `cp` | run a [review profile](#review-profiles) |
| `cr` | ask the reviewer to revise |
| `ci` | chat with the reviewer |
| `cs` | update the findings from a chat |
| `ca` | write an [AI summary](#ai-summary) |
| `ch` | copy a fix-these-findings prompt |
| `cd` | discard the review |

Revise, chat, sync and discard act on the agent whose section the cursor is in (the main agent otherwise), and always continue that agent's own session. Discarding a review brings back that reviewer's previous run, if it has one.

## The review tab

Each reviewer gets a folding section with its findings, notes, the points others raised and draft replies.

| Key | Does |
| --- | --- |
| `x`, `enter` | include or exclude the finding or draft reply |
| `m` | keep only must-fix findings |
| `u` | include every finding again |
| `]f`/`[f` | next or previous finding |
| `c` | edit a draft reply and send it now |

The agent leaves you a note after a review when it has something for you alone: what it couldn't check, doubts, or a question. Notes show under the reviewer's section, with your `<leader>cr` exchanges, and are never posted.

## Guidelines and skills

Reviews follow the repo's own rules. prowl finds `AGENTS.md`, `CLAUDE.md`, `CONTRIBUTING.md`, `REVIEWING.md`, `CODE_REVIEW.md` and `STYLE.md` at the repo root and in every directory the PR touches, plus `CONTRIBUTING.md` under `.github/` or `docs/` and the PR template under `.github/`, and the agent must read them before reviewing. Findings that enforce a written rule cite it, and the citation goes out with the posted comment.

`[agent] context_dirs` adds directories the agent may read, such as a local clone of your org's standards repo. A GitHub link in the guidelines to `github.com/<the PR's owner>/<a context dir's name>/...` is read from that clone.

List skills every review must use in `[agent] skills`. Claude Code and Codex load their own skills, so prowl names them in the prompt. When a skill is installed only for another agent (a Claude skill while Codex reviews), or the agent is a `command`, prowl puts the skill's text into the prompt instead. The review tab shows which guidelines and skills a review followed.

## Several reviewers

With more than one agent in `reviewers`, their reviews share the AI review tab, one section per agent, in the order you listed them. When two agents flag the same spot (same file, within a few lines), each finding says `also found by …`; agreement is a good sign the problem is real. A profile's run counts as its own reviewer here, so "Claude · adversarial" can agree with plain Claude.

`<leader>p` posts one GitHub review made of every included finding. A problem two reviewers raised is posted once, at the higher of their two severities, and the main agent's summary is the body. The authors get one review, not one per agent.

## Models

Models are per agent, under `[agent.models]`. In `<leader>,`, each agent's model option lists what it offers: Claude Code's aliases (`opus`, `sonnet`, `haiku`, `fable`, which always mean the latest of each), or the models in Codex's own catalog. Any other model ID works when you set it in the file. A missing model uses the agent's default.

## Severities

Every finding is must-fix (blocks the merge), should-fix (a real problem that doesn't block it) or a nit. Must-fix findings are always reported; `should_fix` and `nits` under `[agent]` turn the other two off, and the agent can't return a severity you turned off.

`review_instructions` replaces prowl's instructions for what to look for and how to write it, e.g. to add team conventions. The config file shows the built-in text commented out, and Options opens it in your editor. Severities and the review format stay fixed.

## Verification

Each review runs in two turns: the review, then a verification pass that re-checks every finding against the code and drops what it can't confirm and what was already raised. The review tab shows the counts ("verified 9 → 5"). If the verification turn fails, the review keeps the unverified findings.

Findings about the PR itself, such as its description, go in the review body instead of on a line.

## Re-reviews and points raised

A review reads the PR page first: every review, the conversation and the open threads. When you have reviewed the PR before, it is a re-review. prowl fetches the commit you last reviewed, and the agent starts from what changed since then and from what you said is left. It only re-opens code you already reviewed for must-fix problems, and it doesn't repeat as a finding anything already raised.

Instead it checks what was raised. Every unsettled point on the PR (unresolved threads, review bodies that ask for a change, comments that make a claim about the code, and what failing checks or annotations reported) is checked against the code and listed under Points raised in the reviewer's section: `● open`, `✓ addressed`, `✗ wrong` or `? unclear`, with who raised it and the evidence. That covers your own requests on a re-review, other reviewers' points, and bots, however they report. They are for you only and are never posted. The section head and the notification count the ones still open.

## Draft replies

When you are in a review thread and someone else answered last, for example the author replying to your inline comment, the agent reads the whole thread, resolved or not. Where you have something to say back, a short confirmation or the one question still open, it drafts a reply. Drafts show under the reviewer's section as Draft replies, and the thread is marked `↩ draft reply`.

Draft replies go out with the review: `<leader>p` lists them in the editor, and once the review is posted each is sent in its thread. `x` on one leaves it out (or puts it back), the same way as findings. To send one right away, `c` on it (or `<leader>tc` on the thread) opens it in the reply box to edit, and `ctrl+s` sends it.

Until they are sent, the review's icon in the inbox is yellow, the AI review tab counts them (`(3 · ↩2)`), and the desktop notification for a finished review says so: `docs#97: 3 findings, 1 must-fix · 2 replies to send`. Nothing is sent without you, and `prowl edits` keeps each draft beside what you sent.

## Revising and chatting

`<leader>cr` sends the agent a follow-up instruction, such as "drop the second finding". It answers in a sentence or two, and the exchange shows under Notes.

`<leader>ci` opens the review's own session (`claude --resume` or `codex resume`) in a floating window, with everything the reviewer read and thought still in context, so you can ask it about its findings the way you would in a terminal. `ctrl+\` hides the window and leaves the session running; `<leader>ci` brings it back. When you end the session, prowl offers to update the findings from the conversation (`<leader>cs` does the same later). Set `chat_mode = "fullscreen"` to have prowl step aside instead of drawing a window.

The chat runs with your agent's normal permissions inside the PR's throwaway worktree; for a local review, that is your own checkout. A `command` agent has no session, so it has no chat, and each revise sends the whole context again with the earlier review.

## Posting

`<leader>p` opens the review in your editor with the summary as the body and the included findings listed as inline comments. Nothing is posted until you save it. The review goes out as request changes when an included finding is must-fix, and as a comment otherwise; the editor's `event:` line shows which, and you can change it.

Each finding is posted starting with its severity and title (`**must-fix:** Title`), and a rule it cites follows as `_Per AGENTS.md: wrap errors_`.

## Closing or drafting

Now and then a PR shouldn't be reviewed as it is. When it is superseded, a duplicate, the wrong approach or abandoned, the agent suggests closing it; when it isn't ready for review yet, it suggests making it a draft. The suggestion shows in the reviewer's section head and above its findings, with the reason written as the comment you would post, and in the notification.

It is only a suggestion: in the `<leader>p` editor, change `event:` to `close` or `draft` to post the review and then close the PR or make it a draft. `<leader>X` closes a PR with a comment and `<leader>D` makes it a draft (or ready for review again) without a review; both start from the agent's reason when it suggested one.

## History

Each agent's section ends with a History fold: its earlier runs on this PR, and what happened to each finding (posted, excluded, never posted). The next review is told which findings you excluded before, so it doesn't raise them again. History is deleted when the PR merges or closes.

## AI summary

Descriptions are often thin. `<leader>ca` has your main agent write a summary at the top of the PR's Overview: what the PR does in two or three sentences (and where the description gets it wrong), the notable changes by file, and what deserves a careful look. It is one agent turn with the diff in the prompt, so it takes seconds; give it a small model with `[agent.summary_models]` (e.g. `claude = "haiku"`). A summary notes when the PR has moved on since it was written. Set `auto_summary = true` under `[agent]` to have one written for each PR as it first needs you.

## Review profiles

A profile is a named way to review: extra instructions, and optionally its own reviewers and skills. Without `reviewers`, it runs on the main agent. `<leader>cp` picks one and runs it, and its findings get their own section in the AI review tab (e.g. "Codex · adversarial"), next to the plain review rather than replacing it. "adversarial" is built in: it reviews the change as someone trying to break it. Profiles are useful on your own PRs as a check before you ask for review, since nothing is posted until you choose to. Add your own, or replace the built-in one by name:

```toml
[[agent.profiles]]
name = "security"
prompt = "Look only for security problems: injection, auth, secrets, unsafe input."
reviewers = ["codex"]
```

## Running tests

By default a review only reads. To let it run the tests, give the repo a container image under `[agent.run_images]` (or Run images in `<leader>,`):

```toml
[agent.run_images]
"*" = "golang:1.27"
"acme/web" = "node:22"
```

The key is `owner/name`, and `"*"` covers every other repo. The image needs the repo's toolchain. Claude Code and Codex then get a `run` tool from prowl. It runs a shell command at the root of the repo in a fresh Docker container on a copy of the code, with nothing else of yours: no home directory, no environment, no credentials, and no Linux capabilities. It does have the network, which module downloads need, so a PR's tests can reach what your machine can reach over it. A PR's tests are someone else's code, so they never run on your machine directly, and nothing they change reaches your disk. The agent runs the tests to check the change and its findings, treats a failure the change causes as a finding, and says in its note what it ran. On a re-review of a PR it runs only the tests for what changed since your last review, with the repo's usual checks such as the race detector. The chat (`<leader>ci`) gets the same tool. The `command` agent doesn't.

This is opt-in. Without an image for the repo, or without `docker` on your PATH, reviews only read. It costs:

- Disk: the image (about 800 MB for `golang`) and two caches per repo under prowl's data directory, so later runs don't download modules again: one for PR reviews and one for reviews of your own checkout, so a PR can't plant code that your next local review runs. `prowl uninstall --purge` removes the caches; the images stay, and prowl prints the `docker rmi` that removes them.
- Time: each run copies the code into the container (`.git` stays out) and adds the test run to the review, and stops after 10 minutes. The first run pulls the image.

Each run's container goes when the run ends, and a run stops itself after 10 minutes even when the agent was killed. git doesn't work in the container, because `.git` isn't copied; the agent reads history and diffs with its own git commands.

## Learning from your edits

When you post an AI review, prowl keeps the draft it put in your editor next to what you actually posted. `prowl edits [--since 30d] [--repo NAME]` prints each rewrite as draft and posted, the comments you dropped and the ones you added, and how many pieces went out unchanged. Feed it to whatever shapes how you write: a style guide, a writing skill, or a prompt. Set `edit_examples = 3` under `[agent]` to have prowl put your three most recent rewrites into every review prompt, so the agent drifts toward how you write without anything to maintain.

## Reviewing before a pull request

`prowl review` in a checkout reviews the branch before it has a pull request: everything since it left the default branch, committed or not, and new files not added yet. It runs the same AI review as a PR (guidelines, skills, severities and review instructions) with your main agent, in place in your checkout with read-only tools, and prints the findings when it is done. It works the same from a git worktree. It exits with status 2 when a finding is must-fix. A branch with no changes since it left the default branch, committed or not, has nothing to review: prowl says so and exits 0 without starting the agent. `--detach` runs it in the background instead, and `--agent codex` or `--agent claude:adversarial` picks another reviewer or a profile.

By default the review only reads. With a run image set (see [Running tests](#running-tests)), it can run your tests too. Each run works on a copy of your checkout, so it can't touch work you haven't committed.

The review shows in prowl under Mine as a local item (`⌂`) with the branch, its overview and diff, and the usual AI review tab: revise, chat, discard, re-run with `<leader>cc`. Nothing about it is on GitHub, so approving or posting from it is off. Running it again on the same branch starts from what changed since the last review. `prowl findings` in the checkout prints it while the branch has no PR. Once you open a PR for the branch (a draft counts), the local review moves to it: its runs show in the PR's AI review tab, marked as from your checkout before the PR, and the next review of the PR starts from what changed since. They can still be posted; to chat with or revise one, start a new review on the PR, since its session lives in your checkout. A local review goes away when you discard it, or when its branch or checkout is gone.

To have a coding agent review its own work, either add a line like this to the repo's `AGENTS.md` (any agent):

```markdown
Before you say the work is done, run `prowl review` and fix the must-fix findings it prints.
```

or, for Claude Code, a Stop hook in `.claude/settings.json`. Exit status 2 stops Claude from finishing and hands it the findings; `stop_hook_active` keeps it to one review per task:

```json
{
  "hooks": {
    "Stop": [
      { "hooks": [ { "type": "command", "command": "jq -e .stop_hook_active >/dev/null || prowl review 1>&2" } ] }
    ]
  }
}
```

A review takes a few minutes and runs every time the hook fires, so turn it on per project rather than globally.

## Fixing findings with your own coding agent

On your own PRs, an AI review is often more useful as a to-do list than as comments. Two ways to hand it to the agent working in your checkout, without posting anything:

- Install the skill once with `prowl skill install` for Claude Code, or `prowl skill install --agent codex` for Codex. After that, asking the agent in a checkout to "fix the review findings" (or "what did the review say?") is enough: it runs `prowl findings` (or `prowl review` when you ask for a review before a PR), checks each finding against the code, fixes the ones that hold up, says why it left the others, and drafts text for findings about the PR description. It never commits, pushes or posts. The skill's source is [`internal/skill/SKILL.md`](../internal/skill/SKILL.md).
- Without the skill, tell any agent to run `prowl findings --prompt`, which prints the findings wrapped in the same instructions.
- In prowl, `<leader>ch` copies that prompt to the clipboard to paste wherever you like.

After you push, the next review on that PR starts from what changed.
