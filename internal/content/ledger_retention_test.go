package content_test

// The durable retention watermark and the eviction of entries (nocx-rtg0.12),
// design §5.4.
//
// The constraint these tests exist to hold is one sentence from the design:
// coverage CANNOT be computed from the rows that remain. Once eviction has
// deleted the rows there is nothing left to count, so every assertion about
// Coverage below is written to fail against a `SELECT MIN(ended_at) FROM
// entries` implementation — either by naming a horizon older than every
// surviving row, or by naming one at all when no row survives.
//
// The second rule here is that the watermark and the deletion are one
// transaction. That is asserted from both directions: a failing DELETE must
// leave the watermark where it was, and a failing watermark write must leave
// every row in place. Neither half is allowed to exist without the other.

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// ── fixtures ─────────────────────────────────────────────────────────────

// openLedgerAt reopens an existing store file — what proves the watermark is
// on disk rather than in the process that wrote it.
func openLedgerAt(t *testing.T, path string) content.LedgerRepository {
	t.Helper()
	db, err := content.Open(context.Background(), content.Config{
		Path: path, Key: testKey(), Budget: testBudget,
	})
	if err != nil {
		t.Fatalf("reopen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db.Ledger()
}

// closeEntryAt walks an entry to closed with an EXACT ended_at. closeEntry
// stamps time.Now(), which cannot express "this ended long ago" — and a
// retention test that cannot place a row in the past can only assert that
// eviction removed nothing.
func closeEntryAt(t *testing.T, led content.LedgerRepository, id string, endedAt int64) {
	t.Helper()
	ctx := context.Background()
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: id})
	if err != nil {
		t.Fatalf("StartExecution(%q): %v", id, err)
	}
	zero := 0
	payload := content.ShellPayloadJSON(&zero)
	if err := led.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt:           endedAt,
		TerminationReason: content.TermCompleted,
		Status:            content.EntrySuccess,
		Payload:           &payload,
	}); err != nil {
		t.Fatalf("FinishExecution(%q): %v", id, err)
	}
}

// seedClosed records one closed entry per instant, in the order given, so
// ingest_seq runs 1..n while ended_at is whatever the caller says. The two
// orders are deliberately separable: eviction claims to walk ingest_seq, and
// a fixture where the two agree cannot tell that claim from wall-clock order.
func seedClosed(t *testing.T, led content.LedgerRepository, ends ...int64) []string {
	t.Helper()
	envReady(t, led, "local")
	ids := make([]string, 0, len(ends))
	for i, end := range ends {
		id := entryID(i + 1)
		submitAt(t, led, id, "local", "/repo", content.EntryShell, fmt.Sprintf("cmd-%d", i+1))
		closeEntryAt(t, led, id, end)
		ids = append(ids, id)
	}
	return ids
}

func evictOK(t *testing.T, led content.LedgerRepository, req content.EvictionRequest) content.EvictionResult {
	t.Helper()
	res, err := led.EvictEntries(context.Background(), req)
	if err != nil {
		t.Fatalf("EvictEntries(%+v): %v", req, err)
	}
	return res
}

func watermarkOK(t *testing.T, led content.LedgerRepository) content.RetentionWatermark {
	t.Helper()
	wm, err := led.Watermark(context.Background())
	if err != nil {
		t.Fatalf("Watermark: %v", err)
	}
	return wm
}

func coverageOK(t *testing.T, led content.LedgerRepository) *int64 {
	t.Helper()
	return queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 50}).Coverage
}

// ── the criterion: coverage is not derivable from what remains ───────────

// A horizon OLDER than every surviving row. The survivors end at 40000 and
// 50000, so MIN(ended_at) over the store is 40000; the watermark says 3000,
// the newest instant eviction actually removed. No query over the remaining
// rows can produce 3000 — the row that carried it is gone.
func TestCoverageAfterEvictionNamesAHorizonNoSurvivingRowHolds(t *testing.T) {
	_, led := newLedger(t)
	seedClosed(t, led, 1000, 2000, 3000, 40000, 50000)

	res := evictOK(t, led, content.EvictionRequest{Before: 3500, Max: 100})
	if res.Evicted != 3 {
		t.Fatalf("evicted %d entries, want the three that ended before 3500", res.Evicted)
	}

	cov := coverageOK(t, led)
	if cov == nil {
		t.Fatal("Coverage is nil after an eviction that removed three rows")
	}
	if *cov != 3000 {
		t.Fatalf("Coverage = %d, want the watermark horizon 3000", *cov)
	}
	// The load-bearing assertion: 3000 is strictly older than the oldest
	// surviving row, so MIN(ended_at) over the survivors (40000) cannot be
	// the source of it.
	if *cov >= 40000 {
		t.Fatalf("Coverage = %d — that is the surviving rows' MIN(ended_at), not the watermark", *cov)
	}
}

// The strongest form: evict every row. MIN(ended_at) over an empty table is
// NULL, so the pre-watermark implementation reports "no horizon at all" for a
// store that has evicted its entire history — full coverage over nothing. The
// watermark still names the horizon and a count larger than the table holds.
func TestCoverageSurvivesAStoreEvictedEmpty(t *testing.T) {
	_, led := newLedger(t)
	seedClosed(t, led, 1000, 2000, 3000)

	res := evictOK(t, led, content.EvictionRequest{Before: 9000, Max: 100})
	if res.Evicted != 3 {
		t.Fatalf("evicted %d, want all 3", res.Evicted)
	}

	page := queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
	if len(page.Entries) != 0 {
		t.Fatalf("page = %v, want an emptied store", pageIDs(page))
	}
	if page.HasRows {
		t.Fatal("HasRows is true for a store eviction emptied")
	}
	if page.Coverage == nil {
		t.Fatal("Coverage is nil for an emptied store — the horizon it was evicted to is exactly what the user needs")
	}
	if *page.Coverage != 3000 {
		t.Fatalf("Coverage = %d, want the horizon 3000", *page.Coverage)
	}

	wm := watermarkOK(t, led)
	if wm.EvictedCount != 3 {
		t.Fatalf("EvictedCount = %d, want 3 — a count larger than the table now holds", wm.EvictedCount)
	}
}

// Never evicted means the honest answer is still the surviving-rows one: the
// store holds everything it ever had, so the oldest row IS the horizon.
func TestCoverageIsTheSurvivingRowsAnswerUntilSomethingIsEvicted(t *testing.T) {
	_, led := newLedger(t)
	seedClosed(t, led, 7000, 8000, 9000)

	wm := watermarkOK(t, led)
	if wm.EvictedCount != 0 {
		t.Fatalf("EvictedCount = %d on a store that never evicted", wm.EvictedCount)
	}
	if wm.Horizon != nil {
		t.Fatalf("Horizon = %d before any eviction — there is no horizon to state", *wm.Horizon)
	}

	cov := coverageOK(t, led)
	if cov == nil || *cov != 7000 {
		t.Fatalf("Coverage = %v, want the oldest retained row 7000", cov)
	}
}

// An eviction that removes nothing must not invent a horizon: a pass that
// found no candidate leaves the store exactly as complete as it was.
func TestAnEvictionThatRemovesNothingLeavesNoWatermark(t *testing.T) {
	_, led := newLedger(t)
	seedClosed(t, led, 7000, 8000)

	res := evictOK(t, led, content.EvictionRequest{Before: 100, Max: 100})
	if res.Evicted != 0 {
		t.Fatalf("evicted %d from a store whose rows are all newer than the cutoff", res.Evicted)
	}
	wm := watermarkOK(t, led)
	if wm.EvictedCount != 0 || wm.Horizon != nil {
		t.Fatalf("watermark = %+v after a pass that removed nothing", wm)
	}
	if cov := coverageOK(t, led); cov == nil || *cov != 7000 {
		t.Fatalf("Coverage = %v, want the surviving-rows answer 7000", cov)
	}
}

// ── both ends of the interval ────────────────────────────────────────────

// The watermark's horizon becomes true when its eviction COMMITS and stops
// being true when the next eviction commits a newer one. Both ends are named
// here: it does not exist before the first pass; it holds across unrelated
// writes and across a reopen (durable, not cached); and it is replaced —
// forward only — by the second pass, never reverting.
func TestWatermarkHoldsFromItsCommitUntilTheNextEviction(t *testing.T) {
	db, led, path := newLedgerAt(t)
	seedClosed(t, led, 1000, 2000, 3000, 4000, 5000)

	// ── before: the interval has not opened ──
	if wm := watermarkOK(t, led); wm.Horizon != nil {
		t.Fatalf("Horizon = %d before the first eviction", *wm.Horizon)
	}

	// ── opens: the first pass commits horizon 2000 ──
	evictOK(t, led, content.EvictionRequest{Before: 2500, Max: 100})
	first := watermarkOK(t, led)
	if first.Horizon == nil || *first.Horizon != 2000 {
		t.Fatalf("Horizon = %v after the first pass, want 2000", first.Horizon)
	}

	// ── stays true across an unrelated write ──
	submitAt(t, led, entryID(90), "local", "/repo", content.EntryShell, "later work")
	if wm := watermarkOK(t, led); wm.Horizon == nil || *wm.Horizon != 2000 {
		t.Fatalf("Horizon = %v after an unrelated submit, want it untouched at 2000", wm.Horizon)
	}

	// ── and across a reopen: it is on disk, not in memory ──
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened := openLedgerAt(t, path)
	if wm := watermarkOK(t, reopened); wm.Horizon == nil || *wm.Horizon != 2000 {
		t.Fatalf("Horizon = %v after reopen, want the durable 2000", wm.Horizon)
	}
	if wm := watermarkOK(t, reopened); wm.EvictedCount != 2 {
		t.Fatalf("EvictedCount = %d after reopen, want 2", wm.EvictedCount)
	}

	// ── closes: the second pass replaces it, forward ──
	evictOK(t, reopened, content.EvictionRequest{Before: 4500, Max: 100})
	second := watermarkOK(t, reopened)
	if second.Horizon == nil || *second.Horizon != 4000 {
		t.Fatalf("Horizon = %v after the second pass, want 4000", second.Horizon)
	}
	if second.EvictedCount != 4 {
		t.Fatalf("EvictedCount = %d, want the running total 4", second.EvictedCount)
	}
}

// The horizon never moves backwards. A pass whose newest victim is OLDER than
// the standing horizon has learned nothing about coverage — the store was
// already incomplete that far back — and a horizon that reverted would tell
// the user the store recovered history it did not.
func TestHorizonOnlyEverAdvances(t *testing.T) {
	_, led := newLedger(t)
	seedClosed(t, led, 5000, 6000, 7000)

	evictOK(t, led, content.EvictionRequest{Before: 6500, Max: 100})
	if wm := watermarkOK(t, led); wm.Horizon == nil || *wm.Horizon != 6000 {
		t.Fatalf("Horizon = %v, want 6000", wm.Horizon)
	}

	// A backdated row arrives after the horizon was set — a command that
	// closes reporting an end long past. Evicting it removes an instant
	// OLDER than the standing horizon, which teaches the store nothing new
	// about its coverage: it was already incomplete that far back.
	late := entryID(4)
	submitAt(t, led, late, "local", "/repo", content.EntryShell, "backdated")
	closeEntryAt(t, led, late, 1000)

	res := evictOK(t, led, content.EvictionRequest{Before: 1500, Max: 100})
	if res.Evicted != 1 {
		t.Fatalf("evicted %d, want the one row ending at 1000", res.Evicted)
	}
	wm := watermarkOK(t, led)
	if wm.Horizon == nil || *wm.Horizon != 6000 {
		t.Fatalf("Horizon = %v after evicting an older instant, want it held at 6000", wm.Horizon)
	}
	if wm.EvictedCount != 3 {
		t.Fatalf("EvictedCount = %d, want 3", wm.EvictedCount)
	}
}

// ── the order eviction walks ─────────────────────────────────────────────

// Oldest-first by ingest_seq, never by wall clock. The fixture inverts the
// two: ingest_seq 1 ends LAST (9000) and ingest_seq 3 ends first (1000). A
// pass capped at one row must take ingest_seq 1 — the ledger's total order —
// and an implementation ordering by ended_at would take the 1000 row instead.
func TestEvictionWalksIngestSeqNotWallClock(t *testing.T) {
	_, led := newLedger(t)
	ids := seedClosed(t, led, 9000, 5000, 1000)

	res := evictOK(t, led, content.EvictionRequest{Before: 9500, Max: 1})
	if res.Evicted != 1 {
		t.Fatalf("evicted %d under a cap of 1", res.Evicted)
	}

	page := queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10})
	// Newest first by ingest_seq: what remains is 3 then 2.
	wantOnly(t, page, ids[2], ids[1])

	// And the horizon is the instant that row carried — 9000 — which is
	// NEWER than both survivors. Honest: the store cannot speak completely
	// for anything up to 9000, because it removed a row that ended there.
	if wm := watermarkOK(t, led); wm.Horizon == nil || *wm.Horizon != 9000 {
		t.Fatalf("Horizon = %v, want the evicted row's 9000", wm.Horizon)
	}
}

// The cap bounds one pass. Eviction runs inside the writer turn, so an
// unbounded DELETE over a large backlog would stall every other mutation
// behind it.
func TestEvictionRespectsItsCap(t *testing.T) {
	_, led := newLedger(t)
	seedClosed(t, led, 1000, 2000, 3000, 4000, 5000)

	res := evictOK(t, led, content.EvictionRequest{Before: 9000, Max: 2})
	if res.Evicted != 2 {
		t.Fatalf("evicted %d under a cap of 2", res.Evicted)
	}
	// Capped short of the cutoff, the horizon is what was ACTUALLY removed
	// (2000), never the cutoff (9000) — the store is complete after 2000 and
	// claiming 9000 would assert coverage it does not have.
	if wm := watermarkOK(t, led); wm.Horizon == nil || *wm.Horizon != 2000 {
		t.Fatalf("Horizon = %v after a capped pass, want the last row actually removed, 2000", wm.Horizon)
	}
}

// An open entry is not a retention candidate whatever its age: it has no
// ended_at, so it has not finished, and evicting a running command would
// delete the row its own completion is about to write.
func TestEvictionLeavesOpenEntriesAlone(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	submitAt(t, led, entryID(1), "local", "/repo", content.EntryShell, "make watch")

	res := evictOK(t, led, content.EvictionRequest{Before: 1 << 40, Max: 100})
	if res.Evicted != 0 {
		t.Fatalf("evicted %d — an entry that never ended is not old, it is unfinished", res.Evicted)
	}
}

// ── pinned artifacts are exempt ──────────────────────────────────────────

// A pin exempts an entry from BACKGROUND eviction (schema question 4): a
// capsule whose content can be evicted underneath it is a broken promise.
// The pinned row stays, its unpinned neighbour goes, and the horizon reflects
// only what actually went.
func TestEvictionExemptsEntriesHoldingAPinnedArtifact(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	// The PINNED entry is the older one — the row eviction would take first —
	// so the exemption is what changes the outcome rather than the order.
	pinned := entryID(1)
	submitAt(t, led, pinned, "local", "/repo", content.EntryShell, "capsule")
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: pinned})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	if _, err := led.AppendArtifact(ctx, content.AppendArtifact{
		ID: "00000000-0000-7000-8000-00000000a001", EntryID: pinned, ExecutionID: &execID,
		MediaType: content.MediaText, Pinned: true,
	}); err != nil {
		t.Fatalf("AppendArtifact: %v", err)
	}
	zero := 0
	payload := content.ShellPayloadJSON(&zero)
	if err := led.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt: 1000, TerminationReason: content.TermCompleted,
		Status: content.EntrySuccess, Payload: &payload,
	}); err != nil {
		t.Fatalf("FinishExecution: %v", err)
	}

	loose := entryID(2)
	submitAt(t, led, loose, "local", "/repo", content.EntryShell, "ordinary")
	closeEntryAt(t, led, loose, 2000)

	res := evictOK(t, led, content.EvictionRequest{Before: 9000, Max: 100})
	if res.Evicted != 1 {
		t.Fatalf("evicted %d, want only the unpinned entry", res.Evicted)
	}
	wantOnly(t, queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10}), pinned)

	// Only the unpinned row's instant is in the horizon.
	if wm := watermarkOK(t, led); wm.Horizon == nil || *wm.Horizon != 2000 {
		t.Fatalf("Horizon = %v, want the evicted row's 2000", wm.Horizon)
	}
}

// ── the wiring: retention actually runs in the product ───────────────────

// The happy path end to end, through the seam a user reaches: with an age
// limit set in Settings, recording a new command is what retires the ones
// retention no longer covers, and the store afterwards states the coverage it
// actually has.
//
// This is the test that would have caught the failure this epic already had
// once (nocx-rtg0, ContentDB.Add): a store whose write path is complete and
// correct and which nothing ever calls. Every other test here drives
// EvictEntries directly and would pass just as well with the production call
// site deleted.
func TestSubmittingACommandRunsRetention(t *testing.T) {
	policy := content.NewPolicy()
	dir := t.TempDir()
	db, err := content.Open(context.Background(), content.Config{
		Path: dir + "/content.db", Key: testKey(), Budget: testBudget, Policy: policy,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()

	// Two commands that finished at the epoch — far outside any age limit.
	seedClosed(t, led, 1000, 2000)
	if wm := watermarkOK(t, led); wm.EvictedCount != 0 {
		t.Fatalf("EvictedCount = %d before retention was switched on", wm.EvictedCount)
	}

	// The user sets an age limit, then runs a command. The command is what
	// gives the store the moment to retire the old rows.
	policy.SetRetentionDays(1)
	submitAt(t, led, entryID(50), "local", "/repo", content.EntryShell, "new work")

	wantOnly(t, queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10}), entryID(50))
	wm := watermarkOK(t, led)
	if wm.EvictedCount != 2 {
		t.Fatalf("EvictedCount = %d after a submit under a 1-day limit, want the 2 ancient rows", wm.EvictedCount)
	}
	if wm.Horizon == nil || *wm.Horizon != 2000 {
		t.Fatalf("Horizon = %v, want 2000", wm.Horizon)
	}

	// And the store says so. The only surviving entry never ended, so
	// MIN(ended_at) is NULL: without the watermark this store would report
	// no horizon at all, having just discarded two days of history.
	cov := coverageOK(t, led)
	if cov == nil {
		t.Fatal("Coverage is nil after retention evicted two entries")
	}
	if *cov != 2000 {
		t.Fatalf("Coverage = %d, want the horizon 2000", *cov)
	}
}

// The mirror of it: with no age limit — the default — a submit evicts
// nothing, however old the store's rows are. Retention that ran when the user
// had not asked for it would be data loss, not housekeeping.
func TestSubmittingACommandEvictsNothingWithoutAnAgeLimit(t *testing.T) {
	_, led := newLedger(t)
	seedClosed(t, led, 1000, 2000)

	submitAt(t, led, entryID(50), "local", "/repo", content.EntryShell, "new work")

	if wm := watermarkOK(t, led); wm.EvictedCount != 0 {
		t.Fatalf("EvictedCount = %d with no retention limit set", wm.EvictedCount)
	}
	if cov := coverageOK(t, led); cov == nil || *cov != 1000 {
		t.Fatalf("Coverage = %v, want the untouched oldest row 1000", cov)
	}
}

// ── the request is refused rather than answered wrongly ──────────────────

func TestEvictionRefusesAnUnusableRequest(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		req  content.EvictionRequest
	}{
		{"a cap of zero", content.EvictionRequest{Before: 1000, Max: 0}},
		{"a negative cap", content.EvictionRequest{Before: 1000, Max: -1}},
		{"a negative cutoff", content.EvictionRequest{Before: -1, Max: 10}},
	} {
		if _, err := led.EvictEntries(ctx, c.req); err == nil {
			t.Fatalf("EvictEntries with %s was accepted", c.name)
		}
	}
}

// ── the prose of one run is retained or evicted as a UNIT ────────────────
//
// ADR-0040 turned an assistant turn's answer from ONE artifact into several:
// each run of prose between two tool calls is its own `text` block with its
// own body. ADR-0019 §7 evicts bodies and leaves entries, and with one answer
// per turn that was unambiguous — the answer was there, or it was plainly
// gone. With N bodies a sweep deciding per artifact can take pieces 1, 3 and
// 7 and leave the rest, and what a reader then meets is a COMPLETE-LOOKING
// answer with its middle missing, which is worse than an answer that is
// plainly gone.
//
// So the unit is the RUN: a sweep that takes one `text` body of a run takes
// all of them. Everything below is that sentence, from both sides — the
// sizes of the pieces DIFFER in every fixture, because a fixture whose pieces
// are all the same size cannot tell the unit rule from a sweep that happened
// to need exactly that many bytes.
//
// The policy sits ON TOP of §7 and does not change it: a command's terminal
// body still evicts independently of the prose around it, every block of the
// turn survives, and only bodies go.

// budgetLedger opens a store whose RETENTION BUDGET is the number given —
// what the size-driven sweep runs against on the write path. Every other
// fixture here uses testBudget (1 GiB), under which no body is ever a
// candidate; a wiring test has to be able to name a budget the store
// actually exceeds.
func budgetLedger(t *testing.T, retentionBytes int64) content.LedgerRepository {
	t.Helper()
	db, err := content.Open(context.Background(), content.Config{
		Path: t.TempDir() + "/content.db", Key: testKey(),
		Budget: content.Budget{
			RetentionBytes:   retentionBytes,
			DiskCeilingBytes: 1 << 20,
			CompactionFloor:  0.8,
		},
	})
	if err != nil {
		t.Fatalf("Open with a %d-byte retention budget: %v", retentionBytes, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()
	envReady(t, led, "local")
	return led
}

// artifactID mints one distinct artifact id per seed, so a fixture can name
// its bodies without a counter in every test.
func artifactID(seed int) string {
	return fmt.Sprintf("aaaaaaaa-0000-7000-8000-%012d", seed)
}

// bodyFor gives a block a body of EXACTLY n bytes and returns the artifact
// id. n is the byte_len the budget then accounts for, which is what lets a
// test name a cutoff that would naturally fall inside a run.
func bodyFor(t *testing.T, led content.LedgerRepository, entry string, exec *int64, media content.MediaType, seed, n int, pinned bool) string {
	t.Helper()
	ctx := context.Background()
	id, err := led.AppendArtifact(ctx, content.AppendArtifact{
		ID: artifactID(seed), EntryID: entry, ExecutionID: exec,
		MediaType: media, Pinned: pinned,
	})
	if err != nil {
		t.Fatalf("AppendArtifact(%s): %v", entry, err)
	}
	if n > 0 {
		if err := led.AppendChunk(ctx, id, 1, bytes.Repeat([]byte("x"), n)); err != nil {
			t.Fatalf("AppendChunk(%s, %d bytes): %v", id, n, err)
		}
	}
	return id
}

// closedTurn records one assistant turn whose run has FINISHED. The sweep
// takes no body out of a block that has not closed — a turn mid-stream is
// still writing the prose the pass would be freeing — so a turn a body test
// wants swept has to be a turn that ended.
func closedTurn(t *testing.T, led content.LedgerRepository, id, question string, endedAt int64) string {
	t.Helper()
	envReady(t, led, "local")
	submitAt(t, led, id, "local", "/repo", content.EntryAsk, question)
	closeEntryAt(t, led, id, endedAt)
	return id
}

// turnWithProse records one FINISHED assistant turn and one run of prose per
// size, each its own `text` block with its own body, seated in the order
// given. It returns the turn and the artifact id of each piece.
func turnWithProse(t *testing.T, led content.LedgerRepository, seed int, sizes ...int) (string, []string) {
	t.Helper()
	envReady(t, led, "local")
	turn := closedTurn(t, led, entryID(seed*100), fmt.Sprintf("question %d", seed), int64(1000+seed))
	arts := make([]string, 0, len(sizes))
	for i, n := range sizes {
		child := entryID(seed*100 + i + 1)
		if err := submitChild(t, led, child, turn, i, content.EntryText, ""); err != nil {
			t.Fatalf("seating prose %d under %s: %v", i, turn, err)
		}
		arts = append(arts, bodyFor(t, led, child, nil, content.MediaText, seed*100+i+1, n, false))
	}
	return turn, arts
}

// commandUnder seats one shell command inside a turn and gives it a terminal
// body of n bytes — the other half of §7's "a command's VT body evicts
// independently".
func commandUnder(t *testing.T, led content.LedgerRepository, turn string, seed, pos, n int, pinned bool) (string, string) {
	t.Helper()
	ctx := context.Background()
	child := entryID(seed)
	// Submitted here rather than through submitChild because this one is
	// driven to a close, and FinishExecution merges its kind arm into the
	// row's payload with json_patch — which has nothing to merge into when
	// the open stored an empty string (the note on submitAt).
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: child, Client: "test-client", EnvironmentID: "local", Cwd: "/repo",
		ParentID: &turn, Pos: intPtr(pos), Kind: content.EntryShell,
		Intent: "du -sh .", Payload: "{}",
	}); err != nil {
		t.Fatalf("seating a command under %s: %v", turn, err)
	}
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: child})
	if err != nil {
		t.Fatalf("StartExecution(%s): %v", child, err)
	}
	art := bodyFor(t, led, child, &execID, content.MediaVT, seed, n, pinned)
	// The command finished: a body is not a candidate while the block that
	// owns it is still printing.
	zero := 0
	payload := content.ShellPayloadJSON(&zero)
	if err := led.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt: int64(2000 + seed), TerminationReason: content.TermCompleted,
		Status: content.EntrySuccess, Payload: &payload,
	}); err != nil {
		t.Fatalf("FinishExecution(%s): %v", child, err)
	}
	return child, art
}

func artifactOK(t *testing.T, led content.LedgerRepository, id string) *content.Artifact {
	t.Helper()
	a, err := led.Artifact(context.Background(), id)
	if err != nil {
		t.Fatalf("Artifact(%s): %v", id, err)
	}
	if a == nil {
		t.Fatalf("Artifact(%s) is gone — §7 evicts BODIES and leaves the rows", id)
	}
	return a
}

// wantBodyGone asserts the body went and its receipt stayed: no chunks, no
// bytes, and the row still saying it was evicted rather than never written.
func wantBodyGone(t *testing.T, led content.LedgerRepository, id, what string) {
	t.Helper()
	a := artifactOK(t, led, id)
	if a.ByteLen != 0 || a.ChunkCount != 0 || len(a.Chunks) != 0 {
		t.Fatalf("%s: byte_len %d, %d chunks — the body is still here", what, a.ByteLen, a.ChunkCount)
	}
	if !a.Evicted {
		t.Fatalf("%s: the body is empty and the row does not say retention took it — "+
			"\"printed nothing\" and \"no longer kept\" are different sentences", what)
	}
}

func wantBodyHeld(t *testing.T, led content.LedgerRepository, id string, n int, what string) {
	t.Helper()
	a := artifactOK(t, led, id)
	if a.ByteLen != int64(n) || a.ChunkCount == 0 {
		t.Fatalf("%s: byte_len %d over %d chunks, want the %d bytes it was given", what, a.ByteLen, a.ChunkCount, n)
	}
	if a.Evicted {
		t.Fatalf("%s: the body is here and the row claims retention took it", what)
	}
}

func evictBodiesOK(t *testing.T, led content.LedgerRepository, req content.BodyEvictionRequest) content.BodyEvictionResult {
	t.Helper()
	res, err := led.EvictBodies(context.Background(), req)
	if err != nil {
		t.Fatalf("EvictBodies(%+v): %v", req, err)
	}
	return res
}

func entryOK(t *testing.T, led content.LedgerRepository, id string) *content.LedgerEntry {
	t.Helper()
	e, err := led.Entry(context.Background(), id)
	if err != nil || e == nil {
		t.Fatalf("Entry(%s): %v (nil=%v)", id, err, e == nil)
	}
	return e
}

func causedOK(t *testing.T, led content.LedgerRepository, id string) []content.CausedEntry {
	t.Helper()
	kids, err := led.Caused(context.Background(), id)
	if err != nil {
		t.Fatalf("Caused(%s): %v", id, err)
	}
	return kids
}

// ── acceptance 1: every piece of the run, or none of it ──────────────────

// The pieces are 100, 700 and 200 bytes, and the budget needs 800 of the
// 1100 the store holds. A sweep deciding per ARTIFACT stops INSIDE the older
// run — 100 + 700 already frees the 800 it needs — and leaves the third
// piece, which is the complete-looking answer with a hole in it. The unit is
// the run, so all three go; and the younger run, which the pass never had to
// reach, keeps all of its.
func TestABudgetSweepTakesEveryProseBodyOfARunOrNone(t *testing.T) {
	_, led := newLedger(t)
	_, older := turnWithProse(t, led, 1, 100, 700, 200)
	_, younger := turnWithProse(t, led, 2, 50, 50)

	res := evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 300, Max: 100})

	if res.Bodies != 3 {
		t.Fatalf("the pass took %d bodies, want the older run's three", res.Bodies)
	}
	if res.BytesFreed != 1000 {
		t.Fatalf("freed %d bytes, want the run's whole 1000", res.BytesFreed)
	}
	if res.RetainedBytes != 100 {
		t.Fatalf("RetainedBytes = %d after the pass, want the younger run's 100", res.RetainedBytes)
	}
	for i, id := range older {
		wantBodyGone(t, led, id, fmt.Sprintf("piece %d of the older run", i))
	}
	for i, id := range younger {
		wantBodyHeld(t, led, id, 50, fmt.Sprintf("piece %d of the younger run", i))
	}
}

// The cap bounds the pass and never SPLITS a run. Under Max 1 the sweep may
// take one unit and stop — but the unit is three bodies, and taking one of
// them is the defect the rule exists to prevent. It overruns the cap by the
// run it is inside, and by nothing else: the younger run is not touched.
func TestTheBodyCapStopsBetweenRunsAndNeverInsideOne(t *testing.T) {
	_, led := newLedger(t)
	_, older := turnWithProse(t, led, 1, 100, 700, 200)
	_, younger := turnWithProse(t, led, 2, 50, 50)

	res := evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 0, Max: 1})

	if res.Bodies != 3 {
		t.Fatalf("the pass took %d bodies under a cap of 1, want the whole run of 3", res.Bodies)
	}
	for i, id := range older {
		wantBodyGone(t, led, id, fmt.Sprintf("piece %d of the older run", i))
	}
	for i, id := range younger {
		wantBodyHeld(t, led, id, 50, fmt.Sprintf("piece %d of the younger run", i))
	}
}

// ── acceptance 2: a command's terminal body evicts independently ─────────

// The terminal body is the older of the two, so it is what the pass reaches
// first, and 900 bytes is all it needs. The prose beside it stays whole —
// §7 is unchanged by the unit rule, and a run is not dragged out with the
// command it announced.
func TestATerminalBodyEvictsWithoutTakingTheProseAroundIt(t *testing.T) {
	_, led := newLedger(t)
	turn := closedTurn(t, led, entryID(100), "how big is it?", 1000)
	_, vt := commandUnder(t, led, turn, 101, 0, 900, false)
	if err := submitChild(t, led, entryID(102), turn, 1, content.EntryText, ""); err != nil {
		t.Fatalf("seating prose: %v", err)
	}
	prose := bodyFor(t, led, entryID(102), nil, content.MediaText, 102, 200, false)

	res := evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 500, Max: 100})

	if res.Bodies != 1 || res.BytesFreed != 900 {
		t.Fatalf("the pass took %d bodies / %d bytes, want the one terminal body of 900",
			res.Bodies, res.BytesFreed)
	}
	wantBodyGone(t, led, vt, "the command's terminal body")
	wantBodyHeld(t, led, prose, 200, "the prose beside the command")
	if entryOK(t, led, turn).ProseEvicted {
		t.Fatal("the turn says its prose is gone after only the command's body went")
	}
}

// And the reverse, which is the half that proves the two are genuinely
// independent rather than merely ordered: the prose is older, so the run goes
// as a unit and the command's terminal body — younger, and never reached —
// keeps every byte.
func TestProseEvictsWithoutTakingTheTerminalBodyBesideIt(t *testing.T) {
	_, led := newLedger(t)
	turn, prose := turnWithProse(t, led, 1, 400, 500)
	_, vt := commandUnder(t, led, turn, 150, 2, 200, false)

	res := evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 500, Max: 100})

	if res.Bodies != 2 || res.BytesFreed != 900 {
		t.Fatalf("the pass took %d bodies / %d bytes, want the run's two pieces and its 900",
			res.Bodies, res.BytesFreed)
	}
	for i, id := range prose {
		wantBodyGone(t, led, id, fmt.Sprintf("piece %d of the run", i))
	}
	wantBodyHeld(t, led, vt, 200, "the command's terminal body")
	if !entryOK(t, led, turn).ProseEvicted {
		t.Fatal("the turn does not say its prose is gone")
	}
}

// ── acceptance 3: only bodies go; every block stays ──────────────────────

// A budget that keeps nothing at all takes every body in the store, and the
// TREE is exactly what it was: the same children, in the same seats, with
// the same kinds — prose included. Nothing is deleted; §7 evicts bodies and
// leaves entries, and the artifact rows stay as the receipt that there was a
// body here.
func TestATurnWhoseProseWasEvictedKeepsEveryBlockItHad(t *testing.T) {
	_, led := newLedger(t)
	turn, prose := turnWithProse(t, led, 1, 300, 300)
	cmd, vt := commandUnder(t, led, turn, 150, 2, 400, false)
	before := causedOK(t, led, turn)
	if len(before) != 3 {
		t.Fatalf("the fixture turn has %d children, want 3", len(before))
	}

	evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 0, Max: 100})

	after := causedOK(t, led, turn)
	if len(after) != len(before) {
		t.Fatalf("the turn has %d children after the sweep, want the %d it had", len(after), len(before))
	}
	// reflect.DeepEqual rather than ==: a child carries the arguments the call
	// asked for (ADR-0040), which is a map, and a struct holding one is not
	// comparable. The claim is unchanged — the same children, in the same
	// seats, with the same facts.
	for i := range before {
		if !reflect.DeepEqual(after[i], before[i]) {
			t.Fatalf("child %d changed across the sweep: %+v, want %+v", i, after[i], before[i])
		}
	}
	// Each block is still readable as itself, and the bodies' rows are still
	// there saying what happened to them.
	for _, id := range append([]string{turn, cmd}, entryID(101), entryID(102)) {
		if e := entryOK(t, led, id); e.ID != id {
			t.Fatalf("Entry(%s) came back as %s", id, e.ID)
		}
	}
	for i, id := range prose {
		wantBodyGone(t, led, id, fmt.Sprintf("piece %d", i))
	}
	wantBodyGone(t, led, vt, "the command's terminal body")
}

// ── acceptance 4: the turn says it ONCE ──────────────────────────────────

// A soft degrade must be visible in the product (AGENTS.md), and a turn whose
// prose was evicted says so once — not once per piece. The fact is the RUN's,
// because the unit is the run: a turn of three pieces and a turn of one
// report the same single answer, and the tree read a reader assembles the
// turn from says nothing per child at all, so there is nothing to repeat.
func TestATurnSaysItsProseIsGoneOnceNotOncePerPiece(t *testing.T) {
	_, led := newLedger(t)
	three, _ := turnWithProse(t, led, 1, 100, 700, 200)
	one, _ := turnWithProse(t, led, 2, 400)
	kidsBefore := causedOK(t, led, three)

	// The paired "before": neither turn claims anything is missing while
	// every body is in place.
	if entryOK(t, led, three).ProseEvicted || entryOK(t, led, one).ProseEvicted {
		t.Fatal("a turn says its prose is gone while it is still stored")
	}

	evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 0, Max: 100})

	if !entryOK(t, led, three).ProseEvicted {
		t.Fatal("the three-piece turn does not say its prose is gone")
	}
	if !entryOK(t, led, one).ProseEvicted {
		t.Fatal("the one-piece turn does not say its prose is gone")
	}
	// The same single fact whatever the run was cut into — and the children
	// come back unchanged, so a reader drawing the turn from its tree has one
	// sentence to say and no per-piece marker to repeat it with.
	kidsAfter := causedOK(t, led, three)
	if len(kidsAfter) != len(kidsBefore) {
		t.Fatalf("the tree read changed shape across the sweep: %d children, want %d",
			len(kidsAfter), len(kidsBefore))
	}
	// DeepEqual, for the reason the sweep above uses it: a child carries the
	// arguments the call asked for, which is a map.
	for i := range kidsBefore {
		if !reflect.DeepEqual(kidsAfter[i], kidsBefore[i]) {
			t.Fatalf("child %d of the tree read changed across the sweep: %+v, want %+v",
				i, kidsAfter[i], kidsBefore[i])
		}
	}
}

// ── acceptance 5: a pin is exempt, and a run is pinned as a unit ─────────

// A pinned body is exempt from the background sweep for the reason schema
// question 4 gives: a capsule whose content can be evicted underneath it is a
// broken promise. The pinned body here is the OLDEST — the one the pass
// reaches first — so the exemption is what changes the outcome rather than
// the order.
func TestABudgetSweepExemptsAPinnedBody(t *testing.T) {
	_, led := newLedger(t)
	turn := closedTurn(t, led, entryID(100), "keep this", 1000)
	_, pinned := commandUnder(t, led, turn, 101, 0, 600, true)
	_, prose := turnWithProse(t, led, 2, 400)

	res := evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 500, Max: 100})

	if res.Bodies != 1 || res.BytesFreed != 400 {
		t.Fatalf("the pass took %d bodies / %d bytes, want the one unpinned body of 400",
			res.Bodies, res.BytesFreed)
	}
	wantBodyHeld(t, led, pinned, 600, "the pinned terminal body")
	wantBodyGone(t, led, prose[0], "the unpinned run")
}

// The paired case, and the one the unit rule adds: a pin on ONE piece of a
// run keeps the WHOLE run. Prose is retained or evicted as a unit, so a run
// that cannot be evicted whole is not evicted at all — the alternative is a
// pinned sentence surrounded by holes, which is the same complete-looking
// answer with its middle missing.
func TestProsePinnedAnywhereInARunKeepsTheWholeRun(t *testing.T) {
	_, led := newLedger(t)
	turn := closedTurn(t, led, entryID(100), "explain it", 1000)
	sizes := []int{100, 700, 200}
	arts := make([]string, 0, len(sizes))
	for i, n := range sizes {
		child := entryID(101 + i)
		if err := submitChild(t, led, child, turn, i, content.EntryText, ""); err != nil {
			t.Fatalf("seating prose %d: %v", i, err)
		}
		arts = append(arts, bodyFor(t, led, child, nil, content.MediaText, 101+i, n, i == 1))
	}
	_, younger := turnWithProse(t, led, 2, 400)

	res := evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 100, Max: 100})

	if res.Bodies != 1 || res.BytesFreed != 400 {
		t.Fatalf("the pass took %d bodies / %d bytes, want only the younger run's 400",
			res.Bodies, res.BytesFreed)
	}
	for i, id := range arts {
		wantBodyHeld(t, led, id, sizes[i], fmt.Sprintf("piece %d of the pinned run", i))
	}
	wantBodyGone(t, led, younger[0], "the younger run")
	if entryOK(t, led, turn).ProseEvicted {
		t.Fatal("the pinned turn says its prose is gone")
	}
}

// And the mirror of it, which is what makes the exemption an exemption
// rather than the sweep simply never reaching that run: the same fixture
// with nothing pinned loses all three pieces.
func TestTheSameRunGoesWholeWhenNothingInItIsPinned(t *testing.T) {
	_, led := newLedger(t)
	_, arts := turnWithProse(t, led, 1, 100, 700, 200)
	_, younger := turnWithProse(t, led, 2, 400)

	res := evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 100, Max: 100})

	if res.Bodies != 4 || res.BytesFreed != 1400 {
		t.Fatalf("the pass took %d bodies / %d bytes, want all four and 1400", res.Bodies, res.BytesFreed)
	}
	for i, id := range arts {
		wantBodyGone(t, led, id, fmt.Sprintf("piece %d of the unpinned run", i))
	}
	wantBodyGone(t, led, younger[0], "the younger run")
}

// ── acceptance 6: and under an ordinary budget, nothing goes at all ──────

// The pairing AGENTS.md rule 3 asks for, driven directly: a budget the store
// is inside takes nothing, and says so as a pass that removed nothing rather
// than as a horizon nobody asked it to move.
func TestABudgetTheStoreIsInsideEvictsNoBodyAtAll(t *testing.T) {
	_, led := newLedger(t)
	_, prose := turnWithProse(t, led, 1, 100, 700, 200)
	_, vt := commandUnder(t, led, entryID(100), 150, 3, 400, false)

	res := evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 1 << 20, Max: 100})

	if res.Bodies != 0 || res.BytesFreed != 0 {
		t.Fatalf("the pass took %d bodies / %d bytes under a budget nothing exceeds", res.Bodies, res.BytesFreed)
	}
	if res.RetainedBytes != 1400 {
		t.Fatalf("RetainedBytes = %d, want the 1400 the store holds", res.RetainedBytes)
	}
	sizes := []int{100, 700, 200}
	for i, id := range prose {
		wantBodyHeld(t, led, id, sizes[i], fmt.Sprintf("piece %d", i))
	}
	wantBodyHeld(t, led, vt, 400, "the terminal body")
	if entryOK(t, led, entryID(100)).ProseEvicted {
		t.Fatal("a turn under an ordinary budget says its prose is gone")
	}
}

// An empty body and an evicted one are both zero bytes, and they are not the
// same fact: "this command printed nothing" and "this output is no longer
// kept" are different sentences and must stay so. byte_len cannot tell them
// apart — only the receipt can.
func TestAnEvictedBodyIsDistinguishableFromOneThatPrintedNothing(t *testing.T) {
	_, led := newLedger(t)
	turn := closedTurn(t, led, entryID(100), "run both", 1000)
	_, quiet := commandUnder(t, led, turn, 101, 0, 0, false)
	_, loud := commandUnder(t, led, turn, 102, 1, 500, false)

	evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 0, Max: 100})

	wantBodyGone(t, led, loud, "the body retention took")
	if a := artifactOK(t, led, quiet); a.Evicted {
		t.Fatal("a command that printed nothing is reported as one whose output was evicted")
	}
}

// A run that is still being written keeps its prose whatever the budget
// says. The pass frees bodies from a block that has CLOSED — the same rule
// the age sweep states as "an entry that never ended is unfinished, not old"
// — because freeing the body of a turn mid-stream would tear the answer out
// from under the deltas still arriving. For prose the block that must have
// closed is the RUN: a `text` block is born closed (ADR-0040) and cannot
// speak for itself here.
//
// Paired with the closed turn beside it, which loses everything: the rule is
// about the turn's state and not about the pass being unable to reach it.
func TestABudgetSweepLeavesTheProseOfARunThatHasNotFinished(t *testing.T) {
	_, led := newLedger(t)
	envReady(t, led, "local")
	// Still streaming: submitted, never closed.
	live := entryID(100)
	submitAt(t, led, live, "local", "/repo", content.EntryAsk, "still thinking")
	if err := submitChild(t, led, entryID(101), live, 0, content.EntryText, ""); err != nil {
		t.Fatalf("seating prose: %v", err)
	}
	liveProse := bodyFor(t, led, entryID(101), nil, content.MediaText, 101, 500, false)
	_, finished := turnWithProse(t, led, 2, 300)

	res := evictBodiesOK(t, led, content.BodyEvictionRequest{KeepBytes: 0, Max: 100})

	if res.Bodies != 1 || res.BytesFreed != 300 {
		t.Fatalf("the pass took %d bodies / %d bytes, want only the finished turn's 300",
			res.Bodies, res.BytesFreed)
	}
	wantBodyHeld(t, led, liveProse, 500, "the prose of a turn still writing")
	wantBodyGone(t, led, finished[0], "the prose of the finished turn")
	if entryOK(t, led, live).ProseEvicted {
		t.Fatal("a turn still writing says its prose is gone")
	}
}

// ── the wiring: the budget sweep actually runs in the product ────────────

// The happy path through the seam a user reaches. The budget is 500 bytes;
// the assistant writes one turn of 800 bytes of prose, and recording the NEXT
// turn is what retires it — whole, because the unit is the run.
//
// Every other test here drives EvictBodies directly and would pass just as
// well with the production call site deleted (nocx-rtg0, ContentDB.Add).
func TestRecordingTheNextTurnSweepsTheProseTheBudgetNoLongerCovers(t *testing.T) {
	led := budgetLedger(t, 500)
	_, first := turnWithProse(t, led, 1, 400, 400)

	// Still there: the sweep has had no moment to run since the second body
	// landed, and a body is not evicted by being written.
	wantBodyHeld(t, led, first[0], 400, "the first piece before the next turn")

	_, second := turnWithProse(t, led, 2, 100)

	for i, id := range first {
		wantBodyGone(t, led, id, fmt.Sprintf("piece %d of the first turn", i))
	}
	wantBodyHeld(t, led, second[0], 100, "the new turn's prose")
	if !entryOK(t, led, entryID(100)).ProseEvicted {
		t.Fatal("the swept turn does not say its prose is gone")
	}
}

// And the pairing: the same writes under an ordinary budget evict nothing.
// A sweep that ran when the user was nowhere near their budget would be data
// loss, not housekeeping.
func TestRecordingTheNextTurnEvictsNothingUnderAnOrdinaryBudget(t *testing.T) {
	led := budgetLedger(t, 1<<20)
	_, first := turnWithProse(t, led, 1, 400, 400)
	_, second := turnWithProse(t, led, 2, 100)

	for i, id := range first {
		wantBodyHeld(t, led, id, 400, fmt.Sprintf("piece %d of the first turn", i))
	}
	wantBodyHeld(t, led, second[0], 100, "the new turn's prose")
}

// ── the request is refused rather than answered wrongly ──────────────────

func TestBodyEvictionRefusesAnUnusableRequest(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		req  content.BodyEvictionRequest
	}{
		{"a cap of zero", content.BodyEvictionRequest{KeepBytes: 100, Max: 0}},
		{"a negative cap", content.BodyEvictionRequest{KeepBytes: 100, Max: -1}},
		{"a negative budget", content.BodyEvictionRequest{KeepBytes: -1, Max: 10}},
	} {
		if _, err := led.EvictBodies(ctx, c.req); err == nil {
			t.Fatalf("EvictBodies with %s was accepted", c.name)
		}
	}
}

// ── the age sweep meets the tree ─────────────────────────────────────────

// A `text` block cannot exist without its parent — the CHECK says
// parent_id IS NOT NULL — so a turn that ages out takes its prose with it.
// Before ADR-0040 landed there was nothing under a turn to take; afterwards
// the entry sweep's DELETE hit the CHECK through parent_id's ON DELETE SET
// NULL, and because eviction on the write path is best-effort the whole
// subsystem failed silently on the first turn old enough to be a candidate.
//
// What is NOT dragged out with it is the command the turn ran: a tool call
// whose turn was evicted is still a command that ran, and it keeps its row
// with a null parent, exactly as the schema comment says.
func TestEvictingATurnTakesTheProseItHeldAndNothingElse(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	turn, _ := turnWithProse(t, led, 1, 100, 200)
	// A command the turn ran, still running: it has no ended_at, so it is
	// not a candidate on its own, and what happens to it is decided purely
	// by what happens to its parent.
	cmd := entryID(150)
	if err := submitChild(t, led, cmd, turn, 2, content.EntryShell, "make watch"); err != nil {
		t.Fatalf("seating the command: %v", err)
	}

	res := evictOK(t, led, content.EvictionRequest{Before: 9000, Max: 100})

	if res.Evicted != 3 {
		t.Fatalf("evicted %d rows, want the turn and the two runs of prose it held", res.Evicted)
	}
	if e, err := led.Entry(ctx, turn); err != nil || e != nil {
		t.Fatalf("the turn survived its own eviction: %+v (%v)", e, err)
	}
	for _, id := range []string{entryID(101), entryID(102)} {
		e, err := led.Entry(ctx, id)
		if err != nil {
			t.Fatalf("Entry(%s): %v", id, err)
		}
		if e != nil {
			t.Fatalf("a run of prose outlived the turn it was inside: %+v", e)
		}
	}
	// The command the turn ran is still a command that ran.
	survivor := entryOK(t, led, cmd)
	if survivor.ParentID != nil {
		t.Fatalf("the command still points at the turn that is gone: %v", survivor.ParentID)
	}
}

// And the pin travels with the unit here too: a pinned body anywhere in the
// run exempts the turn, because evicting the turn would take the prose that
// holds the pin. The paired ordinary case is the test above, whose identical
// fixture with nothing pinned loses all three rows.
func TestAPinnedProseBodyExemptsTheTurnFromTheAgeSweep(t *testing.T) {
	_, led := newLedger(t)
	turn := closedTurn(t, led, entryID(100), "keep this", 1000)
	if err := submitChild(t, led, entryID(101), turn, 0, content.EntryText, ""); err != nil {
		t.Fatalf("seating prose: %v", err)
	}
	bodyFor(t, led, entryID(101), nil, content.MediaText, 101, 300, true)
	closeEntryAt(t, led, turn, 1000)

	res := evictOK(t, led, content.EvictionRequest{Before: 9000, Max: 100})

	if res.Evicted != 0 {
		t.Fatalf("evicted %d rows, want none — the run holds a pinned body", res.Evicted)
	}
	if e := entryOK(t, led, turn); e.ID != turn {
		t.Fatalf("Entry(%s) came back as %s", turn, e.ID)
	}
	if e := entryOK(t, led, entryID(101)); e.ID != entryID(101) {
		t.Fatal("the pinned run of prose is gone")
	}
}
