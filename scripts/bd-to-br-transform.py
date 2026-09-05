#!/usr/bin/env python3
"""Привести экспорт `bd` 1.1.0 к тому, что принимает `br` (beads_rust).

Ровно два расхождения, оба найдены на живой базе nocx (3399 issue) и оба
заставляют `br sync --import-only` упасть закрыто:

1. `comments[].id` — `bd` пишет UUIDv7-строку, `br` ждёт `i64`. В bd 0.46,
   с которым `br` сверял конформанс, id комментариев были целыми.
2. `external_ref` у `br` уникален. У нас `gh-pr-91` висел на пяти issue.
   Значение проигравших не выбрасывается, а уезжает в
   `metadata.external_ref_duplicate`, чтобы ссылка на PR не потерялась.

Памяти (`_type":"memory"`) `br` не понимает вообще — на них импорт падает с
`missing field id`. Они здесь отбрасываются и уходят своим маршрутом
(cass-memory); см. --memories-out.

    scripts/bd-to-br-transform.py bd-export.jsonl br-issues.jsonl \
        --memories-out .internal/memories-export.jsonl
"""

import argparse
import json
import sys


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("src", help="JSONL от `bd export --all`")
    p.add_argument("dst", help="куда положить JSONL для `br sync --import-only`")
    p.add_argument("--memories-out", help="куда сложить записи _type=memory")
    args = p.parse_args()

    comment_id = 0
    seen_refs: set[str] = set()
    dropped_refs: list[tuple[str, str]] = []
    memories = 0
    issues = 0

    mem_out = open(args.memories_out, "w") if args.memories_out else None
    with open(args.src) as src, open(args.dst, "w") as dst:
        for line in src:
            line = line.strip()
            if not line:
                continue
            d = json.loads(line)

            if d.get("_type") == "memory":
                memories += 1
                if mem_out:
                    mem_out.write(json.dumps(d, ensure_ascii=False) + "\n")
                continue

            for c in d.get("comments") or []:
                if isinstance(c.get("id"), str):
                    comment_id += 1
                    c["id"] = comment_id

            ref = d.get("external_ref")
            if ref:
                if ref in seen_refs:
                    md = d.get("metadata") or {}
                    md["external_ref_duplicate"] = ref
                    d["metadata"] = md
                    d["external_ref"] = None
                    dropped_refs.append((d["id"], ref))
                else:
                    seen_refs.add(ref)

            issues += 1
            dst.write(json.dumps(d, ensure_ascii=False) + "\n")

    if mem_out:
        mem_out.close()

    print(f"issue:              {issues}")
    print(f"id комментариев:    {comment_id} перенумеровано")
    print(f"памяти:             {memories} отложено в {args.memories_out or '(никуда)'}")
    print(f"external_ref снят:  {len(dropped_refs)}")
    for iid, ref in dropped_refs:
        print(f"    {iid}  {ref}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
