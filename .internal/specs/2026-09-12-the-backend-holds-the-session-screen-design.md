# The backend holds the session's screen

- **Date:** 2026-09-12
- **Status:** draft for review
- **Owner's decision:** taken in conversation on 2026-09-12; this document records it and
  works out what it costs.
- **Binding documents this crosses:** `AD-1`, `AD-6`, `AD-9`, `AD-10`,
  [ADR-0001](../../docs/decisions/0001-xterm-js-as-vt-frontend.md),
  [ADR-0002](../../docs/decisions/0002-native-tabs-no-embedded-multiplexer.md),
  [ADR-0009](../../docs/decisions/0009-dom-scrollback-with-explicit-cell-geometry.md),
  [ADR-0041](../../docs/decisions/0041-x-vt-as-the-backend-emulator.md),
  [ADR-0064](../../docs/decisions/0064-a-pane-that-is-read-may-be-answered.md).
- **Beads:** epic `nocx-eidfb` (two machines, one session), epic `nocx-6q1uh` (the session
  surface that waits on this), `nocx-dkawo` (the wave that needs a session outliving its
  window).

## 1. Why this document exists

`nocx-6q1uh` asks for one method — `session.keys` — usable by an external coordinator and
by nocx's own assistant. Designing it ran into a fork that looked like a detail and is not:
**a write into a pane must be judged against what is on that pane's screen, and the two
callers had two different answers to "who holds the screen".** A coordinator's worker pane
has a backend grid, because it is enrolled (AD-6's amendment of 2026-08-25). A person's own
pane has no backend grid at all, so the assistant's evidence would have to come from the
renderer — which is not always attached.

Following that fork back produced a second finding: it is the same missing piece that stops
a second machine attaching to a running session. Both are "who holds the current screen",
and the answer this document records is **the backend does**.

## 2. What is decided today, and where it stops

**`ADR-0001` / `AD-6`:** xterm.js owns the VT state. The backend does not sniff the byte
stream.

**`ADR-0041` / the AD-6 amendment of 2026-08-25:** the backend keeps a real VT grid
(`charmbracelet/x/vt`) for a pane that has been **explicitly enrolled**, for the life of
that enrolment, with exactly two powers — whether nocx may write into the pane, and what
the activity indicator shows. It says in as many words that nothing derived from that grid
is shown to the user as their terminal.

**`nocx-eidfb`, the two-machines epic (owner's, 2026-08-24):** only the session's SIZE moves
to the backend. Its body states the reasoning that this document reverses:

> "And because two clients then see the same bytes at the same size, they see the same
> screen WITHOUT a server-side grid — which is what keeps ADR-0002 intact."

### 2.1 Where that reasoning stops, measured

The sentence holds for a client that has been attached from the beginning. It does not hold
for one that attaches later, and the bound is in the tree:

- `internal/transport/ring.go:12` — `RingCapacity = 256 * 1024`, documented as "~10 screens
  of 132×43 terminal output".
- `AD-9` — on reconnect the backend replays from the client's last offset, "or emits an
  explicit `reset` (clear + resync) if the offset is past the buffer".

So attaching a second machine to a session that has been running for more than about ten
screens of output yields a cleared screen, not the session's screen. "Two machines, one
session, the same screen" is not reachable from the byte stream alone.

### 2.2 A second thing the renderer owns that it should not

`history.record` — the record of a finished command block — originates in the renderer
(`frontend/src/scrollback/blocks.ts`). The renderer parses OSC 133, derives the block, and
sends the fact back to the backend, because the backend refuses to parse. **A session
running with no window attached therefore records no history at all.** That hole is closed
by the same change, without separate work.

## 3. The decision

**The backend maintains an authoritative VT grid for every session. The renderer keeps
rendering, and keeps owning what the user sees and interacts with.**

Concretely:

1. The backend parses each session's output into a grid, as it already does for an enrolled
   pane, and does so for every session rather than only for enrolled ones.
2. A client that attaches receives a **repaint** synthesised from that grid, then the
   ordinary byte stream from that point. This is what tmux does for a reattaching client.
3. The data plane does not change. `AD-1`'s raw binary frames stay raw binary frames; the
   repaint is bytes like any other bytes.
4. xterm.js is not replaced and not bypassed. It receives the repaint and the stream through
   the same path it uses today.
5. The DOM keeps everything it owns today: frozen blocks (`ADR-0009`), selection, search,
   links, the command editor.

### 3.1 The two grids answer different questions, and that is why they need not agree

The obvious objection is that two VT implementations will drift, and that drift is what
`AD-6` exists to prevent. It does not apply here, because the two are not two owners of one
question:

- **xterm.js owns "what this client is displaying."** Its consumer is the person looking at
  it, and everything built on it — selection, blocks, the editor.
- **The backend grid owns "what this session's screen is"** for anybody who was not
  watching: a client attaching now, and a caller about to write into the pane.

They meet at exactly one point, the attach repaint, and there the backend's answer does not
have to agree with anything — it _becomes_ the new client's starting state. From then on both
clients consume the same stream.

For the write gate the renderer's opinion is not merely unnecessary, it is irrelevant: the
truth is the state of the real application, and the backend grid is what models it. If that
model is wrong the gate is wrong, and xterm.js agreeing with the wrong answer would not have
saved it.

**What this changes is therefore not a synchronisation requirement but a fidelity
requirement**, stated in §7.

## 4. The new invariant

To be written as a new ADR rather than as an edit to any existing record — the old records
stay as they are, because they are evidence of what was decided when, and the new one says
what changed and why (AGENTS.md, "An accepted ADR is never edited").

> **The renderer owns what the person sees. The backend owns what the session is.**

Under it:

| Question                                              | Owner                                                   | Consumer                                           |
| ----------------------------------------------------- | ------------------------------------------------------- | -------------------------------------------------- |
| what this client displays, and what is selected in it | xterm.js + DOM                                          | the person                                         |
| what the session's screen is right now                | backend grid                                            | an attaching client; a write gate; an agent driver |
| what the session's finished blocks were               | the ledger (`internal/content`), written by the backend | history, search, a new client above the fold       |
| the session's size                                    | the backend (`nocx-eidfb.1`)                            | every attached client                              |

`AD-6`'s single-owner rule survives intact under this reading: each question still has
exactly one owner. What changes is that "the backend does not sniff the byte stream" stops
being the mechanism that enforces it.

### 4.1 What the new record supersedes, named

- `AD-6`'s rule sentence, "The backend does **not** sniff the byte stream", and with it both
  carve-outs that exist only because of it: the bootstrap-window read of 2026-08-20 and the
  enrolled-pane grid of 2026-08-25. Neither needs to be a carve-out once the backend parses
  every session; both become ordinary consequences, and the _powers_ half of the 2026-08-25
  amendment stays exactly as written, because it bounds what the grid may DECIDE and that
  bound is not loosened here.
- `ADR-0002`'s conclusion in the part where it closed server-side terminal state. Its own
  revisit trigger — "a session that survives the client process entirely" — is already
  recorded as fired in the AD-6 amendment; this is the second half of that firing.
- The reasoning quoted in §2 from `nocx-eidfb`, which the epic body carries rather than an
  ADR. The epic is amended by note, not rewritten.

Left standing, explicitly: `ADR-0001` (xterm.js is the VT frontend — it still is, for
display), `ADR-0009` (the DOM owns frozen blocks), `AD-1` (two planes, raw binary data
plane), `AD-9` (the replay ring stays; it is now a fast path in front of the repaint rather
than the only path), `ADR-0064`'s bounds on what a write may be.

## 5. The attach repaint

`x/vt` can already serialise: `Emulator.Render()` returns "a snapshot of the terminal screen
as a string with styles and links encoded as ANSI escape codes"
(`emulator.go:140`, delegating to `ultraviolet.Buffer.Render`). So the grid does not have to
learn to serialise itself; it does that today.

**`Render()` is cells and styles, and nothing else.** Reading
`ultraviolet/buffer.go:271` it is `Lines(b.Lines).Render()` — no cursor, no modes. A repaint
that carries only that gives an attaching client a correct-looking screen and a broken
keyboard. The repaint must therefore carry, and the design of it must enumerate:

- **cursor** — position, visibility, shape.
- **modes that change what a keystroke means** — bracketed paste (2004), application cursor
  keys (DECCKM), application keypad, mouse reporting (1000/1002/1003/1006), focus reporting
  (1004).
- **which buffer is active** — a client attaching while `vim` runs must land on the alternate
  screen, and must not inherit alternate-screen content as scrollback when the program exits.
- **the session's size**, which the backend already owns (`nocx-eidfb.1`).

Each of these is a bounded, testable item, and each has an obvious failing case to write a
test from. Any mode the repaint does not carry must be named in the design as deliberately
dropped, with what breaks.

## 6. Above the fold: what a new client sees behind the live screen

The live screen comes from the grid. Everything above it comes from the **ledger**, which is
already backend-owned and which §2.2 makes complete for the first time.

The boundary has to be stated or it will be decided differently in each path:

- A client attaching mid-command gets the live region from the repaint, and the finished
  blocks above it from the ledger.
- `x/vt` has a scrollback of its own (`Emulator.Scrollback()`, `emulator.go:451`). It must be
  **bounded**, and the design must say whether it is used at all: the product's scrollback is
  the ledger's blocks, and a second scrollback in the backend is a second answer to the same
  question — the thing §3.1 is careful to avoid elsewhere.

Recommendation, to be argued in the design rather than assumed here: bound `x/vt`'s
scrollback to the smallest value the emulator needs to be correct, and let the ledger be the
only scrollback the product has.

## 7. Fidelity, which replaces the agreement requirement

The backend grid is now the authority for the repaint and for the write gate. What matters
is whether it models a real terminal correctly, and the cheapest oracle available is still
the one `ADR-0041` used: the same bytes through `x/vt` and through headless xterm.js,
compared per column. The spike that did it is in history at `d3872462`
(`render-xterm.mjs` against `render-go`, per-column diff plus chrome-anchor checks).

This is a **correctness test of `x/vt`**, not a synchronisation mechanism, and the difference
matters: a disagreement is a bug to be fixed or a documented divergence, never something the
runtime reconciles.

Concretely required before the epic closes: the corpus in
`internal/agentdriver/testdata/captures/` replayed through both, with a per-cell comparison
and a named, justified list of divergences.

## 8. Measurements to take FIRST

The owner's position is that there is no new per-session cost, because the same information
is already recorded, only from a different source. That is true of the ledger and of the
replay ring. It is not automatically true of the grid, which is new allocation per session.
So this is a number to produce, not an argument to win:

1. **Resident memory per session with a live grid**, at 132×43 and at a large window, with
   `x/vt` scrollback bounded as §6 recommends and unbounded, measured against the same
   session with no grid.
2. **The same at scale** — the tab counts actually in use, not a synthetic one.
3. **CPU on a fast producer** — a build log at full speed through the grid, against the same
   bytes with no grid, since today the backend only copies those bytes.
4. **Repaint size and latency** for a full screen, which is what an attaching client waits
   for.

If (1) and (3) are small, §8 is a paragraph in the ADR saying so, with the numbers. If either
is not small, the design changes before it is built, not after.

## 9. What this gives `nocx-6q1uh`

The fork that produced this document disappears. `session.read`, `session.keys` and
`session.message` have **one** source of evidence — the backend grid — for both callers, and
the "no renderer attached" case stops being a refusal path in the write tools. `session.read`
keeps its `Dynamic` execution row for items, because an exited item still comes from the
ledger; the SCREEN source becomes `InGo`.

`nocx-6q1uh` is therefore blocked by this work and should be re-sequenced behind it.

## 10. Order of work

1. This document reviewed, and the new ADR written and accepted.
2. Measurements of §8. They gate the rest.
3. The grid for every session, replacing the enrolment-scoped one, with the enrolment
   interval kept for what it still bounds — the powers, not the existence of the grid.
4. The repaint (§5), and `nocx-eidfb`'s acceptance criterion met for a client attaching to a
   long-running session.
5. OSC 133 and the block ledger moved to the backend (§2.2), and `history.record` retired
   from the renderer.
6. `nocx-6q1uh` designed on top.

## 11. Deliberately out

- **Rendering cells to the client** (the herdr/Ghostty model). The client keeps receiving
  bytes and keeps parsing them with xterm.js. Moving the paint half would cost the whole
  presentation layer — blocks, selection, search, links, the editor — for a property this
  design already delivers.
- **Retiring the replay ring.** It is the fast path for a brief disconnect and it is cheaper
  than a repaint.
- **Anything about what a write may DO.** `ADR-0064`'s bounds are untouched here; this
  document changes where the evidence comes from, never what the evidence permits.
