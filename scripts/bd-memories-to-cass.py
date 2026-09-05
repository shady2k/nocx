#!/usr/bin/env python3
"""Перенести памяти `bd remember` в репозиторный плейбук cass-memory.

`br` не хранит памятей вообще — ни `remember`, ни `memories`, ни `recall`, а его
импорт падает закрыто на строке `_type":"memory"`. 144 памяти nocx уезжают сюда.

Пишем в `.cass/playbook.yaml`, а не через `cm playbook add`: тот всегда кладёт
правило в ГЛОБАЛЬНЫЙ `~/.cass-memory/playbook.yaml`, который живёт в домашнем
каталоге — значит памяти про nocx всплывали бы в чужих репозиториях, а коллега и
вторая машина не получили бы их вовсе. Репозиторный плейбук коммитится и едет с
клоном; это его прямое назначение, и `cm init --repo` так и говорит.

Изоляцию даёт РАСПОЛОЖЕНИЕ файла, а не поле `scope` внутри него — см. комментарий
у `scope` ниже, там это измерено.

Категория берётся из префикса самой памяти (`lesson (nocx, e2e): …`), потому что
`bd remember` уже писался в этой форме. Без префикса — `general`.

    scripts/bd-memories-to-cass.py .internal/memories-export.jsonl .cass/playbook.yaml
"""

import argparse
import datetime as dt
import json
import re
import sys

# Категории cass-memory; всё остальное сводится к general.
KNOWN = {
    "lesson": "workflow",
    "rule": "workflow",
    "process": "workflow",
    "playbook": "workflow",
    "recipe": "workflow",
    "feedback": "collaboration",
    "constraint": "architecture",
    "environment": "environment",
    "reference": "reference",
    "trap": "debugging",
    "gotcha": "debugging",
}

PREFIX = re.compile(r"^([a-z-]+)\s*\(")


def quote(s: str) -> str:
    """YAML double-quoted scalar: единственная форма, безопасная для нашего текста."""
    out = s.replace("\\", "\\\\").replace('"', '\\"')
    out = out.replace("\n", "\\n").replace("\r", "\\r").replace("\t", "\\t")
    return f'"{out}"'


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("src", help="JSONL с записями _type=memory")
    p.add_argument("dst", help=".cass/playbook.yaml")
    args = p.parse_args()

    now = dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z")
    bullets: list[str] = []
    by_category: dict[str, int] = {}

    with open(args.src) as f:
        for i, line in enumerate(f, 1):
            line = line.strip()
            if not line:
                continue
            d = json.loads(line)
            if d.get("_type") != "memory":
                continue
            key, value = d["key"], d["value"]

            m = PREFIX.match(value)
            category = KNOWN.get(m.group(1), "general") if m else "general"
            by_category[category] = by_category.get(category, 0) + 1

            bullets.append(
                "\n".join(
                    [
                        f"  - id: b-bd-{i:03d}",
                        f"    content: {quote(value)}",
                        f"    category: {category}",
                        "    kind: workflow_rule",
                        "    type: rule",
                        "    isNegative: false",
                        # `global`, а не напрашивающийся `workspace`, и это
                        # измерено: cass-memory 0.2.14 МОЛЧА исключает
                        # workspace-правила из `cm context` — при workspace
                        # запрос точной фразой из памяти отдаёт 0 совпадений,
                        # при global те же 144 находятся сразу. Утечки в чужие
                        # репозитории нет: `.cass/playbook.yaml` подхватывается
                        # по текущему каталогу, так что вне nocx этих правил не
                        # видно вовсе (проверено: внутри 145, снаружи 1).
                        # Изоляцию даёт расположение файла, а не это поле.
                        "    scope: global",
                        "    source: learned",
                        "    tags:",
                        f"      - {quote(key)}",
                        "      - bd-memory",
                        "    state: active",
                        "    maturity: established",
                        f"    createdAt: {now}",
                        f"    updatedAt: {now}",
                        "    sourceSessions:",
                        "      - bd-remember-migration-2026-09-05",
                        "    sourceAgents:",
                        "      - unknown",
                        "    helpfulCount: 0",
                        "    harmfulCount: 0",
                        "    feedbackEvents: []",
                        "    deprecated: false",
                        # Ничего не закрепляем и ничего не отключаем: правила
                        # входят в cass на общих основаниях и живут по её
                        # механике. pinned запрещает авто-депрекацию, а
                        # confidenceDecayHalfLifeDays делит не правило, а его
                        # feedbackEvents, которых у импортированной памяти нет —
                        # так что здесь обе величины штатные, ровно те, что
                        # пишет сам `cm playbook add`.
                        "    pinned: false",
                        "    confidenceDecayHalfLifeDays: 90",
                    ]
                )
            )

    header = "\n".join(
        [
            "# Правила этого репозитория для cass-memory.",
            "# Мержатся с глобальным ~/.cass-memory/playbook.yaml; репозиторные важнее.",
            "#",
            "# Здесь 144 памяти, перенесённые из `bd remember` 2026-09-05, когда трекер",
            "# сменился на br, у которого памятей нет. Сырой экспорт лежит рядом, в",
            "# .internal/memories-export.jsonl, и является источником при повторном прогоне",
            "# scripts/bd-memories-to-cass.py.",
            "schema_version: 2",
            "name: nocx-repo-playbook",
            "description: Правила и уроки nocx, купленные конкретными провалами",
            "metadata:",
            f"  createdAt: {now}",
            "  totalReflections: 0",
            "  totalSessionsProcessed: 0",
            "deprecatedPatterns: []",
            "bullets:",
        ]
    )

    with open(args.dst, "w") as out:
        out.write(header + "\n" + "\n".join(bullets) + "\n")

    print(f"правил записано: {len(bullets)}")
    for c, n in sorted(by_category.items(), key=lambda x: -x[1]):
        print(f"  {c:<14} {n}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
