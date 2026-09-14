# A coordinator and nocx's assistant read, type into and message a pane — design

- **Epic:** `nocx-6q1uh`. **Brainstorm:** `nocx-0j000`.
- **Brief this answers:** `.internal/specs/2026-09-11-coordinator-surface-after-herdr-design.md`
  §7 (items 1–11, from four codex reviews). Every item is answered in §11.
- **Stands on:** epic `nocx-ygxjv` (closed 2026-09-14): one emulator, in the helper's session
  runtime, beside the PTY (ADR-0066).
- **Revision 2** (2026-09-14): answers codex's first review of revision 1 (`357f62e3`), eighteen
  findings, all verified against the tree; dispositions in §14. Two of them were the owner's to
  decide and were decided (§3, decisions 6–8).

## 1. What a user can do that they could not

Let a coordinator, and nocx's own assistant, read a pane, send it keys, and message an agent in it
at its next free prompt or during its turn, through one `session.*` surface, with nothing written
onto a screen the caller did not see.

**End-to-end checks (rule 2), two, because there are two callers:**

1. **Coordinator:** through the real MCP bridge and tool endpoint, a coordinator reads a held
   mock-agent worker with `session.read`, answers a permission menu with `session.keys option`,
   sends `session.message when=now` during a turn and `when=free` after it, while another of its
   calls is in flight; a key whose target menu has since changed writes zero bytes to the PTY.
2. **Built-in assistant:** in a run started from one pane, the assistant reads a **different,
   non-agent** pane in **another workspace**, proposes a key, the approval is asked, the person
   permits, the key reaches the program; a second proposal whose region changed during the ask is
   refused with the region as it now reads, and nothing is written.

## 2. Binding documents this crosses

| Document                                            | What it decided                                                                                                                                                                                                                                                               | What this design does                                                                                                                                                                                                                                       |
| --------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **AD-6** amendment (`docs/architecture.md:159-174`) | A grid decides two powers; power (1) has two cases (text into positively identified `free_text`; keys from a menu's closed set); the frame is re-read immediately before the write. ADR-0066 moved the emulator to the backend; ADR-0064's second case and its re-read stand. | **Amended** (§10): power (1) is conditional input under a helper-minted target — one key or one text atom per target, input during a turn, and for the built-in assistant any pane. The re-read becomes validation by the session's I/O owner at execution. |
| **AD-1**                                            | Raw bytes on the data plane, JSON-RPC on the control plane; ADR-0066 amends inward input to intent.                                                                                                                                                                           | Keys and text are intents encoded by the runtime against the terminal's modes.                                                                                                                                                                              |
| **AD-7**, **AD-8**                                  | Server-authoritative sessions; interfaces at one composition root.                                                                                                                                                                                                            | Two capability types bound by their adapters (§6.1), wired in `internal/app`.                                                                                                                                                                               |
| **ADR-0020**                                        | Authority granted per run; the capability decides which resources a tool reaches; policy cannot widen the run fence.                                                                                                                                                          | **Changed for the built-in assistant** by owner decision 6: its run capability reaches every pane (§6.2). The fence is widened deliberately, in the ADR (§10), not worked around.                                                                           |
| **ADR-0029** (Proposed)                             | A keystroke is bound to what makes it meaningful; the final gate is local and synchronous; a scoped diff answers the common case.                                                                                                                                             | **Superseded** (§10). Rule 1 (local synchronous gate) and rule 4 (scoped comparison) become the target mechanism; rule 5 (model-authored conditions) is not adopted — a changed target returns to the model.                                                |
| **ADR-0063**                                        | Typing refused on evidence against the rule.                                                                                                                                                                                                                                  | Kept for agent targets.                                                                                                                                                                                                                                     |
| **ADR-0064** (Accepted)                             | §1 closed key set, option named by its text; §2 reads by the holding session; §4 what it may never do.                                                                                                                                                                        | §1 **superseded**; option-by-text survives as `session.keys option`. §2 kept and extended to descendants (owner decision 7). §4 restated in the new ADR where it survives.                                                                                  |
| **ADR-0066** (Accepted)                             | The runtime owns screen, modes, replies and admission order; freshness is an admission question.                                                                                                                                                                              | Implemented: admission gets a production caller and a single I/O owner (§4.4).                                                                                                                                                                              |
| 2026-09-11 design §4 decisions 5–10                 | `workers.wait`/drop go; scope of rights; two deliveries; any key; one `session.*` surface.                                                                                                                                                                                    | 6 (as restated by owner decision 7), 7, 9, 10 here. 5 is `nocx-luqz9`.                                                                                                                                                                                      |
| mesh design M1/M2                                   | Talk is mesh, act is star.                                                                                                                                                                                                                                                    | Unchanged: act reaches descendants; neighbours only talk (`nocx-i8umd`).                                                                                                                                                                                    |
| `AGENTS.md` testing rules 1–5                       | User-path tests, happy path per epic, failure intervals, independent tests, the wire in the contract.                                                                                                                                                                         | §12.                                                                                                                                                                                                                                                        |

## 3. Owner decisions (2026-09-14)

1. **The assistant writes into any kind of pane**, not only a recognised agent's. On a pane with no
   recognised agent the condition is "the region I saw is unchanged".
2. **Approval is the existing policy.** The user's permissions decide ask, permit or refuse. A rule
   may be scoped by the program running in the pane.
3. **A changed screen is a refusal returned to the model**, which re-reads and decides whether to
   retry.
4. **The condition is checked in the helper**, not by a coordinator-side re-read and not by a
   classifier moved into the helper.
5. §4, §5 and §7 of revision 1 approved in intent.
6. **The built-in assistant is not bounded by pane or workspace.** It may work with any pane in any
   workspace; policy decides what it may do in each.
7. **An orchestrated agent** (a coordinator, or any agent nocx orchestrates) **acts on the panes of
   its descendants** and only talks to its neighbours.
8. **Text into a pane whose echo is off or unknowable always asks** (every SSH pane is unknowable).
   No standing permit covers it. Keys that are not text are unaffected. A coordinator acting on its
   own descendants is unaffected.

## 4. Architecture

### 4.1 Three tools, two callers

- `session.read` — a pane's screen and, on request, a **target**.
- `session.keys` — one key, one text atom, or a menu option by its text, under a target.
- `session.message` — a message to an agent, at its next free prompt or during its turn.

`workers.screen` and `workers.answer` are removed (§10). The coordinator's dispatcher allowlist
(`internal/assistant/dispatch.go:156-174`) gains the three; the endpoint's `tools.catalogue`
(`internal/toolendpoint/catalogue.go:29`) and dispatch consume the **same bound capability**
(§6.1), so the catalogue offers exactly what dispatch accepts.

### 4.2 The target is minted by the helper, not authored by the caller

A caller cannot be trusted to hand back what it read: tool arguments are model-authored JSON.
So the target is an **opaque token minted by the helper** from the frame it holds.

`session.read { sessionId, target?: { kind, rows? } }` → the coordinator asks the helper's new
`session.target` operation to mint a token over the chosen rows. The helper computes a
**structural digest, version 1**, over:

- session incarnation, active buffer (normal/alternate and its instance), geometry (cols × rows);
- for each row in the range: every cell's grapheme, width and the style attributes the emulator
  holds (`internal/emulator/emulator.go:139-159`), and the row's wrap flag;
- for `input` and `message` targets: cursor position and visibility.

Token = `{ tokenId, sessionId, incarnation, buffer, geometry, rows, digest, programIdentity?,
echo, mintedAt, expiresAt }`, authenticated with HMAC-SHA256 under a key the helper draws per
session incarnation and never exports. Default lifetime 60 s. The model receives the token string
plus readable fields (the region text, and for `menu` the question, options and selected option).

The coordinator keeps a **server-side record** keyed by `tokenId`: the bound capability that asked
for it (§6.1), the target kind, the menu identity, the participant's enrolment incarnation, and any
approval tied to it. A token presented by a different capability than minted it is refused.

**Kinds and rows:**

- `menu`, `input`, `working` — agent panes only; the coordinator chooses the rows from the agent
  rule (§5.3) and records its classification.
- `region` — any pane; the whole visible screen unless the caller names `rows`.

**Incomparable is a refusal:** a buffer switch, resize or incarnation change between mint and
execution refuses with `incomparable`, never compares row text that happens to match.

### 4.3 The write: `session.intent`

```
request:  { token, principal, accessEpoch, kind: key|text, payload }
response: { state: executed | refused | failed_partial | delivery_unknown | cancelled,
            bytesWritten, fenceAfter,
            refusal?: { cause: stale_target | incomparable | expired | forged |
                               completeness_unknown | cannot_encode | would_submit |
                               program_changed | echo_off | busy,
                        regionNow } }
```

The helper verifies the token's MAC and expiry, derives the precondition from the token (never
from the request), and hands the intent to the session's I/O owner (§4.4). A `refused` intent wrote
nothing. `failed_partial` carries `bytesWritten` (`0 < n < len`); `delivery_unknown` is a write
whose outcome the writer cannot report. Neither is ever reported as a refusal
(`internal/sessionruntime/runtime.go:567` today discards `n`; that changes).

### 4.4 One I/O owner per session

Revision 1 proposed taking the runtime mutex for client frames. That would deadlock: the runtime
already holds `Session.mu` across a PTY write (`internal/sessionruntime/runtime.go:88-96`) while the
pump must take it to ingest (`internal/helper/session/session.go:341-346`), so a program flooding
output without reading input stalls — a defect in the tree today, filed as `nocx-6q1uh.1`.

Instead each helper session gets **one I/O owner goroutine** that alone orders input and output:

- a **reader goroutine** does the blocking `proc.Read` and hands chunks to the owner over a channel;
- a **writer goroutine** does the blocking `proc.Write` of chunks the owner hands it and reports
  completions (`n`, error) back; for an SSH process the same shape covers a channel whose window is
  exhausted;
- the owner `select`s over chunks, intents, client frames, runtime replies and write completions.
  It takes the runtime mutex **only** to ingest a chunk or to validate a precondition — never
  across I/O.

**Executing an intent:** the owner first drains every chunk already delivered by the reader and
ingests it; then validates token, incarnation, completeness, program identity and echo, and the
digest, under the runtime mutex; then enqueues the encoded bytes to the writer and records the
intent's **fence** (a per-session monotonic input sequence). Client frames and runtime replies go
through the same queue and receive fences too, so nothing interleaves inside an intent's bytes.

**Output fence:** every ingested chunk is tagged with the highest fence whose bytes the writer had
**completed** when the reader delivered it; frames carry `inputFence`. "Output after my write" is
`inputFence ≥ fenceAfter`.

**Backpressure:** the writer queue is bounded. When full, new intents are refused `busy`; client
frames keep today's lease backpressure; runtime replies are never dropped and count against a
separate small reserve. The owner never blocks on the writer, so the reader is always drained.

**The race that remains, stated:** a program may emit output after it read our input but before
the reader delivers it, or before it has processed the input at all; the fence then says "after"
for bytes the program produced "before" it acted on us. So echo is confirmed by **content in a
fenced frame**, never by the fence alone, and the design claims no more than that.

### 4.5 Normalisation for display

The digest is structural (§4.2). The **text** returned to a model and shown in an approval is: each
row's graphemes with trailing blanks trimmed, continuation cells dropped, rows joined with `\n`.
The renderer's blank-keeping read path is deleted (§5.5).

## 5. Keys, targets and menus

### 5.1 `session.keys`

Exactly one of:

- `{ sessionId, target, key: "Enter" }` — one key. Names: `Enter`, `Esc`, `Tab`, `BackTab`,
  `Backspace`, `Delete`, `Up`, `Down`, `Left`, `Right`, `Home`, `End`, `PageUp`, `PageDown`,
  `Insert`, `F1`–`F12`, `Space`, and one chord `Ctrl+<letter>`, `Alt+<key>`, `Shift+<key>`.
- `{ sessionId, target, text: "..." }` — one text atom, written as a paste. Text containing a
  newline or a control character is refused `would_submit` unless the program has bracketed paste
  on, because without it the newline is an Enter the target did not authorise.
- `{ sessionId, target, option: "<option text as drawn>" }` — `menu` targets only.

**A target authorises one state-changing step.** A sequence (`Down, Enter`; `Enter` then text) is
not accepted: after the first step the screen has moved and no target describes it. This is what
brief §7.2 required and what revision 1 departed from; the departure is withdrawn. Multi-step work is
separate calls, each with a fresh target, or `option`.

### 5.2 How `option` is written

A coordinator-side loop, each step its own freshly minted target and its own intent:

1. read the frame; extract the menu through the rule;
2. question or option set differs from the caller's target → refused, with what is on screen now;
3. wanted option not selected → mint a `menu` target, send one `Up`/`Down` under it, go to 1;
4. wanted option selected → mint a `menu` target (whose digest includes the selection's style and
   marker) and send `Enter` under it.

Bounded by option count plus a settle budget; a menu that does not settle is a refusal naming what
it saw. It replaces `Typist.Choose` (`internal/agenttyping/agenttyping.go:546-590`).

### 5.3 Menu extraction first

The Claude rule has two extractors (`subagents`, `transcript`), and `ReadMenu` guesses rows around
the cursor (`agenttyping.go:492-516`). The rule engine gains, per supported menu state
(`permission_choice`, `modal_choice`), a `menu` extractor yielding `question`, `options[]`,
`selected`, and row bounds of question, options and body; and per agent a declared **input box**
and **menu zone** (the rows in which that agent can draw a menu — for Claude, from the top of the
input box to the bottom of the screen, to be verified by the live procedure). `ReadMenu` and target
construction consume that one result. A menu whose body boundary is not found yields no `menu`
target, only `region`. A real Claude menu answered successfully is required beside that refusal.

### 5.4 Refused without writing

Token forged, expired or presented by another capability; incomparable; completeness unknown;
enrolment incarnation changed; access epoch changed (§6.1); for agent kinds, the coordinator's
fresh classification disagrees with the target kind; digest differs; program identity changed;
echo off without a floor-satisfying approval (§6.4); `would_submit`; `busy`.

### 5.5 `session.read`: one source

`session.read` reads a running item from the renderer today (`internal/assistant/blocks.go:280-285`
→ `internal/transport/ws_readscreen.go:181-190`). After ADR-0066 the renderer is no authority on the
screen. Running items are read from the helper frame (`paneview.Store`); finished items from the
ledger as today; the renderer request path is deleted. The schema
(`contracts/tools/session.read.schema.json`) keeps item and window behaviour and gains `target`,
`classification` (agent state or `none`), `pendingMessages` (§7) and `deliveryStateLost` (§7.5).
Distinct outcomes: no such session; not reachable by this caller; completeness unknown; frame
unavailable (helper unreachable); agent classification `unknown`.

## 6. Authority and approval

### 6.1 Two capability types, bound by their adapters

The shared dispatch request is caller-neutral (`internal/assistant/dispatch.go:16-24`), so the
authority is bound **before** dispatch, by the adapter that authenticated the caller, and never
inferred from parameters:

- **`AssistantPaneAccess`** — minted by the in-process kernel adapter for a built-in assistant run.
- **`DescendantPaneAccess`** — minted by the tool endpoint adapter, bound to the authenticated
  controller session resolved server-side (`internal/app/worker_auth.go:367-427`).

They are distinct types (a sealed sum); tool constructors accept the one their caller has. The
catalogue projection and dispatch take the same bound value.

**Per-pane access gate.** In the coordinator each pane has an access gate and an **access epoch**.
An intent holds the gate (shared) from its authority check through the `session.intent` round trip
and carries the epoch. Every revocation path — delegation ended, run grant retired, worker closed,
participant re-enrolled, run cancelled — takes the gate exclusively and bumps the epoch before it
returns. So once a revocation has returned, no intent admitted under the old authority can still be
written. The helper round trip is bounded (existing 5 s op timeout), which bounds how long a
revocation waits. Queued messages re-check the epoch before each step.

### 6.2 The built-in assistant

Owner decision 6: `AssistantPaneAccess` reaches every live pane in every workspace. This widens the
run fence ADR-0020 set (`internal/agenttools/narrow.go:187` carries only the sessions the grant
names today); the widening is recorded in the new ADR (§10), and every effect on every pane still
passes the policy gate (§6.3). Reading stays `observe`; `session.keys` and `session.message` are
`send-input`.

### 6.3 Policy: pane scope and program predicate

The effect × resource matrix is kept (`internal/assistant/kernel.go:660`,
`internal/content/effectpolicy.go`). Two additions:

- **Pane scope** uses the canonical `ResourceWorkspace` pane sub-scope (`internal/content/ledger.go:
289-302`), not a new compound resource kind.
- **Program predicate** — a separately typed, backend-derived execution-context predicate a row may
  carry: `program: <executable>`. It is **not** `ForegroundCommand` (display-only, mutable,
  omitted when the foreground group is the shell's — `internal/helper/session/inspect.go:162-186`).
  On a local pane the helper derives a **process identity** — foreground process pid, its start
  time, and the executable's path with device and inode — at token mint, puts it in the token, and
  re-derives it at execution; a different identity refuses `program_changed`. On an SSH pane there
  is no process identity; the predicate never matches, so program-scoped rows never apply and the
  programless rows decide.

  Named work, not implied: the predicate's wire and persistence schema, fail-closed parsing, the
  matcher, the settings page that edits such rows, and the approval page that offers "always for
  this program". Domain derivation (local vs ssh host/account/host key) is shared through a neutral
  interface extracted from `internal/agentapproval/store.go`'s `Domain`, not by using the external-
  agent admission store as a policy model.

- **The approval request** is the existing `ApprovalRequest` (`kernel.go:264-345`) carrying the
  pane, the token's readable region, the program identity if any, and the input readably (key name,
  text verbatim, option text). An approval binds to the `tokenId`. A standing permit never bypasses
  the target check.

### 6.4 The echo floor

Owner decision 8. For **text** from the built-in assistant (`session.keys text`, `session.message`):

- the helper reads the line discipline's `ECHO` flag from the PTY it owns at mint and **again at
  execution**; SSH panes report `unknown`;
- `off` or `unknown` at mint → the policy outcome is at least **ask**, whatever standing rows say;
- `on` at mint but `off` at execution → refused `echo_off`, returned to the model, nothing written;
- a floor ask is approved for that token only and is never saved as a standing permit.

Linux and macOS PTYs are tested for on, off, query failure, and a change during the ask.

### 6.5 An orchestrated agent over its descendants

Owner decision 7. `DescendantPaneAccess` reaches the panes of the caller's **descendants**: the
transitive closure of delegations whose controller chain leads to the caller's session, resolved
server-side (`internal/workers/registrar.go:380-396` is the direct-child check today). Full rights,
no approval prompt, no echo floor. Neighbours are `nocx-i8umd` (talk only).

## 7. `session.message`

**Agent panes only;** any other pane is refused naming `session.keys`. `{ sessionId, text,
when: "free" | "now", id, target? }`; `when=now` requires an `input` or `working` target.

### 7.1 Queue

`when=free` returns `queued` immediately; nocx delivers when the agent is free. One FIFO queue per
pane, one delivery at a time, in coordinator memory. State is visible in `session.read`'s
`pendingMessages`. Waking the coordinator on delivery is `nocx-luqz9`.

### 7.2 Delivery, each step its own freshly minted target and intent

1. **Paste**, under an `input` target whose digest covers the menu zone (§5.3) and shows the input
   box empty with the cursor in it. Text already in the box → refused: someone is typing.
2. **Echo:** wait (bounded) for a frame with `inputFence ≥ fenceAfter` whose input box contains the
   text in the form the rule defines (Claude renders a multi-line paste as
   `[Pasted text #N +M lines]`).
3. **Enter**, under a target minted from that echo frame covering the **whole menu zone** — so a
   menu drawn anywhere the agent can draw one changes the digest and refuses the Enter.
4. **Submission confirmed:** input box cleared, or `working`, or the agent's queued-message
   indicator.

### 7.3 Outcomes

`refused` (nothing written) · `failed_partial` / `delivery_unknown` (paste write incomplete, with
`bytesWritten`) · `partial` (paste written, echo not confirmed; Enter not sent; box contents
reported) · `written` (Enter written, submission not confirmed) · `submitted` · `cancelled`
(removed before the paste executed).

**Guarantee:** at-most-once submission to the PTY per idempotency key. It is not an acceptance
receipt from the agent.

### 7.4 Idempotency

Key: `{ capability identity, session incarnation, enrolment incarnation, namespace, id }`, where
namespace is `caller` or `nocx` (the owed task, §8). The record holds a payload hash and the
current or terminal state. Same key, same payload → the recorded state. Same key, different
payload → refused `id_reused`. Records live until the session incarnation ends.

### 7.5 The failure interval after paste

From "the first paste byte may have reached the PTY" to a terminal outcome:

- **caller disconnect** does not cancel: the delivery belongs to the pane's queue, not to the
  connection;
- **authority revoked** (epoch bumped) at any phase: no further step is taken; if the paste was
  written, the state becomes `partial` with the box contents and stays in `pendingMessages` until
  the incarnation ends. nocx never erases the input box — it could erase a person's typing;
- **cancel** before the paste executes → `cancelled`; after → answers the recorded state;
- **coordinator restart:** the queue is in memory. A read of the pane after restart reports
  `deliveryStateLost: { since }`; no per-message outcome is claimed, because nothing survived to
  know it.

Each boundary above is an assertion written into the bead before implementation.

## 8. The owed task, re-homed

A spawn that meets a question today marks `owedTasks` (`internal/app/workers.go:434-490`) and
`workers.answer` delivers the task (`withOwedTask`, `:1537-1550`). Instead, spawn enqueues the task
as a `when=free` message in namespace `nocx`, id `task`, keyed to the participant's enrolment
incarnation. Any answer — `session.keys` from the coordinator or the person pressing Enter — frees
the prompt and the queue delivers it under §7's guarantee. `owedTasks`, `withOwedTask`,
`awaitMenuLeftScreen` and `typeOwedTask` are deleted.

## 9. Components and dependencies

| Unit                                              | Does                                            | Depends on            |
| ------------------------------------------------- | ----------------------------------------------- | --------------------- |
| helper I/O owner (`internal/helper/session`)      | orders reads, writes, replies, intents; fences  | runtime               |
| `sessionruntime`                                  | ingest, structural digest, validation, encoding | emulator              |
| helper `session.target` / `session.intent` ops    | mint and verify tokens; execute                 | I/O owner             |
| rule engine (`internal/agentdriver`)              | menu extractor, input box, menu zone            | —                     |
| `AssistantPaneAccess` / `DescendantPaneAccess`    | authority, access gate and epoch                | workers store, grants |
| `session.read/keys/message` tools                 | argument handling, targets, loops, queue        | all above             |
| policy (`internal/content`, `internal/assistant`) | pane scope, program predicate, echo floor       | helper identity/echo  |

## 10. Removed, superseded, amended

- **Removed:** `workers.screen`, `workers.answer` — registry rows, executors, schemas, their
  `contracts/agent.approvalRequested.schema.json` entries, `workerScreener`, `workerAnswerer`; the
  renderer screen request path. Tests asserting them move to the new tools' user-path tests.
- **New ADR** (next free number): _"A write into a pane is conditioned on a target the helper
  minted, one step per target"_. Supersedes ADR-0064 §1 and ADR-0029; widens ADR-0020's run fence
  for the built-in assistant (owner decision 6) and extends ADR-0064 §2 to descendants (decision 7);
  records the echo floor (decision 8). `INDEX.md` rows updated; the ADRs themselves untouched.
- **AD-6** (`docs/architecture.md:159-174`): power (1) restated as in §2; citations move in the same
  commit.

## 11. The brief's eleven requirements

| §7 item                  | Answer                                                                                                                                                                                                                                            |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1 The race               | §4.4: one I/O owner, drain before validate, fences, no lock across I/O; `bytesWritten`; remaining race stated.                                                                                                                                    |
| 2 Preconditions          | Structural digest with style, buffer, geometry, cursor (§4.2); one step per target (§5.1); `option` revalidates per step (§5.2); incarnations and access epoch bound; ADR-0029 superseded.                                                        |
| 3 Menu extraction        | §5.3, first task.                                                                                                                                                                                                                                 |
| 4 AD-6; unenrolled panes | §10; the assistant writes to any pane under `region` (decision 1); no backend classifier for them.                                                                                                                                                |
| 5 Capabilities           | §6.1: two bound types; server-side resolution; revocation through the access gate.                                                                                                                                                                |
| 6 Opaque input approval  | §6.3–6.4; paths tested: permit, refuse, decline, resume, target changed, program changed, echo floor.                                                                                                                                             |
| 7 One read, a contract   | §5.5, §4.5.                                                                                                                                                                                                                                       |
| 8 Answer path            | §8.                                                                                                                                                                                                                                               |
| 9 Messages               | §7.                                                                                                                                                                                                                                               |
| 10 Concurrency           | Bridge and endpoint are concurrent (`internal/mcpstdio/mcpstdio.go:223-250`, `internal/toolendpoint/endpoint.go:658-740`); `when=free` does not wait in a call; the e2e check proves a read and a corrective key while another call is in flight. |
| 11 Tests                 | §12.                                                                                                                                                                                                                                              |

## 12. Tests

Rule 4 applies with full weight (transport and authority): **the concurrency and authority tests
are written from this spec by an author other than the implementer**, and their assertions are put
into the beads before implementation.

- **Happy paths:** both checks of §1, through production wiring (real coordinator, real helper
  runtime, real bridge and endpoint; the assistant through the real kernel and approval flow).
- **I/O owner, on a real PTY:** a program that floods output without reading input and asks DSR —
  output keeps arriving and the reply is written (watchdog, not a duration); a client frame and an
  intent never interleave; drain-before-validate refuses a write whose region changed in output
  already delivered; `failed_partial` reports the true `bytesWritten`.
- **Targets:** forged MAC, altered rows, expired token, token from another capability, alternate
  screen switch, resize, style-only selection change, cursor-only move — each refused, each paired
  with an unchanged target that succeeds while a spinner runs outside the rows.
- **Authority schedules:** revocation after the authority check and before the write (forced by a
  test hook in the gate) → nothing written; revocation while a message is queued, between paste and
  Enter; descendant two levels down reachable, a neighbour not; the assistant and the coordinator
  cannot use each other's capability.
- **Policy:** program predicate permits `vim` locally; the program exec'ing into another between
  permit and write → `program_changed`; SSH pane never matches a program row; echo floor asks
  despite a programless permit on SSH; echo turning off during an ask → `echo_off`.
- **Messages:** every §7.5 boundary; echo never appears; menu appears between paste and Enter
  outside the input rows; duplicate and reused ids; cancel before and after paste; restart →
  `deliveryStateLost`.
- **Contracts (rule 5):** `session.keys`, `session.message`, extended `session.read`, helper
  `session.target` and `session.intent`; `_DTOConformsToContract` and
  `_OverTheWireConformsToContract` for each.
- **Live, outside CI:** the `nocx-detection-verify` procedure extended with Claude's menu zone, a
  menu answered by `option`, messages during and after a turn, and pasted text into a menu.

## 13. Tasks (for the plan)

1. Helper I/O owner, writer/reader goroutines, fences, `bytesWritten` (closes `nocx-6q1uh.1`).
2. Structural digest, token mint/verify, `session.target` and `session.intent` ops and client.
3. Rule: menu extractor, input box, menu zone; `ReadMenu` on it.
4. `AssistantPaneAccess`, `DescendantPaneAccess`, access gate and epoch; catalogue = dispatch.
5. `session.read` from the helper frame with targets; renderer screen request deleted; contract.
6. `session.keys` (`key`, `text`, `option`) for both callers.
7. Policy: pane scope, program predicate (helper identity), echo floor, settings and approval UI.
8. `session.message`: queue, delivery, idempotency, failure interval.
9. Owed task as a message; `workers.screen`/`workers.answer` removed.
10. New ADR, AD-6 amendment, `INDEX.md`.
11. Independent concurrency and authority tests; both end-to-end checks; live procedure.

Order: 1, 3 in parallel; 2 after 1; 4 in parallel with 2; 5 after 2–4; 6 after 5; 7 after 6 (UI
part may run beside 6); 8 after 6; 9 after 8; 10 beside 6; 11's independent tests written from the
spec as soon as 2 and 4 have interfaces, run last.

## 14. Codex review of revision 1 (2026-09-14, on `357f62e3`)

Every finding was checked against the tree before disposition.

| #   | Finding                                                                           | Disposition                                                                                       |
| --- | --------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| 1   | Runtime mutex across client writes deadlocks the pump                             | Accepted; verified the same shape already exists for replies → `nocx-6q1uh.1`; §4.4 one I/O owner |
| 2   | Stale emulator validated before delivered output is ingested; revision not causal | Accepted; drain-before-validate, fences, content-confirmed echo, §4.4                             |
| 3   | Multi-key sequence crosses states                                                 | Accepted; one step per target, §5.1                                                               |
| 4   | Text digest ignores buffer, geometry, cursor, width, style                        | Accepted; structural digest v1, incomparable refusal, §4.2                                        |
| 5   | Target is model-authored                                                          | Accepted; helper-minted HMAC token plus server-side record, §4.2                                  |
| 6   | Authority check is TOCTOU against revocation                                      | Accepted; per-pane access gate and epoch, §6.1                                                    |
| 7   | Enter does not enforce "no menu"                                                  | Accepted; Enter target covers the rule's menu zone, §5.3, §7.2                                    |
| 8   | Partial writes lose `n`                                                           | Accepted; `bytesWritten`, `failed_partial`, `delivery_unknown`, §4.3                              |
| 9   | In-memory queue cannot report per-message `lost`                                  | Accepted; generic `deliveryStateLost`, §7.5                                                       |
| 10  | Idempotency key undefined                                                         | Accepted; §7.4                                                                                    |
| 11  | Interval after paste unspecified                                                  | Accepted; §7.5                                                                                    |
| 12  | `ForegroundCommand` untrustworthy for a permit                                    | Accepted; process identity minted and revalidated by the helper, §6.3                             |
| 13  | SSH unknown echo can be permitted silently                                        | Owner decided: always ask (decision 8); §6.4                                                      |
| 14  | `pane-program` misuses admission `Domain`, closed resource kinds                  | Accepted; pane sub-scope plus typed predicate, work named, §6.3                                   |
| 15  | "Any pane" not implementable through the run fence                                | Owner decided: unbounded for the assistant (decision 6); fence widened in the ADR, §6.2           |
| 16  | Caller authority not bound                                                        | Accepted; two bound capability types, §6.1                                                        |
| 17  | Tests can pass with parts broken                                                  | Accepted; assistant happy path, adversarial schedules, independent author, §12                    |
| 18  | §8 cross-reference wrong                                                          | Fixed                                                                                             |
