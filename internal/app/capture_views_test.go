package app

// The derived views (nocx-2v80t.2.5) at the STORE seam: a kept settled
// record carries two view rows beside it — the card's SGR grid and the
// searchable plain text — and a view row is an ADDRESS and a provenance
// chain, never a copy: media type and derived_from name what it is and
// where it comes from, the record's retention state rides along, and there
// are NO chunks, because the body is derived from the record's stored bytes
// at the read (proven over the socket in internal/transport). What this
// file proves is the WIRING: which records grow views, which never do, and
// that the views do not depend on the session they arrived through.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/captureview"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// recordWithWords builds one settled record whose departed rows and closing
// screen carry distinct words the wire-level assertions can hunt for.
func recordWithWords(nonce, departedWord, closingWord string, cols int) proto.CaptureParams {
	rec := aCaptureRecord(nonce)
	rec.Closing = proto.CaptureScreen{
		Cols: cols, Rows: 2,
		Lines: []proto.CaptureRow{captureRowOf("screen says "+closingWord, false, false)},
	}
	rec.Departed = []proto.CaptureRow{
		captureRowOf("history held "+departedWord, false, false),
	}
	return rec
}

func storeSettled(t *testing.T, db content.ContentDB, sink *captureSink, entryID, nonce string, rec proto.CaptureParams) {
	t.Helper()
	sink.binds.Bind(nonce, entryID)
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal the record: %v", err)
	}
	out, captureErr := sink.capture(context.Background(), raw)
	if captureErr != nil {
		t.Fatalf("capture: %v", captureErr)
	}
	result, ok := out.(proto.CaptureResult)
	if !ok || !result.Kept {
		t.Fatalf("capture answered %+v, want kept", out)
	}
}

// assertViewRow is the store-level shape of one view: the metadata names
// the record, the retention state is inherited, and NO body is stored — a
// view row that carried chunks would be a fourth copy of the output, and
// the point of this slice is that the record is the one.
func assertViewRow(t *testing.T, db content.ContentDB, recordID string, kind captureview.Kind, mediaType content.MediaType, wantTrunc *content.Truncation) *content.Artifact {
	t.Helper()
	id := captureview.ViewID(recordID, kind)
	// The chain: the vt view names the record; the text view names the vt
	// view. DerivedFrom is the provenance ADR-0019 §6 requires — readable
	// back, never a second answer to "where did this come from".
	wantDerived := recordID
	if kind == captureview.KindText {
		wantDerived = captureview.ViewID(recordID, captureview.KindVT)
	}
	art, err := db.Ledger().Artifact(context.Background(), id)
	if err != nil {
		t.Fatalf("Artifact(view %s): %v", id, err)
	}
	if art == nil {
		t.Fatalf("no view artifact carries %s — the views are not wired", id)
	}
	if art.MediaType != mediaType {
		t.Fatalf("view %s media type = %q, want %q", id, art.MediaType, mediaType)
	}
	if art.DerivedFrom == nil || *art.DerivedFrom != wantDerived {
		t.Fatalf("view %s derivedFrom = %v, want %s", id, art.DerivedFrom, wantDerived)
	}
	if len(art.Chunks) != 0 || art.ByteLen != 0 {
		t.Fatalf("view %s stored a body: %d bytes in %d chunks — a view is an address, not a copy",
			id, art.ByteLen, len(art.Chunks))
	}
	if (art.Truncated == nil) != (wantTrunc == nil) ||
		(wantTrunc != nil && *art.Truncated != *wantTrunc) {
		t.Fatalf("view %s truncated = %v, want %v — the record's retention state must ride along",
			id, art.Truncated, wantTrunc)
	}
	return art
}

// Criterion 1's wiring half, in one test: two different stored captures,
// and each carries BOTH view rows — every view names ITS record, and a
// second record never reuses the first one's addresses. (That the views'
// derived bodies move with the record is asserted where bodies exist: over
// the socket, in internal/transport's view tests.)
func TestChangingTheStoredCaptureChangesBothViews(t *testing.T) {
	db := captureTestStore(t, content.CriticalityRoutine, nil)
	entryID := recordOneCommand(t, db, "make")
	sink := captureSinkFor(db, nil)

	nonceA, nonceB := fenceHex(0xB1), fenceHex(0xB2)
	storeSettled(t, db, sink, entryID, nonceA, recordWithWords(nonceA, "firstOutput", "firstEnd", 40))
	storeSettled(t, db, sink, entryID, nonceB, recordWithWords(nonceB, "secondOutput", "secondEnd", 40))

	recordA, recordB := captureArtifactID(nonceA), captureArtifactID(nonceB)
	for _, recordID := range []string{recordA, recordB} {
		assertViewRow(t, db, recordID, captureview.KindVT, content.MediaVT, nil)
		assertViewRow(t, db, recordID, captureview.KindText, content.MediaText, nil)
	}
	// The change: A's views and B's views live at different addresses —
	// a view is a view of ONE stored capture, never of "the entry's last
	// word".
	if captureview.ViewID(recordA, captureview.KindVT) == captureview.ViewID(recordB, captureview.KindVT) {
		t.Fatal("two captures share one view address")
	}
}

// Criterion 2's wiring half: the text view EXISTS for a record with
// departed rows — the view the search will read. That its derived body
// holds a word that scrolled away is asserted off the socket, where bodies
// are derived (internal/transport).
func TestTheTextCoversRowsThatScrolledAway(t *testing.T) {
	db := captureTestStore(t, content.CriticalityRoutine, nil)
	entryID := recordOneCommand(t, db, "cat long.log")
	sink := captureSinkFor(db, nil)

	nonce := fenceHex(0xC4)
	rec := aCaptureRecord(nonce)
	rec.Departed = []proto.CaptureRow{
		captureRowOf("the error was EACCESQUX mid-run", false, false),
	}
	storeSettled(t, db, sink, entryID, nonce, rec)

	assertViewRow(t, db, captureArtifactID(nonce), captureview.KindText, content.MediaText, nil)
}

// Criterion 3: the view rows live in the STORE, addressed by the record —
// so they answer after everything the pane and its session contributed is
// gone, the same way the record itself does. The fence→entry memory the
// record arrived through is dropped between the write and the reads.
func TestTheViewsReadTheSameAfterThePaneIsGone(t *testing.T) {
	db := captureTestStore(t, content.CriticalityRoutine, nil)
	entryID := recordOneCommand(t, db, "make")
	sink := captureSinkFor(db, nil)

	nonce := fenceHex(0xD1)
	storeSettled(t, db, sink, entryID, nonce, recordWithWords(nonce, "keptOutput", "keptEnd", 40))
	recordID := captureArtifactID(nonce)
	assertViewRow(t, db, recordID, captureview.KindVT, content.MediaVT, nil)
	assertViewRow(t, db, recordID, captureview.KindText, content.MediaText, nil)

	// Everything the pane and its session contributed is gone: the binds
	// memory is dropped, the way the helper connection is on a close.
	sink.binds = newCaptureBindings()

	assertViewRow(t, db, recordID, captureview.KindVT, content.MediaVT, nil)
	assertViewRow(t, db, recordID, captureview.KindText, content.MediaText, nil)
}

// Criterion 4's wiring half: a record the cap cut to a summary passes the
// marker to BOTH view rows — a summary that did not say so on the views
// would read back as whole. (That the bodies are views of the stored
// SUMMARY is asserted off the socket.)
func TestWithRetentionKeptOnlyASummaryBothViewsSaySo(t *testing.T) {
	db := captureTestStore(t, content.CriticalityRoutine, nil)
	entryID := recordOneCommand(t, db, "seq big")
	sink := captureSinkFor(db, nil)

	nonce := fenceHex(0xE4)
	rec := aCaptureRecord(nonce)
	rows := make([]proto.CaptureRow, 0, 6000)
	for i := 0; i < 6000; i++ {
		rows = append(rows, captureRowOf(fmt.Sprintf("row %04d padding padding padding", i), false, false))
	}
	rec.Departed = rows
	storeSettled(t, db, sink, entryID, nonce, rec)

	cap := content.TruncCap
	recordID := captureArtifactID(nonce)
	assertViewRow(t, db, recordID, captureview.KindVT, content.MediaVT, &cap)
	assertViewRow(t, db, recordID, captureview.KindText, content.MediaText, &cap)
}

// The selection rule: ONE settled record's views per entry. An open
// interval's unfinished record produces none — a running command's card has
// no body to draw, exactly as before this slice.
func TestAnUnfinishedRecordProducesNoViews(t *testing.T) {
	db := captureTestStore(t, content.CriticalityRoutine, nil)
	entryID := recordOneCommand(t, db, "sleep long")
	sink := captureSinkFor(db, nil)

	rec := aCaptureRecord(fenceHex(0))
	rec.State = proto.CaptureUnfinished
	rec.Nonce = ""
	rec.Closing = proto.CaptureScreen{}
	// The unfinished ask resolves against the open attempt's binding, the
	// one recordAttemptEntry fed — the session spelling the record carries.
	sink.binds.BindOpen(rec.Session.Session, entryID)
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal the record: %v", err)
	}
	out, captureErr := sink.capture(context.Background(), raw)
	if captureErr != nil {
		t.Fatalf("capture: %v", captureErr)
	}
	if result, ok := out.(proto.CaptureResult); !ok || !result.Kept {
		t.Fatalf("capture answered %+v, want kept", out)
	}

	recordID := unfinishedArtifactID(entryID)
	if art, _ := db.Ledger().Artifact(context.Background(), captureview.ViewID(recordID, captureview.KindVT)); art != nil {
		t.Fatalf("an unfinished record produced a vt view at %s", captureview.ViewID(recordID, captureview.KindVT))
	}
	if art, _ := db.Ledger().Artifact(context.Background(), captureview.ViewID(recordID, captureview.KindText)); art != nil {
		t.Fatalf("an unfinished record produced a text view at %s", captureview.ViewID(recordID, captureview.KindText))
	}
}

// A refused capture stores nothing and derives nothing: the refusal marker
// is the answer at the record's own id, and no view hangs beside it.
func TestARefusedCaptureProducesNoViews(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetOutputEnabled(false)
	db := captureTestStore(t, content.CriticalityRoutine, policy)
	entryID := recordOneCommand(t, db, "make")
	sink := captureSinkFor(db, policy)

	nonce := fenceHex(0xF1)
	rec := recordWithWords(nonce, "x", "y", 40)
	sink.binds.Bind(nonce, entryID)
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal the record: %v", err)
	}
	out, captureErr := sink.capture(context.Background(), raw)
	if captureErr != nil {
		t.Fatalf("capture: %v — refusing to store is an answer, never an error", captureErr)
	}
	result, ok := out.(proto.CaptureResult)
	if !ok || result.Kept || result.Reason != "outputOff" {
		t.Fatalf("capture answered %+v, want the outputOff refusal", out)
	}

	recordID := captureArtifactID(nonce)
	if art, _ := db.Ledger().Artifact(context.Background(), captureview.ViewID(recordID, captureview.KindVT)); art != nil {
		t.Fatal("a refused capture produced a vt view")
	}
	if art, _ := db.Ledger().Artifact(context.Background(), captureview.ViewID(recordID, captureview.KindText)); art != nil {
		t.Fatal("a refused capture produced a text view")
	}
}

// The view write failing never fails the ask: the record is the canonical
// body and it is already stored when the views are written.
func TestAViewWriteFailureStillKeepsTheRecord(t *testing.T) {
	db := captureTestStore(t, content.CriticalityRoutine, nil)
	entryID := recordOneCommand(t, db, "make")
	ledger := db.Ledger()
	sink := captureSinkFor(db, nil)
	sink.set(failingViewsLedger{LedgerRepository: ledger}, nil)

	nonce := fenceHex(0x17)
	storeSettled(t, db, sink, entryID, nonce, recordWithWords(nonce, "x", "y", 40))

	art, err := db.Ledger().Artifact(context.Background(), captureArtifactID(nonce))
	if err != nil || art == nil || art.ByteLen == 0 {
		t.Fatalf("the record itself did not survive the view failure: art=%v err=%v", art, err)
	}
}

// failingViewsLedger wraps the real ledger and fails only the view write —
// the external-call failure path of the one call the wiring makes.
type failingViewsLedger struct {
	content.LedgerRepository
}

func (f failingViewsLedger) CaptureViews(_ context.Context, _ []content.CaptureView) error {
	return fmt.Errorf("content: capture views: the disk gave out")
}
