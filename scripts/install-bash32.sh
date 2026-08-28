#!/bin/sh
# Install a GNU bash 3.2 as `bash32` — the bash macOS ships as /bin/bash.
#
# Why this exists: this is a macOS-first product, Apple froze /bin/bash at
# 3.2.57 (the last GPLv2 release, 2007), and the shell integration script must
# PARSE there. A bash-4-only construct is a syntax error 3.2 raises while
# reading the file, before any version guard inside it can run — which is how
# every bash shell on macOS came up with no integration at all while the whole
# Linux side of CI stayed green (nocx-cn86).
#
# TestBashScript_ParsesUnderBash32 needs a 3.2 to check against. macOS has one
# at /bin/bash and needs nothing from this script. Linux has none: Debian and
# Ubuntu package nothing older than 4, and bash-3.2.57 does not build on a
# modern toolchain without patching. The binary therefore comes from the
# official `bash:3.2` image, which is Alpine — so it is musl-linked, and the
# loader and libncursesw come with it into a private prefix rather than into
# /lib, where a musl libncursesw.so.6 would sit beside the system's glibc copy
# of the same soname. Nothing links against it; it is only ever exec'd.
#
# The CI Linux image (.githooks/images/ci-linux/Dockerfile) does the same thing
# in its own layers rather than calling this script, because a container build
# cannot reach the repo working tree — one duplication, deliberate, and both
# are checked by the same test. This script itself is what CI's Linux runner
# calls (ci.yml). The pre-commit test image was a third copy and went with the
# hook's containerized tests (nocx-hzsiv).
#
# Usage: scripts/install-bash32.sh [prefix]   (default prefix: /usr/local)
set -eu

PREFIX="${1:-/usr/local}"
LIBDIR="$PREFIX/lib/bash32"

if ! command -v docker >/dev/null 2>&1; then
    printf 'install-bash32: docker is required to extract bash 3.2 from the bash:3.2 image\n' >&2
    exit 1
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# `tar -ch`, not `docker cp`: libncursesw.so.6 is a SYMLINK in that image, and
# docker cp copies the link rather than what it points at, so the extracted
# name lands dangling and the next `cp` dies on "cannot stat" — which is
# exactly how this step failed on the runner while the Dockerfile's own
# COPY --from resolved it. -h dereferences.
# The musl loader is named for the architecture (ld-musl-x86_64.so.1 on
# Intel, ld-musl-aarch64.so.1 on Apple Silicon), so the name is discovered
# rather than assumed and staged under a fixed one — hardcoding x86_64 made
# this a no-op-then-fail on every arm64 machine.
LOADER="$(docker run --rm --entrypoint sh bash:3.2 -c 'ls /lib/ld-musl-*.so.1 | head -1')"
LOADER="${LOADER#/}"

docker run --rm --entrypoint tar bash:3.2 \
    -ch -C / -f - \
    usr/local/bin/bash "$LOADER" usr/lib/libncursesw.so.6 \
    | tar -x -C "$TMP"

mkdir -p "$LIBDIR" "$PREFIX/bin"
cp "$TMP/usr/local/bin/bash" "$LIBDIR/bash"
cp "$TMP/$LOADER" "$LIBDIR/loader"
cp "$TMP/usr/lib/libncursesw.so.6" "$LIBDIR/"
chmod +x "$LIBDIR/bash" "$LIBDIR/loader"

cat > "$PREFIX/bin/bash32" <<EOF
#!/bin/sh
exec $LIBDIR/loader --library-path $LIBDIR $LIBDIR/bash "\$@"
EOF
chmod +x "$PREFIX/bin/bash32"

"$PREFIX/bin/bash32" --version | head -1
"$PREFIX/bin/bash32" --version | grep -q 'version 3\.2'
