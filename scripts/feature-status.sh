#!/usr/bin/env bash
# Where a feature stands. See AGENTS.md, "A feature is a root epic".
#
# A feature is a root epic whose direct children are its stages, and every epic
# and bug on its path hangs under the stage it serves. This prints one block
# per stage, computed from the tracker, so it is exactly as true as those
# parent-child edges and cannot drift from them the way a written status can.
#
# Per stage:
#   - tasks closed of total: every non-epic descendant, bugs and chores included;
#   - in progress: what somebody is holding now;
#   - not decomposed: open epics with no children at all, the stage itself
#     included. Their size is UNKNOWN, which is not the same as small, so they
#     are listed rather than counted as zero;
#   - blocked by: open issues OUTSIDE the stage that block work inside it.
#
# Deferred descendants are left out of the counts: deferring is how the owner
# says "not part of this feature now".
set -euo pipefail

root=${1:?usage: scripts/feature-status.sh <root-epic-id>}

command -v jq >/dev/null 2>&1 || {
	echo "jq is required" >&2
	exit 1
}

BEADS_DIR_RESOLVED=$(br where | head -1)
JSONL="$BEADS_DIR_RESOLVED/issues.jsonl"

# One export for the whole run, for br-queue.sh's reason: somebody working with
# --no-auto-flush leaves the file behind the database.
br sync --flush-only >/dev/null

jq -rs --arg root "$root" '
	map(select(.status != "tombstone")) as $all
	| ($all | map({key: .id, value: .}) | from_entries) as $by
	| ($all | map(. as $i | ($i.dependencies // [])[]
		| select(.type == "parent-child") | {child: $i.id, parent: .depends_on_id})) as $pc
	| ($pc | group_by(.parent) | map({key: .[0].parent, value: map(.child)}) | from_entries) as $kids
	| def desc($id): ($kids[$id] // []) as $k | $k + ([$k[] | desc(.)] | add // []);
	  def live: map(select($by[.].status != "deferred"));
	  def line($id): "      \($id)  \($by[$id].title)";
	if $by[$root] == null then "no such issue: \($root)\n" | halt_error(1) else . end
	| ($kids[$root] // [] | live | sort_by($by[.].created_at)) as $stages
	| [$stages[] | . as $s
		| (desc($s) | live) as $d
		| ($d | map(select($by[.].issue_type != "epic"))) as $tasks
		| ([$s] + $d | map(select($by[.].issue_type == "epic"
			and $by[.].status != "closed" and (($kids[.] // []) | live | length) == 0))) as $bare
		| ($d | map(select($by[.].status == "in_progress"))) as $doing
		| ([$d[] | . as $i | ($by[$i].dependencies // [])[]
			| select(.type == "blocks") | .depends_on_id
			| select($by[.] != null and $by[.].status != "closed"
				and ($by[$i].status != "closed") and (. as $b | ([$s] + $d) | index($b) | not))]
			| unique) as $blockers
		| {
			id: $s,
			title: $by[$s].title,
			closed: ($tasks | map(select($by[.].status == "closed")) | length),
			total: ($tasks | length),
			done: ($by[$s].status == "closed"),
			$bare, $doing, $blockers
		}] as $rows
	| "\($by[$root].title)  (\($root))",
	  "stages done: \($rows | map(select(.done)) | length) of \($rows | length)"
	  + "   tasks closed: \($rows | map(.closed) | add // 0) of \($rows | map(.total) | add // 0)"
	  + "   not decomposed: \($rows | map(.bare | length) | add // 0)",
	  "",
	  ($rows[] |
		(if .done then "done"
		 elif .closed == 0 and (.doing | length) == 0 then "not started"
		 else "in progress" end) as $state
		| "\(.id)  [\($state)]  \(.closed) of \(.total) tasks closed",
		  "  \(.title)",
		  (if (.doing | length) > 0 then "    in progress:", (.doing[] | line(.)) else empty end),
		  (if (.bare | length) > 0 then "    not decomposed:", (.bare[] | line(.)) else empty end),
		  (if (.blockers | length) > 0 then "    blocked by, outside this stage:", (.blockers[] | line(.)) else empty end),
		  "")
' "$JSONL"
