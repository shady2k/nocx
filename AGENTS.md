# AGENTS.md — Working rules for AI agents on `nocx`

`nocx` is a local-first, Warp-style terminal (Go backend + xterm.js frontend + Wails v3
desktop). This file is the operating contract for **any** AI agent contributing to the
repo. Read it before writing code.

Every rule here was bought by a specific failure. The failures are named in one line so
you can tell a rule from a preference; the long-form post-mortems are in `git log` for
this file.

## Read first

- [`docs/vision.md`](docs/vision.md) — what we're building, MVP scope, roadmap.
- [`docs/architecture.md`](docs/architecture.md) — the spine: invariants `AD-1`…`AD-10`,
  module boundaries, the WebSocket protocol. **The ADs are binding.**
- The backlog lives in **beads** (`bd`), not in prose.
- [`frontend/src/ui/README.md`](frontend/src/ui/README.md) — before any UI element.
- [README setup](README.md#agent-tooling) — the toolchain _and_ the agent tooling
  (`bd`, the `beads-superpowers` plugin). `make init` installs neither.

**Fresh clone:** install the tooling, then `make init`. Git carries neither the issue
database nor its ref, so until `make init` runs there is no backlog — `bd ready` answers
"no beads database found", and `.beads/issues.jsonl` is a passive export that may lag it.
Afterwards `git commit` stages the export and `git push` runs `bd dolt push`. If a push
fails on beads, fix the sync; `--no-verify` leaves everyone on a backlog that looks
current and is not.

**Never resolve a conflict in `.beads/issues.jsonl` by hand** — neither side is the
answer. The backlog is merged in Dolt by `bd dolt pull`; the file only restates what the
database says, so the resolution is to regenerate it. `make hooks` installs a merge
driver that does exactly that, and `.gitattributes` says when it lags by a commit. If you
ever see this file in a conflict, your clone is missing the driver — run `make hooks`.

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

**That boundary does not cover the login keychain — so run e2e in the container.**

```bash
e2e/run-in-container.sh                        # whole suite, both browsers
PW_PROJECTS=chromium e2e/run-in-container.sh e2e/sidebar.spec.ts
```

`$HOME` moves three things and the keystore is a fourth: `go-keyring` talks to
the Keychain service, not to a directory, and `app.New` probes the system vault
provider on **every** backend start — "a probe is a real keychain write", says
the comment doing it. `wails dev` re-signs the binary each run, so macOS re-asks
every time, and a suite that restarts the backend per spec asks continuously.
The owner watched a dialog appear every two seconds during a local run
(`nocx-o4hg`). A Linux container has no keychain to ask, and it runs the headless
path — `cmd/devharness` plus vite — so it is also about fifteen times faster
than a cold `wails dev` per spec.

**Its failure set is not CI's, and CI is the source of truth.** The container
runs Linux WebKit at a container-default viewport; the shipped app is macOS
WKWebView. Layout-sensitive specs (scroll ownership, tab-strip roving, label
centring) fail there and pass in CI. Use it to iterate, confirm in CI, and never
"fix" a test that is only red in the container without checking which one is
lying.

## Repository layout

- `docs/` — `vision.md`, `architecture.md`, `decisions/` (ADRs).
- `contracts/` — one JSON Schema per JSON-RPC result shape (see below).
- `AGENTS.md` — this file. `CLAUDE.md` only points here.
- Code directories follow the module map in `docs/architecture.md`.

## Code search

**`grep`, `glob` and reading the file.** There is no code-knowledge-graph index, and
nothing sits in front of a file read.

> A graph index (`graphify`) was removed on 2026-08-01 after one measured session: five
> queries, zero answers, while every finding that mattered came from `grep`. It cost 91 MB
> of committed output and a hook that made a graph query mandatory before every read. Our
> questions are almost always _does this exist, and who calls it_ — exactly what `grep`
> answers. Do not reintroduce an index, or any hook in front of a file read, without
> measuring against that baseline.

## How we work

1. Take the next task with the queue command in [What to work on next](#what-to-work-on-next).
2. Read the relevant `AD`(s) before touching a boundary.
3. **TDD**: red → green → refactor. The failing test comes first.
4. Keep it green — the pre-commit hook is the gate on every commit.
5. Update the bead; record any non-obvious decision as an ADR in `docs/decisions/`.

## Testing: five rules, each bought by a green suite over a broken product

**1. A test asserts what a user can do, not what the code currently does.** Exercise the
feature through the seam a person actually reaches — the button exists, it is enabled from
the state a user starts in, activating it reaches the client method, and the result appears
afterwards. A test written by reading the implementation cannot report a missing feature;
it can only confirm that what was written does what it was written to do.

> 2026-07-29: the connection manager shipped with **no way to create a group** — 1041
> frontend tests green, every test mounting the component and asserting what it rendered.
> `groups.create` refused every call the UI could make, because all nine backend tests
> passed an explicit id and the renderer minted none. An empty group rendered as nothing at
> all. Three defects, one shape: every unit correct, the user's task impossible.

**2. Every epic that is not a chore proves its happy path.** Name in one sentence what a
user can do that they could not before, and close the epic only when one automated check
has watched them do it end to end. Write that check when the epic is created — by the end
you know what the code does, and that is the knowledge that makes you write the test the
implementation passes. `cmd/devharness` runs the real backend headless (no wails, no GTK,
no display), so there is no excuse about the harness.

**`deadcode` and coverage are floors, never criteria. Neither can report a feature that is
missing — only that written code is used.**

> 2026-08-01: `nocx-rtg0` ("your commands survive a restart") shipped an encrypted SQLite
> store, a key lifecycle, a budget, a retention policy, a `history.query` method with a
> schema and five Settings controls. `ContentDB.Add` had **no caller outside its own
> tests** — no command was ever recorded. The acceptance criterion was "`deadcode` is
> empty", and it was: `Query` is genuinely reachable, so a reachable read path hid an
> unreachable write path in the same package. The worker had reported "production wiring is
> blocked; the store is test-reachable only"; the next round said "deadcode empty" and it
> was read as "history works". **A reported blocker becomes a bead with a dependency edge
> in the same minute, or it evaporates between rounds.**
>
> Same epic, second way: `contentkey` had tests for every failure path and none asserting
> the key is obtainable on an ordinary machine — where it never was. **For every "returns
> an error when…" there is a paired "and on a normal machine it succeeds".**

**Ask `deadcode -whylive <symbol>`, not `deadcode -filter <package>`.** The filter form is
worse than a weak check on the packages we most want to check: `deadcode`'s RTA marks every
method reached through an interface as reflection-reachable, so for `internal/content` the
filter has **always** printed nothing and cannot be made to print anything. An acceptance
criterion written on it is not merely satisfiable while the write path is dead — it is
unfalsifiable. `-whylive` answers the question actually being asked, and the contrast is
what makes it evidence:

```
deadcode -tags gtk3 -whylive '…/internal/content.sqliteContent.Submit' ./...
  → main → App.Run → … → sqliteContent.Submit        # wired
deadcode -tags gtk3 -whylive '…/internal/content.sqliteContent.AddEdge' ./...
  → "reachable only through reflection"              # not wired
```

> 2026-08-17, `nocx-rtg0.3`: the brief demanded the `-filter` run and the worker ran it,
> got the empty output the brief predicted, and then said so plainly — that the same empty
> output appeared before its commit and on a clean tree, so it was evidence of nothing. It
> used `-whylive` on a wired method and an unwired one in the same package to show the
> difference. That is the check; note the `-tags gtk3` too, without which cgo fails on Linux
> before `deadcode` reaches our code at all.

**And the blind spot is not `-filter`'s — it is RTA's, so the ratchet has it too.** Later
the same day `nocx-re6gk` measured it directly, with three probes in one run: a plain
unwired function **is** reported; the same shape **wired** from one call site is not; and a
dead **method on a type reached only through an interface** is **not reported either**.
That third case is exactly the shape `ContentDB.Add` had. So the gate catches a dead
function, and cannot catch a dead method behind a live interface — which is most of this
codebase, since AD-8 puts every module behind one.

**Therefore: `deadcode` can tell you a symbol is dead. It can never tell you a feature is
wired.** For that, name the seam and ask `-whylive` for it, or write the test that watches
a user do the thing. Rule 2's "every epic proves its happy path" is not a supplement to the
ratchet; on an interface-first codebase it is the only check that works.

**3. Test the failure paths, and state invariants as intervals.** For every external call
your code makes, there is a test where that call fails — mechanical, cheap, and the single
highest-yield check we have. For a procedure touching several stores, enumerate the partial
failures: step 3 of 5 fails — what is now true on disk, in the keychain, and in memory, and
how does the next start recover? And write invariants with **both ends**: not "`Create`
writes `PhasePrepared` before calling the provider" (a moment) but "the record exists from
before the write until metadata references the secret" (a span). If you cannot name the
closing event, you do not yet understand the invariant.

> 2026-07-30, `internal/vault`: eighteen tests, `go vet` clean, `golangci-lint` clean,
> `-race` green. An adversarial read then found ten defects, two release-blocking — a
> `Setup` that returned four times while holding its mutex, deadlocking everything after
> it, and a `Create` that deleted the journal record it had just written. None of the
> eighteen made a dependency fail; all four deadlocking returns had zero coverage. The
> criterion that named only the start of the interval bought a test that guarded only the
> start.

**4. Do not let the author of the code be the only author of its tests.** A test written by
the implementer in the same pass encodes the implementer's model, including the parts that
are wrong — the code and the test agree, and are wrong together. Cheapest fix, almost
always worth it: **write acceptance criteria as assertions rather than prose**, in the bead
itself. Expensive and reserved for code where a defect is costly (the vault, the updater,
the transport): have someone who did not write the implementation write the tests from the
spec. That second reading found eight of the ten defects above, for one round trip.

**5. The wire is a party to the contract.** Every JSON-RPC result shape is declared once,
as a JSON Schema in `contracts/`. The renderer's types are **generated** from it (committed,
never hand-edited); the Go side is **validated** against it. `additionalProperties: false`
plus an explicit `required` is what makes it exact — a schema without both is theatre.
Three checks, and the third is the point:

- `npm run contracts:check` (pre-commit) — the committed generated file matches the schema.
- `…_DTOConformsToContract` — the Go struct marshals to something the schema accepts.
- `…_OverTheWireConformsToContract` — **the real result, off the real socket**. A test that
  validates a payload the test itself built proves the struct is well-formed, not that the
  server sends it.

`contracts/` is filled in **as methods are touched** — a method you add or change gets its
schema in the same commit (`nocx-bt3w` tracks the sweep). See `contracts/README.md`.

> 2026-07-31: `vault.status` had never sent `defaultProvider`. The renderer's type declared
> it, the Vault page read it every render, and `SetDefaultProvider` wrote a value nobody
> could read back — so the page showed two providers, neither marked. Both suites were
> green: Go tests decode into anonymous structs naming the fields that test is about (there
> is no assertion for "and nothing else"), and the frontend's hand-written fixtures were
> written _from the interface_, so they contained the field because the renderer wanted it.
> The schema's first run also caught `providers` marshalling as `null` rather than `[]`.

**A soft degrade must be visible in the product, not only in a log.** History failing to
open is a `slog.Warn` while Settings goes on offering a toggle, a retention age and a budget
that govern nothing. A silent degrade the UI contradicts is how a feature that does not
exist survives a release.

## Before you fix anything

A bug report is a symptom, not a mandate to edit. Five checks, in order — skipping them is
how two agents ship two answers to one question.

1. **Already filed?**

   ```bash
   bd query "status=open" --json | jq -r '.[] | "\(.id) [\(.issue_type)] \(.title)"' | grep -i <keyword>
   bd list --label <topic> --status all   # decisions, research, design beads
   bd memories <keyword>                  # what a past session learned the hard way
   ```

   A hit is not automatically your task — read it. It may be claimed, blocked, or record
   that the behaviour is deliberate. Work the existing bead rather than opening a second.

2. **Deliberate?** `docs/vision.md` and the owning epic say what is explicitly out. The
   empty "Sessions" panel is a placeholder a comment in `main.ts` declares; "fixing" it
   invents a feature nobody asked for.

3. **Which `AD`?** Check before, not after. A fix that routes PTY bytes through JSON-RPC or
   lets the backend sniff the stream is not a fix.

4. **Decided in an ADR?** `ls docs/decisions/`. Re-deciding a settled question inside a
   bugfix is how it stops being settled.

5. **Is the code reachable?** A file on `main` is not a feature in the product.
   And when you are the one PLANNING the work: a task that adds a Go package lands
   together with the wiring that makes it reachable, or its commit cannot pass the
   deadcode ratchet at all — the gate is the hook, not the brief, so a worker cannot be
   briefed out of it (`nocx-z7s6`; two commits went in with `--no-verify` before anybody
   noticed the plan had made that unavoidable).

   ```bash
   deadcode -filter 'nocx/internal/<pkg>' ./...              # unreachable from main()
   grep -rn "New<Thing>(" --include=*.go . | grep -v _test   # who constructs it?
   ```

   Read `internal/app/app.go` and confirm the thing is wired in. Then check the other
   direction too — rule 2 above: a package can be reachable and still have a dead half.

> 2026-07-26, one session, two failures. PR #11 shows closed-unmerged on GitHub while its
> commit is an ancestor of `main` — an agent reading only the PR would have rebuilt
> thousands of lines. Then, having established the files were on `main`, the next claim was
> "so the vault shipped": `deadcode` reported all 26 of its functions unreachable. The tests
> hid it, and the deferral lived only in a code comment. **A `TODO` in source is not a task
> — file the bead before you write the comment.**

### The five checks gate the brief, not the diff

If you are a coordinator writing a brief, a spec or a plan for somebody else to implement,
**the checks above are yours and they apply before you write it.** The brief is where the
architecture is decided; by the time a worker is editing files, the decision has already
been made and the checks can only confirm it. "I am not touching code" is not an exemption —
it is the moment the exemption costs the most.

A brief that crosses a boundary **names the `AD`s and ADRs it touches and what they already
decided, before it says what to build.** Checked by eye at review, like the commit-message
rule.

> 2026-08-04, one session, three times. The nocxify spec proposed `stty -echo`, parsing away
> echoed regions, and inferring stdin ownership from the byte stream — the three techniques
> ADR-0004 names and rejects, in that order, in one paragraph. Then a brief told a worker to
> shell out to `ss`/`netstat` on the **local** machine, against "Interface-first + DI" and
> against `internal/contentkey`, which is the same per-OS problem already solved in this
> repo. Then a report to the owner claimed nocx deliberately never deploys a binary to a
> remote host, while `architecture.md` defers a Tier-B remote helper, AD-2 names it a build
> target, AD-1 reserved a msg-type for its feed, and `nocx-if6` phase B is that relay.
>
> One cause each time: writing from what the conversation remembered instead of reading the
> binding document for the boundary being crossed. The owner caught all three. The third one
> would have shipped a provider seam the relay had to be forked into.

## Before you investigate: two checks that beat reasoning

**Search the memories before fighting the environment.** `bd memories <keyword>` costs
seconds and is pull-based — nothing surfaces them for you.

> A session spent installing Xvfb and rebuilding NixOS twice to run Playwright ended when
> `bd memories e2e` turned up `cmd/devharness` plus the `NOCX_WS_PORT` shim — a headless
> path needing no display, in the repo the whole time.

**When a branch behaves differently from `main`, diff it against `main` first** — before
measuring, instrumenting or theorising:

```bash
git diff origin/main...HEAD -- <path> | grep '^-'
```

> `557e87d` (52 files, +8025/−605) silently dropped one subscription, and the symptom was a
> Playwright click timing out on a visible button. Hours of geometry reasoning; the
> removed-lines diff found it in a minute.

## What to work on next

Asked to "keep going" with no further instruction, this is the whole answer:

```bash
# tasks inside epics somebody has actually taken
for e in $(bd list --type epic --status in_progress --json | jq -r '.[].id'); do
  bd ready --parent "$e" --exclude-type epic -u -n 5
done
# plus standalone bugs, which legitimately have no epic
bd ready --exclude-type epic -u -n 100 --json | jq -r '.[] | select(.parent == null) | "\(.id)  \(.title)"'
```

**If it returns nothing, that is an answer, not a bug** — every open epic's front is
occupied. Finish something in flight or take a free epic; never widen the query.

- **You may not take a task out of an epic nobody has taken.** If the epic is free, take
  the epic (`bd update <epic> --claim`), then come back for its children.
- **Never take work out of a blocked epic** — it is blocked because the same files are
  moving. The queue command enforces this; going around it via `bd list`, `bd query` or an
  ID in a document is the failure mode. If a bead is not in `bd ready`, do not start it.
- **An epic is assigned, its children are claimed.** Owning an epic means seeing it to its
  DONE WHEN. Never `--claim` an epic bead as though it were a task.
- **`bd ready -t epic -u`** lists epics nobody owns and nothing blocks — what you can hand
  to a colleague. Do not flip an epic to `in_progress` to hide it from a task listing;
  `--exclude-type epic` is what does that.

### Backlog invariants

- **An epic blocks another only when they touch the same code** — not "this is more
  important" and not "this comes later". `bd dep add <blocked-epic> <blocker-epic>`.
  Priority, not blocking, is where importance goes. Several epics available at once is
  normal and wanted.
- **`blocked` is computed, never stored.** You cannot set it; you can only add the edge.
  A blocked epic still prints as `○` — read its `DEPENDS ON` list.
- **An epic is a DAG, not a bag.** Sequence children with `blocks` so the front is ~3:
  `bd ready --parent <epic> --exclude-type epic -n 100 --json | jq 'length'`. Do not use
  `bd swarm validate` as a gate — it counts closed children.
- **Where a bug goes.** Inside a live deliverable, a child of that epic. Arriving from
  nowhere, **no parent at all** — a standalone bug is legitimate. Filing it under the
  nearest plausible epic is what grew the two area epics that had to be split. If triage
  shows it is a symptom of something structural, it _becomes_ an epic and carries a
  `discovered-from` edge back to the bug.

> 13 of 20 epic-level edges once encoded "not yet" rather than overlap and were removed;
> before that, a bare `bd ready` offered 68 issues and the queue was unusable.

### Creating an epic

1. **Scope it to a deliverable, not a code area.** Can one person be handed this whole and
   finish it? "Persistence" and "Quality gates" were areas — every new bug landed in them,
   so they could never finish.
2. **Unless it is a chore, name what a user can do that they could not before, and the one
   end-to-end check that watches them do it** (rule 2 above). No such sentence means it is
   a chore — label it — or an area of code wearing an epic's clothes.
3. **A criterion that stops being false exactly once**, plus what is deliberately out.
   Enforced: `bd create -t epic` without `--acceptance` (or a `## Success Criteria`
   heading) fails and creates nothing.
4. **Set the status deliberately** — `open` means free to assign.
5. **`blocks` edges only against epics whose files it collides with.**
6. **Label it** `mvp`, `phase-2`, `phase-3` or `infra`; no `mvp` epic behind a deferred one.

Prefer more, smaller epics — "handed over whole" and "large area" cannot both hold.

### Claiming on a shared backlog

Several people work this repo from their own machines against one shared Dolt database.

```bash
bd dolt pull                        # who took what since your last sync
bd ready && bd update <id> --claim
bd dolt push                        # publish the claim now
```

**Publish every backlog write immediately** — a create, an edit, an edge, a close — not at
session close. An unpushed bead does not exist for anybody else, and the afternoon it costs
is somebody else's. Batch with `bd batch` if you like, then push at the end of the batch.

**A claim is not a lock.** Two clones can claim the same bead; last write wins. The
protocol shrinks the race, it does not close it. Auto-push stays off on purpose (upstream
warns concurrent pushes to a git-protocol Dolt remote can strand history); if races become
routine the fix is a shared sql-server, not a shorter interval (`nocx-wj4`).

## Git authority

Agents have **standing authority to commit and push**. This overrides the "Conservative"
profile in the managed Beads block below — that block defers to repository instructions,
and this is one. Allowed without asking: `git commit`, `git push`, `bd close`,
`bd dolt push`, running the gates. Branch first if you are on `main`.

**Merging a pull request always requires explicit approval** — in that session, for that
PR. Authority to commit and push is not authority to merge.

**Run the gate CI runs, not a subset of it.** `make ci-full` is every CI job, each in the
environment its job runs in — and the four names below are the whole of `ci.yml`:

```bash
make ci-full            # all four, cheapest first
make ci                 # host-side only: the macOS `backend` job + host frontend gates
./scripts/ci-linux.sh   # `backend-linux`, ubuntu-24.04, both keyring variants
./scripts/ci-frontend.sh # `frontend`, node 24 — frontend/ AND the repo root
./e2e/run-in-container.sh # `e2e`, the same image and the same command CI runs
```

`make ci` alone is **not** the gate, whatever it used to say about itself. It covered one
of four jobs, and a release attempt and its follow-up PR both came back red from a job it
had just reported green (2026-08-10). The three containerized runners are byte-for-byte
their CI counterparts in **software** — the same image, packages, Go toolchain and
command.

**The gate belongs to whoever integrates, and to nobody else.** A worker on a branch runs
the unit tests for the files it changed, and stops there. It does not run `make ci-full`,
the containerized jobs or the e2e suite — not as diligence, not "just to be sure". The
coordinator runs all four, **once, on the merged tree, before `git push` to `main`**,
every time, including when every branch that went into it was green alone.

That is not a weakening: the failure that bought this rule was a push to `main`, and there
it binds exactly as hard as before. What comes off is a cost it never bought — and the
cost is not small. 2026-08-12, three branches, each `ci-full` green on its own tree:
`nocx-qduc`, `nocx-wwz0` and `nocx-dvql`+`nocx-5uu5`, about an hour of wall clock apiece.
The merge of them was red twice, and neither defect could have been seen from any branch
because neither existed on one: a struct literal in a test that predated a new required
dependency (a nil dereference), and a test asserting a mechanism the merge had deleted,
which had been green while asserting a value no local path read. The three per-branch full
runs found nothing the merge run did not.

Three costs, all measured the same afternoon, all invisible to the worker paying them. The
containerized jobs serialize on one Docker daemon and one CPU, so parallel workers each
running four jobs finish later than the same work run in sequence. They mount
`node_modules` as named volumes with no worktree in the name, so concurrent runs break
each other's dependency tree (`nocx-x6z3`) — measured as a pre-commit hook failing on a
package another run was mid-install. And a host-side Go run used to write to the
developer's login keychain on every backend start, which on macOS is a modal dialog
apiece: a full gate per branch was also a dialog storm per branch. That one is fixed —
`New` now refuses a test binary that has not declared whether it may reach the OS keystore
(`nocx-o4hg`) — and it is left written down because the shape recurs: `$HOME` isolation
moves directories and cannot move a per-user OS service, so the next one (Secret Service,
a launchd agent, the system clipboard) will arrive the same way and be just as invisible
to the worker paying for it.

**When the merged gate goes red, send it back to the worker, do not fix it in the
coordinator.** A worker is resumable and still holds why it wrote what it wrote; the
coordinator would be re-deriving that from a diff. **Which means the worktrees stay until
that gate is green** — removing one un-resumes its worker, and the rule above then has
nobody to send anything to. Measured within the hour of writing it: the tidy-up before
`make ci-full` cost exactly that, and the reply had to be re-briefed into a fresh worker
from scratch. The exception is a defect that exists
only in the merge — the two above were exactly that, and belonged to whoever resolved it.

**They are not their counterparts in timing, and no setting will make them so.** Each of
these scripts capped itself to the runner's 4 vCPU until 2026-08-11, on the argument that
capacity was the last gap left. Two things were wrong with it. The first is the machine
underneath, and it differs per image — read the file, not this paragraph:
`.githooks/images/ci-linux` pins `--platform=linux/amd64` **to be** the runner, so on a
Mac it runs emulated; `e2e/Dockerfile` pins no platform **deliberately**, so it builds for
the host and runs native arm64 here and native amd64 on CI. Either way throttling to four
cores does not produce the runner, it produces a third machine unlike either — `nocx-2h08`
is one starved resource in `internal/transport` reporting a 30-second timeout under a
different test name in every environment, including a run that was green on the runner and
red here at the same commit. And the cap worked by keeping timing-dependent specs
reproducible, which is what kept them alive.

So the caps are off by default, and the rule that replaces them is the stronger one:
**a test may not depend on timing.** Wait on an observable state change — a frame, a
record, a DOM state — never on a duration. A spec that needs a slow machine to pass is
broken on a fast one too; it has only not been caught yet. `NOCX_CI_CPUS` and
`NOCX_E2E_CPUS` still cap on demand, for bisecting a suspected concurrency defect. That
is a debugging tool, not the gate.

**`backend` is the one job with no container, and it is the one place local and CI still
disagree.** macos-latest is the target OS, so it cannot be containerized, and a developer
Mac is not that runner: `internal/pty` hangs to its 600 s panic here while green there,
and three tests in `internal/app` and `internal/git/local` fail here and pass there
(nocx-58gq, nocx-65v6). Until those are closed, read a local `backend` red against that
list before believing it — and never the other way round: CI is still the source of truth.

**Take the merge slot before integrating into `main`**, and release it whether you succeed
or not — a worker that forgets strands everyone behind it:

```bash
bd merge-slot acquire   # blocks/queues if another agent holds it
# merge, resolve, gate, push
bd merge-slot release   # in the failure path too; `check` says who holds it
```

Without it, two agents resolve conflicts against a `main` moving underneath both and each
resolution invalidates the other's. This is orthogonal to approval: the slot decides _who
merges next_, never _whether_.

### Every commit names its bead

```
<type>(<scope>): <imperative subject, lower case, no full stop> (<bead-id>)

<body: what was wrong, what changed, and why this way rather than the obvious
alternative. Wrap at 80. Prose, not bullets — a bullet list records what you did
and loses the reasoning, which is the only part worth keeping.>

Co-Authored-By: ...
```

- **`<type>`** — `feat`, `fix`, `refactor`, `test`, `docs`, `build`, `chore`, `perf`.
- **`<scope>`** — the module (`frontend`, `pty`, `ssh`, `session`, `transport`, `spec`,
  `beads`). Omit only when the change is genuinely repo-wide.
- **`(<bead-id>)`** at the end of the subject; several when one commit closes several
  (`(nocx-u7wq.1-.5)` for a run). Ids referenced but **not** closed go in the body.
- **No bead for it?** Then there is no task — `bd create` takes seconds. **Trivial?** It
  still had a reason, and it is the one nobody can explain in six months.

Checked by eye at review. If that rots, file a `commit-msg` hook rather than dropping it.

## Engineering rules (non-negotiable)

- **Interface-first + DI.** Every module behind an interface, wired at one composition
  root. Depend on abstractions, obey SRP, keep modules trivially replaceable.
- **Quality gates from every commit** — format, lint, test. Go and TypeScript held to the
  same bar.
- **Observability:** structured logging via `log/slog` behind the logging interface — no
  ad-hoc `fmt.Println`.
- **Clean-only:** no backward-compatibility shims (greenfield — break and refactor freely),
  no dead code, no quick-win hacks. YAGNI.
- **Respect the spine.** Never wrap PTY bytes in JSON-RPC (AD-1); the backend never sniffs
  the byte stream (AD-6); session-id is server-authoritative (AD-7). If an `AD` is wrong,
  change it in `docs/architecture.md` deliberately rather than routing around it.

### Look for the existing answer before you write a second one

**Before you add logic, find out whether the codebase already answers that question, and
extend that answer instead.** A second implementation of one concept is not duplication you
can clean up later — it is a regression with a delay fuse, because the two agree everywhere
you look and disagree somewhere you did not.

This is AD-8 stated as a working habit rather than a module boundary: one owner per
behaviour, and the owner is whoever already has it. It applies to a predicate, a derivation,
a table or a surface just as much as to a package.

Three questions, before the first line:

1. **Does something already decide this?** `grep` for the concept, not your name for it —
   "is this an ssh context", "which command is this token under", "may this be integrated".
   The existing answer is often two words away under a different word.
2. **Can it be extended?** A table that grows by addition, a parameter, one more variant.
   Extending keeps one truth; adding keeps two and hopes they stay in step.
3. **If it genuinely cannot**, say in the code why the existing one did not fit — the next
   person needs to know it was considered, not guess that it was missed.

**Two surfaces may never own the same input.** If a key, a position or a document state can
be claimed by two components, that is the defect, whichever one wins by evaluation order:
the loser goes on advertising what it can no longer deliver.

> 2026-08-05. "Am I in an ssh context" had two derivations — `commandWord(ctx)` on the
> completion side, and `/\bssh\s+/` in the editor. They agreed for every case anyone tried,
> and disagreed on exactly one: `ssh` with no trailing space, which is the state a user is in
> when they press Tab **instead of** the space. So the suppressed surface un-suppressed
> itself at the only moment it mattered and inserted a saved host over the user's choice.
> Underneath it, a whole second suggestion surface — its own list, keys, rendering and accept
> path — had been kept alive beside the completion dropdown, which already rendered the same
> candidates as a row and a ghost. The fix was to delete it, and the bug existed only because
> it had been built rather than found.

### Before you build a UI component: read the kit

**Read [`frontend/src/ui/README.md`](frontend/src/ui/README.md) and list `frontend/src/ui/`
first.** The inventory names every component, its identity classes and its variance.

1. **Does the kit have it?** Import it. A "toggle" is `Checkbox variant="switch"`; a status
   message is `showToast`; a titled group is `Section`/`PageSection`. At 90% fit, add the
   missing variance as a typed `data-*` rather than forking.
2. **Close enough?** Extend that component in `ui/` — the kit grows by variants, not
   near-duplicates.
3. **Genuinely new?** Into `ui/`: one module, one CSS file in `styles/components/`, a
   stable identity class, a test, a row in the README table.

Never build the control **inside the surface** — a hand-rolled `<div class="st-something">`
with its own colours, a bespoke button, a "temporary" status div. Each is a second
vocabulary for one concept, which is the defect two epics (`nocx-pp3y`, `nocx-v0ai`) spent
themselves unwinding.

A surface may **place** a kit component (`flex`, `margin`, `width`, `order`, `align-self`,
`position`) and may never **repaint** it (`background`, `border`, `color`, `font-*`,
`padding`, `box-shadow`). Wanting to repaint means the component is missing a variant — add
it there. If the kit is genuinely wrong, change the kit deliberately, the way an `AD` gets
changed.

## Stack

- **Backend:** Go — `pty`, `ssh` (`golang.org/x/crypto/ssh`), `session`, `transport`,
  `settings`. One core, multiple build targets.
- **Frontend:** xterm.js (WebGL) + TypeScript, CodeMirror 6 for the editor. Terminal render
  state lives here (AD-6) — [ADR-0001](docs/decisions/0001-xterm-js-as-vt-frontend.md).
- **Desktop shell:** Wails v3 (macOS first).
- **Transport:** one WebSocket — raw **binary** data plane + **JSON-RPC 2.0** control
  plane (AD-1).

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
