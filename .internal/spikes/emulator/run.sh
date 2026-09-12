#!/usr/bin/env bash
#
# Reproduces every measurement in REPORT.md from a clean checkout.
#
# The toolchain is not installed: zig comes from nixpkgs (0.16.0, which is
# ghostty's build.zig.zon minimum_zig_version), and the reference probes need
# npm. Nothing is installed globally.
#
#   cd .internal/spikes/emulator && ./run.sh
#
# Output lands in results/. The raw driver output is the evidence the report
# quotes; results/*.txt is the xterm.js reference.
set -euo pipefail
cd "$(dirname "$0")"

GHOSTTY_REPO=https://github.com/ghostty-org/ghostty.git
GHOSTTY_COMMIT=e2e53f861482e080bf45054ba49ef471f9849937

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
mkdir -p results
go run ./cmd/xvt       > results/xvt.jsonl
go run ./cmd/ghosttyvt > results/ghostty.jsonl

# 4. The xterm.js reference — the product's own VT frontend per ADR-0001.
#    Run where the package can install:
#
#      cd reference && npm install @xterm/headless@5.5.0 @xterm/addon-unicode11@0.8.0
#      node xtermjs-cells.js       > ../results/xtermjs-cases.txt
#      node xtermjs-zwj-offsets.js > ../results/xtermjs-zwj.txt
#
# 5. The real-terminal check for the 0x9C case (REPORT.md §1.9) needs Xvfb,
#    xterm and xdotool, and a real terminal's own window title:
#
#      nix shell nixpkgs#xterm nixpkgs#xdotool nixpkgs#xvfb -c bash -c '
#        Xvfb :94 -screen 0 800x600x24 & sleep 2
#        DISPLAY=:94 xterm -T t -e sh -c "printf \"\\033]0;X\\342\\234\\263Y\\007\" >/dev/tty; sleep 8" &
#        sleep 5
#        for W in $(DISPLAY=:94 xdotool search --class xterm); do
#          DISPLAY=:94 xdotool getwindowname $W
#        done'
#
#    It printed X✳Y in three runs. Driving xterm's *input* this way does not
#    work here: there is no window manager, so XTEST keystrokes never reach
#    the window. See REPORT.md §6.

echo "done: results/xvt.jsonl results/ghostty.jsonl"
