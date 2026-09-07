#!/usr/bin/env bash
# What to take next. This is the whole queue — see AGENTS.md.
#
# The rule is the one `bd` had: work comes out of epics somebody has actually
# taken, plus standalone bugs, which legitimately have no epic. Only the
# mechanics changed. `br ready` has neither `--parent` nor `--exclude-type`, and
# its `--json` carries no parent, so both filters are computed here:
#
#   - an epic's children come from `br show <epic> --json`, whose `dependents`
#     carry `dependency_type: "parent-child"`;
#   - "has no parent" comes from the parent-child edge set in
#     `.beads/issues.jsonl`, read once rather than one `br show` per issue.
#     On 3401 issues that is 0.5 s against several minutes.
#
# If the output is empty, that is an ANSWER and not a breakage: every open
# epic's front is occupied. Finish something in flight or take a free epic
# (`br ready -t epic --unassigned`). Do not widen the query.
set -euo pipefail

BEADS_DIR_RESOLVED=$(br where | head -1)
JSONL="$BEADS_DIR_RESOLVED/issues.jsonl"
PER_EPIC=${PER_EPIC:-5}

command -v jq >/dev/null 2>&1 || {
	echo "jq is required" >&2
	exit 1
}

# One export for the whole run: br writes the JSONL on every mutation by
# itself, but somebody working with --no-auto-flush leaves the file behind the
# database.
br sync --flush-only >/dev/null

# The ready set and the "has a parent" set go to disk, not into argv: both
# survive 3401 issues, while `jq --argjson` on a string that size dies with
# "Argument list too long".
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
br ready --json | jq -c 'map(select(.issue_type != "epic"))' >"$tmp/ready.json"
jq -r 'select(.dependencies) | .dependencies[]
	| select(.type == "parent-child") | .issue_id' "$JSONL" | sort -u >"$tmp/haveparent.txt"

echo "=== tasks inside epics somebody has actually taken ==="
for epic in $(br list --type epic --status in_progress --json | jq -r '.issues[].id'); do
	br show "$epic" --json |
		jq -r '.[0].dependents[]? | select(.dependency_type == "parent-child") | .id' >"$tmp/kids.txt"
	[ -s "$tmp/kids.txt" ] || continue
	hits=$(jq -r --rawfile kids "$tmp/kids.txt" \
		'($kids | split("\n") | map(select(length > 0))) as $k
		 | map(select(.id as $i | $k | index($i))) | .[] | "  \(.id)  \(.title)"' \
		"$tmp/ready.json" | head -"$PER_EPIC")
	[ -n "$hits" ] && printf '%s:\n%s\n' "$epic" "$hits"
done

echo
echo "=== standalone bugs: ready, not an epic, no parent ==="
jq -r --rawfile parents "$tmp/haveparent.txt" \
	'($parents | split("\n") | map(select(length > 0))) as $p
	 | map(select(.id as $i | ($p | index($i)) | not)) | .[] | "  \(.id)  \(.title)"' \
	"$tmp/ready.json"
