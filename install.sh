#!/bin/sh
# Installs the latest prowl release on Linux or macOS:
#   curl -fsSL https://raw.githubusercontent.com/tmstorm/prowl/main/install.sh | sh
# It checks for gh and git first and offers to install what's missing with the
# system's package manager, then offers a Nerd Font and the optional tools.
# prowl goes in ~/.local/bin, or $PROWL_INSTALL_DIR. PROWL_YES=1 answers yes
# to every question. On Windows, use install.ps1.
set -eu

repo=tmstorm/prowl
dir=${PROWL_INSTALL_DIR:-$HOME/.local/bin}

# prowl's default theme, Dracula, when this prints to a terminal.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	e=$(printf '\033')
	accent="${e}[1;38;2;189;147;249m" muted="${e}[38;2;98;114;164m" eye="${e}[1;38;2;241;250;140m"
	talon="${e}[38;2;255;184;108m" green="${e}[38;2;80;250;123m" red="${e}[38;2;255;85;85m" off="${e}[0m"
else
	accent='' muted='' eye='' talon='' green='' red='' off=''
fi

step() { printf '\n%s%s%s\n' "$accent" "$1" "$off"; }
ok() { printf '  %s✓%s %s\n' "$green" "$off" "$1"; }
no() { printf '  %s✗%s %s\n' "$red" "$off" "$1"; }
note() { printf '  %s%s%s\n' "$muted" "$1" "$off"; }
die() {
	printf '\n%sprowl:%s %s\n' "$red" "$off" "$1" >&2
	exit 1
}

# ask puts a yes/no question to the person at the terminal. With nobody there
# (CI, no tty) the answer is no, unless PROWL_YES=1.
ask() {
	[ "${PROWL_YES:-}" = 1 ] && return 0
	(true </dev/tty) 2>/dev/null || return 1
	printf '\n%s?%s %s %s[Y/n]%s ' "$eye" "$off" "$1" "$muted" "$off" >/dev/tty
	read -r answer </dev/tty || return 1
	case $answer in '' | y | Y | yes | Yes) return 0 ;; esac
	return 1
}

# verify checks file against the "sum  name" lines in sums.
verify() {
	want=$(grep " $2\$" "$3" | cut -d' ' -f1)
	if command -v sha256sum >/dev/null; then
		got=$(sha256sum "$1" | cut -d' ' -f1)
	else
		got=$(shasum -a 256 "$1" | cut -d' ' -f1)
	fi
	[ -n "$want" ] && [ "$want" = "$got" ]
}

printf '\n%s    ,_____,%s\n' "$muted" "$off"
printf '%s    ( %sO%s,%sO%s )%s    %sprowl%s\n' "$muted" "$eye" "$muted" "$eye" "$muted" "$off" "$accent" "$off"
printf '%s    /)   (\\%s    %sthe PR owl%s\n' "$muted" "$off" "$muted" "$off"
printf '%s   ((  "  ))%s\n' "$muted" "$off"
printf '%s  ===%s^%s===%s^%s===%s\n' "$muted" "$talon" "$muted" "$talon" "$muted" "$off"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $os in
linux | darwin) ;;
*) die "no build for $os here; on Windows, use install.ps1" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "no build for $(uname -m)" ;;
esac

sudo=''
if [ "$(id -u)" -ne 0 ]; then
	sudo='sudo '
fi
pm='' gh_pkg=gh notify_pkg='' font_pkg=''
if [ "$os" = darwin ]; then
	if command -v brew >/dev/null; then
		pm='brew install'
	fi
elif [ -n "$sudo" ] && ! command -v sudo >/dev/null; then
	:
elif command -v pacman >/dev/null; then
	pm="${sudo}pacman -S --needed --noconfirm" gh_pkg=github-cli notify_pkg=libnotify font_pkg=ttf-nerd-fonts-symbols
elif command -v apt-get >/dev/null; then
	pm="${sudo}apt-get install -y" notify_pkg=libnotify-bin
elif command -v dnf >/dev/null; then
	pm="${sudo}dnf install -y" notify_pkg=libnotify
elif command -v zypper >/dev/null; then
	pm="${sudo}zypper --non-interactive install" notify_pkg=libnotify-tools
elif command -v apk >/dev/null; then
	pm="${sudo}apk add" gh_pkg=github-cli notify_pkg=libnotify
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

step "What prowl needs"
need='' lack=''
if command -v gh >/dev/null; then
	ok "gh $(gh --version | sed -n '1s/^gh version \([^ ]*\).*/\1/p')"
else
	no "gh, the GitHub CLI"
	need="$need $gh_pkg" lack="gh"
fi
if git --version >/dev/null 2>&1; then
	ok "git $(git --version | cut -d' ' -f3)"
else
	no "git"
	need="$need git" lack="${lack:+$lack and }git"
fi
if [ -n "$need" ]; then
	[ -n "$pm" ] || die "prowl needs $lack: install it (gh: https://cli.github.com), then run this again"
	note "$pm$need"
	ask "Install $lack with this?" || die "prowl needs $lack: install it, then run this again"
	# shellcheck disable=SC2086
	$pm $need || die "couldn't install $lack: install it by hand (gh: https://cli.github.com), then run this again"
fi

step "Extras"
pkgs='' font=''
case ${TERM_PROGRAM:-}:${TERM:-} in
WezTerm:* | ghostty:* | *:xterm-kitty | *:xterm-ghostty)
	ok "Nerd Font icons: your terminal has them built in"
	;;
*)
	if fc-list 2>/dev/null | grep -qi 'nerd font' || find "$HOME/Library/Fonts" /Library/Fonts -iname '*nerd*' 2>/dev/null | grep -q .; then
		ok "a Nerd Font, for icons"
	else
		no "a Nerd Font, for icons"
		font=yes
	fi
	;;
esac
if [ "$os" = linux ]; then
	if command -v notify-send >/dev/null; then
		ok "notify-send, for desktop notifications"
	elif [ -n "$notify_pkg" ]; then
		no "notify-send, for desktop notifications"
		pkgs="$pkgs $notify_pkg"
	fi
	if command -v wl-copy >/dev/null || command -v xclip >/dev/null || command -v xsel >/dev/null; then
		ok "a clipboard tool, for copying"
	elif [ -n "${WAYLAND_DISPLAY:-}" ]; then
		no "wl-copy, for copying"
		pkgs="$pkgs wl-clipboard"
	else
		no "xclip, for copying"
		pkgs="$pkgs xclip"
	fi
fi
if [ -n "$font" ] && [ -n "$font_pkg" ]; then
	pkgs="$pkgs $font_pkg" font=''
elif [ -n "$font" ] && [ "$os" = darwin ] && [ -z "$pm" ]; then
	note "brew installs it: brew install --cask font-symbols-only-nerd-font"
	font=''
fi
if [ -z "$pm" ]; then
	pkgs=''
fi
if [ -n "$pkgs$font" ]; then
	[ -z "$pkgs" ] || note "$pm$pkgs"
	if [ -n "$font" ] && [ "$os" = darwin ]; then
		note "brew install --cask font-symbols-only-nerd-font"
	elif [ -n "$font" ]; then
		note "the Nerd Font symbols into ~/.local/share/fonts"
	fi
	if ask "Install the extras?"; then
		# shellcheck disable=SC2086
		[ -z "$pkgs" ] || $pm $pkgs || no "couldn't install${pkgs}; prowl works without them"
		if [ -n "$font" ] && [ "$os" = darwin ]; then
			brew install --cask font-symbols-only-nerd-font || no "couldn't install the Nerd Font"
			note "choose Symbols Nerd Font as the font for non-ASCII characters in your terminal"
		elif [ -n "$font" ]; then
			nf=https://github.com/ryanoasis/nerd-fonts/releases/latest/download
			fonts=$HOME/.local/share/fonts/NerdFontsSymbolsOnly
			if curl -fsSL "$nf/NerdFontsSymbolsOnly.tar.xz" -o "$tmp/NerdFontsSymbolsOnly.tar.xz" &&
				curl -fsSL "$nf/SHA-256.txt" -o "$tmp/fonts.sha256" &&
				verify "$tmp/NerdFontsSymbolsOnly.tar.xz" NerdFontsSymbolsOnly.tar.xz "$tmp/fonts.sha256" &&
				mkdir -p "$fonts" && tar -xJf "$tmp/NerdFontsSymbolsOnly.tar.xz" -C "$fonts"; then
				fc-cache -f "$fonts" >/dev/null 2>&1 || true
				ok "installed the Nerd Font symbols to $fonts"
			else
				no "couldn't install the Nerd Font; prowl works without icons"
			fi
		fi
	fi
fi

step "Installing prowl"
archive=prowl_${os}_${arch}.tar.gz
base=https://github.com/$repo/releases/latest/download
if ! curl -fsSL "$base/$archive" -o "$tmp/$archive" 2>/dev/null ||
	! curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" 2>/dev/null; then
	die "couldn't download the latest release from https://github.com/$repo/releases"
fi
verify "$tmp/$archive" "$archive" "$tmp/checksums.txt" || die "the download doesn't match its checksum; not installing"
tar -xzf "$tmp/$archive" -C "$tmp" prowl
mkdir -p "$dir"
install -m 755 "$tmp/prowl" "$dir/prowl"
ok "$("$dir/prowl" version | head -n 1) is in $dir"
case ":$PATH:" in
*":$dir:"*) ;;
*) note "$dir is not on your PATH; add it to run prowl from anywhere" ;;
esac
if ! gh auth status >/dev/null 2>&1; then
	note "gh isn't logged in; prowl offers to run gh auth login when it starts"
fi
printf '\n  Run %sprowl%s to start.\n\n' "$accent" "$off"
