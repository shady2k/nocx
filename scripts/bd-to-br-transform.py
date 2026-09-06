#!/usr/bin/env python3
"""Make a `bd` 1.1.0 export something `br` (beads_rust) will accept.

Exactly two divergences, both found on the live nocx backlog (3401 issues) and
both of which make `br sync --import-only` fail closed:

1. `comments[].id` — `bd` writes a UUIDv7 string where `br` expects an `i64`.
   In bd 0.46, the version br checked its conformance against, comment ids were
   integers. 96 records here.
2. `br` requires `external_ref` to be unique. Ours had `gh-pr-91` on five
   issues. The losers' value is not thrown away — it moves to
   `metadata.external_ref_duplicate`, so the pointer at the PR survives.

Memories (`_type":"memory"`) `br` does not understand at all: the import dies on
them with `missing field id`. They are separated out here and take their own
route into cass-memory — see --memories-out and scripts/bd-memories-to-cass.py.

    scripts/bd-to-br-transform.py bd-export.jsonl br-issues.jsonl \
        --memories-out .internal/memories-export.jsonl
"""

import argparse
import json
import sys


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("src", help="JSONL from `bd export --all`")
    p.add_argument("dst", help="where to write the JSONL for `br sync --import-only`")
    p.add_argument("--memories-out", help="where to put the _type=memory records")
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

    print(f"issues:             {issues}")
    print(f"comment ids:        {comment_id} renumbered")
    print(f"memories:           {memories} set aside in {args.memories_out or '(nowhere)'}")
    print(f"external_ref clear: {len(dropped_refs)}")
    for iid, ref in dropped_refs:
        print(f"    {iid}  {ref}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
