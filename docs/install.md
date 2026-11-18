# Installing prowl

## Linux and macOS

```sh
curl -fsSL https://raw.githubusercontent.com/tmstorm/prowl/main/install.sh | sh
```

The script first checks for `gh` and `git`. When one is missing it shows the command that installs it with your package manager (pacman, apt, dnf, zypper, apk or Homebrew) and asks before running it; say no and it stops with what to install. Then it offers the extras prowl works without: a Nerd Font for icons, and on Linux `notify-send` and a clipboard tool. On Linux the font is the Nerd Font symbols, which most terminals use for icons without changing your font: the `ttf-nerd-fonts-symbols` package on Arch, and a download into `~/.local/share/fonts` elsewhere. WezTerm, Ghostty and kitty have them built in. Last, it downloads the latest release for your system, checks it against the release's checksums, and puts `prowl` in `~/.local/bin` (set `PROWL_INSTALL_DIR` to change that).

With no terminal to ask, as in CI, it installs no extras and stops if `gh` or `git` is missing. `PROWL_YES=1` answers yes to every question, and `NO_COLOR=1` turns the colors off.

## Windows

```powershell
irm https://raw.githubusercontent.com/tmstorm/prowl/main/install.ps1 | iex
```

It checks for `gh` and `git` first and offers to install what's missing with `winget`, then offers JetBrainsMono Nerd Font for icons (choose it as Windows Terminal's font face afterwards). It puts `prowl.exe` in `%LOCALAPPDATA%\Programs\prowl` and adds that folder to your PATH. `PROWL_INSTALL_DIR`, `PROWL_YES=1` and `NO_COLOR=1` work as they do for the script above. Windows support is new and less tested than Linux and macOS: the agent chat opens fullscreen (Windows has no pseudo-terminal for the floating window), notifications are Windows toasts, and the config and data live in `%APPDATA%\prowl` and `%LOCALAPPDATA%\prowl`.

## With Go

```sh
go install github.com/tmstorm/prowl@latest
```

Or from a clone: `go build` (or `task build` / `task install` if you use [Task](https://taskfile.dev)).

## What prowl needs

- The [GitHub CLI](https://cli.github.com), logged in (`gh auth login`). prowl uses its credentials, and offers to run the login on first start.
- Optional: [Claude Code](https://claude.com/claude-code) or the [Codex CLI](https://github.com/openai/codex) for AI reviews and the reviewer chat. Claude Code is the default; see [AI reviews](ai-reviews.md) to switch.
- Optional: a [Nerd Font](https://www.nerdfonts.com) for icons and powerline separators. The first start shows a few icons and asks whether they look right; yes turns on `nerd_font`.
- Optional: an editor for long reviews; prowl uses `$VISUAL`, then `$EDITOR`, then `vi` (`notepad` on Windows).

Notifications use `notify-send` on Linux, Notification Center on macOS and toasts on Windows. The clipboard uses `wl-copy` on Wayland, `xclip` or `xsel` on X11, `pbcopy` on macOS and PowerShell on Windows and WSL. Over SSH, or with none of those, prowl hands the text to your terminal with OSC 52, so it reaches the clipboard of the machine you are sitting at. Most terminals accept that; in tmux it needs `set -g set-clipboard on`.

With no configuration, prowl shows every open PR that involves you across GitHub. To limit it to your work, set `orgs` (see [Config](config.md)).

The rest of the docs are built into prowl for the version you run: `:help` (or `?` then `d`) opens them, and `prowl docs` prints them. [The prowl command](cli.md) covers every command, background notifications, updating, uninstalling and logins, and [The TUI](tui.md) every key.

