# The backend owns the session's screen and its blocks

- **Date:** 2026-09-12 (third revision. The first two are superseded and §3 says what
  each got wrong; they were reviewed by codex and the corrections are folded in.)
- **Status:** draft for review
- **Owner's decision:** taken in conversation on 2026-09-12, in three steps — the backend
  holds the screen; the renderer's destructive cut is the problem, not the remedy; and
  **the backend owns the blocks.** The third step is what this revision is about.
- **Binding documents this crosses:** `AD-1`, `AD-6`, `AD-9`, `AD-10`,
  [ADR-0001](../../docs/decisions/0001-xterm-js-as-vt-frontend.md),
  [ADR-0002](../../docs/decisions/0002-native-tabs-no-embedded-multiplexer.md),
  [ADR-0008](../../docs/decisions/0008-command-blocks-as-a-keyboard-first-ledger.md),
  [ADR-0009](../../docs/decisions/0009-dom-scrollback-with-explicit-cell-geometry.md),
  [ADR-0024](../../docs/decisions/0024-authenticated-shell-integration-channel.md),
  [ADR-0041](../../docs/decisions/0041-x-vt-as-the-backend-emulator.md),
  [ADR-0064](../../docs/decisions/0064-a-pane-that-is-read-may-be-answered.md).
- **Beads:** `nocx-kkn89` (this design), epic `nocx-eidfb`, epic `nocx-6q1uh`, `nocx-dkawo`.
- **Reference read for this revision:** `~/repos/herdr`, `src/server/render_stream.rs` and
  `src/protocol/render_ansi.rs`.

## 1. How this document arrived where it is

It began as the evidence question under `nocx-6q1uh`: a write into a pane must be judged
against that pane's screen, and a coordinator's worker pane has a backend grid while a
person's own pane does not.

Three moves followed, each the owner's:

1. **The backend holds the screen.** One source of evidence for both callers, and the same
   thing a second machine needs to attach.
2. **The renderer's cut is the problem.** Every attempt to reconcile two screens failed on
   one operation — the renderer destroys rows the backend keeps — and reconciling it is not
   possible, because the moment of that operation is not a function of the byte stream.
3. **The backend owns the blocks.** The reason two representations exist at all is that the
   frontend decides, captures and freezes command blocks from its own buffer. Move that
   ownership and the second representation stops existing.

The third move is what makes the whole thing smaller instead of larger, and it is what
this revision records.

## 2. What herdr does, and why it is simpler

`herdr` keeps the terminal state on the server and sends clients **frames**, in two
encodings (`src/server/render_stream.rs`): `SemanticFrame`, a whole frame with identical
frames skipped; and `TerminalAnsi`, the **diff re-encoded as ANSI**
(`src/protocol/render_ansi.rs`) — first frame a full redraw, then only changed cells,
wrapped in synchronised output with the cursor hidden for the paint.

It is simpler than anything we have proposed, and the reason is not that it runs on a
server. **herdr has no command blocks.** One grid, one representation, nothing to keep in
step. Every difficulty in the first two revisions of this document came from nocx having a
second representation of the same content and having built it on the client.

## 3. What the first two revisions got wrong

Recorded because each was believed and acted on, and the corrections are what shaped this
one.

- **The first revision** claimed a late client is limited to the 256 KB replay ring, and
  that a session with no renderer records no history. Both wrong. `reclaimSession`
  (`frontend/src/ipc.ts:1179`) reads recorded output before joining the ring;
  `syncLifecycleLedger` (`internal/transport/ws_lifecycle.go:342`) completes the ledger row
  from authenticated facts. What is renderer-dependent is the **frozen output artifact**,
  not history.
- **The first revision** argued the two screens need not agree because they answer different
  questions. They do need to agree, because the same pane can be watched by a person while
  an automated caller writes into it — a delegated worker gets an ordinary tab
  (`frontend/src/panes.ts:959`).
- **The second revision** said the live region has no offset. It has one
  (`frontend/src/scrollback/controller.ts:470`), conditionally released when the echo row is
  overwritten (`:479`). The historical `nocx-m87n` diagnosis no longer describes the code.
- **The second revision** said xterm's scrollback depth does not touch the coordinate
  question. It does: on a height increase xterm recovers rows from scrollback and moves the
  cursor row, so the same session grown from 3 rows to 5 lands differently with a 0-line and
  a 10-line scrollback.
- **A shared, ordered clear** (the third option considered) survives review but costs a
  protocol: the backend feeds its grid before the delivery ring
  (`internal/transport/ws.go:3289`), so a renderer that clears and then reports is always
  late, and the clear would have to be committed by the backend and applied by clients — a
  session-state and protocol change spanning submission, lifecycle, output application,
  capture and recovery, with nine failure modes each needing a test.

**The decision below removes the need for that protocol rather than specifying it.**

## 4. The decision

**One emulator in the system, on the backend. It owns the screen, the block boundaries and
the block content. The frontend renders what it is sent.**

| Owner    | Owns                                                                                                                                 |
| -------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| backend  | the VT emulator, one per session; OSC 133; where each block begins and ends; **the block's content**; the ledger; the session's size |
| frontend | painting the cards it is sent; the live region; selection, input, and everything else DOM                                            |

Consequences that follow without being designed:

- **No divergence to manage.** There is one emulator, so there is nothing to reconcile, no
  equivalence requirement, no fidelity oracle between two implementations.
- **Attach is a first frame.** A client that attaches gets a full redraw as its first frame —
  that is what the encoder already does for its first frame — and the repaint protocol,
  the safe-boundary checkpointing and the snapshot/offset ordering all disappear.
- **The clear is not an event.** Whatever nocx does at a command boundary, the backend does
  it once and the clients receive its consequences. No ordered clear, no nine failure modes.
- **The fence machinery goes.** The 500 ms deferred fence, `beginBlockNow` opening a block
  before the authenticated fact arrives, `_settleFrozen`, `_freezeVisual` and `clearViewport`
  exist because the decision is taken where the facts are not. They are not ported; they are
  deleted.
- **`history.record` goes.** The backend already opens and closes the durable row; with the
  content in hand it stops needing the renderer to send the facts back.

### 4.1 The live region

It arrives as **frame diffs**, herdr's `TerminalAnsi` shape: a full redraw first, then
changed cells only.

`AD-1` chose raw binary PTY frames for hero rendering performance, and this crosses that
choice — but not in the direction it assumes. A fast-scrolling log emits **fewer** bytes as
frame diffs at a fixed frame rate than as raw PTY output, because intermediate frames are
never painted; that is how tmux and herdr stay fast under `yes`. This must be measured
(§7), not assumed in either direction.

### 4.2 What stays on the frontend, and what moves

Of ~6,900 non-test lines under `frontend/src/scrollback/`, the block LIFECYCLE —
`blocks.ts` (3,028), `controller.ts` (1,134), `serializer.ts` (708) — plus
`history-client.ts` and `history-outbox.ts` (340) is what moves: roughly 5,200 lines become
"render what the backend sent".

Card PRESENTATION stays: `cell-drift`, `cell-fit`, `cell-metric`, `sgr`, `sgr-read`,
`restored-block`, `shell-paint`. The card is still HTML on the client.

This is a relocation, not a saving, and it should not be sold as one. What it buys is that
the copy is single, the tests do not need a browser, and a whole class of races stops
existing because the decision is finally taken where the facts are.

## 5. What this supersedes, and what it must not absorb

A **new ADR**, not an edit to any existing record.

> **One emulator, on the backend. It owns what the session is; the renderer owns what the
> person sees of it.**

**Superseded:**

- `AD-6`'s rule that the backend does not sniff the byte stream, its refusal list
  (`docs/architecture.md:160`) forbidding grid-derived content from being displayed or
  persisted, and both carve-outs, which exist only because of that rule.
- `ADR-0001`'s consequences promising frontend-only OSC parsing. xterm.js remains the
  renderer; it stops being the VT authority.
- `ADR-0008`'s consequences promising a byte-blind backend.
- `ADR-0002` where it closed server-side terminal state; its revisit trigger is recorded as
  fired.
- `ADR-0009` in the part where the block's content is serialised from the client's buffer.
  Its substance — the DOM owns the frozen block's presentation and geometry — stands.
- `AD-9` and the `session.output` contract, including its no-grid reasoning.
- The `ledger.capture` contract's description of a renderer serialiser.
- `nocx-eidfb`'s body reasoning, amended by note.

**Not superseded, and must not be absorbed:**

- The `AD-6` bootstrap-window carve-out. Permission to parse VT is not permission to
  interpret bootstrap readiness tokens or to write bootstrap frames outside their interval.
- **`ADR-0024` decision 1.** Its rule is narrower than "no outcomes from the stream": a
  sighted OSC 133 may only **locate an already-authenticated event** through the rendezvous
  at `:164`, and `C`/`D` have no meaning of their own. A program printing a forged `D` must
  not be able to close a block or choose a capture endpoint. Backend ownership of boundaries
  is compatible with this only if an authenticated fact authorises the transition and the
  sighted marker merely locates it. If ordinary `C`/`D` are to acquire independent meaning,
  the new ADR must say so explicitly rather than arrive at it by implication.
- `internal/notify`'s trust classes.
- `ADR-0064`'s permission boundary. One source of evidence authorises nobody to read a pane
  they could not read before.

## 6. What still has to be designed

Removing the protocol does not remove these.

1. **The encoder.** herdr's is ~2,000 lines and the hard parts are synchronised output,
   hiding the cursor across a paint, batching adjacent changes and minimising cursor
   movement. Ours is new work, and it is the riskiest single piece.
2. **Per-client baseline and resync.** herdr carries `seq`, `repaint_pending` and
   `reset_baseline` (`src/server/render_stream.rs`). A client whose baseline is unknown gets
   a full redraw; the state machine for deciding that is owed.
3. **What a card carries on the wire.** Rows with styles, or something structured. It must
   be enough for the existing presentation — cell geometry, SGR, links, restoration — and it
   is a contract in `contracts/`, generated for the renderer, validated in Go.
4. **The unfinished command.** A long command's rows that have scrolled above the live
   screen are not yet a block and not on the screen. The backend now has them; what it keeps
   and for how long is a retention decision, and `x/vt`'s own scrollback defaults to 10,000
   lines with `SetMaxLines(0)` ignored (`scrollback.go:86`).
5. **Terminal replies.** A program asking where it is must be answered from the authoritative
   state. Today xterm answers from its own
   (`frontend/src/terminal-content.ts:4011`) while the backend's replies are read and dropped
   (`internal/panegrid/panegrid.go:195`). Which component transmits is free; what it says
   must derive from the one emulator.
6. **Resize.** The client resizes immediately (`frontend/src/renderers/xterm.ts:633`) and
   tells the backend after an 80 ms debounce (`frontend/src/terminal-content.ts:3395`). With
   the backend authoritative the order inverts: the client reports, the backend decides, the
   frame follows. The transitional behaviour must be stated.
7. **Recovery.** A helper session can outlive the coordinator and report an output-window
   hole (`internal/transport/ws_readopt.go:100`). An emulator fed only the surviving suffix
   is not authoritative; the honest answer for the write gate is refusal while completeness
   is unknown.
8. **The alternate screen.** A full-screen program's grid is the same emulator's other
   buffer; what the client is sent while it is active, and what it is sent on exit, is owed.
9. **Selection across the boundary.** Live selection reads xterm's buffer today
   (`frontend/src/renderers/xterm.ts:976` → `frontend/src/terminal-content.ts:3335`) and a
   drag started in live output continues through document-level listeners
   (`SelectionService.ts:489`). With the live region a painted frame, what a selection
   crossing into a card returns must be defined.

## 7. Measurements, before anything is built

1. **Throughput and latency of frame diffs against raw bytes** on the hero case — a fast
   build log — at the frame rate we would ship. This decides §4.1 and it is the one that can
   overturn the approach.
2. **Backend memory and CPU per session** with an emulator, at the tab counts actually in
   use, against today's copy-only path.
3. **First-frame size and latency** for an attaching client.
4. **Client cost** of applying diffs against parsing raw output.

## 8. Order of work, and what `nocx-6q1uh` does meanwhile

The owner's choice, taken in the same conversation: `nocx-6q1uh` is built on **the part of
this that comes first anyway**, and not on the old model, and does not wait for the rest.

1. This document reviewed; the new ADR written and accepted.
2. The measurements of §7.
3. **The backend owns the screen** — one emulator per session, replacing the
   enrolment-scoped grid. Grid lifetime separates from observation authority in the same
   change: `paneEnroller.Enrol` (`internal/app/paneenrol.go:102`) creates the grid and
   rejects an existing one, so pre-creating without that separation breaks enrolment and the
   waves that depend on it.
4. **The backend owns block boundaries**, with `ADR-0024` decision 1's rendezvous rule
   intact.
5. **`nocx-6q1uh` is designed and built on (3) and (4).** It needs one authoritative answer
   to "what is on this pane's screen" and an authorisation rule; it needs neither the
   encoder nor the card wire format. This is the step that unblocks the herdr replacement.
6. The encoder and the live region as diffs (§6.1, §6.2).
7. Block content on the wire; the frontend lifecycle machinery deleted (§4.2).
8. Terminal replies, resize inversion, recovery, alt-screen, selection (§6.5–§6.9).

`nocx-eidfb` is closed by (6), not before: attach today displaces the previous subscriber
(`internal/transport/ws_session_handlers.go:822`), and `.4`, `.3` and `.5` remain its
prerequisites.

## 9. Deliberately out

- **Replacing xterm.js.** It keeps painting, and it keeps receiving bytes — ANSI produced by
  our encoder instead of raw PTY output. Its parser stops being the VT authority; nothing
  else about it changes.
- **Retiring the replay ring** while raw bytes are still the live path.
- **What a write may DO.** `ADR-0064`'s bounds are untouched.

## 10. Beads this design produces

- **The `savedY` hazard.** xterm's `clear()` moves the live row and resets `ybase`/`y`
  without adjusting `savedY`, so a later `restoreCursor()` lands on a blank row while both
  freeze guards passed. Live today, bounded to normal-buffer state retained across a freeze.
  This design removes the cause; the bead exists because the defect does not wait for it.
- **Terminal-reply ownership** (§6.5).
- **The frozen output artifact off the renderer** (§4.2) — the concrete first slice of the
  block-content move.
