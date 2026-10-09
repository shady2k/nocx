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
command -v bash >/dev/null 2>&1 ||
    need "bash is not on PATH — pre-commit preserves staged filenames with NUL-delimited reads"
command -v node >/dev/null 2>&1 ||
    need "node is not on PATH — the backlog and commit-link hooks run on it; install Node.js from https://nodejs.org/"
command -v npm >/dev/null 2>&1 ||
    need "npm is not on PATH — root formatting and linting run on it"
command -v br >/dev/null 2>&1 ||
    need "br is not on PATH — the commit-link hook resolves tasks through it; see README.md#agent-tooling"

gate=.githooks/backlog-gate
for f in pre-commit commit-msg pre-merge-commit pre-push; do
    [ -r ".githooks/$f" ] || need ".githooks/$f is missing or unreadable"
done
for f in adapter.mjs check.mjs check-present.mjs check-docs.mjs check-product.mjs \
    check-commits.mjs time-format.mjs document-format.mjs merge-gate.mjs commit-links.mjs config.json; do
    [ -r "$gate/$f" ] || need "$gate/$f is missing or unreadable"
done
[ -r .beads/issues.jsonl ] || need ".beads/issues.jsonl is missing or unreadable"
[ -r .beads/config.yaml ] || need ".beads/config.yaml is missing or unreadable"
[ -r .beads/policy.yaml ] || need ".beads/policy.yaml is missing or unreadable"
[ -r scripts/check-site.sh ] || need "scripts/check-site.sh is missing or unreadable"
for f in package.json package-lock.json eslint.config.mjs .prettierrc.json .prettierignore \
    .githooks/check-control-goroutines.mjs .githooks/control-goroutines-baseline.json \
    .githooks/check-log-context.mjs .githooks/log-context-baseline.json \
    scripts/site-forbidden-phrases.txt; do
    [ -r "$f" ] || need "$f is missing or unreadable"
done

if [ "$missing" = 1 ]; then
    printf 'connect: nothing was connected; supply the above and run make connect again\n' >&2
    exit 1
fi

node -e 'JSON.parse(require("fs").readFileSync(process.argv[1], "utf8"))' "$gate/config.json" || {
    printf 'connect: %s/config.json is not readable JSON\n' "$gate" >&2
    exit 1
}
# Bootstrap only the root tooling; no frontend, Go or product build is needed.
if [ ! -x node_modules/.bin/prettier ] || [ ! -x node_modules/.bin/eslint ]; then
    printf 'connect: installing root formatting and linting tools from package-lock.json\n'
    npm ci --ignore-scripts --no-audit --no-fund || {
        printf 'connect: root tools could not be installed; hooks were not connected\n' >&2
        exit 1
    }
fi
br where >/dev/null 2>&1 || {
    printf 'connect: br has no usable tracker in this checkout\n' >&2
    exit 1
}

# Git silently skips readable hooks without executable permission. Repair only
# these four project-owned entry points before connecting the configuration.
if ! chmod +x .githooks/pre-commit .githooks/commit-msg \
    .githooks/pre-merge-commit .githooks/pre-push; then
    printf 'connect: hook executable permissions could not be repaired; hooks were not connected\n' >&2
    exit 1
fi
for f in pre-commit commit-msg pre-merge-commit pre-push; do
    [ -x ".githooks/$f" ] || need ".githooks/$f is not executable"
done
if [ "$missing" = 1 ]; then
    printf 'connect: nothing was connected; repair permissions and run make connect again\n' >&2
    exit 1
fi

# Import first: a failed tracker operation must never leave hooks connected.
br sync --import-only || {
    printf 'connect: br could not import .beads/issues.jsonl; hooks were not connected\n' >&2
    exit 1
}

old_hooks=$(git config --local --get core.hooksPath 2>/dev/null || :)
old_driver=$(git config --local --get merge.beads-export.driver 2>/dev/null || :)
old_name=$(git config --local --get merge.beads-export.name 2>/dev/null || :)
restore() {
    git config --local --unset-all core.hooksPath 2>/dev/null || :
    [ -n "$old_hooks" ] && git config --local core.hooksPath "$old_hooks"
    git config --local --unset-all merge.beads-export.driver 2>/dev/null || :
    [ -n "$old_driver" ] && git config --local merge.beads-export.driver "$old_driver"
    git config --local --unset-all merge.beads-export.name 2>/dev/null || :
    [ -n "$old_name" ] && git config --local merge.beads-export.name "$old_name"
}
unset_local() {
    if git config --local --get-all "$1" >/dev/null 2>&1; then
        git config --local --unset-all "$1"
    fi
}
if ! git config --local core.hooksPath .githooks ||
    ! unset_local merge.beads-export.driver ||
    ! unset_local merge.beads-export.name; then
    restore
    printf 'connect: git configuration failed; previous settings restored\n' >&2
    exit 1
fi

printf 'connect: hooks from .githooks, backlog imported, every hook input present\n'
