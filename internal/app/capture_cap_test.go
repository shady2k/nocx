package app

// The per-command cap on the record path (nocx-2v80t.2.6): a record larger
// than content.Policy.OutputCapBytes is cut ROWS, never bytes — byte-cutting
// the JSON record concatenates to undecodable bytes — and the handler stores
// content.TruncCap beside it, so reading the capture answers the summary AND
// says it is one. The record path's cap keeps the head and the tail of the
// departed rows and drops the middle, the same cut the renderer's capBody and
// the store's live recording make on their own surfaces (content/policy.go).
// A record under the cap is stored byte for byte, unnamed: the paired
// positive is criterion 3.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// aWideRecord builds a settled record whose departed rows alone are worth
// roughly wantBytes of JSON: enough rows of padded text that no cap a test
// names is reachable without cutting.
func aWideRecord(nonce string, rows, cells int) proto.CaptureParams {
	rec := aCaptureRecord(nonce)
	rec.Departed = make([]proto.CaptureRow, 0, rows)
	for i := range rows {
		text := ""
		for j := range cells {
			text += fmt.Sprintf("%c", 'a'+(i+j)%26)
		}
		rec.Departed = append(rec.Departed, captureRowOf(text, false, false))
	}
	return rec
}

// TestTheCaptureHandlerCapsAnOversizedRecordAndSaysSo is criterion 1's
// producer leg: a record past the per-command cap is stored with its
// departed rows cut — head and tail kept, middle dropped — the body still
// decodes as the record with its opening and closing screens whole, the
// stored artifact names content.TruncCap, and the answer is still kept.
func TestTheCaptureHandlerCapsAnOversizedRecordAndSaysSo(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetOutputCapBytes(4096)
	db := captureTestStore(t, content.CriticalityRoutine, policy)
	entryID := recordOneCommand(t, db, "make all")
	sink := captureSinkFor(db, policy)

	nonce := fenceHex(0xF1)
	sink.binds.Bind(nonce, entryID)
	rec := aWideRecord(nonce, 120, 12)
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal the record: %v", err)
	}
	if len(raw) <= 4096 {
		t.Fatalf("fixture is not oversized: %d bytes, want well past the 4096 cap", len(raw))
	}

	out, captureErr := sink.capture(context.Background(), raw)
	if captureErr != nil {
		t.Fatalf("capture: %v", captureErr)
	}
	if result, ok := out.(proto.CaptureResult); !ok || !result.Kept {
		t.Fatalf("a capped record is still kept: %+v", out)
	}

	art, err := db.Ledger().Artifact(context.Background(), captureArtifactID(nonce))
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if art == nil {
		t.Fatal("the capped record was not stored")
	}
	if art.Truncated == nil || *art.Truncated != content.TruncCap {
		t.Fatalf("truncated = %v, want %q — a summary that does not say it is one is the defect", art.Truncated, content.TruncCap)
	}
	if art.ByteLen > 4096 {
		t.Fatalf("stored body is %d bytes, past the 4096 cap", art.ByteLen)
	}
	var body bytes.Buffer
	for _, chunk := range art.Chunks {
		body.Write(chunk)
	}
	var stored proto.CaptureParams
	if err := json.Unmarshal(body.Bytes(), &stored); err != nil {
		t.Fatalf("the capped body does not decode as the record: %v", err)
	}
	// The cut keeps the head and the tail: the first and the last departed
	// rows are the ones that survive, in order.
	if len(stored.Departed) == 0 || len(stored.Departed) >= len(rec.Departed) {
		t.Fatalf("departed rows after the cap = %d, want some but fewer than the %d sent", len(stored.Departed), len(rec.Departed))
	}
	if got := captureRowText(t, stored.Departed[0]); got != captureRowText(t, rec.Departed[0]) {
		t.Fatalf("first kept row = %q, want the record's first %q", got, captureRowText(t, rec.Departed[0]))
	}
	last := len(stored.Departed) - 1
	if got := captureRowText(t, stored.Departed[last]); got != captureRowText(t, rec.Departed[len(rec.Departed)-1]) {
		t.Fatalf("last kept row = %q, want the record's last %q", got, captureRowText(t, rec.Departed[len(rec.Departed)-1]))
	}
	// The screens are the summary's frame: the cap cuts rows between them,
	// never the opening or the closing screen itself.
	if len(stored.Closing.Lines) != len(rec.Closing.Lines) {
		t.Fatalf("the closing screen was cut: %d rows, want %d", len(stored.Closing.Lines), len(rec.Closing.Lines))
	}
}

// TestTheCaptureHandlerStoresAnUnderCapRecordByteForByte is criterion 3's
// paired positive at the producer: the ordinary record — retention on,
// nothing degraded — is stored byte for byte as it crossed, with NO
// truncation named, because nothing was cut.
func TestTheCaptureHandlerStoresAnUnderCapRecordByteForByte(t *testing.T) {
	policy := content.NewPolicy()
	db := captureTestStore(t, content.CriticalityRoutine, policy)
	entryID := recordOneCommand(t, db, "ls -la")
	sink := captureSinkFor(db, policy)

	nonce := fenceHex(0xF2)
	sink.binds.Bind(nonce, entryID)
	raw, err := json.Marshal(aCaptureRecord(nonce))
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
	if err != nil || art == nil {
		t.Fatalf("Artifact: %v, %v", art, err)
	}
	if art.Truncated != nil {
		t.Fatalf("an uncut record is named %q — the flag means a cap acted, and none did", *art.Truncated)
	}
	var body bytes.Buffer
	for _, chunk := range art.Chunks {
		body.Write(chunk)
	}
	if !bytes.Equal(body.Bytes(), raw) {
		t.Fatalf("the stored body is not the record that crossed:\n stored: %s\n sent:   %s", body.String(), raw)
	}
}

// TestTheCapWithNoRowsToCutStoresTheRecordWhole pins the cap's edge: a
// record whose size is all screens and no departed rows cannot be cut ROWS,
// so nothing is cut, no truncation is named, and the record is stored whole —
// the store's own ceiling, not this cap, is the backstop past it.
func TestTheCapWithNoRowsToCutStoresTheRecordWhole(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetOutputCapBytes(64) // smaller than any real record
	db := captureTestStore(t, content.CriticalityRoutine, policy)
	entryID := recordOneCommand(t, db, "true")
	sink := captureSinkFor(db, policy)

	nonce := fenceHex(0xF3)
	sink.binds.Bind(nonce, entryID)
	raw, err := json.Marshal(aCaptureRecord(nonce))
	if err != nil {
		t.Fatalf("marshal the record: %v", err)
	}
	if len(raw) <= 64 {
		t.Fatalf("fixture is not oversized: %d bytes", len(raw))
	}

	out, captureErr := sink.capture(context.Background(), raw)
	if captureErr != nil {
		t.Fatalf("capture: %v", captureErr)
	}
	if result, ok := out.(proto.CaptureResult); !ok || !result.Kept {
		t.Fatalf("capture answered %+v, want kept", out)
	}
	art, err := db.Ledger().Artifact(context.Background(), captureArtifactID(nonce))
	if err != nil || art == nil {
		t.Fatalf("Artifact: %v, %v", art, err)
	}
	if art.Truncated != nil {
		t.Fatalf("truncated = %v, want nil — no row was cut, so no cut is named", art.Truncated)
	}
	if art.ByteLen != int64(len(raw)) {
		t.Fatalf("stored %d bytes, want the whole %d", art.ByteLen, len(raw))
	}
}

// captureRowText reads one row's text the way the assertions above compare
// rows: the graphemes of its cells, joined.
func captureRowText(t *testing.T, row proto.CaptureRow) string {
	t.Helper()
	text := ""
	for _, c := range row.Cells {
		text += c.Text
	}
	return text
}
