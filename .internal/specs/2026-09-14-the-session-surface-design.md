# A coordinator and nocx's assistant read, type into and message a pane — design

- **Epic:** `nocx-6q1uh`. **Brainstorm:** `nocx-0j000`.
- **Brief this answers:** `.internal/specs/2026-09-11-coordinator-surface-after-herdr-design.md`
  §7 (items 1–11, from four codex reviews). Every item is answered in §10 below.
- **Stands on:** epic `nocx-ygxjv` (closed 2026-09-14): one emulator, in the helper's session
  runtime, beside the PTY (ADR-0066).
- **Status:** sections 1–3 approved by the owner on 2026-09-14; sections 4–9 written by the author
  on the owner's instruction and sent to codex review.

## 1. What a user can do that they could not

Let a coordinator, and nocx's own assistant, read a pane, send it any keys, and message an agent
in it at its next free prompt or during its turn, through one `session.*` surface, with nothing
written onto a screen the caller did not see.

**End-to-end check (from the epic, kept):** through the real MCP bridge and tool endpoint, a
coordinator reads a held mock-agent worker with `session.read`, answers a permission menu with
`session.keys` naming the option, sends `session.message when=now` during a turn and `when=free`
after it, while another of its calls is still in flight; a key conditioned on a menu that has
since changed writes zero bytes to the PTY.

## 2. Binding documents this crosses

| Document                                            | What it decided                                                                                                                                                                                                                                                       | What this design does                                                                                                                                                                                                                                                                           |
| --------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-6** amendment (`docs/architecture.md:159-174`) | A grid decides two powers; power (1) has two cases: text into positively identified `free_text`, keys from a menu's closed set, frame re-read immediately before the write. ADR-0066 moved the emulator to the backend; ADR-0064's second case and its re-read stand. | **Amended** (§9): power (1) gains conditional input under a named target, any key, input during a turn, and — for the built-in assistant only — any pane under a `region` target. The re-read becomes a revalidation inside the runtime at execution.                                           |
| **AD-1**                                            | Raw bytes on the data plane, JSON-RPC on the control plane; ADR-0066 amends the inward direction to intent.                                                                                                                                                           | Keys and text are intents encoded by the runtime against the terminal's modes; no raw PTY bytes cross JSON-RPC.                                                                                                                                                                                 |
| **AD-7**, **AD-8**                                  | Server-authoritative sessions; interfaces at one composition root.                                                                                                                                                                                                    | One `SessionAccess` capability interface with two implementations (§6), wired in `internal/app`.                                                                                                                                                                                                |
| **ADR-0020**                                        | Authority granted per run; which resources a tool reaches is the capability's answer.                                                                                                                                                                                 | Kept: the tool never decides which sessions it reaches.                                                                                                                                                                                                                                         |
| **ADR-0029** (Proposed)                             | A keystroke is bound to what makes it meaningful, not to the frame; the final gate is local and synchronous; a scoped diff answers the common case.                                                                                                                   | **Superseded** by the new ADR (§9): the binding is a caller-named region checked by the runtime under its own lock. Its rules 1 and 4 survive as that mechanism; its rule 5 (model-authored conditions) is not adopted — a changed region is returned to the model, which re-reads and decides. |
| **ADR-0063**                                        | Typing is refused on evidence against the rule.                                                                                                                                                                                                                       | Kept for agent targets.                                                                                                                                                                                                                                                                         |
| **ADR-0064** (Accepted)                             | §1 closed key set, option named by its text; §2 reads by the holding session; §4 what it may never do.                                                                                                                                                                | §1's closed key set **superseded** by the new ADR. Option-by-text survives as `session.keys option`. §2 kept. §4 re-read and restated in the new ADR.                                                                                                                                           |
| **ADR-0066** (Accepted)                             | The runtime owns screen, modes, replies and the order input is admitted in; freshness is an admission question.                                                                                                                                                       | Implemented: `sessionruntime.Admit`/`Execute` get their first production caller.                                                                                                                                                                                                                |
| 2026-09-11 design §4 decisions 5–10                 | `workers.wait` and the drop go (parts 2–3); scope of rights; two deliveries; any key; one `session.*` surface.                                                                                                                                                        | Implemented for 6–7, 9–10. Decision 5 is not this epic's (`nocx-luqz9`).                                                                                                                                                                                                                        |
| 2026-09-03 mesh design M1/M2                        | Talk is mesh, act is star.                                                                                                                                                                                                                                            | Unchanged: only a coordinator's own workers and the assistant's granted panes are reached. Neighbours are `nocx-i8umd`.                                                                                                                                                                         |
| `AGENTS.md` testing rules 1–5                       | User-path tests, a happy path, failure intervals, independent tests, the wire in the contract.                                                                                                                                                                        | §8.                                                                                                                                                                                                                                                                                             |

## 3. Owner decisions (2026-09-14)

1. **Any pane for the assistant.** The built-in assistant may write keys and text into any pane,
   not only one where nocx recognised an agent. On a pane with no recognised agent the condition is
   "the region I saw is unchanged".
2. **Approval is the existing policy.** The user's permissions decide ask, permit or refuse, as for
   every other tool. A rule may be scoped by the program running in the pane — e.g. `vim` permitted,
   another program refused.
3. **A changed screen is a refusal returned to the model.** Nothing is written; the model re-reads
   the screen and decides whether to retry.
4. **The condition is checked inside the helper runtime**, not by a coordinator-side re-read and not
   by a classifier moved into the helper.
5. Sections 1–3 of this design (§4, §5, §7) are approved as written.

## 4. Architecture (approved)

### 4.1 Three tools, both callers

- `session.read` — a pane's screen and a **target**: what the caller saw.
- `session.keys` — keys, text chunks, or a menu option by its text, conditioned on a target.
- `session.message` — a message to an agent, at its next free prompt or during its turn.

`workers.screen` and `workers.answer` are removed (§9). The coordinator's dispatcher allowlist
(`internal/assistant/dispatch.go:156-174`) gains these three; the endpoint's `tools.catalogue`
(`internal/toolendpoint/catalogue.go:29`) is made to offer exactly what that dispatcher accepts —
today it offers `session.*` from the grant and then refuses the call.

### 4.2 The target

Returned by `session.read`, passed back verbatim by the caller. Fields:

- `sessionId`, the session **incarnation**, and for an enrolled agent the **enrolment incarnation**;
- `kind`: `menu` | `input` | `working` | `region`;
- `rows`: `[first, last]` in the visible screen;
- `digest`: SHA-256 of the normalised text of those rows (§4.4);
- `revision`: the runtime revision the reading was taken at (informational; never compared for
  equality — a spinner advances it every tick);
- for `menu`: `question`, `options[]`, `selected` (as drawn).

Who computes the rows:

- **Agent panes:** the coordinator, from the agent rule (§5.3). `menu` covers the question through
  the last option, selection marker included. `input` and `working` cover the input box.
- **Any other pane (`region`):** the whole visible screen by default; the caller may narrow it by
  passing `rows` to `session.read`. Without narrowing, a status-line clock refuses every write,
  which is the correct answer to a caller who claims to rely on the whole screen.

A `region` target is also accepted on an agent pane (a coordinator pressing `Ctrl+O`); the
`menu`/`input`/`working` kinds add a coordinator-side classification check before admission.

### 4.3 The write: one helper operation over the runtime

A new helper session-service operation, `session.intent`, admits and executes one
`sessionruntime.Intent` synchronously and returns its terminal state:

```
request:  { sessionId, incarnation, principal, kind: key|text|paste,
            payload, precondition: { rows: [a,b], digest } }
response: { state: executed|refused|failed|cancelled,
            revisionAfter,            // runtime revision immediately after the write
            refusal?: { cause: stale_region|stale_incarnation|completeness_unknown|
                               cannot_encode|write_failed,
                        regionNow: string } }
```

`sessionruntime.Precondition` gains `Rows`; `Execute` compares the digest of those rows, not of
`screenTextLocked()`. Under the runtime lock it (1) checks incarnation, completeness and the region
digest, (2) encodes the intent against the current modes (DECCKM, bracketed paste), (3) writes, or
refuses with nothing written. `revisionAfter` is taken under the same lock, so "frames after my
write" is expressible (`revision > revisionAfter`).

### 4.4 Normalisation

One rule for both callers and for the digest: each row is the runtime's cell text with trailing
blanks trimmed; wide-character continuation cells are dropped; rows joined with `\n`. The worker
path's right-trim is kept; the renderer's blank-keeping read path is deleted with the renderer
source (§5.5).

### 4.5 One writer

Frames from an attached client (a person typing) today reach the PTY through
`hostSession.write` → `proc.Write` (`internal/helper/session/session.go:901-921`), under the host
session's lock, while the runtime writes its replies under its own. Both go through the runtime's
write lock after this epic, so an intent's check-and-write is atomic against the program's output
being ingested **and** against every other input. Client input does not become an intent here
(that is `nocx-zg3k3`); it only takes the same lock.

**The race that remains, stated:** the program may redraw between our write and the moment it
reads its input. No design removes it; the region check makes the window the program's own
scheduling latency rather than a network round trip plus a queue.

## 5. Keys, targets and menus (approved)

### 5.1 `session.keys`

`{ sessionId, target, keys: [ {key: "Enter"} | {text: "..."} , ... ] }` or
`{ sessionId, target, option: "<option text as drawn>" }`.

Key names: `Enter`, `Esc`, `Tab`, `BackTab`, `Backspace`, `Delete`, `Up`, `Down`, `Left`, `Right`,
`Home`, `End`, `PageUp`, `PageDown`, `Insert`, `F1`–`F12`, `Space`, and `Ctrl+<letter>`,
`Alt+<key>`, `Shift+<key>` combinations. Any key may be sent (2026-09-11 decision 9). The set is a
closed vocabulary of names so the runtime can encode it against the modes; it is not a closed set
of permitted keys.

### 5.2 How `keys` and `option` are written

- **`keys`** — the whole sequence is **one** intent under **one** precondition, written
  contiguously; no other input interleaves. This departs from brief §7.2 ("each key in a sequence is
  revalidated"): after the first key the screen has moved and there is no precondition left to
  state for the second. A caller who needs per-step revalidation uses `option`.
- **`option`** — a coordinator-side loop, each step a separate intent:
  1. read the frame and extract the menu through the rule;
  2. if the question or the option set differs from the target — refuse, returning what is on
     screen now;
  3. if the wanted option is not selected, send one `Up`/`Down` conditioned on the current menu
     region, and go to 1;
  4. send `Enter` conditioned on the region in which the wanted option is the selected one.

  This is today's `Typist.Choose` (`internal/agenttyping/agenttyping.go:546-590`) with every step
  checked in the helper. It is bounded by the option count plus a settle budget; a menu that does
  not settle is a refusal naming what it saw.

### 5.3 Menu extraction first

The Claude rule today has two extractors (`subagents`, `transcript`,
`internal/agentdriver/…/claude.rule.json:256-270`), and `ReadMenu` guesses rows around the cursor
(`agenttyping.go:492-516`). The rule engine gains a `menu` extractor per supported menu state
(`permission_choice`, `modal_choice`) yielding `question`, `options[]`, `selected`, and the row
bounds of question, options and body. `ReadMenu` and target construction consume that one result.
A menu whose body boundary cannot be found yields **no** `menu` target: `session.read` returns a
`region` target and only a region-conditioned write is possible. A real Claude menu answered
successfully is required beside that refusal (brief §7.3).

### 5.4 Refused without writing

Completeness unknown; session or enrolment incarnation changed; caller lacks authority over the
pane (§6); for agent kinds, the coordinator's classification disagrees with the target kind; region
digest differs at execution.

### 5.5 `session.read`: one source

`session.read` today reads a running item from the renderer (`executeSessionScreen` →
`WSServer.RequestScreen`, `internal/assistant/blocks.go:280-285`,
`internal/transport/ws_readscreen.go:181-190`). The renderer is not an authority on the screen
after ADR-0066. A running item is read from the helper frame (`paneview.Store`), finished items
from the ledger as today. The renderer request path is deleted. The schema
(`contracts/tools/session.read.schema.json`) keeps its item and window behaviour and gains
`target`, `classification` (agent state or `none`), and `pendingMessages` (§7). Distinct outcomes,
each its own result shape: no such session, not held by this caller, completeness unknown, frame
unavailable (helper unreachable), and for agent panes classification `unknown`.

## 6. Authority and approval

### 6.1 One capability interface

```go
type SessionAccess interface {
    // Resolve names the pane and why this caller may reach it, server-side.
    Resolve(ctx, sessionID) (Pane, error)
    // Check re-establishes the same authority now; called before every intent.
    Check(ctx, Pane) error
}
```

- **Own-session implementation** (the built-in assistant): panes within the run grant's session
  scope (`narrowSession`, `internal/agenttools/narrow.go:187`), as `session.read` today.
- **Delegated-worker implementation** (a coordinator): a pane is reachable iff its participant's
  delegation names the caller's session as `ControllerSession` and permits the effect
  (`internal/workers/registrar.go:380-396` moves behind this interface). Session → participant
  resolution is server-side; the model never names a participant.

`Check` runs **before every intent**, not once per call: today delegation is checked at the start
of `Registrar.Answer` and not again through its up-to-20 s wait (`registrar.go:419-435`,
`internal/app/workers.go:1587-1630`). Revocation (grant epoch retired, delegation ended, worker
closed) cancels queued messages and fails the next step with nothing written.

### 6.2 A coordinator over its own workers

Full rights over its own workers (2026-09-11 decision 6): read, keys, message, no approval prompt.
The worker's own permission settings are unaffected; a turn-starting message borrows them, which is
why neighbours never get this path.

### 6.3 The built-in assistant: the existing policy

`session.keys` and `session.message` declare effect `send-input` (mutate). They go through the
kernel gate like every tool (`internal/assistant/kernel.go:1842-1881`, `decideInvocationWithReason`
`:660`): expiry, floor, verdict of the effect × resource matrix, ask/permit/refuse.

- **New resource kind `pane-program`:** `{ domain, program }`. `domain` is the approval domain the
  backend already derives (`internal/agentapproval/store.go` `Domain`: local, or ssh host + account
  - host key). `program` is the foreground command of the pane as the helper observes it
    (`proto` `ForegroundCommand`, `internal/helper/proto/session_service.go:754-759`). A policy row
    may be scoped by program: `vim` permit, `sudo` refuse, everything else ask.
- **Unknown program** (an SSH pane has no local process evidence; an observation that failed) is
  never matched by a row that names a program; it falls to rows that name none, and by default to
  ask.
- **The approval request** carries the pane, the target's region text (what the model saw), and the
  input readably: key names, text verbatim, the option text. It is built from the existing
  `ApprovalRequest` (`kernel.go:264-345`), not a parallel structure.
- **After approval** nothing is re-asked: the helper's region check decides. A changed region is
  `refused` with `regionNow`, returned to the model (owner decision 3). A standing permit never
  bypasses the region check.
- **A floor (proposed, for review):** text into a local pane whose terminal has `ECHO` off (a
  password prompt) is never covered by a standing permit; it always asks. The helper reads the
  line discipline flags from the PTY it owns. On SSH panes the far terminal's flags are not
  observable; there the floor cannot fire and the program scope is `unknown`, which already asks.

## 7. `session.message` (approved)

**Agent panes only.** On any other pane: refused, naming `session.keys`. "Free prompt" is only
defined for a recognised agent.

`{ sessionId, text, when: "free" | "now", id, target? }` — `id` is the caller's idempotency key;
`when=now` requires an `input` or `working` target.

**`when=free` does not block the call.** It returns `queued` immediately; nocx delivers when the
agent is free. The message's state is visible in `session.read` → `pendingMessages`. Waking the
coordinator on delivery is `nocx-luqz9`. (The brief assumed a waiting call; that contradicts
decision 5 — a coordinator must not hang.) One FIFO queue per pane, one delivery at a time, in
coordinator memory; a coordinator restart reports queued messages as `lost` on the next read of
that pane rather than silently forgetting them.

**Delivery, the same for both modes, each step a separate intent:**

1. **Paste**, as bracketed paste if the program enabled it, conditioned on "input box empty, no
   menu" (the `input` region digest of an empty box). Text already in the box → refused: someone is
   typing, and a stale echo must not be mistaken for ours.
2. **Echo:** wait (bounded) for a frame with `revision > revisionAfter` whose input region contains
   the text. A multi-line paste renders in Claude as `[Pasted text #N +M lines]`; the rule defines
   the echo form per agent.
3. **Enter**, conditioned on the input region exactly as it was when the echo was confirmed.
4. **Submission confirmed:** the input box cleared, or the agent moved to `working`, or it shows its
   queued-message indicator.

**Outcomes:** `refused` (nothing written) · `partial` (paste written, echo not confirmed; Enter not
sent; the result carries the input box as it now reads) · `written` (Enter written, submission not
confirmed) · `submitted` (confirmed) · `cancelled` (removed before the paste executed) · `lost`
(coordinator restarted while queued).

**Cancellation and duplicates:** a queued message may be cancelled until the paste executes; after
that, cancellation answers "already written". A repeated `id` is never delivered twice. Authority is
re-checked before each step, including while queued.

**To measure live, not assume:** whether a Claude menu reacts to pasted text. The "no menu"
precondition protects either way.

## 8. The owed task, re-homed

Today a spawn that meets a question marks the participant in `owedTasks`
(`internal/app/workers.go:434-490`) and `workers.answer` delivers the task after the menu leaves
(`withOwedTask`, `:1537-1550`). With `workers.answer` removed, **spawn enqueues the task as a
`when=free` message** with a deterministic id (`task:<participant>`) instead of marking a set. Any
answer — `session.keys` from the coordinator, or the person pressing Enter in the pane — frees the
prompt and the queue delivers it, exactly once by id, with the outcomes of §7. `owedTasks`,
`withOwedTask`, `awaitMenuLeftScreen` and `typeOwedTask` are deleted.

## 9. What is removed, superseded and amended

- **Removed:** `workers.screen`, `workers.answer` — registry rows, executors, schemas, their entries
  in `contracts/agent.approvalRequested.schema.json`, `workerScreener`, `workerAnswerer`, and the
  renderer screen request path (§5.5). Tests that asserted them move to the new tools' user-path
  tests, not deleted silently.
- **New ADR** (next free number): _"A write into a pane is conditioned on the region its caller
  saw, and the runtime checks it"_. Supersedes ADR-0064 §1 (closed key set) and ADR-0029
  (proposed). States ADR-0064 §4's prohibitions again where they survive. `INDEX.md` rows updated
  (`Superseded by ADR-NNNN` / `Accepted (§1 superseded by ADR-NNNN)`); ADRs themselves untouched.
- **AD-6** (`docs/architecture.md:159-174`): power (1) is restated with its cases — text into an
  identified `free_text` or input box; any key under a named target; input during a turn; and for
  the built-in assistant, any pane under a `region` target with approval by policy. The re-read
  sentence becomes "revalidated by the runtime at execution". Citations move in the same commit.

## 10. The brief's eleven requirements, answered

| §7 item                          | Answer                                                                                                                                                                                                                                                                                                                                                                                                                         |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1 The race                       | §4.3, §4.5: check and write under the runtime lock, one writer, `state` reports what was written; the remaining race stated.                                                                                                                                                                                                                                                                                                   |
| 2 Preconditions, not identity    | The region includes the selection marker and the input box contents; incarnation and enrolment incarnation bound; `option` revalidates per step; `keys` is one intent (departure stated, §5.2). ADR-0029 superseded (§9).                                                                                                                                                                                                      |
| 3 Menu extraction first          | §5.3, first task.                                                                                                                                                                                                                                                                                                                                                                                                              |
| 4 AD-6 amended; unenrolled panes | §9. Unenrolled panes: the assistant may write under a `region` target (owner decision 1); a coordinator never reaches one (it reaches only its workers). No backend classifier for them.                                                                                                                                                                                                                                       |
| 5 Capabilities                   | §6.1.                                                                                                                                                                                                                                                                                                                                                                                                                          |
| 6 Approval of opaque input       | §6.3: `pane-program` resource, readable request, revalidation by the region check, floor for no-echo. Paths to test: permit, refuse, decline, resume, target-changed.                                                                                                                                                                                                                                                          |
| 7 One read, a contract           | §5.5, §4.4: helper frame only; normalisation stated; distinct outcomes.                                                                                                                                                                                                                                                                                                                                                        |
| 8 Answer path re-homed           | §8: owed task as a queued message, exactly once by id.                                                                                                                                                                                                                                                                                                                                                                         |
| 9 Messages as a state machine    | §7: ordering by `revisionAfter`; outcomes; stale echo, multi-line, truncation (echo mismatch → `partial`), cancellation, duplicates.                                                                                                                                                                                                                                                                                           |
| 10 Concurrency                   | `nocx-tlaft` closed: the bridge runs calls concurrently (`internal/mcpstdio/mcpstdio.go:223-250`), the endpoint dispatches per request (`internal/toolendpoint/endpoint.go:658-740`). `when=free` no longer waits in a call at all. The one-connection-per-session slot (`internal/app/worker_auth.go:97-109`) stays; the e2e check proves a read and a corrective key while another call is in flight on that one connection. |
| 11 Tests                         | §11.                                                                                                                                                                                                                                                                                                                                                                                                                           |

## 11. Tests

- **Happy path (rule 2):** the epic's end-to-end check (§1), mock agent through production wiring:
  real `cmd/nocx-server` coordinator, real helper runtime, real MCP bridge and endpoint.
- **Runtime:** `Execute` with a row precondition — refuses on a changed region with zero bytes on
  the PTY (read off the PTY, not a mock); succeeds on an unchanged region under a spinner in
  another row; encodes `Up` as `ESC[A` and `ESC O A` under DECCKM; a client frame and an intent do
  not interleave.
- **Failing-call matrix,** each with bytes written, input left in the box, outcome and cleanup, each
  paired with its ordinary success: helper unreachable; frame unavailable; completeness unknown;
  delegation store error; grant revoked while a message is queued and between paste and Enter; PTY
  write failure; coordinator disconnect after paste; echo never appears; menu appears between paste
  and Enter; duplicate id; cancel before and after paste.
- **Policy:** `pane-program` row permits `vim`, refuses another program, asks for `unknown`; a
  standing permit still refused by a changed region; the no-echo floor asks despite a permit.
- **Contracts (rule 5):** schemas for `session.keys`, `session.message`, the extended
  `session.read`, and the helper `session.intent` op; `_DTOConformsToContract` and
  `_OverTheWireConformsToContract` for each.
- **Live, outside CI** (2026-09-11 decisions 12, 14): the `nocx-detection-verify` procedure extended
  with a menu answered by `option`, a message during a turn and after it, and the pasted-text-into-
  a-menu measurement.

## 12. Tasks (for the plan)

1. Runtime: row precondition, `revisionAfter`, one write lock for client frames and intents;
   helper `session.intent` op and its client.
2. Rule: `menu` extractor with question, options, selected and row bounds; `ReadMenu` on it.
3. `SessionAccess` with both implementations; per-intent `Check`; catalogue equals dispatch.
4. `session.read` from the helper frame with `target`; renderer screen request deleted; contract.
5. `session.keys` (`keys`, `option`) for both callers; `pane-program` resource and policy rows;
   no-echo floor.
6. `session.message`: queue, delivery state machine, `pendingMessages`, cancellation.
7. Owed task as a queued message; `workers.screen`/`workers.answer` removed.
8. New ADR, AD-6 amendment, `INDEX.md`.
9. End-to-end check; live procedure extension.

Order: 1 and 2 in parallel (disjoint files); 3 after 1; 4 after 1–3; 5 after 4; 6 after 5; 7 after
6; 8 alongside 5; 9 last.

## 13. Out

Neighbours (`nocx-i8umd`); coordinator wake and removal of `workers.wait` (`nocx-luqz9`); hooks
(`nocx-7faow`); client keyboard input as intents (`nocx-zg3k3`); remote workers (`nocx-cxq7d`).

## 14. Open for review

1. Is one-intent-per-`keys`-sequence (§5.2) safe enough, or must a sequence be refused when the
   caller did not use `option`?
2. The no-echo floor (§6.3): is reading `ECHO` from the PTY the helper owns reliable across Linux
   and macOS, and is "always ask" the right strength?
3. `pane-program` from `ForegroundCommand`: is the foreground command a trustworthy scope (a
   program can rename itself), and what should an SSH pane's program be when shell integration
   knows the running command?
4. Taking the runtime's write lock for client frames (§4.5): latency and lock-order risk against
   `hostSession.mu`.
