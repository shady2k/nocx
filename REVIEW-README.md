# The scrollback design review, kept where the branch is

Four rounds with a codex reviewer on 2026-09-21, and the briefs that drove them. Kept
here rather than merged, the way the reviews that retired `feat/capture-is-one-record`
are kept on that branch.

The DECISIONS these produced are not here — they are in the tracker, where the work is:
`nocx-zg3k3.10` and its tasks, `nocx-zg3k3.5`'s body, and `nocx-zg3k3.5.1`. What is here
is the reasoning behind them, and two things that are not restated anywhere else and will
be wanted when the durable record is built:

- **Round 2** carries the protocol-16 shape for carrying a history page over the frozen
  helper ABI — a new `TypeJournalData` frame with its own header beside a
  byte-for-byte-frozen `SessionFrame`, `journal-read` / `journal-ack` ops and a
  `journal-advanced` notification — and the reasoning for why the existing session
  channel cannot carry it.
- **Round 3** carries the minimum viable shape of a libghostty row-eviction effect, and
  why per-row beats per-batch and beats a library-owned queue.

Read the briefs alongside the answers: each round's brief states what was conceded and
what was challenged, so the answers are not readable as standalone claims.

**One correction to carry.** The round-4 answer names `ghostty_terminal_get_scrollbar`.
No such symbol exists in the pinned headers; the retained-row count is
`GHOSTTY_TERMINAL_DATA_SCROLLBACK_ROWS` read through `ghostty_terminal_get`, and there is
a `GhosttyTerminalScrollbar` struct for the scrollable-area dimensions.

**And the thing the whole review missed**, which is the reason to read the tracker first:
the emulator's departed-rows report already existed, built and accepted on 2026-09-20 as
`nocx-2v80t.2.1`, and the review re-derived its central finding — that the scrollback
depth count is the only departure signal libghostty publishes and is not monotonic under
pruning — two days later and independently. The search that would have found it was for
the behaviour ("rows that leave the screen"), not for our name for it ("scroll-off",
"drain").
