# ADR-0056 — Supervision outlives the coordinator, not the backend

- **Status:** Accepted
- **Date:** 2026-09-04
- **Related:** [ADR-0024](0024-authenticated-shell-integration-channel.md) (the
  authenticated channel a participant declares over),
  [ADR-0020](0020-the-agent-gets-a-lane-authority-is-granted-per-run.md)
  (authority per run, never per session),
  [ADR-0043](0043-one-connection-to-the-encrypted-store.md) (one connection on the
  content store), and AD-7 (the session is the backend-owned identity).
- **Bead:** `nocx-dkawo.3`. The design it implements is
  `.internal/specs/2026-08-24-orchestration-mechanism-design.md`, D1, D2 and D14.

## Context

The backend holds a wave record so a coordinator agent cannot forget its
children (D1). An agent exists only while it takes a turn, so anything that
depends on an act the agent must remember is not an invariant; the backend is a
process without turns, and supervision is therefore its.

That record holds six things. Five of them are rows in the encrypted content
store — participants, delegations, liveness, the two terminal facts. The sixth
is **undispatched facts and their deadlines**: what needs judgement, who must
judge it, and when it reaches a person if nobody does.

The obvious reading of "the backend holds the record" is that all six are
durable, and it is worth writing down why the sixth is not. It is the question
the design itself left open as §10.7 — «does the record survive a BACKEND
restart?» — narrowed to the one part of it this slice had to answer.

## Decision

**The undispatched fact set lives in memory, and a backend restart closes it
rather than carrying it.**

The five durable things are unchanged. Only the sixth is scoped to one backend
lifetime, and the same restart that discards it terminalizes every participant
it could have described.

## Why

Three facts about a restart make a durable fact set describe nothing:

1. **The participant is already judged.** The startup sweep terminalizes every
   non-terminal participant as `interrupted`, because the worker died with the
   backend that held it and no pin exists in this tree that could prove a
   process found at the far end was ours if it were not. A fact that survived
   the restart would ask for judgement about a record the same restart has
   already closed.
2. **The coordinator is gone.** Authority is per run (ADR-0020) and the wake is
   a keystroke into a live pane. After a restart there is no run and no pane, so
   the fact's first route does not exist.
3. **The person would be told about a wave that no longer exists.** The second
   route survives — a notification can always be raised — but what it would say
   is that a worker is waiting for a coordinator that cannot be woken, about
   work nothing is doing. That is a worse answer than silence, because it asks
   for an action there is nothing to act on.

The rejected alternative is a durable set with a reconciliation pass at startup.
It costs a table, a migration rung under ADR-0055, and a startup reconciliation
whose only correct behaviour is to discard everything it reads — which is the
in-memory design with extra steps and one more schema to keep in step.

## What this does NOT decide

**What the person is told about the restart itself.** A backend crash during a
live wave is an unsupervised interval by construction, and §10.7 of the design
asks what the human learns when it happens. Nothing here answers that; the sweep
writes `interrupted` and says so in the log, and whether that deserves a
notification is open.

## Consequences

- A wave that spans a restart is reported through the participants' own
  `interrupted` state, which is durable, and not through a pending fact.
- "Nothing ticks while there is nothing to dispatch" is checkable rather than
  claimed: an empty set and no armed alarm are the same statement, which is D2's
  whole advantage over the lease it replaced.
- If a backend restart during a live wave turns out to be common, this record is
  what has to be revisited — D1 would then have put supervision in a process not
  durable enough to hold it, and §10.7 becomes a decision rather than a question.
