#!/usr/bin/env bash
# Что брать следующим. Это вся очередь целиком — см. AGENTS.md.
#
# Правило то же, что было на `bd`: работа берётся из эпиков, которые кто-то уже
# взял, плюс standalone-баги, у которых эпика законно нет. Изменилась только
# механика. У `br ready` нет ни `--parent`, ни `--exclude-type`, а `ready --json`
# отдаёт голый массив без родителя, поэтому оба фильтра считаются здесь:
#
#   - дети взятого эпика — из `br show <epic> --json`, где `dependents` несёт
#     `dependency_type: "parent-child"`;
#   - «нет родителя» — из набора рёбер `parent-child` в `.beads/issues.jsonl`,
#     который читается один раз, а не по вызову `br show` на каждый issue.
#     На 3401 issue это 0.5 с против нескольких минут.
#
# Если вывод пуст — это ОТВЕТ, а не поломка: фронт каждого открытого эпика
# занят. Доделай начатое или возьми свободный эпик (`br ready -t epic --unassigned`).
# Не расширяй запрос.
set -euo pipefail

BEADS_DIR_RESOLVED=$(br where | head -1)
JSONL="$BEADS_DIR_RESOLVED/issues.jsonl"
PER_EPIC=${PER_EPIC:-5}

command -v jq >/dev/null 2>&1 || {
	echo "нужен jq" >&2
	exit 1
}

# Один экспорт на весь прогон: br пишет JSONL сам при каждой мутации, но если
# кто-то работал с --no-auto-flush, файл отстаёт от базы.
br sync --flush-only >/dev/null

# Готовые задачи и множество «у кого есть родитель» — на диск, а не в argv:
# оба переживают 3401 issue, а `jq --argjson` на такой строке падает с
# "Argument list too long".
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
br ready --json | jq -c 'map(select(.issue_type != "epic"))' >"$tmp/ready.json"
jq -r 'select(.dependencies) | .dependencies[]
	| select(.type == "parent-child") | .issue_id' "$JSONL" | sort -u >"$tmp/haveparent.txt"

echo "=== задачи внутри эпиков, которые кто-то взял ==="
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
echo "=== standalone-баги: ready, не эпик, без родителя ==="
jq -r --rawfile parents "$tmp/haveparent.txt" \
	'($parents | split("\n") | map(select(length > 0))) as $p
	 | map(select(.id as $i | ($p | index($i)) | not)) | .[] | "  \(.id)  \(.title)"' \
	"$tmp/ready.json"
