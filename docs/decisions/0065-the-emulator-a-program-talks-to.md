# ADR-0065 — The emulator a program talks to is not the emulator that watches it

- **Status:** Proposed
- **Date:** 2026-09-12
- **Supersedes:** [ADR-0041](0041-x-vt-as-the-backend-emulator.md) **in its choice only.**
  ADR-0041's method, its corpus description and its explicit statement of what it did and
  did not measure stand as evidence and are cited below rather than replaced.
- **Related:** the session-runtime design
  `.internal/specs/2026-09-12-the-backend-holds-the-session-screen-design.md`;
  [ADR-0001](0001-xterm-js-as-vt-frontend.md) (xterm.js is the VT frontend);
  [ADR-0009](0009-dom-scrollback-with-explicit-cell-geometry.md);
  `AD-6` and its amendment of 2026-08-25.
- **Beads:** `nocx-cu50k` (the spike), `nocx-kkn89` (the design).
- **Evidence:** `.internal/spikes/emulator/REPORT.md`, merged at `d3fcd45f`, with its
  corpus, drivers and raw results beside it.

## Context

ADR-0041 chose `charmbracelet/x/vt` for a job it named precisely: a **private, second
reading** of an enrolled pane, for observation only, granted exactly two powers by AD-6's
amendment — whether nocx may write into the pane, and what its activity indicator shows.
It chose on one criterion, column geometry against xterm.js, and it wrote down what it had
not measured.

The session-runtime design gives the emulator a different job. It becomes the terminal the
program **converses with**: the screen every client paints, the evidence an automated write
is judged against, the encoder of every keystroke, and the answer to every question the
program asks about its own terminal. That is not a larger version of the observer's job. An
observer that is wrong shows a wrong dot; an authority that is wrong types into the wrong
dialog, tells a program it is on a row it is not, and hands a second machine a screen the
first one never had.

**So the choice has to be made again, against the new job.** This record does not say
ADR-0041 was wrong. It says the criterion it was right about no longer decides.

## What was measured

`nocx-cu50k` ran nine behavioural probes and a geometry corpus against `x/vt`
(`96af6d2cb5f6`, the pinned version) and `libghostty-vt`
(`ghostty-org/ghostty@e2e53f86`), with headless `@xterm/headless@5.5.0` plus
`addon-unicode11` — the product's own frontend per ADR-0001 — as the reference.

**The nine probes: `x/vt` fails all nine; `libghostty-vt` fails one.** Four of `x/vt`'s
failures are silent wrongness rather than an absent feature:

- a modified arrow (`Ctrl-Left`, `Shift-Up`) or a modified function key emits **nothing**;
- `F13` emits U+FFFD, because unhandled codes fall through `string(key.Code)` and
  ultraviolet's codes sit above Unicode's maximum;
- ANSI insert mode is reported set by both a callback and a DECRQM reply, and then ignored:
  printing always overwrites;
- a cursor-position report under origin mode with a scrolling region answers the absolute
  row, while addressing respects the origin.

The other five: a grapheme cluster split across two `Write` calls becomes different cells;
a combining mark does not join an ASCII base; `CSI 1000000000 b` costs ~180 s and ~10⁹
allocations, which is a remote program's denial of service against the runtime; a height
reduction discards the cursor's row and keeps the rows above it; and an OSC title carrying
U+2733 is terminated by its own `9C` byte.

`libghostty-vt`'s single failure is that it clamps `REP` at 65535 — a bound, not a defect,
and the answer to a question `x/vt` has not asked.

**The geometry corpus: the criterion reproduces as a property, not as a discriminator.**
ADR-0041's captures are gone — `spike/vt-agreement/.gitignore` at `d3872462` lists
`frames/`, and they were never committed — so a fresh corpus of the same shapes was
recorded through the product's own `cmd/agent-capture`, plus the emoji case ADR-0041
records as untested.

| capture                                     | `x/vt`                 | `libghostty-vt`   |
| ------------------------------------------- | ---------------------- | ----------------- |
| `bash`, `htop`, `vim`, `less`, `wide` (CJK) | 4800/4800 columns      | 4800/4800 columns |
| `wizard`                                    | 4790/4800              | **4800/4800**     |
| `emoji`                                     | 84/100 content columns | **97/100**        |

The CJK line — five kana, each a two-column cluster with its continuation — is placed
identically by all three. `x/vt`'s ten columns on `wizard` are the `0x9C` defect above, for
which the product already ships `internal/panegrid/c1filter.go` and we have an upstream PR
(`charmbracelet/x#976`).

**Chunked replay.** `libghostty-vt`'s final screen is invariant to where the writes fall,
down to one byte per write, on all seven captures. `x/vt`'s is not, on `emoji`. Neither is
**xterm.js's**, which loses a character in two captures when fed a byte at a time — so
byte-boundary sensitivity is not a property the product could have inherited by choosing
its own frontend; it is the reference's own limit.

## Decision

**`libghostty-vt` is the emulator the session runtime is built on.** `x/vt` keeps the
observer role it holds today until the runtime replaces that path, and is then removed.

Three reasons, in order of weight:

1. **Grapheme assembly across write boundaries is a foundation property, and `x/vt` does
   not have it.** A screen that depends on where the kernel split a program's output is not
   an authority: the same bytes must produce the same screen, and they do not. This is not
   tunable from outside the emulator — it is where `handlePrint` and `flushGrapheme` are.
2. **The criterion that chose `x/vt` no longer separates them.** On every shape ADR-0041
   measured, both are exactly xterm.js. On the shape it did not measure, `libghostty-vt` is
   closer. There is no capture on which `x/vt`'s columns are the better ones.
3. **Input is the runtime's job now, and `libghostty-vt` has it.** Its key encoder carries
   application cursor keys, application keypad, `modifyOtherKeys`, the Kitty keyboard
   protocol and their flags as configuration driven from terminal state. `x/vt`'s does not
   encode a modified arrow at all, and the list of options ghostty's encoder exposes is the
   size of the work that would be ours.

## What this costs, and it is not small

- **A CGo binding and a Zig build.** The spike's provisional binding was 699 lines and about
  two hours on one platform, with a C shim the ABI forces and one crash diagnosed. A
  production binding adds object lifetime and ownership, API-version churn, and
  cross-compilation for every platform nocx ships — none of which the spike measured.
- **An unstable upstream.** `libghostty-vt`'s own header says _incomplete, work in progress,
  unstable_, and it ships an ABI manifest because of it. This is a dependency we will have
  to track deliberately rather than pin and forget.
- **No sixel and no kitty graphics in `x/vt` either**, so nothing is lost there;
  `libghostty-vt` decodes Kitty images, which is more than we have.

**The cost of not deciding this way** is the list ADR-0041 never signed up for: nocx owning
Unicode cluster assembly with ZWJ, flags, skin tones and VS16; the whole of the input
protocols; mode semantics, where "recognised, reported, not honoured" is a pattern the
spike caught in the cheapest available place; a resize policy that through reflow depends
on the Unicode work; and resource bounds against a hostile program. Five subsystems of
terminal emulator, maintained by us.

## What this decision does NOT rest on

Recorded so a later reader can see the edges of the evidence:

- **Not a re-run of ADR-0041.** The bytes are different and the originals are gone. A fresh
  100/100 on the same shapes is evidence about this corpus.
- **One emoji capture of ten lines** is what separates the candidates on geometry. A sample,
  not a distribution.
- **Colour, attributes, selection, alternate screen as a state, graphics and performance**
  are not in the corpus.
- **Cross-compilation and the production binding are unmeasured**, and they are the largest
  remaining unknown.

If any of those turns out badly, this record is superseded by a new one, not edited.
