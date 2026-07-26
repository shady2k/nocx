# AGENTS.md — Working rules for AI agents on `nocx`

`nocx` is a local-first, Warp-style terminal (Go backend + xterm.js frontend + Wails v2
desktop). This file is the operating contract for **any** AI agent (Claude Code, Cursor,
OpenCode, …) contributing to the repo. Read it before writing code.

## Read first (sources of truth)

- [`docs/vision.md`](docs/vision.md) — what we're building, MVP scope, roadmap.
- [`docs/architecture.md`](docs/architecture.md) — the architecture spine: invariants
  (`AD-1`…`AD-10`), module boundaries, the WebSocket protocol. **The ADs are binding.**
- The task backlog lives in **beads** (`bd`), not in prose. Get work with `bd ready`.
- **New here?** The [README setup](README.md#agent-tooling) is the full install guide —
  the toolchain _and_ the per-machine agent tooling (`bd`, the `beads-superpowers`
  plugin, and optional `graphify`). `make init` does not install any of it.

## First thing in a fresh clone

Two kinds of setup, in order. **First, install the tooling on your machine** — the
toolchain (Go, Node, Wails, `bd`) _and_ the agent tooling that is not vendored:
the [`beads-superpowers`](https://github.com/DollarDill/beads-superpowers) Claude
Code plugin (Superpowers skills + the `bd` session hooks) and, optionally,
`graphify` for knowledge-graph code search. The [README](README.md#agent-tooling)
has the exact commands; `make init` installs none of it and assumes the tools
already exist.

**Then wire up the repo:**

```bash
make init
```

Run it before touching code. Git carries neither the issue database nor the ref
it lives on, so until `make init` has run there is **no backlog**: `bd ready`
answers "no beads database found", and an agent that reads `.beads/issues.jsonl`
instead is reading a passive export that may lag the database.

After that, task state syncs by itself and you should not sync it by hand:

- `git commit` writes and stages `.beads/issues.jsonl`, so the snapshot travels
  in the same commit as the work it describes.
- `git push` runs `bd dolt push`, which is what a fresh clone actually reads.

If a push stops with a beads failure, fix the sync — do not reach for
`--no-verify`. That path leaves everyone else on a backlog that looks current
and is not, which is precisely the failure this setup exists to prevent.

## Repository layout

- `docs/` — living source-of-truth docs (`vision.md`, `architecture.md`, `decisions/` ADRs).
- `AGENTS.md` — this file (the agent rules). `CLAUDE.md` only points here.
- `_bmad/`, `.claude/`, `.agents/`, `.opencode/` — vendored BMAD agent tooling.
- Code directories appear as the app grows — follow the module map in `docs/architecture.md`.

## How we work

1. Take the next task from beads — the one command in [What to work on
   next](#what-to-work-on-next), not a bare `bd ready`, and claim it with the three-step
   protocol below, not a bare `bd update --claim`.
2. Read the relevant `AD`(s) in `docs/architecture.md` before touching a boundary.
3. **TDD**: red → green → refactor. Write the failing test first.
4. Keep it green: language-specific format, lint, and tests all pass (pre-commit runs them).
   The pre-commit hook is the gate on every commit; CI validates release branches and tags.
5. Update the task in beads; record any non-obvious decision as an ADR in `docs/decisions/`.

### Before you fix anything: find out whether it is already decided or already filed

A bug report is a symptom, not a mandate to start editing. Four checks, in this order,
before the first line of a fix. They are cheap; skipping them is how two agents ship two
different answers to the same question, and how a "fix" quietly contradicts a decision
somebody already argued out.

1. **Is it already filed?** Search the tracker before creating anything or starting
   anything.

   ```bash
   bd query "status=open" --json | jq -r '.[] | "\(.id) [\(.issue_type)] \(.title)"' | grep -i <keyword>
   bd list --label <topic> --status all      # decisions, research and design beads
   bd memories <keyword>                     # what a past session learned the hard way
   ```

   A hit is not automatically your task — read it. It may already be claimed, may be
   blocked, or may record that the behaviour is deliberate. If the symptom you were
   handed is a duplicate, say so and work the existing bead instead of opening a second
   one; two beads for one defect is how a fix lands twice and conflicts with itself.

2. **Is it deliberate?** Read [`docs/vision.md`](docs/vision.md) for what we are building
   and what is explicitly out, and the epic that owns the area for what it declared out of
   scope. Things that look broken often are not. The "Sessions" panel in the activity bar
   is empty because a comment in `main.ts` says it is a deliberate placeholder — an agent
   who "fixed" it would have invented a feature nobody asked for.

3. **Which `AD` does it touch?** [`docs/architecture.md`](docs/architecture.md) is binding.
   Check before, not after: a fix that routes PTY bytes through JSON-RPC or lets the
   backend sniff the stream is not a fix. If the `AD` is genuinely wrong, change it
   deliberately in the document — do not route around it in one module.

4. **Was it decided in an ADR?** `ls docs/decisions/` and read the one that covers the
   area. Re-deciding a settled question inside a bugfix is how the settled question stops
   being settled.

5. **Is the code you found actually reachable?** A file existing on `main` does not mean
   the feature is in the product. `grep` finds definitions; it does not tell you whether
   anything calls them, and a test calling them looks identical to a caller that matters.

   ```bash
   deadcode -filter 'nocx/internal/<pkg>' ./...   # unreachable from main(), tests excluded
   grep -rn "New<Thing>(" --include=*.go . | grep -v _test   # who constructs it?
   ```

   Read the composition root — `internal/app/app.go` — and confirm the thing you are
   about to work on is wired into it.

The two failures this prevents, both measured on 2026-07-26 in one session:

**Checks 1-4.** PR #11 ("SSH Connection Manager") shows as **closed, unmerged** on
GitHub, and its feature set is on `main` anyway: its commit `557e87d` is an ancestor of
`main`, and PR #12 later reworked it onto the ADR-0011 credential boundary. An agent
reading only the closed PR would have concluded the work was dropped and rebuilt several
thousand lines. `git log --all --grep` and `git merge-base --is-ancestor` answered it in
a minute.

**Check 5, learned by getting it wrong in that same session.** Having established the
files were on `main`, the next claim made was "so the vault shipped". It did not.
`internal/app/app.go:81` wires `credential.NewKeychain()`; `NewVault()` and
`NewCredentialStore()` have no callers outside tests; `deadcode` reports all 26 functions
of the vault — `Unlock`, `SaveSecret`, `deriveKey`, `encryptGCM` — as unreachable.
Four hundred lines of working, well-tested crypto that no user can reach. The tests are
what hid it: `unused` sees a test calling `NewVault` and correctly stays silent, which is
why three closed beads (nocx-1vr, nocx-l7o, nocx-dcd) hardened a subsystem nothing
executes. **Coverage proves a unit works. It says nothing about whether the product uses
it.** See nocx-25k9.1 and nocx-ckoy.1.

The root cause is worth naming, because it is cheap to repeat: the deferral was recorded
in a code comment — `credential.go:14-19`, "only to avoid editing app.go in this wave" —
and nowhere else. The wave ended and nothing asked for the comment back. **A `TODO` in
source is not a task. If you defer something, file the bead before you write the
comment.**

### Before you investigate: two cheap checks that beat reasoning

Both of these were learned by skipping them and losing an afternoon.

**Search the memories before fighting the environment.** `bd memories <keyword>`
costs seconds. A session spent installing Xvfb, chasing an `EGL_BAD_PARAMETER`
abort and rebuilding NixOS twice to get the Playwright suite running ended when
`bd memories e2e` turned up a memory describing `cmd/devharness` plus the
`NOCX_WS_PORT` shim in `e2e/harness.ts` — a headless path needing no wails, no
GTK and no display at all. It had been in the repo the whole time. Memories are
pull-based: nothing surfaces them for you, so ask.

**When a branch behaves differently from `main`, diff it against `main` first.**
Before measuring, instrumenting or theorising:

```bash
git diff origin/main...HEAD -- <path> | grep '^-'
```

A large feature commit can silently drop a line, and the symptom will look like
anything but a deletion. `557e87d` (52 files, +8025/−605) removed one
subscription — `tab.onBufferChange = () => … syncAltScreenClass()` — and the
visible result was a Playwright click timing out on a button that hit-testing
reported as visible. Reasoning about geometry and DOM measurement took hours;
the removed-lines diff found it in a minute and, swept across the whole
directory, proved nothing else had been lost.

### What to work on next

Asked to "keep going" with no further instruction, this is the whole answer:

```bash
# tasks inside epics somebody has actually taken
for e in $(bd list --type epic --status in_progress --json | jq -r '.[].id'); do
  bd ready --parent "$e" --exclude-type epic -u -n 5
done
# plus standalone bugs, which legitimately have no epic
bd ready --exclude-type epic -u -n 100 --json | jq -r '.[] | select(.parent == null) | "\(.id)  \(.title)"'
```

**You may not take a task out of an epic nobody has taken.** Owning the epic comes first —
that is what "an epic is handed over whole" means in practice, and a bare
`bd ready --exclude-type epic` does not enforce it. Measured on 2026-07-26: of seven tasks
it offered, five belonged to `nocx-5mn`, which no one had claimed. A worker did take
`nocx-d3q.1` while `nocx-d3q` sat at `○`. If the epic you want is free, take the epic
(`bd update <epic> --claim`), then come back for its children.

It returns single tasks drawn only from epics that are open for work — 9 of the ~90 open
issues, against the 68 a bare `bd ready` returned on 2026-07-26 before the backlog was
sequenced (nocx-k0xk.1). Take the top one and claim it with the three-step protocol below.
**If it returns nothing, that is an answer, not a bug**: every open epic's front is
occupied. Finish something in flight or take a free epic — never widen the query to find
more work.

**Never take work out of a blocked epic.** A blocked epic is blocked because something else
is being changed in the same files; work started there gets rewritten or collides. The
guard is automatic if you use the command above — a blocked parent hides all its children
from `bd ready`, including children with no dependency of their own. Verify rather than
trusting this paragraph: `nocx-d3q.1` has no dependencies and is still not ready, because
`nocx-d3q` is blocked. The failure mode is going around the guard — `bd list`, `bd query`,
or picking an ID out of a document — so if a bead is not in `bd ready`, do not start it,
and if you believe the block is wrong, say so and get it removed rather than working
through it.

The invariants below keep that command honest. Break one and the noise comes back.

**An epic blocks another only when they touch the same code.** Not "this is more important",
not "this comes later in the plan" — the edge means _two people cannot hold these at once_.
Several epics being available simultaneously is normal and wanted; the only requirement is
that they are disjoint, so two agents can take two epics without landing in the same files.

```bash
bd dep add <blocked-epic> <blocker-epic>   # blocked-epic cannot start until blocker lands
```

This criterion replaced an earlier one, and the difference matters. Edges were first used to
express a schedule, which parked every epic behind the current foundation and left nothing
to hand out; 13 of 20 epic-level edges turned out to encode "not yet" rather than overlap
and were removed. What survives is checkable: `nocx-8yg` (fonts, themes, hotkeys) waits on
`nocx-2gf` because app hotkeys and the editor keymap fight over the same keys, and
`nocx-25k9` (vault) waits on `nocx-9le` because both rewrite the SSH profile UI — while
those two run in parallel with each other, which is the point.

Priority, not blocking, is where "this matters more" goes.

**`blocked` is computed, never stored.** `bd query "status=blocked"` returns nothing while
`bd stats` counts a couple of dozen. You cannot set the status; you can only add the edge
that causes it. A blocked epic's stored status stays `open`, and `bd show --short` prints it
as `○` with no hint that anything is holding it — the `●` beside it is priority, not
blocking. To see what actually holds an epic, read its `DEPENDS ON` list.

**An epic is a DAG, not a bag.** Wire the children with `blocks` so the epic exposes a
handful of entry points rather than all of them at once. The check is the front itself:

```bash
bd ready --parent <epic> --exclude-type epic -n 100 --json | jq 'length'
```

Three or so is healthy. An epic where every child is an entry point has recorded no internal
order at all, and that is what made `bd ready` unusable as a queue in the first place.
`nocx-5mn` is at five today and would benefit from sequencing.

Do **not** use `bd swarm validate` for this — it counts closed children in its waves, so it
reports max parallelism 7 for an epic whose real front is 3. It is good for reading the shape
of an epic and useless as a gate.

**An epic has three states, and the third one is the useful one.** `in_progress` means
somebody is working it _right now_. Blocked means parked, or waiting on its predecessor in
someone's stream. Plain `open` and unblocked means **free to hand to a colleague** — and
that is a feature, not an oversight:

```bash
bd ready -t epic -u        # epics nobody owns and nothing blocks — what you can give away
```

Do not mark an epic `in_progress` to stop it appearing in a task listing. That is backwards,
and it was done once here: three epics were flipped to `in_progress` purely to keep a bare
`bd ready` clean, which then reported five active tracks when the owner was running three.
The status has to describe reality; `--exclude-type epic` is what keeps epics out of a
task-level query.

Corollary worth checking for, because it hides: a child sitting `in_progress` inside a
_blocked_ epic means work is happening in a frozen track. Usually it is a stale claim from
an earlier session rather than live work. `bd list --status in_progress` against the epic
states finds them.

**Where a bug goes.** Inside a live deliverable, it is a child of that epic — `nocx-au6`
belongs to deleting wterm because the seam lying about capabilities is part of that job. A
bug that arrives from nowhere gets **no parent at all**: a standalone bug is legitimate and
shows up in `bd ready` on its own. Do not file it under the nearest plausible epic — that
reflex is exactly what grew the two area epics, one honest-looking parent at a time. If
triage shows the bug is a symptom of something structural, it _becomes_ an epic (or spawns
one) and carries a `discovered-from` edge back to itself, the way `nocx-4ff` points at
`nocx-gs0` and the way `nocx-bw2` anchored `nocx-rdkh`.

**An epic is assigned, its children are claimed.** Those are different acts and both are
needed. Taking an epic means owning the whole deliverable — set it `in_progress`, assign
yourself, and see it to its DONE WHEN. Then work its children one at a time through
`bd ready`, `-u` when listing and `--claim` when taking, which is what lets two agents work
two disjoint epics without coordinating. What you never do is `--claim` the epic bead itself
as though it were a task; `--exclude-type epic` on every task-level query keeps that from
happening by accident.

**Creating an epic.** Five things, and the first is the one that gets skipped:

1. Scope it to a deliverable, not a code area. "Persistence" and "Quality gates" were areas;
   both had to be closed and split because every new bug in the area landed in them, so they
   could never finish and could only be cherry-picked. Ask: can one person be handed this
   whole and finish it?
2. Write a criterion that stops being false exactly once, and name what is deliberately out.
   This one is **enforced**, not advised: `validation.on-create` is `error`, so `bd create -t
epic` without acceptance criteria fails and creates nothing. Pass `--acceptance "..."`, or
   put a `## Success Criteria` heading in the description — `bd lint` accepts either. An area
   of code has no such criterion by construction, which is precisely why `nocx-6ek` and
   `nocx-k0xk` could never close; the gate exists to stop the next one being created.
3. Set the status deliberately. `bd create` leaves it `open`, which means _free to assign_ —
   correct for a real backlog item, wrong if you already own it.
4. Add `blocks` edges only against epics whose files it collides with (above).
5. Label it `mvp`, `phase-2`, `phase-3` or `infra`, and check the ordering invariant still
   holds: no `mvp` epic sits behind a deferred one.

Prefer more, smaller epics. The backlog went from 15 to 23 doing exactly this, and that was
the goal rather than a side effect — "handed over whole" and "large area" cannot both hold.

### Claiming work on a shared backlog

Several people work this repo from their own machines against one shared issue database
(`refs/dolt/data` on the git remote). Claim in three steps, never just the middle one:

```bash
bd dolt pull                # see who took what since your last sync
bd ready && bd update <id> --claim
bd dolt push                # publish the claim now, not at your next git push
```

`git pull` refreshes the backlog on its own (`.githooks/post-merge`, `post-rewrite`), and
`git push` publishes it (`.githooks/pre-push`). The explicit pull/push above exists because
claiming is the one moment where minutes of staleness cost somebody a duplicated afternoon.

**Publish every backlog write immediately, not at session close.** `bd dolt push` right
after you create a bead, edit one, add a dependency edge, or close one — the same rule the
claim protocol above states, applied to every write and not only to claims. An unpushed
bead does not exist for anybody else: a colleague runs the queue command, does not see it,
and files or works the same thing. Batch the writes if you like (`bd batch` is one
transaction), then push once at the end of the batch — but do not carry them into the next
task.

```bash
bd create ... && bd dep add ... && bd dolt push
```

The cost is a few seconds. The cost of skipping it is somebody else's afternoon, and it is
paid by them rather than by you, which is exactly why the rule has to be written down.

**A claim is not a lock.** Two people can claim the same bead from two clones; both pushes
land, Dolt merges them, last write wins. The protocol shrinks the race window — it does not
close it. Auto-push (`dolt.auto-push`) stays off on purpose: upstream warns that concurrent
pushes to a git-protocol Dolt remote can corrupt or strand remote history. If claim races
ever become routine, the fix is a shared Dolt sql-server, not a shorter interval (nocx-wj4).

## Git authority

Agents have **standing authority to commit and push** on this repo. This overrides the
"Conservative (default)" profile in the managed Beads block below — that block defers to
repository instructions, and this is one. It lives here rather than inside the block
because the block is regenerated from a hash and edits to it are lost.

Allowed without asking, every session: `git commit`, `git push`, `bd close`,
`bd dolt push`, and running the quality gates. Branch first if you are on `main`.

**Merging a pull request always requires explicit approval.** Not a green-CI one, not
your own, not a one-line one — the user has to ask for it in that session. Authority to
commit and push is not authority to merge, and approval to merge one PR does not carry
to the next.

Run the full local gate before pushing, not only the part you touched: `gofumpt -l .`,
`golangci-lint run`, `go test -race ./...`, plus `npx prettier --check .`, `npx eslint .`,
`cd frontend && npm run typecheck`, `cd frontend && npm test` for frontend changes.

**Take the merge slot before integrating into `main`.** When several worktrees land in
sequence, hold `nocx-merge-slot` for the whole merge-and-resolve, and release it whether
you succeed or not:

```bash
bd merge-slot acquire     # blocks/queues if another agent holds it
# merge, resolve conflicts, run the gate, push
bd merge-slot release
```

It is an exclusive lock — `open` means free, `in_progress` means held, `metadata.holder`
says who has it and `metadata.waiters` is the queue. Without it two agents resolve
conflicts against a `main` that is moving underneath both, and each resolution invalidates
the other's; beads calls this "multiple polecats racing to resolve conflicts and creating
cascading conflicts". A worker that forgets the release strands everyone behind it, so
release in the failure path too — `bd merge-slot check` tells you who is holding it.

This is orthogonal to the approval rule above: the slot decides _who merges next_, never
_whether_ the merge is allowed.

## Engineering rules (non-negotiable)

- **Interface-first + DI.** Every module lives behind an interface, wired at a single
  composition root. Depend on abstractions, obey SRP, keep modules trivially replaceable.
- **Quality gates from every commit:** language-specific formatting, linting, and test,
  enforced by the pre-commit hook. Mandatory tests for every language — Go and TypeScript
  are held to the same bar.
- **Observability:** structured logging via Go `log/slog` behind the logging interface —
  no ad-hoc `fmt.Println`.
- **Clean-only:** no backward-compatibility shims (greenfield — break & refactor freely),
  no dead code (delete it), no quick-win hacks. YAGNI — don't build speculative features.
- **Respect the spine.** Don't violate an `AD` to save time; if an `AD` is wrong, change it
  in `docs/architecture.md` deliberately rather than routing around it. E.g.: never wrap PTY
  bytes in JSON-RPC (AD-1 data plane); the backend never sniffs the byte stream (AD-6);
  session-id is server-authoritative (AD-7).

## Stack

- **Backend:** Go — `pty`, `ssh` (via `golang.org/x/crypto/ssh`), `session`, `transport`,
  `config`. One core, multiple build targets.
- **Frontend:** xterm.js (WebGL) + TypeScript UI. Terminal render state lives here (AD-6).
  See [ADR-0001](docs/decisions/0001-xterm-js-as-vt-frontend.md) (amended 2026-07-26).
- **Desktop shell:** Wails v2 (macOS first).
- **Transport:** one WebSocket — raw **binary** data plane + **JSON-RPC 2.0** control plane (AD-1).

## Current top risk

The VT-frontend risk was settled in
[ADR-0001](docs/decisions/0001-xterm-js-as-vt-frontend.md).
Next risk to watch: run `bd ready`.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:970c3bf2 -->

## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md for details and anti-patterns.

## Agent Context Profiles

The managed Beads block is task-tracking guidance, not permission to override repository, user, or orchestrator instructions.

- **Conservative (default)**: Use `bd` for task tracking. Do not run git commits, git pushes, or Dolt remote sync unless explicitly asked. At handoff, report changed files, validation, and suggested next commands.
- **Minimal**: Keep tool instruction files as pointers to `bd prime`; use the same conservative git policy unless active instructions say otherwise.
- **Team-maintainer**: Only when the repository explicitly opts in, agents may close beads, run quality gates, commit, and push as part of session close. A current "do not commit" or "do not push" instruction still wins.

## Session Completion

This protocol applies when ending a Beads implementation workflow. It is subordinate to explicit user, repository, and orchestrator instructions.

1. **File issues for remaining work** - Create beads for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Handle git/sync by active profile**:
   ```bash
   # Conservative/minimal/default: report status and proposed commands; wait for approval.
   git status

   # Team-maintainer opt-in only, unless current instructions forbid it:
   git pull --rebase
   bd dolt push
   git push
   git status
   ```
5. **Hand off** - Summarize changes, validation, issue status, and any blocked sync/commit/push step

**Critical rules:**

- Explicit user or orchestrator instructions override this Beads block.
- Do not commit or push without clear authority from the active profile or the current user request.
- If a required sync or push is blocked, stop and report the exact command and error.

<!-- END BEADS INTEGRATION -->

<!-- BEGIN BEADS CODEX SETUP: generated by bd setup codex -->

## Beads Issue Tracker

Use Beads (`bd`) for durable task tracking in repositories that include it. Use the `beads` skill at `.agents/skills/beads/SKILL.md` (project install) or `~/.agents/skills/beads/SKILL.md` (global install) for Beads workflow guidance, then use the `bd` CLI for issue operations.

### Quick Reference

```bash
bd ready                # Find available work
bd show <id>            # View issue details
bd update <id> --claim  # Claim work
bd close <id>           # Complete work
bd prime                # Refresh Beads context
```

### Rules

- Use `bd` for all task tracking; do not create markdown TODO lists.
- Run `bd prime` when Beads context is missing or stale. Codex 0.129.0+ can load Beads context automatically through native hooks; use `/hooks` to inspect or toggle them.
- Keep persistent project memory in Beads via `bd remember`; do not create ad hoc memory files.

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/SYNC_CONCEPTS.md for details and anti-patterns.
<!-- END BEADS CODEX SETUP -->
