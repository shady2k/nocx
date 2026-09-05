#!/usr/bin/env python3
"""Move `bd remember` memories into this repository's cass-memory playbook.

`br` holds no memories at all — no `remember`, no `memories`, no `recall` — and
its import fails closed on a `_type":"memory"` line. The 144 nocx memories come
here instead.

We write `.cass/playbook.yaml` directly rather than calling `cm playbook add`,
because that command always writes `~/.cass-memory/playbook.yaml`, which lives in
the home directory: nocx memories would surface in unrelated repositories, and
the colleague and the second machine would never see them at all. The repo
playbook is committed and travels with the clone — that is its stated purpose,
and `cm init --repo` says so.

Isolation comes from WHERE the file is, not from the `scope` field inside it.
See the comment on `scope` below; it is measured.

The category is taken from the memory's own prefix (`lesson (nocx, e2e): …`),
because `bd remember` was already written in that shape. No prefix means
`general`.

    scripts/bd-memories-to-cass.py .internal/memories-export.jsonl .cass/playbook.yaml
"""

import argparse
import datetime as dt
import json
import re
import sys

# cass-memory categories; anything else collapses to general.
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
    """YAML double-quoted scalar: the one form that is safe for this text."""
    out = s.replace("\\", "\\\\").replace('"', '\\"')
    out = out.replace("\n", "\\n").replace("\r", "\\r").replace("\t", "\\t")
    return f'"{out}"'


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("src", help="JSONL carrying _type=memory records")
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
                        # `global`, not the obvious `workspace`, and this is
                        # measured: cass-memory 0.2.14 SILENTLY excludes
                        # workspace rules from `cm context` — an exact phrase
                        # out of a memory matched 0 with workspace and all 144
                        # with global. It does not leak into other
                        # repositories: `.cass/playbook.yaml` is found from the
                        # working directory, so outside nocx these rules are
                        # invisible (checked: 145 inside, 1 outside). The file's
                        # location is what isolates them, not this field.
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
                        # Nothing is pinned and nothing is switched off: these
                        # rules enter cass on ordinary terms and live by its
                        # mechanics. `pinned` would forbid auto-deprecation, and
                        # `confidenceDecayHalfLifeDays` decays a rule's
                        # feedbackEvents, which an imported memory has none of —
                        # so both are left exactly as `cm playbook add` writes
                        # them.
                        "    pinned: false",
                        "    confidenceDecayHalfLifeDays: 90",
                    ]
                )
            )

    header = "\n".join(
        [
            "# This repository's rules for cass-memory.",
            "# Merged with the global ~/.cass-memory/playbook.yaml; repo rules win.",
            "#",
            "# These are the memories carried over from `bd remember` on 2026-09-05,",
            "# when the tracker became br, which has no memory store. The raw export sits",
            "# beside them in .internal/memories-export.jsonl and is the source whenever",
            "# scripts/bd-memories-to-cass.py runs again.",
            "schema_version: 2",
            "name: nocx-repo-playbook",
            "description: nocx rules and lessons, each bought by a specific failure",
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

    print(f"rules written: {len(bullets)}")
    for c, n in sorted(by_category.items(), key=lambda x: -x[1]):
        print(f"  {c:<14} {n}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
