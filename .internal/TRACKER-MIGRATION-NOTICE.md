# The tracker changed under you: `bd` → `br`. Read this before your next bead command.

2026-09-05. You are working in a worktree of `nocx`. Every worktree on this
machine shares ONE tracker store at `/home/dev/repos/nocx/.beads`, and that store
was migrated today from `bd` (Go beads, embedded Dolt) to **`br`**
([beads_rust](https://github.com/Dicklesworthstone/beads_rust)): SQLite plus a
tracked `.beads/issues.jsonl`.

**`bd` still runs and still answers.** That is the danger. Its Dolt store is still
on disk, so a `bd` command works and looks normal — and writes into a store nobody
reads any more. One close has already been lost that way and had to be recovered by
hand.

## Do this now

1. **Stop calling `bd`.** Not `bd ready`, not `bd close`, not `bd dolt push`.
2. **Use `br` instead.** Same verbs, same issue ids (`nocx-*` are unchanged, and so
   are every dependency, label and comment).
3. **Do not commit `.beads/` from your worktree.** You will not need to: `br`
   resolves the MAIN checkout's database, so your worktree's `git status` stays
   clean no matter how much you change the backlog. If `.beads/` ever shows up in
   your `git status`, do not stage it — say so instead.
4. **Finish your current task normally.** Nothing about your code work changes.

## The verbs you actually use

| `bd`                                       | `br`                                                |
| ------------------------------------------ | --------------------------------------------------- |
| `bd ready`                                 | `br ready`                                          |
| `bd ready` meaning "what should I do next" | `scripts/br-queue.sh`                               |
| `bd show <id>`                             | `br show <id>`                                      |
| `bd update <id> --claim`                   | `br update <id> --claim`                            |
| `bd close <id> --reason "..."`             | `br close <id> --reason "..."`                      |
| `bd create "T" -t task -p 1`               | `br create "T" -t task -p 1`                        |
| `bd dep add <child> <parent>`              | `br dep add <child> <parent>`                       |
| `bd search <phrase>`                       | `br search <phrase>`                                |
| `bd memories <word>`                       | `cm context "<what you are doing>" --json`          |
| `bd dolt push` / `bd dolt pull`            | nothing — the JSONL is a tracked file, git moves it |
| `bd merge-slot`                            | nothing — removed deliberately                      |

`br ready --json` returns a bare array, not `{issues: [...]}`; `br list --json` does
return `{issues, total, ...}`. `br ready` has no `--parent` and no `--exclude-type`,
which is why the queue is a script now.

## What does NOT change

Your worktree, your branch, your task, your commit rules. Issue ids are identical —
the migration preserved all 3401 of them along with 3332 dependency edges, 2794
labels and 96 comments, verified field by field.

## If you already ran `bd` today

Say so in your report, naming the bead and what you did to it. It is recoverable —
the coordinator reconciles `bd`'s export into `br` — but only if somebody knows to
look. Do not try to fix it yourself, and do not re-run the command against `br`
without saying so; a double close is harder to unpick than a missing one.

Full reasoning: `.internal/specs/2026-09-05-bd-to-br-migration-design.md`.
The rules live in `AGENTS.md`, which now carries the whole `bd` → `br` table.
