# Exact history-floor provenance — implementation plan

- **Date:** 2026-10-03
- **Task:** [`nocx-2mm9u`](../../.beads/issues.jsonl) — exact retained departure floor and layout generation in the fork snapshot.
- **Fork PR:** [#2](https://github.com/shady2k/ghostty/pull/2), currently blocked at `538b5f36bcd84157782fc8a0eddee5677c29d284`.
- **Status:** review-ready design draft. The provenance semantics below are settled. Implementation is blocked until both `nocx-zg3k3.5.14` (width-only shadow resize-OOM fix) and `nocx-zg3k3.5.15` (combined width+height journal-aware resize-OOM fix) merge; `.15` depends on `.14`, and both block this task. They exercise distinct resize failure paths in the same PageList code. After both merge, rerun both fault-injection checks before provenance implementation. Keep the two local red tests and caller-serialization note in the PR worktree; do not push them yet.

## Goal and binding decisions

Store enough provenance to return the exact retained departure floor after width reflow,
partial page pruning, history erasure, and active-area refill. Do not derive the floor from
`odometer - physical_history_rows`. The owner approved exact ordinal provenance on
2026-10-03; do not weaken the contract.

The fork-side snapshot is one serialized C getter that returns rows, total, floor, layout
generation, and odometer together. Ghostty terminal handles are not internally synchronized.
A caller must serialize every operation on one handle. Do not add a C mutex or race concurrent
calls on the same handle. The separate Go-port acceptance test belongs to `nocx-nv3in`, under
the existing terminal mutex.

The updated bead and reviewer set these acceptance boundaries:

- `retained_floor` is an ordinal watermark over origin fragments currently in HISTORY. It is
  not a physical history-page cursor and does not count ACTIVE-screen fragments.
- `PageList.clone` creates a detached fresh list, not a live-clock clone: it resets generation
  and omits `Departures`, so it must drop/rebase origin tags. Only a complete live C-terminal
  clone that also copies tags and the matching odometer/floor/generation could preserve them.
- Content-preserving width reflow changes layout generation. It leaves odometer and floor
  unchanged when every tagged historical fragment remains in HISTORY. If reflow moves the
  final fragment of an origin into ACTIVE, that origin is no longer retained history and the
  floor may advance even though its cells still exist on screen.
- Real partial page pruning may advance the floor exactly. ED3 sets floor to the current
  odometer (which may already be the floor) and changes generation; neither advances odometer.
- Provenance allocation capacity for surviving nodes is charged alongside raw page storage
  under the existing heuristic live-retention byte limit. Keep `page_size` as raw backing
  bytes; track sidecar capacity separately. Report resize's transient old-plus-new peak
  separately. The current limit is not a hard instantaneous cap.
- Metadata OOM must never produce a successful snapshot with a floor inconsistent with the
  retained history. The durable departure journal remains separate and is not discarded to
  fund the live-history sidecar.

The governing design is `.internal/specs/2026-10-02-departure-journal-design.md`, especially
§3.3 and §6. The page-memory limit behavior is visible in Ghostty's
`src/terminal/PageList.zig` `createPageExt` and `Limits.enforce` paths.

## Floor, origin, and physical-cursor semantics

The odometer starts at zero. A real primary-boundary departure increments it to the next
1-based origin ordinal. Let `O` be the current odometer and `R` the set of ordinals represented
by at least one provenance span in HISTORY (not ACTIVE). The exact retained floor is:

- `min(R) - 1` when `R` is nonempty;
- `O` when `R` is empty.

Thus any surviving historical fragment keeps its ordinal in `R`; the floor cannot pass it.
Gaps do not permit skipping the first still-retained ordinal. Pruning must leave every
ordinal below the new floor absent from HISTORY. The floor never moves backward.

A content-preserving width reflow changes layout generation but leaves floor and odometer
unchanged when every tagged historical fragment remains in HISTORY. If reflow moves a span
across the active/history boundary, only its HISTORY portion remains tagged. Moving the final
HISTORY fragment of an ordinal into ACTIVE is history loss: remove that history token and the
floor may advance even though the cells still exist on the active screen. If a span is split
between HISTORY and ACTIVE, the HISTORY part continues to hold the ordinal. Reflow never
increments the odometer.

A primary-boundary departure assigns one new ordinal to the full crossing row. If a row
re-enters ACTIVE, retire its old history token; if it departs again, assign only the new
ordinal. Do not layer old and new ordinals. A blank row that crosses gets an ordinal. A blank
row still retained in HISTORY keeps its token even when its cell values are cleared; physical
row deletion or active refill removes the token. During reflow, queue tagged blank-row tokens
until a destination row is emitted; attach each token to that row, or retire it if no row
materializes. This covers the existing `ReflowCursor.reflowRow` behavior that defers blank rows,
drops trailing blanks, and skips blank wrap continuations (`src/terminal/PageList.zig:1729-1755`).
ED3 removes all HISTORY tokens. The journal captures actual crossings independently of whether
its bounded staging area accepts a row.

A row or fragment born in HISTORY from active-only content during reflow has no departure
ordinal. Keep it untagged. It does not advance the odometer or affect the ordinal floor, but it
still contributes to the physical history row count and current layout. For example, `O=0`,
`H=2`, `retained_floor=0` is valid when reflow places active-only content in two history rows.

**Do not use this ordinal floor as a physical pagination cursor.** The current
`internal/sessionruntime/history_page.go` derives `head` and offsets from departure counts and
requires `HistoryPageView`'s physical `H` to satisfy `head - floor`; the wire schema currently
labels `floor/start/end` as absolute physical row numbers. That equation fails for the valid
untagged example above. The later `nocx-gomch` port/wire leaf must give physical pagination its
own layout-generation-local head/cursor, independent of this fork's ordinal floor, and test the
`O=0, H=2, floor=0` case. This fork change does not add a C ABI field for a physical cursor;
only add one if the later port proves it necessary.

### OOM contract from the current source

The C write API is `void`: `src/terminal/c/terminal.zig::vt_write` calls
`Stream.nextSlice`. A fallible VT action reaches `src/terminal/stream_terminal.zig::Handler.vt`,
which catches the action error, sets `semantic_failure`, logs a warning, and lets stream
processing continue. C callers can read the sticky `vt_processing_error` field; it is not a
per-write result, and the C test `get vt_processing_error` confirms it remains set after reset.
A write is not transactional: earlier actions remain applied, a failing
scroll action does not roll back the feed, and later bytes/actions may still be processed.
Therefore, sidecar OOM at one departure must fail before that crossing commits: no new origin
tag, odometer increment, or history-row growth for the failed action; set the existing VT
processing-error signal, and keep the snapshot exact. Tests must not assert whole-feed rollback.
The existing journal refusal path remains distinct: a real crossing still advances odometer
and records the refusal even when journal staging is full.

C resize differs: `src/terminal/c/terminal.zig::resize` returns `Result.out_of_memory` when
`src/terminal/stream_terminal.zig::Handler.resize` / `Terminal.resize` returns OOM.
`Screen.resize` documents that an error
leaves the screen unchanged, and its tripwire test asserts the pre-call PageList and page
bytes. `Terminal.resize` also has an existing best-effort alternate-screen fallback: after a
successful primary resize, alternate resize failure may discard/recreate alternate state and
still return success. Do not change either behavior in this provenance plan.

### Fault-injection evidence (source revision `538b5f36bcd84157782fc8a0eddee5677c29d284`)

I used a detached scratch worktree at `/tmp/ghostty-nocx-resize-oom-probe`; the PR worktree
and its two red repros were not edited. The temporary C-ABI test populated a 10×1 terminal
with 8,000 rows, then resized width 10→5. Its pre-call snapshot was
`rows=8000,total=8001,floor=0,generation=0,odometer=8000`; the successful reflow made two
allocator calls.

- Filtered Debug command:
  `nix develop --command zig build test-lib-vt '-Dtest-filter=TEMP C resize allocator failure probe'`.
  Failing allocation #0 returns C `Result.out_of_memory`; dimensions and all snapshot scalars
  remain unchanged, and a following write/snapshot read succeeds. Failing allocation #1
  aborts before C returns: `PageList integrity check failed: error.TotalRowsMismatch`.
  The injected call is the next destination-node allocation in `PageList.resizeCols`: the
  stack is `UntouchedPool.allocItem/create` → `PageList.createPageExt:4628` →
  `cursorNewPage:2725` → `ReflowCursor.reflowRow:1754` → `resizeCols:1503` →
  `PageList.verifyIntegrity:872` during the resize integrity defer. This is after reflow has
  replaced the live list and begun writing destination rows.
- Filtered ReleaseFast command:
  `nix develop --command zig build test-lib-vt -Doptimize=ReleaseFast '-Dtest-filter=TEMP C resize allocator failure probe'`.
  Failing allocation #1 returns `.out_of_memory`; C dimensions and snapshot scalars compare
  equal to the pre-call values, and a following `vt_write` plus snapshot read succeeds. That
  does not make the list coherent: the Debug integrity check proves its actual page-row sum
  differs from cached `total_rows`; ReleaseFast merely removes that assertion.
- Filtered feed command:
  `nix develop --command zig build test-lib-vt '-Dtest-filter=get vt_processing_error'`.
  The temporary test appends `xy` after the OOMing OSC action, then observes sticky
  `vt_processing_error=true` and `cursor_x=2`. Later input in the same `vt_write` is processed;
  there is no whole-feed rollback. The existing C write seam reports failure through the
  sticky status, not a per-write result.

The width-only shadow resize-OOM defect is tracked as `nocx-zg3k3.5.14`, assigned to an
independent worker on a separate fork branch from the pin base; it blocks `nocx-2mm9u`. A
second, distinct failure is tracked as `nocx-zg3k3.5.15`: injected OOM during combined
width+height resize through the journal-aware path can abort the Screen integrity check at
`cursor.y < pages.rows`. `.15` depends on `.14`, touches the same PageList files, and also
blocks `nocx-2mm9u`; it is not covered by the width-only shadow transaction test. Both fixes
must merge before provenance implementation. Then rerun both filtered failure paths and assert
unchanged-on-error Screen state plus journal consistency (no lost, duplicate, or phantom
pending departure/odometer effect). No provenance-specific rollback or C ABI change is
proposed here; do not redesign product behavior in this plan.

## Candidate representation

Use an optional external sidecar owned by each `PageList.Node`. Standard `Page.memory` is a
fixed pooled mapping; Ghostty compression decommits that mapping. Keep provenance outside it
so the origin map remains available with a compressed node and does not enlarge every pooled
page. The external allocation is still charged to retained-history memory and remains
resident while its node is compressed.

Suggested layout:

```zig
const RowRange = struct {
    first: u32,
    count: u32,
};

const OriginSpan = struct {
    x_start: u32, // inclusive cell coordinate
    x_end: u32,   // exclusive cell coordinate
    ordinal: u64,
};

const ProvenanceSidecar = struct {
    row_ranges: []RowRange, // indexed in this node's physical row order
    spans: []OriginSpan,    // sorted by row, then x_start
    row_capacity: usize,
    span_capacity: usize,
};
```

Use 32-bit x endpoints even though `CellCountInt` is currently `u16`; the endpoint is
exclusive, wide page handling must be explicit, and the sidecar should not rely on a narrower
coordinate than the page APIs. `RowRange` offsets use 32 bits; the page byte ceiling bounds
span count below `u32` for the proposed 16-byte native span. Each row's spans are disjoint,
ordered, and coalesced when adjacent spans carry the same ordinal. Empty ranges mean untagged.
A span includes blank cell slots so cell clearing cannot accidentally erase a retained row
origin. Reflow must explicitly carry or retire tagged blank tokens as specified below.

The sidecar is a candidate, not a settled implementation. `Page.cloneRowFrom` receives
`Page` values today, not `Node` sidecars; clone/reflow wrappers must explicitly decide whether
to copy or retire each source row's tags. A single ordinal or minimum per physical row is
expressly insufficient: merge → split → partial prune can leave a different source ordinal in
each fragment.

`PageList.clone` is a detached row/page clone, not a clone of the live history clock. At
`src/terminal/PageList.zig:1102-1236` it creates new nodes and initializes a fresh `PageList`
without copying `history_layout_generation` or the `departures` attachment (which defaults to
null); generation defaults to zero. The detached `PageList` has no odometer. If it is wrapped
as a new live C terminal, that fresh terminal must start O/floor at zero. Do not claim that this
API preserves source O/floor/generation. Detached clones must drop/rebase provenance tags. Only
a future operation that clones a complete live C terminal, including its sidecar tags and
matching `Departures` odometer/floor/generation, may retain the origins; no such preservation is
implied by `PageList.clone`.

Cache the exact global retained floor in the live `Departures`/`PageList` state and update it
synchronously after every provenance mutation. Snapshot reads return that cache in O(1). A
per-node minimum is useful for mutation-time recomputation, but deriving the global minimum by
scanning all node minima is not an O(1) snapshot; when a mutation can remove the current global
minimum, recompute from surviving HISTORY nodes then (or maintain an explicit ordered minimum
index). Never defer a full scan/decompression to the snapshot getter. Tests must exercise floor
updates after every span-changing operation and verify that snapshot access uses the global
cache.

## Memory, retention, and failure behavior

`page.zig::std_capacity` is 215×215. `Row` and `Cell` are each packed `u64` (8 bytes), so the
standard row-plus-cell grid is exactly `215*8 + 215*215*8 = 371,520` bytes before `MetaLayout`
and page alignment. A native `OriginSpan` with `u32` endpoints and a `u64` ordinal is 16 bytes.

Approximate sidecar payload for one standard page:

| Case                                          | Row-range bytes | Span bytes |             Sidecar bytes |
| --------------------------------------------- | --------------: | ---------: | ------------------------: |
| one span per each of 215 rows                 |           1,720 |      3,440 | 5,160 (~1.4% of the grid) |
| worst case, one span per each of 46,225 cells |           1,720 |    739,600 |    741,320 (~2× the grid) |

For wide fallback capacity, `initialCapacity` can retain 215 rows at up to 65,535 columns:
14,090,025 cells. A one-span-per-cell sidecar is then about 225,440,400 bytes plus row
ranges. This is why the sidecar must be dynamic and charged by its allocated capacity, not
preallocated to the maximum on every page. These are payload estimates; allocator metadata
and geometric spare capacity add overhead. Verify exact `@sizeOf` and capacity accounting in
the implementation tests before fixing a budget number.

Keep `PageList.page_size` as the raw backing-byte count: PagePool and create/destroy paths
assume it describes fixed `Page.memory` mappings. Add a separate `provenance_bytes` count for
the allocated Node-sidecar capacities:

- count `row_ranges.capacity * @sizeOf(RowRange)` and
  `spans.capacity * @sizeOf(OriginSpan)`, not only populated lengths;
- update it on sidecar allocation, capacity growth, free, node erase, reset, deinit, and pool
  node reuse;
- add `provenance_bytes` to `MemoryStats`; because the sidecar stays resident when a page is
  compressed, include it in estimated resident bytes;
- in `Limits.exceeded(.bytes)` and `growInternal`'s byte-recycle check, compare the
  overflow-checked sum `page_size + provenance_bytes` (plus any candidate page backing bytes)
  against the heuristic limit. Overflow means exceeded; never wrap;
- keep `createPageExt` and `PagePool.item_size` accounting strictly about raw Page backing.

Node/allocator headers remain under the same exclusions as current memory statistics unless
an existing allocator-accounting hook can measure them. Release external buffers with the
owning node, including pages destroyed while reflowing or returned to the pool.

The live-retention limit is a heuristic with deferred byte enforcement. `PageList.resize`
ends by enforcing `.lines` only (`src/terminal/PageList.zig:1368-1372`); existing byte
checks are at `setMaxBytes` (`3977-3982`) and the `growInternal` recycle check (`4067-4069`).
Keep `page_size` raw and add `provenance_bytes` only to those existing byte-enforcement/check
points. Do not add `.bytes` enforcement to resize or silently change which resize-time history
is retained. Tests must assert that a post-resize combined-byte overage is permitted until the
next existing byte-enforcement point, then assert that normal eligible-page pruning applies
the combined `page_size + provenance_bytes` budget (subject to active-area minimums) and updates
the floor. Separately measure peak old pages + destination pages + sidecars during reflow; do
not promise a hard transient 32 MiB ceiling or convert the measured peak to a new cap.

Before a crossing commits, reserve its provenance capacity. If normal retention pruning is
part of making room, remove old history spans and advance floor/generation exactly; if capacity
still cannot be obtained, fail that crossing before tagging the row, incrementing the odometer,
or growing history. In an isolated sidecar-OOM test with no prune, assert `vt_processing_error`
and unchanged history snapshot; in a pressure/prune case, assert the snapshot reflects the
completed prune but not the failed crossing. The journal refusal path is not a provenance
failure: if the row crosses, odometer and provenance still advance while the journal reports
its refusal.

For resize, reserve sidecar capacity before the PageList commit point and preserve the
existing `Screen.resize` unchanged-on-error contract. The width-only PageList violation is
tracked by `nocx-zg3k3.5.14`; the distinct combined width+height journal-aware failure is
`nocx-zg3k3.5.15`. Both are prerequisites; do not add another rollback policy here. The C
history getter has no provenance-error status, so a sidecar OOM must not leave it serving a
stale floor. Add no C result field unless later source testing proves it necessary.

Transient resize allocation peak is not yet measured. The existing resize-OOM inconsistencies
are tracked separately. After both `nocx-zg3k3.5.14` and `.15` merge, rerun the width-only and
combined width+height failure tests, then measure old-plus-new page and sidecar peak separately
from the steady-state retention cap.

## Mutation and lifecycle table

| Operation and hook                                                                                                    | Provenance action                                                                                                                                                                                                                  | Odometer / floor / generation                                                                                                                                        | Acceptance check                                                                                                    |
| --------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| Retained primary scroll: `PageList.growAndCaptureDeparture` / `growInternal`; `Screen.cursorDownScroll`               | Assign the next ordinal to the departing row before it can be recycled or pruned.                                                                                                                                                  | O +1; floor reflects the post-retention span set; generation changes only if layout/prune requires it.                                                               | Row content and span ordinal agree, including page-recycle path.                                                    |
| Zero-scrollback in-place scroll: `Screen.cursorDownScroll`, `cursorScrollRegionUp`                                    | Journal the crossing; no retained-history span survives.                                                                                                                                                                           | O +1; with no tagged history, floor=O; no dropped journal entry is hidden.                                                                                           | Zero-retention capture and row-region paths; alternate screen excluded.                                             |
| Primary row-count shrink, including while alternate is active: `resizeWithoutReflowAndCapture` / `captureHistoryRows` | Tag rows that actually cross into primary history exactly once, before line enforcement.                                                                                                                                           | O + per crossing; generation follows layout change; page pruning may advance floor.                                                                                  | Shrink during alternate screen emits once; return to primary emits nothing twice.                                   |
| Width reflow: `resizeCols`, `ReflowCursor.copyRun/writeCell`                                                          | Split/merge spans with copied cell intervals; active-only reflow-born history stays untagged; remove any origin fragment moved into ACTIVE.                                                                                        | O unchanged; generation +1; floor unchanged if all tagged HISTORY fragments remain; may advance when the final fragment leaves HISTORY, even if cells remain active. | Preserved widening repro; boundary crossing; untagged active-only history.                                          |
| Active-area refill: `resizeWithoutReflow` when history is consumed                                                    | Retire origins for fragments that leave history; if later departed, assign a new ordinal, never stack origins.                                                                                                                     | O unchanged on refill; generation +1; floor recomputed only from remaining history spans.                                                                            | Refill, partial span across history/active, later departure gets a fresh ordinal.                                   |
| Eligible line/byte prune: `Limits.enforce`, `erasePage`, `growInternal` recycle                                       | Remove sidecar for deleted rows/nodes; compute the first still-retained ordinal. Combine raw and sidecar bytes only at existing `.bytes` checks (`setMaxBytes:3977-3982`, `growInternal:4067-4069`); resize remains `.lines`-only. | O unchanged; floor advances to exact prefix; generation +1.                                                                                                          | Both preserved red tests and deferred-overage/next-byte-enforcement test.                                           |
| ED3 / partial history erase: `PageList.eraseHistory` and C `CSI 3 J` path                                             | Remove affected history spans; ED3 removes all history spans.                                                                                                                                                                      | O unchanged; ED3 floor=O; generation +1.                                                                                                                             | Partial erase preserves later spans; ED3 clears floor exactly.                                                      |
| Reset/deinit/recycle                                                                                                  | Release spans and reset caches; no tag survives into reused page/node.                                                                                                                                                             | Reset does not increment O; floor follows remaining/cleared history rule; generation invalidates old coordinates.                                                    | Reuse node/page after reset, no stale origin.                                                                       |
| Split/compact/compression within a live `PageList`                                                                    | Move/remap sidecar ranges with the same row mutations; compressed nodes retain external sidecars.                                                                                                                                  | O/floor/generation unchanged except for an explicitly lossy row removal.                                                                                             | Split/compact and full/incremental compression preserve exact spans without restoring pages just to read the floor. |
| Detached `PageList.clone` (`src/terminal/PageList.zig:1102-1236`)                                                     | Clone row cells into new nodes but drop/rebase origin tags; the detached list has no copied `Departures` clock.                                                                                                                    | New PageList generation starts at zero and has no odometer; any fresh enclosing C terminal starts its own O/floor at zero.                                           | Clone tagged history and verify no source ordinal leaks into the detached clone.                                    |
| Snapshot getter                                                                                                       | Copy all scalars under the documented caller-serialization contract.                                                                                                                                                               | No mutation.                                                                                                                                                         | Over-the-real-C-seam scalar snapshot; no concurrent same-handle C test.                                             |

The table is a map of current sites, not permission to leave an unenumerated mutation path
untested. Before implementation, search for every operation that moves, clears, splits, or
reuses rows and add it to this table.

## Red/green acceptance tests

Keep the two current fork-tree red tests as the first tests; do not edit them to match the
implementation:

1. **Widening preserves all origins.** Distinct ten-cell markers at width 10 give O=10,
   H=10, floor=0. Widen to 20 gives O=10, H=5 with every marker still present, including
   the oldest marker, which remains in HISTORY; expected floor=0 and generation increases.
   Current code incorrectly returns floor=5.
2. **Narrow + actual page-granular partial prune.** Distinct markers retain original ordinal
   provenance through split rows. Current run's marker-derived expected floor is 12,329,
   while `O - physical_rows` returns 9,157. The test adapts to the actual minimum page limit;
   it must not fake a below-minimum 10-row cap.

Add red/green tests for:

- partial physical-row pruning inside a marker: floor stays before the ordinal while any of
  its history span remains, then advances after the last span is removed;
- active refill then redeparture: old history origin retires; new departure gets one new
  ordinal and no layered tags;
- width reflow that moves tagged fragments across the history boundary: floor holds while
  any fragment remains in HISTORY, then may advance when its final fragment enters ACTIVE;
- active-only reflow-born history stays untagged and leaves `O=0,H=2,floor=0` valid;
- clearing cell values in a retained blank row preserves its ordinal; deleting the row or
  refilling it into ACTIVE removes the history token. For `ReflowCursor.reflowRow`'s deferred
  blank rows (`src/terminal/PageList.zig:1729-1755`), a tagged blank between nonblank rows is
  queued and attached to the emitted destination row; a tagged trailing blank dropped by reflow
  is retired because no destination row materializes. Cover blank wrap continuations too;
- page split/compact/reuse and compressed-page round trips preserve exact ranges and update
  node/global caches. A detached `PageList.clone` (`src/terminal/PageList.zig:1102-1236`)
  creates new nodes, resets generation, and omits `departures`, so it drops/rebases source tags
  rather than claiming to preserve O/floor/generation. Only a complete live C-terminal clone
  that also copies sidecars and the matching `Departures` clock may retain them;
- alternate-screen resize captures hidden-primary shrink once; alternate scroll itself never
  gets a primary departure;
- global-floor cache updates after each mutation. Per-node minima alone do not make the global
  snapshot O(1); any floor increase must be resolved during the mutation, not by scanning pages
  in the getter;
- sidecar accounting keeps `PageList.page_size` and `MemoryStats.raw_bytes` raw, reports
  allocated capacity in `MemoryStats.provenance_bytes`, includes resident sidecar bytes in
  estimates, and makes the checked combined byte sum drive retention at the existing byte
  enforcement points. After resize, assert the permitted deferred combined-byte overage; at the
  next existing byte enforcement, assert eligible-page pruning and the resulting exact floor.
  Do not add byte enforcement to resize;
- force OOM at a departure-sidecar preflight at `PageList.growAndCaptureDeparture` /
  `Departures.captureTopRow` before crossing side effects (`src/terminal/PageList.zig` and
  `src/terminal/departures.zig`). Assert the pending departure-journal state and O are unchanged,
  as are history rows, provenance, floor, and generation. In the same C `vt_write`, send later
  printable input and assert sticky `vt_processing_error` plus cursor progress; this proves
  action-local failure, not whole-feed rollback
  (`src/terminal/stream_terminal.zig::Handler.vt`);
- after both `nocx-zg3k3.5.14` and `.15` merge, force OOM through the C resize seam at each
  allocator failure point in both width-only and combined width+height paths. Assert C
  `.out_of_memory`, the `Screen.resize` unchanged-on-error state, and journal consistency with
  the pre-call screen (no lost, duplicate, or phantom pending departure/odometer effect). Keep
  direct regressions for the `.14` `PageList.resizeCols` post-mutation failure and the distinct
  `.15` journal-aware `cursor.y < pages.rows` failure;
- `history_snapshot` returns rows, total, floor, generation, and odometer from one serialized
  call; C API caller-serialization is documented in the header;
- record transient old+new resize peak separately from the steady retention cap. Run the same
  64 KiB feed-ingest and width-reflow resize benchmarks in ReleaseFast on the pre-sidecar
  baseline (`538b5f36bcd84157782fc8a0eddee5677c29d284`) and sidecar build; gate both against
  the existing stage performance budget in spec §9, without inventing a new threshold;
- later `nocx-gomch` test pages `O=0,H=2,floor=0` using physical, generation-local cursors,
  not the ordinal floor.

The Go-port concurrent `Ingest`/`Resize`/ED3 versus `HistoryRows` test remains in the later
`nocx-nv3in` leaf and uses Go's existing mutex; it is not a same-handle C race test. Correcting
physical pagination and stale-generation cursors for untagged reflow-born history belongs to
the later `nocx-gomch` wire/page leaf.

## Execution slices and worktree boundaries

This is a high-risk PageList/storage change. Plan **four sequential fork implementation
sessions after both `nocx-zg3k3.5.14` and `.15` merge and this plan is accepted**, plus later
Go-port work. Keep fork implementation in one worktree through acceptance: the slices collide
in `PageList.zig`, `page.zig`, and `departures.zig`; do not have two workers edit those files
concurrently.

1. **OOM/accounting preflight (one design session).** Wait for both `nocx-zg3k3.5.14` and
   `.15` to merge, then rerun the filtered C resize fault tests for width-only and combined
   width+height paths against `Screen.resize`'s unchanged-on-error contract and verify journal
   consistency. Confirm sidecar-capacity accounting and the steady/transient budget contract.
   The provenance and pagination semantics above are already specified; no new whole-feed
   rollback is assumed.
2. **Sidecar storage/accounting (one implementation session).** Add Node-owned storage,
   capacity accounting, memory statistics, allocation/failure behavior, and lifecycle tests
   for deinit/recycle/compress/compact plus detached-clone tag dropping. Keep empty-sidecar
   nodes allocation-free.
3. **Origin assignment and transport through layout changes (one implementation session).**
   Tag actual departures and carry range slices through `PageList` reflow/copy/resize and
   in-place split/compact paths. Detached `PageList.clone` drops/rebases tags; only a future
   complete live C-terminal clone that also clones the matching `Departures` clock may retain
   them. Cover primary, no-scrollback, and alternate-screen resize hooks.
4. **Retirement and public floor (one implementation session).** Remove tags at active refill,
   prune/partial erase/ED3/reset; update the global exact-floor cache during each mutation; expose
   the one C snapshot and finish red/green, OOM, retention, and ReleaseFast benchmark gates. Do
   not repin the release here.
5. **Later Go-port work in separate leaves/worktrees.** `nocx-nv3in` owns the
   mutex-serialized concurrent `HistoryRows` acceptance test. `nocx-gomch` owns physical
   pagination and stale-generation cursors, including the untagged `O=0,H=2` case. This plan
   does not wire the Go port or update the release archive.

The four fork sessions are rough planning units, not a promise of elapsed hours. Main risks
are sidecar invariants through every row mutation, per-node capacity accounting and reflow
peak memory, OOM staging, and preserving the current `copyRun` fast path. Budget one session
per slice with a reviewer who did not author the provenance logic; if any slice does not fit,
stop at a compiling boundary and hand off rather than splitting conflicting worktrees.

## Explicit exclusions

- No contract weakening, `odometer - physical_rows` fallback, or per-row minimum shortcut.
- No Go-port wiring, physical pagination, or wire-contract changes in this fork plan; no
  release repin, archive rebuild, or manifest edit. Later paging work is `nocx-gomch`.
- No new C ABI fields for a physical cursor or provenance-error status unless source testing
  proves the existing snapshot/write/resize signals insufficient.
- No C mutex and no same-handle concurrent C test; caller serialization is the contract.
- No PR merge or close. PR #2 stays open and blocked until the provenance behavior and checks
  are accepted.
- No provenance implementation until both `nocx-zg3k3.5.14` and `.15` merge and the owner
  reviews this plan. No additional product-semantic choice is currently open; the two tracked
  resize-OOM fixes are the blockers.
