#!/usr/bin/env bash
# Read-only tracker bootstrap for CI.
#
# CI has no write credentials: it must not register a machine ordinal, change
# hooks, or publish. This fetches refs/tracker/state, imports the recorded state
# into a temporary br database, and points this checkout's .beads at it. The
# adapter and commit check then read the store, and a gate can refuse a missing
# recording without inventing one.
set -euo pipefail

repo=$(git rev-parse --show-toplevel)
cd "$repo"
ref=refs/tracker/state
git fetch --no-tags origin "$ref:$ref"

store=${RUNNER_TEMP:-${TMPDIR:-/tmp}}/nocx-tracker-store
rm -rf "$store"
mkdir -p "$store/.beads"

git show "$ref:.beads/issues.jsonl" >"$store/.beads/issues.jsonl"
if git cat-file -e "$ref:.beads/beads.base.jsonl" 2>/dev/null; then
    git show "$ref:.beads/beads.base.jsonl" >"$store/.beads/beads.base.jsonl"
fi
git show "$ref:.beads/config.yaml" >"$store/.beads/config.yaml"
if git cat-file -e "$ref:.tracker-sync/policy.yaml" 2>/dev/null; then
    git show "$ref:.tracker-sync/policy.yaml" >"$store/.beads/policy.yaml"
else
    cp .githooks/backlog-gate/tracker-policy.yaml "$store/.beads/policy.yaml"
fi

(
    cd "$store"
    br --no-color --no-auto-import sync --import-only --rebuild >/dev/null
)

if [ -e .beads ] && [ ! -L .beads ]; then
    printf 'connect-tracker-ci: .beads is a real folder; refusing to replace it\n' >&2
    exit 1
fi
ln -sfn "$store/.beads" .beads
br where --json >/dev/null
printf 'connect-tracker-ci: tracker store imported for this run\n'
