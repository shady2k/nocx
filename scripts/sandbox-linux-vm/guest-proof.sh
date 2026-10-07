#!/bin/bash
set -euo pipefail

lane=${1:?lane required}
cd /ci/repo
case "$lane" in
  source)
    go run ./scripts/sandbox-smoke-linux source
    go run ./scripts/sandbox-smoke-linux packaged
    ;;
  packaged)
    go run ./scripts/sandbox-smoke-linux packaged
    ;;
  *)
    printf 'invalid sandbox VM lane: %s\n' "$lane" >&2
    exit 1
    ;;
esac
