#!/usr/bin/env python3
"""Move `bd remember` memories into this repository's cass-memory playbook.

`br` holds no memories at all — no `remember`, no `memories`, no `recall` — and
its import fails closed on a `_type":"memory"` line. The 144 nocx memories come
here instead.

We write `.cass/playbook.yaml` directly rather than calling `cm playbook add`,
because that command has no `--repo` and always writes
`~/.cass-memory/playbook.yaml`, which lives in the home directory: nocx memories
would surface in unrelated repositories, and the colleague and the second machine
would never see them at all. The repo playbook is committed and travels with the
clone — that is its stated purpose, and `cm init --repo` says so.

**This direct write is the one-off migration route, not the ongoing one.** A rule
written from now on goes through `cm playbook import rules.json --repo`, which is
the supported path and the only `playbook` subcommand that takes `--repo`. Running
this script again would overwrite the playbook with the 144 exported memories,
which is not what anybody wants after 2026-09-05 — see AGENTS.md on why they were
dropped — so it refuses a destination that already holds rules unless `--force`.

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
import os
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
    p.add_argument(
        "--force",
        action="store_true",
        help="overwrite a destination that already holds rules",
    )
    args = p.parse_args()

    # The destination is a tracked file that people write rules into by hand and
    # `cm` rewrites in place. Clobbering it with the 144 migrated memories is a
    # thing this script can only do on purpose.
    if not args.force and os.path.exists(args.dst):
        with open(args.dst) as existing:
            if "\n  - id:" in existing.read():
                print(
                    f"{args.dst} already holds rules; refusing to overwrite. "
                    "Pass --force if that is genuinely what you want.",
                    file=sys.stderr,
                )
                return 1

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

    # No comment header, however much this file would benefit from one: `cm`
    # reserialises the playbook from its own model whenever it writes a rule, and
    # comments do not survive that. A fifteen-line header explaining the file was
    # dropped without a word the first time a rule landed there. The explanation
    # lives in AGENTS.md instead.
    header = "\n".join(
        [
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
