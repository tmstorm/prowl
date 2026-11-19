# The prowl command

`prowl` with no command opens the TUI (see [The TUI](tui.md)). The commands below work without it, for scripts, status bars, hooks and the coding agent in your checkout. Each prints its errors as `prowl: <error>` on stderr and exits 1; `prowl review` also exits 2 on a must-fix finding. `-h` or `--help` after any command prints its section of this page. Flags go before or after a command's arguments, with their value after a space or an `=`: `--since 12h` or `--since=12h`.

## prowl watch

```sh
prowl watch
```

Polls GitHub every `poll_interval` and sends a desktop notification when a PR moves into a state that needs you. When it starts, it catches up on what changed while it wasn't running; more than a few changes come as one summary. It prints a line per sync (`14:02:11 synced 23 PRs, 1 changes`) and keeps going until stopped.

Only one runs at a time: a second `prowl watch` says `prowl watch is already running` and exits 0. While one runs, the TUI leaves notifications to it, so none arrive twice. It reads the config when it starts and keeps those settings until it restarts. It checks your logins every half hour and installs updates (see [Updating](#updating)); when its own binary is replaced, it restarts on the new one within a poll.

## prowl service

```sh
prowl service install
prowl service uninstall
```

`install` runs `prowl watch` at login and starts it now: a systemd user service on Linux, a launchd agent on macOS, and on Windows an entry in your startup programs (`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`, listed under Task Manager's Startup apps), which needs no admin. `uninstall` stops it and removes it.

On Linux, check it with `systemctl --user status prowl-watch` and follow its log with `journalctl --user -u prowl-watch -f`. It's your user's service, so no `sudo`. After editing the config, `systemctl --user restart prowl-watch`, or `prowl service uninstall` and `install` anywhere, makes it read the file again.

## prowl list

```sh
prowl list
```

Syncs once and prints the PRs that need you, one per line, for scripts and status bars. Snoozed PRs are left out. Each line is the bucket, the PR, its title, its author and why it needs you:

```text
needs review   acme/api#42                  fix: retry on timeout (bob) — review requested
re-check       acme/web#7                   feat: dark mode (carol) — pushed since you approved
```

It prints nothing when nothing needs you.

## prowl review

```sh
prowl review [--detach] [--agent NAME[:PROFILE]]
```

Reviews the branch checked out in the current directory before it has a pull request: everything since it left the default branch, committed or not, and new files not added yet. It shows progress on stderr, prints the findings as Markdown when it's done (as `prowl findings` does), and ends with a line like `done in 3m12s · 4 findings, 1 must-fix`. See [Reviewing before a pull request](ai-reviews.md#reviewing-before-a-pull-request).

- `--detach` starts the review in the background and returns at once. The review shows under Mine in the TUI, and `prowl findings` prints it when it's done.
- `--agent NAME` reviews with another agent than the main one, e.g. `--agent codex`, and `--agent NAME:PROFILE` runs a [review profile](ai-reviews.md#review-profiles), e.g. `--agent claude:adversarial`.

Exits 2 when a finding is must-fix, so a hook can keep a coding agent working until its review is clean. A branch with nothing to review says so and exits 0 without starting the agent.

## prowl findings

```sh
prowl findings [PR] [--all] [--prompt] [--agent NAME[:PROFILE]]
```

Prints the AI reviews of a PR as Markdown: every reviewer's findings combined, then each reviewer's note. PR is `owner/repo#N`, or anything `gh pr view` takes in the checkout: a number, a link or a branch. Without one, it's the current branch's PR, or the branch's local review when it has no PR. When the branch has moved since the review, the output says so.

- `--all` includes findings you excluded in the review.
- `--prompt` wraps the findings in instructions for a coding agent: verify each against the code, fix the ones that hold up, say why the others don't. It prints plain Markdown even in a terminal.
- `--agent` limits it to one agent with its profiles, or to one profile.

A review still running is left out, with a note on stderr. With no finished review, it says how to start one and exits 1. See [Fixing findings with your own coding agent](ai-reviews.md#fixing-findings-with-your-own-coding-agent).

## prowl skill

```sh
prowl skill install [--agent claude|codex] [DIR]
prowl skill print
```

`install` writes the `prowl-findings` skill, which lets the agent in your checkout act on review findings when you ask it to. It goes to Claude Code's skills (`~/.claude/skills/prowl-findings`, following `CLAUDE_CONFIG_DIR`), or to Codex's with `--agent codex` (following `CODEX_HOME`), or to DIR. `print` shows it.

## prowl edits

```sh
prowl edits [--since 30d] [--repo NAME]
```

Shows how you edited AI reviews before posting them: each rewrite as the draft and what you posted, the comments you dropped and the ones you added. `--since` takes days or a duration (`30d`, `12h`; 30 days by default) and `--repo` limits it to one repo. See [Learning from your edits](ai-reviews.md#learning-from-your-edits).

## prowl slack

```sh
prowl slack [--off]
```

Signs you in to Slack, so PRs shared there show in your inbox. The first time, it asks for your team's Slack app's client ID (or prints the manifest to create the app), then opens Slack in your browser. `--off` signs out and deletes the token. See [Slack](slack.md#setting-it-up).

## prowl logs

```sh
prowl logs [--since 1h] [--run ID] [--path]
```

Prints the end of prowl's log, which every prowl process appends to: the TUI, `prowl watch` and the reviews running in the background. Each line says when, how bad, which process and what happened, such as a sync with how many PRs and how long it took, a review's phases, or an error with its cause. `--since 1h` prints the lines of the last hour (`2d` for two days), `--run 412` the lines of one review with what its agent printed, and `--path` the folder the log and the reviews' output are in. The log holds IDs, counts, durations and errors, never a token, a PR's text, a prompt or a review, so you can paste it into an issue. `PROWL_DEBUG=1` adds every GitHub and Slack call with its timing.

## prowl docs

```sh
prowl docs [TOPIC | PAGE SECTION]
```

With nothing, lists these docs. With a topic, prints what it leads to: a page (`prowl docs slack`), a section (`prowl docs snoozing`), a key or a setting (`prowl docs stale_after` prints its row with the table's header). A topic that isn't one lists the sections that mention it. `prowl docs PAGE SECTION` prints a section of a page by its heading or anchor. In a terminal the Markdown is rendered; piped, it's printed as it is.

## prowl man

```sh
prowl man DIR
```

Writes the man pages for this version under DIR: `DIR/man1/prowl.1`, this page, and `DIR/man5/prowl-config.5`, the [config](config.md). The install script writes them to `~/.local/share/man`, where `man prowl` finds them, and `prowl update` writes them with each new version, also for an install from before prowl had them. Packages install them under `/usr/share/man`.

## prowl schema

```sh
prowl schema
```

Prints the JSON Schema a review must match, for a `command` reviewer (see [Agents](ai-reviews.md#agents)). Severities you turned off are left out of it.

## prowl version

```sh
prowl version
```

Prints the version, and the newer release and how to install it when one is out. `--version` does the same.

## prowl update

```sh
prowl update
```

Installs the newest release over this prowl and prints what's new. A prowl from a package manager or `go install` says which command updates it instead. See [Updating](#updating).

## prowl uninstall

```sh
prowl uninstall [--purge] [--yes]
```

Lists what prowl put on this machine and asks before removing it: the background service, the review worktrees and `refs/prowl/*` refs it added to checkouts under `repo_roots`, the `prowl-findings` skill (only as this prowl writes it; a copy that differs stays, and it says so), the man pages the install script wrote, and the `prowl` binary. If a step fails, the binary stays so you can run it again to finish. On Windows the binary goes once prowl exits, and its folder comes off your PATH when nothing else is in it.

- `--purge` also removes the config, the data (reviews, snoozes, caches) and the Slack token. Docker images for running tests stay; prowl prints the `docker rmi` that removes them.
- `--yes` (`-y`) skips the question.

prowl leaves the binary alone when a package manager installed it, and says which command removes it, such as `sudo pacman -R prowl-bin`. Run `prowl uninstall` first: package managers can't clean up what prowl put in your home folder. When prowl's folder isn't writable, it says to remove the binary by hand.

## Updating

prowl asks GitHub once a day whether a newer release is out (`check_updates`). What happens next depends on how you installed it.

- **With the install script**, prowl updates itself. `prowl watch` installs the new release, or the TUI does when `prowl watch` isn't running. A TUI already open keeps the version it started with until you restart it. `prowl update` does the same by hand. `auto_update = false` keeps the check but leaves installing to you.
- **From a package manager** (the AUR's `prowl-bin` is coming soon, then Homebrew and Scoop), the package manager owns `prowl` and prowl never replaces it. It tells you once per release, in the TUI and in `prowl version`, which command updates it, such as `yay -S prowl-bin`.
- **With `go install`**, it tells you to run `go install github.com/tmstorm/prowl@latest`.
- **In a folder prowl can't write to**, it points you at the releases page to download the new version yourself.
- **A build from source** never checks.

On Windows a running `.exe` can't be overwritten: prowl's own update moves it aside first, and `install.ps1` stops `prowl watch`, updates prowl and starts it again.

Each release's `checksums.txt` is signed with prowl's release key, and prowl installs nothing that doesn't match a signed checksum. To check a download yourself, use [minisign](https://jedisct1.github.io/minisign/) with the key in [`minisign.pub`](../minisign.pub):

```sh
minisign -Vm checksums.txt -p minisign.pub
sha256sum --check --ignore-missing checksums.txt
```

## Accounts

prowl uses the logins of the tools it drives: the GitHub CLI, your review agent and Slack. The TUI, `prowl watch`, `prowl list`, `prowl review` and `prowl update` need a GitHub login, and without one they stop and say to run `gh auth login`; the TUI offers to run it for you. A token in `GH_TOKEN` or `GITHUB_TOKEN` works too.

prowl checks the logins every half hour, in the TUI and in `prowl watch`. A review won't start on a logged-out agent, and while Slack refuses your sign-in prowl stops reading it and won't post there. `prowl watch` sends a desktop notification when the agent is logged out or its login ends within three days, and when Slack refuses your sign-in. The TUI shows the same, and logs in again from [the accounts list](tui.md#the-accounts-list).

A `command` agent can join in with `login_command` (what logging in runs) and `status_command` (exit 0 when logged in; it may print `{"loggedIn": true, "account": "...", "expiresAt": "<RFC 3339>"}`) under `[agent]`.

## Environment

| Variable | Does |
| --- | --- |
| `GH_TOKEN`, `GITHUB_TOKEN` | the GitHub token, instead of the GitHub CLI's login |
| `PROWL_SLACK_TOKEN` | the Slack token, for a machine without an OS keyring |
| `XDG_CONFIG_HOME` | where the config goes: `$XDG_CONFIG_HOME/prowl/config.toml` |
| `XDG_DATA_HOME` | where prowl keeps its data: `$XDG_DATA_HOME/prowl` |
| `VISUAL`, `EDITOR` | the editor for reviews and comments, when `editor` isn't set |
| `CLAUDE_CONFIG_DIR`, `CODEX_HOME` | where Claude Code's and Codex's own settings and skills are |
| `PROWL_INSTALL_DIR` | where the install script puts `prowl` (`~/.local/bin` by default) |
| `PROWL_YES` | `1` answers yes to the install script's questions |
| `PROWL_DEBUG` | `1` adds every GitHub and Slack call to the log (see [prowl logs](#prowl-logs)) |

A `command` reviewer gets `PROWL_SCHEMA` (the path of a file with the JSON Schema its review must match) and `PROWL_MODEL` (its model from `[agent.models]`) in its environment.

## Files

| Path | Holds |
| --- | --- |
| `~/.config/prowl/config.toml` | the config (see [Config](config.md)); `%APPDATA%\prowl\config.toml` on Windows |
| `~/.local/share/prowl` | reviews, snoozes, review snapshots, drafts and the test caches; `%LOCALAPPDATA%\prowl` on Windows |
| `~/.local/share/prowl/worktrees` | the throwaway checkouts reviews run in |
| `~/.local/share/prowl/logs` | `prowl.log`, the log, and `runs/`, what each review's agent printed, kept 30 days |
| `~/.local/share/man` | the man pages, when the install script installed prowl |
