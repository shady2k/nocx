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

**Your dev profile is not the installed app's.** Anything you build or run from
this repo — `wails dev`, `make dev-web`, `make build`, and the Playwright suite,
which launches a backend of its own — resolves `nocx-dev` rather than `nocx`,
because the directory is chosen by the build tag and only `-tags release` picks
the shipped one (`internal/storage/appdir.go`). So a dev stand starts with no
profiles and no vault, and that is correct: before this, an e2e run wrote the
developer's real settings and reset their theme on every pass (nocx-ti8w). If
you want your real SSH profiles in the dev stand, copy them across by hand —
nothing migrates them for you, and nothing should.

**And the e2e suite gets a disposable `$HOME`.** On the default path
`playwright.config.ts` applies it to the `wails dev` backend and you need do
nothing. On the **headless** path you start the backend yourself, so the suite
cannot isolate it and refuses to run until you say you have: export
`NOCX_E2E_HOME_DIR` and launch devharness with that `HOME` — `e2e/preflight.ts`
prints the exact command when it stops you. Do not work around it by unsetting
`NOCX_WS_PORT`; the boundary is what keeps a run off your settings, your vault
documents, your `~/.nocx` and your shell rc files.

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

### A test asserts what a user can do, not what the code currently does

**Before you call anything done, exercise it the way a user reaches it — end to end, through
the seam a person actually touches.** Not the unit. Not the handler in isolation. The whole
path: the button exists, it is clickable, and the thing it promises actually happens on the
other side.

This is not a style preference. A test written by reading the implementation cannot report a
missing feature, because it has no notion of what is absent — it can only confirm that what
was written does what it was written to do. That is the failure mode, and it is silent:
every gate stays green while the product does not work.

Measured on 2026-07-29, one session, all found by a user clicking:

- **The connection manager shipped with no way to create a group.** Eight epics closed, 1041
  frontend tests green, `deadcode` clean, every acceptance criterion read clause by clause.
  `ConnectionsView` had a full group **editor** — impact preview, danger confirmation,
  delete — reachable only from a group header that only appeared once a group already
  existed. No test noticed, because every test mounted the component and asserted what it
  rendered.
- **`groups.create` refused every call the UI could make.** `JSONStore.CreateGroup` requires
  a non-empty id and the handler minted none, so it answered `group ID is required`. There
  are nine backend tests for `groups.create` and **all nine pass an explicit id**
  (`ProfileGroup{ID: "g1", …}`). They encoded the caller's convenience, which was true for a
  test and false for the renderer. One test with an empty id would have caught it.
- **An empty group rendered as nothing at all**, so the button — once it existed — looked
  broken, and the group could not be reached to be renamed or deleted.

Three defects, one shape: each unit was correct and the user's task was impossible.

So, concretely:

- **Added an RPC method?** Call it the way the renderer calls it, including the fields the
  renderer leaves empty. A handler tested only with fully-populated params is tested against
  its author's assumptions.
- **Added a UI action?** Assert the control is present and enabled from the state a user
  starts in, that activating it reaches the client method, and that the result appears in the
  list afterwards. "The dialog opens" is not the feature.
- **Wired something new into the composition root?** The reachability checks in the next
  section tell you whether it is connected. They do not tell you whether it works.

The existing rule below — _"Coverage proves a unit works. It says nothing about whether the
product uses it"_ — is the same lesson one level down, learned when the vault's 26 tested
functions turned out to be unreachable. This one is its sibling: **reachable is not usable.**

### More tests will not save you. These three habits will

The rule above is about tests that miss a feature. This one is about tests that miss a **defect**
in the feature they cover, which is a different failure and is not fixed by writing more of them.

Measured on 2026-07-30, on `internal/vault/vault.go`: eighteen tests, `go vet` clean,
`golangci-lint` clean, `go test -race` green. An adversarial read against the spec then found ten
correctness defects, two of them release-blocking — a `Setup` that returns four times while
holding its mutex, deadlocking every later operation, and a `Create` that deletes the journal
record it had just written, reopening precisely the crash gap the journal exists to close. Every
one of those survived every gate. Adding a nineteenth test of the same kind would have changed
nothing.

**1. Ask what the failure paths do, not only what the happy path does.**

Of those eighteen tests, none made a dependency return an error. All four deadlocking returns in
`Setup` had zero coverage. The metric that matters is not how many tests exist but how many
**dependency-failure paths** are exercised: for every external call your code makes, there is a
test where that call fails. It is mechanical, it is cheap, and it would have caught five of the
ten defects on its own.

For a procedure with several steps that touches more than one store, go further and enumerate the
partial failures: step 3 of 5 fails — what is now true on disk, in the OS keychain, and in memory,
and how does the next start recover? "It returns an error" is not an answer.

**2. State invariants as intervals, not as moments.**

The journal defect was specified into existence. The acceptance criterion read: _"`Create` writes
`PhasePrepared` **before** calling the provider — prove it with a `Put` that panics."_ The worker
implemented exactly that, and the test passes. But the property that matters is not a moment, it
is a span: the record must exist **from before the write until metadata references the secret**. A
criterion that names only the start of the interval buys a test that guards only the start.

So write invariants with both ends: what event makes this true, and what event is allowed to end
it. If you cannot name the closing event, you do not yet understand the invariant.

**3. Do not let the author of the code be the only author of its tests.**

A test written by the implementer, in the same pass, encodes the implementer's model of the
problem — including the parts that are wrong. The worker believed clearing the journal on success
was correct, so the test asserts that it is cleared. Nothing inside that loop can discover the
belief is mistaken; the code and the test agree, and they are wrong together.

Two ways out, in increasing cost. Cheapest and almost always worth it: **write the acceptance
criteria as assertions rather than prose**, in the task itself, so there is nothing left to
interpret — `after Create the journal holds a PhaseSecretWritten entry for the id; after Commit it
does not`. Expensive, and reserved for code where a defect is costly — the vault, the updater, the
transport: **have someone who did not write the implementation write the tests from the spec**, or
have an independent reader check the code against the spec afterwards. That second reading is what
found eight of the ten defects above, and it cost one round trip.

**4. Before accepting any new symbol, ask who calls it.** One `grep`, every time.

This is not a new rule — the reachability checks two sections down have always said it. It is here
because knowing the check and running it are different things, and on the same day as the defects
above, the same reviewer skipped it twice. Once caught, once not:

- `frontend/src/vault.tsx` — 18 passing tests, imported by nothing. Found, because someone thought
  to grep for the import.
- `internal/vault/system/SecretServiceAvailable` — written to guard a keyring integration test,
  compiled on every platform, given a non-Linux stub, accepted. Called by nothing. The CI job that
  exists to run that test stands up a real Secret Service, goes green, and exercises the keyring in
  zero tests. Nobody grepped.

Both were reviewed carefully. Careful is not the same as systematic, which is why this is a listed
habit and not advice.

Note what this section is not. Mutation testing (`nocx-u4b`) answers a narrower question — does a
test assert on the line it executes — and it would have flagged the weak assertions here. It cannot
find the deadlock or the lifecycle races, because those are _missing_ code and thread
interleavings, and mutation operators only delete and replace what is already written. It is a
complement to this section, not a substitute for it.

### Two green suites, one broken feature: the wire is a party to the contract

The two sections above are about a test agreeing with the code it was written from. This
one is what happens when **two** codebases each agree with themselves and disagree with
each other, and there is nothing in between to notice.

Measured on 2026-07-31. `vault.status` had never sent `defaultProvider`. The renderer's
`VaultStatus` declared the field, the Vault page read it on every render to mark which
store new secrets go to, and `vault.SetDefaultProvider` wrote a value nobody could ever
read back. The page therefore showed two storage providers, neither marked, both
offering to become the default one of them already was. Found by a user looking at the
screen; every gate was green, on both sides.

Neither side was under-tested. They were tested **separately**, and each side's tests
were written from that side's belief about the wire:

- The Go tests decode the result into an anonymous struct naming the two or three fields
  that test is about. **A field nobody names is a field whose absence nobody notices** —
  there is no assertion in the language for "and nothing else".
- The frontend tests mock the client with hand-written fixtures, and those fixtures were
  written _from the interface_. They contained `defaultProvider` because the renderer
  wanted it, not because the backend sent it. The mock encoded the wish.

So: **every JSON-RPC result shape is declared once, as a JSON Schema in `contracts/`.**
The renderer's types are **generated** from it; the Go transport is **validated** against
it. Not two declarations checked against each other — one declaration that belongs to
neither party.

```
                  contracts/vault.status.schema.json
                                 │
               ┌─────────────────┴──────────────────┐
     generated │                                    │ validated
               ▼                                    ▼
frontend/src/generated/vault.status.ts     the marshalled Go DTO, and the
(committed; never hand-edited)             real result off the WebSocket
```

The two directions are deliberately not symmetric. On the renderer, drift is
**impossible**: the types are generated and committed, `vault-client.ts` re-exports them
and declares nothing of its own, so a type that wants a field the wire does not carry
cannot be written. On the Go side it is **reliably detected** rather than impossible —
generating Go wire DTOs would either infect the domain types or need a mapping layer, and
this seam has not earned that. `additionalProperties: false` plus an explicit `required`
is what makes the check exact in both directions; a schema without both is theatre.

Three checks, and the third is the point:

- `npm run contracts:check` (pre-commit) — the committed generated file still matches the
  schema.
- `TestVaultStatus_DTOConformsToContract` — the Go struct marshals to something the schema
  accepts. Catches field tags, `omitempty`, a nil slice becoming `null`, an enum spelled
  differently.
- `TestVaultStatus_OverTheWireConformsToContract` — the **real result, off the real
  socket**, satisfies the schema. This is the one that would have caught
  `defaultProvider`. A test that validates a payload the test itself built proves the
  struct is well-formed, not that the server sends it.

Two things learned by getting them wrong, both within an hour of writing the above:

- **A sample is not a contract.** The first version of `contracts/` was a fully-populated
  sample response compared by key set. It cannot express types, nullability or enums —
  changing `autoSealMinutes` from `int` to `string` kept it green. A schema replaced it.
- **The schema finds things immediately, so write it before you believe the code.** On its
  first run it caught a second defect: `providers` marshalled as `null` rather than `[]`
  when no providers are registered, which is the same class as `nocx-25k9.14` and would
  have thrown on the renderer's first `.map`.

`contracts/` covers `vault.status` today and is filled in **as methods are touched** — a
method you add or change gets its schema in the same commit (`nocx-bt3w` tracks the
sweep). See `contracts/README.md` for how to add one. Until a method has a schema its wire
format is unchecked, and you should assume the two sides disagree, because once they
demonstrably did.

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

### Every commit names its bead

**A commit message must carry the bead id of the work it does.** Not optional, not
"where relevant". A commit with no id is a change nobody can trace back to a decision:
`git log` stops answering _why_ and only answers _what_, and the why is the part that is
expensive to reconstruct.

The format:

```
<type>(<scope>): <subject in the imperative, lower case, no full stop> (<bead-id>)

<body: what was wrong, what changed, and why this way rather than the obvious
alternative. Wrap at 80. Prose, not bullets — a bullet list records what you did and
loses the reasoning, which is the only part worth keeping.>

Co-Authored-By: ...
```

- **`<type>`** — `feat`, `fix`, `refactor`, `test`, `docs`, `build`, `chore`, `perf`.
- **`<scope>`** — the module: `frontend`, `pty`, `ssh`, `session`, `transport`, `spec`,
  `beads`. Omit only when the change is genuinely repo-wide.
- **`(<bead-id>)`** — at the end of the subject. `(nocx-abc1)`. Several ids when one
  commit genuinely closes several: `(nocx-u7wq.1-.5)` for a contiguous run,
  `(nocx-abc1, nocx-def2)` otherwise.
- Additional ids that are _referenced but not closed_ go in the body, named as such, so
  the subject line stays the list of what this commit finishes.

Two things that are not exceptions and get asked about anyway:

- **No bead for it?** Then there is no task, and per the rule further up this file a
  `TODO` in source is not a task either. File the bead first — `bd create` takes seconds
  and the id is what the message needs.
- **Trivial change?** A one-line fix still had a reason, and a one-line fix with no id
  is the one nobody can explain six months later. `chore(beads): sync the export`
  belongs to a bead too, or it is noise that should not be a commit of its own.

The id is checked by eye at review, not by a hook. If that stops working, the fix is a
`commit-msg` hook that rejects a subject with no `(nocx-…)` — file it rather than
letting the convention rot.

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

### Before you build a UI component: read the kit

**Read [`frontend/src/ui/README.md`](frontend/src/ui/README.md) and list
`frontend/src/ui/` before writing any UI element.** Not after, not "if the kit looks
like it has one" — the inventory table names every component, its identity classes and
its variance, and it takes a minute to read.

Three things, in this order:

1. **Does the kit already have it?** Then import it from `ui/`. A "toggle" is
   `Checkbox variant="switch"`; a status message is `showToast`; a titled group of
   controls is `Section` or `PageSection`. If it does the job at 90%, add the missing
   variance as a typed `data-*` on the existing component rather than forking it.
2. **Does something close enough exist that this is a variant of it?** Extend that
   component in `ui/`, with the variance in its props and its rules in its own CSS file.
   The kit grows by variants, not by near-duplicates.
3. **Genuinely new?** Then it goes in `ui/` as a component — one module, one CSS file in
   `styles/components/`, a stable identity class, a test, and a row in the README table.

What you may never do is build the control **inside the surface**: a hand-rolled
`<div class="st-something">` with its own colours and spacing, a bespoke button, a
"temporary" status div, a one-off `.tsx` helper that draws a control the kit is supposed
to own. Every one of those is a second vocabulary for the same thing, and the app then
shows two looks for one concept — which is the entire defect the kit migration
(`nocx-pp3y`, `nocx-v0ai`) spent two epics unwinding. Measured examples that got in:
`.st-export-status`, which each surface would have re-invented until Toast existed, and
the settings rail painting a Button's selected state from outside.

A surface may **place** a kit component (`flex`, `margin`, `width`, `order`,
`align-self`, `position`) and may never **repaint** it (`background`, `border`, `color`,
`font-*`, `padding`, `box-shadow`). If you find yourself wanting to repaint one, the
component is missing a variant — add it there.

If the kit is genuinely wrong for the case, say so and change the kit deliberately, the
same way an `AD` gets changed. Do not route around it in one surface.

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
