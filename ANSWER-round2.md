# Round 2: answers and convergence

## 1. Point by point

### 1. History is already addressable: agree

The active-area-only rule is ours, not libghostty's. `point.h:32-38` gives the
coordinate a `uint32_t y`, expressly allowing screen/history coordinates beyond
one page; `point.h:47-58` defines ACTIVE, VIEWPORT, SCREEN and HISTORY.
`terminal.h:2330-2358` accepts all four. Our `nocxGridRefAt` selects ACTIVE and
narrows `y` to `uint16_t` at `internal/emulator/ghostty/bridge.c:228-235`.

The corrected cost is:

- There is **no 20.1-50.4 MiB retained Go copy**. libghostty retains the history;
  Go can hold only the requested page and discard it after encoding.
- Set both libghostty limits. The byte limit is approximate to one roughly
  400 KiB page (`terminal.h:1358-1378`); the physical-line limit can exceed its
  setting by dozens to roughly one hundred rows (`terminal.h:1380-1402`). With
  settings of 8 MiB and 10,000 physical rows, the first limit reached wins. That
  bounds libghostty's scrollback allocation near 8 MiB or 10,000 rows; it does
  not add a same-sized Go history.
- A 43x132 request contains 5,676 cells. If it is materialized using today's
  shallow 40-byte Go `Cell`, the temporary page is about 222 KiB plus grapheme
  backing strings. A C row encoder can instead stream directly to the bounded
  page body and avoid that materialization. In either case the retained Go cost
  is zero after delivery.
- HISTORY/SCREEN lookup may traverse the complete page list
  (`terminal.h:2338-2346`). Calling it once per cell would replace the memory
  error with an avoidable CPU error. Resolve a row once and traverse/encode it
  inside the C bridge.

**Result:** add HISTORY/SCREEN reading to the emulator adapter. It is the
recovery/read primitive for the producer, not a second product store. My first
Option 1 memory figure is withdrawn; it priced a representation the option did
not require.

### 2. A tracked reference is an anchor, not a scroll-off signal: disagree

The useful part of the proposed reading is correct: a tracked reference follows
scrolling, pruning and resize/reflow (`grid_ref.h:46-50`,
`terminal.h:2360-2369`), and converting an active cell to HISTORY later tells us
that this particular cell scrolled off (`terminal.h:2392-2407`).

It is not an event stream:

- Reset, pruning or another discard can destroy the semantic location. Then
  `has_value` is false and point/snapshot return `GHOSTTY_NO_VALUE`
  (`grid_ref.h:52-58`; `grid_ref_tracked.h:39-51,66-82,110-133`). That reports
  loss; it cannot return the lost row.
- Resize/reflow updates the reference when a meaningful mapping survives
  (`grid_ref.h:48-54`). Therefore a ref is suitable for finding an anchor after
  reflow, but a physical HISTORY row count is not an append cursor across that
  mutation.
- An alternate-screen switch does not retarget it. The ref remains owned by
  the screen/page-list on which it was created and conversions continue against
  that owner (`grid_ref.h:65-70`; `grid_ref_tracked.h:61-69`). Calling `set`
  explicitly moves it to the currently active screen
  (`grid_ref_tracked.h:84-108`).
- A ref for every row visible before `vt_write` still misses a row that is
  created, reaches the screen, and scrolls into history during that same call.
  No code can pin that row between libghostty's internal mutations.

The headers publish no byte or asymptotic cost per ref, so a numeric cost would
be invented. They do say each ref is allocated, must be freed, and adds
bookkeeping to terminal mutations; the instruction is to use them sparingly for
long-lived anchors (`grid_ref.h:72-90`). Ten thousand refs are therefore the
wrong design. One producer anchor is appropriate. A bounded, temporary set of
viewport-height refs around one resize may be acceptable, but must be measured.

**Result:** use one tracked ref for the last harvested semantic position and
for loss detection. Do not use one per retained row and do not treat refs as
the journal producer.

### 3. Reading and recording are layers: agree; count-after-Ingest is not sufficient

I agree with the layering. Libghostty HISTORY answers what remains scrollable
in the live terminal. The durable journal answers what survives the runtime and
what cards and live scrollback share.

The library supplies cheap observations, not a stable append cursor:

- `GHOSTTY_TERMINAL_DATA_SCROLLBACK_ROWS` is the current physical row count
  (`terminal.h:1679-1691`). Scrollbar state is maintained amortized O(1), and
  the documented integration is to poll once per frame/write batch because
  there is deliberately no notification (`terminal.h:1619-1635`).
- A single tracked anchor plus the current count can find the retained suffix
  after an ordinary write, even if pruning kept the count unchanged: convert
  the anchor to HISTORY and read rows after its new `y`.
- The value is not monotonic across prune, reset or resize/reflow. More
  importantly, one `ghostty_terminal_vt_write` can scroll several rows and then
  reset the terminal in the same byte batch. After the call the count is zero
  and the anchor has no value. The observations prove that rows were lost but
  contain none of their cells. The same class exists for output that enters and
  leaves an alternate screen within one call. Our `Ingest` feeds a no-fence
  chunk in one `vt_write` (`internal/emulator/ghostty/terminal.go:296-325`).

Therefore polling after every `Ingest` is a good fast path and recovery check,
but it cannot produce a complete journal. It can only produce a journal with an
explicit gap whenever the anchor disappears. A normal RIS/reset must not create
a data-loss gap in an otherwise complete local session.

**Result:** add a pinned libghostty drain API, not one callback across CGo per
row. Internally it queues semantic rows when they leave the primary active area
and retains that queue through reset/prune until the embedder drains it. After
each `vt_write`, nocx drains the batch while holding the existing terminal lock.
HISTORY plus one tracked anchor remains the reconciliation/recovery path. The
drain must be bounded; overflow produces a first-class gap and completeness
transition.

### 4. The frozen channel does not carry pages: disagree with reusing it; a versioned change is needed

`TypeSessionData` is specifically raw PTY bytes. Its fixed header is session,
subscriber and lease epoch, followed immediately by payload
(`internal/helper/proto/session_frame.go:8-17,65-88`). It deliberately has no
inner version or message type (`session_frame.go:34-46`). The host merely routes
those bytes (`internal/helper/host/service.go:33-37`) and
`SendSessionData` always emits that type (`internal/helper/host/host.go:314-325`).
A semantic page cannot be put there without changing the meaning of the frozen
ABI.

The general host does have two relevant mechanisms:

- a helper can send a small, unsolicited, ordered `TypeNotify`
  (`host.go:369-378`; `proto/session_service.go:222-262`);
- an ordinary coordinator request receives a response, and responses over 1
  MiB are already split into a sentinel plus `TypeChunk` frames
  (`host.go:572-630`). Reverse requests also exist
  (`host/reverse.go:42-120`), but requests themselves are not chunked
  (`host/reverse.go:85-92`).

Chunked JSON could technically carry a base64 page. It is not the selected
mechanism: ADR-0066 keeps terminal frames on the binary data plane
(`docs/architecture.md:115`), and history rows are terminal presentation data,
not a small ledger fact.

The exact helper ABI change is protocol **16**:

- allocate `TypeJournalData = 13` in `internal/helper/proto/frame.go` and add it
  to the closed set at `frame.go:75-83`;
- add a separate `JournalFrame`, leaving `SessionFrame` byte-for-byte frozen.
  Its binary header is
  `[session:16][page-id:16][revision:u64][from:u64][through:u64][flags:u8]`,
  with integers big-endian, followed by canonical journal records. `flags`
  contains `final`, `more` and `gap-before`; no field is overloaded with PTY
  meaning;
- add session ops `journal-read` with
  `{session,pageId,after,maxRows,maxBytes}` and `journal-ack` with
  `{session,through}`. A read writes one or more bounded `TypeJournalData`
  frames keyed by `pageId`, then returns a small control result
  `{through,more,completeness}`. An ack is accepted only after the coordinator's
  durable transaction;
- add the small `journal-advanced` notification
  `{session,through,revision}`. It wakes the puller but carries no rows, so
  reconnect does not depend on having received it.

`proto.Version` covers frame types, ops and result shapes, and requires a bump
for any published change (`internal/helper/proto/version.go:3-29`); the current
value is 15 (`version.go:234`). New generations may coexist precisely because
of that bump. This is not an in-place extension justified by `unknown_op`: the
new frame type is necessary, and an old decoder treats unknown types as garbage
(`proto/frame.go:44-52`).

**Result:** this does not block the design, but it is the first implementation
stage. No field is added to `ScreenFrame`, and no semantic page is smuggled
through `SessionFrame`.

### render.h reading: partly agree

Confirmed:

- Render state is the visible viewport and DIRTY means redraw state, not a row
  eviction (`render.h:22-34,54-69`; `render.h:232-249`). It cannot be the
  scroll-off producer.
- The row-cells handle is preallocated and reusable (`render.h:701-720`).
  `HAS_STYLING` avoids fetching a full style for unstyled cells
  (`render.h:772-777`), `GRAPHEMES_UTF8` avoids the Go codepoint loop
  (`render.h:779-790`), and row-local selection avoids one selection query per
  cell (`render.h:762-770`). These are the right primitives for the live-frame
  reader.
- `CELLS_RAW` is the actual single-call borrowed whole-row view, and upstream
  explicitly warns that its bit positions are not ABI-protected and C-header
  callers should use `ghostty_cell_get` (`render.h:251-265`). We should not
  persist or decode those private bits.

One correction: `ROW_DATA_CELLS` populates a reusable **iterator**, not a caller
array containing the whole decoded row. The caller still advances with
`ghostty_render_state_row_cells_next` and queries cell values
(`render.h:701-720,794-806,825-845`). Used directly from Go, that is still a
CGo crossing for each advance/query. The efficient implementation is a nocx C
bridge that loops with the supported getters and writes the public canonical
row representation before crossing into Go once per row/batch.

Selection does not enter the journal: it is transient client/render state. The
render API changes the live-frame extraction task and supplies reusable cell
conversion machinery; it does not change journal ownership, retention or the
need for the scroll-off drain.

## 2. The converged design

### Producer

1. Configure libghostty with both transient scrollback limits: 20,000 physical
   rows and 16 MiB. This is a two-times staging margin over the product journal,
   not product retention. The page-granularity qualifications above still
   apply.
2. Extend the pinned library with a bounded semantic-history drain. It records
   every primary-screen row segment at the mutation that removes it from the
   active area, before reset/prune can erase it. It exports public cell facts,
   wrap/continuation and hyperlink facts, never raw `GhosttyCell` bits.
3. After every `vt_write`, under `terminal.mu`, drain all queued records. Query
   `SCROLLBACK_ROWS` and reconcile the single tracked anchor. A mismatch creates
   a gap; it never silently invents an empty interval.
4. Before resize, drain first and snapshot the active rectangle. After resize,
   start a new geometry epoch and use a bounded temporary set of active-row
   anchors to account for lines moved by reflow. Reset creates a journal
   boundary. Alternate-screen intervals retain opening/closing snapshots, not
   invented line scrollback.
5. The session owner assigns monotonic journal cursors and the runtime revision,
   appends the batch to a crash-safe helper delivery spool, then publishes the
   corresponding live frame/revision. A frame may not advertise revision R
   until every journal record through R is in that spool.

The helper spool is transport staging, not a second product reader. It exists
until a coordinator durably commits and acknowledges its cursor; process death
or coordinator replacement therefore does not turn an already-produced row
into an unreported hole.

### Stored record and bounds

`internal/content` gets one canonical journal in place of `session_output`. A
record contains:

- monotonic journal cursor and runtime revision;
- primary/alternate screen identity and geometry epoch;
- a runtime-issued logical-row id plus physical segment order;
- cells as UTF-8 grapheme, width/continuation, public style/color facts and
  hyperlink target;
- wrap/continuation and semantic-row facts;
- boundary records for resize, reset, alternate-screen intervals and closing
  snapshots;
- explicit gap range/reason and completeness.

Selection and libghostty's raw packed cell/row values are absent. Cards store
cursor intervals and projections, not copied output.

Retain the newest contiguous **10,000 logical rows or 8 MiB encoded per
session**, whichever comes first. Eviction removes the oldest logical rows and
advances the journal base with `evicted` completeness. The helper spool uses
the same semantic base and is separately bounded at 20,000 logical rows or 16
MiB. If an unreachable coordinator lets it overflow, it records a transport
gap before discarding. Batches and page bodies are capped at 256 KiB.

### Readers and contracts

- Cards query journal intervals in `internal/content`.
- Live scrollback asks the coordinator for a page ending before a stable journal
  cursor. At the tail it joins the active `session.frame` at one runtime
  revision. New output does not move a reader's historical anchor.
- Frontend control JSON-RPC adds a small `session.history.read` request carrying
  `{sessionId,pageId,before,maxRows,maxBytes}`. The result metadata is small;
  the page itself uses a new binary data-plane `history-page` message keyed by
  `pageId`, with the same canonical journal body. This leaves
  `contracts/session.frame.schema.json` active-area-only.
- Remote/helper delivery uses protocol 16's `journal-advanced`, `journal-read`,
  `TypeJournalData` and `journal-ack` described above. The coordinator commits
  the same canonical body before acking it.

Implementation stages are therefore separable: (1) public row encoder and
render-state frame reader; (2) pinned drain plus HISTORY/anchor reconciliation
and mutation adversaries; (3) helper spool and protocol 16; (4) content journal
and `session_output` removal; (5) frontend history-page data frame and DOM
projection; (6) make cards read journal intervals and remove their old output
source.

## 3. Where we still disagree

1. A tracked ref is not a scroll-off signal. It is an anchor that can report
   movement or loss; it cannot report the contents it lost, nor rows created and
   destroyed within one library call.
2. HISTORY does not grow monotonically in the sense a journal requires.
   Physical counts change under reflow and pruning, and reset can erase an
   intermediate suffix before the post-`Ingest` observation.
3. Consequently, count plus cursor after `Ingest` is insufficient for a
   complete journal. It is sufficient only for a best-effort journal that marks
   ordinary terminal resets as data loss. I do not accept that product
   behaviour.
4. `ROW_DATA_CELLS` is not a whole decoded row in one call. It initializes a
   reusable iterator. The one-crossing result requires our own supported C
   bridge; `CELLS_RAW` is not an ABI-safe shortcut.
5. The existing session channel cannot carry semantic page data. The generic
   chunked response path can carry bytes encoded in JSON, but using it for
   terminal presentation would reverse ADR-0066's binary data-plane decision.

## 4. What is still unverified

- The headers give limit granularity, not measured resident memory or mutation
  cost for this pinned build. The 16 MiB/20,000-row staging settings and the
  viewport-height temporary tracked refs need an RSS/latency measurement.
- No header promises the ordering and payload lifetime of the proposed drain;
  it is new pinned-library ABI and needs adversarial tests for multi-scroll plus
  RIS in one write, prune-at-cap, resize/reflow, alternate-screen enter/exit,
  hyperlink/grapheme rows and allocation failure.
- The exact resize mapping from pre-resize physical segments to logical-row ids
  is not supplied by the public API. The temporary-ref algorithm must be
  prototyped before its journal schema is frozen; failure must produce a
  boundary/gap, not duplicate rows.
- `snapshot.h:23-34,79-113` confirms that full snapshots include history and
  unfinished parser state, but format v1 has no binary compatibility guarantee.
  It is useful for a checkpoint experiment, not the journal encoding.
- Crash durability policy is not measured. “In the helper spool” must be given
  an explicit transaction/fsync rule and a test that kills the helper between
  append, notification, coordinator commit and ack.
- The 256 KiB canonical page cap has not been tested against pathological
  graphemes, styles and hyperlink targets. The encoder needs a single-record
  oversize rule before the wire contracts are frozen.
- Protocol 16's coexistence/deployment path follows the existing generation
  design, but no implementation or compatibility test exists yet. In
  particular, the new binary type needs literal-byte golden vectors like the
  frozen session frame.

WORKER_DONE::sbk2-3c7dc0063a01
