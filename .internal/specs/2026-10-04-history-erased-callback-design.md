# The history-erased callback — complete durable capture on fresh upstream, one observational fork effect

- **Date:** 2026-10-04
- **Task:** `nocx-zg3k3.5.18` — the refreshed terminal library gets a minimal durable-capture
  design. Supersedes the departure-journal plan ([`2026-10-02-departure-journal-design.md`](2026-10-02-departure-journal-design.md),
  marked superseded by the upstream-first direction of 2026-10-03).
- **Owner decision, 2026-10-04 (recorded on `nocx-zg3k3.5.18`):** add ONE narrowly
  observational callback to the fork, in the shape this document specifies.
- **Status of the design:** approved for planning; no implementation code carries it yet.

## 1. The requirement, restated

A command no window watched has complete output after restart, independent of bounded or zero
live scrollback, except for explicitly budgeted helper/coordinator overflow (ADR-0075).
The durable record therefore cannot depend on the emulator's live retention: rows that the live
budget prunes or erases must still reach the durable row stream.

## 2. Why each previous answer lost

Measured at the pinned upstream `befcdfd2` (scratch `/tmp/nocx-vt-adversarial-csi`):

- **Fixed internal budget + bounded 4 KiB feeding (no fork).** Falsified by the 11-byte
  counterexample `x ESC[1000000b`: one write expands to ~1,000,001 printed cells; at width 80
  that is >=12,476 departures with only 796 retained, at width 1 >=999,977 departures with
  18,604 retained. `anchor.c` reports tracked-row point result -4 after eviction: the loss is
  detectable but the rows are gone. The post-write reader measures depth growth, and pruning
  inside a write makes net growth less than departures.
- **The departure journal (2026-10-02 design).** Correct but heavy: a fork ABI with a
  monotonic odometer, a staged pull journal at both crossing paths, a retained floor, a layout
  generation, a replay ledger, and new accounting pools. The owner retired it on 2026-10-03 as
  too much fork surface for the guarantee it buys.

## 3. The chosen mechanism: one synchronous "history being erased" effect

A new effect in the existing upstream effects table (`GHOSTTY_TERMINAL_OPT_*`,
`include/ghostty/vt/terminal.h`, the same synchronous callback mechanism as BELL, RESET,
SEMANTIC_PROMPT): **a history page is about to be erased**. It is invoked synchronously before
the rows are destroyed and hands nocx the rows to copy. It changes no terminal behaviour.

### 3.1 The effect shape (proposal, to be confirmed against the fork's style)

```c
typedef struct GhosttyTerminalHistoryErased {
    size_t   size;       /* struct size, as every sized C struct in the header */
    uint32_t first_row;  /* HISTORY-coordinate y of the first row being destroyed */
    uint32_t count;      /* rows [first_row, first_row + count) */
} GhosttyTerminalHistoryErased;

typedef void (*GhosttyTerminalHistoryErasedFn)(
    GhosttyTerminal terminal,
    void *userdata,
    const GhosttyTerminalHistoryErased *erased);
```

Registration follows the existing options exactly: one new `GHOSTTY_TERMINAL_OPT_*` value, a
function pointer as the option value, `NULL` clears it. nocx copies the rows named by the span
through its existing HISTORY-tag read path while the callback runs — the rows still exist at
that moment — into the existing bounded departed-row stream. No row serialization is added to
the fork; the fork only announces destruction. This is the "narrowly observational" property:
the fork delta is one effect, three call sites, and a header type.

**Zig plumbing (for the fork leaf, named so the worker does not rediscover it):** the effect
callbacks live on the C wrapper's `Effects`; `PageList` is deep inside `Terminal` and does not
see them. The minimal upstream-shaped thread: `PageList` gains an optional
`history_erased: ?*const fn(pl: *PageList, first_row: usize, count: usize) void = null` field,
set from `Screen.init`/`Terminal.init` options, reachable at the four fire sites; the C
wrapper's `wrap()`/`set` pushes its effect pointer down to the primary and alternate page
lists. Null default means the build is byte-for-byte unchanged for any caller that does not
install the effect — the "changes no terminal behaviour" property is structural, not a promise.

## 4. Every path by which a row that is in history is destroyed — proven complete

Verified by reading the pinned upstream `befcdfd2`. `PageList` holds the pages, oldest first;
history rows are the rows above the active boundary. A history row ceases to exist only when
one of the following runs. Each is a destruction path; each is where the effect fires (or a
place where its firing is proven unnecessary).

### P1. `Limits.enforce` → `erasePage(first)` — the automatic retention prune

`PageList.zig` `Limits.enforce` (L7076) loops while a limit is exceeded, marks the first
page's tracked pins garbage (L7104-7107), then calls `erasePage(first)` (L7112). This is the
only automatic prune site: `enforce` is called by `grow` fast path (L4023), `grow` slow path
after reuse/append (L4128, L4149), `setMaxBytes` (L3976), `setMaxLines` (L3990), `resize`
no-reflow shrink (L1287), `resize` reflow end (L1339), and `clone`'s own enforce (L1217).
**The effect fires right before `erasePage(first)`, at the exact point the fork already marks
the pins garbage** — the owner's named site.

### P2. `grow`'s byte-limit prune block — pop first page and reuse/destroy

`PageList.zig` `grow` (L4000). When a new page would exceed the byte budget and the list has
more than one page, the prune block pops the FIRST page (L4045): pool-owned pages are reused
as the new last page after `restore(.discard)` + `@memset` (content destroyed at L4097-4101);
heap-owned pages are `destroyNode`d (L4092-4099). This path does **not** go through `enforce`.
**The effect fires at the top of the prune block, before `popFirst`** — after this point the
rows are gone. This is the owner's `~PageList.zig:4045` site.

### P3. `eraseHistory` / `eraseRows` — ED3, zero budget, no_scrollback resize

`PageList.zig` `eraseHistory` (L5520) → `eraseRows` (L5546), called from
`Terminal.setScrollbackMaxBytes(0)` (`Terminal.zig:478` — the zero-live-scrollback apply),
`Terminal.eraseDisplay(.scrollback)` (`Terminal.zig:3719` — ED3 / `CSI 3 J`), and
`Screen.resize` when `no_scrollback` (`Screen.zig:2148`). `eraseRows` destroys full history
pages via `erasePage` (L5580) and the boundary page's history rows via the partial shift +
`resetRow` path (L5596-5635). **The effect fires per full page before `erasePage`, and for the
partial boundary rows before the shift/resetRow** — the exact rows being destroyed, handed as
a span.

### P4. `reset` — RIS / ESC c / full reset

`Terminal.fullReset` (`Terminal.zig:4939`) → `screens.active.reset()` → `PageList.reset`
(L984) → `releasePages` (L1012): every page — history and active — is destroyed. **The effect
fires over the history span before `releasePages`.** (The C ABI's existing RESET effect fires
after the reset, too late to copy; it stays as it is.)

### P5. `deinit` — terminal teardown

`PageList.deinit` (L965) → `releasePages` (L973). Destroyed rows here are rows that already
departed and were captured at their departure, or rows still on the (closing) screen that the
runtime's closing-screen machinery captures separately. **No effect is needed at deinit**; the
design obligation is that the port drains the departed stream before freeing the terminal
(already the port's Close contract), and the check below asserts it.

### Paths that are NOT destruction of history content — why the effect must NOT fire there

- **Reflow** (`resizeCols` L1343+, `resizeWithoutReflow` L2792+): old nodes are destroyed
  (L1461-1498, L2598, L3018-3165) but each row's content was cloned into the reflowed pages
  first. Rows move; they do not cease. Firing there would hand nocx rows that still exist —
  duplicates.
- **`compact`** (L3653), **`split`** (L3729), **`increaseCapacity`** (L4216): the old node is
  replaced by a new one carrying the same content.
- **Compression** (`compressPage` L5004): the resident mapping is decommitted, the content
  survives compressed.
- **`trimTrailingBlankRows`** (L3205-3247): during a no-reflow row-shrink, it trims blank
  rows at the bottom of the active area; those rows never entered history (they are active
  rows being removed, with no text), so they are out of scope and **the effect does not fire
  there**. This is why the effect is fired at the four named sites, not by re-wiring every
  `erasePage` call: firing inside `erasePage` generally would hand nocx blank active rows the
  durable stream never reports.
- **`scrollClear`** (L3564): scrolls the ACTIVE area up into history — nothing is destroyed.
- **`eraseRow`/`eraseRowBounded`** (L5191/5303), **`eraseActive`** (L5529), **`scroll`**
  (L3293), scroll-region and alternate-screen paths: operate on the active area; rows never
  enter history there. Out of scope per the owner's decision.

**Completeness proof.** Every history row lives on a `PageList` node. The only primitives that
make a node's content cease to exist are `erasePage`, `destroyNode`, and `releasePages`. The
production (non-test) call sites of these primitives, enumerated from the pinned source, are:

| primitive      | call sites                                                                                                                                                | classification                                                            |
| -------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
| `erasePage`    | `Limits.enforce` L7112; `eraseRows` L5580; `trimTrailingBlankRows` L3247                                                                                  | P1, P3; blank-active-only trim is out of scope (not a fire site)          |
| `destroyNode`  | `grow` prune L4092/4097; reflow L1461-1498/2598/3018-3165; `compact` L3677-3708; `split`-errdefer L3757; `increaseCapacity` L4337/4373; `erasePage` L5684 | P2, reflow (content moved), compact/split/capacity (content moved), P1/P3 |
| `releasePages` | `reset` L1012; `deinit` L973                                                                                                                              | P4, P5                                                                    |

So the complete destruction set for history content is exactly {P1, P2, P3, P4}; P5 is
captured-at-departure by construction. The effect fires at each, once per destroyed span, in
the order the spans are destroyed.

**Ordering guarantee for the durable stream.** The spans are handed to nocx in the library's
own destruction order, which is oldest-to-newest within a prune loop (P1/P2 always take the
first page), and the partial/whole ordering of P3/P4 is the iterator's own. The port appends
in arrival order, so the durable stream keeps the departure order property the existing report
already holds ("the same bytes split at every position report the same rows").

## 5. How each check in the DONE WHEN is passed

The reproducible check is a probe over the pinned archive (the port's own test seam plus a C
probe for the fork effect), asserting row counts and content per scenario. For each scenario,
what the mechanism does:

### 5.1 Normal live history (default budget)

Every departure survives until the post-write read, which captures it as today. The callback
does not fire (nothing is pruned). Check: full transcript, zero loss, zero duplicates.

### 5.2 Nearly-full live history

Pruning happens between writes (an earlier feed's pages age out). Those rows departed earlier
and were already captured; if their prune is observed by the post-write reader as a depth drop,
the port's existing re-baseline prevents a false loss report — and with the callback, the
prune spans are handed explicitly so the port never has to guess (see dedupe, §6).

### 5.3 Zero live history

The apply maps to `setMaxBytes(0)` → P3 fires and the currently retained history is copied
before it is erased. Afterwards the emulator keeps a capture floor of `>= 1 page` (the
byte budget is never literally zeroed; see §7), so rows still pass through pages and P1/P2
fire as the floor prunes them. Live scrollback stays empty because the live tier's served
surface clamps to the person's 0 (nocx-side, §7). Check: feed under a 0 setting, then assert
the durable stream holds every row and the live history page serves none.

### 5.4 Multiple departures inside one write (the 11-byte counterexample)

During one `vt_write`, `grow` appends and P1/P2 fire per page as the budget binds; each fired
span is copied before its rows die. The post-write read then captures only the survivors, so
the union of callback spans + newest growth is exactly the departures. This is the check the
no-fork reader fails (12,476 vs 796 at width 80).

### 5.5 History erasure inside one write (ED3/RIS same-write)

`clear`-style output that scrolls and then erases within one feed: P3/P4 fire before the
erasure, so the same-write departures are copied while alive. The owner allows this either via
the same effect (chosen here) or the nocx-side write-boundary split the previous session
measured; the same effect is uniform and does not depend on byte scanning.

### 5.6 Resize/reflow

Reflow moves content; the effect correctly does not fire in reflow's `destroyNode` (would be
duplicates). Rows that a row-shrink pushes off the top depart normally and are captured at the
write boundary; the boundary-page partial erases of P3 are handed as spans; the port re-baselines
on resize exactly as it does today. Check: shrink/grow mid-transcript, both axes, with pruning
active; complete and duplicate-free.

### 5.7 Reconnect (resend), restart, no open window — and why the resend must not read the live tier

The reviewer's correction of 2026-10-04 stands here: this design does NOT inherit the
shipped resend as-is. Today `internal/helper/session/rows.go` `resendFromScrollback` (L639)
rebuilds the unacknowledged span by re-reading the EMULATOR'S LIVE history
(`t.HistoryRows` via `ReadDepartedScreen`), bounded below by `EarliestIntervalStart`, and the
helper keeps no delivered-row copy (the wire FIFO frees rows once delivered; the owner's
decision gives the helper no second buffer). Two failures follow under the bounded live tier
this stage keeps:

- rows that were DELIVERED but not yet acknowledged are no longer in the helper's queue, so
  the resend re-reads them from scrollback; if the live tier pruned them during the
  disconnect, the resend cannot rebuild them — a loss the "short" path then states;
- a width change during the disconnect reflows the scrollback, so the resend serves reflowed
  rows at the wrong absolute indices — the rows served are not the rows
  that were emitted.

The history-erased callback does not fix this by itself: the callback feeds the durable row
STREAM, and a delivered-but-unacked row is no longer in that stream's helper queue. So the
reconnect check is passed by the design only together with a helper-side resend change:

**The helper keeps delivered rows in its bounded buffer until an ack proves the whole
logical prefix durable, and resend serves from that buffer, never from the emulator's live
history.** The retention is the existing bounded helper buffer (`history.helperBufferMB`,
ADR-0075): it holds rows from their emission until a prefix-proving ack (a real reclaim
watermark or explicit missing ranges — not `EarliestIntervalStart`'s retain-forever repair),
and overflow takes ADR-0075's existing path (the block in flight ends incomplete). Resend
then serves original cells at their original absolute indices: pruned-during-disconnect rows
are still in the helper's retained window, and a width change while disconnected cannot
reflow them because they no longer read from the emulator.

This is exactly the scope of the retired leaves `nocx-ho1ri` (resend from an indexed replay
of original cells, prefix-proven reclaim) and `nocx-4nkw1` (one byte pool for the helper's
buffers). The upstream-first reset retired the JOURNAL, not this helper defect; the two
leaves are reopened with corrected bodies under this design.

Reconnect checks (lower-tier, deterministic):

- **width change during a disconnect**: rows emitted at indices [m, d) survive a reflow
  while the coordinator is away; the resend serves the ORIGINAL cells at their original
  indices, and a generation mismatch is stated as loss, never relabelled;
- **prune during a disconnect**: rows the live tier pruned while the coordinator was away
  are still served from the helper's retained window (they were emitted before the prune);
- **an ack that leaps a missing head**: an ack claiming rows the helper cannot prove durable
  reclaims nothing; the unproven prefix is re-offered, and the loss path names the cause.

Restart reads the coordinator's store; no open window is the ordinary headless path; the
callback changes neither. The checks exist to prove the whole path — new fork effect,
helper retention, coordinator store — holds across a disconnect/rejoin with no hole.

## 5.8 Port-side note: reading rows inside the callback

The port's callback handler must copy via the same HISTORY-tag read path it uses after writes
(`nocxGridRefAt`/row reads). Reads inside effects are already established (`cb_size` and
`cb_title_changed` call `ghostty_terminal_get` inside callbacks); `grid_ref` is the same kind
of read. The rows named by the span are in a page that is still linked and intact — the fork
fires before `erasePage`/`popFirst`/`releasePages` — so resolution is valid. The port leaf
proves it with a test that installs the effect and performs reads inside the handler before
asserting the terminal state after the write. Reads must still respect the header's rule: no
`vt_write` reentrancy.

## 6. Memory and dedupe

- **Dedupe.** The rows a callback hands may already be captured: P1/P2 normally prune rows that
  departed and were read at an earlier boundary. The port keeps its existing cursor — how many
  departed rows it has captured (its "head" counter), resolved for the callback by the span's
  position: rows at or below the captured frontier are skipped; rows above it are appended.
  This is exactly the owner's "everything else nocx keeps reading the way it already does: a
  tracked-pin cursor after each write", with the callback spans feeding the same frontier. No
  odometer is needed: the frontier is nocx's own count, and the fork never sees it.
- **The copy is bounded.** The callback copies only into the existing bounded helper row-stream
  buffer (`history.helperBufferMB`, ADR-0075's helper side). No fork-side staging exists; the
  callback carries a span, not rows. A million-row REP feed therefore streams into the same
  bounded queue, and overflow takes ADR-0075's existing path: the block in flight ends
  incomplete and recording resumes at the next command's end marker. AD-10 binds.

## 7. Zero live scrollback, nocx-side

Today `ApplyScrollback(0)` sends bytes=0, which makes Ghostty enter `no_scrollback` (rows
erased in place, never in pages — the zero-retention hole that no post-write read and no
history-erase callback can see). Under this design the zero branch keeps a NONZERO byte budget
(the existing 32 MiB ceiling, never zeroed) and applies lines=0 — which the library reads as
its own page-floor minimum (measured: 399 rows retained at 80 columns under lines=0), a bounded
capture window. Rows always pass through pages and P1/P2 fire, exactly as at any small budget.
The person's `scrollbackLines = 0` is then honoured at the nocx-side live surface: the
history-page op clamps to the setting and serves nothing, so "no live scrollback" is exactly
what the person asked for while the durable record stays complete. This is the decision
`nocx-ccr8e` calls decoupling durable capture from live retention; the live tier's bounds stay
as shipped. (Consequence, named for the implementers: the port test
`TestApplyScrollbackZeroLeavesNoHistory`, which asserts bytes=0 erases everything and capture
falls silent, is rewritten to assert the new contract instead: retained content is a bounded
floor, the durable stream stays complete, the live surface serves none.)

## 8. Comparison against the DONE WHEN's alternatives; the smallest passing mechanism

| check                                      | corrected post-write reader + bounded feeding                                  | **narrow complete-row callback (chosen)**                                               | fork journal                                                   |
| ------------------------------------------ | ------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------- | -------------------------------------------------------------- |
| normal                                     | passes                                                                         | passes                                                                                  | passes                                                         |
| nearly-full                                | passes (re-baseline)                                                           | passes (spans explicit)                                                                 | passes                                                         |
| zero live history                          | fails (no history to read)                                                     | **passes** (floor + P1-P4)                                                              | passes (primary-boundary crossing)                             |
| many departures in one write (11-byte REP) | **fails (measured: 12,476 vs 796)**                                            | **passes** (P1/P2 per page)                                                             | passes                                                         |
| ED3/RIS in one write                       | fails without a byte-scan split                                                | **passes** (P3/P4)                                                                      | passes                                                         |
| resize/reflow                              | passes (re-baseline, no reflow loss)                                           | passes (effect does not fire in reflow)                                                 | passes                                                         |
| reconnect / restart / no window            | fails on reconnect: resend reads the live tier (prune and reflow eat the span) | **passes** — with the helper's retained-window resend (§5.7, nocx-ho1ri/4nkw1 reopened) | passes (journal-era replay ledger)                             |
| fork delta                                 | none                                                                           | **one effect + 3 sites + header type**                                                  | odometer + journal stage + drain + floor + generation + ledger |

The next-smallest passing candidate after the reader's REP failure is the callback; the journal
passes too but carries the most fork ABI. The callback is therefore the smallest passing
mechanism.

## 9. Memory, six-target ABI and upstream-port cost

- **Memory:** the callback adds no fork-side staging; the port copies spans into the existing
  bounded helper buffer (ADR-0075 overflow path). Peak transient = one erased page being copied
  inside the callback, which is at most the library's own page budget and is already included in
  the session's byte accounting for the live tier.
- **Six-target ABI:** the effect is a real header change (`include/ghostty/vt/terminal.h` type +
  option) and one `src/terminal/c/terminal.zig` option case, released exactly as `nocx-v8d69`
  established: all six manifest targets (linux/amd64, linux/arm64, both glibc/musl triples,
  darwin/amd64, darwin/arm64), `MANIFEST.json` hashes, fetch-verify, `make vt-verify-link`.
- **Upstream-port cost:** a small, behavior-neutral observational hook — an effect that fires
  when the library is about to destroy history pages and lets an embedder copy them. It follows
  the existing effects design (synchronous, opt-in, NULL default), so it is shaped to be offered
  upstream; offering it is a separate owner decision and no upstream PR is opened by this stage.

## 10. What this design deliberately does not build

- **Fork side:** no departure odometer, no retained floor, no layout generation, no
  fork-side staging, no dynamic limit raising, no byte-scan of destructive sequences — the
  fork change is exactly the one observational history-erased effect.
- **Helper side:** the resend keeps delivered rows in the EXISTING bounded helper buffer
  until a prefix-proving ack (nocx-ho1ri/4nkw1's scope, reopened); no second buffer beyond
  ADR-0075's accounting, no journal, no ledger outside the helper's own budget.
- The 500-block transcript acceptance (`nocx-i9qw0`) remains the separate final check; the
  lower-tier deterministic tests replace the journal-plan's lower-tier list one-for-one with
  the checks in §5.

## 11. Evidence

- Pin: `nocx/build-libghostty-vt-befcdfd2` at `befcdfd2c3a1cb24d9ec886e93c95b2b5daa7028`
  (upstream `shady2k/ghostty`), release `libghostty-vt-befcdfd2c3a1`, committed at `43b9fed7`.
- Upstream lines: `src/terminal/PageList.zig` L7076/7104-7112/4000/4045/4092-4101/5520/5546/
  5580/5596-5635/984/1012/965/973/1343/2792/3653/3729/4216/5004/5191/5303/5529/3564;
  `src/terminal/Terminal.zig` L478/3719/4939; `src/terminal/Screen.zig` L2148/957.
- Counterexample measurement: `/tmp/nocx-vt-adversarial-csi/` (`probe.c`, `anchor.c`).
