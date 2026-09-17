# Issue tracker: `br` (beads)

Issues and specs for this repo live in **`br`** — a single binary over SQLite plus a
tracked `.beads/issues.jsonl`. Not GitHub Issues, not markdown files.
[`AGENTS.md`](../../AGENTS.md) is the binding contract for how the backlog is used and it
wins over any skill; this file only records the command surface the engineering skills
need.

**`br` never runs git.** Every backlog write is published by hand, immediately — a create,
an edit, an edge, a close — not at session close:

```bash
br sync --flush-only          # db -> .beads/issues.jsonl
git add .beads/issues.jsonl   # commit it with the code it describes
```

## Conventions

- **Create an issue**: `br create "<title>" -t <task|bug|feature|epic|chore> -p <0-4> -l <area> --description-file -`
  (`-d` for a one-liner; `--description-file` takes a path or `-` for stdin, and survives
  shell quoting where `-d` does not). Every bead carries **exactly one area label** from
  the list in `AGENTS.md`.
- **Read an issue**: `br show <id>` (`--json` for machine-readable, which also carries the
  `rollup` of an epic's descendants; `br comments <id>` for the conversation).
- **List issues**: `br list --label <area> --status all`, `br list -t epic --unassigned`,
  `br list --json`. Area first — it is the only listing short enough to read whole.
- **Search**: `br search <phrase>` — title, description, id and comment text.
- **Comment**: `br comments <id> add "<text>"`.
- **Apply / remove labels**: `br label add <id> <label>` / `br label remove <id> <label>`,
  or `br update <id> --add-label <l> --remove-label <l>`. A label exists the first time it
  is applied; there is nothing to provision.
- **Claim work**: `br update <id> --claim` — atomic, setting assignee to the actor and
  status to `in_progress`. Stopping means `br update <id> --status open` **in the same
  minute**: an unheld bead in `in_progress` is invisible to `br ready`.
- **Close**: `br close <id> --reason "<evidence a stranger can check>"` — a file, a symbol,
  a test, a commit. "done" and "duplicate" are not reasons.
- **Dependencies**: `br dep add <blocked> <blocker>`. `blocked` is computed, never stored.

## What to work on next

```bash
br ready
```

Narrow with the binary's own flags — `--epic <id>`, `--parent <id>`, `-r/--recursive`, a
repeatable `-t/--type`, `-l/--label`, `--unassigned` — and never with a script around
them. If it returns nothing, that is an answer, not a bug. Never start a bead that is not
in it.

## When a skill says "publish to the issue tracker"

`br create …`, then `br sync --flush-only` and commit `.beads/issues.jsonl`.

## When a skill says "fetch the relevant ticket"

`br show <id>` plus `br comments <id>`.

## Wayfinding operations

Used by `/wayfinder`. The **map** is an epic bead; **children** are its tasks.

- **Map**: `br create "<effort>" -t epic -l <area>`. The Notes / Decisions-so-far / Fog
  body lives in its description (`br update <epic> --description-file -`).
- **Child ticket**: `br create "<question>" -t task --parent <epic> -l <area>`, with the
  wayfinder type as a second label (`wayfinder:research`, `:prototype`, `:grilling`,
  `:task`).
- **Blocking**: `br dep add <child> <blocker>`. Sequencing children with edges so only a
  few are ready at once is the house style — an epic is a DAG, not a bag.
- **Frontier query**: `br ready --epic <map>`. It already drops the blocked and the
  claimed. Do not reconstruct it from `br list`.
- **Claim**: an **epic** is assigned —
  `br update <epic> --assignee "$(git config user.email)" --status in_progress` — and a
  **task** is claimed (`br update <id> --claim`). Never `--claim` an epic.
- **Resolve**: `br comments <id> add "<answer>"`, then `br close <id> --reason "…"`, then
  append the context pointer to the map's Decisions-so-far.
