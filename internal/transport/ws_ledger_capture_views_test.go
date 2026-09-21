package transport

// The views over the wire (nocx-2v80t.2.5): ledger.artifact answers a view
// id with the record's stored bytes RENDERED AT THE READ — the SGR grid the
// card draws, the plain text search will read — and the record's retention
// state on every answer. These tests drive the real method through the real
// socket against a real store (AGENTS.md testing rule 5); the records and
// their view rows are written through the store directly, exactly as the
// capture path's wiring (internal/app) writes them.
//
// The four criteria, at the seam where bodies exist:
//
//   1. changing the stored capture changes BOTH views, in one test — every
//      body tracks its record and nothing else;
//   2. a word only in rows that scrolled away is IN the text view — the
//      view covers the whole interval, not the last screen;
//   3. two reads of one view answer byte-identically — the derivation is a
//      pure function of the stored bytes;
//   4. a summary record's views answer the named summary state AND carry
//      views of the stored summary, never of a whole nobody kept.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/captureview"
	"github.com/shady2k/nocx/internal/content"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/log"
)

// storeSettledRecord writes one settled record against the entry through
// the store, then its two view rows — the store-level half of what the
// capture ask does — and answers the record's artifact id.
func storeSettledRecord(t *testing.T, db content.ContentDB, entryID, nonce string, rec proto.CaptureParams, truncated *content.Truncation) string {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal the record: %v", err)
	}
	recordID := captureview.ViewID(nonce, "record") // deterministic per test nonce
	cols, rows := rec.Closing.Cols, rec.Closing.Rows
	if _, err := db.Ledger().CaptureOutput(t.Context(), content.CaptureOutput{
		EntryID: entryID, ArtifactID: recordID,
		MediaType: content.MediaJSON, CaptureMethod: content.CaptureTerminalCells,
		CaptureVersion: 1, Truncated: truncated,
		TerminalCols: &cols, TerminalRows: &rows, Seq: 1, Body: raw,
	}); err != nil {
		t.Fatalf("CaptureOutput: %v", err)
	}
	views := []content.CaptureView{
		{
			EntryID: entryID, RecordID: recordID,
			ID:        captureview.ViewID(recordID, captureview.KindVT),
			MediaType: content.MediaVT, DerivedFrom: recordID,
			Truncated: truncated, TerminalCols: &cols, TerminalRows: &rows,
		},
		{
			EntryID: entryID, RecordID: recordID,
			ID:        captureview.ViewID(recordID, captureview.KindText),
			MediaType: content.MediaText, DerivedFrom: captureview.ViewID(recordID, captureview.KindVT),
			Truncated: truncated, TerminalCols: &cols, TerminalRows: &rows,
		},
	}
	if err := db.Ledger().CaptureViews(t.Context(), views); err != nil {
		t.Fatalf("CaptureViews: %v", err)
	}
	return recordID
}

func readArtifact(t *testing.T, conn *websocket.Conn, id string, callID int) artifactRead {
	t.Helper()
	raw, rpcErr := artifactCall(t, conn, id, callID)
	if rpcErr != nil {
		t.Fatalf("ledger.artifact(%s) answered %+v, want a result", id, rpcErr)
	}
	validateJSON(t, loadSchema(t, "ledger.artifact.schema.json"), raw, "ledger.artifact")
	var art artifactRead
	if err := json.Unmarshal(raw, &art); err != nil {
		t.Fatalf("decode read of %s: %v", id, err)
	}
	return art
}

func viewRecord(departed, closing string) proto.CaptureParams {
	return proto.CaptureParams{
		State:        proto.CaptureSettled,
		Revision:     7,
		Completeness: proto.CompletenessComplete,
		Opening:      proto.CaptureScreen{Cols: 40, Rows: 2},
		Departed: []proto.CaptureRow{
			{Cells: []proto.CaptureCell{{Text: departed, Width: len(departed), HasText: departed != ""}}},
		},
		Closing: proto.CaptureScreen{
			Cols: 40, Rows: 2,
			Lines: []proto.CaptureRow{
				{Cells: []proto.CaptureCell{{Text: closing, Width: len(closing), HasText: closing != ""}}},
			},
		},
	}
}

// Criterion 1: ONE test, two records, both views each. Every body tracks
// its own record's bytes and moves when they move — the failure shape of a
// view that is still a second, independent body.
func TestLedgerArtifact_ChangingTheStoredCaptureChangesBothViews(t *testing.T) {
	db := newLedgerStoreWithPolicy(t, nil)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "make")

	recA := viewRecord("firstOutput", "firstEnd")
	recB := viewRecord("secondOutput", "secondEnd")
	recordA := storeSettledRecord(t, db, entryID, "a", recA, nil)
	recordB := storeSettledRecord(t, db, entryID, "b", recB, nil)

	vtA := readArtifact(t, conn, captureview.ViewID(recordA, captureview.KindVT), 1)
	textA := readArtifact(t, conn, captureview.ViewID(recordA, captureview.KindText), 2)
	vtB := readArtifact(t, conn, captureview.ViewID(recordB, captureview.KindVT), 3)
	textB := readArtifact(t, conn, captureview.ViewID(recordB, captureview.KindText), 4)

	if !strings.Contains(vtA.Body, "firstOutput") || !strings.Contains(vtA.Body, "firstEnd") {
		t.Fatalf("A's card view = %q, want its own record's words", vtA.Body)
	}
	if !strings.Contains(textA.Body, "firstOutput") {
		t.Fatalf("A's text view = %q, want its own record's word", textA.Body)
	}
	if !strings.Contains(vtB.Body, "secondOutput") || !strings.Contains(textB.Body, "secondOutput") {
		t.Fatalf("B's views = %q / %q, want B's record's words", vtB.Body, textB.Body)
	}
	if vtA.Body == vtB.Body {
		t.Fatal("changing the stored capture did not change the card body")
	}
	if textA.Body == textB.Body {
		t.Fatal("changing the stored capture did not change the searchable text")
	}
}

// Criterion 2: a word that only exists in rows that scrolled away is found
// in the text view — the whole interval, not the last screen.
func TestLedgerArtifact_TheTextViewCoversScrolledAwayRows(t *testing.T) {
	db := newLedgerStoreWithPolicy(t, nil)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "cat long.log")

	rec := viewRecord("the error was EACCESQUX mid-run", "done")
	recordID := storeSettledRecord(t, db, entryID, "c", rec, nil)

	text := readArtifact(t, conn, captureview.ViewID(recordID, captureview.KindText), 1)
	if !strings.Contains(text.Body, "EACCESQUX") || !strings.Contains(text.Body, "done") {
		t.Fatalf("the text view = %q, want the departed word AND the boundary screen", text.Body)
	}
}

// Criterion 3: the derivation reads the stored bytes and nothing else, so
// the same read answers the same bytes — twice in a row, on separate calls.
func TestLedgerArtifact_AViewReadsStably(t *testing.T) {
	db := newLedgerStoreWithPolicy(t, nil)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "make")

	rec := viewRecord("keptOutput", "keptEnd")
	recordID := storeSettledRecord(t, db, entryID, "d", rec, nil)

	first := readArtifact(t, conn, captureview.ViewID(recordID, captureview.KindVT), 1)
	second := readArtifact(t, conn, captureview.ViewID(recordID, captureview.KindVT), 2)
	if first.Body != second.Body || first.ByteLen != second.ByteLen {
		t.Fatalf("the view moved between reads: %q → %q", first.Body, second.Body)
	}
}

// Criterion 4: a summary record's views say summary — the named state rides
// every view answer, and the bodies are views of the STORED summary (the
// cut record), never of a whole interval nobody kept.
func TestLedgerArtifact_ASummarySaysSoOnBothViews(t *testing.T) {
	db := newLedgerStoreWithPolicy(t, nil)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "seq big")

	cap := content.TruncCap
	rec := viewRecord("head of a very long output", "tail")
	recordID := storeSettledRecord(t, db, entryID, "e", rec, &cap)

	vt := readArtifact(t, conn, captureview.ViewID(recordID, captureview.KindVT), 1)
	text := readArtifact(t, conn, captureview.ViewID(recordID, captureview.KindText), 2)
	if vt.Truncated == nil || *vt.Truncated != string(content.TruncCap) {
		t.Fatalf("the vt view truncated = %v, want %q", vt.Truncated, content.TruncCap)
	}
	if text.Truncated == nil || *text.Truncated != string(content.TruncCap) {
		t.Fatalf("the text view truncated = %v, want %q", text.Truncated, content.TruncCap)
	}
	if vt.Body == "" || text.Body == "" {
		t.Fatalf("a summary answered empty bodies (%q / %q) — a summary is not nothing", vt.Body, text.Body)
	}
	if !strings.Contains(vt.Body, "tail") || !strings.Contains(text.Body, "tail") {
		t.Fatalf("the views are not views of the stored summary: %q / %q", vt.Body, text.Body)
	}
}

// The boundary: a view address of a record nobody stored is still invalid
// params — the same answer a restored block has always read as a hole. A
// derived view existing only for records that exist is what keeps "the
// command printed nothing" from being forged out of a mistyped id.
func TestLedgerArtifact_AViewOfNoRecordIsInvalidParams(t *testing.T) {
	db := newLedgerStoreWithPolicy(t, nil)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)

	_, rpcErr := artifactCall(t, conn, captureview.ViewID("0198f2b0-0000-7000-8000-000000000000", captureview.KindVT), 1)
	if rpcErr == nil {
		t.Fatal("a view of a record nobody stored answered a result, want invalid params")
	}
}
