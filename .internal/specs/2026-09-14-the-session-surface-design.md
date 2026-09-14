# An orchestrating agent reads, types into and messages its descendants' panes — design

- **Epic:** `nocx-6q1uh`. **Brainstorm:** `nocx-0j000`.
- **Brief this answers:** `.internal/specs/2026-09-11-coordinator-surface-after-herdr-design.md`
  §7 (items 1–11). Every item is answered in §12.
- **Stands on:** epic `nocx-ygxjv` (closed 2026-09-14): one emulator, in the helper's session
  runtime, beside the PTY (ADR-0066).
- **Revision 6** (2026-09-14): answers codex round 5 on revision 5 (`b28d8881`); revision 5 answered round 4 on `d8be1b3d`. Revisions 1
  (`357f62e3`), 2 (`0e11c56f`), 3 (`63203c36`) and 4 were reviewed by
  codex; dispositions in §15. After the second review the owner split the epic (decision 9): this
  document is the **shared core plus the orchestrating caller**. The built-in assistant acting on
  arbitrary panes is the sibling epic `nocx-3g262` (§14), which inherits decisions 1–3, 6 and 8 and
  the review findings that belong to it.

## 1. What a user can do that they could not

Let an orchestrating agent — a coordinator `claude` in a nocx pane, or nocx's own assistant over
workers it spawned — read a descendant's pane, answer its menus, send it keys, and message it at
its next free prompt or during its turn, through one `session.*` surface, with nothing written onto
a screen the caller did not see and nothing written after its authority was revoked.

**End-to-end check (rule 2):** through the real MCP bridge and tool endpoint, a coordinator reads a
held mock-agent worker with `session.read`, answers its permission menu with `session.keys option`,
sends `session.message when=now` during a turn and `when=free` after it, while another of its calls
is in flight; a key whose target menu has since changed writes zero bytes to the PTY; a key sent
after the delegation was ended writes zero bytes.

## 2. Binding documents this crosses

| Document                                            | What it decided                                                                                                                                  | What this design does                                                                                                                                                                                                                                             |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-6** amendment (`docs/architecture.md:159-174`) | Power (1) has two cases: text into positively identified `free_text`; keys from a menu's closed set; frame re-read immediately before the write. | **Amended** (§11): power (1) is conditional input under a helper-minted, one-shot target over a descendant's pane — one key, one text atom, or an option — including input during a turn. The re-read becomes validation at the session I/O owner's commit point. |
| **AD-1**                                            | Raw bytes on the data plane, JSON-RPC on the control plane; ADR-0066 amends inward input to intent.                                              | Keys and text are intents encoded by the runtime against the terminal's modes.                                                                                                                                                                                    |
| **AD-7**, **AD-8**                                  | Server-authoritative sessions; interfaces at one composition root.                                                                               | One `DescendantPaneAccess` capability, bound by each adapter before dispatch (§7.1).                                                                                                                                                                              |
| **ADR-0020**                                        | Authority granted per run; the capability decides what a tool reaches.                                                                           | Unchanged here. The assistant reaches only its own descendants in this epic, under its existing kernel gate. Widening is the sibling epic's.                                                                                                                      |
| **ADR-0029** (Proposed)                             | A keystroke is bound to what makes it meaningful; local synchronous gate; scoped comparison.                                                     | **Superseded** (§11): rules 1 and 4 become the target mechanism; rule 5 (model-authored conditions) is not adopted.                                                                                                                                               |
| **ADR-0063**                                        | Typing refused on evidence against the rule.                                                                                                     | Kept for agent targets.                                                                                                                                                                                                                                           |
| **ADR-0064** (Accepted)                             | §1 closed key set, option by its text; §2 reads by the holding session; §4 what it may never do.                                                 | §1 **superseded**; option-by-text survives. §2 extended from direct workers to descendants (decision 7). §4 restated where it survives.                                                                                                                           |
| **ADR-0066** (Accepted)                             | Runtime owns screen, modes, replies, admission order; freshness is an admission question.                                                        | Implemented: admission gets a production caller and a single I/O owner (§5).                                                                                                                                                                                      |
| 2026-09-11 design §4                                | Decisions 5–10: drop `wait`/report; scope; two deliveries; any key; one `session.*` surface.                                                     | 6 (as restated by decision 7), 7, 9, 10 here; 5 is `nocx-luqz9`.                                                                                                                                                                                                  |
| mesh design M1/M2                                   | Talk is mesh, act is star.                                                                                                                       | Act reaches descendants; neighbours only talk (`nocx-i8umd`).                                                                                                                                                                                                     |
| `AGENTS.md` testing rules 1–5                       |                                                                                                                                                  | §13.                                                                                                                                                                                                                                                              |

## 3. Owner decisions (2026-09-14)

1–3, 6, 8 concern the built-in assistant acting on arbitrary panes and move to the sibling epic
(§14): any pane kind; approval by the existing policy with program scope; a changed screen returns
to the model; the assistant unbounded by pane or workspace; text into a pane whose echo is off or
unknowable always asks. Kept here:

- **(3) A changed screen is a refusal returned to the caller**, which re-reads and decides.
- **(4) The condition is checked in the helper**, not by a coordinator-side re-read, not by a
  classifier moved into the helper.
- **(7) An orchestrated agent acts on the panes of its descendants** and only talks to neighbours.
- **(9) Split:** this epic is the shared core and the orchestrating caller; the assistant on
  arbitrary panes is a sibling epic.

## 4. The surface

### 4.1 Three tools

- `session.read { sessionId, target?: kind }` — a descendant pane's screen, its classification,
  pending messages, and on request a target.
- `session.keys { sessionId, target, key | text | option }` — one step under a target.
- `session.message { sessionId, text, when: free|now, id, target? }` — a message to the agent;
  `session.message { sessionId, cancel: id }` — the disjoint cancel form (§8.6).

`workers.screen` and `workers.answer` are removed (§11). Both callers reach the three tools through
`DescendantPaneAccess`: the coordinator through the tool endpoint (its dispatcher allowlist,
`internal/assistant/dispatch.go:156-174`, gains them), the assistant through the kernel, where its
existing policy gate applies per call as it does to `workers.answer` today. The endpoint's
catalogue and dispatch consume the same bound capability, so the catalogue offers exactly what
dispatch accepts (`internal/toolendpoint/catalogue.go:29` offers `session.*` and dispatch refuses
it today).

`session.read` on a session that is not a descendant: refused `not_reachable`. Plain shells are
never descendants (only enrolled participants are).

### 4.2 Keys

Exactly one of:

- `key` — `Enter`, `Esc`, `Tab`, `BackTab`, `Backspace`, `Delete`, `Up`, `Down`, `Left`, `Right`,
  `Home`, `End`, `PageUp`, `PageDown`, `Insert`, `F1`–`F12`, `Space`, or one chord
  `Ctrl+<letter>` / `Alt+<key>` / `Shift+<key>`. Any key (2026-09-11 decision 9).
- `text` — one atom, written as a paste. A newline or control character is refused `would_submit`
  unless the program has bracketed paste on.
- `option` — the option's text as drawn; `menu` targets only (§6.3).

**One target authorises one state-changing step.** Sequences are separate calls with fresh targets,
or `option`.

## 5. The helper: one I/O owner per session

### 5.1 Why

The runtime holds `Session.mu` across a PTY write (`internal/sessionruntime/runtime.go:88-96`) and
the pump must take it to ingest (`internal/helper/session/session.go:341-346`): a program that
floods output without reading input stalls its pane today (`nocx-6q1uh.1`). Conditional input needs
validation and write to be one linearisable step, and the pump must never wait on a write. A mutex
cannot give both; an owner can.

### 5.2 Structure

Each helper session has **one owner goroutine**. It alone decides the order of everything that
changes the terminal or its model: output ingest, runtime replies, client frames, intents, resize,
and shutdown. It never blocks on I/O.

- **Local PTY:** the master fd is set `O_NONBLOCK` **before** it is wrapped by `os.NewFile`, on both
  platforms — on Darwin `creack/pty` opens `/dev/ptmx` blocking and wraps it at once
  (`pty_darwin.go:14-18`), so `internal/pty` opens the master itself or re-wraps a dup that is
  non-blocking from the start; an fd wrapped while blocking is never pollable. Readiness and reading
  are split: a **readiness goroutine** waits on the poller (`SyscallConn().Read` returning `false`
  until readable) and only signals the owner; the **owner** performs the reads, as a raw
  non-blocking `read(2)` loop through `SyscallConn().Read`, until `EAGAIN`. An expired deadline is
  never used as a probe — Go returns a timeout before issuing `read(2)`.
- **SSH channel (a local helper holding an SSH channel):** `ssh.Channel` has no readiness boundary
  (`internal/helper/sshsvc/shell.go:293-294`) — a blocking reader may hold bytes the owner has not
  seen. So there is **no read barrier**, and state-changing intents on such a session are
  **refused `no_read_barrier`**. Reads and snapshots still work. A pane whose PTY is held by a helper
  on the far host has a real PTY and a real barrier there. Descendants on SSH are not in this epic's
  scope anyway (remote workers are `nocx-cxq7d`); the refusal is what makes that explicit.
- **Writer:** a writer goroutine performs the `Write` of exactly one item at a time on the same
  non-blocking file (the Go poller absorbs `EAGAIN` for a pollable file) and reports `(n, err)`
  back. The owner hands it the next item only after the previous completed.

### 5.3 The commit point

Every input item (reply, client frame, intent, resize) is queued in the owner in arrival order.
When the writer is idle and an item reaches the head:

1. the owner **drains**: reads to `EAGAIN` and ingests (a local PTY; sessions without a barrier never
   reach this step with an intent);
2. for an intent, **validates** under the runtime mutex (§6.2): token, one-shot state, incarnation,
   completeness, access epoch (§7.2), digest;
3. **hands the encoded bytes to the writer** and assigns the item its **fence** (a per-session
   monotonic input sequence).

Step 3's handoff is the **linearisation point**: nothing else is written between validation and the
write, because the writer is idle and the head cannot be passed. While the write is in flight the
owner keeps reading and ingesting; a stalled write never stops the drain.

**The race that remains, stated:** between the handoff and the kernel accepting the bytes, and
between the kernel accepting them and the program reading them, the program may still change its
screen. No design removes that.

### 5.4 Fences

Each ingested chunk is stamped, **at the owner's read**, with the highest fence whose write had
completed when that read happened; frames carry `inputFence`. On a local PTY, bytes read after a
completed write were readable only after it — but may still have been _produced_ before the
program read our input. So `inputFence ≥ fenceAfter` means "observed after my write", never "caused
by my write". Echo is therefore confirmed by content in a fenced frame (§8.2). A session without a
read barrier stamps at the owner's receipt of a reader chunk; it takes no intents, so nothing relies
on that stamp.

### 5.5 Replies and bounds

Runtime replies are produced by ingest and queued as input items. Queues are bounded:

- intents: when the intent queue is full, new intents are refused `busy`;
- client frames: today's lease backpressure;
- replies: a reserve of fixed size. **If it overflows** — the program keeps asking questions while
  not reading its input — further replies are dropped, the runtime's completeness becomes `lost`,
  and every later intent on that incarnation is refused `completeness_unknown`. The drain continues.
  A person can still type; automation on that pane stops, visibly (`session.read` reports it).

### 5.6 Resize

`hostSession.resize` → `CommitGeometry` (`internal/helper/session/session.go:952-960`) becomes an
owner item. Its PTY resize and any repair writes go through the same writer; the geometry change
makes every outstanding target `incomparable` from the moment the item is committed.

### 5.7 Shutdown

Three distinct operations, never collapsed into one close — the existing code separates process
exit from output EOF because collapsing them lost the final output
(`internal/helper/session/session.go:407-420`):

- **request termination** (signal the process group / close the SSH session's remote side);
- **interrupt the writer** (local: `SetWriteDeadline` in the past on the pollable file, which
  unblocks a pending write; SSH: **there is no per-channel interrupt** — `CloseWrite` only sends EOF
  and a write blocked in `remoteWin.reserve` stays blocked until the peer closes the channel or the
  pooled mux fails, `golang.org/x/crypto/ssh/channel.go:247-269, 585-600`. So the SSH writer is
  **detached**, below);
- **close the readable side** (local PTY master / SSH channel).

**Process exit or graceful stop:** stop admission (`closing`) → request termination → interrupt the
writer → keep reading **until EOF** and ingest the tail → resolve queued items (not handed to the
writer → `cancelled`; in flight → the writer's result, or `delivery_unknown`) → join readiness,
reader and writer goroutines → close the readable side → close runtime and screen.

**Forced stop** (helper shutdown with a deadline): the same order, but if EOF does not arrive by the
deadline the readable side is closed and the session reports `tailLost: true` — the design does not
promise a drain that cannot occur.

**A writer that cannot be interrupted is detached, not joined.** For an SSH channel the owner sends
channel close (not mux close — sibling channels share the pooled connection), waits a join deadline,
and on expiry **detaches** the writer: its completion channel is buffered (capacity 1) so its
eventual send never blocks, it holds no lock and never touches the runtime after the handoff, and
its in-flight item resolves `delivery_unknown`. The session then closes runtime and screen and
reports `writerDetached: true`. The detached goroutine ends when the peer closes the channel or the
pooled connection ends. **The first detach taints that pool generation:** the pool admits no new
channel on it (a new session dials a fresh generation), lets its non-detached siblings run to their
own end, and closes the connection when none remain — which ends every detached writer on it.
**A helper holds at most 8 detached writers.** A detach that would exceed the cap closes its tainted
connection at once instead, ending its siblings' channels with a named reason
(`detached_writer_cap`) reported on those sessions; the cap is fail-closed rather than a leak. Since SSH-channel sessions take no
intents (§5.2), a detached writer can only have been carrying client input or a reply.

Channels are closed only by their sending side. Tests: exit with unread tail bytes (tail ingested);
forced stop with a program that never closes its output (`tailLost`); shutdown with a local write
blocked (writer interrupted, joined) and with an SSH write blocked by a peer holding its window at
zero (writer detached, session closed within the join deadline, sibling channel on the same
connection unaffected).

### 5.8 Liveness test (replaces `nocx-6q1uh.1`'s assertion)

A real-PTY program that floods output, does not read input, and asks DSR: output keeps arriving
(watchdog on a frame count, not a duration), the reply is written once the program reads, and on a
program that never reads, the reserve overflows into `completeness: lost` rather than a stall.

## 6. Targets

### 6.1 Minted from a retained snapshot

`session.read` asks the helper for a **snapshot**: `{ snapshotId, frame, revision, inputFence,
completeness, accessEpoch, readBarrier }`. `accessEpoch` is the helper session's current access
epoch (§7.2) and is stored with every token the snapshot yields; it starts at 1 for each session
incarnation, and a helper restart is a new incarnation. The helper retains the last few snapshots per session (a ring of 8, each for at most
2 s). The coordinator classifies that frame with the agent rule and chooses the rows, then asks the
helper to **mint a target from the same `snapshotId`**. An evicted snapshot refuses `snapshot_gone`
and `session.read` retries once from a fresh snapshot. Classification, rows, menu identity and
digest therefore always describe one frame.

### 6.2 Token

The helper computes a **structural digest, version 1**, over the snapshot: session incarnation,
active buffer and its instance, geometry; for each row in range every cell's grapheme, width and
style attributes (`internal/emulator/emulator.go:139-159`) and the row's wrap flag; for `input`
targets, cursor position and visibility.

Token: `{ tokenId, sessionId, incarnation, buffer, geometry, rows, digest, mintedAt, expiresAt }`,
HMAC-SHA256 under a key the helper draws per session incarnation and never exports; lifetime 60 s.

**Bounded, and one-shot.** Minting **reserves a record slot** for the token; a session holds at
most `maxLiveTokens` = 256 slots. When all slots are held, `session.target` is refused `capacity` before a token
exists — never by evicting a live replay barrier. A slot is released only when **both** its token
has expired **and** 5 minutes have passed since the token reached a terminal state (or since it
expired unused). Retention never depends on a result having been read — the helper cannot observe
receipt. After release, `session.intent.status` answers `unknown`, and a coordinator holding an
`indeterminate` intent reports it `indeterminate` (never `cancelled`, never `executed`).

**The stored result is compact and bounded:** `{ state, bytesWritten, fenceAfter, refusal.cause }`
— at most 128 bytes, never `regionNow`. `regionNow` (bounded to 16 KiB of normalised text with
`regionTruncated: true` beyond it) is returned only on the response that produced the result; a
replay or `status` returns the compact record with `regionOmitted: true`, and the caller re-reads.

At the commit point the owner atomically moves `tokenId` from _unused_ to _consumed_ and binds the
**canonical intent** (`kind`, `payload`, `accessEpoch`). A second `session.intent` with the same
token and the same canonical intent returns the recorded result, or `in_progress` with
`retryAfterMs` while the write is still in flight; with a different intent → `token_spent`.
`session.intent.status { tokenId }` (non-mutating) answers the same without presenting a payload:
`unknown` (never seen, or slot released) · `in_progress` · the recorded result.

The coordinator keeps a record per `tokenId`: the bound capability (§7.1), target kind, menu
identity, the participant's enrolment incarnation, and the delegation chain generations (§7.2). A
token presented under another capability → `forged`.

**Incomparable is a refusal:** buffer switch, resize or incarnation change since the snapshot →
`incomparable`.

### 6.3 Kinds and rows (agent rule)

The rule engine gains, per supported menu state (`permission_choice`, `modal_choice`), a `menu`
extractor yielding `question`, `options[]`, `selected`, and row bounds of question, options and
body; and per agent an **input box** and a **menu zone** — the rows in which that agent can draw a
menu (Claude: from the top of the input box to the bottom of the screen; to be confirmed by the live
procedure). `ReadMenu` (`internal/agenttyping/agenttyping.go:492-516`) is replaced by that result.

- `menu` — rows of question through last option, selection included. A menu whose body boundary is
  not found yields no `menu` target.
- `input` — the input box, cursor included.
- `working` — the menu zone (so a menu appearing refuses).
- `region` — the whole visible screen, or named rows; any descendant pane.

A key under `menu`/`input`/`working` also requires the coordinator's classification of the
snapshot to equal the target kind.

### 6.4 `option`

Under one call and its one approval (§7.3), a coordinator-side loop with a step budget of
`len(options) + 2`:

1. snapshot, classify, extract the menu;
2. question or option set differs from the call's original target → refused, with what is on screen;
3. wanted option selected → mint a `menu` target and send `Enter`; done;
4. otherwise mint a `menu` target, send one `Up`/`Down`, then **wait for a snapshot with
   `inputFence ≥ fenceAfter` in which the selection moved by exactly one** (settle deadline 2 s);
   a selection that did not move, moved elsewhere, or revisits a seen state → refused naming what
   it saw; go to 1.

It replaces `Typist.Choose` (`agenttyping.go:546-590`).

### 6.5 The write op

```
session.intent  { token, accessEpoch, commitBy, kind: key|text, payload }
→ { state: executed | refused | failed_partial | delivery_unknown | cancelled | in_progress,
    bytesWritten, fenceAfter, retryAfterMs?,
    refusal?: { cause: stale_target | incomparable | expired | forged | token_spent |
                       snapshot_gone | completeness_unknown | cannot_encode | would_submit |
                       access_revoked | no_read_barrier | commit_deadline | capacity |
                       busy | closing,
                regionNow } }

session.intent.status { tokenId } → { state: unknown | in_progress | <recorded result> }
```

`refused` wrote nothing; `failed_partial` carries `0 < bytesWritten < len`; `delivery_unknown` is a
write whose completion was not reported (shutdown). Neither is ever a refusal
(`runtime.go:567` discards `n` today; that changes).

## 7. Authority

### 7.1 `DescendantPaneAccess`

Bound **before** dispatch by the adapter that authenticated the caller — never inferred from
parameters (the dispatch request is caller-neutral, `internal/assistant/dispatch.go:16-24`):

- the tool endpoint binds it to the authenticated controller session
  (`internal/app/worker_auth.go:367-427`);
- the kernel binds it to the run's session, for workers the assistant spawned.

It reaches the panes of the caller's **descendants**: participants whose delegation chain —
`Delegation.ControllerSession` (`internal/workers/workers.go:312-325`) followed upward — reaches the
bound session, each link `Active` and permitting the effect. Resolved server-side per call.

### 7.2 Revocation that cannot be outrun

**Generations in the coordinator.** Every delegation carries a generation. The workers store holds
one mutex under which (a) a delegation is created, (b) a delegation is ended or suspended and its
generation bumped, and (c) a chain is resolved into a **chain vector** `[(delegation, generation)]`.
A spawn under a delegation being revoked serialises against that revocation and sees it ended.

**Epoch in the helper.** Each helper session has an **access epoch**, reported in every snapshot
(§6.1) and carried by every intent. When a revocation ends or suspends a delegation, the
coordinator — still in the revocation call — collects the sessions of every descendant pane under
it and sends `session.access.bump(sessionId, above: epoch) → epoch` to each helper. The owner applies
a bump as an input item ahead of queued intents and **acknowledges only after every older
uncommitted intent is terminal** (`access_revoked`). The bump is idempotent (`above` names the epoch
it supersedes), so a lost acknowledgement is recovered by sending it again.

**Commit deadlines make the barrier provable without an answer.** Every intent carries `commitBy`,
an absolute deadline on the machine's monotonic clock (`CLOCK_MONOTONIC` on Linux, `mach_continuous_
time`-based on Darwin — the coordinator and a local helper share it), 5 s after the coordinator sent
it. The owner refuses an intent reaching its commit point after `commitBy` (`commit_deadline`), and
refuses at receipt one whose `commitBy` has already passed. So when a helper does not acknowledge a
bump, the revocation waits until the latest `commitBy` of any intent it sent to that session has
passed; after that no old intent can commit, whatever the helper's state or the connection's. It
returns with that session marked `confirmedBy: deadline` instead of `confirmedBy: ack`. A far-host
helper does not share the clock; it gets no intents in this epic (§5.2), and a later epic that sends
them must require the acknowledgement.

Until a session's bump is acknowledged (or the deadline passed), the coordinator admits no new
intent there, and afterwards it needs a fresh snapshot for the new epoch.

**Transport timeouts are not safety boundaries.** An intent RPC whose caller context ends does not
release anything as though execution ended: the coordinator records the intent as `indeterminate`
and asks `session.intent.status` (§6.2) before reporting an outcome; if the helper cannot be asked,
the outcome is reported `indeterminate` once `commitBy` has passed, never `cancelled`.

### 7.3 The assistant over its descendants

The kernel's policy gate (`internal/assistant/kernel.go:1842-1881`) applies **per call**, as for
`workers.answer` today: `session.keys` and `session.message` declare `send-input`. The approval binds
to the call — pane, key/text/option, the original target's menu identity — and covers the call's own
minted steps (the `option` loop's targets and the message's delivery steps) within its step budget.
No new policy mechanism is introduced in this epic.

## 8. `session.message`

Agent panes (descendants) only.

### 8.1 Queue

`when=free` returns `queued` immediately; nocx delivers when the agent is free. One FIFO queue per
pane, one delivery at a time, in coordinator memory; state in `session.read`'s `pendingMessages`.
Waking the caller on delivery is `nocx-luqz9`. `when=now` requires an `input` or `working` target
and runs the delivery within the call.

### 8.2 Delivery, each step its own freshly minted target

1. **Paste** under an `input` target covering the menu zone with the input box empty. Text in the
   box → refused: someone is typing.
2. **Echo:** wait (bounded) for a snapshot with `inputFence ≥ fenceAfter` whose input box contains
   the text in the rule's echo form (Claude: `[Pasted text #N +M lines]` for multi-line).
3. **Enter** under a target minted from that echo snapshot covering the whole menu zone.
4. **Submission confirmed:** box cleared, or `working`, or the queued-message indicator.

### 8.3 Outcomes and guarantee

A message is always in exactly one **phase** from a closed set:

- in flight: `queued` · `pasting` (paste step claimed) · `awaiting_echo` · `entering` (Enter step
  claimed) · `awaiting_submission`;
- terminal: `refused` (nothing written) · `failed_partial` / `delivery_unknown` (paste incomplete,
  `bytesWritten`) · `partial` (paste written, echo not confirmed, Enter not sent, box contents
  reported) · `written` (Enter written, submission not confirmed) · `submitted` · `cancelled` ·
  `indeterminate` (a step's helper outcome could not be learned, §7.2).

`session.read`'s `pendingMessages` and every `session.message` response carry the phase.
**At-most-once submission** per idempotency key; not an acceptance receipt.

### 8.4 Idempotency

One typed key, every part server-derived except `id`:

```
MessageKey {
  caller:      endpoint | kernel
  controller:  { sessionId, identity: session.Identity }  // the bound session's own identity
                                                          // (internal/session/session.go:60-75);
                                                          // an admitted coordinator and a kernel
                                                          // run's session both have one
  authority:   endpoint → the admission epoch the connection was admitted under
                          (internal/toolendpoint/authorizer.go:47-62);
               kernel   → the run id
  participant: { id, liveness: Liveness }          // compared with Liveness.SameIncarnation
                                                   // (internal/workers/workers.go:190-196)
  namespace:   caller | nocx
  id:          string                               // from the caller; "task" in namespace nocx
}
```

`Delegation.Epoch` is **not** used: it is documented as the controller's incarnation and filled with
the participant's (`internal/workers/workers.go:317`, `registrar.go:299`) — filed as `nocx-bm99e`,
fixed before this key is built.

The record holds `payloadHash` = SHA-256 of canonical JSON v1 `{ "v":1, "text", "when",
"targetKind" }` (keys sorted, UTF-8, no insignificant whitespace) and the state. Same key, same
hash → the recorded state; different hash → `id_reused`. Records live until the participant's
incarnation ends.

### 8.5 Ownership and lock order

- A delivery step: under the **queue mutex**, refuse if the record is cancelled, else claim the step
  (record phase + generation) — the claim is cancel's linearisation point (§8.6) — and release;
  resolve authority (store mutex, released); call `session.intent` holding **no** coordinator lock;
  under the queue mutex, commit the phase only if the record's generation still matches.
- A revocation: store mutex → bump generation → release → helper bumps (no lock held) → queue mutex
  → mark affected records. It never holds the queue mutex while taking the store mutex or calling a
  helper.
- No coordinator lock is ever held across a helper call.

### 8.6 The failure interval after paste

- **caller disconnect** does not cancel: a delivery belongs to the pane's queue;
- **authority revoked** at any phase: no further step; if the paste executed, state `partial` with
  box contents, kept until the incarnation ends; nocx never erases the box;
- **cancel** is `session.message { sessionId, cancel: id }`, resolved to the full server-derived
  `MessageKey` in namespace `caller`. It linearises on the queue claim (§8.5) and is **idempotent**.
  Response shapes, exactly:
  - `{ result: "cancelled", phase: "cancelled" }` — the message was `queued` and is now cancelled, or
    was already `cancelled` (a retry after a lost response gets the same answer);
  - `{ result: "too_late", phase }` — any other phase; the paste step was claimed and may be written;
  - `{ result: "no_such_message" }` — no record for that key. This is not a message phase and carries
    none; it is the one `session.message` response exempt from §8.3's phase rule.

  A cancel never reports `cancelled` for a message whose paste can still be written. Namespace `nocx`
  (the owed task) is not cancellable by a caller;

- **coordinator restart:** the queue is in memory; a read afterwards reports
  `deliveryStateLost: { since }`, no per-message outcome.

Each boundary is an assertion in the bead before implementation.

## 9. The owed task, re-homed

A spawn that meets a question marks `owedTasks` today (`internal/app/workers.go:434-490`) and
`workers.answer` delivers it (`withOwedTask`, `:1537-1550`). Instead, spawn enqueues the task as a
`when=free` message in namespace `nocx`, id `task`. Any answer frees the prompt and the queue
delivers it under §8's guarantee. `owedTasks`, `withOwedTask`, `awaitMenuLeftScreen`,
`typeOwedTask` are deleted.

## 10. Components

| Unit                                                                                                              | Does                                                                                         | Depends on                   |
| ----------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- | ---------------------------- |
| helper session owner (`internal/helper/session`)                                                                  | ordering, drain, commit point, fences, bounds, resize, shutdown, access epoch, token records | runtime, process             |
| `sessionruntime`                                                                                                  | ingest, snapshots, structural digest, validation, encoding                                   | emulator                     |
| helper ops `session.snapshot`, `session.target`, `session.intent`, `session.intent.status`, `session.access.bump` | wire                                                                                         | owner                        |
| rule engine (`internal/agentdriver`)                                                                              | menu extractor, input box, menu zone                                                         | —                            |
| `DescendantPaneAccess` (`internal/app`, `internal/workers`)                                                       | chain resolution, generations, revocation fan-out                                            | workers store, helper client |
| tools `session.read/keys/message`                                                                                 | arguments, targets, option loop, message queue                                               | all above                    |

## 11. Removed, superseded, amended

- **Removed:** `workers.screen`, `workers.answer` (registry rows, executors, schemas, their
  `contracts/agent.approvalRequested.schema.json` entries, `workerScreener`, `workerAnswerer`);
  `owedTasks` and its helpers; the renderer screen request path for `session.read`
  (`internal/assistant/blocks.go:280-285`, `internal/transport/ws_readscreen.go:181-190`) — after
  ADR-0066 the helper frame is the only screen authority; finished items still read from the ledger.
- **New ADR:** _"A write into a descendant's pane is one step under a target the helper minted"_.
  Supersedes ADR-0064 §1 and ADR-0029; extends ADR-0064 §2 to descendants. `INDEX.md` rows updated;
  the ADRs themselves untouched.
- **AD-6:** power (1) restated as in §2; citations move in the same commit.

## 12. The brief's eleven requirements

| §7 item                  | Answer                                                                                                                                                                                                      |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1 The race               | §5: one owner, drain, commit point, fences, bounds; remaining race stated.                                                                                                                                  |
| 2 Preconditions          | Structural digest from a retained snapshot (§6.1–6.2); one step per target (§4.2); `option` waits causally per step (§6.4); incarnations, one-shot tokens, access epoch. ADR-0029 superseded.               |
| 3 Menu extraction        | §6.3, first task.                                                                                                                                                                                           |
| 4 AD-6; unenrolled panes | §11. Unenrolled panes are not descendants and are refused; writing to them is the sibling epic.                                                                                                             |
| 5 Capabilities           | §7: one bound capability, server-side chain resolution, revocation with generations and helper epochs.                                                                                                      |
| 6 Opaque input approval  | §7.3 for the assistant over its descendants; approval on arbitrary panes is the sibling epic.                                                                                                               |
| 7 One read, a contract   | §6.1, §11.                                                                                                                                                                                                  |
| 8 Answer path            | §9.                                                                                                                                                                                                         |
| 9 Messages               | §8.                                                                                                                                                                                                         |
| 10 Concurrency           | Bridge and endpoint concurrent (`internal/mcpstdio/mcpstdio.go:223-250`, `internal/toolendpoint/endpoint.go:658-740`); `when=free` does not wait in a call; no coordinator lock across helper calls (§8.5). |
| 11 Tests                 | §13.                                                                                                                                                                                                        |

## 13. Tests

**Rule 4:** the owner, token and revocation tests are written from this spec by an author other than
the implementer; their assertions go into the beads first.

- **Happy path:** §1's check through production wiring.
- **Owner, real PTY:** §5.8 liveness; a client frame and an intent never interleave; an intent behind
  a blocked client frame validates only when it reaches the head (stale target refused); output
  arriving while an intent waits is drained before validation; `failed_partial` reports true
  `bytesWritten`; reply-reserve overflow → `completeness: lost`; resize while a write is blocked and
  resize racing a commit → `incomparable`; shutdown with unread tail, with local and SSH writes
  blocked.
- **Targets:** forged MAC; altered rows; expired; token under another capability; token reused
  concurrently and after a lost response (one execution); evicted snapshot; alternate-screen switch;
  resize; style-only selection change; cursor-only move — each paired with an unchanged target that
  succeeds while a spinner runs outside the rows.
- **Option:** a menu that repaints late after consuming a key does not overshoot; oscillation
  refused.
- **Authority:** revocation between authority check and commit (test hook) → `access_revoked`, zero
  bytes; a grandchild's intent racing its grandparent's revocation; a grandchild spawn racing it; a
  helper that does not answer the bump → connection cut, uncommitted intents cancelled; a neighbour
  unreachable; a plain shell unreachable.
- **Messages:** each §8.6 boundary; echo never appears; menu appears between paste and Enter outside
  the input rows; duplicate and reused ids; cancel before and after paste; restart →
  `deliveryStateLost`; watchdog at each §8.5 handoff.
- **Contracts (rule 5):** `session.keys`, `session.message`, extended `session.read`, the four helper
  ops; `_DTOConformsToContract` and `_OverTheWireConformsToContract`.
- **Revision 4 additions:** the production local PTY adapter on Linux and Darwin — bytes preloaded,
  one drain consumes all of them, the next read reaches `EAGAIN` with no sleep; an SSH-channel
  session refuses an intent `no_read_barrier` and still serves a snapshot; `maxLiveTokens` (the production constant, 256)
  live tokens then `capacity` with no eviction, and concurrent mint/consume at the cap; a first write blocked while a
  retry gets `in_progress` and `session.intent.status` later gets the result; a bump acknowledged only
  after an older queued intent is `access_revoked`; a helper paused between peer close and EOF
  handling cannot let a revocation return before `commitBy` passes, and the old intent is refused
  `commit_deadline`; cancel after the paste claim → `too_late`; message keys that differ only in
  controller incarnation, admission epoch or participant incarnation do not collide; forced stop with
  a program that never closes output → `tailLost`.
- **Live, outside CI:** `nocx-detection-verify` extended with Claude's menu zone and echo form, an
  option answered, messages during and after a turn, pasted text into a menu.

## 14. The sibling epic: the built-in assistant on any pane

Filed as `nocx-3g262`, blocked by this one. It inherits: owner decisions 1–3, 6, 8 (§3); review
findings R1-12/13/14/15 and R2-8/10/14/15/16/17 (§15); and these requirements, recorded so they are
not lost:

- an outer capability over every pane in every workspace, with a server-derived pane inventory and
  address resolver; the meaning of existing unscoped policy rows once the bound is every pane
  (nocx is greenfield — no migration, but the decision must be explicit);
- a program predicate over a foreground **process group** (not a pid), fail-closed for pipelines and
  shell-shared groups, with a content identity for the executable (digest or code signature),
  rederived at the commit point; Linux `/proc` and Darwin `sysctl` algorithms named;
- the echo floor keyed on the **password-prompt signature — `ECHO` off with `ICANON` on** — not on
  `ECHO` alone, which every raw-mode TUI (Claude, vim) also clears; a typed `EchoState()` on the
  helper's process boundary with Linux (`TCGETS`) and Darwin implementations, SSH `unknown`,
  re-read at the commit point;
- approval of asynchronous (`when=free`) assistant text whose floor is only known at delivery:
  an `awaiting_approval` queue state, or refusal;
- operation-level approval records for multi-step operations on arbitrary panes.

## 15. Codex reviews

### Round 1 (on `357f62e3`) — dispositions as of revision 2

| #   | Finding                                               | Disposition                                                       |
| --- | ----------------------------------------------------- | ----------------------------------------------------------------- |
| 1   | Runtime mutex across client writes deadlocks the pump | Accepted; `nocx-6q1uh.1`; §5                                      |
| 2   | Stale emulator validated; revision not causal         | Accepted; §5.3–5.4 (rev 3: real barrier locally, weakened on SSH) |
| 3   | Multi-key sequence crosses states                     | Accepted; §4.2                                                    |
| 4   | Text digest ignores structure                         | Accepted; §6.2                                                    |
| 5   | Target is model-authored                              | Accepted; §6.2                                                    |
| 6   | Authority check TOCTOU                                | Accepted; §7.2 (rev 3: generations + helper epoch)                |
| 7   | Enter does not enforce "no menu"                      | Accepted; §8.2 menu zone                                          |
| 8   | Partial writes lose `n`                               | Accepted; §6.5                                                    |
| 9   | Per-message `lost` impossible                         | Accepted; §8.6                                                    |
| 10  | Idempotency key undefined                             | Accepted; §8.4 (rev 3: server-derived parts)                      |
| 11  | Interval after paste                                  | Accepted; §8.6                                                    |
| 12  | `ForegroundCommand` untrustworthy                     | Sibling epic (§14)                                                |
| 13  | SSH unknown echo                                      | Owner decision 8; sibling epic                                    |
| 14  | `pane-program` model                                  | Sibling epic                                                      |
| 15  | "Any pane" through the run fence                      | Owner decision 6; sibling epic                                    |
| 16  | Caller authority not bound                            | Accepted; §7.1                                                    |
| 17  | Tests can pass with parts broken                      | Accepted; §13                                                     |
| 18  | Cross-reference                                       | Fixed                                                             |

### Round 2 (on `0e11c56f`)

| #   | Finding                                                | Disposition                                                                                                                      |
| --- | ------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Validation then enqueue is not a write precondition    | Accepted; commit point at the idle writer's head, §5.3                                                                           |
| 2   | Draining a channel is no read barrier; fence mislabels | Accepted; owner reads non-blocking to `EAGAIN` locally, SSH weakened and stated, stamp at read, content-confirmed echo, §5.2–5.4 |
| 3   | Reply reserve has no overflow behaviour                | Accepted; overflow → `completeness: lost`, §5.5                                                                                  |
| 4   | Resize outside the owner                               | Accepted; §5.6                                                                                                                   |
| 5   | No shutdown protocol                                   | Accepted; §5.7                                                                                                                   |
| 6   | Classification and token from different frames         | Accepted; retained snapshots, mint by `snapshotId`, §6.1                                                                         |
| 7   | Token not one-shot                                     | Accepted; consumed at commit, result retained, §6.2                                                                              |
| 8   | `option` steps vs token-bound approval                 | Accepted for this epic: approval per call covers its minted steps, §7.3; arbitrary panes → sibling                               |
| 9   | `option` overshoots without a causal wait              | Accepted; §6.4                                                                                                                   |
| 10  | Queued assistant text vs echo floor                    | Sibling epic (§14)                                                                                                               |
| 11  | No 5 s op timeout; gate liveness                       | Accepted; helper epoch bump with deadline, connection cut, indeterminate recovery, §7.2                                          |
| 12  | Per-pane gates do not cover transitive revocation      | Accepted; generations under the store mutex, helper epochs for the subtree, §7.2                                                 |
| 13  | Queue/gate lock order                                  | Accepted; §8.5                                                                                                                   |
| 14  | No foreground pid                                      | Sibling epic (process group), §14                                                                                                |
| 15  | Path+inode not an executable identity                  | Sibling epic, §14                                                                                                                |
| 16  | No echo seam; commit-point read                        | Sibling epic, §14 (plus the `ICANON` correction)                                                                                 |
| 17  | Widening changes unscoped rows; no pane addressing     | Sibling epic, §14                                                                                                                |

### Round 3 (on `63203c36`)

| #   | Finding                                                         | Disposition                                                                                                  |
| --- | --------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| 1   | Expired-deadline read is no probe; Darwin fd not pollable       | Accepted; `O_NONBLOCK` before wrapping, readiness goroutine + owner raw read loop to `EAGAIN`, §5.2          |
| 2   | SSH has no barrier, so the headline guarantee fails there       | Accepted; intents refused `no_read_barrier` on a local helper's SSH channel, §5.2                            |
| 3   | Close-then-drain cannot keep the tail                           | Accepted; termination, writer interrupt and readable close split; forced stop reports `tailLost`, §5.7       |
| 4   | One-shot records unbounded without a mint cap                   | Accepted; slot reserved at mint, 64 live tokens, `capacity`, §6.2                                            |
| 5   | No wire for lost-response recovery                              | Accepted; `in_progress` + `session.intent.status`, §6.2, §6.5                                                |
| 6   | Closing the connection is not a barrier                         | Accepted; acknowledged bump after older intents are terminal, or monotonic `commitBy` deadline elapsed, §7.2 |
| 7   | No source of the access epoch for an intent                     | Accepted; epoch in every snapshot, fresh snapshot after a bump, §6.1, §7.2                                   |
| 8   | Post-call generation check cannot enforce cancel                | Accepted; cancel linearises on the queue claim, `too_late` after it, §8.5–8.6                                |
| 9   | Idempotency key parts ambiguous; `Delegation.Epoch` mislabelled | Accepted; typed `MessageKey` and canonical payload v1, §8.4; defect filed `nocx-bm99e`                       |

### Round 4 (on `d8be1b3d`)

| #   | Finding                                               | Disposition                                                                                                                                                    |
| --- | ----------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | `CloseWrite` does not interrupt a blocked SSH write   | Accepted; channel close, join deadline, then a detached writer with a non-blocking completion; the pool reaps a connection held only by detached writers, §5.7 |
| 2   | "Result read" is not observable                       | Accepted; retention by time after terminal state and token expiry only, §6.2                                                                                   |
| 3   | 4 KiB cap vs unbounded `regionNow`                    | Accepted; compact stored result, `regionNow` only on the producing response (bounded, truncation flagged), §6.2                                                |
| 4   | Controller `Liveness` not available for a coordinator | Accepted; `session.Identity` of the bound session, §8.4                                                                                                        |
| 5   | Cancel has no operation or response                   | Accepted; disjoint cancel form of `session.message`, closed phase set, §4.1, §8.3, §8.6                                                                        |

Clock note from round 4, carried into the plan: `commitBy` is an integer nanosecond reading from
`unix.ClockGettime(CLOCK_MONOTONIC)` on Linux and `CLOCK_MONOTONIC_RAW` (the `mach_continuous_time`
domain) on Darwin, behind a build-tagged reader — never a `time.Time`, wall time, or Go's
process-relative monotonic reading.

### Round 5 (on `b28d8881`)

| #   | Finding                                              | Disposition                                                                                                                                                       |
| --- | ---------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Detached SSH writers unbounded behind a live sibling | Accepted as suggested; the first detach taints the pool generation (no new channels, closed when siblings end), cap of 8 per helper, fail-closed at the cap, §5.7 |
| 2   | Cancel not idempotent; `unknown` has no phase        | Accepted as suggested; `cancelled` is idempotent success, `no_such_message` exempt from the phase rule, exact shapes, §8.6                                        |
| 3   | Acceptance still says 64 tokens                      | Fixed; tests reference `maxLiveTokens`, §6.2, §13                                                                                                                 |

Round 5's verdict was `READY_FOR_PLAN: no` on exactly these three, each with a concrete remedy that
revision 6 adopts as proposed; no design area was reopened. The review closes on that basis.
