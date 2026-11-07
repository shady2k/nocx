# Prepare fidelity-preserving fixes for active-output tabs

- **Status:** supporting preparation, proposed for review; no optimization implemented.
- **Owner request:** 2026-11-07, “проведи подготовку к исправлению и сделай pr в репе”
  (prepare the fixes and open a pull request).
- **Preparation leaf:** `nocx-3an1s.1.11`, serving busy-terminal O1 under
  `nocx-3an1s.1`. This PR does not accept or close that stage.
- **Base:** `cc53f3b844468c58320f3aad2d6126ee7faddab3`; investigated product paths
  are unchanged from measured revision `f49860272193af2bce04745c691b0ad621252092`.
- **Evidence:** [diagnostic report](../../docs/research/2026-11-07-busy-terminal-performance.md)
  and [portable measurements](../../docs/research/2026-11-07-busy-terminal-measurements.json).
- **Admission:** the five follow-ons below are tracked **deferred**. The current
  finding budget remains 12/12; preparation does not authorize implementation,
  raise that budget, supersede an ADR or approve a merge. The owner reviews the
  plan and selects/adopts implementation work separately.

## Problem

Developers running several programs that continuously print or redraw pay for
work unrelated to what changed: full active-grid extraction, full retained-output
transfer/parse/reconciliation, and fresh immutable cell arrays. Hiding a tab
suppresses painting, not all upstream or model work. These costs compete with
input and foreground updates.

The diagnostic distinguishes these costs from previously fixed wholesale DOM
rebuilds, hidden painting and synthetic focus intents. It also distinguishes
native working-set peaks from a proven leak: fresh unmodified controls stabilized;
the earlier larger ramp still has no demonstrated owner.

## Solution and outcome frame

**For:** developers with several tabs actively producing screen output.
**Why:** retain a usable foreground terminal while reducing redundant CPU and
allocation work. **Priority:** identical terminal facts, durable output and
selection behavior before speed. No output may disappear to improve a benchmark.

The main user path is to run scrolling commands and full-screen programs in
several tabs, type in a foreground pane, inspect older output, activate a hidden
tab, and read/copy completed output after reconnect. The success signal is less
measured redundant work with the same accepted output and no unexplained input,
completion or memory regression. Preparation supplies bounded fix scopes and the
checks that decide those claims, not a new numerical product SLA.

This is not a universal refactor. Prefer small changes inside existing owners;
keep contract-changing incremental history behind its own design proof. Keep the
RAM investigation separate from optimizations that merely allocate less.

## Binding decisions, before any implementation

The current [architecture](../../docs/architecture.md) owns AD-1, AD-6, AD-8,
AD-9 and AD-10. Read their amendments, not their superseded original wording.

| Constraint                                                                                                                          | Existing decision                                                                                                                                                                                              | Required check                                                                              |
| ----------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| One backend VT authority; binary screen/input carrier, typed control facts                                                          | AD-1/6/8; [ADR-0066](../../docs/decisions/0066-one-emulator-and-it-is-the-backends.md)                                                                                                                         | Existing runtime/frame and real-wire checks; no new parser or raw-byte sniffing             |
| Live snapshot is not the durable execution record                                                                                   | [ADR-0072](../../docs/decisions/0072-xterm-is-removed-first-and-the-interval-record-follows-the-cell-model.md)                                                                                                 | Old snapshot selection remains stable; completed/hidden command rows remain durable         |
| Frame/session/subscriber/revision identities and bounded continuation stay intact                                                   | [ADR-0073](../../docs/decisions/0073-the-screen-frame-is-keyed-by-its-session-and-its-reader-and-continues-on-its-own-carrier.md)                                                                              | Real-wire frame checks; no new frame carrier or mixed-revision assembly                     |
| Ingestion and durable facts remain lossless within their declared loss policy; visual frames may coalesce; effects are not repeated | AD-10; [ADR-0075](../../docs/decisions/0075-an-overflowing-row-stream-ends-the-block-in-flight-incomplete.md)                                                                                                  | Overflow closes the correct block incomplete; effects and lifecycle ordering remain correct |
| Reconnect changes no command lifecycle; live and durable history use different coordinates                                          | AD-9; [ADR-0076](../../docs/decisions/0076-the-coordinator-going-away-changes-no-block.md), [ADR-0078](../../docs/decisions/0078-the-terminals-history-has-two-tiers-and-the-live-one-is-the-emulators-own.md) | Restart/reconnect, explicit loss, retained head/tail and final-close scenarios              |
| Synchronized output withholds frames, not replies, effects or departed rows                                                         | [ADR-0094](../../docs/decisions/0094-a-synchronized-output-hold-withholds-the-frame-and-fails-open.md)                                                                                                         | Hold/release/fail-open and mid-hold attach/resend remain unchanged                          |
| Output and buffer switches cannot change desired geometry or input ownership                                                        | [ADR-3kqrem7v](../../docs/decisions/3kqrem7v-a-running-program-keeps-one-header-and-one-grid-across-screen-modes.md)                                                                                           | No output-driven resize/focus; same running header/grid and selection gestures              |

Retention settings, final cumulative credit and durable confirmations stay where
they are. The milestone explicitly excludes the bulk terminal carrier. No
accepted decision is edited by this preparation.

## Inventory: extend these owners

| Job                              | Existing owner and reachability                                                                           | What is already delivered                                                                                                              |
| -------------------------------- | --------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| Ingest and publish active screen | `internal/sessionruntime/runtime.go`: `Ingest` → prefix refresh and frame publication; `reflow_window.go` | Alternate prefix skip (`nocx-vekk3`), no unused text extraction (`nocx-aknvn`), synchronized-output hold                               |
| Extract styled cells             | `internal/emulator/ghostty/terminal.go`: `Row` / scalar `Cell`; private `bridge.c` bulk history traversal | Departed/history bulk read (`nocx-zg3k3.12`); active rows still use per-cell CGO                                                       |
| Read retained output             | `restore-client.ts` → `LedgerOperation` → ledger handler → content `Artifact`                             | Shared in-flight read, per-entry refresh coalescing and final follow-up                                                                |
| Accept and paint stored rows     | `BlockManager.applyStoredRows`, pending map, `block-rows.ts` painter                                      | Preserved DOM and hidden-paint deferral (`nocx-aqrem`), element-owned selection storage                                                |
| Accept full live frames          | `cell-model.ts`: `createCellModel().apply` → existing painter/selection                                   | One row-position decode (`nocx-3zuaw`), immutable snapshots, hidden model acceptance; painter metric-identity fix is already delivered |

The source seams are linked by existing production callers, not unwired packages.
The new work does not duplicate the delivered optimizations. `nocx-n275f` already
made cap eviction one pass; the remaining scan is measured but not included here.

## Proposed work packages

These are preparation scopes, not a markdown task-status list. `br` owns their
state and assignments. P1–P4 have concrete implementation boundaries; P5 is a
bounded prerequisite design result, **not** a ready incremental implementation.

| Package                                       | Tracker           | Result                                                                    | Real prerequisite                                                                    |
| --------------------------------------------- | ----------------- | ------------------------------------------------------------------------- | ------------------------------------------------------------------------------------ |
| P1 — omit unneeded prefix capture             | `nocx-3an1s.1.12` | No prefix-only grid read when no pin needs it                             | Plan/admission; no other package                                                     |
| P2 — bulk active-row extraction               | `nocx-3an1s.1.13` | Existing row API extracts active cells through one bulk traversal         | Plan/admission; private bridge shape and long-grapheme contract settled in this leaf |
| P3 — do not settle pending-only output        | `nocx-3an1s.1.14` | An unrelated visible pane accepts pending rows without layout/follow work | Plan/admission; no other package                                                     |
| P4 — reuse immutable unchanged rows           | `nocx-3an1s.1.15` | Full-frame acceptance avoids rebuilding unchanged row storage             | Plan/admission; no other package                                                     |
| P5 — prove incremental retained-read contract | `nocx-3an1s.1.16` | One bounded ledger read/acceptance contract covering mutation and loss    | Plan/admission; must finish before its later implementation is decomposed            |

P1, P2, P3 and P4 have separate write scopes and can be worked independently;
priority is not a dependency. P5 can be designed alongside them. The coordinator
integrates and verifies combinations once; no worker runs the full product gate.
There are no artificial stage/root blocking edges. A future incremental-history
implementation consumes P5's contract, not P1–P4.

### P1: pin-demand guard, not a snapshot cache

Keep `Screen()` and successful `observeBufferLocked` before any new skip. Preserve
the alternate branch, including its retained primary eligibility. Inspect the
live observation **and every captured rendezvous**. On primary, if all
`OutputStartTrack` values are nil, clear their stale `OutputPrefixBasis` values
and return without `takeObservationScreenLocked`. Any nonnil pin retains current
validation; do not invent another eligibility algorithm for dead/off-screen pins.

Observable scenarios:

- No pins: publication still supplies the current frame; no stale prefix basis
  can later exclude new output. A disposable boundary counter establishes the
  removed prefix-only reads; it is not a permanent test of a private call graph.
- A parked captured rendezvous is the only pin holder: its prefix still receives
  validation and its returned interval excludes the correct old output.
- Primary → alternate → primary with identical text still invalidates an old
  conditional-input target; do not confuse unchanged cells with unchanged epoch.
- Rewrite above a pin, including below a parked cursor, then resize: the rewrite
  must not be blessed as an old prefix. Unchanged prefixes still repair correctly.
- Unreadable screen and mixed pinned/unpinned owners preserve the existing failure
  and successful-validation semantics. A failed read never fabricates evidence.

Do not combine this with observation/publication snapshot reuse. Only a final
sub-feed capture could plausibly be reused within one lock; earlier sub-feeds and
resize invalidate it, observation/frame error semantics differ, and publication
adds revision/mode/availability metadata. A synchronized hold also changes when a
frame may be published. That larger proposal is deliberately not one of these
implementation packages. `nocx-7epsi`'s hidden primary rewrite during a buffer
transition remains separate; it needs emulator observation, not byte sniffing.

### P2: extend the existing bulk bridge

`Terminal.Row` already has the required public shape. Generalize the private
history bulk traversal to the active coordinate space and move both consumers to
that single traversal. The upstream emulator ABI and public Go interface need no
new owner. Retire the superseded active per-cell **row** construction, but retain
scalar `Cell` for its consumers and an independent fidelity oracle.

Before coding, specify the private coordinate-tag contract and error behavior.
History's bounded retry/length assumptions must not silently narrow the active
path's current complete-grapheme behavior. Returned Go cells/strings remain owned
and stable after subsequent writes/resizes. Include erased RGB backgrounds,
default/palette/RGB colors, decorations, wide head/tail spacers, combining/ZWJ
clusters, wrap/continuation and whole long graphemes. Errors remain errors rather
than blank replacement rows. Preserve the existing departed-row traversal.

Acceptance compares active `Row` with scalar `Cell` across primary/alternate
screens, reflow and later mutation, plus existing departed/history fidelity tests.
Measure bridge crossings and allocations at 79x40 and 120x40 separately from the
full-app workload. Do not turn a component speedup into a whole-helper claim.

### P3: accept pending rows without settling another block

At **response application**, use the existing `blockForAttempt(entryId)` lookup.
Hidden panes and visible panes without a matching local block still call
`applyStoredRows`; they skip `settleAround` and `scrollToBottomIfFollowing`.
Matching visible blocks retain the existing path. Do not filter notifications
away: rows arriving before `bindAttempt` are legitimate, and the bounded pending
map already owns them.

Acceptance pairs the negative case with the positive one: unrelated growth and
close do not move/settle the visible pane; later binding paints the accepted
pending rows; matching visible growth still follows when requested; user
scroll-away, hidden activation and closing-row/agent completion remain correct.
This removes no artifact reads and promises no pending-memory reduction.

### P4: structural sharing inside the current cell model

Reuse already-immutable rows, not mutable arrays. Full validation precedes atomic
installation. Equality must cover text, marks, runs and every style field, wrap,
continuation, row position and relevant geometry/buffer facts. Own the comparison
facts: retaining a caller's mutable wire arrays can make an altered input appear
equal to a previously decoded row. Avoid serializing each row merely to compare
it; that would replace allocation with another allocation stream.

Keep bounded bookkeeping for the current model, not a history of frames. Start
with conservative invalidation across geometry/buffer changes. Always advance
revision, cursor and frame state on accepted newer frames, even when every row
can be shared. A rejected frame must update neither the installed snapshot nor
its comparison state. Palette/font/geometry paint invalidations remain the
painter's responsibility; row reuse cannot suppress them.

Acceptance exercises old pinned snapshots and selections, mutation of prior
wire inputs, style-only and width/mark changes, wrap/continuation, resize/buffer
switch, malformed later rows and stale revisions. Measure identical, sparse-row
and all-row-change cases; an all-changing frame must retain a bounded fallback.
The isolated freeze experiment is only evidence of a cost, not permission to
remove freezing or a forecast of full-redraw gains.

### P5: the missing durable-row contract

There is **no existing durable-row paging API**. `ledger.query.before` pages
entries; `OpenBlockRowsForSession.NextRow` is a writer/recovery cursor.
`StoredBlockRows.artifactVersion` currently carries artifact identity, not a
content revision. Append, recovered-head prepend, cap eviction and seal mutate
that same artifact. Current `get` plus `artifact` calls do not form one atomic
metadata/body read, and chunk sequences can be sparse, zero or negative.

Extend the existing content-ledger → capability → ledger-read-handler boundary.
A tail-only “rows after my last cursor” is insufficient. The prerequisite proof
must decide all of the following with fixture-based traces:

| Case                            | Contract obligation                                                                                                                     |
| ------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------- |
| Identity and mutation           | Separate artifact identity from ordered content/availability revision; last row, byte length and chunk maximum are not substitutes      |
| Initial read and continuation   | Atomic metadata/body state; concrete row and encoded-byte bounds; explicit handling of one oversized row                                |
| Continuous append plus eviction | A specified cross-page consistency strategy with finite progress, not indefinite restart whenever output advances                       |
| Recovered head                  | Deliver a prepend below the prior first row even if the last row is unchanged                                                           |
| Head/tail retention             | Remove evicted middle ranges while preserving protected head and newest tail; no contiguous-offset assumption                           |
| Seal/loss/truncation            | Accept metadata-only changes and final retained closing rows even with no row advance                                                   |
| Body retention or deletion      | Distinguish empty output, unavailable/evicted body, missing artifact/entry and read failure; late replies cannot resurrect evicted rows |
| Reconnect/rebind                | Define cursor validity after restart; reject responses from the old binding; coordinator disconnect never seals a block                 |

A source-level mismatch needs explicit treatment in the proof: retention can keep
artifact metadata while deleting body chunks, but the current artifact response
does not expose the internal eviction receipt. An empty body is therefore not a
sufficient new-protocol definition of “the command printed nothing.” This is a
contract hazard, not a separately reproduced UI failure claimed by this PR.

Choose numeric response limits and cross-page consistency from current wire/store
bounds and measured row sizes; they are **not decided here**. P5 ends with those
choices reviewed and finite mutation traces passing. Its eventual implementation
is one vertical delivery across repository read contract, SQLite transaction,
existing capability/handler, canonical schemas/generated types, restore-client,
revision-aware acceptance and painter/selection/final-close integration. Move all
affected callers and remove the obsolete live full-body refresh path in that
same delivery; retain full artifact reads only for genuinely distinct consumers.
Do not add a second carrier/cache or confuse live-history coordinates with durable
ones. No implementation leaf is presented as ready before this proof.

## Checks and places of observation

No new product UI is proposed. Observe existing terminal panes, copied selections,
completed command blocks, truthful incomplete/unavailable notices, and reconnect.
Permanent regression tests assert consumer-visible fidelity; throwaway counters
and profiles establish eliminated work. No wall-clock performance threshold is
added to ordinary unit tests.

| Scope | Existing seams to reuse                                                                                                            | Required additional scenario                                                                                 |
| ----- | ---------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| P1    | `internal/sessionruntime/output_prefix_identity_test.go`, reflow/geometry tests, `internal/helper/session` conditional-token tests | No-pin and captured-only-pin cases, stale basis, epoch changes, old-output exclusion                         |
| P2    | `internal/emulator/ghostty/departed_bulk_test.go`, `Row`/`Cell` interface                                                          | Active-row parity, long graphemes, ownership after mutation, error paths                                     |
| P3    | `frontend/src/terminal-content.test.ts`, `scrollback/blocks.test.ts`                                                               | Unrelated pending-only read does not scroll; later bind succeeds; matching growth remains positive           |
| P4    | `frontend/src/cell-model.test.ts`, `painter/selection-gesture.test.ts`, existing painter tests                                     | Prior input mutation, pinned snapshot/selection, metadata-only frame, invalid frame then valid reuse         |
| P5    | Content eviction/floor/unavailable tests; `ws_ledger_query_test.go`; restore-client, block-rows and terminal-content tests         | Atomic read under append/seal/eviction, prepend/holes, finite continuation, stale binding/revision rejection |

For implementation workers, run the unit tests of touched packages and the
production-API smoke before reporting. Examples (commands are **planned**, not
claimed executed by this preparation):

```sh
go test -tags gtk3 ./internal/sessionruntime ./internal/helper/session
go test -tags gtk3 ./internal/emulator/ghostty
go test -tags gtk3 ./internal/sessionruntime -run '^$' \
  -bench '^BenchmarkIngestCapture$' -benchmem -benchtime 10x
(cd frontend && npx vitest run src/cell-model.test.ts)
```

Use the existing Vitest configuration, not a new runner.
Implementation-specific filters should preserve the positive
and failure cases above. Existing `TestIngestCaptureStaysWithinItsBudget` is an
allocation guard, not a native multi-tab responsiveness proof.

The integrating coordinator alone runs `make ci-full` on the merged product tree
and the real native comparison in the diagnostic report. Use disposable HOME and
normal containerized suite runners; do not run workers' full gates concurrently.
Exercise 1/2/4/6 active producers, scrolling and alternate output, hidden activation,
scroll-away, final output, selection/copy and stop/close recovery. Also exercise
the charter's sixteen-busy-session responsiveness case before accepting O1.
Record actual accepted output/progress so lower throughput cannot masquerade as
lower CPU. Report before/after CPU, allocations, memory trajectory and genuine
input-to-visible latency separately; do not sum nested profile shares.

A new wire shape owes its canonical schema, generated renderer types, Go DTO check
and real-socket contract check in the same delivery. Existing trace/request
logging and failing-test context remain in use; no new telemetry is proposed.

## Alternatives deliberately not selected

- Remove freezing: breaks snapshot ownership; the diagnostic variant is not a fix.
- Skip all hidden processing: loses accepted state/effects or stale-on-activation
  correctness; hidden painting is already suppressed.
- Share one cached observation snapshot with every publisher: wrong across
  sub-feeds, resize, error modes, holds, attach and resend without further design.
- Lower frame cadence, shrink history, change credit or retry/timeouts: changes
  behavior to hide cost rather than eliminating measured redundant work.
- Disable request logs: not supported as the dominant CPU explanation and conflicts
  with required observability.
- Add a new bulk carrier or independent history store: outside the charter and
  duplicates existing authority.

## Out

Implementation or merge of the five packages; claim that the earlier native RAM
ramp is fixed; release/deployment; macOS proof not yet run; ledger eviction
optimization beyond its existing one-pass fix; logging/ack policy changes;
retention-policy changes; the separate rapid-close race (`nocx-3an1s.1.10`);
`nocx-7epsi`'s transition-observation defect; a second VT parser or state owner.

The RAM follow-on stays on `nocx-3an1s.1.9`. Its next investigation must preserve
the earlier session sequence/runtime state and distinguish live native ownership
from allocator retention. A lower-allocation patch alone cannot close it.

## Preparation verification and approval boundary

This supporting PR owes source/ADR review, portable-evidence consistency checks,
Markdown/JSON formatting, valid local links, commit/task checks and the normal
PR document checks. It changes no executable product/tooling code, so it owes no
product suite or new permanent tests. The automatic document-readiness gate is
not installed; readiness is reviewed manually and no passing receipt is invented.

The preparation can be accepted independently of the proposed implementation.
Owner approval/admission is still required before taking deferred leaves. P5 also
requires its contract proof before a corresponding implementation can be called
ready. Neither a clean gate nor merging this documentation silently raises the
finding budget or changes the root's DONE WHEN.

## DONE WHEN

The preparation PR contains the portable measured evidence, current-owner
inventory, binding constraints, five tracked deferred scopes, observable
acceptance scenarios and honest design/admission gates; links and evidence agree,
and independent review finds no unaddressed contradiction. Another worker can
start an admitted P1/P2/P3/P4 scope without re-deriving the diagnostic findings and
can see why incremental retained delivery must first finish P5. No optimization,
stage acceptance or native RAM fix is represented as delivered by this document.
