# The backend holds the session's screen

- **Date:** 2026-09-12 (second revision — the first was reviewed by codex and is superseded
  by this text; what changed and why is §3)
- **Status:** draft for review
- **Owner's decision:** taken in conversation on 2026-09-12. The second revision's central
  move — the renderer stops destroying the grid — is the owner's, not this document's.
- **Binding documents this crosses:** `AD-1`, `AD-6`, `AD-9`, `AD-10`,
  [ADR-0001](../../docs/decisions/0001-xterm-js-as-vt-frontend.md),
  [ADR-0002](../../docs/decisions/0002-native-tabs-no-embedded-multiplexer.md),
  [ADR-0008](../../docs/decisions/0008-command-blocks-as-a-keyboard-first-ledger.md),
  [ADR-0009](../../docs/decisions/0009-dom-scrollback-with-explicit-cell-geometry.md),
  [ADR-0024](../../docs/decisions/0024-authenticated-shell-integration-channel.md),
  [ADR-0041](../../docs/decisions/0041-x-vt-as-the-backend-emulator.md),
  [ADR-0064](../../docs/decisions/0064-a-pane-that-is-read-may-be-answered.md).
- **Beads:** `nocx-kkn89` (this design), epic `nocx-eidfb`, epic `nocx-6q1uh`, `nocx-dkawo`.

## 1. Why this document exists

`nocx-6q1uh` asks for one method usable by an external coordinator and by nocx's own
assistant: send keys to a pane. A write into a pane must be judged against what is on that
pane's screen, and the two callers had different answers to "who holds the screen" — a
worker pane is enrolled and has a backend grid, a person's own pane has none.

Following that fork produced a second finding: it is the same missing piece that stops a
second machine attaching to a running session. Both are "who holds the current screen".

Following it once more — and this is the second revision's subject — produced a third: the
two candidate holders disagree only because of one operation nocx performs on one of them,
and that operation was a workaround for something else.

## 2. What is decided today, and where it stops

**`ADR-0001` / `AD-6`:** xterm.js owns the VT state; the backend does not sniff the stream.

**`ADR-0041` / the AD-6 amendment of 2026-08-25:** the backend keeps a real VT grid
(`charmbracelet/x/vt`) for an **explicitly enrolled** pane, with exactly two powers.

**`nocx-eidfb` (owner's, 2026-08-24):** only the session's SIZE moves to the backend, on the
reasoning that "two clients then see the same bytes at the same size, they see the same
screen WITHOUT a server-side grid".

### 2.1 Where the byte-stream argument stops — corrected

The first revision claimed a late client is limited to the 256 KB replay ring
(`internal/transport/ring.go:12`) and gets a cleared screen past it. That was wrong:
`reclaimSession` (`frontend/src/ipc.ts:1179`) reads recorded output before joining the ring,
so a late client is not categorically limited to ten screens.

What survives the correction, and is the actual motivation: byte replay is not a
**guaranteed** screen reconstruction. Retention policy can legitimately have kept nothing,
historical geometry is not recorded alongside the bytes, and replaying an arbitrary prefix
costs time proportional to the session's whole life. A screen the backend already holds
costs one snapshot.

### 2.2 What the renderer actually owns that it should not — corrected

The first revision claimed a session with no renderer records no history. That was wrong.
`lifecycle.submitAttempt` opens the durable row, `PublishLifecycle` synchronises the ledger
before delivery, and `syncLifecycleLedger` (`internal/transport/ws_lifecycle.go:342`)
completes it from authenticated facts with `TermTransportGone` when the transport is gone;
raw output is recorded backend-side in `ws_session_record.go`.

The true, narrower statement: without a renderer a completed command's row is closed but
carries **no frozen output artifact**, and its status can be `EntryUnknown`. Moving that
capture is substantial separate work and is **not** a free consequence of this design.

## 3. What changed in this revision, and the finding that changed it

The first revision argued the backend grid and xterm need not agree because they answer
different questions. Codex refuted the argument's load-bearing half and the refutation
holds: **the renderer performs a VT mutation the backend never sees, and it is not
reproducible from the stream.**

At every freeze the renderer calls xterm's `clear()`, which does more than drop archived
rows (`@xterm/xterm` `Terminal.ts:1224`):

```js
this.buffer.lines.set(0, this.buffer.lines.get(this.buffer.ybase + this.buffer.y)!)
this.buffer.lines.length = 1
this.buffer.ydisp = 0; this.buffer.ybase = 0; this.buffer.y = 0
```

The **live** row — the one nothing has frozen — moves to row 0 and the screen's origin moves
with it. Absolute cursor addressing after that point lands on different cells in the two
models, and `savedY` is not adjusted, so a later `restoreCursor()` lands on a now-blank row.
Both of the freeze guards can have passed.

Worse for the first revision's remedy: the clear is **not** a function of the byte stream.
`_settleFrozen` is driven by authenticated lifecycle facts arriving out of band, by a
renderer-local running block that `beginBlockNow` sets at submit before the running fact
arrives, and by a 500 ms deferred-fence timer. A completion waiting on its fence and a
person submitting before that timer fires produce a different presentation from identical
PTY bytes. No backend model can reproduce it from what it sees.

### 3.1 The cut was a workaround, not a design

`nocx-m87n`, the bug `clearViewport` was restored for, records its own root cause:

> "the live region shows the WHOLE terminal viewport, not the rows of the running command.
> `liveContentHeight()` measures from viewport row 0 to the last non-empty row, and
> `setLiveHeight` sizes the live box to that. **Nothing offsets the region to the running
> block's `startLine`.**"

So the live region has no start. Destroying the rows above it was how row 0 was made to be
the right place. **Give the live region its offset and the destruction is unnecessary**, and
with it goes the only structural reason the two models differ.

This is the owner's move, and it inverts the design: the expensive half of the first
revision — teaching the backend to reproduce a presentation — disappears, because there is
nothing to reproduce.

## 4. The decision, in two halves

**(a) The renderer stops destroying the grid.** A finished command's rows are serialised into
their DOM block as they are today, and then **left in xterm's buffer**. The live region is
given an offset — the running block's start — instead of relying on row 0. Nothing about the
product's appearance changes: the scrollback is still DOM, xterm is still only the VT engine
plus the alt-screen surface, and the frozen block still owns its own presentation
(`ADR-0009` stands).

**(b) The backend maintains an authoritative VT grid for every session,** as it already does
for an enrolled pane, and owns the block boundaries it parses from OSC 133 beside the
lifecycle facts it already holds. A client that attaches receives a repaint synthesised from
that grid plus those boundaries, and then the ordinary byte stream.

(a) is what makes (b) cheap: with no destructive mutation on either side, the two models
share one coordinate system by construction, and the repaint needs no translation.

### 4.1 Why this is not the layout nocx moved away from

The direction change of 2026-07-24 replaced _DOM overlays on a full-screen xterm_ with _DOM
scrollback plus xterm as the VT engine_. This design keeps that: the blocks are DOM, not
overlays, and xterm is not asked to present them. The only change is that xterm's buffer is
not truncated behind them.

## 5. The new invariant

A **new ADR**, not an edit to any existing record — the old records stay as evidence of what
was decided when (AGENTS.md, "An accepted ADR is never edited").

> **The renderer owns what the person sees. The backend owns what the session is. Neither
> destroys what the other reads.**

| Question                                              | Owner                                   | Consumer                                           |
| ----------------------------------------------------- | --------------------------------------- | -------------------------------------------------- |
| what this client displays, and what is selected in it | xterm.js + DOM                          | the person                                         |
| what the session's screen is right now                | backend grid                            | an attaching client; a write gate; an agent driver |
| where each block starts and ends                      | the backend (OSC 133 + lifecycle facts) | every client's live-region offset; the repaint     |
| a finished block's durable body                       | the ledger (`internal/content`)         | history, search, a new client above the fold       |
| the session's size                                    | the backend (`nocx-eidfb.1`)            | every attached client                              |

## 6. Equivalence: what must match, and what need not

The first revision's blanket claim ("different questions, so no agreement needed") is
withdrawn. The requirements are separate and each is scoped:

1. **The write gate.** Equivalence is required for **every displayed target of automation** —
   not only for the assistant in a person's own pane. A delegated worker gets an ordinary tab
   (`frontend/src/panes.ts:959`) and viewing it does not suspend the write authority, so a
   person can be watching the exact pane a coordinator is answering. Revalidation must cover
   the action's facts and the key derivation, not merely that both readings classify as a
   menu.
2. **Attach and continuation.** The repaint plus the subsequent stream must land a new client
   in the state the session is in. §7.
3. **Freeze boundaries.** Every client must place the same block boundaries, which is why (b)
   puts them in the backend rather than deriving them per client.
4. **Selection and search.** These are DOM operations over the blocks
   (`frontend/src/scrollback/blocks.ts:1532`); no `SearchAddon` is installed. The buffer's
   copy of already-carded rows is therefore never read, and its post-resize divergence from
   the card is invisible — but the rule must be written down, and hit-testing must not reach
   into the clipped region.
5. **Resize.** x/vt truncates and pads; xterm reflows the normal buffer. The rule for what a
   client sees after a resize, and what a repaint after a resize carries, is owed.

## 7. The attach repaint

`x/vt` can serialise: `Emulator.Render()` returns the screen with styles and links as ANSI
sequences (`emulator.go:140`). It is **not sufficient on its own**: it renders only the
active buffer, resets style and hyperlink at line ends, omits trailing spaces, separates rows
with LF, and emits neither an erase nor a carriage return
(`ultraviolet/buffer.go:141,228,271`).

### 7.1 The state a repaint must carry

Cells alone give a correct-looking screen and a broken keyboard. Enumerated, each with an
obvious failing test:

- cursor position, visibility and shape; the **saved** cursor.
- current rendition and any open hyperlink — distinct from the styles already on cells.
- scrolling margins, origin mode, insert mode, autowrap, and **pending wrap** (a cursor in
  the last column is not enough to know whether the next character wraps).
- tab stops; character-set designation and selection.
- modes that change what a keystroke means: bracketed paste, DECCKM, application keypad,
  mouse reporting (1000/1002/1003/1006), focus reporting.
- which buffer is active, and the inactive buffer's contents — a client attaching inside
  `vim` must not inherit alternate-screen content as scrollback when the program exits.
- the dynamic palette and default colours.
- the session's size, which the backend already owns.

Any of these the repaint deliberately drops must be named, with what breaks.

### 7.2 Parser state: checkpoint at a safe boundary, and a bounded failure

A repaint taken mid-sequence cannot be continued. Serialising partial parser state is not
required; **checkpointing at a ground-state boundary is**, with these properties:

- the boundary is detected **during parsing**, not at read-chunk ends — a chunk can cross
  ground and finish inside another sequence.
- the wait is bounded in time and memory. A program can hold the parser inside an
  unterminated OSC or DCS indefinitely (`x/ansi` `parser/transition_table.go:211,259`), so
  "wait for ground" has no guaranteed end.
- on exhaustion the attach returns a named **"snapshot unavailable: incomplete sequence"**
  rather than a wrong continuation. An older complete checkpoint plus its fully retained
  suffix is an acceptable alternative where one exists.
- x/vt keeps its parser private (`emulator.go:47`), so a supported checkpoint accessor is
  needed, taken after the emulator has applied the completed input.

### 7.3 Delivery: its own message type, and one publication point

The repaint is **not** PTY output. It travels as its **own message type** on `AD-1`'s
existing binary data plane (`version || msg-type || session-id || payload`): it is not
written to the replay ring, it does not advance the client's PTY offset
(`frontend/src/ipc.ts:547` counts every binary payload), and it names the offset, geometry
and session incarnation the continuation resumes from.

The remaining race is not accounting but **ordering**: `pumpToRing`
(`internal/transport/ws.go:3284`) updates the grid before appending to the ring, so a
snapshot can be taken at offset `N+k` and labelled with frontier `N`, and the continuation
replays `k` bytes into a picture that already has them. Grid and frontier must be published
under one mechanism, geometry changes included. One trap: `ring.write()` can wait for
capacity (`ring.go:182`), and that wait must not sit inside a lock an attach needs to
establish the consumer that would drain it.

This is solved **with** the repaint, not before the grid-for-every-session step.

## 8. Terminal replies

A program can ask the terminal about itself, and the reply is written back into the PTY.
Today xterm answers — `renderer.onData(...) → session.send(...)`
(`frontend/src/terminal-content.ts:4011`) — from **its own** coordinates, while the backend
grid's replies are read and dropped on purpose (`internal/panegrid/panegrid.go:195`).

So the program is already living in the renderer's numbering. Declaring the backend
authoritative while xterm goes on answering independently gives the program two
interlocutors with different ideas of where it is.

**Requirement: a reply must derive from the authoritative state.** Which component transmits
it is free — xterm may relay a backend-computed answer. What is forbidden is two components
answering the same state-dependent question from different coordinates.

Note that decision (a) removes the _cause_ of the divergence rather than papering over it:
with no `clear()`, the renderer's coordinates and the backend's are the same, and this
requirement becomes cheap to satisfy. It is written down anyway, because it is what makes
the coordinate question decidable at all.

## 9. Above the fold

The live region comes from the grid, offset to the running block's start. Everything above
comes from the ledger's blocks.

- **The hole:** a long **unfinished** command whose first rows have scrolled above the live
  screen. They are neither a finished ledger block nor a live cell. The design owes a
  representation for them.
- **The overlap:** with (a) in place, the renderer no longer removes carded rows from the
  buffer, so a repaint that carries them and a ledger that also carries them would draw the
  same output twice. The boundary is the running block's start, from the backend, and it is
  the same boundary the live region's offset uses.
- **x/vt's own scrollback** defaults to 10,000 lines and `SetMaxLines(0)` is ignored
  (`scrollback.go:86`), so "bound it to nothing" is not available. Bound it deliberately and
  say what it is for; the ledger remains the only **durable** block store.

## 10. Fidelity, which replaces the agreement requirement

The backend grid is the authority for the repaint and the write gate, so what matters is
whether it models a real terminal correctly. The oracle is `ADR-0041`'s: the same bytes
through `x/vt` and through headless xterm.js, compared per cell. The spike is in history at
`d3872462`.

**The comparison must be prefix → attach → suffix, not whole-capture.** Matching the picture
at the attach instant does not prove correct continuation. Attach inside sequences, across a
buffer switch, and across a resize.

## 11. Measurements to take FIRST

1. **Client memory without the cut.** Today `clear()` truncates xterm's buffer at every
   freeze; without it the buffer grows to `scrollback: 10000`
   (`frontend/src/renderers/xterm.ts:364`) at 3 words per cell
   (`@xterm/xterm` `BufferLine.ts:22`). Measure at the tab counts actually in use — and note
   that the cards now own the scrollback, so the xterm limit can be **reduced** rather than
   kept. The coordinate fix does not depend on the limit: VT absolute addressing is within
   the screen, not the scrollback.
2. **Backend memory per session with a live grid**, bounded and unbounded scrollback, against
   the same session with no grid.
3. **CPU on a fast producer** through the grid, against the same bytes with no grid.
4. **Repaint size and latency** for a full screen.

If these are small, they become a paragraph of numbers in the ADR. If they are not, the
design changes before it is built.

## 12. What the new ADR supersedes, named

- `AD-6`'s rule sentence, "the backend does **not** sniff the byte stream", and its refusal
  list (`docs/architecture.md:160`), which forbids displaying grid-derived content or
  persisting it as history — repaint and backend capture change exactly those permissions.
  The _powers_ half of the 2026-08-25 amendment stays as written: it bounds what the grid may
  DECIDE, and nothing here loosens that.
- `ADR-0001`'s consequences, which promise frontend-only OSC parsing.
- `ADR-0008`'s consequences, which promise a byte-blind backend.
- `ADR-0002`'s conclusion where it closed server-side terminal state; its own revisit trigger
  is already recorded as fired.
- `AD-9` and the `session.output` contract, including its explicit no-grid/same-size
  reasoning.
- The `ledger.capture` contract's description of a renderer serializer, if and when capture
  moves.
- The reasoning quoted in §2 from `nocx-eidfb`'s body — amended by note, not rewritten.

**Not superseded, and must not be absorbed:**

- The AD-6 bootstrap-window carve-out. Permission to parse VT is not permission to interpret
  bootstrap readiness tokens or to write bootstrap frames outside their interval; those are
  separate powers held by `ADR-0024` and the input quarantine.
- `ADR-0024` decision 1: no stream-derived lifecycle or history **authority**. §14's move of
  OSC 133 parsing to the backend must not reinstate it — the backend may derive block
  BOUNDARIES; it may not derive an attempt's outcome from the stream.
- `internal/notify`'s trust classes. Backend execution does not turn an inference into an
  authenticated fact.
- `ADR-0064`'s permission boundary. This design gives both callers one source of EVIDENCE; it
  authorises nobody to read a pane they could not read before, and a person's own pane stays
  unreadable to a coordinator.

## 13. Recovery: a grid that missed output

A helper session can outlive the coordinator; on re-adoption the backend resumes from
recorded progress and the helper can report an output-window hole
(`internal/transport/ws_readopt.go:100`), while the grid lived in coordinator memory.

A grid fed only the surviving suffix is not authoritative, and one that missed a midstream
interval is not either. The design owes: where recoverable terminal state lives, how
completeness is established, and what attach and write do while completeness is unknown —
the honest answer for the write gate being refusal.

## 14. Order of work

1. This document reviewed; the new ADR written and accepted.
2. Measurements of §11. They gate the rest.
3. **Decision (a):** the live region gets its offset; `clearViewport` goes. `nocx-m87n`'s
   regression test must stay green by the offset rather than by the cut.
4. The grid for every session, replacing the enrolment-scoped one. Grid lifetime must be
   separated from observation authority in the same change: `paneEnroller.Enrol`
   (`internal/app/paneenrol.go:102`) creates the grid and rejects an existing one, so
   pre-creating grids without that separation breaks enrolment and the waves that depend on
   it.
5. Block boundaries owned by the backend, and the live-region offset fed from them.
6. The repaint (§7), with the publication and ordering of §7.3.
7. Terminal replies derived from authoritative state (§8).
8. The frozen output artifact moved off the renderer (§2.2) — separate, substantial, not a
   prerequisite for the above.
9. `nocx-6q1uh` designed on top. Its dependency is the **evidence contract and the
   authorisation**, not the whole migration; it can be designed once (a), (4) and (5) are
   settled.

Note for `nocx-eidfb`: the repaint does not close it alone. Attach currently displaces the
previous subscriber (`internal/transport/ws_session_handlers.go:822`), and `nocx-eidfb.4`
(controller/observer), `.3` (session-size delivery to an observer) and `.5` remain
prerequisites for its literal acceptance criterion.

## 15. Deliberately out

- **Rendering cells to the client** (the herdr/Ghostty model). The client keeps receiving
  bytes and parsing them with xterm.js.
- **Retiring the replay ring.** It is the fast path for a brief disconnect.
- **Anything about what a write may DO.** `ADR-0064`'s bounds are untouched.

## 16. Beads this design produces

- **The `savedY` hazard.** xterm's `clear()` moves the live row and resets `ybase`/`y` without
  adjusting `savedY`, so a later `restoreCursor()` lands on a blank row while both freeze
  guards passed. Bounded to **normal-buffer state retained across a freeze** — background
  writers, asynchronous prompt updates, nested-environment transitions. NOT "every
  non-alt-screen progress renderer"; incidence is for the bead to measure. Decision (a)
  removes the cause, so this bead may close as a consequence — it is filed separately because
  it is live today.
- **Terminal-reply ownership** (§8).
- **The frozen output artifact off the renderer** (§2.2).
