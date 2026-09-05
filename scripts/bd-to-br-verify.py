#!/usr/bin/env python3
"""Сверить бэклог в `br` с экспортом `bd` поштучно.

Гейт миграции: ни одного потерянного issue, ребра, метки или комментария и
ни одного расхождения в текстовых полях. Печатает таблицу и возвращает
ненулевой код, если хоть что-то разошлось.

    bd export --all -o /tmp/bd.jsonl
    scripts/bd-to-br-transform.py /tmp/bd.jsonl /tmp/br.jsonl
    br sync --flush-only
    scripts/bd-to-br-verify.py /tmp/br.jsonl .beads/issues.jsonl
"""

import argparse
import collections
import json
import sys

TEXT_FIELDS = (
    "title",
    "description",
    "acceptance_criteria",
    "close_reason",
    "design",
    "notes",
    "owner",
    "assignee",
    "created_at",
    "closed_at",
)


def load(path: str) -> dict[str, dict]:
    out: dict[str, dict] = {}
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            d = json.loads(line)
            if d.get("_type") == "memory":
                continue
            out[d["id"]] = d
    return out


def edges(m: dict[str, dict]) -> set[tuple]:
    s = set()
    for d in m.values():
        for e in d.get("dependencies") or []:
            s.add((e.get("issue_id"), e.get("depends_on_id"), e.get("type")))
    return s


def count(m: dict[str, dict], field: str) -> int:
    return sum(len(d.get(field) or []) for d in m.values())


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("expected", help="JSONL от bd, прогнанный через трансформер")
    p.add_argument("actual", help=".beads/issues.jsonl после br sync --flush-only")
    args = p.parse_args()

    a, b = load(args.expected), load(args.actual)
    bad: list[str] = []

    def check(name: str, want, got) -> None:
        ok = want == got
        mark = "OK  " if ok else "FAIL"
        print(f"{mark}  {name:<28} bd={want}  br={got}")
        if not ok:
            bad.append(name)

    check("issue", len(a), len(b))
    check("id потеряно", 0, len(set(a) - set(b)))
    check("id лишних", 0, len(set(b) - set(a)))

    ea, eb = edges(a), edges(b)
    check("рёбра зависимостей", len(ea), len(eb))
    check("рёбра потеряно", 0, len(ea - eb))
    for f in ("labels", "comments"):
        check(f, count(a, f), count(b, f))

    for field in ("status", "issue_type", "priority"):
        ca = collections.Counter(d.get(field) for d in a.values())
        cb = collections.Counter(d.get(field) for d in b.values())
        check(field, dict(sorted(ca.items(), key=str)), dict(sorted(cb.items(), key=str)))

    for field in TEXT_FIELDS:
        differ = [i for i in a if i in b and (a[i].get(field) or "") != (b[i].get(field) or "")]
        check(f"поле {field}", 0, len(differ))
        for i in differ[:3]:
            print(f"        {i}")

    if bad:
        print(f"\nРАСХОЖДЕНИЯ: {', '.join(bad)}")
        return 1
    print("\nСверка чистая.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
