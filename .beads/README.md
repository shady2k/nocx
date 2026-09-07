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

Gone on the owner's primary machine as of 2026-09-05: `embeddeddolt/` and `backup/`
were deleted once the git copy was proved to restore (3405 issues out of the
committed JSONL alone, in an empty directory). `.beads` went from 2.7 GB to 48 MB,
and `bd` now fails loudly here instead of writing into a store nobody reads.

**They still exist on any clone that has not migrated**, which is why
`.beads/.gitignore` still names them. Delete them there the same way, and only
after `br stats` on that clone agrees with everyone else's.

`refs/dolt/data` and `refs/beads/snapshot` are still on origin, deliberately, as a
cold second copy until every clone is on `br`. The first copy is the JSONL in git,
and it is the better one: recovery is `git checkout <commit> -- .beads/issues.jsonl`
followed by `br sync --import-only --rebuild`.

## Local history is bounded, and git is the real history

`.br_history/` keeps snapshots of the JSONL per machine and grew to 71 MB in one
migration day. Git holds the same history better, so keep this small:

```bash
br history list
br history prune --max-bytes 26214400     # 25 MB
export BR_HISTORY_MAX_BYTES=26214400      # or cap it up front, per machine
```

`.br_recovery/` holds quarantined sidecars from recovery paths and can be deleted
once whatever it was recovering from is resolved.

Full reasoning: `.internal/specs/2026-09-05-bd-to-br-migration-design.md`.
