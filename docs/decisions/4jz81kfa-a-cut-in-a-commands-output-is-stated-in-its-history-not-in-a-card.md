# ADR-4jz81kfa — A cut in a command's output is stated in its history, not in a card

- **Status:** Accepted (owner decision, 2026-11-08, on `nocx-ipuy3`; removes the
  presentation `nocx-fz4qa` and `nocx-zg3k3.14.24` added, and supersedes only
  that presentation — the transport contract in
  [`.internal/specs/2026-10-10-live-session-output-gap-design.md`](../../.internal/specs/2026-10-10-live-session-output-gap-design.md)
  is unchanged)
- **Date:** 2026-11-08
- **Related:** AD-6 (the backend owns terminal state), AD-8 (one owner per
  behaviour), ADR-0019 §7 (retention evicts bodies while entries stay),
  ADR-0040's retention rule, and the block-rows artifact of ADR-0073's
  vocabulary (`ledger.blockRows`).

## Context

A session's output is kept under two bounds, and they lose different things.

The **live** terminal keeps rows in the backend's emulator. A full-screen
program draws those rows on the alternate screen, which has no scrollback: its
redraws are overwritten in place and leave nothing behind.

The **durable** store keeps what a command printed as the block-rows artifact,
under `history.outputCapKB`. Past the cap it drops a middle stretch and keeps
the head and the tail, which is the shape errors and the invocation need.

`frontend/src/recovery-notice.tsx` stated the _recording's_ byte holes as a
card above the pane. Three things made it wrong rather than merely noisy:

- It named a session-level total, in bytes, with no place in the product a
  person could look at. The bytes it counted were mostly not readable output:
  a full-screen program's redraws grow the recording's hole constantly, so the
  card returned right after it was closed.
- The pane's history is drawn from the runtime, not from the recording. A
  window that came back met the same finished hole on every bind and read it
  as the close losing output.
- Its replacement mechanism (`recovery-announcements.ts`, remembering which
  ranges an earlier window named) could only ever subtract from a number that
  named no place.

## Decision

**No card, and no session-level byte total.** The owner removed the card
altogether on 2026-11-08.

What replaces it is one quiet line, in the command's own history, at the place
its output was cut. The block-rows artifact carries an absolute row number on
every row it kept, so the rows either side of a hole leave a jump in that
numbering; that jump is the only place in the product that says _where_
output was cut, and the line is drawn there
(`frontend/src/scrollback/block-rows.ts`, `storedRowCuts`).

- A block whose rows were cut by the size limit says so at the cut: "Earlier
  output was not kept: this session's size limit dropped N rows here." The
  count is the gap the row numbers leave, read from the rows themselves.
- The block's own completeness notice keeps every other cause (an overflowed
  stream, capture refused, rows lost before capture, a coordinator that was
  away). It states the cap too, but only when the block has no rows left to
  carry the line.
- A full-screen program's redraws leave no rows in the artifact, so there is
  no cut and no line. Nothing readable was lost, so nothing is said.
- The line is inside the block's body, so a person meets it by scrolling to
  it. It is not a banner, not dismissible, and cannot come back on a reopen:
  it is a property of the stored history, not an event about a reclaim.

The line is nocx's sentence, not the program's: `blockOutputText` skips it when
a block is copied, because a copy is the text the command printed.

`recovery-account.ts` (the one derivation of "how much is missing") had no
reader left once the card went, and neither did `recovery-announcements.ts`;
both are deleted rather than kept for a surface that may want them later. The
session handle still carries `recovered` and still applies the recording's
revision, because that fact belongs to the handle; nothing paints it.

## Consequences

- A hole in the bytes the recording dropped is no longer announced anywhere.
  That is deliberate: those bytes were never a place a person could read.
- A person who wants the whole of a command's output raises
  `history.outputCapKB`; the block no longer nags about it, and the setting
  still says what it does.
- The e2e that compared the card with the recording's ledger is replaced by
  one that finds the line at the cut and compares its count with the
  artifact's own `droppedRows` (`e2e/output-cut-marker.spec.ts`), plus the
  full-screen case that must show no line at all.
