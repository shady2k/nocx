#!/usr/bin/env bash
# Run one command, keep its output and its exit status for the quarantine gate,
# and leave the step green either way.
#
#   NOCX_TEST_LOG_DIR  the directory the job's gate step reads (required)
#   scripts/ci-run-logged.sh <name> <command> [args...]
#
# The helper returns zero for an ordinary test result so every suite runs.
# Its own infrastructure and evidence writes MUST fail the Actions step.
# The final gate checks the workflow's positive expected-step manifest and the
# completion seals. A killed helper, failed tee or missing status cannot pass.
# Go commands emit JSON; Playwright reports are copied into the completion seal
# when NOCX_QUARANTINE_REPORT names their file. Only the gate judges test reds.
#
# The status is the FIRST command in the pipeline, not tee's: `pipefail` is set
# for exactly that, and PIPESTATUS is read on the very next line so nothing can
# overwrite it. bash 3.2 — still /bin/bash on the macOS runner — has both.
set -uo pipefail

name=${1:?usage: ci-run-logged.sh <name> <command> [args...]}
shift
: "${NOCX_TEST_LOG_DIR:?NOCX_TEST_LOG_DIR must name the directory the gate reads}"

case "$name" in
  ''|*[!a-zA-Z0-9_-]*) echo "invalid evidence step name" >&2; exit 2 ;;
esac
set -e
mkdir -p "$NOCX_TEST_LOG_DIR"
# Never reuse completion evidence left by an earlier invocation.
rm -f "$NOCX_TEST_LOG_DIR/$name.complete" "$NOCX_TEST_LOG_DIR/$name.status"
if [ -n "${NOCX_QUARANTINE_REPORT:-}" ]; then
  rm -f "$NOCX_QUARANTINE_REPORT"
fi
if [ -n "${NOCX_QUARANTINE_WRAPPER_STATUS:-}" ]; then
  rm -f "$NOCX_QUARANTINE_WRAPPER_STATUS"
fi
set +e
"$@" 2>&1 | tee "$NOCX_TEST_LOG_DIR/$name.log"
status=("${PIPESTATUS[@]}")
set -e
if [ "${status[1]}" -ne 0 ]; then
  echo "could not write test log evidence" >&2
  exit "${status[1]}"
fi
printf '%s\n' "${status[0]}" >"$NOCX_TEST_LOG_DIR/$name.status"
node "$(dirname "$0")/quarantine-seal.mjs" "$NOCX_TEST_LOG_DIR" "$name"
exit 0
