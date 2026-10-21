#!/bin/sh
# Install the pooka CLI from the latest GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/PiForgeZA/pooka/main/install.sh | sh
#
# Options, as arguments or environment variables:
#   --version vX.Y.Z   POOKA_INSTALL_VERSION   release to install (default: latest)
#   --bin-dir DIR      POOKA_INSTALL_BIN_DIR   where to put the binary (default: ~/.local/bin)
#
# The script never needs root and never touches system directories. It verifies
# the archive against the release's checksums.txt. Afterwards run `pooka setup`.
set -eu

REPO="PiForgeZA/pooka"
VERSION="${POOKA_INSTALL_VERSION:-}"
BIN_DIR="${POOKA_INSTALL_BIN_DIR:-$HOME/.local/bin}"

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --bin-dir) BIN_DIR="$2"; shift 2 ;;
    -h|--help) sed -n '2,11p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

err() { echo "install.sh: $*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || err "curl is required"
command -v tar >/dev/null 2>&1 || err "tar is required"

case "$(uname -s)" in
  Linux) OS=linux ;;
  Darwin) OS=darwin ;;
  *) err "unsupported OS: $(uname -s). pooka runs on Linux and macOS." ;;
esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) err "unsupported architecture: $(uname -m)" ;;
esac

if [ -z "$VERSION" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
  [ -n "$VERSION" ] || err "could not determine the latest release"
fi
case "$VERSION" in
  v*) ;;
  *) VERSION="v$VERSION" ;;
esac
VER="${VERSION#v}"
ARCHIVE="pooka_${VER}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$VERSION"

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT INT TERM

echo "Downloading pooka $VERSION for $OS/$ARCH"
curl -fsSL -o "$WORK/$ARCHIVE" "$BASE/$ARCHIVE" || err "download failed: $BASE/$ARCHIVE"
curl -fsSL -o "$WORK/checksums.txt" "$BASE/checksums.txt" || err "download failed: $BASE/checksums.txt"

expected=$(grep " $ARCHIVE\$" "$WORK/checksums.txt" | cut -d' ' -f1)
[ -n "$expected" ] || err "$ARCHIVE is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$WORK/$ARCHIVE" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$WORK/$ARCHIVE" | cut -d' ' -f1)
else
  err "need sha256sum or shasum to verify the download"
fi
[ "$expected" = "$actual" ] || err "checksum mismatch for $ARCHIVE: expected $expected, got $actual"

tar -xzf "$WORK/$ARCHIVE" -C "$WORK" pooka
mkdir -p "$BIN_DIR"
install -m 0755 "$WORK/pooka" "$BIN_DIR/pooka"
echo "Installed $BIN_DIR/pooka ($("$BIN_DIR/pooka" --version 2>/dev/null || echo "$VERSION"))"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "Note: $BIN_DIR is not on your PATH. Add it, for example:"
     echo "  export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac
echo
echo "Next: run 'pooka setup' to prepare your Proxmox host."
