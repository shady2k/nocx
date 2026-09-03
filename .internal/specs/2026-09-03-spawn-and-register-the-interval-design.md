# Spawn-and-register: the interval, both its ends, and what the next start does

- **Date:** 2026-09-03
- **Bead:** `nocx-u0apy`, a child of `nocx-dkawo`
- **Status:** approved by the owner, 2026-09-03 — delegated review; see
  `.internal/plans/2026-09-03-the-wave-record.md` §0 for the two citation corrections applied below
- **Closes:** open question 5 of `.internal/specs/2026-08-24-orchestration-mechanism-design.md` §10
- **Reads with:** `.internal/specs/2026-09-03-the-waves-authority-model-design.md`, whose `A12`
  decides what recovery is allowed to do

## 1. This specification has been written once already

`internal/helper/session/service.go:451-568` is `Service.spawn`, and its doc comment is the
document this bead asks for, one scope down:

> **The ORDER here is the whole of the partial-failure story, and each step names what is true if
> the next one fails:**
>
> 1. clamp the bound and reserve it against the aggregate budget. A refusal here has forked
>    NOTHING — a budget checked after the fork would leave an orphan shell every time it refused.
> 2. mint the id and spawn with it. … A spawn or id failure releases the reservation and returns
>    with no entry or process.
> 3. allocate the window, record the launch and register. Only now is the session findable —
>    which is the opening end of the interval "a session is in the inventory from the moment its
>    PTY exists".
> 4. start the output pump and the exit watcher. Both are attached to a session that already
>    exists, so a process that exits between step 3 and step 4 is still observed: the watcher
>    sees an already-closed Done.

`internal/transport/ws_readopt.go:62-159` states the method in one sentence — **"THE ORDER IS THE
ROLLBACK"** — and the reason it works: the step that can fail for a reason the caller cannot undo
goes FIRST, so the failure path is one removal and nothing else.

`internal/app/session_reconcile.go:228-253` supplies the recovery discipline: **"A FAILURE IS
NEVER A VERDICT."** An inventory that could not be reached produces `unknown`, and the only path
to `absent` runs through an inventory that ANSWERED. Its `causeFor` cannot return a verdict at
all — the type says so.

`internal/update/journal.go:51-54` supplies the rule for the crash case: **"Reconciliation
observes which identity sits at which path and derives state from that — the record never claims
what happened."**

This document is those four, applied to a wave participant. Nothing here is invented.

## 2. Whether a journal is needed at all

`internal/vaultreset/vaultreset.go:14-28` refuses one, and gives the test:

> A durable record with phases and a resume-on-startup path was considered and rejected: **it
> exists to support deferred background cleanup, and there is none here.**

Applied here the answer splits, and the split is the design:

- **Within one backend lifetime there is no deferred cleanup**, because every compensation is
  available immediately and synchronously. So no journal: **ordering is the rollback**, exactly as
  in `Service.spawn`.
- **Across a backend restart there is no deferred cleanup either**, because the 2026-08-15 `D5`
  makes stage-1 workers die with the backend. A record that outlived them would describe nothing.

But the record IS durable while the process is not, so the sixth cut is real: a crash leaves a
record saying `live` for a process that no longer exists. **That case already has a shipped
answer, and it is the executions rule rather than the sessions rule.**

`internal/content/sqlite.go:415-455`, `closeUnanchoredEntries`, run from `Open`, unconditionally:

```sql
UPDATE executions SET state = 'interrupted', termination_reason = 'interrupted', ended_at = ?
WHERE state IS NOT NULL AND state NOT IN ('completed','cancelled','failed','interrupted')
```

with the reason at `sqlite.go:405-412`: the executions half was deliberately NOT narrowed, "for
the same reason `ask` is closed here: an execution with a state is an agent run … and an agent run
is **this process's own work**."

**A wave participant is that same class.** Under `D5` it is this process's own work and it dies
with this process. Sessions get the other treatment — carried over in memory, judged later by
`reconcileSessions`, never judged by `Open` — because a helper may still be holding them.
A participant has no helper holding it.

**Decision: no journal. Ordering within the lifetime, and the existing unconditional startup
terminalizer across restarts.** If `D5` is ever repealed — workers surviving the backend, which
is what the helper epic is for — this decision is repealed with it, and a participant becomes a
session-class record with a carry-over set. That is stated here so the next author does not have
to derive it.

## 3. The interval

> **A participant record exists from before the first irreversible spawn effect, until either the
> enrolled incarnation is supervised, or the record is durably terminal.**

**The opening end** is the commit of the participant row in `prepared`, and it is before the
spawn for the vault journal's reason (`internal/vault/journal.go:11-13`): "the journal is written
first, then the provider call, because a go-keyring call that times out may still complete later —
and an unjournalled late write would be permanently undiscoverable." A spawn that times out may
still have forked. A fork nobody recorded is the same undiscoverable orphan.

**The closing end is two events, and neither is sufficient alone** — the shape
`internal/transport/ws_panegrid.go:45-68` already uses for the grid, whose withdrawal has an end
that "does not depend on the enrolling shell surviving to send its own withdrawal":

1. supervision is attached to a live enrolled incarnation, or
2. the record reaches a durable terminal state — by a compensation in this procedure, by the
   participant's own terminal declaration, by process exit, or by the startup terminalizer of §2.

## 4. The order, and what each step leaves if the next one fails

| #   | Step                                                                                                                                                                                                                                         | If the NEXT step fails, this is true                                                                                                                                                                                                                                                                                                                                                                  |
| --- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | **Validate authority and reserve the bound.** The delegation the coordinator holds is checked, and one participant slot is reserved against the wave's bound and against `panegrid.MaxEnrolled`                                              | **Nothing has been forked, and no record exists.** A refusal here is free, which is the whole reason it is first (`Service.spawn` step 1: "a budget checked after the fork would leave an orphan shell every time it refused")                                                                                                                                                                        |
| 2   | **Commit the participant record in `prepared`, in the same transaction as its wave membership and its lineage edge.** The participant id is minted here                                                                                      | A durable participant in `prepared` with nothing behind it. **This is cut C1 and it is deliberately reachable** — it is the state the interval exists to make discoverable. It is not `live`, so nothing reads it as a running worker, and §5's recovery terminalizes it                                                                                                                              |
| 3   | **Create the session and spawn the launcher**, with the participant id already minted and passed in — `Reg.Open`'s method (`internal/session/session.go:484-588`): the id exists before any connect, so "a failed connect registers nothing" | A live launcher and a `prepared` record. **C2 is not reachable as "a live agent nobody owns"**, because the record preceded the fork. The compensation is: kill the launcher, terminalize the record, release the reservation                                                                                                                                                                         |
| 4   | **The launcher enrols BEFORE it `exec`s the real agent**, and refuses visibly if it cannot (2026-08-24 §7.1 steps 2–5)                                                                                                                       | **C3 — an ordinary full agent with no reporting surface — is closed on the far side, not here.** `D4` fail-closed is satisfied by the launcher never reaching its `exec`, so the unorchestrated agent is never created. The backend's duty is only to not leave a record outliving a launcher that refused: an enrolment that never arrives terminalizes the record on the reservation's own deadline |
| 5   | **Commit the delegation** (`A2` of the authority design), keyed to the coordinator's controller session                                                                                                                                      | An enrolled, authenticated participant no coordinator may address. **C4.** The compensation is symmetric and available: withdraw the enrolment, kill, terminalize. It is reachable only inside one backend lifetime, because a crash here takes the worker with it                                                                                                                                    |
| 6   | **Mark the participant `live` AND attach supervision, in that order, with supervision attached to a record that already exists**                                                                                                             | **C5 — live but unsupervised — is unreachable by construction, and this is `Service.spawn` step 4's trick.** Supervision attaches to a record that exists, so a process exiting between the two is still observed: the watcher sees an already-terminal process rather than missing the transition. The ordering, not a lock, is what makes it race-free                                              |

**"Started" is never inferred from a dispatch returning.** This is the memory
`playbook-orca-workers-omp-pi-how-to-run` written into a mechanism: _dispatch is not delivery,
and "dispatched" is not proof of start_. The record moves to `live` at step 6 on the strength of
an enrolment that arrived, never on the strength of step 3 returning without error.

## 5. Recovery, per cut

**Recovery here can only terminalize. It may never adopt, and that is a consequence, not a
choice.** Adoption requires identifying the process found at the far end as the one we spawned —
`A12` and §5 of the authority design record that no such pin exists: no `pidfd` anywhere in the
tree, start-time read only remotely and only as a diagnostic, `SO_PEERCRED` with no carrier on a
socketpair. `internal/procwatch` answers "is the executable I launched still the one running under
that pid", but its own header says an observation is a GUESS and the contract says "never
authority". It may therefore inform a RECORD decision and may not confer authority — which is
enough to decide terminalize-versus-leave, and not enough to decide adopt.

| Cut | State found                                             | Action                                                                                                                                                                                             |
| --- | ------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| C1  | `prepared`, no enrolment, reservation past its deadline | Terminalize the record; release the reservation. Nothing to kill                                                                                                                                   |
| C2  | `prepared`, a launcher was forked                       | Kill the launcher; terminalize; release. Within a lifetime this is the synchronous compensation, not a recovery pass                                                                               |
| C3  | Cannot occur — see step 4                               | —                                                                                                                                                                                                  |
| C4  | Enrolled, no delegation                                 | Withdraw the enrolment, kill, terminalize                                                                                                                                                          |
| C5  | Cannot occur — see step 6                               | —                                                                                                                                                                                                  |
| C6  | Any non-terminal state, after a backend restart         | **`interrupted`**, unconditionally, by the existing `closeUnanchoredEntries` sweep. Not adopted, because `D5` says the worker is gone and `A12` says we could not prove it was ours if it were not |

**A failure is never a verdict**, inherited verbatim from `session_reconcile.go`: a compensation
that itself fails leaves the record non-terminal and retries, and never writes a terminal state it
did not establish. The one asymmetry worth stating: `interrupted` costs a record that outlives its
process by one restart; a wrongly adopted participant would cost a coordinator addressing a
process that is not its worker. They are not symmetric and the code must not treat them as though
they were.

## 6. How this is tested

The house pattern, and it is not a new one. `internal/shellintegration/publisher_fault_test.go:21`
is the canonical instance: a wrapper double over the real dependency, failing either a named op or
the Nth call of a kind, with the selector rule stated at `:67` — "an ordinal says WHEN a fault
fires and is what an enumeration needs; a path says WHERE".

`TestFaultAtEveryBoundaryConverges` (`:244`) is the shape to copy exactly, and its two properties
are the acceptance criteria of this bead:

- The table is **discovered, not hard-coded**: one clean run on a recording double counts the
  calls per kind, and every ordinal from 1 to that count becomes a subtest. A hand-written list of
  cuts goes stale the first time a step is added; a discovered one cannot.
- The outcome is **read from the state, not predicted from the ordinal**: "Both are legitimate; a
  torn state in between is not."

Then the fault is healed and the retry must converge, and every assertion reads through a
**freshly constructed reader over the same path** (`reopenStore(t, path)`,
`internal/content/ledger_agent_test.go:907` and eighteen other call sites), never through the
fake's own memory.

For the restart cut specifically, `internal/content/reconcile_test.go:287`
(`TestARefusedAbsentSweepLeavesNothingHalfJudged`) already has the shape: a `RAISE(ABORT)` trigger
on a second connection, then a reopen, then "the next pass completes exactly what the first one
could not".

**One gap worth naming rather than inheriting:** `internal/app/session_reconcile_test.go` has call
counters but no Nth-call failure, so the reconciliation pass this design extends **has no
partial-failure test today**. That is `nocx-cujkz`, filed rather than fixed here.

## 7. Deliberately out

- **Adoption of a found process** (§5). It returns when a pin exists — `nocx-zo2ng` and the
  enrolment work — and this document is where the next author should look for why it was refused.
- **Worktree creation** as part of step 3. `worktree create` returning workspace, tab, root pane
  and worktree in one call is the wave primitive herdr has and we do not; it is layer 3 of the
  `nocx-dg51h` ordering and it slots into step 3 without changing the interval.
- **The reservation deadline's value** (step 4). A number that is wrong in either direction
  breaks it, and 2026-08-24 open question 8 already owns "what the deadline actually is",
  measured in `nocx-dkawo.4` rather than guessed here.
- **What the human is told when a restart interrupts a wave** — 2026-08-24 open question 7,
  which this document sharpens but does not close: the sweep now has a defined effect on
  participants, and the notification is still undesigned.

## 8. Assertions

1. A participant record exists before any fork attributable to it. Asserted by a double that
   fails the spawn and finding the record already present through a fresh reader.
2. A refusal at step 1 leaves no record, no reservation held, and no process.
3. A launcher that cannot enrol never `exec`s the agent, and the pane says so (`D4`).
4. A participant is `live` only after an enrolment arrived — never because a dispatch returned.
5. A process that exits between marking `live` and attaching supervision is still observed as
   exited (step 6's ordering).
6. After a simulated backend restart, every non-terminal participant reads `interrupted`, and
   none reads `live`.
7. A compensation that itself fails leaves the record non-terminal, and a second pass completes
   it.
8. The fault table is discovered from a recording run, and every ordinal in it converges after
   the fault is healed.
9. No path in this procedure adopts a process it did not observe being spawned in this backend
   lifetime.

## 9. What would falsify this design

- **A participant marked `live` on the strength of a dispatch returning.**
- **Supervision attached before the record exists**, which reopens C5.
- **An adoption path**, while no pin exists — it would be claiming the authority `A12` refuses.
- **A journal**, unless `D5` is repealed first. A journal without deferred cleanup is the thing
  `vaultreset` rejected by name.
- **A hand-written table of cuts** in the test, rather than a discovered one.
