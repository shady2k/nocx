# One emulator, in the session runtime

- **Date:** 2026-09-12 (sixth revision; §3 records what each earlier one got wrong)
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

Three things changed in the fifth revision, all from its review round, and they stand:

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
background renderer still takes part in the program's terminal conversation. **And there
is no report to consult:** no contract in `contracts/` carries which panes a client
displays — the whole surface is `panes.create/close/move/setCwd`, `tabs.*`, `attach`,
`layout.read` and `sessions.live/status/inventory` — while `uistate.Layout.ActiveTab`
(`internal/uistate/uistate.go:79`) is remembered WINDOW state written to disk for restore,
one value, not a live per-client report. `WSServer.FocusSession`
(`internal/transport/ws_session_focus.go:42`) asks a client to show a session and is not an
answer about what it shows. Were such a report added it would still be an asynchronous
client claim that can go false between the check and the write.

Instead: **observation and control are separate capabilities.** A person may watch an
agent-driven worker without taking control. Taking control is an explicit operation the
runtime acknowledges, and it invalidates the agent's write authority from a named
boundary — bytes already written to the PTY cannot be recalled. Reading permission stays
independently scoped, so watching grants a coordinator nothing about somebody else's
session (`ADR-0064` §2 survives intact). If opening a pane should pause automation, opening
issues an automatic take-control request; it does not consult a boolean.

### 1.2 The terminal half of the runtime, and what ADR-0065 changed about it

The fifth revision's version of this section was a table about `charmbracelet/x/vt`
concluding that input encoding was "a wiring job rather than a build". **ADR-0065 measured
that, and it is false.** `x/vt` fails all nine behavioural probes, and a modified arrow
(`Ctrl-Left`, `Shift-Up`) or a modified function key emits **nothing at all**. The emulator
the runtime is built on is `libghostty-vt`, and this section is restated against it.

| Needed                                                                                                                        | Measured                                                                                                                                                 |
| ----------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| the same bytes produce the same screen wherever the writes were split                                                         | **yes** — identical to its own whole-file screen under every tested partition, where `x/vt` and headless xterm.js each differ from themselves            |
| key encoding against application modes: application cursor keys, application keypad, `modifyOtherKeys`, Kitty keyboard, flags | present, as configuration driven from terminal state (`ghostty/binding.go:399`)                                                                          |
| wrap / continuation stored per line                                                                                           | **yes** (`REPORT.md` §2.1) — this was the fifth revision's soft-wrap gap, and the new choice CLOSES it                                                   |
| an incremental render-state interface to publish from                                                                         | yes (`REPORT.md` §2.2)                                                                                                                                   |
| answering the program's own `DECRQM` query                                                                                    | **no.** `CSI 4 $p` returns an empty reply (`results/ghostty.jsonl:206`). The C-API mode query works, but the program cannot call the C API. Ours to add. |
| bounded work against a hostile program                                                                                        | `REP` clamped at 65535                                                                                                                                   |

**Three costs, named so they are not discovered later.**

1. **The binding is real work.** A CGo binding and a Zig build, pinned as one matched set of
   source, toolchain and configuration, with object lifetime, ABI churn and a build route for
   every target nocx ships — including the helper's, because the runtime sits beside the PTY.
   ADR-0065 is `Proposed` until `nocx-cm1ac` builds and runs it on macOS arm64.
2. **Application-driven input negotiation is an API claim here, not a measurement.** The
   probes passed `nil` for the terminal (`cmd/ghosttyvt/main.go:207`). "The program enables a
   mode, the runtime derives the encoder from it, the key produces the expected bytes, the
   program disables it and the encoding changes back" is the first integration gate, and it
   is `nocx-ygxjv.2`'s.
3. **An extension seam is lost.** Neither emulator displays sixel, so nothing displayed is
   lost, and `libghostty-vt` additionally decodes Kitty images. But `x/vt` exposed a DCS/APC
   consumer seam and the measured ghostty interface ignores sixel without handing it back
   (`REPORT.md` §2.5), so a future sixel decision becomes upstream's or a patch's rather than
   ours. Graphics sit outside "changed cells" in any case and travel as a separate payload,
   as herdr's do (`src/server/render_stream.rs:94`).

**Input is still a build, not a wiring job.** ADR-0065's reason 3 is that `libghostty-vt` HAS
the encoder — not that nocx has the input path. The runtime still has to own which modes are
set, derive the encoder from them, and admit the encoded bytes in order. That is §6.1, and it
is the largest single item in this document.

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
  encoder → the client's cell renderer displays what the backend has", plus input
  correctness and delivery freshness.

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

**R5, found by `nocx-nqatl` while taking `nocx-ygxjv` into work.** Eight, and the first is
the one that would have cost most, because §4 is what the ownership record is written from.

- **"AD-6's amendment is only POWERS, and stands verbatim"** — wrong, and it preserved
  exactly the rule this design exists to break. The 2026-08-25 amendment also binds the
  grid's LIFETIME (`docs/architecture.md:154`): a grid exists only for an explicitly enrolled
  pane, is discarded at withdrawal, and "an unenrolled pane has no backend grid at any
  point." See §4.1.
- **"The backend knows which panes each client renders"** — wrong, there is no such signal on
  the wire at all. See §1.1.
- **A transitional permission for `nocx-6q1uh`** — a shim, in a greenfield repository that
  does not build them, and §9 withdrew the third revision's version of the same thing three
  paragraphs above granting its own. Owner's decision, 2026-09-12. See §9.
- **"xterm.js remains the renderer"** (§4.1) against "xterm.js goes" (§1, §6.11) — the first
  was text from before the fifth revision's own decision. Stale in §2, §6.8 and §6.9 too.
- **"The terminal half is already bought"** (§1.2) — a table about `x/vt` concluding input
  was a wiring job, which ADR-0065 then measured as nine failures out of nine, a modified
  arrow emitting nothing among them. The section was one revision behind the ADR it produced.
- **"the backend captures block B and removes its rows from the live screen"** (§5) —
  contradicts §1 of the same revision, which forbids rebasing the terminal because a command
  finished. The snapshot-revision conclusion survives; that justification does not.
- **"freshness … is the property ADR-0064 actually depends on"** (§6.10) — wrong. ADR-0064
  depends on RE-READING the frame immediately before the write, not on a human having seen
  it. See §6.10.
- **Terminal replies at the last step** (§9) — they are a precondition of a runtime proved
  with no client attached, which is the epic's own success criterion.

## 4. The invariant, as a new ADR

Not an edit to any existing record.

> **One emulator, on the backend. It owns what the session is; the renderer owns what the
> person sees of it, and what the person does to it arrives as intent, not as bytes.**

### 4.1 Superseded

- `AD-6`'s rule that the backend does not sniff the byte stream, and its refusal list
  (`docs/architecture.md:160`) forbidding grid-derived content from being displayed or
  persisted.
- **`AD-6`'s grid LIFETIME — a separate supersession, and the fifth revision missed it.**
  That revision called the 2026-08-25 amendment's contents POWERS and left them standing
  "verbatim". They are not only powers: the amendment also binds WHEN a grid may exist
  (`docs/architecture.md:154`). "A grid exists for a pane that has been **explicitly enrolled
  for observation** … and for no other pane"; it "closes when that pane's record becomes
  durably terminal or the enrolment is withdrawn, at which point the grid is discarded and
  the pane is never read again"; "An unenrolled pane has no backend grid at any point." A
  session-owned emulator for EVERY session contradicts all three sentences, and so does
  `nocx-ygxjv.3`, whose entire subject is separating grid lifetime from observation
  authority. Leaving the amendment verbatim would have preserved exactly the rule this design
  exists to break.
- **The amendment's limits on what a grid may DECIDE stand verbatim** — no wave state, no
  lifecycle attempt, no execution attempt, no network destination. Those ARE powers, and they
  are not consequences of where the parser runs. The new record must say this in those words
  beside the lifetime supersession, because "a grid may now exist everywhere" reads as "a
  grid may now decide more" unless it is refused out loud.
- `ADR-0001` where it makes xterm.js the VT frontend, and its consequences promising
  frontend-only OSC parsing. **xterm.js is removed, not demoted** (§1, §6.11). The fifth
  revision's "xterm.js remains the renderer; it stops being the VT authority" predates that
  decision and is withdrawn.
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
  choose a capture endpoint. **What does NOT survive is decision 7's PLACEMENT.** The
  rendezvous executes in the renderer today — `BlockManager.freezeFromAttempt`, `sightFence`
  and `_pendingFence` behind its `FENCE_DEFER_MS` timer
  (`frontend/src/scrollback/blocks.ts:2479`, `:2542`) — because the renderer owned the VT.
  It moves beside the emulator. The invariants above are preserved and their executor
  changes; a record that keeps decision 7 whole keeps two named owners for one rendezvous.
- `internal/notify`'s trust classes.
- `ADR-0064`'s permission boundary.

## 5. Attach is a frame AND a card projection, related by a revision

"Attach is just a first frame" is true of the live rectangle only. The failing case:

1. the client loads the cards it can see;
2. the backend seals block B's capture at its closing boundary;
3. the client receives the frame taken after that boundary.

B is in neither read. Reverse the order and it is in both. **Note what this case is not:**
the fifth revision wrote step 2 as the backend capturing B and "removing its rows from the
live screen", which §1 forbids — the terminal is not rebased because a command finished, and
an immutable capture and the same cells still on screen are both legitimate at once. The
race is between two independent READS, not between a card and rows it took away. So attach delivers **a snapshot
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

**Soft wrap is an emulator requirement, not a format choice — and ADR-0065 changed who owes
it.** `x/vt`'s scrollback stores cloned cell arrays with no per-line continuation field
(`scrollback.go:13`), which is why the fifth revision listed continuation as work to be
built. `libghostty-vt` stores wrap per line (`REPORT.md` §2.1), so the emulator half is
bought. What remains is carrying continuation through the capture record and the wire format
without losing it, and that is the adapter's.

**This section is also thinner than its own bead, and the bead is right.** `nocx-2v80t.2`
names the shape: an opening snapshot, the ordered changes and **the rows that LEFT the
screen** during the interval, a closing snapshot at the authenticated boundary, and explicit
completeness and retention. The table above is the WIRE FORMAT of a card's rows; the capture
RECORD is that other thing, and a long unfinished command whose first rows have scrolled away
is neither a finished card nor a live cell.

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
selection model over stable content identities. The fifth revision framed this as xterm
selection against DOM selection "when xterm paints synthesised ANSI"; with xterm removed
(§6.11) there is no synthesised ANSI, and the two mechanisms are the cell renderer's
coordinate selection and the DOM's. The hard part is unchanged: a live frame can change — or
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
the backend can classify menu B while the browser still displays menu A, so an action taken
against a screen needs an explicit relationship to the revision that screen was read at. This
is freshness, not disagreement.

**It is not what `ADR-0064` depends on, and the fifth revision said it was.** ADR-0064
requires the frame to be RE-READ immediately before the write, so that "the identification
that authorises it is the one taken microseconds before rather than the one a caller saw"
(`docs/decisions/0064-a-pane-that-is-read-may-be-answered.md:75`, implemented at
`internal/agenttyping/agenttyping.go:371` → `:402` and `:545` → `:570`). Nothing there
requires a human to have SEEN the frame, and requiring it would let a slow observer stop
control. Freshness is a real and separate obligation, and it is an admission question (§6.1):
an action is bound to evidence the ACTING caller obtained, and the contract names what is
revalidated between admission and execution.

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

## 9. Order of work

1. This document reviewed; the ownership record written and accepted (`nocx-g5p8c`).
2. The measurements of §8 (`nocx-rpzdo`).
3. One emulator per session on the backend, replacing the enrolment-scoped grid — **and
   answering the program's own questions from it** (§6.5). Grid lifetime separates from
   observation authority in the same change: `paneEnroller.Enrol`
   (`internal/app/paneenrol.go:102`) creates the grid and rejects an existing one.
4. Backend-owned block boundaries, with §6.4's rendezvous intact and executed beside the
   emulator rather than in the renderer (§4.2).
5. Structured input (§6.1) and non-visual effect delivery (§6.2).
6. The live region as frames, starting from **full snapshots** and the simplest renderer
   whose correctness can be inspected (§6.11), with a per-client baseline. Delta encoding and
   a scroll-aware encoder (§7) are optimisations of a foundation that is already correct.
7. The card wire format (§6.3) and the capture record (`nocx-2v80t.2`); the frontend block
   lifecycle deleted; `history.record` retired, including the masked command and capture
   offers its ack carries today (`frontend/src/history-client.ts:67`).
8. Resize inversion, recovery, the alternate screen, selection (§6.6–§6.9).

**Terminal replies are not a late step, and the fifth revision made them one.** It put them
at the end, after the emulator and structured input. But `nocx-ygxjv.2`'s criterion is that a
program's own query is answered from the runtime's state, and a runtime proved with NO CLIENT
ATTACHED cannot defer the program's own conversation: today xterm answers from its own
coordinates (`frontend/src/terminal-content.ts:4011`, an `onData` path with no guard of any
kind) while the backend's replies are read and dropped
(`internal/panegrid/panegrid.go:195`). Two answerers is the defect. One of them stops in the
same change that makes the other authoritative, which is why this is step 3.

**The cutover of live display, cards and interaction is one coordinated step, not three.**
Step 6 cannot precede step 7: a server diff against frame F plus a surviving `clearViewport`
(`frontend/src/scrollback/controller.ts:722`) means the next diff assumes cells the client
has erased.

**`nocx-6q1uh` gets no early permission, and needs none.** The third revision tried to give
it one by declaring the backend authoritative while the frontend still cleared its own buffer
and still answered the program's questions. That was withdrawn — it does not repair the
divergence, it exposes it to a new consumer — and then this section granted a smaller one in
its place: automation proceeds on a pane no client displays. Both are the same shape, a
mechanism that exists only to start one epic sooner and is deleted at the cutover. **nocx is
greenfield and does not build shims** (AGENTS.md: "no backward-compatibility shims … no
quick-win hacks. YAGNI"), and the second grant additionally rested on a signal the wire does
not carry (§1.1). What remains is the edge the tracker already holds: `nocx-6q1uh` is blocked
by `nocx-ygxjv`. Its API and its backend may be designed and built against (3) and (4); its
calls reach live panes after the cutover. Nothing here withdraws an existing `workers`
capability — those are not a new permission and were not granted by this document.

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

Filed while writing the sixth revision:

- **`nocx-g5p8c`** — the ownership record of §4 is unwritten, and three epics cite ADR-0065
  as though it had done that job. ADR-0065 supersedes ADR-0041's CHOICE only, and is itself
  `Proposed`.
- **`nocx-rpzdo`** — the §8 measurements do not exist, and `nocx-kkn89` was closed as though
  they did.
- **`nocx-nqatl`** — this revision.
