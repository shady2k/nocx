# ADR-0079 — A worker's conversation resumes after the backend restarts, from a minimal durable restart record

- **Status:** Accepted
- **Date:** 2026-10-05
- **Decided by:** the owner, 2026-10-05, in the herdr-parity scope decision recorded in
  `docs/milestones/v0-6.md`: the durable worker restart record is approved ("#3 durable worker
  record (supersedes D5): APPROVED by the owner via this plan (nocx-xn63t.5.1). Record it as a
  new ADR superseding D5, not by editing the spec.")
- **Supersedes:** D5 of
  `.internal/specs/2026-08-15-workspaces-lineage-and-orchestration-design.md` **in one half
  only**: the sentence "at stage 1 workers die with the backend". Everything else D5 defers —
  PTY survival, daemon reattach, cross-host replay, orphan reaping and adoption — stands, and
  this record does not reopen it.
- **Related:** `nocx-xn63t.5` and its tasks `nocx-xn63t.5.1` (the durable record) and
  `nocx-xn63t.5.2` (the restore-and-relaunch); the launch record epic `nocx-dz9vj`
  (command/args/env and the resume fields); ADR-0024 decision 2 (enrolment channel);
  `AD-7` (session identity is server-authoritative).

## Context

D5 (2026-08-15) deferred worker durability as a whole: at stage 1 a worker dies with the
backend, deliberately — herdr's behaviour. The herdr-parity stage 5 (`nocx-xn63t.5`)
requires the opposite for the owner's day: "nocx on the VM restarts; every tab that held a
claude or codex worker comes back in its worktree with that agent's conversation resumed".

The key that makes both true at once: a conversation is resumed by **launching the agent
against its recorded identity** — claude `--resume <id>`, codex `resume [SESSION_ID]`,
omp's resume, prime-agent `-r <id>`. The PTY, the helper session and the live coordinator
state die with the machine, and that is fine; the agent's own conversation store lives on
disk and survives a reboot. What must be durable is therefore a **small restart record** —
worker-to-pane, agent id, launch directory/worktree, resume id/mode — not the worker's live
state.

## Decision

A minimal **worker restart record** is persisted (per worker, under the app directory),
holding: the pane identity, the agent id, the launch directory/worktree, and the resume
id/mode the launch record (`nocx-dz9vj`) defines. On backend start after a restart, a tab
that held a worker **relaunches its agent with the recorded resume identity**; a tab whose
agent cannot resume — no resume support, a lazy session id, a checkout that is gone — says
so in the tab and never opens an empty shell silently. Until restart succeeds, the record's
process state is **interrupted**, never reported live. The live worker store
(`internal/workers/store.go`'s `MemoryStore`) stays the store for live workers; the restart
record is a separate minimal table/JSON read only at startup restore.

## Why this rather than the obvious alternative

A full durable worker store (live state, checkpoints, reattach) is what D5 deferred, and it
is beyond herdr parity — the owner cut the milestone to parity on the same day. Nothing
durable at all would make the stage's own criterion untestable: the working day would end at
the first VM reboot, and herdr's parity behaviour (tabs come back, conversations resume) is
exactly what the owner asked 0.6 to match.

## What the next person inherits

`nocx-xn63t.5.1` implements the record (a test reopens the backend against the same app
directory and resolves the full tuple); `nocx-xn63t.5.2` implements the restore and the
"cannot resume, and here is why" surface; the resume fields themselves are defined by
`nocx-2txuc` under `nocx-dz9vj`, which this record does not change.
