# Config

`~/.config/prowl/config.toml` (`%APPDATA%\prowl\config.toml` on Windows), also editable in the app with `<leader>,`. prowl writes every option into the file, commented out while it is at its default, so you can see what exists without pinning today's defaults. Uncomment a line to change it. prowl picks up edits to the file while it runs.

| Setting | Default | What it does |
| --- | --- | --- |
| `orgs` | none | GitHub orgs to pull PRs from; empty is your PRs across all of GitHub |
| `watch_repos` | none | repos (owner/name) whose every open PR shows in All (`<leader>w`) |
| `repo_roots` | none | where your clones live; reviews reuse `<root>/<repo>` |
| `poll_interval` | `3m` | how often prowl and `prowl watch` check GitHub (30s at least) |
| `stale_after` | `24h` | a PR waiting on you longer than this is flagged |
| `notify_timeout` | `8s` | how long desktop notifications stay up |
| `leader` | `space` | the key that opens the action menu, written `<leader>` in these docs: space, `,`, `\` or `;` |
| `theme` | `dracula` | dracula, catppuccin, tokyonight, gruvbox, nord, rose-pine, kanagawa |
| `nerd_font` | `false` | powerline separators and icons |
| `mouse` | `true` | clicks and the wheel |
| `show_legend` | `true` | the icon legend under the inbox |
| `playful` | `true` | the owl |
| `repo_prefix` | none | stripped from repo names on screen, e.g. `acme-` shows acme-api as api; empty shows full names |
| `color_repos` | `false` | each repo its own color |
| `sort_by_type` | `true` | sort by Conventional Commit type inside a section |
| `type_order` | fix, revert, feat, perf, refactor, test, docs, build, ci, style, other, chore | that order |
| `[type_colors]` | built in | color per commit type: red, green, orange, pink, yellow, purple, cyan, dim or `#rrggbb` |
| `recheck_after_push` | `true` | a push after you approved brings the PR back as re-check |
| `group_by` | `status` | status or repo (`<leader>g`) |
| `chat_mode` | `popup` | popup or fullscreen, for `<leader>ci` |
| `editor` | `$VISUAL`, `$EDITOR`, then `vi` | the editor for reviews |
| `bots` | `github-actions` | accounts treated as bots |
| `[slack_names]` | none | GitHub login = name for Slack lines, e.g. `alice = "@Alice"` |
| `[agent] reviewers` | `claude` | the agents that review: claude, codex, command; the first is the main one |
| `command`, `login_command`, `status_command` | none | the `command` agent and its login |
| `skills` | none | skills every review must use |
| `context_dirs` | none | extra directories the agent may read, e.g. your org's standards repo |
| `local_commands` | none | commands `prowl review` may run in your checkout, e.g. `go test` (Claude Code only) |
| `should_fix`, `nits` | `true` | report should-fix findings and nits |
| `review_instructions` | built in | what to look for and how to write it |
| `auto_summary` | `false` | write an AI summary of each PR as it first needs you |
| `edit_examples` | `0` | your recent rewrites of drafts fed into reviews as examples |
| `[agent.models]`, `[agent.summary_models]` | each agent's default | model per agent, for reviews and for summaries |
| `[[agent.profiles]]` | `adversarial` built in | named review focuses for `<leader>cp` |
