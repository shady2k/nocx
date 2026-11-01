# Backlog integration

Maintained by `setup-shady2k-skills`. The installed checks carry setup protocol
0.56.0 after this update lands. Setup task nocx-gkpwp owns the proof evidence
and any unperformed checks; protocol 0.51.0 was installed by nocx-fs95y.
Protocol 0.46.0 was installed by nocx-q8yjf.25,
whose record — with nocx-q8yjf.26–.28 and root nocx-nl23a — the published
export carries but this tracker's database has lost, so `br` cannot show them.
Protocol 0.44.0 was installed by nocx-q8yjf.24. A branch alone is not an
installed update. The protocol ships
with the skills and is not restated here. This file holds project facts and
commands; changing choices live only in the config. Each person's plugin,
runtime and connected hooks are checked directly, never represented as shared
installation-state settings.

AGENTS.md wins over this file wherever they disagree; the disagreement is then this file's
bug.

## Project

- **Product repository:** none. `product.mjs where` answers `none` here: nocx is an
  ordinary project, not a product repository and not a code repository under one.
- **Task ids of this repository's commits resolve against:** this repository's own
  tracker, read through the export `br where` names (the main checkout's
  `.beads/issues.jsonl`).
- **Config:** [`.githooks/backlog-gate/config.json`](../../.githooks/backlog-gate/config.json).
  Read `currentMilestone`, `findingBudget`, `scope` and `execution` there, never here.
- **Scope: team** (owner, 2026-09-19). The rules bind everyone who works here: `make connect`
  connects the hooks in every clone, CI's `ci-backlog` enforces them on every pull request,
  and `.claude/settings.json` offers the plugin to anyone who trusts the folder in Claude
  Code. AGENTS.md carries the install line for people who do not have it yet.
- **Jev: consented** (owner, 2026-09-28), route `openrouter`, with nocx's bead-id shape in
  the config's `jev.idPattern` so ids are masked. The owner was told that masking goes by
  shape, and that a customer name or log line pasted into a conversation has none and can
  leave unmasked. The key's place is each person's own (`~/.config/shady2k-skills/jev.json`);
  a person without one works without Jev.
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

Both are **`br` statuses**, declared in the tracked [`.beads/policy.yaml`](../../.beads/policy.yaml)
(nocx-q8yjf.20, 2026-09-29). Until then they were comments on an issue left `in_progress`,
because the integration was written believing beads has neither; `br` 0.7.0 takes any status
the policy declares. The cost of the old shape was measured the day it was replaced: 11
issues read `in_progress` while nobody held them, against AGENTS.md's "in_progress means a
worker is holding it now", and a session read them as abandoned work.

The policy does two things. `strict` makes `br` refuse a status it does not list, naming the
allowed set. `required_fields` makes `br` refuse a transition into either status without a
comment, and writes that comment in the same transaction. The comment is the record:

```text
submitted: <revision> -- <local-check evidence>      # worker result, awaiting merge
implemented: <revision> -- <related-check evidence>  # merged into the stage, awaiting acceptance
```

Writing one: `br update <id> --status submitted --transition-comment "submitted: 1a2b3c4 --
go build ./... and go vet ./internal/x"`. The adapter reads the latest comment of the issue's
own status's kind. Neither status is in `br ready`; `--claim` on either moves it back to
`in_progress`, and a reopen is `br update <id> --status in_progress`, not a comment.
When a worker submits, the coordinator takes the assignee (`br update <id> --actor <coordinator>
--assignee <coordinator>`), so the result is held for merging and no worker holds it.
Without both halves — a revision, `--`, then evidence — the gate reports
`submitted-without-evidence` / `implemented-without-evidence`. It reports the same
rule when the status sits on a leaf that is **not under a stage**: measured 2026-09-21
on the setup task, which hangs under a chore, so its proof record is a plain comment.
A setup, grooming or charter task outside any stage therefore records its evidence
plainly and stays `in_progress` until it closes. Proved 2026-09-29 on a scratch tracker
with this repository's policy file: a transition without the comment and an undeclared
status are both refused, and open → submitted → implemented → in_progress → close runs.

The adapter includes the native `acceptance_criteria` field under an Acceptance
Criteria heading in `body`, alongside the description. Criteria stored there are
not missing merely because the description contains no heading.

Stage acceptance, while the stage is still open, is stored as a comment:
`accepted: <revision> -- <criterion and acceptance evidence>`. It is not a close,
and the revision must be present in the consuming checkout. Contextual ready
selection and claims below read this record; a submitted prerequisite still
releases nothing.

`br` reads the policy from the MAIN checkout's `.beads`, whichever worktree runs it, so a
change to it acts only once the main checkout has it.

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
- **Merge:** a merge naming nothing borrows links from incoming commits made under
  the rule. Each is checked on its own too. A newly written merge of old history
  still needs its own leaf task when there is no link to borrow; set its merge
  message when landing it.
- **Before the rule:** commits committed before `commitLinksFrom` (config) are
  listed and fail nothing. A new merge is not old merely because its incoming
  history is old. The committer date is the author's to set, so this is honesty,
  not enforcement.

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
  tracked export for historical and gate exports. `--at <rev>` reads any revision
  (`:0` is staged), `--export <path>` reads a file; with neither flag it reads the working tree.
  Contextual ready and claims resolve br's main export with `br where --json`,
  refresh it through native `sync --flush-only`, then read its complete edges and
  comments. No stale worktree export is imported and no task or assignment changes
  during readiness. The generated main export is refreshed, not staged or committed.
  `list` omits edges/comments; full `show` over the historical queue was too slow. It
  maps statuses, emits `holder` (assignee), `createdAt` and stored acceptance criteria,
  drops tombstones, and reads the submitted, implemented and accepted-stage records
  above. Provenance edges are dropped; only `blocks` gates. It also emits
  `landed`: the commits reachable from both the judged revision (HEAD for the
  staged copy and the working tree) and `origin/main`, else `main`, whose header
  names a task, parsed by `commit-links.mjs`'s `linkedIds` — the commit check's own
  parser. That list turns on the gate's `merged-but-open`; a branch's own commits
  are not on it, and a checkout with no main branch is refused rather than given an
  empty list. Its own test:
  `node --test .githooks/backlog-gate/adapter.test.mjs`.
- **Rules:** `check.mjs`, `check-commits.mjs`, `check-docs.mjs`,
  `check-present.mjs` and `check-product.mjs`, with `time-format.mjs`,
  `document-format.mjs`, `regular-file.mjs` and `argv.mjs`, in [`.githooks/backlog-gate/`](../../.githooks/backlog-gate/).
  These are byte-for-byte copies of the plugin files under
  `skills/backlog/setup-shady2k-skills/` at source commit
  `3fca4e833c56d708a95156a5a716d66492c9cc39` (plugin 0.126.0, setup protocol
  0.56.0), never edited here. Compare each copy with that source. Run all five `--selftest`
  commands from the plugin directory, where their fixtures live; `check.mjs`
  additionally takes `--config <repo>/.githooks/backlog-gate/config.json` for
  portability.
  Protocol 0.45.0 adds an optional `check.mjs --at <revision>` guard: the
  input's `source` must end in ` @ <revision>` matching that exact argument.
  A known 0.46.0 limitation lets an empty or missing `--at` value skip that
  optional guard; the fix remains in upstream shady2k/skills PR #33.
  The owner approved using the published version rather than waiting for it.
  The adapter emits the revision suffix for historical exports, but existing
  hooks and CI do not pass this optional checker flag; their revision selection
  and the in-branch tracker layout remain unchanged. This update does not
  install the separate off-branch tracker migration.
  The commit-link wrapper sets `recordsOnly` for staged changes and historical
  single-parent commits only when `.beads/issues.jsonl` is the sole changed
  path, the same issue ids remain, every other field except `comments` and
  `updated_at` is unchanged, and at least one issue's comments actually differ.
  The commit check refuses those commits: records ride with the landing.
  New or deleted issues, substantive queue edits and changes accompanying any
  other path are not classified as records-only. Merge commits keep their
  existing link/incoming-history behavior; older commits retain their date
  exemption. This is a structural check, not a judgment of comment prose or
  commit titles, and it does not detect bookkeeping outside that export.
- **Present documents:** the config's `presentDocuments` lists what describes the present
  here — AGENTS.md, README.md, `docs/architecture.md`, `docs/agents/`, the frontend state
  ownership and lifecycle protocol documents, `contracts/README.md` and the UI kit's README.
  `presentIgnores` holds what those documents name that is not a path of this tree (Go's
  `log/slog`, the plugin's own paths, the `docs/adr` this repository
  forbids). The check runs in CI's `ci-backlog` on every pull request, including
  drafts, from the merge base to the PR's head, and by hand before a pull request is opened:
  `node .githooks/backlog-gate/check-present.mjs --config .githooks/backlog-gate/config.json --base "$(git merge-base origin/main HEAD)"`.
  A dead reference the change made refuses it; older drift is printed and fails nothing. The
  first run, on 2026-09-28 at `0ad291c6d`, found three, filed as one debt item (nocx-q8yjf.16).
  No local hook runs it.
- **Product documents and practices:** the real pre-commit hook runs
  `node .githooks/backlog-gate/check-product.mjs --staged`; CI runs it with
  `--rev <actual PR head or pushed revision>`. It needs no project setting.
  Product documents keep their existing paths. A practice record, when one is
  adopted, is a `PRA-NNN` file under `docs/practices/`; this repository has no
  product documents or practice records today, so there is nothing to judge.
  The separate product/feature/acceptance document gate remains uninstalled.
- **Work records:** session claims and receipts, and command-duration records used
  to bound later runs, are comments printed by `runs.mjs` in the plugin's
  `take-task`, `close-out` and `ask-shady2k`. The adapter exports every comment
  starting with `[shady2k-time` or `<!-- shady2k-exec`, raw and whole, damaged
  or not, as `comments: [{ id, at, author, body }]` from beads' `id`,
  `created_at`, `author` and `text`; ordinary comments are left out.
  `runs.mjs --backlog` reads this normalized JSON, not raw JSONL. Execution
  records must survive that boundary for the next command's bound to use its
  recorded durations rather than the first-run default (nocx-q8yjf.26).
  The adapter emits that JSON on stdout with:
  `node .githooks/backlog-gate/adapter.mjs --export "$(br where --json | jq -r .jsonl_path)"`.
  Capture stdout to a temporary file and pass that file to `--backlog`; `br` keeps the
  main export current. Post a printed record unchanged with
  `br comments add <id> -f <file holding the printed body> --actor <agent>`; `-m` with a
  shell-quoted multi-line string can reflow a record. Proved 2026-09-26 on the
  setup task: posted, exported, byte-identical.
- **Time records adopted 2026-09-26** (nocx-q8yjf.12). The 61 leaves in flight then —
  active, submitted or implemented with no claim record, almost all of them the stages "The
  frontend stops deciding blocks" and "The runtime's cells reach the client" — are listed
  in the config's `timeRecordsExempt`, and their time is unknown. Their coordinators'
  transcripts are on the VM, but each held twenty to forty items at once, so claims
  recovered one at a time would have cut its hours by pick-up order into figures that look
  measured and are not; the owner chose the exemption. The list only shrinks: work handed
  in unclaimed later gets the rule's own fix, never a place on it.
- **A run's time is its coordinator's clock** (0.32.0, nocx-q8yjf.13). A leaf handed in
  under a feature or stage whose coordinator holds a claim counts as recorded without a
  claim of its own: the coordinator waits while its workers work, so its span covers them.
  A worker's own span, where one is written, adds only its effort by phase; subagent
  workers write none (the run script counts a subagent inside its coordinator's session),
  and the run script reads omp transcripts as well as Claude Code and Codex ones.
- **Launch a run from its Git checkout.** In omp, use `omp --cwd <checkout>` rather than
  launching in a non-Git parent and changing only tool working directories. The run
  script attributes sessions by their recorded working copy. During this setup it
  refused recovery for a local transcript whose recorded cwd was the existing non-Git
  parent, not this repository. That transcript is present, not missing;
  this setup's elapsed time is unmeasured. Do not fabricate a claim, mark a present
  transcript missing, edit its metadata or add a new time-record exemption.
- **Tracker layout:** the store lives outside every code branch, at
  `refs/tracker/state` in this repository. Each machine keeps one working copy at
  `$XDG_STATE_HOME/nocx/tracker` (default `~/.local/state/nocx/tracker`), and every
  checkout and worktree of that machine reaches it through a `.beads` symlink. No
  code branch carries `.beads/issues.jsonl`. `scripts/connect-tracker-store.sh`
  connects a clone: it puts `core.hooksPath` outside the tree, runs tracker-sync
  connect, installs `.githooks/backlog-gate/tracker-policy.yaml` as the published
  `.tracker-sync/policy.yaml` and the store-local `.beads/policy.yaml`, and publishes.
  The tracker at a code revision is read from the store's recording of that exact
  revision; a missing recording is refused, never guessed from a later state. `make
connect` runs this connector before it checks the backlog inputs.
- **Tracker synchronization:** `tracker-sync watch` publishes a change within seconds
  and syncs every 10 seconds. The user systemd unit
  (`~/.config/systemd/user/tracker-sync.service`) keeps it running; its copied
  mechanism lives under `~/.local/state/nocx/mechanism/` so removing a worktree does
  not stop it. Pushing `refs/tracker/state` starts no code CI, so the backlog's own
  rules are checked on that ref's delta by `.github/workflows/tracker-gate.yml` rather
  than by a code push.
- **Document gate: not installed yet.** Its task is "The document gate checks product
  intent, feature readiness and acceptance evidence on every change here" (nocx-q8yjf.2).
  Until it lands, document readiness is checked by reading, and every report says the
  automatic check did not run. No document policy, baseline, receipts or synchronization
  are wired, and nothing about documents is enforced by CI except the present-documents
  check above, which judges names and not readiness. Since 0.33.0 a required check
  may name the paths it does not read (`ignores`), and its receipt is then pinned to a
  revision of its own inputs (`checkRevisions`, from `checkRevision` in the plugin's
  `document-format.mjs`); wiring the gate finds those paths from each check's command and
  proves the pin on two commits, a prose edit and an edit to a file the check reads.
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
- **Backlog gate**, `block-new`, at the publish boundary (nocx-fg79x.3). The tracker
  store is published on `refs/tracker/state`, outside every branch, and a code push or
  pull request carries no tracker state — so the rules run on the published delta.
  `.github/workflows/tracker-gate.yml` (hourly, and by hand) compares the published store
  head with the last head the gate **accepted**, `refs/tracker-gate/checked`, through
  [`publish-gate.mjs`](../../.githooks/backlog-gate/publish-gate.mjs):

  ```bash
  G=.githooks/backlog-gate
  node $G/publish-gate.mjs --baseline <last-checked-store-commit> --head <published-store-head>
  ```

  It is the same `check.mjs`, with the same `block-new` strength the code hooks used: a
  violation the baseline does not carry fails, and everything older is printed and fails
  nothing. The baseline is the last head the gate accepted, not the head's parent, so a
  violation introduced several publishes ago is still new; the marker advances only on a
  green run, so a violation stays red until the store is fixed. Two limits are worth
  knowing: the job cannot refuse the publish (GitHub starts no workflow for a push to a
  ref that is not a branch or a tag, and the mechanism runs its own git calls with the
  project's hooks disabled), and the marker leaves the checkout every time, so the check
  runs on a schedule rather than on the publish itself. Its test:
  `node --test .githooks/backlog-gate/publish-gate.test.mjs`.

  Neither the pre-commit hook nor CI's `ci-backlog` runs the rules on a code change: a
  code commit stages no tracker state, and the pre-commit section 7 is disabled with
  `RUN_BACKLOG=0` for that reason. `ci-backlog` keeps the commit-link check, the
  present-documents check and the gate's own tests.

- **JSON report:** add `--json` to the `check.mjs` line.
- **Recorded tracker states import into br:** every code revision the gate judges
  must have a recording under `.tracker-sync/recordings/`, and the recorded store
  commit's `.beads/issues.jsonl` must import into br. A connected checkout reads it
  through the mechanism (`tracker-sync.mjs export --at <rev>`), which owns the store
  layout and verifies the recording's digest; a clone with no store — CI, which fetches
  `refs/tracker/state` in `scripts/connect-tracker-ci.sh` — reads the same recording
  from that ref. `adapter.mjs` refuses a missing or malformed recording instead of
  falling back to a later state.
- **Commit-link check:** [`commit-links.mjs`](../../.githooks/backlog-gate/commit-links.mjs)
  parses messages and calls `check-commits.mjs`. `--message <file>` checks a pending
  message; tasks come from the export `br where` names, which is the database's own view,
  so a bead created a minute ago resolves. `--range <base>..<head>` checks every commit the
  range introduces, with tasks from the export at `<head>`. An empty range is exit 2, never
  a pass. `--pull-request <base-sha> --head <head-sha>` is the form CI uses: the base is the
  event's own `pull_request.base.sha`, so a rerun of an already-merged PR still checks the
  commits it introduced instead of collapsing to `head..head` (nocx-1ebrs.66.1.32).
  `--introduced <tip> --by <ref> [--before <sha>] --remote <remote>` checks
  commits not reached by the target remote's other actual refs or the pushed ref's
  old tip. CI already sees the published ref, so that ref itself is excluded.
  Local-only tags and other remotes cannot hide a commit. A tag is peeled to a commit.
  None says `<ref> introduces no commits: nothing to check`; unreadable input is exit 2.
  `node --test .githooks/backlog-gate/commit-links.test.mjs`.
- **Local entry points:** `.githooks/commit-msg` (links), `.githooks/pre-push` (links for
  the commits a push introduces), and `.githooks/pre-merge-commit`, which delegates to
  pre-commit; the backlog's own rules are not a local code-hook gate (they run on the
  published store, above). A hook that cannot find
  what it reads refuses and names `make connect`: `commit-msg` without `node`,
  the adapter, `check-commits.mjs` or the config, `commit-links.mjs` without `br` or with an empty export, the backlog gate without its adapter, its rules, `time-format.mjs` or its
  config (exit 2). A tree that never had the integration passes visibly; a configured
  tree missing `commit-links.mjs` does not.
- **Connecting a clone:** `make connect` (`scripts/connect-clone.sh`), which `make init`
  runs first. It checks all four hooks, all five shipped checks, both format helpers,
  adapter and project wrappers, readable config, tracker policy/config and the export,
  plus Git, Bash, Node, npm, br and the root tool configs. Bash is the pre-commit
  runtime: both scope classification and formatter arguments preserve NUL-delimited paths.
  Missing root formatter/linter binaries
  are installed from the locked root dependencies without install scripts; no Go,
  frontend or product build is started. It reports missing files together and imports
  the tracker successfully before connecting git hooks or clearing the retired driver.
  Failure does not leave a half-connected clone. Safe to rerun.
- **CI:** `ci-backlog` in `.github/workflows/ci.yml` checks commit links and the
  present documents on a code pull request; it does not compare backlog states, since a
  code pull request carries none (nocx-fg79x.3). Pull requests use the merge base and
  actual PR head, not GitHub's synthetic merge. An unexpectedly empty range refuses.
  Pushes use the same introduced-commit enumeration as the local pre-push hook, against
  the remote's actual refs and the old tip where it exists; a new ref carrying no new
  commits passes visibly. Product-document checks read the actual incoming revision,
  and the incoming backlog and its configuration both come from that revision.
  An advertised object absent from this clone cannot bound locally reachable history
  and is skipped, regardless of its ref namespace; range enumeration never downloads
  unrelated histories merely to exclude them (nocx-q8yjf.27).
  **The backlog's rules** run in `.github/workflows/tracker-gate.yml` on the published
  store's delta (see the gate entry above).
  **Pre-push** reads every stdin ref tuple and runs those range checks. It runs no
  product suite and does not consult tracker synchronization status.
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
- **Reviewer:** prefer an independent Codex model different from the implementer,
  one round at stage acceptance. In omp use its reviewer agent and retain the
  actual reported model, not only the agent's name. The 2026-10-09 reconciliation
  used `openai-codex/gpt-6-astra` for correctness independently of
  `openai-codex/gpt-6.1-sol`; its additional security review used the implementer's
  model independently and is disclosed as same-model. From Claude Code the way is
  the external `codex` CLI: proved 2026-10-09 on the VM (`vm-agents`) with one real
  call, `codex exec --skip-git-repo-check -s read-only "<prompt>"`, which answered
  on `gpt-5.6-sol` as recorded in its session transcript (the model's own answer
  about itself is not the evidence). A machine without the `codex` executable, or
  with Claude CLI unauthenticated, does not advertise that way as working. A host
  without the preferred model uses the config's disclosed independent fallback or
  escalates.
- **Parallel execution:** separate git worktrees, one per worker. The coordinator assigns
  leaves one at a time, with at most `execution.maxWorkers` workers and disjoint file sets.
  Generated files and lockfiles (`package-lock.json`, `go.sum`, `contracts/` generated
  types) count as collisions. The stage's coordinator integrates.
  The same coordinator serializes all readiness-affecting writes with assignments,
  including dependency edits, container/prerequisite status changes and stage acceptance.
  Workers submit their own results but never self-assign or change another prerequisite.
  Native `claim_exclusive` protects the holder atomically; readiness is checked
  before that native write, not in its transaction. A forced claim is safe only
  under this single-coordinator rule. This is the owner's operating policy
  (confirmed 2026-10-09), not an interprocess or distributed lock. Stop assigning
  if another coordinator or writer is changing readiness-affecting state.

## Tracker operations

`br` is a single binary over SQLite plus the tracked export. It never runs git, and it
resolves the MAIN checkout's database even from a worktree. Its own docs: `br robot-docs
guide`, `br <command> --help`.

| operation                        | here                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| create                           | `br create "<title>" -t epic\|task\|bug\|chore -l <area> -d "<body with DONE WHEN for an epic>" [--parent <id>] [-p 0-3]`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| link / unlink                    | `br dep add <consumer leaf> <producer leaf>` (type `blocks`), with the reason and what releases it in a comment · `br dep remove …` · parent: `-t parent-child` · provenance: `-t discovered-from`, which gates nothing                                                                                                                                                                                                                                                                                                                                                                                      |
| claim                            | `node .githooks/backlog-gate/adapter.mjs --claim <leaf> --stage <stage> --checkout <path> --actor <harness>-<role>:<person>@<machine>:<branch>#<session>` validates readiness and checkout ancestry, then invokes br's atomic exclusive claim. Edges stay intact. Native `--force` is used only for verified implemented prerequisites, never to override another holder or an unfinished prerequisite. Post the record `runs.mjs claim` prints. Container ownership remains separate.                                                                                                                       |
| release                          | `br update <id> --status open --assignee ""`: an unfinished hold only, in the minute it stops                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| submitted                        | `br update <id> --status submitted --transition-comment "submitted: <rev> -- <evidence>"`, then the coordinator takes the assignee                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| implemented                      | coordinator only: `br update <id> --status implemented --transition-comment "implemented: <merge rev> -- <related-check evidence>"`                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| reopen                           | `br update <id> --status in_progress` with a comment saying why, and recheck every issue blocked by it; a closed one: `br reopen <id>` first                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| close                            | `br close <id> -r "<accepted at <rev>: evidence a stranger can check>"`, only after stage acceptance, or with an explicit duplicate/cancellation reason naming the survivor                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| comment / edit                   | `br comments add <id> -f <file> --actor <agent>` posts a work record byte-for-byte. A wrong record is retired with the `runs.mjs void` record, not edited or deleted. Ordinary notes and field edits use `br comments add` and `br update`; reparenting preserves real dependency edges.                                                                                                                                                                                                                                                                                                                     |
| defer / undefer                  | `br defer <id> --until <date>` · `br undefer <id>`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| milestone / label                | `br label add <root> <milestone>` (read the value from config); `br label add\|remove <id> <label>`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| ready                            | `node .githooks/backlog-gate/adapter.mjs --ready --stage <stage> --checkout <path>` reads the active br store and returns unheld open leaves whose prerequisites are satisfied there. Same-stage implemented prerequisites need evidence and their revision in the checkout. A prerequisite in another stage of the same feature needs a stored accepted revision that includes its integration and is present in the checkout. Real dependency edges, not generated id ordering, establish precedence. Other features require closure. Submitted and implemented work are never offered to implement again. |
| holds                            | `br list --status in_progress` (assignee on each row)                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| pending integration / acceptance | `br list --status submitted` / `br list --status implemented`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                |
| children                         | `br dep list <id> --direction up -t parent-child` (every status, closed included) · counts: `br show <id> --json \| jq '.[0].rollup'`                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| search / show                    | `br search "<phrase>"` (title, body, comments) · `br list --label <area> --status all` · `br show <id>` · `br comments <id>`                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| publish                          | `node .githooks/backlog-gate/tracker-sync.mjs publish --repo "$(git rev-parse --show-toplevel)" --recipe .githooks/backlog-gate/br-tracker.mjs`; the watcher usually does it. No tracker file is committed. **Never `br sync --merge` on a database that is merely ahead** (AGENTS.md).                                                                                                                                                                                                                                                                                                                      |

For setup and other leaves outside a feature stage, the coordinator uses native
`br --no-auto-import --no-auto-flush update <leaf> --claim --actor <full-agent-name>`
after inspecting its real prerequisites. Do not invent a stage to claim it.

**Where contextual ready and native `br ready` differ.** Both exclude deferred or
blocked ancestors; the adapter also checks intermediate groups, not only the
stage. Native ready may offer a non-epic parent as work, so the adapter removes
containers of every type. Native ready does not recognize the integrated and
accepted-stage evidence above; the adapter can offer those dependants only after
the evidence and checkout ancestry checks. Closed prerequisites behave the same.
Compare scoped ready leaves and explain these differences, not raw counts that
include containers.

## Open

Nothing. The one entry here — who runs the related tests — was decided by the owner on
2026-09-21 and is recorded under "Checks and execution" above.
