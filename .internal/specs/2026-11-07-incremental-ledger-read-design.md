# Incremental durable-row reads: one immutable response, bounded continuation

- **Date / leaf:** 2026-11-07 / `nocx-3an1s.1.16` (P5).
- **Status:** coordinator executed the corrected 16-scenario model and encrypted-store
  proof; independent targeted re-review accepted both proof corrections.
  No production pagination API is implemented here.
- **Admission:** P5 in the [approved preparation](2026-11-07-busy-terminal-performance-design.md#p5-the-missing-durable-row-contract).
  This is a contract proof, not permission to implement, supersede an ADR, or merge.
- **Recommended decision:** capture one revision-consistent, incremental response
  inside the existing content owner. Drain it through bounded pages. Spill that
  same in-flight response to a private encrypted temporary SQLite file when needed;
  never keep a live content transaction open for network continuation.

## Outcome and constraints

For people running busy terminals, stop repeatedly transferring and decoding all
retained row bodies while preserving exactly what the ledger says, including
recovered heads, missing middles and final output. Speed does not authorize output
loss. An old immutable selection snapshot stays immutable. The live screen and
its frame revision remain different from the durable artifact and its revision.

The following is the closed constraint set for this design. `M` names the disposable
model trace; `E` the real encrypted-store probe; `I` the later implementation check.
The last column does not claim that an unexecuted fixture passed.

| ID  | Requirement and provenance                                                                                                                                  | Check                                                                                                    |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| C1  | Extend content ledger → capability → ledger handler; one authority (AD-6/8, ADR-0019). No second writer, history service, cache, or VT parser.              | Inventory below; I: real-wire caller cutover and architecture review                                     |
| C2  | A read's metadata, availability, membership and row bytes belong to one revision; installation is atomic (AD-9, ADR-0066/0072/0073).                        | M: continuous-append-eviction, closing-row; E: revision 7 survives source deletion at 8                  |
| C3  | Every response is at most 65,536 encoded bytes and 256 records, including exceptional rows.                                                                 | M: every emitted page; 1,600-row, 30,000-row and 9-MiB-row fixtures                                      |
| C4  | A healthy reader finishes under continuous append plus eviction without restarting; a stalled reader has a finite lifetime (AD-10 and operation catalogue). | M: mutation after every page, zero restarts; restart-binding-expiry                                      |
| C5  | Prepend and sparse membership are not tail cursors; retention alone removes durable rows (ADR-0078).                                                        | M: prepend-sparse-protected-ends; negative/zero/positive chunk sequences                                 |
| C6  | Seal, truncation and each loss cause advance metadata even without a new row. Disconnect does not seal (ADR-0075/0076/0094).                                | M: metadata-only-seal-loss, closing-row, restart-binding-expiry                                          |
| C7  | Present-empty, unavailable, not-recorded, missing identity and failed read remain different; stale responses cannot resurrect newer unavailable state.      | M: availability-algebra, unavailable-during-read                                                         |
| C8  | Preserve complete cell facts, long graphemes, style, wrap and continuation (ADR-0072); no new artifact-size readability cap.                                | M: oversized-row-and-cell-fidelity; I: existing decoder/painter/selection tests                          |
| C9  | Preserve the encrypted store's one-handle exclusion (ADR-0043), key-before-access and private file posture.                                                 | E: actual adiantum/OpenBlob copy, canary, wrong-key refusal, cleanup; I: crash/vault lifecycle           |
| C10 | No network-held source connection/WAL snapshot; response storage is bounded and reclaimed, not a new retention policy (ADR-0078).                           | E: writer advances before spool is read; M: disk-pressure-cleanup; I: allocation/admission/expiry probes |
| C11 | Geometry/input ownership and the binary live-frame carrier do not change (AD-1, ADR-3kqrem7v, ADR-0073/0094).                                               | No production edits in P5; I: existing real-terminal fidelity suite                                      |

No accepted ADR is changed. Temporary response materialization is not another
history owner: it has no independent ingest, retention choice, restore API,
search, reuse by a later read, or authority after its invocation ends. It is the
existing artifact response's temporary storage, freed on completion or invalidation.
This distinction, including disk accounting, must survive implementation review.

## Inventory and source facts

Paths below are relative to the repository. They describe the prepared base, not
an already existing implementation of this proposal.

- `internal/content/ledger_block_rows.go` owns append, prepend, cap eviction,
  recovery cursor and seal in transactions. `OpenBlockRowsForSession.NextRow` is
  a writer/recovery coordinate, **not** a read cursor. `ledger.query.before`
  pages entries, not rows. Chunks have sparse, negative, zero or positive `seq`;
  the eviction tests deliberately cover those values.
- `ledger_sqlite.go:Artifact` reads metadata and then chunks in separate statements.
  `artifactByID` reads the internal eviction receipt, but
  `internal/transport/ws_ledger_query.go:ledgerArtifactResponse` does not expose it.
  Zero bytes alone cannot distinguish an empty command from an evicted body.
- `internal/capability/ledger.go:LedgerService/ledgerService` and
  `LedgerOperation.Run` are the existing read boundary and admission owner.
  Add the cohesive read there, not a second repository or transport service.
- `frontend/src/restore-client.ts:readBlockRowsForEntry` calls `ledger.get` then
  `ledger.artifact`, and shares in-flight reads. `terminal-content.ts` coalesces
  per-entry refreshes and schedules the closing follow-up. `restoredBody` shares
  this reader. These are the future replacement sites, not an invitation to
  create another fetching loop.
- `scrollback/block-rows.ts:StoredBlockRows.artifactVersion` is currently immutable
  **artifact identity**, not mutation revision. `scrollback/blocks.ts` currently
  admits rows by greatest `from`; this cannot detect prepend, deletion or
  metadata-only changes. Both must cut over together with the reader.
- The stored JSONL row's cell vocabulary is `contracts/ledger.blockRows.schema.json`,
  shared with the live frame. `BlockRowsText`, painter, selection and agent copy
  are existing consumers, not permission to create another row format.

### The actual bounds, not presumed bounds

- `internal/content/policy.go` defaults output retention to **256 KiB**.
  `internal/settings/settings.go:HistoryOutputCapKB` permits **16–4096 KiB**.
- The streamed-row chunk target is **16 KiB**, cut at whole-row boundaries.
  A single long row can exceed it. `evictBlockRowsToCap` explicitly permits
  protected whole-chunk overshoot; its zero-sequence stopping rule can also leave
  more bytes than the nominal cap. The current store has **no proven hard
  per-artifact byte or row ceiling** for this media type.
- `MaxArtifactBytes = 1 MiB` limits the separate `CaptureOutput` path, not streamed
  block rows. It cannot justify refusing an existing large row here.
- `block_rows_encode_test.go:TestBlockRowsLine_PlainRowStoresUnderThreeTimesItsText`
  records an 18-character plain row under 54 encoded bytes, versus 542 in the
  retired representation. The disposable fixture also prints exact synthetic
  JSONL sizes at widths 18, 79, 120 and 400; those are **not** application benchmarks.
- Transport's 8-MiB params limit and 32-MiB inbound WebSocket limit are not outbound
  ledger page budgets. The live screen's 1-MiB carrier bound belongs to ADR-0073,
  not this JSON-RPC read. The 64-KiB page is a new engineering choice: four
  normal chunk targets, with a row-count bound to cap many tiny-row decodes.

### Why a live SQLite cursor is not the answer

`internal/content/sqlite.go:maxOpenConns = 1` is correctness, not tuning.
[ADR-0043](../../docs/decisions/0043-one-connection-to-the-encrypted-store.md)
contains the encrypted-WAL torn-block failure: another connection to the same
file is not safe. Holding a read transaction across client page requests would
hold the only source connection and stop every ledger writer for a slow client.
Adding another source connection would undo the accepted containment. An ordinary
WAL snapshot on an unencrypted multi-connection store would also pin WAL growth;
that is not the store this project has.

`sqliteContent` already owns `keyHex`, `keyedURI`, private-file enforcement, and
`Backup`'s `sql.Conn.Raw(driver.Conn).Raw()` access. `Backup` copies the **whole
store**, so it is not the new reader: copying unrelated history would defeat the
purpose. Reuse its keyed-VFS pattern within content, not its full-database copy.

The current ncruces v0.35.2 dependency provides `Conn.OpenBlob`, `Blob.Seek`,
`Blob.Read`, `Blob.Size` and `Blob.Close`. `artifact_chunks` is an ordinary rowid
STRICT table, despite its composite primary key. Resolve its actual `rowid`;
read ranges through a 16-KiB incremental BLOB buffer. Do not equate `seq` to rowid.
Do **not** use `SELECT body` or claim `substr(body,...)` proves bounded internal
allocation: the SQLite value may first materialize a large BLOB. The real probe
uses the actual incremental BLOB API through the existing driver.

## Considered options

| Option / named tactic                                                   | Result                                                                                                                                                                         |
| ----------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Tail cursor / keyset scan of current rows                               | Reject: misses prepend, holes, loss/seal and availability changes.                                                                                                             |
| Per-page current snapshots with a fixed upper fence                     | Reject: finite progress, but produces a multi-revision card; violates C2. It is not the recommendation.                                                                        |
| Optimistic revision check; restart when revision advances               | Reject: continuous append/eviction can starve a read forever. Bounding each attempt hides unbounded total work.                                                                |
| Keep a source read transaction through all pages (snapshot isolation)   | Reject: monopolizes the only encrypted source connection; no new source connection permitted.                                                                                  |
| Materialize one bounded all-memory response and refuse larger artifacts | Reject: 4-MiB retention is not a hard bound. An 8-MiB cutoff would be a new readability regression.                                                                            |
| Mutation journal / MVCC historical rows retained for every reader       | Reject for this delivery: another persistent history/retention mechanism, tombstone lifetime and reader lag policy.                                                            |
| **Materialized response / bounded continuation, encrypted spill**       | **Choose:** one immutable revision, finite progress independent of writers, no retained mutation log, no new artifact-size cutoff. Pay finite capture work and temporary disk. |

The assumption-rejecting option is to keep full artifact reads unchanged: it is
correct about neither atomic metadata/body nor explicit eviction receipts, and
retains the measured redundant transfer/parse work. It is a control for later
measurement, not a solution to this contract.

## Identity and ordered revision

Allocate `readRevision` from one persisted content-owned row-read mutation counter,
and stamp affected artifacts in the same transaction as every externally observable
row/metadata/availability change: append, recovered-head insertion, cap deletion,
seal, loss-only delivery, truncation, body-retention receipt, artifact/entry removal
and relevant metadata correction. This is not `ledger_sequence`'s entry-ingest
order, a frame revision, or a retained event log. A no-op replay allocates nothing;
rollback changes neither data nor counter. Present artifacts report their last
stamp. A missing identity reports the counter observed in that same read transaction,
which orders the absence after its deletion without an unbounded tombstone log.
Neither byte length, greatest row, chunk maximum nor lifecycle phase substitutes
for revision. Artifact ID remains identity, never content version.

Persist `membershipRevision` as the last `readRevision` that changed membership,
and each row's `bornRevision` (or replacement revision if correction is ever
supported). They index the same fact, not independent clocks. One sparse index
inside the ledger points to existing chunk bytes: artifact ID, absolute row key,
chunk rowid, row JSON byte offset/length, insertion revision. No body is copied
into that index. Delete it with its chunk in the same transaction. Prepend uses
new insertion revisions even below every previously seen coordinate. Migration
backfills this derived index before exposing the new read; malformed legacy
content is an explicit read error, never an invented blank row.

Revision identity is `(storeIncarnation, artifactId, artifactGeneration,
readRevision)`. `artifactGeneration` is its creation's allocated revision: even
an externally reused ID after deletion cannot impersonate the old artifact.
A normal process restart preserves incarnation, generations and revisions;
database replacement/restore changes incarnation so old higher revisions cannot
mask restored content. Revisions and row keys on the new envelope are canonical
unsigned decimal strings, not lossy JavaScript doubles. Cell vocabulary stays
unchanged. Byte offsets and signed chunk sequences remain private. An implementation
must index unsigned row keys in their numeric order, not lexicographic decimal order.

## One read, from capture to atomic installation

The future existing-ledger method is provisionally named `ledger.rows`; its
three request forms are `begin`, `next`, and `cancel` on the **same capability**.
The request names entry, expected artifact when already resolved, view binding,
and an optional fully installed base revision. `next` carries an opaque cursor,
not an offset into the live artifact. `cancel` is idempotent and is not required
for expiry cleanup to work. These names are a design contract, not added schemas.

1. **Admit before taking the database connection.** Share the existing per-client,
   per-entry in-flight invocation. Reserve a response slot and temporary storage
   through the content owner. A waiting person sees the catalogue's waiting
   state; capacity is not a new `busy` error or a spin/retry loop.
2. **Capture R in one source read transaction.** Resolve entry/artifact ownership,
   media type, incarnation, revision, typed metadata and eviction receipt together.
   Produce retained-row membership as sorted, maximal, inclusive `[first,last]`
   intervals, streaming the row-key index. Emit row JSON only where insertion
   revision is greater than the valid base. Without a valid base, emit all rows.
   If `membershipRevision <= base`, omit the manifest entirely and say `unchanged`.
   The manifest is otherwise a **complete replacement**, not an append instruction.
3. **Materialize the immutable response, not a reusable body cache.** Stream
   interval records and changed row bytes to the response sink in bounded pieces.
   The sink uses memory up to 256 KiB of encoded response, then spills the same
   response to a private adiantum SQLite file. Memory and file are two storage
   tiers of that invocation, not two readers. No full row/body is assembled by
   the server just to split it again. Close all BLOB handles and source statements,
   then end the source transaction **before** returning page one.
4. **Drain only the captured response.** Each page repeats R and its fixed typed
   metadata, carries a contiguous record ordinal, and advances an opaque cursor.
   Append, prepend, cap eviction and seal after capture do not restart or extend
   this read. A short current-identity/availability check uses the existing source
   owner, never the old source transaction. Normal content advancement is not
   invalidation; an explicit whole-body unavailability/removal or binding revoke is.
5. **Stage in the existing projection owner.** Validate all page identity, revision,
   ordinal, membership ordering and cell facts before installation. Keep the
   installed immutable view and its selection alive while the response is staged.
   At final page, retain base rows named by the new manifest, remove absent rows,
   insert changed rows, validate exact membership, and install rows plus metadata atomically.
   Intervals must have ordered nonnegative endpoints and be sorted, disjoint and
   maximal across page boundaries. Every changed key must belong to the manifest;
   filtered base rows plus changed rows must cover it completely. A matching row
   count alone is insufficient (`[1,2]` cannot be satisfied by keys `{1,3}`).
   A metadata-only response still installs its newer revision. Missing
   a supposedly reusable base row is `base-mismatch`, not a fabricated row: discard
   this assembly and request one explicit full read. This is not a write-driven
   restart policy. Corrupt or mixed-revision pages discard the entire assembly.
6. **Release.** Completion closes the response handle, deletes its private files
   and removes the owner's lease registration. An owner-driven deadline scheduler
   performs the same reclamation on idle/absolute expiry **without another next,
   cancel or begin request**; page checks alone are insufficient. Disposal/revocation
   use this same release operation. Expiry wakes queued admission, so abandoned
   clients cannot permanently occupy the four slots. The next coalesced notification
   reads from the last installed revision, not the largest row; keep the existing
   final-close follow-up.

Every changed body row is complete and immutable at R. Retained unchanged bodies
are reused from the immutable base. A full manifest removes arbitrary retention
holes, including multiple disjoint holes, while preserving protected head/tail.
Its encoded size is O(number of intervals), not a preallocated vector of all row
IDs. A pathological alternating membership can still have O(rows) intervals;
streaming those through the same limits prevents hidden unbounded manifests.
There is no full-body hash or serialization of unchanged rows per notification.
The index/manifest scan remains O(retained row count): this proposal reduces body
copy/transfer/parse/reconciliation, not all metadata work to O(changes).

## Concrete bounds and oversized rows

| Resource         | Choice and accounting                                                                                                                                                                                                                                                                                                  |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Page             | At most **65,536 bytes**, measured on the actual UTF-8 JSON-RPC response including envelope, strings, escaping and cursor. No oversized exception response.                                                                                                                                                            |
| Records/page     | At most **256** row, manifest-interval or fragment records combined. This also bounds complete row decodes.                                                                                                                                                                                                            |
| Fragment         | At most **45,056 raw bytes (44 KiB)**, base64 of serialized stored **cell facts**, never PTY bytes. Base64 is at most 60,076 bytes, leaving envelope space; actual encoded-size check still decides.                                                                                                                   |
| Oversized row    | One identity plus total byte length, byte offset and ordered fragments. No partial JSON/UTF-8/cluster decoding or painting. Reassemble before row validation; complete long graphemes, styles and wrap survive. No row-size rejection added.                                                                           |
| Source copy      | **16-KiB** incremental BLOB reads; never an entire oversized chunk allocation.                                                                                                                                                                                                                                         |
| Response memory  | Spill after **256 KiB** encoded response; at most **512 KiB application-owned scratch** per active read/capture, including page/fragment/driver buffers. Explicit 64-KiB page-cache targets per temporary connection are separate; this is not a total process/WASM/RSS promise.                                       |
| Concurrency      | At most **4** active response leases process-wide, one per `(client, entry, binding)`; same-key consumers share. Capture is already serialized by the source's one connection. Continuations retain their slots, so another begin cannot starve an admitted reader of its own continuation.                            |
| Capture lifetime | Existing ledger read maximum **60 s**, declared in the operation catalogue, with cancellation/rollback. No client wait occurs inside it. This bounds a failure, not a promise that a huge read is cheap.                                                                                                               |
| Lease            | **30 s idle**; non-renewable absolute deadline `min(3600 s, 120 s + ceil(encodedSpoolBytes / 256 KiB) s)`. Fixed at capture completion. A returned advancing page renews only idle time.                                                                                                                               |
| Page operation   | Catalogue maximum **5 s**, clipped by lease remainder; admission wait follows the catalogue, not a call-site timer. Expiry is an explicit read outcome.                                                                                                                                                                |
| Temporary disk   | Before copying, reserve a conservative byte allowance from available physical content-disk headroom; count all temporary DB/journal/WAL files and outstanding reservations. Enforce it at allocation (`max_page_count` plus file accounting), not only after close. No new retention setting or per-artifact size cap. |

Reservation uses known changed-row byte sizes and interval/record overhead; the
proof uses a conservative `4*changedBytes + 256*changedRows + 128*retainedRows +
1 MiB` allowance, without allocating the corresponding row vector. Implementation
must establish its actual spool-format bound and account page rounding and
sidecars; if reservation cannot fit, wait within admission or report temporary
space failure. Never evict durable output to fund a read. Disk-full during capture
aborts the response, rolls back its private file, releases source/slots, and
preserves the last installed view. This is normal resource failure, not a new
permanent size-based unreadability rule. Unlimited data on a full disk cannot be
promised readable by materialization.

**Unavoidable client cost:** a currently supported huge row still needs memory
proportional to that row to become an immutable decoded row. An atomic install
also retains the prior view while staging changed rows. Fragmentation bounds
wire/server working buffers, not the final client document. Do not claim a constant
client heap limit, hide the staging allocation, or truncate a grapheme to obtain
one. The 9-MiB-row proof exercises this distinction rather than treating 4 MiB as
an absolute retained-body ceiling.

## Availability, revocation and restart

The result is a closed algebra, never `body == ""` inference:

- `present`: zero or more rows; an empty present artifact is a real empty output.
- `unavailable`: artifact metadata exists and its retention/deletion receipt says
  the body is no longer retained. Carry cause and revision, clear the projection.
- `not-recorded`: entry exists but no row artifact was recorded (for example policy).
- `entry-missing` / `artifact-missing`: named identity is absent. Artifact mismatch
  is not silently rebound to whichever artifact happens to be present.
- `read-error`: I/O, malformed content, permission, timeout or temporary-space
  failure. Preserve last good projection with an explicit unreadable/stale status;
  never convert it to empty, unavailable or a successful final close.
- `cursor-expired` / `binding-stale`: no continuation exists for this identity.
  These are not an empty body and do not alter command lifecycle.

A lease binds connection/client authority, view-binding generation, store
incarnation, entry, artifact and generation, captured revision and response ID. It is not a new
live-session identity. The existing transport connection authority still governs
access; an opaque cursor is not authorization. Check all bindings on every next.

A whole-body removal/availability change increments the ordered artifact revision
and invalidates its in-flight read receipts. Next answers terminal `invalidated`
with the current typed availability, not a page of old body. A notification or
newer accepted response also raises the client's availability barrier; an older
late page cannot resurrect it. A final-page check and body deletion can race in
real time: the claim is that **after a client learns the newer fact**, it rejects
the older one, not that a response can predict a deletion it has not observed.
Ordinary cap eviction does not invalidate R; the later complete snapshot removes
those rows atomically. Applying R then R+1 is not resurrecting an already accepted
R+1. Final-close metadata/rows belong to whichever snapshot actually includes seal.

Server restart destroys continuation handles and purges abandoned response files.
An installed checkpoint may be reused only after validating the same durable
store incarnation/artifact and a non-future revision; an unfinished assembly is
never a base. Reconnect creates a fresh view-binding generation, so old responses
are rejected even if their numeric revision is higher. Vault lock/reset, store
replacement, owner disposal, request cancellation and authority revocation cancel
leases and clean their files. A coordinator disappearance never seals an open
block; the writer's durable `NextRow` continues independently (ADR-0076).

Temporary files live under a private **0700 content-owned directory**, files
**0600**, names unguessable and not client paths. Key the adiantum destination
before any SQLite access; never use a plaintext temp file then encrypt it later.
Use one handle per file, private bounded page caches and no plaintext SQLite temp
spill. Exclude response files from backup, diagnostics and restore inventory.
Unpublish/close the handle before deleting files; purge only this designated
response directory at next unlocked startup, never arbitrary paths. Do not claim
secure erasure: ciphertext cleanup and non-reusability are the guarantee. The
source key remains inside content, never in the handler/cursor/log.

## Finite progress and the cost that remains

Capture has a fixed revision and a finite number of records. Every non-final page
advances its ordinal by at least one, including long-row fragments. Thus at most
`recordCount + 1` successful page calls finish an admitted snapshot, independent
of how many appends/evictions occur afterward. No server mutation extends its
upper bound and no successful page restarts at zero. Once writers quiesce, one
subsequent completed delta produces exactly the current retained body. Under
continuous writers each completed snapshot is a real past R; freshness is the
existing coalesced follow-up's job, not an excuse to abandon finite reads.

A stalled/expired/revoked reader is different: it terminates explicitly and
releases resources. Do not automatically restart it forever or hide lifetime
failure behind a successful empty result. New user/read-loop demand may start a
new invocation under the existing scheduling policy.

**Material cost, not an unresolved consistency decision:** capture holds the
single source connection while copying changed bytes to the response sink.
Writers pause during that finite work. Spill adds encrypted disk I/O and needs
space. Unlike a network-held transaction, a stalled client cannot lengthen this
pause. Unlike a new read connection, it does not reopen the encrypted-WAL race.
There is no constant-time capture claim. Large artifacts, many tiny rows and slow
disks must be measured against ledger ingestion and buffer-overflow behavior
before implementation acceptance. A slow/full disk can fail a read; it cannot
silently change retention or introduce an artifact-size cutoff. No product SLA
or new loss-policy exception is established by the chosen engineering limits.

## Future clean vertical cutover

A later explicitly admitted implementation must deliver the following together,
not land a disconnected paging scaffold:

1. Content schema migration and row-offset/revision index; every append, prepend,
   cap eviction, seal, loss, body deletion and store replacement updates identity,
   revision and index atomically. Include rollback and migration fixtures.
2. The existing ledger repository/capability/handler read, temporary response
   lifetime/resource ownership, catalogue entry, canonical request/result schemas,
   generated types and real-wire limits. No handler reaches concrete SQLite.
3. Replace `readBlockRowsForEntry`'s metadata/body pair for durable rows; retain
   its coalescing owner. Cut restoration, growth, close, hidden/pending blocks and
   agent command output onto the same reader. Remove the old live full-body row
   refresh path and last-row-only acceptance, not leave fallback aliases.
4. Rename identity-as-`artifactVersion` at affected acceptance/selection callsites
   to its real meaning; carry a separate read revision and binding. Atomic immutable
   snapshots must reach painter, copy/selection, pending maps and closing promises.
   An in-progress or failed snapshot cannot settle final output as complete.
5. Keep full `ledger.artifact` reads only for distinct consumers: prose/tool result,
   legacy VT bodies and explicit full-artifact export/search paths that genuinely
   need that API. Update their documented availability semantics intentionally;
   do not silently run the obsolete row refresh as a fallback.

An artifact revision is not a live frame revision. The reader consumes the
existing reconnect/session binding snapshot; it does not redefine AD-9's coordinated
live-frame/card restore boundary or manufacture a common clock from independent
counters. Future real reconnect tests must exercise that boundary, not just RPCs.

## Retained proof material and exact execution

Reproducible, explicitly nonproduction contract evidence is retained under
[`docs/research/2026-11-07-ledger-read-proof/`](../../docs/research/2026-11-07-ledger-read-proof/).
Its runtime databases are temporary; the scripts and observed result JSON are
design evidence, not a production implementation or permanent synthetic product test.

- `scenarios.json`: actual default/max-setting cap values, page/slice limits and
  fixture sizes. No key, user data or captured terminal output.
- `prove.py`: runnable deterministic reference traces, using synthetic SQLite
  source/spool data and a disposable projection model. It is **not** encrypted,
  not production code and not evidence of renderer/store implementation speed.
  Its `substr` fixture read expresses byte ranges only; the real bounded-copy
  mechanism is exercised by the Go probe, not inferred from that model.
- `adiantum_probe.go`: standalone real ncruces driver/VFS experiment, using the
  existing keyedURI pattern, one connection per file, a 9-MiB BLOB and 16-KiB
  `OpenBlob.Read` slices. It reads no production database and imports no product
  implementation scaffold. It does not test the future handler or frontend.
  Its `//go:build ignore` excludes it from `go ./...`; explicit `go run` still works.
- `model-results.json` and `encrypted-spool-results.json`: coordinator-observed
  results of the corrected 16-scenario model and the real encrypted-store probe.
  These are observed outputs, not hand-generated expected fixtures.

Coordinator commands, from the prepared/integrated nocx worktree:

```sh
python3 docs/research/2026-11-07-ledger-read-proof/prove.py --output /tmp/nocx-ledger-model-results.json
```

From the prepared/integrated nocx worktree, with its existing `go.mod`:

```sh
/home/andrey/.cache/nocx-toolchains/go/bin/go run docs/research/2026-11-07-ledger-read-proof/adiantum_probe.go
```

The following assertions passed in coordinator execution, including both
review-added owner-expiry and exact-membership traces:

- Model exits zero and ends `PASS: all ledger contract model assertions`, also
  writing `/tmp/nocx-ledger-model-results.json`, not overwriting retained evidence.
  Every emitted response is <=64 KiB
  and <=256 records; 1,600/30,000 retained rows finish while every page triggers
  append and middle eviction, with zero restarts and exact captured-R installation.
- Next delta equals current survivors. A recovered head with unchanged maximum,
  signed/sparse/zero chunk IDs and middle holes retains exactly `[90,91,97,100]`.
  A metadata-only seal/loss read transfers no bodies or manifest. Empty, unavailable,
  missing, not-recorded and error states differ. Late unavailable/binding responses
  are rejected; restart does not seal. Final closing rows and seal install together.
- Artifact/store replacement and reused-ID generation invalidate cursors. An
  out-of-order page installs nothing. Mid-copy cancellation removes the partial
  response and releases the source transaction. The four-lease model shares
  same-key demand, leaves a fifth read waiting, permits existing continuation,
  then admits that fifth read after release without ever owning five snapshots.
- Without any next/cancel call, the owner timer removes four dormant response
  files and registrations, permitting the fifth reader; absolute expiry also
  reclaims a recently active lease. Out-of-manifest keys, reversed, overlapping,
  unsorted and incomplete manifests all discard staging while preserving the
  installed rows, metadata and revision.
- A 9-MiB row and styled/wide/combining/ZWJ/wrapped facts survive byte-exactly;
  non-final pages never replace the installed immutable projection. The model
  asserts its source byte windows never exceed16 KiB; it does not assert the
  Python/SQLite internal heap is so bounded.
- The real probe ends `PASS: real encrypted spool, bounded incremental copy,
source progress and cleanup`: **9,437,184 bytes / 576 parts / max16,384-byte
  source read**, unchanged SHA-256 after source deletion, source revision8 while
  spool metadata remains open at7, no plaintext canary/header in files, wrong key
  refused, correct key reopens, response file removed. It prints capture duration
  and actual file bytes; neither is preclaimed performance evidence.

### Observed executions and review corrections

The coordinator reported both initial proofs passing on 2026-11-07. The model
completed 1,600 rows in 7 pages and 30,000 in 118 pages, with zero restarts. The
9-MiB-row trace took 210 pages, max 60,709 encoded bytes/page and 16,384 bytes/source
window. The real encrypted probe copied 9,437,184 bytes in 576 parts, observed
33 ms capture and a 9,744,384-byte response file; source revision 8 did not change
the revision-7 response. Canary absence, wrong-key refusal, correct-key reopen
and cleanup passed. The 33 ms is one fixture observation, not a product SLA.

`ReviewPerfContract` found two P2 defects in the model: page-driven expiry did not
reclaim the owner's slot without client traffic, and cardinality-only manifest
validation could accept the wrong key set. The corrections above add independent
owner expiry/reclamation and exact membership validation, with targeted adversarial
traces. On 2026-11-07 `ReviewPerfContract` statically re-reviewed the retained
corrections and reported both findings addressed, with no additional findings in
those fixes. It performed no execution. The coordinator subsequently reran the
retained Python proof: all 16 scenarios passed, including owner-driven expiry with
zero next/cancel calls and all five malformed-membership variants. The retained
`model-results.json` is copied from that actual corrected output.

The Go probe checks explicit cleanup, not a crash sweeper or real vault revocation.
The model checks lifetime/identity semantics, not catalogue admission or UI paint.
Implementation acceptance additionally needs four-lease admission under pressure,
crash/lock/reset cleanup, spool allocation failure and cancellation mid-copy,
index migration/corruption/transaction rollback, actual canonical-schema responses,
real reconnect, selection, hidden/pending blocks, final-close and retained-loss
sentences. Run those once in the later vertical delivery. The independent targeted
review and corrected coordinator execution are recorded above. Final design
acceptance remains with the coordinator/owner. No P5
implementation readiness or product benchmark win is claimed here.
