# `bd` (Go beads + Dolt) gives way to `br` (beads_rust, SQLite + JSONL)

Date: 2026-09-05
Status: designed, executed and verified on the owner's primary machine. The second
machine and the colleague are still to do — see [What is left](#what-is-left).
Session bead: `nocx-mmptx`

## The problem

The backlog store — embedded Dolt — cost more than it returned, and its failures
amplified each other. This is written down in the open P0 `nocx-v48vl` and measured
there:

- `bd dolt pull` could not catch up a clone that had fallen behind. Its read path
  forked `git cat-file blob <sha>` per object, 20–40 a second, and re-read the same
  multi-megabyte blobs. At 881 issues and ~55 commits behind it made no measurable
  progress in eight minutes.
- `.dolt/git-remote-cache` never pruned. Every aborted attempt left another
  near-identical ~210 MB pack: one pack at 09:00 became nine and 1.9 GB by evening,
  and `bd dolt push` degraded from 3 seconds to over 300. **Failure made the next
  failure more expensive.**
- `bd dolt pull` ignored SIGTERM. `timeout` without `--kill-after` reported 124 and
  returned while the process kept the store's exclusive lock; four sessions were
  found queued behind one such orphan, the oldest at 85 minutes.

Adjacent: `nocx-akfl6` (the local Dolt store lost 881 issues, 284 of them live) and
`nocx-wj4` (sync was one-way — nothing pulled, so a colleague's backlog was only as
fresh as the last time they remembered to type `bd dolt pull`).

On disk: **1.4 GB** of `embeddeddolt` plus **502 MB** of `backup`.

## What `br` is

A Rust port of beads, frozen at the "classic" SQLite + JSONL architecture. The
binary is `br`, to keep it apart from `bd`. Jeffrey Emanuel wrote it with Steve
Yegge's endorsement, because his own tooling is built around exactly that
architecture while upstream beads moves toward GasTown.

The behavioural difference that matters: **`br` never runs git.** Mutating commands
write SQLite and auto-export `issues.jsonl`; ordinary commands check whether the
JSONL got newer and import it first. Committing and pushing are ours.

## What was measured on live data

2026-09-05, `br` 0.5.10 (musl, checksum verified), against an export of the working
`bd` 1.1.0 database (3401 issues, 144 memories).

### The import is complete and exact

```
{"created":3401,"updated":0,"skipped":0,"tombstone_skipped":0,
 "orphans_removed":0,"blocked_cache_rebuilt":true}      # 17.7 s
```

Round-trip check, `bd export` against `br sync --flush-only`:

|                                                                                                                                  | `bd` | `br`                      |
| -------------------------------------------------------------------------------------------------------------------------------- | ---- | ------------------------- |
| issues                                                                                                                           | 3401 | 3401                      |
| ids lost / extra                                                                                                                 | —    | 0 / 0                     |
| dependency edges                                                                                                                 | 3330 | 3330                      |
| labels                                                                                                                           | 2791 | 2791                      |
| comments                                                                                                                         | 96   | 96                        |
| statuses / types / priorities                                                                                                    | —    | identical, count by count |
| `title`, `description`, `acceptance_criteria`, `close_reason`, `design`, `notes`, `owner`, `assignee`, `created_at`, `closed_at` | —    | 0 differences             |

**The `nocx-*` ids survive.** `br` rewrites a prefix only on an explicit
`--rename-prefix`, and mixed prefixes are supported. Every reference to `nocx-*` in
commits, in `.internal/plans/`, in `AGENTS.md` and in the beads themselves stays
valid.

### The `bd` export needs exactly two fixes

Both found on live records, both mechanical, both done by
`scripts/bd-to-br-transform.py`:

1. **`comments[].id`** — `bd` 1.1.0 writes a UUIDv7 string where `br` expects an
   `i64` (comment ids were integers in bd 0.46, which `br` checked its conformance
   against). 96 records. `br` fails closed:
   `invalid type: string "01a04a05-…", expected i64`.
2. **`br` requires `external_ref` to be unique.** One group: `gh-pr-91` on five
   issues (`nocx-a0qhd.8`, `nocx-a0qhd.6`, `nocx-6ftmj`, `nocx-zs278`,
   `nocx-a0qhd.7`). `br` fails closed: `Duplicate external_ref: gh-pr-91`. The
   losers keep the value in `metadata.external_ref_duplicate`.

### `br` will not take memories at all

A `_type":"memory"` line kills the import: `Invalid JSON at line 3400: missing
field 'id'`. There is no `remember`, no `memories`, no `recall`. The 144 memories
had to leave the tracker — that is a requirement, not an oversight.

### `br` understands git worktrees

This falsified the design's biggest assumed risk. From a worktree that has its own
checked-out `.beads/` in the tree (our case: `config.yaml` and `metadata.json` were
committed), `br where` answers with the **main** checkout's path:

```
$ cd ../brtrial-wt && br where
/…/brtrial/.beads
  database: /…/brtrial/.beads/beads.db
  jsonl:    /…/brtrial/.beads/issues.jsonl
```

Its own `.gitignore`, written by `br init`, carries a `redirect` entry for exactly
this. One database per machine, shared by all ~40 worktrees — as it was under `bd`.

### Branches do not touch the file

The second thing that decides the design, and also measured rather than reasoned.
An agent in a worktree created an issue and edited another:

```
$ br create "work from a worktree" -t task -p 2
✓ Created nocx-s1840
$ git status --porcelain          # in the worktree
                                  # ← empty
$ ls -l ../brtrial/.beads/issues.jsonl
9153090                           # ← the write landed in the main checkout
```

`br` writes the main worktree's database, and there is no hook to stage the file.
A feature branch therefore cannot change `issues.jsonl`, and the conflict from
`nocx-5xgrn` cannot recur: it needs both sides to have changed the file.

### 8.8 MB in the history costs 1 KB per commit

Ten backlog commits (create, edit notes, close, reopen, export), after `git gc`:

```
baseline .git:                    2984 KB
after 10 backlog commits:         2996 KB
per commit:                          1 KB
```

The JSONL is sorted by id, so only changed lines differ and delta compression is
nearly perfect.

### Speed and size

|                       | `bd` + Dolt            | `br`                        |
| --------------------- | ---------------------- | --------------------------- |
| store                 | 1.4 GB + 502 MB backup | 13 MB SQLite + 8.8 MB JSONL |
| `ready`               | —                      | 0.23 s                      |
| import of 3401 issues | —                      | 17.7 s                      |

## The decisions

### 1. `.beads/` goes back into git

Exactly as `br` documents. The three objections to this were measured above and none
held: worktrees are safe, branches do not touch the file, and the history grows 1 KB
per commit.

- `.beads/issues.jsonl` left `.gitignore`.
- `.beads/.gitignore` is now the one `br init` generates (`*.db*`, the fsqlite
  sidecars, `.br_history/`, `.br_recovery/`, `beads.base.jsonl` and the other merge
  artefacts, `redirect`, `.write.lock`), plus the Dolt leftovers.
- Down: `git pull`, then `br sync --import-only` (usually automatic).
- Up: `br sync --flush-only`, `git add .beads/issues.jsonl`, commit.
- Divergence between machines: `br sync --reconcile-additive`, whose plan is bound
  to a `plan_sha256`, deletes nothing and does not rewrite the JSONL.

**Explicitly out of scope:** a dedicated branch or a separate repository for the
backlog. Both would solve a problem the measurements show we do not have.

### 2. Memories move to cass-memory

They are `.cass/playbook.yaml`, a tracked file, generated from the raw export in
`.internal/memories-export.jsonl` by `scripts/bd-memories-to-cass.py`. Read them
with `cm context "<task>" --json`.

Two things measured while doing it, both counter-intuitive:

- The rules carry `scope: global` rather than the obvious `workspace`, because
  **`cm context` silently returns nothing for workspace rules** — an exact phrase
  from a memory matched 0 with `workspace` and all 144 with `global`. They do not
  leak: `.cass/` is found from the working directory, so `cm playbook list` shows
  145 rules inside nocx and 1 outside.
- Nothing is `pinned` and nothing is exempted. `confidenceDecayHalfLifeDays` decays
  a rule's `feedbackEvents`, of which an imported memory has none, so the field does
  nothing here whatever it says.

Adding a memory means appending to the JSONL and re-running the generator — never
`cm playbook add`, which writes the per-user global playbook nobody else sees.

### 3. The merge slot is dropped

`bd merge-slot` went with `bd` and gets no replacement. `scripts/merge-slot.sh` is
deleted and the AGENTS.md section with it. The owner's call. What it guarded still
exists, and AGENTS.md now says so plainly — including that the slot was never a lock
either, by that file's own account.

### 4. `beads-superpowers` stays, overridden through `AGENTS.md`

Its process skills are worth more than its `bd` half costs. By the plugin's own rule
repository instructions win over skills, so AGENTS.md carries the whole `bd` → `br`
table and that table is the instruction.

### 5. The queue is a script now

`br ready` has no `--parent` and no `--exclude-type`, and `ready --json` carries no
parent. `scripts/br-queue.sh` computes both: an epic's children from `br show <epic>
--json` (`dependents` carry `dependency_type: "parent-child"`), and "no parent" from
the parent-child edge set read once out of `.beads/issues.jsonl`. 3.9 s over 3401
issues.

`br show --json` also returns a `rollup` of descendants by status, which replaces the
`jq max` over children that AGENTS.md used for "is this epic actually live".

### 6. Deleted

`.githooks/beads-hook.sh` entirely, with `post-merge` and `post-rewrite`: git moves
the backlog now, and `br` imports it before the next command. `pre-push` pushes
nothing and only warns when code is about to leave without the backlog — warns,
never blocks, for the reason this repository already recorded about gates people
learn to pass with `--no-verify`. Also gone: `scripts/merge-slot.sh`, the two hook
tests, `.beads/hooks/`, `PRIME.md`, `export-state.json`, `interactions.jsonl`, and
the Dolt keys in `.beads/config.yaml`.

Deleted on the primary machine the same day, once the git copy was proved to
restore: 3405 issues came back out of the committed `.beads/issues.jsonl` alone, in
an empty directory. `.beads` went from 2.7 GB to 48 MB, and `bd` now answers
`no beads database found` instead of writing where nobody reads. `refs/dolt/data`
and `refs/beads/snapshot` stay on origin as a cold second copy until every clone is
on `br`; the first copy is the JSONL in git, and it is the better one.

### 7. Tooling per machine

| What                         | How                                                                                        |
| ---------------------------- | ------------------------------------------------------------------------------------------ |
| `br` 0.5.10                  | `install.sh --dest ~/.local/bin --skip-skills --verify` (musl, static)                     |
| the agent-instruction plugin | `/plugin marketplace add Dicklesworthstone/beads_rust`, `/plugin install beads@beads-rust` |
| `cass`                       | the prebuilt `cass-linux-x86_64.tar.gz`; rustup is not needed                              |
| `cm`                         | `install.sh --easy-mode --verify` (a bun binary, runs through nix-ld)                      |
| `minisign`                   | **into the NixOS system config** — verifies `br`'s release signatures                      |
| `sqlite3`                    | **into the NixOS system config** — for reading the database by hand                        |

`nix-ld` is enabled and `/lib64/ld-linux-x86-64.so.2` is present, so glibc binaries
run. `bd` comes off the system config last, once `br` has run for a week.

## What actually happened, including the part that went wrong

Executed in this order on the primary machine: transform and verify, `br init`,
import, clean-up of the layout, hooks and documentation, memories, then the worker
notice.

**`br init` inherited a database name from `bd`.** It read the old
`metadata.json` (`"database": "dolt"`) and created its SQLite in a file literally
named `dolt`. Caught immediately, and the fix is to write both `metadata.json` and
`config.yaml` in `br`'s own shape before importing, not after.

**Workers kept using `bd`, and it kept working.** The Dolt store is still on disk,
so a `bd` command from any of the ~40 worktrees succeeds and looks normal while
writing where nobody reads. One close was lost that way — `nocx-rowqt.10`, closed in
`bd` at 16:42:13Z, 36 minutes after the migration export — and `bd` also
auto-committed `sync.remote` back into `.beads/config.yaml`. Both were recovered:
re-exporting `bd` through the transform and importing it into `br` upserts by
`updated_at`, which restored the close with its original reason and touched nothing
else (`{"created":0,"updated":1,"skipped":3401}`).

The lesson is the one this repository already knows in other forms: **a migration
that leaves the old tool executable has not finished.** Removing `embeddeddolt` is
what actually ends it, and until then the notice in
`.internal/TRACKER-MIGRATION-NOTICE.md` is the only thing standing between a worker
and a silent write into a dead store.

## What is left

1. **Take `bd` off the NixOS system config**, on all three machines. This matters
   more than deleting data: while the binary is on PATH, a worker in any of the ~40
   worktrees can call it out of habit. It is one line, and reversible.
2. **The second machine and the colleague.** `git pull`, install the tooling,
   `br sync --import-only`, then delete their own `embeddeddolt` and `backup`.
   Gate: `br stats` agrees across all three clones.
3. **Delete `refs/dolt/data` and `refs/beads/snapshot` on origin** once (2) holds
   and a week has passed uneventfully.
4. **Close `nocx-v48vl`** noting that the defect left with the store.
5. **A `SessionStart` hook for `cm context`**, if the memories turn out to be missed
   in practice. Deliberately not built yet: what a session start does not know is
   the task, and injecting 144 rules blind is noise.

## Acceptance criteria

- `br stats` agrees on all three clones, and every one of the 3401+ ids starts with
  `nocx-`.
- `scripts/bd-to-br-verify.py` prints no divergence across issues, edges, labels,
  comments and the ten text fields. **Met on the primary machine.**
- An agent in a worktree creates and closes a bead; `git status` there stays empty
  and the change appears in the main checkout's `.beads/issues.jsonl`. **Met.**
- A PR from a branch where backlog work happened arrives on GitHub with no conflict
  in `.beads/issues.jsonl`.
- `cm context` returns, for three real queries, the memories `bd memories` found for
  the same words. **Met** (headless e2e, keychain, docker in pre-commit).
- No call to `bd` is left in the tree (`grep -rn '\bbd '` over hooks, scripts,
  `Makefile`, `AGENTS.md`, `CLAUDE.md`, `README.md`). **Met** — the only remaining
  mentions are historical explanation and the `bd` → `br` table.
- `.beads/embeddeddolt` and `.beads/backup` are gone and `du -sh .beads` is under
  50 MB. **Met on the primary machine: 48 MB**, after also pruning `.br_history`,
  which had reached 71 MB in a single migration day.

## Deliberately not done

- **A dedicated branch or repository for the backlog** — it would solve a problem
  the measurements show does not exist.
- **A replacement for the merge slot** — the owner's decision to drop it.
- **Forking `beads-superpowers`** — overriding it through `AGENTS.md` instead.
- **Editing the historical documents** in `.internal/plans/` and `.internal/briefs/`
  that mention `bd`. That is an archive; it describes what was.
- **The author's own `bd-to-br-migration` skill** — it rewrites documentation only,
  assumes classic bd without Dolt, and does not mention either of the two format
  divergences we actually hit.
