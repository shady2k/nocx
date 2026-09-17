#!/usr/bin/env bash
#
# Reproducible build of the libghostty-vt flat-ABI wasm module for this spike.
#
# Produces, in .build/ (gitignored):
#   vt.wasm              shim.c + the wasm-target libghostty-vt.a, flat ABI
#   ghostty-vt.wasm      ghostty's OWN wasm module, copied out for comparison
#   libghostty-vt.a      the wasm-target static archive, for the record
#
# Pins, both fetched into .build/ and .vendor/ (gitignored), both verified
# rather than assumed:
#   - Zig 0.16.0, which is ghostty's build.zig.zon minimum_zig_version at this
#     commit; a different Zig is rejected at configure time.
#   - ghostty e2e53f861482e080bf45054ba49ef471f9849937 (2026-09-11), the exact
#     commit .internal/spikes/emulator/REPORT.md measured, so the probe
#     comparison in REPORT.md is between the same source built two ways.
#
# Needs: bash, curl, tar (xz), git, network. Nothing is installed globally and
# nothing is written outside this directory.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD="$HERE/.build"
VENDOR="$HERE/.vendor"
ZIG_VERSION="0.16.0"
GHOSTTY_COMMIT="e2e53f861482e080bf45054ba49ef471f9849937"
GHOSTTY_REPO="https://github.com/ghostty-org/ghostty.git"
mkdir -p "$BUILD" "$VENDOR"

# --- 1. Pinned Zig, downloaded locally; the version is enforced --------------
ZIG="${ZIG:-$BUILD/zig/zig}"
need_dl=0
if [ ! -x "$ZIG" ]; then
  need_dl=1
elif [ "$("$ZIG" version 2>/dev/null)" != "$ZIG_VERSION" ]; then
  if [ "$ZIG" = "$BUILD/zig/zig" ]; then
    need_dl=1
  else
    echo "!! \$ZIG ($ZIG) is $("$ZIG" version 2>/dev/null), ghostty needs exactly $ZIG_VERSION" >&2
    exit 1
  fi
fi
if [ "$need_dl" = 1 ]; then
  case "$(uname -s)-$(uname -m)" in
    Darwin-arm64)  ZARCH="aarch64-macos" ;;
    Darwin-x86_64) ZARCH="x86_64-macos" ;;
    Linux-x86_64)  ZARCH="x86_64-linux" ;;
    Linux-aarch64) ZARCH="aarch64-linux" ;;
    *) echo "!! no Zig $ZIG_VERSION mapping for $(uname -s)-$(uname -m); pass ZIG=/path/to/zig" >&2; exit 1 ;;
  esac
  echo ">> fetching Zig $ZIG_VERSION ($ZARCH)"
  curl -fSL --retry 2 "https://ziglang.org/download/$ZIG_VERSION/zig-$ZARCH-$ZIG_VERSION.tar.xz" -o "$BUILD/zig.tar.xz"
  rm -rf "$BUILD/zig" && mkdir -p "$BUILD/zig"
  tar xf "$BUILD/zig.tar.xz" -C "$BUILD/zig" --strip-components=1
  rm -f "$BUILD/zig.tar.xz"
  ZIG="$BUILD/zig/zig"
fi
echo ">> zig $("$ZIG" version) at $ZIG"

# --- 2. Pinned ghostty source, always reconciled to the exact commit ---------
GHOSTTY="$VENDOR/ghostty"
if [ ! -d "$GHOSTTY/.git" ]; then
  git init -q "$GHOSTTY"
  git -C "$GHOSTTY" remote add origin "$GHOSTTY_REPO"
fi
if [ "$(git -C "$GHOSTTY" rev-parse -q --verify HEAD 2>/dev/null)" != "$GHOSTTY_COMMIT" ]; then
  echo ">> fetching ghostty @ $GHOSTTY_COMMIT"
  git -C "$GHOSTTY" fetch -q --depth 1 origin "$GHOSTTY_COMMIT"
  git -C "$GHOSTTY" checkout -q -f FETCH_HEAD
fi
if [ "$(git -C "$GHOSTTY" rev-parse HEAD)" != "$GHOSTTY_COMMIT" ]; then
  echo "!! ghostty checkout is $(git -C "$GHOSTTY" rev-parse HEAD), wanted $GHOSTTY_COMMIT" >&2
  exit 1
fi
echo ">> ghostty $(git -C "$GHOSTTY" rev-parse --short HEAD)"

# --- 3. libghostty-vt for wasm32-freestanding --------------------------------
# This build emits TWO things: the static archive our shim links against, and
# ghostty's own wasm module (build.zig's GhosttyLibVt.initWasm). Both are kept
# so the report can say what the stock artifact is worth on its own.
echo ">> building libghostty-vt (wasm32-freestanding, ReleaseFast)"
( cd "$GHOSTTY" && "$ZIG" build -Demit-lib-vt=true -Dtarget=wasm32-freestanding -Doptimize=ReleaseFast )

cp "$GHOSTTY/zig-out/lib/libghostty-vt.a" "$BUILD/libghostty-vt.a"
if [ -f "$GHOSTTY/zig-out/bin/ghostty-vt.wasm" ]; then
  cp "$GHOSTTY/zig-out/bin/ghostty-vt.wasm" "$BUILD/ghostty-vt.wasm"
fi

# --- 4. Compile the shim and link it against the archive ---------------------
# The export list is derived from shim.c rather than repeated here, so the
# module's ABI and the source that defines it cannot drift apart. The last
# identifier before the first "(" on an EXPORT line is the symbol name.
# Sorted and deduplicated: two of the definitions in shim.c sit on either side
# of a #if/#else (the kitty pair), so a raw parse would list them twice and the
# count printed below would overstate what the module actually exports.
EXPORTS=()
while IFS= read -r sym; do EXPORTS+=("-Wl,--export=$sym"); done < <(
  awk '/^EXPORT /{ line=$0; sub(/\(.*/,"",line); n=split(line, parts, /[ \t]+/); sym=parts[n]; gsub(/[^A-Za-z0-9_]/,"",sym); print sym }' "$HERE/shim.c" |
    sort -u
)
if [ "${#EXPORTS[@]}" -lt 10 ]; then
  echo "!! only ${#EXPORTS[@]} exports parsed out of shim.c; aborting rather than shipping a stub" >&2
  exit 1
fi
for required in vt_new vt_inbuf vt_write_n vt_cell_select; do
  if ! printf '%s\n' "${EXPORTS[@]}" | grep -qx -- "-Wl,--export=$required"; then
    echo "!! export list is missing $required; the parse is broken" >&2
    exit 1
  fi
done

# Kitty graphics is force-disabled on freestanding targets, so those symbols
# are absent from the archive. Decide from the archive, not from the header.
KITTY=0
if grep -aq "ghostty_kitty_graphics_image" "$GHOSTTY/zig-out/lib/libghostty-vt.a"; then
  KITTY=1
fi
echo ">> wasm archive carries kitty graphics symbols: $KITTY"

# Scratch buffer sizes, in KiB. They are the shim's own share of every
# instance's linear memory, so they are a knob rather than a constant; the
# report measures both ends of it.
INBUF_KB="${INBUF_KB:-1024}"
OUTBUF_KB="${OUTBUF_KB:-1024}"
OUT="${OUT:-$BUILD/vt.wasm}"

echo ">> compiling shim.c -> $OUT (${#EXPORTS[@]} exports, inbuf ${INBUF_KB}K, outbuf ${OUTBUF_KB}K)"
# Remove the previous artifact first: a failed link must not leave an older
# module in place for the next run to measure as if it were the new one.
rm -f "$OUT"
"$ZIG" cc \
  -target wasm32-freestanding -O2 \
  -I "$GHOSTTY/include" \
  -DNOCX_HAVE_KITTY="$KITTY" \
  -DINBUF_KB="$INBUF_KB" -DOUTBUF_KB="$OUTBUF_KB" \
  -nostdlib -Wl,--no-entry \
  "${EXPORTS[@]}" \
  "$HERE/shim.c" "$GHOSTTY/zig-out/lib/libghostty-vt.a" \
  -o "$OUT"

# --- 5. Optionally the native archive, for the comparison measurement -------
# The same commit built for the host, so "the wasm build is X% slower" is a
# measurement rather than a claim. Most of the cost is this second full build,
# so it is opt-in; REPORT.md names the command either way.
if [ "${NATIVE:-0}" = "1" ]; then
  echo ">> building libghostty-vt (native, ReleaseFast) into zig-out-native/"
  ( cd "$GHOSTTY" && "$ZIG" build -Demit-lib-vt=true -Doptimize=ReleaseFast --prefix zig-out-native )
  echo ">> native archive: $GHOSTTY/zig-out-native/lib/libghostty-vt.a"
  echo ">> now: cd nativecmp && go build -o ../.build/nativecmp ."
fi

echo ">> done"
[ -f "$OUT" ] || { echo "!! $OUT was not produced" >&2; exit 1; }
ls -l "$OUT" "$BUILD/ghostty-vt.wasm" "$BUILD/libghostty-vt.a"
