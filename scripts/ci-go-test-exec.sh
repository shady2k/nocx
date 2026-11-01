#!/usr/bin/env bash
# cmd/go omits a non-1 TestMain exit status after output already exists.
# Preserve the actual binary exit as structured-converter output. Signals and
# nonstandard exits must not ride beside a completed quarantined test.
set -uo pipefail
"$@"
status=$?
printf 'NOCX_GO_TEST_EXIT %s\n' "$status"
exit "$status"
