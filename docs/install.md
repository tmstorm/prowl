# Installing prowl

## Linux and macOS

```sh
curl -fsSL https://raw.githubusercontent.com/tmstorm/prowl/main/install.sh | sh
```

The script downloads the latest release for your system, checks it against the release's checksums, and puts `prowl` in `~/.local/bin` (set `PROWL_INSTALL_DIR` to change that).

## Windows

```powershell
irm https://raw.githubusercontent.com/tmstorm/prowl/main/install.ps1 | iex
```

This puts `prowl.exe` in `%LOCALAPPDATA%\Programs\prowl` and adds that folder to your PATH. Windows support is new and less tested than Linux and macOS: the agent chat opens fullscreen (Windows has no pseudo-terminal for the floating window), notifications are Windows toasts, and the config and data live in `%APPDATA%\prowl` and `%LOCALAPPDATA%\prowl`.

## With Go

```sh
go install github.com/tmstorm/prowl@latest
```

Or from a clone: `go build` (or `task build` / `task install` if you use [Task](https://taskfile.dev)).

## What prowl needs

- The [GitHub CLI](https://cli.github.com), logged in (`gh auth login`). prowl uses its credentials, and offers to run the login on first start.
- Optional: [Claude Code](https://claude.com/claude-code) or the [Codex CLI](https://github.com/openai/codex) for AI reviews and the reviewer chat. Claude Code is the default; see [AI reviews](ai-reviews.md) to switch.
- Optional: a [Nerd Font](https://www.nerdfonts.com) for icons and powerline separators (`nerd_font = true`).
- Optional: an editor for long reviews; prowl uses `$VISUAL`, then `$EDITOR`, then `vi` (`notepad` on Windows).

Notifications use `notify-send` on Linux, Notification Center on macOS and toasts on Windows. The clipboard uses `wl-copy`, `xclip`, `xsel`, `pbcopy` or PowerShell, whichever the system has.

With no configuration, prowl shows every open PR that involves you across GitHub. To limit it to your work, set `orgs` (see [Config](config.md)). The config lives at `~/.config/prowl/config.toml` and state (review snapshots, snoozes, AI reviews, worktrees) in `~/.local/share/prowl`, following `XDG_CONFIG_HOME` and `XDG_DATA_HOME`.

## Notifications in the background

```sh
prowl service install
```

runs `prowl watch` at login and starts it now: a systemd user service on Linux, a launchd agent on macOS, a Task Scheduler task on Windows. `prowl service uninstall` removes it. The same switch is in the options (`<leader>,` → Background notifications), and prowl suggests it once, the first time you open it. While it runs, the TUI leaves notifications to it.

On Linux, check it with `systemctl --user status prowl-watch` and follow its log with `journalctl --user -u prowl-watch -f`. It's your user's service, so no `sudo`.

When prowl is updated, the running `prowl watch` notices its executable changed and restarts on the new version within a poll interval. On Windows a running `.exe` can't be overwritten: prowl's own update moves it aside first, and `install.ps1` stops the task, updates prowl and starts the task again.

## Updating

prowl asks GitHub once a day whether a newer release is out. What happens next depends on how you installed it.

- **With the install script**, prowl updates itself. The background service installs the new release, or the TUI does when the service isn't running. The TUI you have open keeps the version it started with and says `prowl v0.2.0 is installed`; `<leader>U` restarts it on the new one. `prowl update` does the same by hand and prints what's new.
- **From the AUR**, and later Homebrew or Scoop, the package manager owns `prowl` and prowl never replaces it. It tells you once per release, under the legend and in `prowl version`, which command updates it, such as `yay -S prowl-bin`.
- **With `go install`**, it tells you to run `go install github.com/tmstorm/prowl@latest`.
- **A build from source** never checks.

The first time a new version starts, the legend offers its release notes, and `<leader>N` shows them at any time. `check_updates = false` turns the daily check off. `auto_update = false` keeps the check but leaves installing to you, with `prowl update` or `<leader>U`.

Each release's `checksums.txt` is signed with prowl's release key, and prowl installs nothing that doesn't match a signed checksum. To check a download yourself, use [minisign](https://jedisct1.github.io/minisign/) with the key in [`minisign.pub`](../minisign.pub):

```sh
minisign -Vm checksums.txt -p minisign.pub
sha256sum --check --ignore-missing checksums.txt
```

## Uninstalling

```sh
prowl uninstall
```

lists what prowl put on this machine and asks before removing it: the background service, the review worktrees and `refs/prowl/*` refs it added to checkouts under `repo_roots`, the `prowl-findings` skill (only as this prowl writes it; a copy that differs stays, and it says so) and, unless a package manager owns it, the `prowl` binary. If a step fails, the binary stays so you can run it again to finish. On Windows the binary goes once prowl exits, and its folder comes off your PATH when nothing else is in it. Your config and data stay for a reinstall; `prowl uninstall --purge` removes them too. `--yes` skips the question.

When a package manager installed prowl, it says which command removes the binary, such as `sudo pacman -R prowl-bin`. Run `prowl uninstall` first: package managers can't clean up what prowl put in your home folder.

## Commands

- `prowl` opens the TUI.
- `prowl watch` polls GitHub and sends a desktop notification when a PR moves into a state that needs you. When it starts, it catches up on what changed while it wasn't running; more than a few changes come as one summary. While it runs, the TUI leaves notifications to it, so none arrive twice. `prowl service install` runs it at login.
- `prowl list` prints what needs you, one PR per line, for scripts and status bars.
- `prowl review [--detach] [--agent NAME[:PROFILE]]` reviews the branch checked out here before it has a pull request (see [Reviewing before a pull request](ai-reviews.md#reviewing-before-a-pull-request)).
- `prowl findings [PR] [--all] [--prompt] [--agent NAME[:PROFILE]]` prints the AI review's findings as markdown, then each reviewer's note. Run it in a checkout and it finds the current branch's PR, or its local review when it has none; or pass a number or `owner/repo#N`.
- `prowl skill install [--agent claude|codex] [DIR]` installs the `prowl-findings` skill (see [Fixing findings with your own coding agent](ai-reviews.md#fixing-findings-with-your-own-coding-agent)); `prowl skill print` shows it.
- `prowl edits [--since 30d] [--repo NAME]` shows how you edited AI reviews before posting them (see [Learning from your edits](ai-reviews.md#learning-from-your-edits)).
- `prowl schema` prints the JSON Schema a review must match, for a `command` reviewer.
- `prowl update` installs the newest release over this prowl and prints what's new, or says which command updates it when a package manager installed it (see [Updating](#updating)).
- `prowl version` prints the version, and the newer release when one is out.
- `prowl uninstall [--purge] [--yes]` removes what prowl put on this machine (see [Uninstalling](#uninstalling)).

## Accounts

prowl uses the logins of the tools it drives: the GitHub CLI and your agent. If `gh` isn't logged in when prowl starts, it offers to run `gh auth login`. `<leader>l` shows every login, with the account and, for Claude Code, when the login runs out. `enter` on one runs that tool's own login (`gh auth login`, `claude auth login`, `codex login`) and checks again when it finishes. prowl checks in the background every half hour. The statusline says when your review agent is logged out, or `Claude relogin in 2d` when its login runs out within three days, so you can log in again early from `<leader>l`. A review won't start on a logged-out agent. `prowl watch` sends a desktop notification when the agent is logged out or its login ends within a day.

A `command` agent can join in with `login_command` (what `<leader>l` runs) and `status_command` (exit 0 when logged in; it may print `{"loggedIn": true, "account": "...", "expiresAt": "<RFC 3339>"}`) under `[agent]`.
