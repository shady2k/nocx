#!/usr/bin/env bash
# Take, release and inspect the merge slot with a holder a person can act on.
#
# `bd` resolves the holder as --holder, then $BEADS_ACTOR, then git user.name,
# then $USER. Nothing in this repo sets the first two, so every agent on this
# machine acquires as the same string and `check` can only say that SOMEONE is
# merging. That is not enough to decide anything: a coordinator meeting a held
# slot cannot tell its own stale hold from a colleague's live one, and the only
# way out is to ask a human (nocx-e3if5).
#
# The worktree directory and the branch are what a person decides on, and both
# are free to compute here. So the correct call is this script, and it is
# shorter than the bare one.
#
#   scripts/merge-slot.sh acquire [--wait]
#   scripts/merge-slot.sh release
#   scripts/merge-slot.sh check
#
# This changes what the slot REPORTS and nothing about what it enforces. A
# claim is not a lock; the slot shrinks the race, it does not close it.
set -euo pipefail

holder() {
	local dir branch
	dir=$(basename "$(git rev-parse --show-toplevel)")
	branch=$(git branch --show-current || true)
	printf '%s:%s' "$dir" "${branch:-detached}"
}

case "${1:-}" in
acquire)
	shift
	bd merge-slot acquire --holder "$(holder)" "$@"
	;;
release)
	shift
	bd merge-slot release "$@"
	;;
check)
	shift
	bd merge-slot check "$@"
	;;
*)
	echo "usage: $(basename "$0") {acquire [--wait]|release|check}" >&2
	exit 2
	;;
esac
