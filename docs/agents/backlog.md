# Backlog integration

Maintained by `/shady2k-skills:setup-shady2k-skills`; last reconciled to the skill set
0.22.0 on 2026-09-23 by its setup task, "The backlog tooling here matches shady2k-skills
0.22.0 and is proved from main" (nocx-xtf0b). The protocol itself ships with the skills
and is not restated here. This file holds the project's facts and the commands that were
run and seen to work. Changing choices — strength, milestone, budgets, scope and execution
settings — live only in the config. No installation state is recorded anywhere: the
`--version` of the three installed checks on `main` is the repository's installation, and
each person's plugin and hooks are their own.

AGENTS.md wins over this file wherever they disagree; the disagreement is then this file's
bug.

## Project

- **Config:** [`.githooks/backlog-gate/config.json`](../../.githooks/backlog-gate/config.json).
  Read `currentMilestone`, `findingBudget`, `scope` and `execution` there, never here.
- **Scope: team** (owner, 2026-09-19). The rules bind everyone who works here: `make init`
  connects the hooks in every clone, CI's `ci-backlog` enforces them on every pull request,
  and `.claude/settings.json` offers the plugin to anyone who trusts the folder in Claude
  Code. AGENTS.md carries the install line for people who do not have it yet.
- **Vision and roadmap:** [`docs/vision.md`](../vision.md); its §6 "Strategic roadmap" is the
  roadmap. Status comes from the tracker (`scripts/feature-status.sh <feature>`), never from
  either document.
- **Milestone charters:** `docs/milestones/<milestone label>.md`, written by
  `/shady2k-skills:to-milestone`. The first is [`v0-5`](../milestones/v0-5.md).
- **Current specifications:** none yet in the capability format. The nearest current-state
  documents are [`docs/architecture.md`](../architecture.md) (binding invariants AD-1…AD-10)
  and [`contracts/`](../../contracts/) (every JSON-RPC result shape, checked over the wire).
  Neither is a behavioural spec by capability, and coverage is unknown outside them.
- **Changes:** design specs in `.internal/specs/<date>-<topic>-design.md`; a small change
  lives in its bead's body.
- **Document resources:** the plugin's `templates/` and `documents.md`, by the plugin's
  path; nothing is copied in until the document gate is installed.
- **Workflow ownership:** `br` owns task status. `beads-superpowers` is kept for its process
  skills only (AGENTS.md, "This file wins over a skill"). ADRs are the decision records.
- **Architecture and explorations:** `docs/architecture.md`; explorations are not retained
  as documents unless asked.
- **Glossary and decisions:** AGENTS.md is the `CONTEXT.md`; decisions are ADRs in
  [`docs/decisions/`](../decisions/), never edited once accepted.
- **Acceptance records:** on the stage bead, as a comment: base and final revision, the
  tasks included, the criterion, the full-check, mutation and review evidence, and what is
  left pending.
- **Features and stages:** `epic` (`br create -t epic`), and only that type: the gate looks
  for a DONE WHEN on epics. Beads' `feature` type maps to `other` and is invisible to the
  criterion check. A feature is a root epic; its stages are child epics (AGENTS.md, "A
  feature is a root epic").
- **Labels:** every live issue wears exactly one area label from AGENTS.md's list. Ideas
  wear `idea`, findings `finding` plus the milestone label they were filed in.

### Submitted and implemented

Beads has neither status, and AGENTS.md forbids a label that restates a field, so both are
**comments with a fixed first word** on an issue that stays `in_progress`. That keeps it
out of `br ready`. The adapter reads the latest such comment:

```text
submitted: <revision> -- <local-check evidence>      # worker result, awaiting merge
implemented: <revision> -- <related-check evidence>  # merged into the stage, awaiting acceptance
reopened: <why>                                      # cancels the marker above it
```

Writing one: `br comments add <id> "submitted: 1a2b3c4 -- go build ./... and go vet ./internal/x"`.
When a worker submits, the coordinator takes the assignee (`br update <id> --actor <coordinator>
--assignee <coordinator>`), so the result is held for merging and no worker holds it.
Without both halves — a revision, `--`, then evidence — the gate reports
`submitted-without-evidence` / `implemented-without-evidence`. It reports the same
rule when the marker sits on a leaf that is **not under a stage**: measured 2026-09-21
on the setup task, which hangs under a chore, so its proof record is a plain comment.
A setup, grooming or charter task outside any stage therefore records its evidence
plainly and never as a marker. Proved 2026-09-19 on a
scratch tracker through claim → submitted → implemented → reopened → implemented → close.

### Commit task links

The convention is AGENTS.md's "Every commit names its bead". What is read: every `nocx-…`
id inside parentheses in the **header paragraph**, meaning the lines up to the first blank
line. A subject that wraps carries its ids onto its second line, and 5 of 593 commits in
the week before 2026-09-19 did. Ids in the body are references and are not read. Each id
must be an existing **leaf**, closed ones included. An epic, or anything with children, is
refused. 76 of those 593 commits named a stage or an epic, almost all `chore(beads)`
decompositions, and that work now gets its own task.

- **Revert:** git keeps the reverted subject in quotes, ids included, so it links to the
  same tasks.
- **Container, here and in the dependency rule, are two different words.** This check
  refuses any id that is somebody's parent, whatever became of the children: a decomposed
  task never becomes nameable by a commit again, and the work gets its own leaf. The
  backlog gate's `nonleaf-dependency` reads it the other way since 0.22.0 — a parent whose
  children have all closed is a leaf again and may carry an edge, because what remains is
  in the issue itself. Neither was relaxed towards the other; they answer different
  questions.
- **Merge:** a merge naming nothing is linked by the commits it brings in (between its
  first parent and itself). Each of those is checked on its own too. GitHub's
  "Merge pull request #N" passes this way, not as an exemption. A merge that brings in
  nothing and names nothing fails.
- **Before the rule:** commits committed before `commitLinksFrom` (config) are listed as
  such and fail nothing. So are merges bringing in only such commits. The committer date is
  the author's to set, so this is honesty, not enforcement.

### Cleanup recovery

The 2026-09-19 reconciliation released five epics held `in_progress` since 2026-09-15 with
nothing in their trees moving. Each one's status went to `open`, its assignee stayed as
owner, and it got a comment naming the rollback. The field-level journal is on the setup
task:

| epic                                                                             | before        | after  |
| -------------------------------------------------------------------------------- | ------------- | ------ |
| An agent nocx has never seen is described by the person who runs it (nocx-dhq4u) | `in_progress` | `open` |
| A wave of workers runs inside nocx (nocx-dkawo)                                  | `in_progress` | `open` |
| Two machines, one session, the same screen (nocx-eidfb)                          | `in_progress` | `open` |
| Every local pane is the helper's (nocx-ie23r)                                    | `in_progress` | `open` |
| The coordinator outlives its window (nocx-p2q1q)                                 | `in_progress` | `open` |

Rollback of any one: `br update <id> --status in_progress`. It touches only the status and
keeps every later change.

## Checks and execution

- **Backlog adapter:** [`adapter.mjs`](../../.githooks/backlog-gate/adapter.mjs) reads the
  tracked export, never the database. `--at <rev>` reads any revision (`:0` is the staged
  copy), `--export <path>` reads any file, and with neither it reads the working tree. It
  maps statuses, emits `holder` (assignee) and `createdAt`, drops tombstones, and reads the
  markers above. Provenance edges are dropped; only `blocks` gates.
- **Rules:** [`check.mjs`](../../.githooks/backlog-gate/check.mjs),
  [`check-commits.mjs`](../../.githooks/backlog-gate/check-commits.mjs) and
  [`check-docs.mjs`](../../.githooks/backlog-gate/check-docs.mjs) are byte-for-byte copies
  of the shady2k-skills plugin's `skills/backlog/setup-shady2k-skills/` at 0.22.0, never
  edited here. Proving it: `cmp` each against the plugin copy, `--version` prints `0.22.0`,
  and the selftests run from the plugin directory because the fixtures live there:
  `node check.mjs --selftest --config <repo>/.githooks/backlog-gate/config.json`,
  `node check-commits.mjs --selftest`, `node check-docs.mjs --selftest`.
- **Document gate: not installed yet.** Its task is "The document gate checks product
  intent, feature readiness and acceptance evidence on every change here" (nocx-q8yjf.2).
  Until it lands, document readiness is checked by reading, and every report says the
  automatic check did not run. No document policy, baseline, receipts or synchronization
  are wired, and nothing about documents is enforced by CI.
- **Document evidence level: records** (owner, 2026-09-19). The owner may push to `main`
  directly and CI cannot block that, so there is no protected CI to verify receipts
  against. When the document gate lands, acceptance evidence is the stage's acceptance
  record — each check's status with where its output is kept, and the owner's approval as
  their recorded words — trusted, not verified. No runner, receipt signing or forgery
  defences are built for it. Moving to `protected` later needs no document rewritten.
- **Checks follow what a change can touch** (owner, 2026-09-19). A `supporting` change —
  documents, `.beads/`, `.githooks/`, process tooling, nothing under product code — owes
  review and the tests of the tooling it changes, not the product suites and not
  `make ci-full`. In the document policy that is `appliesTo` on each required check.
  AGENTS.md "Git authority" carries the same exception.
- **Backlog gate**, `block-new`. The pre-commit hook (section 7) and CI both run it:

  ```bash
  G=.githooks/backlog-gate
  node $G/adapter.mjs --at HEAD > /tmp/backlog-baseline.json
  git show HEAD:$G/config.json > /tmp/backlog-baseline-config.json
  node $G/adapter.mjs --at :0 |
    node $G/check.mjs --config $G/config.json --baseline /tmp/backlog-baseline.json \
      --baseline-config /tmp/backlog-baseline-config.json -
  ```

  It compares the staged export against HEAD, never the working tree: `br` rewrites the
  export on almost every command. The baseline is judged by its own day's config, so a
  config change that creates violations is new. The hook runs only when the export or the
  gate itself is staged.

- **JSON report:** add `--json` to the `check.mjs` line.
- **Commit-link check:** [`commit-links.mjs`](../../.githooks/backlog-gate/commit-links.mjs)
  parses messages and calls `check-commits.mjs`. `--message <file>` checks a pending
  message; tasks come from the export `br where` names, which is the database's own view,
  so a bead created a minute ago resolves. `--range <base>..<head>` checks every commit the
  range introduces, with tasks from the export at `<head>`. An empty range is exit 2, never
  a pass.
- **Local entry points:** `.githooks/pre-commit` (backlog), `.githooks/commit-msg` (links),
  and `.githooks/pre-merge-commit`, which delegates to pre-commit. `make init` sets
  `core.hooksPath .githooks`, and that is the whole installation in a fresh clone.
- **CI:** `ci-backlog` in `.github/workflows/ci.yml`. The backlog baseline is the PR's merge
  base, or the push's `before`, or else `HEAD^`. The commit range is merge base..PR head
  (not GitHub's synthetic merge), or `before..HEAD` on a push, or `merge-base(origin/main)..HEAD`
  for a new branch. That last range is empty for a release tag or a manual run on a commit
  main already holds; the step says so and passes, because those commits were checked in
  their pull requests. An empty PR range is still an error.
  **CI does not run on a push to `main`** (the triggers are PRs, `release/**`, dispatch and
  release.yml's call). A direct push to `main` — the owner's, deliberately ungated — is
  checked by the local hooks only.
- **Bulk-edit age correction:** the deferral of 2026-09-17T19:23Z rewrote 799 timestamps.
  Its pair is `--ages-from <export at 6121375e> --ages-through <export at de4c58d5>`, the
  last export before it and the first after. A second cluster, 97 issues at 19:32Z, has no
  pair recorded; the gate prints it as a warning.
- **Static checks:** `.githooks/pre-commit` (seconds).
- **Related tests:** `go test -tags gtk3 ./<touched packages>`; `npx vitest run <touched
files>` in `frontend/`. **The worker runs them for what it touched** — the owner's decision
  of 2026-09-21, settling the contradiction between AGENTS.md "Git authority" and the
  earlier rule of 2026-09-14 in favour of AGENTS.md (`execution.workersRunTests: true`).
  The worker runs nothing else: the merged-tree gate, the containerized jobs and the e2e
  suite stay the coordinator's, for the three costs AGENTS.md records.
- **Full stage checks:** `make ci-full` on the merged tree, run by the coordinator once
  before pushing to `main` (AGENTS.md, "Git authority"), except for a supporting change,
  which owes review and its own tooling's tests (above).
- **Mutation checks:** no mutation tool is installed for Go or TypeScript. The agreed
  alternative is in the config (`execution.mutationFallback`): the coordinator plants 2–3
  mutations by hand in the changed logic at stage acceptance and records which tests went
  red. A skipped check is recorded as skipped, never as passed.
- **Reviewer:** `codex` (another model), one round at stage acceptance. It is available
  when `codex exec --skip-git-repo-check "<prompt>"` answers (checked 2026-09-19, codex-cli 0.154.0). The fallback is a
  fresh Claude agent, disclosed as the same model.
- **Parallel execution:** separate git worktrees, one per worker. The coordinator assigns
  leaves one at a time, with at most `execution.maxWorkers` workers and disjoint file sets.
  Generated files and lockfiles (`package-lock.json`, `go.sum`, `contracts/` generated
  types) count as collisions. The stage's coordinator integrates.

## Tracker operations

`br` is a single binary over SQLite plus the tracked export. It never runs git, and it
resolves the MAIN checkout's database even from a worktree. Its own docs: `br robot-docs
guide`, `br <command> --help`.

| operation                        | here                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| create                           | `br create "<title>" -t epic\|task\|bug\|chore -l <area> -d "<body with DONE WHEN for an epic>" [--parent <id>] [-p 0-3]`                                                                                                                                                                                                                                                                                                                                                                                           |
| link / unlink                    | `br dep add <consumer leaf> <producer leaf>` (type `blocks`), with the reason and what releases it in a comment · `br dep remove …` · parent: `-t parent-child` · provenance: `-t discovered-from`, which gates nothing                                                                                                                                                                                                                                                                                             |
| claim                            | `br update <id> --claim --actor <agent>`, always with `--actor`: without it `br` records `$USER`, the person. The holder is the agent doing the work, under its full name `<harness>-<role>:<person>@<machine>:<branch>#<session>` — a bare role names nobody, because several run at once on different machines and branches — and the person only for their own decisions, checks and approvals. Epics stay owned by the person (AGENTS.md). `claim_exclusive` refuses a claim while another actor holds the bead |
| release                          | `br update <id> --status open --assignee ""`: an unfinished hold only, in the minute it stops                                                                                                                                                                                                                                                                                                                                                                                                                       |
| submitted                        | `br comments add <id> "submitted: <rev> -- <evidence>"`, then the coordinator takes the assignee                                                                                                                                                                                                                                                                                                                                                                                                                    |
| implemented                      | coordinator only: `br comments add <id> "implemented: <merge rev> -- <related-check evidence>"`                                                                                                                                                                                                                                                                                                                                                                                                                     |
| reopen                           | `br comments add <id> "reopened: <why>"`, and recheck every issue blocked by it; a closed one: `br reopen <id>` first                                                                                                                                                                                                                                                                                                                                                                                               |
| close                            | `br close <id> -r "<accepted at <rev>: evidence a stranger can check>"`, only after stage acceptance, or with an explicit duplicate/cancellation reason naming the survivor                                                                                                                                                                                                                                                                                                                                         |
| comment / edit                   | `br comments add <id> "…"` · `br update <id> --title … --description-file <f>` · move: `br dep remove <id> <old>` then `br dep add <id> <new> -t parent-child`                                                                                                                                                                                                                                                                                                                                                      |
| defer / undefer                  | `br defer <id> --until <date>` · `br undefer <id>`                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| milestone / label                | `br label add <root> <milestone>` (read the value from config); `br label add\|remove <id> <label>`                                                                                                                                                                                                                                                                                                                                                                                                                 |
| ready                            | `br ready -t task -t bug` · in one stage: `br ready --epic <stage>`. br releases a dependant only when its prerequisite CLOSES, so an `implemented` prerequisite in the same stage is released by the coordinator by hand, in a checkout at or after its merge revision                                                                                                                                                                                                                                             |
| holds                            | `br list --status in_progress` (assignee on each row)                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| pending integration / acceptance | `br search "submitted:"` / `br search "implemented:"`, then confirm the latest marker with `br comments <id>`                                                                                                                                                                                                                                                                                                                                                                                                       |
| children                         | `br dep list <id> --direction up -t parent-child` (every status, closed included) · counts: `br show <id> --json \| jq '.[0].rollup'`                                                                                                                                                                                                                                                                                                                                                                               |
| search / show                    | `br search "<phrase>"` (title, body, comments) · `br list --label <area> --status all` · `br show <id>` · `br comments <id>`                                                                                                                                                                                                                                                                                                                                                                                        |
| publish                          | `br sync --flush-only`, copy or stage `.beads/issues.jsonl` in the checkout being committed, commit with a leaf id, push. **Never `br sync --merge` on a database that is merely ahead** (AGENTS.md)                                                                                                                                                                                                                                                                                                                |

**Where the model and `br ready` differ.** Measured 2026-09-18 and still true: `br ready`
also hides a leaf whose ANCESTOR is blocked or deferred, which the normalized model does
not derive. Nothing `br ready` lists is missing from the model. It lists one thing the
model calls a container, though: an open parent whose type is not `epic` — measured
2026-09-23, `nocx-q8yjf` is a `chore` with two live children and `br ready` offers it as
takeable work. `-t epic` cannot filter it out and `-t task -t bug` only hides it by
accident, so a container of any other type has to be read before it is taken. The model keeps status and
edges as stored; a queue is `br ready`'s job.

## Open

Nothing. The one entry here — who runs the related tests — was decided by the owner on
2026-09-21 and is recorded under "Checks and execution" above.
