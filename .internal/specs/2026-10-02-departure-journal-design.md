# The departure journal — durable capture at the primary boundary, independent of live retention

- **Date:** 2026-10-02
- **Task:** `nocx-zg3k3.5.13` — rework the interval record's plan (`nocx-zg3k3.5`, reopened) for the
  departure journal.
- **Status of the design:** superseded by the owner's upstream-first direction on 2026-10-03
  (`nocx-zg3k3.5.17`). **Do not implement this journal, its ABI, or its dependent leaves as
  written below.** First pin fresh upstream Ghostty while keeping only what is needed to build
  libghostty-vt binaries; then design the smallest change that actually proves complete durable
  capture. The 2026-10-02 product choice still stands: durable output is independent of bounded
  live scrollback, including a session with no window. The three consultations below are dated
  evidence for the former mechanism, not instructions for new work.
- **What this document is not:** product code. No code, contract or fork change accompanies it;
  the task breakdown it generates (`br`, leaves under `nocx-zg3k3.5`) is the other half of the
  deliverable.

## 1. Problem

The durable record is fed by the emulator's departure report, and the report cannot see a row
that retention already erased. `nocx-zg3k3.10.1` bounded the live tier — every session carries
`terminal.scrollbackLines` (default 10,000) and the port applies a 32 MiB per-session memory
ceiling always (`internal/emulator/ghostty/scrollback.go`). The adapter had cleared both limits
to make capture complete; that unbounded world was load-bearing for the record: when the library
prunes pages inside one feed, the feed's own departures and the pruned pages land in one depth
count that no scalar separates, so the port's honest answer is an error and the block freezes
"Output incomplete" (`internal/emulator/emulator.go`, `DepartedRows`' retention paragraph; the
hole ADR-0075 inherited). At the transcript spec's volume — 500 blocks, ~50,000 rows, ~70 MiB at
its pane width — the default binds at block ~105 and the line maximum cannot help because 32 MiB
binds at ~29k rows. No setting value reaches the premise, so `e2e/transcript-scroll-budget.spec.ts`
is skipped on main (commit `1d738ee7`) until this lands.

The owner's decision: **the durable record keeps every row regardless of the live tier's bounds,
and the live tier keeps its bounds as shipped.** Option 1 (raise the 32 MiB ceiling) was rejected
because it buys capacity, not correctness — any finite bound reproduces the loss, and zero
retention reproduces it instantly. Option 3 (accept bounded capture, adapt consumers) was
rejected with it.

The invariant this plan implements, as the first consultation stated it: **capture happens at
the primary active-area boundary crossing, before any retention decision, and is therefore
independent of live retention.** The durable record stays bounded by the settings that already
own it (`history.outputCapKB`, `history.outputEnabled`, `history.retentionDays/MiB`,
`history.diskCeilingMiB`) and by `maxResendEnds = 64`, never by `terminal.scrollbackLines` or the
32 MiB ceiling.

## 2. Why the boundary crossing, and what the alternatives were

A row must be captured at the last instant its cells certainly exist: the moment it crosses the
top of the primary active area. That crossing is the first location common to the retained path
and the zero-retention path:

- **With retention**, `grow()` moves the row into the page list, and pruning may recycle its page
  later — inside the same feed that produced it.
- **With zero retention**, the row never becomes history at all: `cursorDownScroll` shifts the
  active area in place through `eraseRow`, and `no_scrollback` bypasses both history append and
  page pruning. The row's cells are gone before Go has any readable representation of them.

Every hook after the crossing is too late for one of the two paths, which is what the ranked
rejection of the alternatives (consultation #3, recorded on `nocx-ccr8e`) establishes:

| alternative                             | why it fails                                                                                                                                                                                                                                             |
| --------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| a Go durable ring filled at drain time  | the whole 64 KiB sub-feed is ingested before the drain reads the report; at zero retention the rows are erased in place and vanish before Go has a representation; it is also a third loss-capable buffer                                                |
| a history-append or pre-prune hook      | blind at zero retention (`no_scrollback` bypasses both history append and pruning); hoisted above that branch it **is** the primary-boundary hook under another name                                                                                     |
| raw PTY event log + a second emulator   | correct only with a full ordered event log — bytes, resizes, authenticated lifecycle events, fences, settings changes — because lifecycle authority is deliberately outside the PTY (ADR-0024); doubles CPU; bounded replay still needs boundary capture |
| explicit scalar prune events            | repairs the departure arithmetic, recovers no contents once one feed's departures exceed the retained tail; carrying contents and indices makes it a journal at the wrong hook                                                                           |
| a synchronous per-row boundary callback | correct, but needs a borrowed-row ABI, cannot re-enter non-reentrant Ghostty, pays per-row cgo crossings, needs the same bounded staging and the same six-target release; batching it accumulates rows in the fork — a journal again                     |

The journal is the cheapest defensible shape of the boundary capture. It is a **pull** journal: Go
drains it, so the fork holds no Go-bound queue and the cross-thread surface stays the port's own.

## 3. The fork ABI change

The fork (`shady2k/ghostty`, pinned by `third_party/libghostty-vt/MANIFEST.json`) gains, behind
the existing `libghostty-vt` C ABI:

1. **A monotonic departure odometer** for the primary screen: a counter that advances by one for
   every row that crosses the top of the primary active area, and is **never** advanced by
   pruning, ED3, reset or reflow. A prune or an erase-saved-lines advances the retained floor
   and the layout generation; it never masquerades as a departure and never rebases the odometer.
   The odometer is per primary screen and survives alternate-screen excursions (the alternate
   screen has no history of its own and departs nothing, exactly as today).
2. **A pull journal captured at the crossing, before any retention decision.** Both crossing
   paths — the `grow()` append into the page list and the zero-retention in-place shift — stage
   the departing row's cells (cells, style, wrap flags — the same reading `DepartedRows` copies
   today) with the odometer value the crossing produced. The journal is a write-ahead stage with
   a **bounded** staging area whose peak is charged to the shared pool (§4); Go pulls entries
   through a new drain entry point, per sub-feed, and a fully-drained journal is the normal state
   after every `Ingest` and every `Resize` return. The drain returns rows oldest-first with the
   odometer span it covers; **raw journal Next only delimits what to drain** (§5).
3. **Retained floor and layout generation on the history snapshot read.** Every history read the
   port takes (the `HISTORY` point tag path behind `Terminal.HistoryRows`) is answered as one
   atomic snapshot: the rows, the total, the **retained floor** (the odometer value below which
   nothing is retained — the floor the port today derives as `screenDepartedRows − HistoryRows.Total`,
   which reflow and refill debt can silently corrupt) and a **layout generation** (§6). Reading
   floor, head and generation is one operation, not three.

**Marker and block attribution stays in Go** (ADR-0024 decision 1): the fork's journal carries
rows and odometer arithmetic and nothing else. A sighted fence or output mark only locates an
already-authenticated event; the fork never sees a nonce, never seals an interval, never emits an
end marker.

**Alternate screen.** Alt-screen scrolling emits no durable departures — unchanged. A shrink
while the alternate screen holds the pane pushes hidden primary rows into the primary's history;
that crossing **emits into the journal at `Resize`, once**, and not again when the alternate
screen returns. The existing shapes in `internal/sessionruntime/alt_screen_resize_rows_test.go`
are the acceptance for both halves.

## 4. The byte-credit pool — one budget, every stage

**The journal, the Go replay ledger, the pending wire FIFO, closing screens and markers all spend
ONE byte-credit pool: the session's existing `history.helperBufferMB` allocation.** There is no
new setting, no hidden internal budget, and no third loss-capable buffer — ADR-0075 stands
unamended, and its decision that the buffers are the person's settings is exactly why the new
stage must live inside one of them.

- **What counts:** the C journal's staged peak (the fork reports or takes a bound the pool sets),
  the Go conversion/replay ledger's held rows, the wire FIFO's queued rows and markers, the
  closing screens, and the end markers. Every byte of row content the session holds between the
  boundary crossing and the durable ack spends the pool.
- **Ownership counting:** each row spends once per simultaneous owner. Copying across the C/Go
  boundary or from journal to ledger may briefly hold two owners — **both count**; the pool is
  charged for the transient double, so "copying cannot briefly double-spend the bound" is a test,
  not a hope. A row confirmed durable and reclaimed spends nothing.
- **Overflow:** when the pool is exhausted, the existing ADR-0075 path runs — the block in flight
  ends incomplete from the named row, the journal drops what it cannot hold (rows until the next
  end marker, which is carried with its own fence), and recording resumes at the next command.
  The floor of `MinRowBufferBytes` (one closing screen plus one marker) stays the guarantee that
  the overflow marker itself always fits.
- **Two loss causes, never confused:** live-retention loss (the defect this plan removes) and
  helper-buffer overflow (the honest, stated, person-sized bound that remains) must be
  distinguishable in every emission — tests assert both directions.
- **A synchronous, fully drained C journal** may remain an implementation detail of the fork only
  because it has no independent lossy cap and its peak is inside this accounting. If the fork's
  staging ever refuses rows, that refusal is pool exhaustion and takes the §4 overflow path,
  surfaced — never a silent fork-side drop.

## 5. The logical cursor, and the snapshot ordering at the marker split

The fork's raw odometer space and Go's logical row-stream space are different, and the difference
is the closing-screen suppression the runtime already performs
(`internal/sessionruntime/observation.go`, `drainObservationLocked` → `suppressBoundaryScreenLocked`
→ `streamRowsLocked`): raw `screenDepartedRows` is larger than logical `departedRows` by exactly
the rows the suppression window withheld.

- **EndRow is the LOGICAL `departedRows`, snapshotted after suppression.** The raw journal Next
  delimits what to drain; it is never an interval boundary.
- **The ordering at the marker split is load-bearing and already exists** (`Session.Ingest`
  feeds sub-chunks split at each complete fence marker): feed the sub-chunk ending at the marker;
  drain every journal entry that sub-chunk produced; run closing-screen suppression; stream the
  kept logical rows; **then** snapshot `departedRows`; only after that may the fence's sighting
  (`sightDrainedFenceLocked` / `splitObservationAtFenceLocked`) rebase the next interval, and
  only after that may any byte after the marker be fed. `splitObservationAtFenceLocked`'s
  `EndRow: s.departedRows` and the completion-first seal's `emitIntervalEndLocked(…,
s.departedRows, …)` keep their meaning: logical, post-suppression.
- The per-sub-feed drain keeps a feed's own departures streaming before the end marker of the
  interval that feed belongs to, exactly as the report-based drain does today.

## 6. The layout generation across the live tier

Live-history positions move under reflow, pruning and ED3 (ADR-0078, "What the next person
inherits"), and the `session.historyPage` contract today promises the opposite — "a resize …
move[s] nothing" (`contracts/session.historyPage.schema.json`, `start`). That sentence is
corrected in the same change that makes it true:

- A **layout generation** is added to the fork's history snapshot (§3.3), to
  `sessionruntime.HistoryPageView`, to the helper's DTO, and to the `session.historyPage` wire
  result (and the correlated rows carrier). It advances on every layout-invalidating reflow and
  every destructive retention change; prune and ED3 advance floor **and** generation, never the
  departure odometer.
- **A live-history cursor is (generation, before).** A request whose generation does not match
  the current one is answered with an explicit stale/reset answer naming the current generation
  and floor — never rows measured against a layout the caller never saw, and never silently
  reused positions.
- Floor, head and generation are read atomically from the fork (§3.3); the port stops deriving
  the floor as `screenDepartedRows − HistoryRows.Total`.

## 7. Resend: the replay ledger

`resendFromScrollback` reconstructs indices from the current, reflowable retained history, so a
width change can split or join rows and the cells it resends are not the rows that originally
carried those indices — in conflict with exact capture. It is replaced:

- **An indexed replay ledger of original cells**, populated after Go suppression, holds every
  logical row from its emission until a **prefix-proven** durable acknowledgement reclaims it.
  The ledger and the pending wire FIFO are one bounded ownership under `history.helperBufferMB`
  (§4), not two copies.
- **Resend offers [mark, D) from the ledger's original cells**, interleaving the named dropped
  ends as today (`maxResendEnds = 64` unchanged). Reflowed resend — reading the live history to
  relabel rows at old indices — is **rejected**.
- **Reclaim needs an ack that proves the entire logical prefix durable**: a real reclaim
  watermark (or explicit missing ranges), not "retain from the earliest interval forever". The
  existing `EarliestIntervalStart`/confirmed-mark repair exists because an ack once leapt over a
  block's missing head; the ledger's reclaim must be prefix-proven for the same reason.
- **Generation mismatch states loss, never relabels.** If an exact span is absent because the
  pool overflowed, ADR-0075's incomplete semantics state it. A same-generation live-history
  fallback may be an optimization, never the correctness source.

## 8. The fork release is a real release

The ABI change is a fork release, not a Go-wrapper-only change:

- the fork source lands on `shady2k/ghostty` (the pin's branch over `nocx-pin-e2e53f861482`)
  with the generated public headers;
- all **six manifest targets** are rebuilt — linux amd64/arm64 in musl and glibc, darwin
  amd64/arm64 — and `MANIFEST.json` records the new commit, `baseCommit`, `patch`, tag, and the
  archive and header sha256s (`make vt-recipe-pin`);
- `make vt-verify-link` proves every archive links — including **new-symbol verification**, so a
  mismatched headers/archive ABI on one of the six targets cannot ship;
- `vtfetch fetch` verifies before anything links, and the release is published to the fork's
  GitHub release **out of band by the coordinator** (`third_party/libghostty-vt/README.md`,
  "Publishing" — an outward-facing act, not a task brief);
- the licences document and notices staging ride the same pin, as every re-pin does.

## 9. Checks — the acceptance shape

**One end-to-end check, unchanged in shape:** `PW_PROJECTS=chromium e2e/run-in-container.sh
e2e/transcript-scroll-budget.spec.ts` — the 500-block transcript scroll, unskipped (reverting
`1d738ee7`), passing with the DEFAULT live budget. This is the spec that freezes at block ~105
today.

**The deterministic lower-tier list** (recorded on `nocx-ccr8e`; TDD-first at the seams below):

| check                                                                                                                               | seam                              |
| ----------------------------------------------------------------------------------------------------------------------------------- | --------------------------------- |
| capture complete at DEFAULT, 100k and ZERO retention (100k binds at 32 MiB; zero discards live immediately, capture still complete) | port/runtime tests, real geometry |
| same-feed prune: departures and pruned pages in one feed no longer conflate                                                         | port                              |
| raw vs logical at a marker: EndRow is post-suppression logical `departedRows`                                                       | runtime                           |
| marker + following bytes: sub-feed split keeps rows before the end marker, rebases after                                            | runtime                           |
| reflow during resend: resend serves original cells, never reflowed ones                                                             | helper                            |
| prune/ED3 stale generations: cursor mismatch answers stale/reset, floor+generation advance, odometer does not                       | runtime + wire                    |
| alternate-screen output departs nothing                                                                                             | runtime                           |
| hidden-primary shrink emits once at `Resize`, not again on return                                                                   | runtime/port                      |
| journal drain/release on success, error and close                                                                                   | port/runtime                      |
| the pool's transient double-copy stays inside `history.helperBufferMB`; overflow takes ADR-0075's path and names the row            | helper                            |
| the two loss causes are distinguishable in the emission                                                                             | helper/runtime                    |
| `session.historyPage` carries generation over the real wire (DTO conform + over-the-wire conform)                                   | transport/contracts               |

The performance budget for the ingest hot path (the stage's existing benchmark) must hold:
journal drain is on that path, and it may not make a bounded-feed ingest measurably more
expensive than the report read it replaces.

## 10. Out

- Raising the 32 MiB live ceiling or changing `terminal.scrollbackLines` semantics (option 1,
  rejected; the live tier stays as shipped).
- Bounded capture with visible incomplete marks (option 3, rejected) — and any consumer change
  that shrinks the transcript spec to fit inside the live budget.
- A second emulator, a Go-side scrollback copy beside the emulator's, or a raw PTY event log.
- Amending ADR-0075, or any new user-facing buffer setting: the pool is `history.helperBufferMB`.
- Durable-tier retention changes; `clear`/ED3 boundary semantics (ADR-0078 unchanged).
- The delivery mechanics (record id, outbox, ack-after-commit, recovery) — filed separately, as
  the stage already records.

## 11. Risks the reviewers named, carried into the briefs

- Double count, duplicate or omission around grow/refill debt, hidden-primary reflow, ED3/reset,
  and marker sub-feed boundaries.
- C/Go ownership and temporary double copies escaping `history.helperBufferMB`.
- A scalar confirmation that cannot safely reclaim the exact replay ledger.
- Stale live cursors silently reused across a generation.
- Shipping a mismatched headers/archive ABI on one of the six targets.
- Replacing live-retention loss with helper-buffer loss without surfacing ADR-0075's incomplete
  state — the tests must distinguish the two causes.

## DONE WHEN

The 500-block transcript-scroll-budget e2e passes with the DEFAULT live budget on a session
whose capture is fed by the departure journal, and every deterministic lower-tier check in §9 is
green — a person's 500-command transcript is complete in the record while the live tier keeps
its 10,000-line default, and at zero retention capture is complete while nothing is scrollable.
Where the outcome is seen: the transcript spec itself, run in the container; the loss wording on
a card, in the product.
