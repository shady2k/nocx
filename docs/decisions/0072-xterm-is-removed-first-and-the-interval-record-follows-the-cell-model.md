# ADR-0072 — xterm.js is removed first, and the interval record follows the cell model

- **Status:** Accepted
- **Date:** 2026-09-21
- **Supersedes:** the cutover order in [ADR-0066](0066-one-emulator-and-it-is-the-backends.md)
  — the paragraph beginning "There is no partial cutover of the client" in its section
  "Why this rather than the obvious alternative" — and the same claim where the design
  restates it (`.internal/specs/2026-09-12-the-backend-holds-the-session-screen-design.md`
  §9: "The cutover of live display, cards and interaction is one coordinated step, not
  three. Step 6 cannot precede step 7."). ADR-0066 itself is not edited; everything else
  in it, ownership first, stands whole.
- **Related:** `nocx-zg3k3` (the client epic), `nocx-zg3k3.2` and `nocx-zg3k3.2.5` (the
  cell model and the live-display cutover), `nocx-zg3k3.5` (the interval record —
  successor to the retired `nocx-2v80t.2`, carrying every one of its requirements),
  `nocx-2v80t.2.8` (the measurements that retired it). Design §6.3's distinction between
  the live model and the record is NOT superseded.

## Context

ADR-0066 and the design's §9 bound the client cutover with two sentences: live display,
cards and interaction move in one coordinated step, and the record step (§9's step 7)
cannot precede the cell-model step (step 6). Read as a whole they told the next worker
that the cards and the record ride the same step as the live display — that the xterm
removal waits for capture work, and that capture work has no step of its own.

That coupling stopped holding on 2026-09-21, when stage `nocx-2v80t.2` — the stage that
carried the old order's record work and built it ahead of the cell model it was bound to
land with — was retired unaccepted. It built the interval record from what the renderer
could capture, and to do so it built a **second cell representation of the screen beside
the one the xterm removal builds**: the same two-representations fidelity defect
ADR-0066 exists to remove, resurfaced one level up, in the client. Its own bead
(`nocx-2v80t.2.8`) carries the measurements — a record that does not fit one helper frame
at ordinary terminal geometry — and the two independent reviews that retired the branch
rather than finishing or freezing it are kept where the branch is
(`feat/capture-is-one-record`, commit `94e6d30e`). The successor that carries every one
of the retired stage's requirements is `nocx-zg3k3.5`.

## Decision

> **xterm.js is removed first. The interval record — the runtime-owned record of what a
> command printed during its execution — is built on the cell model afterwards.**

The client's cell model and the live-display cutover land first (`nocx-zg3k3.2`, whose
last task is the cutover itself). The record is then built ON that cell model
(`nocx-zg3k3.5`): it shares the cell vocabulary and the revision identity, and it is not
one object with the live model. The live model answers which cells exist at revision R;
the record answers what was observed during execution interval I, including rows no
longer in the live rectangle. That distinction is design §6.3's, and it stands.

## Why this rather than the obvious alternative

The obvious alternative is the old order, and it was not re-decided in the abstract — it
was tried at stage scope and retired. A record that must carry cells the client no longer
holds needs a cell representation of its own; building that representation before the
client's cell model exists guarantees a second one, and two representations that must
agree have the fidelity problem forever — ADR-0066's own argument, one level up. Building
the record on the cell model makes it a second CONSUMER of one vocabulary instead of a
second vocabulary.

## What stands

- **What ADR-0066's paragraph actually protects.** Its hazard is a server diff against
  frame F plus a surviving `clearViewport` — the next diff assumes cells the client has
  erased. That hazard is display and clearing, and it is closed before the painter
  lands: `nocx-2v80t.3.3` removes every path that clears or rebases the client's buffer,
  and the cell model waits for it. There is therefore a partial cutover of the client in
  this plan, and this record says so rather than asserting the old invariant survives:
  display cuts over first (`nocx-zg3k3.2.5`), input becomes intent later
  (`nocx-zg3k3.3`), selection later still (`nocx-zg3k3.4`). Input continuing to travel
  as bytes while the screen arrives as frames is coherent because the runtime owns the
  emulator on both sides — bytes in, frames out — so nothing derives the screen twice.
  What this record does NOT claim is that display and input may cut over in either
  order; the hazard the old paragraph named is the display half, and that is the half
  sequenced first.
- **ADR-0066's ownership decision**, whole: one emulator, on the backend, beside the PTY.
- **The retired stage's requirements**, carried whole by `nocx-zg3k3.5` — one record per
  authenticated interval, the output of a command nobody watched, rows that left the
  screen retained or their loss explicit and counted, card and search text as projections
  of one source, and the five distinct readable states.

## What this record does NOT decide

The record's wire shape and transport — that is `nocx-zg3k3.5`'s work, and
`nocx-2v80t.2.8`'s frame-size measurement is one of its inputs. The client renderer
choice is the 2026-09-21 design note's, not this record's.
