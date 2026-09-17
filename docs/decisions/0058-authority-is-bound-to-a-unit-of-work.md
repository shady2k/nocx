# ADR-0058 — Authority is bound to a unit of work, never to a container

- **Status:** Proposed
- **Date:** 2026-09-06
- **Related:** [ADR-0020](0020-the-agent-gets-a-lane-authority-is-granted-per-run.md)
  (`D5`: authority is granted per run; a container never confers it),
  [ADR-0028](0028-eino-runs-the-loop-the-grant-is-ours.md) (the dispatcher narrows,
  it does not check), [ADR-0060](0060-supervision-outlives-the-coordinator-not-the-backend.md)
  (which cites "authority per run, never per session"), AD-7 (the session is
  backend-owned), AD-8 (one owner per behaviour).
- **Design:** `.internal/specs/2026-09-03-the-waves-authority-model-design.md` §5,
  whose `A12` is the ceiling this record does not raise.
- **Refines:** ADR-0020 `D5`. It does not amend it: `D5` is correct as written for
  the caller it was written about, and this record says what its rule means for a
  caller that has no run.
- **Bead:** `nocx-rowqt.4`.

## Context

nocx has two callers for one wave dispatcher, and the second one arrived with a
shape ADR-0020 does not describe.

The in-process caller is the built-in assistant. Its unit of work is a **run**, and
`D5` fits it exactly: the grant is minted per run at
`internal/transport/ws_readscreen.go`, immutable once the run starts, and recorded
on the run — which is what makes the blast-radius question a query. "This run held
a grant for these environments and touched these three sessions on two hosts" is
answerable because there is a row to ask.

The second caller is a subscription CLI — `claude` typed by hand in a nocx pane —
reaching the same dispatcher over a private unix socket (`internal/waveendpoint`).
It has no run. `internal/app/waveauth.go` builds its `RunContext` with `Session`
and `Workspace` filled in and **`RunID` empty**, because there is nothing to put
there: the person did not start an execution, they started a shell that will call
five methods over an hour.

So the literal reading of `D5` says the external caller may hold no authority at
all, and the literal reading of ADR-0060's "never per session" says the obvious
alternative — a grant that lives as long as the session — is the one thing
forbidden. Both readings are about the same fear, and it is a real one: a
container that confers authority, so that dragging a tab or leaving a shell open
silently widens what may be touched.

## Decision

**The unit of authority is a bounded interval of work with a named closing event.
The run is the in-process instance of that. The external coordinator's instance is
the ADMISSION INTERVAL, and the session is not an instance of it at all.**

The admission interval has both ends, which is what makes it a unit rather than a
container:

- It **opens** when a pane enrols through the lifecycle channel (`agent_enrol`) and
  a peer on the socket is pinned to that session's backend-owned process tree as a
  `(pid, startTime)` root — `internal/wavepin`, per `nocx-rowqt.8`.
- It **closes** on `agent_withdraw`, on connection loss, or when the session ends.
  `internal/app/waveauth.go` holds one live coordinator slot per session and
  releases it at exactly that point; a call from the same tree after withdraw is
  refused.
- Within it the grant is **immutable**, minted from the admitted session rather
  than from anything the peer sent — including the environment scope, which is
  derived from the session's own kind and host rather than assumed.

A session outlives many admission intervals, and holds no authority between them.
That is the whole of the distinction ADR-0060 was protecting, and it survives.

## Why this rather than the obvious alternatives

**Minting a synthetic run per admission** was the first idea and it is worse than
it looks. A run in this codebase is not a label — it carries a lease, deadlines, an
output budget, an interactivity policy and a termination reason (ADR-0020 `D2`,
`D4`), all of which would be fabricated for a coordinator that executes nothing.
An empty shell of a run is a lie a stranger would later read as data.

**Amending ADR-0020** was considered and rejected: `D5` is accepted, ADR-0060 cites
it, and re-deciding a settled question inside another task is how it stops being
settled. This record cites it instead, and leaves it as it stands.

**Granting per session** is what both prior records forbid, and this one does not
do it — see the closing event above.

## What this does NOT claim, and who owes it

**The accountability half of `D5` is not met.** The grant is recorded nowhere: no
row names the admission interval, so "which caller held a grant for this
environment" cannot be asked of the external path the way it can of a run. That is
the actual cost of having no run, it is not repaired by this record, and it is
`nocx-rowqt.13`.

**This is not human approval.** The admitting act is enrolment — a pane a person
opened — and not the 2026-08-15 `D13` approval in which a human admits an agent
once, seeing the executable and the scope. That is `nocx-rowqt.12`, it is not
built, and until it is, nothing in the product may describe this endpoint as
human-approved.

**The ceiling stays `A12`.** A wave call may do nothing a same-uid actor on that
machine could not already do through the session itself. The pin separates trees
within one uid; it is not a defence against that uid.

## What the next person inherits

When the D13 approval arrives (`nocx-rowqt.12`), it mints the interval rather than
replacing this shape: an approved agent still works inside an interval with two
ends, and the approval changes who may open one, not how long it lasts. When the
ledger arrives (`nocx-rowqt.13`), the row it writes is keyed by the interval, which
is why the interval has an identity in the first place.
