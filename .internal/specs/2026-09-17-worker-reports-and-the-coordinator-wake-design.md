# A worker reports, nocx watches, and the coordinator is woken — design

- **Date:** 2026-09-17
- **Epic:** `nocx-luqz9` (absorbs `nocx-k2csf`), stage `nocx-xn63t.4` of the herdr replacement.
- **Decided with the owner** in a grilling session on 2026-09-17. Vocabulary: `CONTEXT.md`.
- **Record:** [ADR-0070](../../docs/decisions/0070-a-worker-says-nocx-sees-the-coordinator-judges.md).

## 1. Why

A coordinator today learns about a worker only from `workers.wait`, which answers on a
declaration the wrapper sends after the agent process exits (`NOCX_AGENT_REPORT`,
`docs/lifecycle-protocol.md` §16). An interactive Claude does not exit when its turn ends, so
the wait holds to its deadline (`nocx-9f1d4`), and the only party that could end the worker is
the coordinator that is waiting. herdr cannot be uninstalled while that is the loop.

## 2. Binding documents crossed

| Document                                                     | What it decided                                                                              | What this design does                                                                                                    |
| ------------------------------------------------------------ | -------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| **AD-6** (the list of what a grid may never decide)          | A grid assigns no wave state; two powers only                                                | Amended by ADR-0070: a third power, telling a coordinator what its worker's pane shows. It still assigns no record state |
| 2026-08-24 orchestration design **D2**, **D6**, **D9**, §7.2 | Only exit and declaration decide state; per-fact deadline to the human; `wait` a convenience | Superseded: no declaration, no outcome states, deadline replaced by §5.4                                                 |
| `docs/lifecycle-protocol.md` **§16**                         | The drop and the declaration over the authenticated channel                                  | Removed (the drop); the channel's enrolment role is untouched                                                            |
| **ADR-0024** decision 2                                      | The authenticated channel carries enrolment and declarations                                 | Declarations leave it; a report travels as an MCP tool call on the tool endpoint (the pane's bearer, ADR-0058)           |
| 2026-09-03 mesh design **P1–P7**                             | Checkpoints append-only, never a completion report, never wake (P4)                          | Kept. A checkpoint is `kind: progress`; `done` is a separate kind                                                        |
| 2026-09-11 coordinator-surface design §4.2, §4.3–4.5, §8     | Wake without LLM; preamble; checkpoints; wait and drop removed; outcome open                 | Implemented here; §8's outcome question answered in §4                                                                   |
| **AGENTS.md** testing rules 1–5                              | User-path tests, one happy path, failure intervals, wire contracts                           | §8; every new tool result gets a schema in `contracts/`                                                                  |

## 3. Vocabulary

`CONTEXT.md` defines **Coordinator, Worker, Idle, Blocked, Exited, Report, Outcome, Mailbox,
Wake**. The words `completed`, `failed`, `abandoned`, `declaration` and `drop` leave the
product with this epic.

## 4. Decisions

1. **Nobody but the coordinator judges an outcome.** nocx records no success or failure. The
   worker record's states become `prepared`, `live`, `exited`, `closed` (by the coordinator)
   and `interrupted`; `completed`, `failed` and `abandoned` are removed with `reduce`'s
   conjunction.
2. **Two ways nocx learns about a worker, never merged.**
   - **The worker says:** one MCP tool, `workers.report { kind, text }`, with
     `kind ∈ {done, question, progress}`. The text is the worker's own words, carried in the
     call — no file, so it works for a worker on a remote host through the helper's lane. It
     is a claim, not a verdict.
   - **nocx sees:** the pane observer's classification of the worker's screen, mapped to
     **idle** (`free_text`), **blocked** (`permission_choice`, `modal_choice`, `error`),
     **working** (`working`, `unknown`), and **exited** (process exit).
3. **Everything goes to the coordinator's mailbox as a pointer-bearing message.** Report
   messages carry the worker's text; observed messages carry only worker, state and time —
   never screen content. Observed idle is recorded every time the worker settles idle,
   including right after a `done` report: a report and a state are different facts.
4. **A state is recorded only after it has held for the settle window** (default 3 s). The
   pane observer sweeps once a second (was 120 ms).
5. **Reading.** A coordinator reads its mailbox with `workers.inbox` (the same tool a worker
   uses for its own). `workers.holdings` remains the snapshot of what its session holds.
   `session.read` stays the current screen only; when that is not enough the coordinator asks
   the worker with `session.message`. Reading an agent's transcript file and hooks are later
   epics (`nocx-7faow` for hooks; the transcript epic filed beside it).

## 5. The wake

1. **What wakes.** New unread mail of kinds `done`, `question`, or an observed `idle`,
   `blocked` or `exited`. `progress` never wakes (P4).
2. **When.** Only while the coordinator is itself idle. nocx types one line — "nocx: you have
   N new messages from your workers. Call workers.inbox." — and never a worker's words. While
   the coordinator is working nothing is typed and no timer runs.
3. **Once per batch.** A second line is typed only for mail that arrived after the last line,
   or as a retry (below). Reading the mailbox clears the batch.
4. **Retries and the human.** If the coordinator stays idle with the batch unread, the line is
   retyped after the retry pause (default 2 min), up to the attempt limit (default 3); after
   that the human is notified that coordinator X is not reading its workers' mail. **If the
   coordinator is blocked while it holds live workers, the human is notified at once** after
   the settle window, mailbox empty or not: nothing is being coordinated.
5. All four numbers (sweep, settle window, retry pause, attempts) are injected parameters, and
   no test depends on their values.

## 6. The worker's preamble

`workers.spawn` types a preamble before the task: who its coordinator is, the tools it has, and
the reporting rules — call `workers.report` with `done` when finished and `question` when it
cannot continue without an answer, then end the turn; record `progress` checkpoints at
milestones. A question is not a blocking call: the worker asks and ends its turn, and the
answer arrives as its next message. Role rules taken from the repository stay deferred
(`nocx-k2csf.1`).

## 7. Removed

`workers.wait` (tool, schema, registrar `Wait`), `NOCX_AGENT_REPORT` and the drop in
`nocx.bash`/`nocx.zsh`, the declaration record and its outcome states, the per-fact deadline
backstop in `internal/workers/backstop.go` (replaced by §5), and every document line naming
them (protocol §16, `contracts/toolendpoint.md`, tool descriptions).

## 8. Order of work, and the end-to-end check

Each step is a tracer bullet, merged green before the next starts:

1. Observed worker states reach the coordinator's mailbox (sweep 1 s, settle window, idle /
   blocked / exited messages, `workers.inbox` for a coordinator).
2. An idle coordinator is woken for new mail; retries; the human is called for an unread batch
   and at once for a blocked coordinator.
3. `workers.report` with `done`, `question`, `progress`; the first two wake, the third does not.
4. The worker preamble at spawn.
5. Removal of `workers.wait`, the drop, the declaration, the outcome states and the old
   backstop.
6. The end-to-end check, and the owner's live run.

**The check** (`AGENTS.md` rule 2), over `cmd/nocx-server` or the real-helper stand in
`internal/app`: a coordinator spawns a mock-agent worker; the worker's pane shows the
preamble then the task; the worker calls `workers.report done` and settles idle; the idle
coordinator receives exactly one wake line within settle window + one sweep, with no
`workers.wait` call; `workers.inbox` returns the report and the idle state in order; a second
worker blocked on a menu wakes it the same way; a worker that exits does too; the coordinator
left idle and unread is retyped and then the human notification is raised; `workers.close`
closes the worker's tab.
