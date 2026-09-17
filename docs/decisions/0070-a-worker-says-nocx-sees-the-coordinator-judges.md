# ADR-0070 — A worker says, nocx sees, and only the coordinator judges

- **Status:** Accepted
- **Date:** 2026-09-17
- **Supersedes:** D2, D6 and D9 and §7.2 of the 2026-08-24 orchestration mechanism design;
  the drop and the declaration of [`docs/lifecycle-protocol.md`](../lifecycle-protocol.md)
  §16; the declaration half of [ADR-0024](0024-authenticated-shell-integration-channel.md)
  decision 2 (enrolment stays on that channel). Amends **AD-6**'s list of what a grid may
  decide. No record is edited.
- **Related:** design `.internal/specs/2026-09-17-worker-reports-and-the-coordinator-wake-design.md`;
  mesh design P1–P7 (kept). Beads `nocx-luqz9`, `nocx-9f1d4`, `nocx-k2csf`.

## Context

A worker's state was decided by two facts only: its process exit and its declaration — `ok`
or `fail` written to a file the shell wrapper sent after the agent exited. The screen was
forbidden to decide anything about a worker, so that a misread frame could never produce a
false completion.

That design assumed an agent that exits when it is done. `workers.spawn` starts an
interactive Claude, which ends its turn and waits. The declaration therefore never arrived
while the worker lived, `workers.wait` held to its deadline (`nocx-9f1d4`), and the one party
that could end the worker was the coordinator blocked in the wait. The product could not do
what herdr does every day.

## Decision

1. **The worker says.** A worker reports through an MCP tool, `workers.report`, with a kind
   (`done`, `question`, `progress`) and its own text. The call travels on the tool endpoint
   under the pane's bearer, so it works from a remote host and needs no file. A report is a
   claim.
2. **nocx sees.** The pane observer's classification of a worker's screen — idle, blocked,
   exited — is delivered to the coordinator's mailbox as an observation, after it has held
   for a settle window. It carries no screen content.
3. **Only the coordinator judges.** nocx records no outcome. The record's states are
   `prepared`, `live`, `exited`, `closed`, `interrupted`; an observation moves none of them.
4. **The wake is nocx's, not an LLM's.** When the coordinator is idle and has unread mail,
   nocx types one pointer line; it retries a bounded number of times and then calls the
   human, and calls the human at once for a coordinator that is blocked while holding live
   workers.
5. **AD-6 gains a third power**: a grid may tell a coordinator what its own worker's pane
   shows. Its list of what a grid may never decide — wave state, lifecycle attempt, execution
   attempt, network destination — stands verbatim.

## Why this rather than the alternatives

- **Keep the declaration and make the worker exit.** An interactive worker that exits loses
  its context, and the coordinator loses the ability to give it more work — the reason
  workers are interactive at all.
- **Deliver the declaration while the agent runs** (a signal to the wrapper, a watched file).
  It keeps a verdict in nocx that nocx cannot check, and a file on a remote host needs a
  carrier the tool endpoint already is.
- **Let the screen decide completion.** A worker idle at its prompt may have finished, given
  up, or misunderstood; telling them apart needs meaning. The observation says only what is
  visible, and the coordinator — which has the meaning — decides. A misread frame now costs an
  unnecessary wake, which is recoverable, and still never a false completion, because nothing
  records one.
- **A separate tool per kind, or a blocking `ask`** (Orca's shape). One mailbox is read by one
  tool, so one write tool with a kind keeps a new kind an enum value. A blocking ask needs a
  timeout and a resume; a worker that asks and ends its turn needs neither, because nocx can
  type the answer as its next message.

## Consequences

- `workers.wait`, `NOCX_AGENT_REPORT`, the declaration record and `completed` / `failed` /
  `abandoned` are deleted, with their schemas and documentation.
- The per-fact deadline backstop becomes the idle-coordinator retry of decision 4.
- Hooks (`nocx-7faow`) later make the observation authoritative while they report; they do
  not reintroduce a verdict.
- A worker that never reports is still visible: its idle, blocked and exited observations reach
  the coordinator whether or not it called anything.
