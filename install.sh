#!/bin/sh
# Pearl CLI installer: downloads the latest release binary for this platform,
# verifies its SHA-256 checksum, and installs it into a user-owned bin dir.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/August-H/pearl-cli/main/install.sh | sh
#
# Optional environment overrides (useful for mirrors and testing):
#   PEARL_INSTALL_DIR   target directory (default: ~/.local/bin)
#   PEARL_DOWNLOAD_BASE asset base URL
set -eu

REPO="August-H/pearl-cli"
DOWNLOAD_BASE="${PEARL_DOWNLOAD_BASE:-https://github.com/$REPO/releases/latest/download}"
INSTALL_DIR="${PEARL_INSTALL_DIR:-$HOME/.local/bin}"

if ! command -v curl >/dev/null 2>&1; then
    echo "curl is required to download Pearl: https://curl.se" >&2
    exit 1
fi

case "$(uname -s)" in
    Darwin) OS=darwin ;;
    Linux) OS=linux ;;
    *)
        echo "Unsupported operating system: $(uname -s)." >&2
        echo "On Windows use install.ps1 instead:" >&2
        echo "  irm https://raw.githubusercontent.com/$REPO/main/install.ps1 | iex" >&2
        exit 1
        ;;
esac

case "$(uname -m)" in
    arm64 | aarch64) ARCH=arm64 ;;
    x86_64 | amd64) ARCH=amd64 ;;
    *)
        echo "Unsupported architecture: $(uname -m)" >&2
        exit 1
        ;;
esac

ASSET="pearl-$OS-$ARCH"
TEMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TEMP_DIR"' EXIT INT TERM

echo "Downloading Pearl ($ASSET)..."
curl -fsSL "$DOWNLOAD_BASE/$ASSET" -o "$TEMP_DIR/pearl" ||
    {
        echo "Could not download $DOWNLOAD_BASE/$ASSET." >&2
        echo "Check https://github.com/$REPO/releases for available releases," >&2
        echo "or build from source: go build -o pearl ./cmd/pearl" >&2
        exit 1
    }
curl -fsSL "$DOWNLOAD_BASE/$ASSET.sha256" -o "$TEMP_DIR/pearl.sha256"

EXPECTED="$(cut -d' ' -f1 "$TEMP_DIR/pearl.sha256")"
if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL="$(sha256sum "$TEMP_DIR/pearl" | cut -d' ' -f1)"
else
    ACTUAL="$(shasum -a 256 "$TEMP_DIR/pearl" | cut -d' ' -f1)"
fi
if [ "$ACTUAL" != "$EXPECTED" ]; then
    echo "Checksum mismatch; refusing to install." >&2
    echo "  expected $EXPECTED" >&2
    echo "  actual   $ACTUAL" >&2
    exit 1
fi

mkdir -p "$INSTALL_DIR"
chmod 755 "$TEMP_DIR/pearl"
mv -f "$TEMP_DIR/pearl" "$INSTALL_DIR/pearl"

VERSION="$("$INSTALL_DIR/pearl" version 2>/dev/null | head -n 1 | cut -d' ' -f3 || true)"

case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
        echo
        echo "NOTE: $INSTALL_DIR is not on your PATH."
        echo "Add this line to your ~/.zshrc or ~/.bashrc:"
        echo "  export PATH=\"$INSTALL_DIR:\$PATH\""
        echo
        ;;
esac

echo "Installed $INSTALL_DIR/pearl ${VERSION:+($VERSION)}"
echo "Next step: run \"pearl configure\" to add your OpenRouter API key."
