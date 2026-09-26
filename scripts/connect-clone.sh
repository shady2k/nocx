#!/bin/sh
# connect-clone.sh — connect this clone to the repository's hooks and backlog.
#
# `make connect` runs it, and `make init` runs it first. It is the one command a
# fresh clone needs before its first commit: it points git at .githooks, imports
# the tracked backlog into br's database, and checks every local input a hook
# reads. Everything missing is reported in one run, and nothing is connected
# until nothing is missing: a clone with hooks and no `br` would refuse every
# commit-message check with a reason nobody set out to learn.
#
# Safe to rerun.
set -u

missing=0
need() {
    printf 'connect: %s\n' "$1" >&2
    missing=1
}

command -v git >/dev/null 2>&1 || need "git is not on PATH"
command -v node >/dev/null 2>&1 ||
    need "node is not on PATH — the backlog and commit-link hooks run on it; install Node.js from https://nodejs.org/"
command -v br >/dev/null 2>&1 ||
    need "br is not on PATH — the commit-link hook resolves tasks through it; see README.md#agent-tooling"

gate=.githooks/backlog-gate
for f in pre-commit commit-msg pre-merge-commit pre-push; do
    [ -f ".githooks/$f" ] || need ".githooks/$f is missing from this checkout"
done
for f in adapter.mjs check.mjs time-format.mjs check-commits.mjs commit-links.mjs config.json; do
    [ -f "$gate/$f" ] || need "$gate/$f is missing from this checkout"
done
[ -f .beads/issues.jsonl ] || need ".beads/issues.jsonl is missing from this checkout"

if [ "$missing" = 1 ]; then
    printf 'connect: nothing was connected; supply the above and run make connect again\n' >&2
    exit 1
fi

node -e 'JSON.parse(require("fs").readFileSync(process.argv[1], "utf8"))' "$gate/config.json" || {
    printf 'connect: %s/config.json is not readable JSON\n' "$gate" >&2
    exit 1
}

git config core.hooksPath .githooks
# Left behind by the retired beads merge driver: a driver naming a binary that
# no longer exists fails every merge that touches the export.
git config --unset merge.beads-export.driver 2>/dev/null || true
git config --unset merge.beads-export.name 2>/dev/null || true

br sync --import-only || {
    printf 'connect: br could not import .beads/issues.jsonl\n' >&2
    exit 1
}

printf 'connect: hooks from .githooks, backlog imported, every hook input present\n'
