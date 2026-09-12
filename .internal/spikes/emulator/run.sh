#!/usr/bin/env bash
#
# Reproduces the driver measurements in REPORT.md from a clean checkout: the
# pinned ghostty fetch, the libghostty-vt build, and both probe drivers.
#
#   cd .internal/spikes/emulator && ./run.sh
#
# The toolchain is not installed: zig comes from nixpkgs (0.16.0, which is
# ghostty's build.zig.zon minimum_zig_version). Nothing is installed globally.
#
# Steps 1-3 are the committed evidence and run unconditionally. Step 4 is the
# xterm.js reference and needs npm; it runs when npm is present. Step 5 is the
# live-xterm check, which is NOT automated here: it is unreliable under Xvfb on
# this machine and is recorded by hand in REPORT.md §1.9 with its failure modes
# in §6.
set -euo pipefail
cd "$(dirname "$0")"

GHOSTTY_REPO=https://github.com/ghostty-org/ghostty.git
GHOSTTY_COMMIT=e2e53f861482e080bf45054ba49ef471f9849937
DRIVER_TIMEOUT=${DRIVER_TIMEOUT:-600}

# 1. Vendored upstream source, at the exact commit the report measured. Not
#    evidence, not shipped. The checkout is verified rather than assumed, so a
#    run cannot silently measure a different ghostty than REPORT.md describes.
mkdir -p .vendor
if [ ! -d .vendor/ghostty/.git ]; then
  git init -q .vendor/ghostty
  git -C .vendor/ghostty remote add origin "$GHOSTTY_REPO"
fi
# GitHub serves a shallow fetch by sha. On a remote that refuses, this fails
# loudly here rather than quietly building the default branch.
git -C .vendor/ghostty fetch --depth 1 origin "$GHOSTTY_COMMIT"
git -C .vendor/ghostty checkout -q --detach FETCH_HEAD
git -C .vendor/ghostty log -1 --format='%H %ci %s'
if [ "$(git -C .vendor/ghostty rev-parse HEAD)" != "$GHOSTTY_COMMIT" ]; then
  echo "FATAL: checked out $(git -C .vendor/ghostty rev-parse HEAD)," \
       "report measured $GHOSTTY_COMMIT" >&2
  exit 1
fi

# 2. Build libghostty-vt: static archive + shared object + public headers.
#    ~85 s wall on the machine the report measured.
nix shell nixpkgs#zig -c \
  sh -c 'cd .vendor/ghostty && zig build -Demit-lib-vt=true -Doptimize=ReleaseFast'
ls -l .vendor/ghostty/zig-out/lib

# 3. The two drivers. Each writes one JSON observation per line to stdout.
#
#    The timeout is a backstop, not the probe: probe 7 bounds its own REP case
#    internally and reports "completed: false" when the budget expires. This
#    exists so that a pathological implementation cannot hang the run before it
#    has written any evidence — stdout is a file, so a killed driver still
#    leaves the observations it had already emitted, and the warning says so
#    rather than letting a truncated file pass for a complete one.
mkdir -p results
run_driver() {
  local name="$1" pkg="$2" out="$3"
  set +e
  timeout "$DRIVER_TIMEOUT" go run "$pkg" > "$out"
  local rc=$?
  set -e
  case "$rc" in
    0) ;;
    124)
      echo "WARNING: $name hit the ${DRIVER_TIMEOUT}s driver timeout;" \
           "$out is PARTIAL, not a complete result" >&2 ;;
    *)
      echo "FATAL: $name exited $rc" >&2
      exit "$rc" ;;
  esac
  wc -l "$out"
}
run_driver xvt       ./cmd/xvt       results/xvt.jsonl
run_driver ghosttyvt ./cmd/ghosttyvt results/ghostty.jsonl

# 4. The xterm.js reference — the product's own VT frontend per ADR-0001, and
#    the authority for the column-geometry rows in §1.1 and §4. Needs npm.
#
#    It installs into a temporary directory outside the repository and is run
#    through NODE_PATH, because `npm install` walks UP from the current
#    directory to find a package.json: running it in reference/ has no manifest
#    of its own, found the repository root's, and rewrote the root
#    package.json and package-lock.json. A spike must not do that.
if command -v npm >/dev/null 2>&1; then
  XJS_HOME=$(mktemp -d)
  (
    cd "$XJS_HOME"
    npm init -y >/dev/null 2>&1
    npm install --no-audit --no-fund --silent \
      @xterm/headless@5.5.0 @xterm/addon-unicode11@0.8.0
  )
  NODE_PATH="$XJS_HOME/node_modules" node reference/xtermjs-cells.js \
    > results/xtermjs-cases.txt
  NODE_PATH="$XJS_HOME/node_modules" node reference/xtermjs-zwj-offsets.js \
    > results/xtermjs-zwj.txt
  rm -rf "$XJS_HOME"
  wc -l results/xtermjs-cases.txt results/xtermjs-zwj.txt
else
  echo "npm not found: skipping the xterm.js reference (REPORT.md §1.1, §4)" >&2
fi

# 5. The live-xterm title check is manual. It needs Xvfb, xterm and xdotool,
#    and a real terminal's own window title:
#
#      nix shell nixpkgs#xterm nixpkgs#xdotool nixpkgs#xvfb -c bash -c '
#        Xvfb :94 -screen 0 800x600x24 & sleep 2
#        DISPLAY=:94 xterm -T t -e sh -c "printf \"\\033]0;X\\342\\234\\263Y\\007\" >/dev/tty; sleep 8" &
#        sleep 5
#        for W in $(DISPLAY=:94 xdotool search --class xterm); do
#          DISPLAY=:94 xdotool getwindowname $W
#        done'
#
#    It printed X✳Y in three runs. Driving xterm's *input* the same way does not
#    work here: there is no window manager, so XTEST keystrokes never reach the
#    window, and the keystroke-free DSR route returned zero bytes. REPORT.md §6.

echo "done: results/xvt.jsonl results/ghostty.jsonl"
