# ADR-tqjt5rkk — The composer hides only when the command outlives a grace

- **Status:** Accepted
- **Date:** 2026-11-08
- **Related:** supersedes, for what is shown, step 1 of
  [ADR-0004](0004-input-ownership-and-editor-abstraction.md) §2 ("hide the DOM editor
  before sending anything"); ADR-0004 §1 (input ownership) unchanged;
  [ADR-0024](0024-authenticated-shell-integration-channel.md) (the lifecycle the
  "still running" is read from); `nocx-j9v81`.

## Context

Every submit took the composer off the screen in the same task that sent the
line: `CommandEditor.commit` emptied the draft, hid the box and called submit,
and the box's ink left on a ghost. For a command that returns in a few
milliseconds — `ls`, `true`, `cd` — the box therefore left and came back inside
two or three frames, and what a person saw was a flicker at the bottom of the
pane, once per Enter.

The owner named three alternatives and chose to keep hiding: the composer as a
permanent status strip, hiding only when the command's output does not fit, and
type-ahead into a program that is already running. Hiding is the behaviour they
want for a command that runs. What reads as jitter is hiding a command that has
already returned.

Warp was measured by hand, by the block time Warp itself prints: a block of
0.028 s (`ls`) keeps its editor, a block of 0.052 s (`sleep 0.02`) hides it. So
the window has to sit between 28 and 52 ms of command time, and 40 ms is the
middle of it. The e2e uses commands clearly on each side: `true`, and
`sleep 0.1`.

The threshold is the owner's **40 ms**, and it is not the machine's to move. On
a real machine a no-op command blocks for a few milliseconds and keeps its
composer. A machine whose own block for that command is longer than the grace —
a loaded container, where the interval the block measures includes the
transport's and this renderer's own work — gets the behaviour its own number
says: `true` takes its composer down there, and the e2e states the rule against
the length the block printed rather than against the machine (`sleep 0.2`, a
fifth of a second, is the case no clock can argue with). Moving the threshold
for such a machine would be changing the decision to hide a problem the test
has.

ADR-0004 §1 still decides the keyboard: after the line is out, the shell — or
whatever it started — owns the keys, and nocx does not sniff the stream to guess
whether the program wants them. That is not what this record changes. What it
changes is when the box is PAINTED away.

## Decision

**The hand-off keeps its step 1 where ownership is concerned, and loses it where
paint is concerned.** At a person's shell submit the composer empties its draft,
releases the keyboard and becomes `inert`, all in the submit's own task; the line
goes out at once. The box then stays DRAWN, and what decides its departure is the
command's own length.

That length is the block's own, and the block's own is the attempt's: the
interval its two BACKEND timestamps carry, `startedAt` to `completedAt` — the
figure the block's header prints, because the freeze takes it from the same
attempt. One number, so the length a person reads on the block and the decision
the composer took cannot disagree.

**Nothing is decided before the completion arrives, and that is the point.**
Between the grace and the completion the renderer holds no measurement of the
command: a deadline counted on its own clock cannot tell a command that ended
20 ms ago from one that is still running, because the fact that says so is
exactly what has not arrived. So the box stays DRAWN and inert through that
window, and the completion is what settles it:

- a command whose block ran under the grace keeps the composer where it never
  left it. The prompt mark, a few milliseconds behind the completion, hands the
  keyboard back — no ghost, no fade, no layout change at all;
- a command whose block outlived the grace takes the box down at that moment, in
  the departure the hand-off used to perform at the submit: the ink fade, the
  departure ghost, and the settle glide that plays the freed height back.

**What a long command pays for that.** Its box stays on screen until its
completion, so the pane's `DEPARTURE_FALLBACK_MS` (250 ms from the submit) is
what a command that reports nothing gives up at — a command still unreported at
that bound is long by any reading, and the box leaves then so the running
command gets its space. The same bound covers the completion whose prompt mark
never arrives. That wait is the price of never blinking a quick command; it is
paid only by commands that run for at least a quarter of a second.

The alternate screen outranks the wait: a full-screen program owns the viewport
by the shell's own markers, so entering it settles the departure at once instead
of leaving an inert composer under `vim` until the bound.

Keys typed while the box waits reach the program and never the composer. That is
a consequence of the ownership step above rather than a second rule: the box is
blurred and `inert`, the grid already has the keyboard, and `isVisible` — the
word every routing decision reads — has been false since the submit. A click
during the wait cannot put the caret back either, which is what `inert` is for.

## Why this rather than …

- **Hiding at the submit, as before.** That is the flicker: a `true` block is back
  in a few tens of milliseconds, and the box spent that interval as an empty
  rectangle on its way out and back in.
- **Not hiding at all.** The owner's choice was to keep hiding. A composer that
  never leaves also costs the running command the pane height it needs, which is
  the argument `nocx-g6hnk` already settled.
- **Hiding on the size of the output.** Output arrives after the command, so the
  decision would be made by a stream the renderer may not have yet — and the
  question the owner asked is about the command, not the bytes it printed.
- **Hiding on a deadline that expires by itself.** The obvious implementation —
  arm 40 ms at the submit, hide when it fires — and it says nothing about the
  command: the timer fires whether the command ended or not, so it has to be
  right about a length nothing has measured yet, and it takes a command that
  has already returned off the screen. The grace here is a COMPARISON with a
  length, not a delay after a keystroke.
- **Trusting the prompt mark alone.** The prompt mark is a fact about the shell,
  not about the command, and it arrives after the completion. Counting to it
  charges the command for everything the shell did after it finished.

## Consequences

- A quick command's composer never leaves the layout, so nothing moves for it:
  no ghost, no fade, no settle glide. The block opens above a box that stays
  where it was, which is what Warp's own editor does.
- A command that outlives the grace departs exactly as every command used to,
  inside the departure's ink fade, ghost and glide; only its moment moves — to
  the completion that reported the length, or to the fallback bound when no
  completion is reported at all. The one visible cost of deciding at the
  completion is that a command which ends before the bound leaves a few
  milliseconds before its prompt returns: the fade is barely under way when it
  is called back, which the departure's own ghost-and-return machinery already
  handles (`nocx-1ebrs.40`).
- `isVisible` keeps its single meaning — the box owns the keyboard — and is now
  deliberately not the same question as "is the box on screen". Anything that
  asks the second must read `display`; anything routing a key must keep asking
  the first.
- The departure belongs to the pane, because every fact it needs is a lifecycle
  fact the pane already holds: the submit that armed it, the attempt's start
  and completion, and the prompt that ends it.

## Revisit when

- The block's own length stops being the figure a person reads — a header that
  rounds, or one that reports a figure the product does not decide on, would
  separate the display from the behaviour this record joins.
- Warp's editor changes hands again, or a command people expect to keep starts
  losing its composer: the threshold is the owner's, and moving it is their
  measurement to repeat.
- A second thing starts waiting on the grace (a summoned composer, an assistant
  turn). It is one timer with one meaning today, and a second caller should
  argue for its own record rather than reuse this one.
