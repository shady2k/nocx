#!/usr/bin/env bash
#
# The geometry measurement: every capture in corpus/, replayed through all
# three emulators — x/vt, libghostty-vt and the xterm.js reference — whole and
# chunked, dumped column by column, then scored against xterm.js.
#
#   cd .internal/spikes/emulator && ./geometry.sh
#
# Needs the libghostty-vt build from run.sh step 2 (.vendor/ghostty/zig-out),
# go, node and npm. The xterm.js packages are installed into .vendor/node —
# inside the spike and gitignored — and never into the repository root: npm
# install walks UP to the nearest package.json, and reference/ has none, so an
# install from there rewrites the root package.json and package-lock.json.
set -euo pipefail
cd "$(dirname "$0")"

out=results/geometry
bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT

go build -o "$bin/geom" ./cmd/geom

mkdir -p "$out" .vendor/node
if [ ! -d .vendor/node/node_modules/@xterm/headless ]; then
  (cd .vendor/node && npm init -y >/dev/null && npm install --silent \
    @xterm/headless@5.5.0 @xterm/addon-unicode11@0.8.0)
fi
export NODE_PATH="$PWD/.vendor/node/node_modules"

# What produced the numbers, recorded rather than remembered.
{
  echo "# versions"
  echo "go $(go version | awk '{print $3}')"
  echo "node $(node --version)"
  echo "libghostty-vt $(git -C .vendor/ghostty rev-parse HEAD 2>/dev/null || echo 'no vendor checkout')"
  grep -E 'x/vt|ultraviolet|x/ansi' go.mod
  grep -E '@xterm/(headless|addon-unicode11)' .vendor/node/package.json
  echo "bash $(bash --version | head -1)"
  echo "htop $(htop --version | head -1)"
  echo "vim $(vim --version | head -1)"
  echo "less $(less --version | head -1)"
  echo "claude $(claude --version)"
} > "$out/versions.txt"

# The feed shapes. whole is the reference; the splits ask whether the final
# screen depends on where the writes fell, and bytewise is the worst case of
# that question. bash is 76 KiB — bytewise there is 76 000 writes for no
# additional question, so it is applied to the captures small enough to have
# been split mid-rune in practice.
modes=(whole split:2 split:3 split:5 split:8 split:16 split:32)

for capture in corpus/*.jsonl; do
  name=$(basename "$capture" .jsonl)
  bytes=$(wc -c < "$capture")
  echo "=== $name ($bytes bytes of JSONL)"
  for mode in "${modes[@]}"; do
    parts=${mode#split:}
    flag=()
    [ "$mode" = whole ] || flag=(--parts "$parts")
    "$bin/geom" replay -emu xvt -capture "$capture" -out "$out/$name.xvt.$mode.json" "${flag[@]}"
    "$bin/geom" replay -emu ghostty -capture "$capture" -out "$out/$name.ghostty.$mode.json" "${flag[@]}"
    node reference/geometry.mjs "$capture" "$out/$name.xterm.$mode.json" "${flag[@]}"
    if [ "$mode" = whole ] && [ "$bytes" -lt 65536 ]; then
      "$bin/geom" replay -emu xvt -capture "$capture" -out "$out/$name.xvt.bytewise.json" -bytewise
      "$bin/geom" replay -emu ghostty -capture "$capture" -out "$out/$name.ghostty.bytewise.json" -bytewise
      node reference/geometry.mjs "$capture" "$out/$name.xterm.bytewise.json" --bytewise
    fi
  done
  "$bin/geom" score -capture "$name" -dir "$out"
done

echo "done: $out/*.score.json"
