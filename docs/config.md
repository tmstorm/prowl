# Config

The config lives at `~/.config/prowl/config.toml` (`%APPDATA%\prowl\config.toml` on Windows), following `XDG_CONFIG_HOME`. prowl's state (review snapshots, snoozes, AI reviews, worktrees) lives in `~/.local/share/prowl` (`%LOCALAPPDATA%\prowl`), following `XDG_DATA_HOME`.

prowl writes every setting into the file, commented out while it is at its default, so you can see what exists without pinning today's defaults. Uncomment a line to change it. The TUI's options change most settings without the file (see [Options](tui.md#options)). The TUI picks up edits to the file while it runs; `prowl watch` reads it when it starts (see [prowl watch](cli.md#prowl-watch)).

When prowl saves the config (from the options, when you change the grouping or watch a repo, or in `prowl slack`), it writes the whole file again, and comments you added by hand are lost.

With no configuration, prowl shows every open PR that involves you across GitHub. Set `orgs` to limit it to your work.

## General

| Setting | Default | What it does |
| --- | --- | --- |
| `orgs` | none | GitHub orgs to pull PRs from; empty is your PRs across all of GitHub |
| `repo_roots` | none | where your clones live; reviews reuse `<root>/<repo>` |
| `poll_interval` | `3m` | how often prowl and `prowl watch` check GitHub (30s at least) |
| `notify_timeout` | `8s` | how long desktop notifications stay up, on Linux and Windows (macOS decides for itself) |
| `check_updates` | `true` | ask GitHub once a day whether a newer prowl is out |
| `auto_update` | `true` | install it in the background when the install script installed prowl (see [Updating](cli.md#updating)) |
| `bots` | `github-actions` | accounts treated as bots: their activity starts folded and doesn't count as a reply |

## Inbox

| Setting | Default | What it does |
| --- | --- | --- |
| `watch_repos` | none | repos (owner/name) whose every open PR shows in All; see [Watching a repo](inbox.md#watching-a-repo) |
| `stale_after` | `24h` | a PR waiting on you longer than this is due, and overdue at twice that |
| `sort_by_type` | `true` | sort by Conventional Commit type inside a section, then by wait |
| `type_order` | fix, revert, feat, perf, refactor, test, docs, build, ci, style, other, chore | the order for `sort_by_type` |
| `[type_colors]` | built in | color per commit type: red, green, orange, pink, yellow, purple, cyan, dim or `#rrggbb` |
| `recheck_after_push` | `true` | a push after you approved brings the PR back as re-check |
| `group_by` | `status` | status or repo |

## Slack

See [Slack](slack.md).

| Setting | Default | What it does |
| --- | --- | --- |
| `slack_client_id` | none | client ID of your team's Slack app, for `prowl slack` |
| `slack_channels` | none | Slack channel IDs whose PR links show in the inbox (Channels, in the options' Slack section) |
| `slack_dms` | `true` | PR links sent to you in Slack DMs show in the inbox too |
| `slack_after_review` | `react` | on the message that shared a PR you reviewed: react, reply in its thread, or off |
| `[slack_reactions]` | `reviewing = "eyes"`, `approved = "white_check_mark"`, `changes_requested = "x"`, `commented = "speech_balloon"` | the emoji for each state of your review |
| `[slack_names]` | none | GitHub login = Slack user ID (`U0123ABCD`, a mention) or a name to print, e.g. `alice = "@Alice"` |

## Agent

These go under `[agent]`. See [AI reviews](ai-reviews.md).

| Setting | Default | What it does |
| --- | --- | --- |
| `reviewers` | `claude` | the agents that review: claude, codex, command; the first is the main one |
| `command` | none | the shell command for the `command` agent |
| `login_command`, `status_command` | none | the `command` agent's login (see [Accounts](cli.md#accounts)) |
| `skills` | none | skills every review must use |
| `context_dirs` | none | extra directories the agent may read, e.g. your org's standards repo |
| `should_fix`, `nits` | `true` | report should-fix findings and nits |
| `review_instructions` | built in | what to look for and how to write it |
| `auto_summary` | `false` | write an AI summary of each PR as it first needs you |
| `edit_examples` | `0` | your recent rewrites of drafts fed into reviews as examples |
| `[agent.models]`, `[agent.summary_models]` | each agent's default | model per agent, for reviews and for summaries |
| `[agent.run_images]` | none | container image per repo (`owner/name`, `"*"` for the rest) for the agent's `run` tool; see [Running tests](ai-reviews.md#running-tests) |
| `[[agent.profiles]]` | `adversarial` built in | named ways to review: `name`, `prompt`, and optional `reviewers` and `skills`; see [Review profiles](ai-reviews.md#review-profiles) |

## TUI

These change only the TUI. See [The TUI](tui.md).

| Setting | Default | What it does |
| --- | --- | --- |
| `leader` | `space` | the key that opens the action menu, written `<leader>` in these docs: space, `,`, `\` or `;` |
| `mouse` | `true` | clicks and the wheel |
| `editor` | `$VISUAL`, `$EDITOR`, then `vi` (`notepad` on Windows) | the editor for reviews and long comments |
| `chat_mode` | `popup` | the agent chat in a window over prowl, or `fullscreen` |
| `theme` | `dracula` | dracula, catppuccin, tokyonight, gruvbox, nord, rose-pine, kanagawa |
| `nerd_font` | `false` | powerline separators and icons |
| `show_legend` | `true` | the icon legend under the inbox |
| `playful` | `true` | the owl |
| `repo_prefix` | none | stripped from repo names on screen, e.g. `acme-` shows acme-api as api; empty shows full names |
| `color_repos` | `false` | each repo its own color |
