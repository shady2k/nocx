# One emulator, in the session runtime

- **Date:** 2026-09-12 (fifth revision; §3 records what each earlier one got wrong)
- **Status:** draft for review
- **Owner's decision, 2026-09-12:** the long-term model, taken deliberately over the
  smaller incremental option. **"Нам и нужна долгосрочная модель, никаких быстрых побед.
  Нужно сделать архитектурно правильно."** The scope in §6 is therefore accepted scope, not
  a list of risks to be traded away later.
- **Binding documents this crosses:** `AD-1`, `AD-6`, `AD-9`, `AD-10`,
  [ADR-0001](../../docs/decisions/0001-xterm-js-as-vt-frontend.md),
  [ADR-0002](../../docs/decisions/0002-native-tabs-no-embedded-multiplexer.md),
  [ADR-0008](../../docs/decisions/0008-command-blocks-as-a-keyboard-first-ledger.md),
  [ADR-0009](../../docs/decisions/0009-dom-scrollback-with-explicit-cell-geometry.md),
  [ADR-0024](../../docs/decisions/0024-authenticated-shell-integration-channel.md),
  [ADR-0041](../../docs/decisions/0041-x-vt-as-the-backend-emulator.md),
  [ADR-0064](../../docs/decisions/0064-a-pane-that-is-read-may-be-answered.md).
- **Beads:** `nocx-kkn89` (this design), epics `nocx-eidfb`, `nocx-6q1uh`, `nocx-dkawo`.
- **Reference read:** `~/repos/herdr` — `src/server/render_stream.rs`,
  `src/protocol/render_ansi.rs`, `src/protocol/wire.rs`.

## 1. The decision

**One VT emulator in the system, in a long-lived session runtime that sits beside the PTY.
It owns the screen, the terminal's modes, the answers to the program's own questions, the
block boundaries and the block content. The client paints cells and sends intent.**

Three things changed in this revision, all from the fifth review round:

- **The runtime sits beside the PTY, not in the coordinator.** For a remote session that is
  beside the remote PTY, with SSH as an authenticated carrier to it. Today the helper can
  keep a process alive while the coordinator loses the output needed to rebuild its
  emulator (`internal/transport/ws_readopt.go:100`); this removes that failure mode
  structurally rather than recovering from it. The desktop process and the orchestration
  coordinator may die without taking terminal state with them.
- **A block never owns part of the current terminal.** The terminal is not cleared, rebased
  or reinterpreted because a command finished, and the live surface stops being defined as
  "the rows below the current command". The whole terminal is the session's mutable
  surface; cards are records of activity in it. `CUP 1;1` always addresses the terminal's
  first row and a card never competes for it. This removes the cut AND the low-water-mark
  problem at once.
- **The client receives cells, not ANSI, and xterm.js goes.** Keeping xterm as a painter
  would mean the browser executing cursor moves and erases only to rebuild the cells the
  runtime already has, and would keep selection bound to xterm's private machinery
  (`ADR-0009:122` records that dependency). Verified: xterm's only input is `write(bytes)`
  and its buffer is read-only (`xterm.d.ts:809`, `:1216`), so cells cannot be pushed in and
  its renderer cannot be addressed without its parser.

| Owner                            | Owns                                                                                                                                                      |
| -------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| session runtime (beside the PTY) | the emulator; terminal modes; committed geometry; ordered input admission; the answers to the program's questions; the authenticated lifecycle rendezvous |
| capture producer                 | terminal observations bound to an execution interval; immutable capture bodies; explicit completeness                                                     |
| document service                 | entries, attempts, the ordered block tree, artifact references, retention, permissions                                                                    |
| projection service               | consistent client snapshots and the changes after them, relating terminal state and document references by revision                                       |
| client                           | layout, font measurement, painting, selection, accessibility, IME — and submission of **intent**                                                          |

The first two live together to begin with; the last two may share a process. These are
ownership boundaries, not a demand for separate services.

### 1.1 Watching is not controlling

The fourth revision proposed that automation may act on a pane no client displays. That is
withdrawn: hidden panes still mount renderers (`frontend/src/panes.ts:1728`) and the
terminal-reply path has no visibility guard (`frontend/src/terminal-content.ts:4011`), so a
background renderer still takes part in the program's terminal conversation — and
visibility is an asynchronous client report that can go false between the check and the
write.

Instead: **observation and control are separate capabilities.** A person may watch an
agent-driven worker without taking control. Taking control is an explicit operation the
runtime acknowledges, and it invalidates the agent's write authority from a named
boundary — bytes already written to the PTY cannot be recalled. Reading permission stays
independently scoped, so watching grants a coordinator nothing about somebody else's
session (`ADR-0064` §2 survives intact). If opening a pane should pause automation, opening
issues an automatic take-control request; it does not consult a boolean.

### 1.2 The terminal half of the runtime is already bought

`charmbracelet/x/vt` is in the tree, already drives the enrolled-pane grid, and was measured
against headless xterm.js for column geometry in `ADR-0041`. Verified in the module:

| Needed                                                                                                                      | Present                                                                  |
| --------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| parse, grid, alternate buffer, charsets, OSC, DCS, mouse, focus                                                             | yes                                                                      |
| 19 modes including DECCKM `?1`, bracketed paste `?2004`, alt-screen `?1047/?1049`, mouse `?9/?1000/?1001/?1002/?1003/?1006` | `mode.go`                                                                |
| **key encoding against those modes** — `SendKey(uv.KeyEvent)`                                                               | `key.go:25`                                                              |
| **paste wrapped per bracketed-paste** — `Paste(text)`                                                                       | `emulator.go:301`                                                        |
| `SendText`, `SendKeys`, `InputPipe()` — where encoded bytes leave for the PTY                                               | `emulator.go:294-316`                                                    |
| replies to the program (device attributes, in-band resize)                                                                  | generated; read and dropped today at `internal/panegrid/panegrid.go:195` |
| damage tracking — `Damage`, `CellDamage`, `RectDamage`                                                                      | `damage.go`                                                              |

So "input becomes intent", which the review called the largest missing item, is a wiring
job rather than a build: the client sends a key event, the runtime calls `SendKey`, the
bytes leave through `InputPipe`. Damage tracking is already the substrate a diff encoder
needs later.

**Two real gaps, named so they are not discovered:**

1. **Graphics.** No sixel and no kitty protocol anywhere in `x/vt`. herdr carries graphics
   as a separate payload outside its cell diff (`src/server/render_stream.rs:94`). Either
   nocx declares them unsupported or this is our own work.
2. **Soft wrap.** Buffer lines carry no continuation flag — `Wrap` in ultraviolet belongs to
   `StyledString` at print time, not to a stored line. The card serialiser joins
   continuations by xterm's `isWrapped` today (`frontend/src/scrollback/serializer.ts:483`).
   The runtime must preserve continuation **while parsing**; it cannot be recovered
   afterwards.

## 2. What this buys, stated exactly

These stop existing rather than being solved:

- **Seeding a second emulator with application state.** The browser never needs the
  program's saved cursor, parser continuation, margins or inactive buffer in order to
  execute the next bytes, because it no longer executes them.
- **Safe-boundary checkpointing for a browser attach.** The backend can snapshot applied
  cells while keeping its own incomplete parser state to itself. (Recovery of the BACKEND
  emulator is a separate problem — §6.7.)
- **The clear as a wire event.** A client receives consequences, not an instruction to
  mutate its own terminal.
- **Equivalence between two application emulators**, and the fidelity oracle between them.

What does **not** disappear, contrary to the third revision:

- **Ordering obligations.** Capture-before-discard, card/frame coherence, reconnect and
  baseline recovery all remain — they move, they do not vanish.
- **The fence.** §6.4.
- **Fidelity testing.** Its subject changes from "two emulators agree" to "backend →
  encoder → xterm displays what the backend has", plus input correctness and delivery
  freshness.

## 3. What the earlier revisions got wrong

Kept because each was believed and acted on.

- **R1:** a late client is limited to the 256 KB ring — wrong, `reclaimSession`
  (`frontend/src/ipc.ts:1179`) reads recorded output first. A session with no renderer
  records no history — wrong, `syncLifecycleLedger`
  (`internal/transport/ws_lifecycle.go:342`) closes the row from authenticated facts; what
  is renderer-dependent is the frozen output ARTIFACT.
- **R1:** the two screens need not agree — wrong; a delegated worker gets an ordinary tab
  (`frontend/src/panes.ts:959`), so a person can watch the pane an automated caller writes
  into.
- **R2:** the live region has no offset — wrong, it has one
  (`frontend/src/scrollback/controller.ts:470`), conditionally released at `:479`.
- **R2:** xterm's scrollback depth does not touch the coordinate question — wrong; on a
  height increase xterm recovers rows from scrollback and moves the cursor row.
- **R3:** "the fence machinery goes" — wrong, see §6.4. "Attach is a first frame" — true
  only of the live rectangle, see §5. "Frame diffs emit fewer bytes than raw output" —
  not unconditionally, see §7. "Nothing else about xterm changes" — wrong, input encoding
  and non-visual effects live in xterm today, see §6.1 and §6.2. "The tests do not need a
  browser" — wrong; cell geometry, hit-testing, IME and selection still do.

## 4. The invariant, as a new ADR

Not an edit to any existing record.

> **One emulator, on the backend. It owns what the session is; the renderer owns what the
> person sees of it, and what the person does to it arrives as intent, not as bytes.**

### 4.1 Superseded

- `AD-6`'s rule that the backend does not sniff the byte stream, and its refusal list
  (`docs/architecture.md:160`) forbidding grid-derived content from being displayed or
  persisted — **but only those two**. The 2026-08-25 amendment's limits on what a grid may
  DECIDE (no wave state, no lifecycle attempt, no execution attempt, no network
  destination) are POWERS, not consequences of where the parser runs, and they stand
  verbatim.
- `ADR-0001`'s consequences promising frontend-only OSC parsing. xterm.js remains the
  renderer; it stops being the VT authority.
- `ADR-0008`'s consequences promising a byte-blind backend.
- `ADR-0002` where it closed server-side terminal state.
- `ADR-0009` where the block's content is serialised from the client's buffer. Its
  substance — the DOM owns the frozen block's presentation and geometry — stands.
- **`AD-1`**, in the part where the data plane carries raw PTY bytes in both directions. It
  carries frames outbound and structured input inbound; the two-plane split survives.
- **`AD-9`** and the `session.output` contract, including its no-grid reasoning.
- **`AD-10`**, which promises bounded per-session credit and lossless ordered byte delivery.
  Coalesced visual updates need a different delivery contract: what is still lossless (the
  ingest into the emulator, the ledger), what is explicitly lossy (intermediate frames), and
  how fairness and credit are expressed over frames.
- The `ledger.capture` contract's description of a renderer serialiser.
- `nocx-eidfb`'s body reasoning, amended by note.

### 4.2 Not superseded, and not to be absorbed

- The `AD-6` bootstrap-window carve-out. Parsing VT is not permission to interpret bootstrap
  readiness tokens or to write bootstrap frames outside their interval.
- **`ADR-0024` decision 1 and decision 7.** A sighted marker may only LOCATE an
  already-authenticated event; it never authorises one. The rendezvous is **OSC 1337 with a
  matching nonce** (`internal/shellintegration/scripts/nocx.bash:1505`,
  `frontend/src/renderers/xterm.ts:580`) — not ordinary OSC 133, whose `C`/`D` have no
  meaning of their own. A program printing a forged `D` must not be able to close a block or
  choose a capture endpoint.
- `internal/notify`'s trust classes.
- `ADR-0064`'s permission boundary.

## 5. Attach is a frame AND a card projection, related by a revision

"Attach is just a first frame" is true of the live rectangle only. The failing case:

1. the client loads the cards it can see;
2. the backend captures block B and removes its rows from the live screen;
3. the client receives the post-freeze frame.

B is in neither. Reverse the order and B is in both. So attach delivers **a snapshot
revision** — cards through revision R, the live frame at revision R — and then changes after
R. Geometry, the active presentation, the running block and the availability of an artifact
belong to the same revision.

herdr does not treat frames as stateless either: it prepares against a baseline and commits
that baseline separately after sending (`src/server/render_stream.rs:121`). nocx adds a
second client-side presentation — the cards — to that protocol.

## 6. The contracts this commits us to

Accepted scope, per the owner's decision.

### 6.1 Input becomes intent, not bytes

The largest item, and it was missing from the third revision. xterm today encodes keys
against application modes — arrow keys read `applicationCursorKeys`
(`@xterm/xterm` `Terminal.ts:1023`) — and nocx delegates paste wrapping to xterm's
bracketed-paste handling (`frontend/src/renderers/xterm.ts:995`). A frame carrying cells and
a cursor conveys none of that, so a program that enabled DECCKM or bracketed paste would
receive **differently encoded input while the screen looked correct**.

herdr carries the other half of the architecture: structured key, text, mouse, paste and
focus events (`src/protocol/wire.rs:115`). nocx moves application input encoding to the
backend, where the modes live. What the frontend sends is intent.

### 6.2 Non-visual effects need their own delivery

BEL, notification requests, OSC 52 clipboard writes, cwd, title and shell snapshots can
leave every cell unchanged and each has a handler today
(`frontend/src/renderers/xterm.ts:936`, `:972`, `:489`). They need event delivery with
permissions and an explicit replay rule: **a full frame must never repeat a clipboard write
or a notification.**

### 6.3 The card's wire format

Styled rows are not enough. It must distinguish:

| Information                                                            | Why                                                                                                                                             |
| ---------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| grapheme and cell boundaries, authoritative column widths              | `cell-fit` takes chars, width, bold and italic and measures locally because fonts differ per machine (`frontend/src/scrollback/cell-fit.ts:23`) |
| logical lines vs physical continuation rows                            | the serialiser joins `isWrapped` continuations and preserves hard newlines (`frontend/src/scrollback/serializer.ts:483`)                        |
| default / palette / RGB colour, kept apart                             | restored SGR is deliberately repainted in the current theme (`frontend/src/scrollback/restored-block.ts:113`)                                   |
| blank cells, trailing spaces, line column counts                       | HTML, SGR and text share one walk today so they agree (`serializer.ts:343`)                                                                     |
| block identity, kind, placement, author, status, artifact availability | a missing artifact is not empty output, and a restored block can carry prose (`restored-block.ts:51`)                                           |
| links, with identity and validation                                    | they are decorated today and must survive                                                                                                       |

**Soft wrap is an emulator requirement, not a format choice.** `x/vt`'s scrollback stores
cloned cell arrays with no per-line continuation field (`scrollback.go:13`), so the backend
must preserve continuation while parsing — it cannot be recovered afterwards.

### 6.4 The fence stays

`ADR-0024` decision 7 records that SSH orders each channel independently and that an
authenticated completion can arrive before the last output bytes (`:566`). Moving both
consumers into one process does not order their arrivals. So a pending rendezvous state and
a bounded missing-fence policy remain; only the 500 ms number is free to change. Both
arrival orders need handling, and if the fence lands first and later output overwrites or
trims its rows before the authenticated event arrives, **the capture source must survive
that interval** — a row number is not enough.

### 6.5 Terminal replies

Answered from the one emulator. Which component transmits is free; what it says is not.
Today xterm answers from its own coordinates
(`frontend/src/terminal-content.ts:4011`) while the backend's replies are read and dropped
(`internal/panegrid/panegrid.go:195`).

### 6.6 Resize inverts

The client resizes immediately (`frontend/src/renderers/xterm.ts:633`) and tells the backend
after an 80 ms debounce (`frontend/src/terminal-content.ts:3395`). Under this model the
client reports, the backend decides, the frame follows. The transitional behaviour — what a
client shows between its own resize and the first frame at the new geometry — must be
stated.

### 6.7 Recovery of the backend emulator

A helper session can outlive the coordinator and report an output-window hole
(`internal/transport/ws_readopt.go:100`). An emulator fed only the surviving suffix is not
authoritative, and one that missed a midstream interval is not either. Completeness must be
establishable, and while it is unknown the write gate refuses and the attach says so.

### 6.8 The alternate screen, and 6.9 selection

What a client is sent while a full-screen program owns the pane, and on its exit. And a
selection model over stable content identities: xterm selection and DOM selection remain
different mechanisms when xterm paints synthesised ANSI, and a live frame can change — or
become a card — mid-drag (`frontend/src/renderers/xterm.ts:976`,
`frontend/src/terminal-content.ts:3335`, `SelectionService.ts:489`).

### 6.11 The client's cell renderer

xterm.js goes, so the client needs a renderer that takes **cells** and paints them: grapheme
boundaries with authoritative widths, default/palette/RGB kept apart, links, cursor,
selection by coordinate, IME composition and accessibility. It is not a terminal emulator
and must not contain a parser.

Verified: no such thing is available by extracting it from what we have. xterm.js takes only
`write(bytes)` and exposes a read-only buffer; its canvas and WebGL addons render their own
`Terminal`'s buffer and cannot be addressed without it. hterm has the same shape. Every web
terminal is a parser-and-renderer bundle and we want only the second half.

So this is a decision with a search in front of it, and the search is a task of its own with
stated criteria: takes cells rather than bytes; carries grapheme widths; supports selection
by coordinate, IME and accessibility; is readable; and is licensed to fork. Candidates to
evaluate include cell-grid renderers from outside the terminal world — TUI frameworks
compiled to wasm, grid/canvas text layers — and a fork of xterm.js keeping only its renderer
and selection. Writing one is the fallback, not the plan.

Per the build order (§9), the reference client starts with **full snapshots** and the
simplest renderer whose correctness can be inspected. Delta encoding and a fast canvas are
optimisations of a foundation that is already correct, not the organising principle of it.

### 6.10 Delivery freshness

One authority does not mean the person has seen its latest state. With frame-rate coalescing
the backend can classify menu B while the browser still displays menu A, so automation
against a **displayed** pane needs an explicit relationship to the displayed revision. This
is freshness, not disagreement — but it is the property `ADR-0064` actually depends on.

## 7. The throughput question, corrected

The third revision claimed frame diffs always emit fewer bytes than raw output. That is
false as stated. herdr's encoder compares cells at fixed coordinates and does **not**
recognise scrolling (`src/protocol/render_ansi.rs:773`). A 100×40 terminal producing 600
distinct full-width lines per second, displayed at 60 fps: raw printable output ≈ 60 KB/s;
a positional diff rewriting ~4,000 cells per frame ≈ 240 KB/s before ANSI overhead.

So: coalescing wins hugely when the producer far outruns the display, and loses on
scrolling unless the encoder is **scroll-aware** — which is more than herdr's encoder does
and is therefore ours to build. Compact VT operations (scroll, erase, insert, repeat) change
many cells from few source bytes and amplify the same effect; very wide terminals multiply
it; graphics and sixel are outside "changed cells" entirely and herdr carries them as a
separate payload (`src/server/render_stream.rs:94`).

**And measure TOTAL delivery, not live delivery.** If a card promises the complete retained
output, the lines omitted from live frames still reach the browser when that card is
fetched.

## 8. Measurements, before anything is built

1. Frame diffs against raw bytes on the hero case, with a **scroll-aware** encoder and
   without, at the frame rate we would ship — and total bytes including card fetches.
2. Backend memory and CPU per session with an emulator, at real tab counts.
3. First-frame size and latency for an attaching client.
4. Client cost of applying diffs against parsing raw output.
5. Backend cost of capture, storage and per-client diffing.

## 9. Order of work — and what `nocx-6q1uh` may and may not do meanwhile

The third revision tried to give `nocx-6q1uh` an early unblock by declaring the backend
authoritative while the frontend still cleared its own buffer and still answered the
program's questions. **That is withdrawn.** It does not repair the divergence; it exposes it
to a new consumer.

1. This document reviewed; the new ADR written and accepted.
2. The measurements of §8.
3. One emulator per session on the backend, replacing the enrolment-scoped grid. Grid
   lifetime separates from observation authority in the same change: `paneEnroller.Enrol`
   (`internal/app/paneenrol.go:102`) creates the grid and rejects an existing one.
4. Backend-owned block boundaries, with §6.4's rendezvous intact.
5. Structured input (§6.1) and non-visual effect delivery (§6.2).
6. The encoder (§7), per-client baseline, and the live region as frames.
7. The card wire format (§6.3); the frontend block lifecycle deleted; `history.record`
   retired, including the masked command and capture offers its ack carries today
   (`frontend/src/history-client.ts:67`).
8. Terminal replies, resize inversion, recovery, alt-screen, selection (§6.5–§6.9).

**The cutover of live display, cards and interaction is one coordinated step, not three.**
Step 6 cannot precede step 7: a server diff against frame F plus a surviving
`clearViewport` (`frontend/src/scrollback/controller.ts:722`) means the next diff assumes
cells the client has erased.

**`nocx-6q1uh` meanwhile.** Its API and its backend may be designed and built on (3) and
(4). What it may **not** do before the cutover is act on a pane a client is **displaying** —
that is exactly the unpaid authority. The backend knows which panes each client renders, so
the rule is expressible: automation proceeds on a pane no client displays, and is refused,
by a named reason, on one that is. That is the coordinator's ordinary case — workers in
background tabs — and it collects no authority it has not paid for.

`nocx-eidfb` closes after the cutover, not at step 6, and `.4`, `.3` and `.5` remain its
prerequisites.

## 10. Deliberately out

- **Keeping xterm.js.** It goes. §1 records why, and the client's renderer is §6.11.
- **What a write may DO.** `ADR-0064`'s bounds are untouched.

## 11. Beads this design produces

- **The `savedY` hazard** — live today, bounded to normal-buffer state retained across a
  freeze; the cutover removes the cause, the bead exists because the defect does not wait.
- **Terminal-reply ownership** (§6.5).
- **The frozen output artifact off the renderer** — the first concrete slice of §6.3.
- **Structured input** (§6.1) — large enough to be its own epic.
