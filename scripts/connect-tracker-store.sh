#!/usr/bin/env bash
# Connect the active worktree to this machine's tracker ref, then install the
# project policy into the recipe's published mechanism island.
set -euo pipefail

repo=$(git rev-parse --show-toplevel)
cd "$repo"
gate=.githooks/backlog-gate
mechanism="$gate/tracker-sync.mjs"
recipe="$gate/br-tracker.mjs"
policy="$gate/tracker-policy.yaml"
for path in "$mechanism" "$recipe" "$policy"; do
    [ -r "$path" ] || { printf 'tracker connect: %s is missing; run setup-shady2k-skills\n' "$path" >&2; exit 2; }
done
command -v node >/dev/null 2>&1 || { printf 'tracker connect: node is missing\n' >&2; exit 2; }
command -v git >/dev/null 2>&1 || { printf 'tracker connect: git is missing\n' >&2; exit 2; }
command -v br >/dev/null 2>&1 || { printf 'tracker connect: br is missing\n' >&2; exit 2; }

# tracker-sync refuses to embed a machine-specific state path into a hooks
# folder that will be committed. This project's hook folder is tracked, so use
# the per-machine external hooks folder and keep the project hook entrypoints
# as links there.
common=$(git rev-parse --git-common-dir)
case "$common" in /*) ;; *) common="$repo/$common" ;; esac
main_checkout=$(cd "$(dirname "$common")" && pwd -P)
machine_key=$(basename "$main_checkout")
state_base=${XDG_STATE_HOME:-"$HOME/.local/state"}
# A hooks folder is shared by every worktree of a repository when its path is
# keyed only by the machine. Symlinking one checkout's project hooks there makes
# another worktree execute the first checkout's hooks, and removing that
# checkout leaves dangling links Git silently skips. Key the folder by the
# checkout path as well, using git's own hash so no extra tool is needed.
checkout_key=$(printf '%s' "$repo" | git hash-object --stdin)
hooks="$state_base/$machine_key/hooks/$checkout_key"
mkdir -p "$hooks"
git config extensions.worktreeConfig true
git config --worktree core.hooksPath "$hooks"

machine=${NOCX_TRACKER_MACHINE:-$(hostname -s 2>/dev/null || hostname)}
node "$mechanism" connect --checkout "$repo" --machine "$machine" --prefix nocx --recipe "$recipe"

status=$(node "$mechanism" status --json --repo "$repo" --recipe "$recipe")
store=$(printf '%s' "$status" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const x=JSON.parse(s);if(!x.store)process.exit(2);process.stdout.write(x.store)})')
mkdir -p "$store/.tracker-sync" "$store/.beads"
install -m 0644 "$policy" "$store/.tracker-sync/policy.yaml"
install -m 0644 "$policy" "$store/.beads/policy.yaml"
node "$mechanism" publish --repo "$repo" --recipe "$recipe"

# tracker-sync owns the post-* hooks; the project still owns these local gates.
for hook in pre-commit commit-msg pre-merge-commit pre-push; do
    if [ -e "$hooks/$hook" ] && [ ! -L "$hooks/$hook" ]; then
        printf 'tracker connect: refusing to replace existing hook %s\n' "$hooks/$hook" >&2
        exit 1
    fi
    ln -sfn "$repo/.githooks/$hook" "$hooks/$hook"
done

br where --json >/dev/null
br sync --status
printf 'tracker connect: policy installed from %s and published with the tracker store\n' "$policy"
