#!/bin/sh
# Install Barnraise from the GitHub releases of leandrodaf/barnraise.
#
#   curl -fsSL https://raw.githubusercontent.com/leandrodaf/barnraise/main/install.sh | sh
#
# Environment:
#   BARNRAISE_VERSION      tag to install, e.g. v0.1.0 (default: the latest release)
#   BARNRAISE_INSTALL_DIR  where to put the binary (default: /usr/local/bin as root,
#                          ~/.local/bin otherwise)
set -eu

REPO="leandrodaf/barnraise"
BIN="barnraise"

say() { printf '%s\n' "$*" >&2; }
fail() { say "install.sh: $*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }

fetch() { # url dest
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    fail "curl or wget is required"
  fi
}

need uname
need tar

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) fail "unsupported operating system: $os (Linux and macOS are supported)" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) fail "unsupported architecture: $arch (amd64 and arm64 are supported)" ;;
esac

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  fail "sha256sum or shasum is required to verify the download"
fi

tmp=$(mktemp -d 2>/dev/null || mktemp -d -t barnraise)
trap 'rm -rf "$tmp"' EXIT INT TERM

version=${BARNRAISE_VERSION:-}
if [ -z "$version" ]; then
  fetch "https://api.github.com/repos/$REPO/releases/latest" "$tmp/latest.json"
  version=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp/latest.json" | head -n 1)
  [ -n "$version" ] || fail "could not find the latest release"
fi
case "$version" in v*) ;; *) version="v$version" ;; esac
plain=${version#v}

archive="${BIN}_${plain}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

say "Downloading $BIN $version for $os/$arch"
fetch "$base/$archive" "$tmp/$archive" || fail "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

want=$(awk -v f="$archive" '$2 == f || $2 == "*"f {print $1}' "$tmp/checksums.txt")
[ -n "$want" ] || fail "$archive is not listed in checksums.txt"
got=$(sha256 "$tmp/$archive")
[ "$want" = "$got" ] || fail "checksum mismatch for $archive: expected $want, got $got"
say "Checksum verified"

tar -xzf "$tmp/$archive" -C "$tmp" "$BIN" || fail "the archive does not contain $BIN"

dir=${BARNRAISE_INSTALL_DIR:-}
if [ -z "$dir" ]; then
  if [ "$(id -u)" = "0" ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"
if [ -w "$dir" ]; then
  install -m 0755 "$tmp/$BIN" "$dir/$BIN" 2>/dev/null || { cp "$tmp/$BIN" "$dir/$BIN" && chmod 0755 "$dir/$BIN"; }
else
  fail "$dir is not writable; set BARNRAISE_INSTALL_DIR or run as root"
fi

say "Installed $dir/$BIN ($("$dir/$BIN" version 2>/dev/null || echo "$version"))"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "Add $dir to your PATH: export PATH=\"$dir:\$PATH\"" ;;
esac
say "Next: cd your-repo && $BIN init && $BIN up"
