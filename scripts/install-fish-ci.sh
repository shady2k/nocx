#!/bin/sh
# Provision the audited native Fish for shell-integration acceptance, not the
# user's account shell. Both CI and local container runners call this owner.
# Usage: scripts/install-fish-ci.sh <private destination directory>
set -eu

DEST="${1:?usage: install-fish-ci.sh <destination directory>}"
VERSION=4.9.3
case "$(uname -s):$(uname -m)" in
    Linux:x86_64)
        ASSET="fish-$VERSION-linux-x86_64.tar.xz"
        SHA=8f643d10ad1abb0ef7072764c01ceb5cf61b2d373fda400cf60f0bad69b7e095 ;;
    Linux:aarch64|Linux:arm64)
        ASSET="fish-$VERSION-linux-aarch64.tar.xz"
        SHA=57349f0111728c64b703a84e611aa97ff4d1fa8466385d64e67bd5bc3ffa49fc ;;
    Darwin:*)
        ASSET="fish-$VERSION.app.zip"
        SHA=3619fa92d540ed0dff4816594429290550d21b92226e0523457e5f0242c5e9a8 ;;
    *) printf 'install-fish-ci: unsupported platform\n' >&2; exit 1 ;;
esac

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT HUP INT TERM
curl -fsSL "https://github.com/fish-shell/fish-shell/releases/download/$VERSION/$ASSET" -o "$TMP/archive"
if command -v sha256sum >/dev/null 2>&1; then
    printf '%s  %s\n' "$SHA" "$TMP/archive" | sha256sum -c -
else
    printf '%s  %s\n' "$SHA" "$TMP/archive" | shasum -a 256 -c -
fi
mkdir -p "$DEST/bin"
case "$ASSET" in
    *.tar.xz) tar -xJf "$TMP/archive" -C "$DEST/bin" fish ;;
    *.zip)
        unzip -oq "$TMP/archive" -d "$DEST"
        ln -sf "../fish-$VERSION.app/Contents/Resources/base/usr/local/bin/fish" "$DEST/bin/fish" ;;
esac
[ "$("$DEST/bin/fish" --version)" = "fish, version $VERSION" ]
"$DEST/bin/fish" --version
if [ -n "${GITHUB_PATH:-}" ]; then
    printf '%s/bin\n' "$DEST" >> "$GITHUB_PATH"
    printf 'NOCX_REQUIRE_NATIVE_FISH=1\n' >> "$GITHUB_ENV"
fi
