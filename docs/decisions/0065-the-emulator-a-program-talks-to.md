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
program asks about its own terminal. That is not a larger version of the observer's job.

**This is not the first time the emulator's reading has had a safety consequence, and
saying so would be a softening.** The observer already decided whether nocx may write into
a pane — that is power (1) above, and a wrong reading already meant a keystroke into a
screen nocx had misread. What the new job ADDS is input encoding, the program's replies,
and the screen every client paints. What it changes is the blast radius, not its existence.

**So the choice has to be made again, against the new job.** This record does not say
ADR-0041 was wrong: its evidence supported the deliberately limited comparison it made. It
does not certify x/vt as correct for every behaviour inside that role either — nobody
measured that. It says the criterion ADR-0041 was right about no longer decides.

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

**`libghostty-vt` is not clean, and the probe table's shape hid one of its results.** It
clamps `REP` at 65535 — but that probe is titled _bounded work_, so a clamp is the ANSWER
to the requirement rather than a failure of it, and counting it as a failure while calling
it acceptable blurs the two. Its real measured shortcoming is elsewhere and must be carried
as an obligation: **it does not answer `CSI 4 $p`**, the program's own DECRQM query
(`results/ghostty.jsonl:206`, an empty reply). The C-API mode query works, and the report
reasoned that "the C query is the path that matters" — which is wrong for this
architecture, because the program cannot call the C API. The public query probably makes
the missing reply cheap to add, but the work exists and is ours.

The REP figures are an **extrapolation**, not a completed run: the billion-repeat case was
stopped at a 30-second budget (192 922 025 repeats, 19.3%) and ~180 s / ~10⁹ allocations is
projected from the completed smaller cases. The denial-of-service conclusion stands; the
number is a projection and this record says so.

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

**Chunked replay.** Every capture was replayed split into 2, 3, 5, 8, 16 and 32 parts, and
the six captures under 64 KiB additionally one byte per write — `bash` at 76 923 bytes was
not replayed bytewise (`geometry.sh:63`). Under those partitions `libghostty-vt`'s final
screen is identical to its own whole-file screen every time; `x/vt`'s is not, on `emoji`;
and **xterm.js's is not either**, losing a character in two captures fed bytewise. So
byte-boundary sensitivity is not a property the product could have inherited by choosing
its own frontend — it is the reference's own limit.

**What "identical" means here, exactly:** the scorer compares normalised cell text and
width classes (`cmd/geom/main.go:555`). It does not compare cursor position, modes, saved
cursor, pending replies or continuation behaviour. So this establishes final-cell agreement
under the tested partitions, not equivalent terminal state after every partition. The
explicit grapheme probes (§ probes 1 and 2) are the stronger evidence for the invariance
argument; neither dataset is a universal proof.

## Decision

**`libghostty-vt` is the emulator the session runtime is built on.** `x/vt` keeps the
observer role it holds today until the runtime replaces that path, and is then removed.

Three reasons, in order of weight:

1. **Grapheme assembly across write boundaries is a foundation property, and `x/vt` does
   not have it.** A screen that depends on where the kernel split a program's output is not
   an authority: the same bytes must produce the same screen, and they do not. This is not
   tunable from outside the emulator — it is where `handlePrint` and `flushGrapheme` are.
2. **The criterion that chose `x/vt` no longer separates them.** Five captures tie exactly
   at 4 800 of 4 800 columns, CJK among them. `wizard` — one of the shapes ADR-0041
   measured — is **not** a tie: raw `x/vt` scores 4 790, and while the product ships a
   filter that removes that defect, **the filtered path was not re-measured** (`REPORT.md`
   §7.4). On the one shape ADR-0041 did not measure, `libghostty-vt` is the closer. There
   is no capture on which `x/vt`'s columns are the better ones — and that, rather than "both
   are exactly xterm.js", is what the table supports.
3. **Input is the runtime's job now, and `libghostty-vt` has it.** Its key encoder carries
   application cursor keys, application keypad, `modifyOtherKeys`, the Kitty keyboard
   protocol and their flags as configuration driven from terminal state; `x/vt`'s does not
   encode a modified arrow at all. **Measured against inspected, honestly:** the probes
   exercised legacy encoding and manually chosen Kitty flags, passing `nil` for the terminal
   (`cmd/ghosttyvt/main.go:207`). The `setopt_from_terminal` path exists in the binding
   (`ghostty/binding.go:399`) and was **not** exercised, so application-driven protocol
   negotiation — program enables a mode, the runtime derives the encoder from it, the key
   produces the expected bytes, the program disables it and the encoding changes back — is
   an API claim here, not a measurement. It is the first integration gate.

4. **Two measured advantages the summary would otherwise lose.** `libghostty-vt` stores
   wrap/continuation per line and exposes a working incremental render-state interface
   (`REPORT.md` §2.1-§2.2). Those are exactly what the design's capture and cell-publication
   halves need, and `x/vt` has neither — its damage types have no emission path and its
   stored lines carry no continuation flag. This is more concrete than counting encoder
   options.

## What this costs, and it is not small

- **A CGo binding and a Zig build.** The spike's provisional binding was 699 lines and about
  two hours on one platform, with a C shim the ABI forces and one crash diagnosed. A
  production binding adds object lifetime and ownership, API-version churn, and
  cross-compilation for every platform nocx ships — none of which the spike measured.
- **An unstable upstream.** `libghostty-vt`'s own header says _incomplete, work in progress,
  unstable_, and it ships an ABI manifest because of it. This is a dependency we will have
  to track deliberately rather than pin and forget.
- **Graphics, with one qualification.** Neither displays sixel, so nothing DISPLAYED is
  lost, and `libghostty-vt` decodes Kitty images, which is more than we have. But `x/vt`
  exposes a DCS/APC consumer seam while the measured ghostty interface ignores sixel without
  handing it back (`REPORT.md` §2.5) — so an extension hook IS lost, and a future sixel
  decision is upstream's or a patch's rather than ours.

**The cost of not deciding this way** is the list ADR-0041 never signed up for: nocx owning
Unicode cluster assembly with ZWJ, flags, skin tones and VS16; the whole of the input
protocols; mode semantics, where "recognised, reported, not honoured" is a pattern the
spike caught in the cheapest available place; a resize policy that through reflow depends
on the Unicode work; and resource bounds against a hostile program. Five subsystems of
terminal emulator, maintained by us.

## Before this record may be Accepted

One measurement, narrow and not another emulator comparison. The spike bound a
Linux-built static archive with `-lm -lpthread`
(`.internal/spikes/emulator/ghostty/binding.go:9`), while the helper — which is where the
runtime sits, beside the PTY — builds for `linux/amd64`, `linux/arm64`, `darwin/amd64` and
`darwin/arm64` with `CGO_ENABLED=0` (`Makefile:84`). Remote deployment therefore matters as
much as linking the desktop app, and a deployment obstacle found after the runtime is built
is found in the worst place.

Required:

- build, link and run the minimal binding on **macOS arm64**;
- exercise create, ingest output, extract cells, a reply callback, key encoding driven from
  terminal state, resize, destroy;
- establish a build and link route for the other committed runtime targets, including what
  a remote host needs natively.

Cross-compiling Darwin from Linux is not required — native builders are legitimate. Windows
is not a gate; the vision places it later.

**Not gates, but the first integration gates after it:** colour and attribute extraction;
alternate screen entered, resized and exited; resource use under sustained output; binding
lifetime under stress; and the application-driven input negotiation named in reason 3.
Acceptance must not be read as claiming any of them passed.

## Holding an unstable dependency

`libghostty-vt` says _incomplete, work in progress, unstable_ and ships an ABI manifest
because of it. The answer is not to hedge with a second emulator.

1. **Pin source, toolchain and configuration together** — the full Ghostty SHA, the exact
   Zig version, feature flags, target and deployment baseline — and preserve the source and
   its dependencies in a vendored archive or controlled mirror with content hashes. Never
   build production from a moving branch.
2. **Bind one matched set of headers and library bytes**, statically linked into the
   versioned runtime by preference. The ABI manifest detects structural change; it promises
   nothing about semantics, lifetimes or behaviour.
3. **Keep upstream types inside a narrow adapter.** nocx defines its own cells, styles,
   effects, inputs and errors; borrowed data is copied before it expires; terminal access is
   explicitly serialised. No Ghostty enum value or memory layout reaches the wire protocol
   or a durable document.
4. **Make every upgrade a deliberate compatibility change**: ABI check, the committed
   regression corpus, input negotiation, query replies, resize and buffer transitions,
   capture extraction, and a smoke test per target. Expected results change only with an
   explanation of the behavioural difference. Fixes go upstream; any local patch queue stays
   small and test-backed.
5. **Pin the implementation for a session's lifetime.** A new client or coordinator may not
   swap the emulator under a live PTY. New builds start new sessions; existing runtimes keep
   running. A checkpoint carries its implementation identity, and replacing a library is not
   automatically a valid restore.
6. **Roll back to an older known-good Ghostty, never to `x/vt`.** Switching an authoritative
   session to it would change widths, replies, input encodings and resize semantics in
   exactly the places this record's evidence says they differ. The adapter of point 3
   isolates an implementation dependency; it does not make two terminal semantics
   interchangeable, and nothing in this record should be read as keeping `x/vt` as a
   production fallback.

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
