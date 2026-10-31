#!/usr/bin/env bash
# Keep the Makefile's allocation-budget selection, but ask its Go runner for
# structured evidence. This prefix is used only by the quarantined CI step.
set -euo pipefail
if [ "$1" != test ]; then
  echo 'the JSON runner accepts only go test' >&2
  exit 2
fi
shift
exec go test -json -exec "$PWD/scripts/ci-go-test-exec.sh" "$@"
