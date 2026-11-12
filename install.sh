#!/bin/sh
# Installs the latest prowl release on Linux or macOS:
#   curl -fsSL https://raw.githubusercontent.com/tmstorm/prowl/main/install.sh | sh
# It goes in ~/.local/bin, or $PROWL_INSTALL_DIR. On Windows, use install.ps1.
set -eu

repo=tmstorm/prowl
dir=${PROWL_INSTALL_DIR:-$HOME/.local/bin}

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case $os in
linux | darwin) ;;
*) echo "prowl: no build for $os here; on Windows, use install.ps1" >&2; exit 1 ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) echo "prowl: no build for $(uname -m)" >&2; exit 1 ;;
esac

archive=prowl_${os}_${arch}.tar.gz
base=https://github.com/$repo/releases/latest/download
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

curl -fsSL "$base/$archive" -o "$tmp/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"
want=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
if command -v sha256sum >/dev/null; then
  got=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
else
  got=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "prowl: the download doesn't match its checksum; not installing" >&2
  exit 1
fi

tar -xzf "$tmp/$archive" -C "$tmp" prowl
mkdir -p "$dir"
install -m 755 "$tmp/prowl" "$dir/prowl"
echo "prowl installed to $dir/prowl"
case ":$PATH:" in
*":$dir:"*) ;;
*) echo "$dir is not on your PATH; add it to run prowl from anywhere" ;;
esac
"$dir/prowl" version
