# Backlog: how work is tracked here

Written by `/setup-shady2k-skills` on 2026-09-18. The skills of the set read this
file first and nothing else about the tracker. Edit it freely; re-run the
installer only to change the tracker or the gate's strength.

## The protocol

**Levels.** Each has one size and one author.

| level     | what it is                                                        | size             |
| --------- | ----------------------------------------------------------------- | ---------------- |
| Vision    | where this is going; a document, never an issue                   | rarely rewritten |
| Milestone | a slice with a charter: what is in, what is out, a finding budget | what ships next  |
| Feature   | a root issue holding one **outcome**                              | several stages   |
| Stage     | an issue under a feature, with its own DONE WHEN                  | a few sessions   |
| Task, bug | a leaf                                                            | one session      |

An **outcome** is what becomes possible or true, and who observes it: "a person
creates a connection group", "a rollback is one command and one minute". Its
DONE WHEN stops being false exactly once, and names the check that watches it.

**The horizon.** Only the current milestone is decomposed below features. The
next one exists as feature titles; anything further is the vision's business.
**Everything live belongs to the current milestone**: its root wears the
milestone's label. Whatever does not is deferred.

**Ready** means a ready **leaf**: a task or a bug nobody holds and nothing
blocks. Features and stages are finished, never taken.

**Two lanes outside the flow.**

- **Ideas**: deferred, no parent, no edges, a review date. Never in the ready
  queue, closed without regret.
- **Findings**: bugs and debt found mid-milestone. The charter declares a budget
  for them. A finding beyond the budget goes to the next milestone or displaces
  something by the owner's explicit decision; it never goes silently to the
  front.

**Edges.** A blocking edge records a collision, two issues changing the same
thing, and sits on the leaf that collides. Importance is priority; "later" is a
milestone. Provenance is never a blocker.

**Status is true.** Active means somebody is holding it now. Stopping means
releasing it in the same minute.

**Names, not identifiers.** Everything a person reads says "Title" (id), the id
in parentheses and only where somebody must act on it. A title is a sentence
the work can be understood from.

## This project

- **Vision:** [`docs/vision.md`](../vision.md)
- **Milestone charters:** `docs/milestones/<milestone label>.md` — the directory
  does not exist yet; `/to-milestone` writes the first one.
- **Current milestone:** read `currentMilestone` from the gate config, never from here.
- **Evidence that may be cited at close:** a commit, a test, a file or a symbol —
  the same bar AGENTS.md sets, "evidence a stranger can check". "Duplicate" and
  "done" are not reasons, and a file on `main` is not a feature in the product:
  where the claim is that something works, the evidence is the check that
  watched it work.
- **Features and stages are created as:** `epic` (`br create -t epic`). The gate
  looks for a DONE WHEN on that type only. Beads also has a `feature` type; it
  maps to `other` and is invisible to the criterion check, so do not use it for
  a stage.
- **Labels:** every live issue, features and stages included, wears exactly one
  area label from AGENTS.md's list. Ideas wear `idea`, findings wear `finding`.
  `mvp` and `phase-1/2/3` are the roadmap axis this backlog predates the
  milestones with; they are still in the vocabulary and are orthogonal to both.

## The gate

- **Config:** `.githooks/backlog-gate/config.json`
- **Adapter:** `.githooks/backlog-gate/adapter.mjs`
- **Rules:** `.githooks/backlog-gate/check.mjs` — vendored byte for byte from
  the shady2k-skills plugin and never edited here. Its `--selftest` lives with
  the plugin, because the fixtures do:
  `node <plugin>/skills/backlog/setup-shady2k-skills/check.mjs --selftest --config .githooks/backlog-gate/config.json`
  is what proves the copy's rules, and `diff <(sed '2,6d' .githooks/backlog-gate/check.mjs) <plugin copy>` proves it is the copy.
- **Strength:** `block-new`, chosen 2026-09-18. Only a violation this commit
  introduces is red; the debt already there is printed every time and fails
  nothing.
- **Run it:**

  ```bash
  G=.githooks/backlog-gate
  node $G/adapter.mjs --at HEAD > /tmp/backlog-baseline.json
  node $G/adapter.mjs --at :0 |
    node $G/check.mjs --config $G/config.json --baseline /tmp/backlog-baseline.json -
  ```

  `--at :0` is the STAGED export and `--at HEAD` the last committed one. Never
  the working tree: `br` rewrites `.beads/issues.jsonl` on almost every command,
  and several worktrees on this machine write the same database, so a
  working-tree read judges backlog writes this commit is not making. The hook in
  `.githooks/pre-commit` runs exactly the two lines above; CI's `ci-backlog` job
  runs them against the merge base.

- **Every violation as JSON:** add `--json` to the `check.mjs` line.
- **Ages from before a bulk edit:** add `--ages-from <snapshot>`. Snapshots so
  far: the deferral of 2026-09-17T19:23Z, which rewrote 799 timestamps in one
  minute — the export from before it is at commit `0f7f5c22`, so

  ```bash
  node $G/adapter.mjs --at 0f7f5c22 > /tmp/backlog-ages.json
  ```

  is the snapshot. Measured 2026-09-18: it turns 7 stale edges into 9 and leaves
  every other check where it was.

**The gate is clean** when its report says `new errors: 0`. That line means the
same under every strength, which red and green do not: under `report` nothing
is ever red, and under `block` old debt is always red. Every skill that writes
to the backlog runs the gate **before** publishing, and a new error is fixed by
its own `fix` line before anything leaves this machine.

## Tracker verbs

How this project's tracker does each thing the skills ask for. `br` is a single
binary over SQLite plus the tracked JSONL export, it never runs git, and it
resolves the MAIN checkout's database even from a worktree — so these commands
answer the same in every worktree, and only the last one publishes.

| verb      | how                                                                                                                                                                                                                                                                                                                                                  |
| --------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| create    | `br create "<title>" -t epic\|task\|bug\|chore -l <area> -d "<body>" [--parent <id>] [-p 0-3]`                                                                                                                                                                                                                                                       |
| link      | `br dep add <blocked> <blocker>` (type `blocks`, the default) · parent: `br dep add <child> <parent> -t parent-child` · provenance is `-t discovered-from` and the adapter drops it, so it gates nothing                                                                                                                                             |
| claim     | `br update <id> --claim` — assignee and `in_progress` in one write. A claim is not a lock: two clones can claim the same bead, last write wins                                                                                                                                                                                                       |
| release   | `br update <id> --status open`, in the same minute you stop holding it                                                                                                                                                                                                                                                                               |
| close     | `br close <id> -r "<evidence a stranger can check>"`                                                                                                                                                                                                                                                                                                 |
| comment   | `br comments add <id> "<text>"` (`br comments <id>` lists them)                                                                                                                                                                                                                                                                                      |
| edit      | `br update <id> --title "…" -d "…"` · move: `br dep remove <id> <old parent>` then `br dep add <id> <new parent> -t parent-child`                                                                                                                                                                                                                    |
| unlink    | `br dep remove <issue> <depends-on>`                                                                                                                                                                                                                                                                                                                 |
| defer     | `br defer <id> [--until 2026-10-01]` — the review date is the `--until`; without one it is deferred indefinitely                                                                                                                                                                                                                                     |
| undefer   | `br undefer <id>`                                                                                                                                                                                                                                                                                                                                    |
| milestone | a label: `br label add <root id> v0-5`. Beads has no milestone field, and the gate reads the label on the issue's ROOT                                                                                                                                                                                                                               |
| ready     | `br ready` · leaves only: `br ready -t task -t bug` · within one epic: `br ready --epic <id>`. It hides what a blocked or deferred ANCESTOR holds, which the normalized model does not — see below                                                                                                                                                   |
| holds     | `br list --status in_progress` (the assignee is on each row)                                                                                                                                                                                                                                                                                         |
| children  | `br show <id> --json \| jq '.[0].rollup'` — derived, so it cannot drift from the children the way a copied timestamp can                                                                                                                                                                                                                             |
| label     | `br label add <id> <label>` · `br label remove <id> <label>` · `br label list-all` for the tree's whole vocabulary with counts                                                                                                                                                                                                                       |
| search    | `br search "<phrase>"` (title, body, comments) · one area whole: `br list --label <area> --status all`                                                                                                                                                                                                                                               |
| show      | `br show <id>` — body, labels, edges, comments                                                                                                                                                                                                                                                                                                       |
| publish   | `br sync --flush-only && git add .beads/issues.jsonl && git commit && git push`. Nothing does this for you, and an unpublished bead does not exist for anybody else. **Never `br sync --merge` on a database that is merely ahead** — it tombstones every issue the JSONL has not seen; `--reconcile-additive` is the one that adds without deleting |

**Where the adapter and `br ready` disagree, and why neither is wrong.**
Measured 2026-09-18 on 3866 issues: `br ready` listed 29, the normalized model's
open-and-unblocked-by-anything-live 44, and nothing `br` called ready was
missing from the model — which is the test that no provenance edge is being read
as a dependency. All 15 of the difference are `br` hiding a leaf its ANCESTOR
blocks: 6 under a blocked parent, 8 parents with unclosed children, and
`nocx-ms7v.4` under a deferred parent. The model keeps status and edges exactly
as the tracker stores them and derives nothing, deliberately — the rules ask
about issues, and a queue is `br ready`'s job.
