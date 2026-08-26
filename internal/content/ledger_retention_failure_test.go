package content_test

// The failure paths of eviction (nocx-rtg0.12), one test per external call it
// makes — AGENTS.md testing rule 3: "for every external call your code makes,
// there is a test where that call fails".
//
// Each case forces the failure at a different statement and then asserts the
// SAME invariant from the other side: entries and the watermark move together
// or not at all. That is what makes these more than error-plumbing tests —
// a DELETE that committed while its watermark write failed would leave the
// store silently claiming coverage it lost, which is the exact defect the
// watermark exists to prevent.
//
// The faults are injected with SQLite triggers on a second connection to the
// same encrypted file (rawLedger). A trigger that RAISEs is a real statement
// failure at a real call site, deterministic and with no timing in it.

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

// storeState is what every case below compares before and after: how many
// entries survive and what the watermark says. The pair moving together is
// the invariant; either moving alone is the defect.
type storeState struct {
	entries int
	count   int64
	horizon *int64
}

func readState(t *testing.T, led content.LedgerRepository) storeState {
	t.Helper()
	page := queryOK(t, led, content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 100})
	wm := watermarkOK(t, led)
	return storeState{entries: len(page.Entries), count: wm.EvictedCount, horizon: wm.Horizon}
}

func (s storeState) equal(o storeState) bool {
	if s.entries != o.entries || s.count != o.count {
		return false
	}
	switch {
	case s.horizon == nil && o.horizon == nil:
		return true
	case s.horizon == nil || o.horizon == nil:
		return false
	default:
		return *s.horizon == *o.horizon
	}
}

// The DELETE fails. Nothing may be removed and the watermark may not move:
// no watermark without its deletion.
func TestEvictionRollsBackWhenTheDeleteFails(t *testing.T) {
	_, led, path := newLedgerAt(t)
	seedClosed(t, led, 1000, 2000, 3000)
	before := readState(t, led)

	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		`CREATE TRIGGER evict_delete_boom BEFORE DELETE ON entries
		 BEGIN SELECT RAISE(ABORT, 'delete refused'); END`,
	); err != nil {
		t.Fatalf("install delete trigger: %v", err)
	}

	if _, err := led.EvictEntries(context.Background(),
		content.EvictionRequest{Before: 9000, Max: 100}); err == nil {
		t.Fatal("EvictEntries succeeded while the DELETE was refused")
	}

	after := readState(t, led)
	if !before.equal(after) {
		t.Fatalf("state moved on a failed DELETE: before %+v, after %+v", before, after)
	}
	if after.count != 0 || after.horizon != nil {
		t.Fatalf("watermark = (%d, %v) after a DELETE that never happened", after.count, after.horizon)
	}
}

// The watermark UPDATE fails. The rows it was about to account for must still
// be there: no deletion without its watermark. This is the direction that
// actually loses data if the two are not one transaction — the rows would be
// gone and nothing would record that they ever existed.
func TestEvictionRollsBackWhenTheWatermarkWriteFails(t *testing.T) {
	_, led, path := newLedgerAt(t)
	seedClosed(t, led, 1000, 2000, 3000)
	before := readState(t, led)
	if before.entries != 3 {
		t.Fatalf("fixture has %d entries, want 3", before.entries)
	}

	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		`CREATE TRIGGER evict_watermark_boom BEFORE UPDATE ON retention_watermark
		 BEGIN SELECT RAISE(ABORT, 'watermark refused'); END`,
	); err != nil {
		t.Fatalf("install watermark trigger: %v", err)
	}

	if _, err := led.EvictEntries(context.Background(),
		content.EvictionRequest{Before: 9000, Max: 100}); err == nil {
		t.Fatal("EvictEntries succeeded while the watermark write was refused")
	}

	after := readState(t, led)
	if after.entries != 3 {
		t.Fatalf("entries = %d after a failed watermark write — rows were deleted with nothing recording it", after.entries)
	}
	if !before.equal(after) {
		t.Fatalf("state moved on a failed watermark write: before %+v, after %+v", before, after)
	}
}

// The victim SELECT fails. Eviction reads the pin exemption through the
// artifacts table; without it there is no way to tell an exempt row from an
// ordinary one, so the pass must refuse rather than evict everything.
func TestEvictionFailsWhenTheVictimSelectFails(t *testing.T) {
	_, led, path := newLedgerAt(t)
	seedClosed(t, led, 1000, 2000)
	before := readState(t, led)

	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		`CREATE TRIGGER evict_select_boom BEFORE DELETE ON artifacts
		 BEGIN SELECT RAISE(ABORT, 'unused'); END`,
		`DROP TABLE artifact_chunks`,
		`DROP TABLE artifacts`,
	); err != nil {
		t.Fatalf("drop artifacts: %v", err)
	}

	if _, err := led.EvictEntries(context.Background(),
		content.EvictionRequest{Before: 9000, Max: 100}); err == nil {
		t.Fatal("EvictEntries succeeded while it could not read the pin exemption")
	}

	after := readState(t, led)
	if after.entries != before.entries {
		t.Fatalf("entries = %d after a failed victim select, want the %d it started with", after.entries, before.entries)
	}
	if after.count != 0 {
		t.Fatalf("EvictedCount = %d after a pass that never selected a victim", after.count)
	}
}

// The transaction cannot even begin: the store is closed. Eviction must
// report it rather than pretend a pass happened.
func TestEvictionFailsOnAClosedStore(t *testing.T) {
	db, led := newLedger(t)
	seedClosed(t, led, 1000, 2000)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := led.EvictEntries(context.Background(),
		content.EvictionRequest{Before: 9000, Max: 100}); err == nil {
		t.Fatal("EvictEntries succeeded on a closed store")
	}
}

// Reading the watermark is its own external call, and it fails the same way.
// A Watermark that swallowed the error would report a never-evicted store —
// count 0, no horizon — which is precisely the false "full coverage" answer.
func TestWatermarkReadFailsOnAClosedStore(t *testing.T) {
	db, led := newLedger(t)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := led.Watermark(context.Background()); err == nil {
		t.Fatal("Watermark succeeded on a closed store")
	}
}

// Coverage reads the watermark inside the query's transaction. When that read
// fails the whole query must fail: answering the page while silently dropping
// the horizon is the soft degrade AGENTS.md forbids — the overlay would say
// "searched everything" over a store that had been evicted.
func TestQueryFailsWhenTheWatermarkCannotBeRead(t *testing.T) {
	_, led, path := newLedgerAt(t)
	seedClosed(t, led, 1000, 2000)

	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		`DROP TABLE retention_watermark`,
	); err != nil {
		t.Fatalf("drop watermark: %v", err)
	}

	if _, err := led.QueryEntries(context.Background(),
		content.LedgerQuery{Scope: content.ScopeEverywhere, Limit: 10}); err == nil {
		t.Fatal("QueryEntries answered with a coverage it could not read")
	}
}

// ── the failure paths of the BODY sweep ──────────────────────────────────
//
// The same rule as above, one table down: the chunks and the byte accounting
// that describes them move together or not at all. A pass that deleted the
// chunks while its receipt write failed would leave the store holding an
// artifact that claims bytes nothing can read — and the budget would go on
// counting them, so the next pass would evict something else to free space
// already freed.

// bodyState is the pair the invariant is about: the bytes the store says it
// retains, and the chunks actually behind them.
func bodyState(t *testing.T, led content.LedgerRepository, ids ...string) (int64, int) {
	t.Helper()
	var bytesHeld int64
	var chunks int
	for _, id := range ids {
		a := artifactOK(t, led, id)
		bytesHeld += a.ByteLen
		chunks += a.ChunkCount
	}
	return bytesHeld, chunks
}

// The chunk DELETE fails. Nothing may be freed and no receipt may be written.
func TestBodyEvictionRollsBackWhenTheChunkDeleteFails(t *testing.T) {
	db, led, path := newLedgerAt(t)
	_ = db
	_, prose := turnWithProse(t, led, 1, 100, 700)
	beforeBytes, beforeChunks := bodyState(t, led, prose...)

	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		`CREATE TRIGGER evict_chunk_boom BEFORE DELETE ON artifact_chunks
		 BEGIN SELECT RAISE(ABORT, 'chunk delete refused'); END`,
	); err != nil {
		t.Fatalf("install chunk trigger: %v", err)
	}

	if _, err := led.EvictBodies(context.Background(),
		content.BodyEvictionRequest{KeepBytes: 0, Max: 100}); err == nil {
		t.Fatal("EvictBodies succeeded while the chunk DELETE was refused")
	}

	afterBytes, afterChunks := bodyState(t, led, prose...)
	if afterBytes != beforeBytes || afterChunks != beforeChunks {
		t.Fatalf("bodies moved on a failed DELETE: %d bytes over %d chunks, want %d over %d",
			afterBytes, afterChunks, beforeBytes, beforeChunks)
	}
	for _, id := range prose {
		if artifactOK(t, led, id).Evicted {
			t.Fatalf("%s carries an eviction receipt for a pass that removed nothing", id)
		}
	}
}

// The receipt write fails. The chunks must still be there: a body freed with
// nothing recording it is the direction that actually loses the answer, since
// a reader would then see an empty body it cannot tell from a silent command.
func TestBodyEvictionRollsBackWhenTheReceiptWriteFails(t *testing.T) {
	_, led, path := newLedgerAt(t)
	_, prose := turnWithProse(t, led, 1, 100, 700)
	beforeBytes, beforeChunks := bodyState(t, led, prose...)

	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		`CREATE TRIGGER evict_receipt_boom BEFORE UPDATE ON artifacts
		 BEGIN SELECT RAISE(ABORT, 'receipt refused'); END`,
	); err != nil {
		t.Fatalf("install receipt trigger: %v", err)
	}

	if _, err := led.EvictBodies(context.Background(),
		content.BodyEvictionRequest{KeepBytes: 0, Max: 100}); err == nil {
		t.Fatal("EvictBodies succeeded while the receipt write was refused")
	}

	afterBytes, afterChunks := bodyState(t, led, prose...)
	if afterChunks != beforeChunks {
		t.Fatalf("chunks = %d after a failed receipt write, want the %d they started with — "+
			"the bodies were freed with nothing recording it", afterChunks, beforeChunks)
	}
	if afterBytes != beforeBytes {
		t.Fatalf("byte_len = %d after a failed receipt write, want %d", afterBytes, beforeBytes)
	}
}

// The candidate SELECT fails. The sweep reads what it retains and what is
// pinned through the artifacts table; without it there is no way to tell an
// exempt body from an ordinary one, so the pass must refuse rather than
// free everything.
func TestBodyEvictionFailsWhenTheCandidateSelectFails(t *testing.T) {
	_, led, path := newLedgerAt(t)
	turnWithProse(t, led, 1, 100, 700)

	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		`DROP TABLE artifact_chunks`,
		`DROP TABLE artifacts`,
	); err != nil {
		t.Fatalf("drop artifacts: %v", err)
	}

	if _, err := led.EvictBodies(context.Background(),
		content.BodyEvictionRequest{KeepBytes: 0, Max: 100}); err == nil {
		t.Fatal("EvictBodies succeeded while it could not read what it retains")
	}
}

// The transaction cannot even begin: the store is closed.
func TestBodyEvictionFailsOnAClosedStore(t *testing.T) {
	db, led := newLedger(t)
	turnWithProse(t, led, 1, 100)
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := led.EvictBodies(context.Background(),
		content.BodyEvictionRequest{KeepBytes: 0, Max: 100}); err == nil {
		t.Fatal("EvictBodies succeeded on a closed store")
	}
}
