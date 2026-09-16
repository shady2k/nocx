#!/usr/bin/env bash
# e2e/screenshot-board.sh — capture the nocx-9bpeq review board from a running
# `make dev-web`, in the e2e image (it carries the browsers Playwright pins; the
# host may have none). Output goes to the temp dir, never into the repository.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="nocx-e2e:local"
out="${TMPDIR:-/tmp}/nocx-screenshot-board/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$out"
if ! curl -fsS "http://127.0.0.1:${NOCX_WEB_PORT:-5180}/" >/dev/null; then
  echo "screenshot-board: no dev-web stand on 127.0.0.1:${NOCX_WEB_PORT:-5180} — run 'make dev-web' first" >&2
  exit 1
fi
docker build -q -f "$repo_root/e2e/Dockerfile" -t "$image" "$repo_root/e2e" >/dev/null
docker run --rm --network host --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -e BOARD_URL="http://127.0.0.1:${NOCX_WEB_PORT:-5180}/" -e BOARD_OUT=/out \
  -v "$repo_root/node_modules:/board/node_modules:ro" \
  -v "$repo_root/e2e/screenshot-board.mjs:/board/board.mjs:ro" \
  -v "$out:/out" -w /board "$image" node board.mjs
echo "$out/board.png"
