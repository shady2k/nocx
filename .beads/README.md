# `.beads/` — the executable backlog

The tracker is **`br`** ([beads_rust](https://github.com/Dicklesworthstone/beads_rust)).
`br ready` to see work, `scripts/br-queue.sh` when the question is "what next", and
[`AGENTS.md`](../AGENTS.md) for the rules — including the `bd` → `br` table, because
the `beads-superpowers` plugin still speaks the old binary's name.

## What is in here, and what git carries

| File                        | Tracked | What it is                                                       |
| --------------------------- | ------- | ---------------------------------------------------------------- |
| `issues.jsonl`              | **yes** | The backlog itself. One JSON object per issue, sorted by id.      |
| `config.yaml`               | yes     | `issue_prefix: nocx`, and nothing else worth setting so far.      |
| `metadata.json`             | yes     | Which files in here are the database and the export.              |
| `.gitignore`                | yes     | Everything below, plus the leftovers named at the end.            |
| `beads.db` + its sidecars   | no      | SQLite, rebuilt from the JSONL by `br sync --import-only`.        |
| `beads.base.jsonl`          | no      | Merge base for `br sync --merge`. Per-clone, never shared.        |
| `.br_history/`              | no      | Bounded local snapshots of the JSONL.                             |

`issues.jsonl` is tracked on purpose, and that is a reversal — it was untracked on
2026-08-29 because a pre-commit hook regenerated and staged it on every commit, so
every branch touched it and pull requests conflicted. `br` removes the cause rather
than the file: nothing stages it, and `br` resolves the **main checkout's** database
even when run from a worktree that has its own `.beads/` in the tree. A feature
branch therefore cannot change it, and two branches cannot conflict over it.

## The database is disposable, the JSONL is not

```bash
br sync --import-only            # JSONL -> SQLite   (after a git pull; usually automatic)
br sync --flush-only             # SQLite -> JSONL   (before git add; usually automatic)
br sync --status                 # which way they are out of step
br sync --merge                  # both changed: three-way against beads.base.jsonl
br sync --import-only --rebuild  # JSONL is authoritative, rebuild the database to match
br doctor                        # when none of the above explains what you are seeing
```

Losing `beads.db` costs a `--import-only`. Losing `issues.jsonl` costs the backlog,
which is why it is the tracked half.

## Leftovers from `bd`

`embeddeddolt/` and `backup/` are the old Go tracker's Dolt store, about 1.9 GB.
They are ignored, kept only until nobody wants a rollback, and can be deleted
outright. See `.internal/specs/2026-09-05-bd-to-br-migration-design.md`.
