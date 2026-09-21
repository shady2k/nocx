package app

// The capture readback (nocx-2v80t.2.3): the stored record is the body a
// client reads. Two things have to survive storage for the criteria to
// hold — the rows' wrap flags, which are what let a reader join a
// soft-wrapped line and keep a hard newline apart, and the retention
// switch, which decides whether there is a body at all. The record the
// runtime built and the path it takes to the store are proven elsewhere
// (internal/sessionruntime, internal/helper/session, and this package's
// capture_store_test.go, which pins body == wire byte for byte); what this
// file adds is the READ: what comes back out of the store, and the paired
// halves of the retention criterion against the SAME record.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// captureRowOf builds one wire row: the text in a single cell plus the wrap
// flags exactly as the helper's conversion carries them (Wrap: followed by a
// continuation of this line; Continuation: continues the previous line).
func captureRowOf(text string, wrap, continuation bool) proto.CaptureRow {
	return proto.CaptureRow{
		Wrap:         wrap,
		Continuation: continuation,
		Cells:        []proto.CaptureCell{{Text: text, Width: 1, HasText: true}},
	}
}

// joinCaptureRows reads rows back the way a client attaching afterwards
// would: graphemes joined per row, a newline before every row that does not
// continue the previous one — the same reading sessionruntime's own render
// of a record states on its side of the store.
func joinCaptureRows(t *testing.T, rows []proto.CaptureRow) string {
	t.Helper()
	var sb strings.Builder
	for i, row := range rows {
		if i > 0 && !row.Continuation {
			sb.WriteByte('\n')
		}
		for _, c := range row.Cells {
			if c.Width == 0 {
				continue // spacer cells carry no text
			}
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}

// TestTheStoredRecordReadsBackAsLogicalLines is criterion 1 and 2's storage
// leg: a record whose departed rows carry a soft-wrapped line and a
// hard-newlined pair is stored against its entry, and the body that comes
// back out of the store joins into ONE logical line and TWO — in order,
// nothing missing.
func TestTheStoredRecordReadsBackAsLogicalLines(t *testing.T) {
	db := captureTestStore(t, content.CriticalityRoutine, nil)
	entryID := recordOneCommand(t, db, "cat long.log")
	sink := captureSinkFor(db)
	nonce := fenceHex(0xC3)
	sink.binds.Bind(nonce, entryID)

	long := strings.Repeat("a", 200) // three 80-column rows on the real screen
	rec := aCaptureRecord(nonce)
	rec.Departed = []proto.CaptureRow{
		captureRowOf(long[:80], true, false), // the wrapped line, first row
		captureRowOf(long[80:160], true, true),
		captureRowOf(long[160:], false, true), // its last
		captureRowOf("alpha", false, false),   // ended by a hard newline
		captureRowOf("beta", false, false),
	}
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

	art, err := db.Ledger().Artifact(context.Background(), captureArtifactID(nonce))
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if art == nil {
		t.Fatal("the record was not stored against the entry")
	}
	var body bytes.Buffer
	for _, chunk := range art.Chunks {
		body.Write(chunk)
	}
	var stored proto.CaptureParams
	if err := json.Unmarshal(body.Bytes(), &stored); err != nil {
		t.Fatalf("the stored body does not decode as the record: %v", err)
	}
	got := joinCaptureRows(t, stored.Departed)
	want := long + "\nalpha\nbeta"
	if got != want {
		t.Fatalf("the stored record reads back as:\n%q\nwant:\n%q", got, want)
	}
}

// TestTheSameRecordStoresNothingWithRetentionOffAndEverythingWithItOn is
// criterion 4's pair: the SAME record for the SAME command against two
// stores that differ in nothing but the output-retention switch. Off: the
// answer is outputOff and nothing is stored. On: the answer is kept and the
// body is the whole record.
func TestTheSameRecordStoresNothingWithRetentionOffAndEverythingWithItOn(t *testing.T) {
	runAgainst := func(t *testing.T, policy *content.Policy, nonce string) (content.ContentDB, any, error) {
		t.Helper()
		db := captureTestStore(t, content.CriticalityRoutine, policy)
		entryID := recordOneCommand(t, db, "cat report.txt")
		sink := captureSinkFor(db)
		sink.binds.Bind(nonce, entryID)
		raw, err := json.Marshal(aCaptureRecord(nonce))
		if err != nil {
			t.Fatalf("marshal the record: %v", err)
		}
		out, captureErr := sink.capture(context.Background(), raw)
		return db, out, captureErr
	}

	off := content.NewPolicy()
	off.SetOutputEnabled(false)
	offDB, out, err := runAgainst(t, off, fenceHex(0xE1))
	assertRefusal(t, offDB, out, err, fenceHex(0xE1), "outputOff")

	// The paired positive: retention on, the same record, the same command.
	onDB, out, err := runAgainst(t, nil, fenceHex(0xE2))
	if err != nil {
		t.Fatalf("capture: %v — refusing to store is an answer, never an error", err)
	}
	result, ok := out.(proto.CaptureResult)
	if !ok || !result.Kept || result.Reason != "" {
		t.Fatalf("capture answered %+v, want kept with no reason", out)
	}
	art, artErr := onDB.Ledger().Artifact(context.Background(), captureArtifactID(fenceHex(0xE2)))
	if artErr != nil {
		t.Fatalf("Artifact: %v", artErr)
	}
	if art == nil || art.ByteLen == 0 {
		t.Fatalf("with retention on the body is missing (artifact %v), want the whole record stored", art)
	}
}
