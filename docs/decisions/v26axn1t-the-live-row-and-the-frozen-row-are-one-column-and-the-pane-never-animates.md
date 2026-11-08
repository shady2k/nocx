# ADR-v26axn1t — The live row and the frozen row are one column, and the pane never animates

- **Status:** Accepted (owner decision, 2026-11-08, on `nocx-djx1u`)
- **Date:** 2026-11-08
- **Decided by:** the owner, on the stand and on the macOS nightly: apt's output
  "jumps and twitches with every new line", its lines "drawn flush left with no
  block indent" while it runs and "the block takes its proper indented shape only
  when the command ends"; and then, replacing an earlier wish for a smooth
  scroll: "like Warp, no animation. The output is anchored to the bottom, and
  every frame shows the newest rows where they will stay; nothing reflows, the
  content height never grows before its rows are painted, live and finished
  blocks have one shape. A person scrolled up is not moved."
- **Supersedes, in part:** [ADR-3kqrem7v](3kqrem7v-a-running-program-keeps-one-header-and-one-grid-across-screen-modes.md)'s
  two geometry rows — "Active grid inline gutter 0px" and "Active body
  top/bottom padding 0px / 0px" — and the sentence that says the running
  geometry is a pane property while "frozen rows retain the ordinary transcript
  gutter and rhythm". Its invariant, **one geometry independent of
  presentation**, is unchanged and is what this record keeps. Also supersedes
  the 140 ms FLIP settle (`scrollback/controller.ts`, bought by `nocx-i4h04.2`,
  `.3`, `.4` and `nocx-2v80t.3.19`/`.35`), which no record of its own ever
  accepted. Neither accepted record is edited.

## Context

A running command's rows and the block it becomes were drawn in different
shapes. `.scrollback-inner > :not(.cell-metric-probe)` gives every row of the
transcript a `--pane-inline-padding` gutter (24px at the default pane); a rule
added for ADR-3kqrem7v then zeroed that gutter on the running block, its
committed body and the live region, so the running grid kept the pane's full
width and the program was never resized by presentation. The price, measured on
the owner's screen and in the e2e container (`e2e/output-jitter.spec.ts`, the
burst test): a running row's left edge at 0 and a frozen row's at 24 — apt's
lines moved 24px right at the instant the card froze, every time.

Around the same geometry sat a 140 ms FLIP settle. It was measured in the same
spec on the owner's report of jumping: the frame after every `block.grew`
carried `.scrollback-inner` translated by exactly the rows that delivery had
committed (68, 96, 116 and 223 px across one apt run), re-armed every ~50 ms, so
the pane never stopped moving; and at the freeze it moved the last rows DOWN by
34 px, which is a frame that goes back. The live box's height was also the
frame BEFORE its rows — the resize pass ran ahead of the painter — so a burst of
ten rows arrived as a step of 170 px with the newest rows clipped until the box
caught up, and at the hand-off a box sized for the taller scroller left the grid
cut mid-row with a blank band beside the returned composer.

## Decision

1. **One inline gutter, in every mode.** The terminal area wears
   `--pane-inline-padding` at the prompt, while a command runs, under an
   alternate-screen program and after it freezes. The running block therefore
   wears the same gutter as the frozen block it becomes, and the grid is fitted
   INSIDE that gutter (`terminal-content.usableViewport` reads the live area's
   own content box). ADR-3kqrem7v's invariant holds because the gutter is the
   same in every presentation: the desired grid never depends on a buffer
   switch, a mode change or a card's state.
2. **Nothing animates.** No settle, no glide, no FLIP. A structural change
   lands whole in one task, the tail is re-pinned in the same task
   (`ScrollbackController.settleAtTail`), and the frame that paints shows the
   rows where they will stay. `prefers-reduced-motion` no longer has a
   movement to suppress here, because there is none.
3. **The box and the rows it shows are one paint.** The live region's height is
   derived from the frame being painted — its last row of text and the cap the
   scroller currently offers — and set inside that frame's paint batch, in the
   same callback as the painter's rows. A resize pass may still correct a
   rectangle; it may never grow the box ahead of the rows.

## Consequences

- A command's output never changes column position when it finishes. The
  terminal area is 48px narrower than it was (at the default pane, one pane
  gutter each side), and every session — prompt, running, full-screen, frozen —
  shares that width, so no program is resized by anything but a pane change.
- A burst of rows that arrives together is painted together, in one frame, and
  the scrollbar's room appears with the rows that fill it.
- What is given up is the settle: the composer leaving at submit and returning
  at the end now lands as one step rather than a 140 ms glide. That is the
  owner's decision, taken against a measurement in which the glide was the
  jitter rather than its cure.
- `motion.ts` keeps `SETTLE_MS`/`SETTLE_EASING`, which the composer's own box
  transition still uses; the scrollback has no motion left.
