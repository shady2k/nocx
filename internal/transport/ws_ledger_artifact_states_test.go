package transport

// ledger.artifact answers a capture's THREE states (nocx-2v80t.2.6), and the
// contract is the wire's, so each is proven off the REAL socket against the
// frozen schema (AGENTS.md testing rule 5):
//
//   - kept whole: body, byteLen, truncated null — the ordinary read,
//     unchanged;
//   - kept a summary (the per-command cap cut rows at the producer): the
//     summary body AND truncated "cap" — it says it is one;
//   - kept nothing (output retention off, a sensitive entry, a critical
//     environment): the read ANSWERS the state — the zero-byte marker the
//     store recorded comes back as truncated "suppressed", a named state and
//     neither an error nor an empty success — while an id NO capture ever
//     named is still invalid params, exactly as before.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

const unknownArtifactID = "0198f2b0-0000-7000-8000-000000000000"

type artifactRead struct {
	ID        string  `json:"id"`
	MediaType string  `json:"mediaType"`
	Body      string  `json:"body"`
	Truncated *string `json:"truncated"`
	ByteLen   int64   `json:"byteLen"`
}

func artifactCall(t *testing.T, conn *websocket.Conn, id string, callID int) (json.RawMessage, *jsonrpcErrorObj) {
	t.Helper()
	raw := jsonrpcCallWithID(t, conn, "ledger.artifact", map[string]any{"id": id}, callID)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode ledger.artifact response: %v\nraw: %s", err, raw)
	}
	return env.Result, env.Error
}

// TestLedgerArtifact_ARefusedCaptureReadsAsTheNamedState is criterion 2 off
// the real socket: the write is refused (the ack says outputOff, stored
// false), the store records the refusal as a marker at the capture's own id,
// and the read of that id ANSWERS the state — truncated "suppressed", zero
// bytes, the asked-for media type echoed — validating against the frozen
// schema. The read half follows the write answer; it is not an error and not
// a bare empty success.
func TestLedgerArtifact_ARefusedCaptureReadsAsTheNamedState(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetOutputEnabled(false)
	db := newLedgerStoreWithPolicy(t, policy)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "cat secrets.txt")

	// The write half: a real capture ask over the wire, answered outputOff.
	result, rpcErr := captureCall(t, conn, captureParams(entryID, captureArtifactID, 1, "the output"), 1)
	if rpcErr != nil {
		t.Fatalf("ledger.capture: %+v", rpcErr)
	}
	var ack captureAck
	if err := json.Unmarshal(result, &ack); err != nil || ack.Stored {
		t.Fatalf("ack = %+v (%v), want stored false — refusing to store is an answer", ack, err)
	}

	// The read half: the same id answers the state the write recorded.
	read, readErr := artifactCall(t, conn, captureArtifactID, 2)
	if readErr != nil {
		t.Fatalf("ledger.artifact answered %+v, want the named state as a RESULT", readErr)
	}
	validateJSON(t, loadSchema(t, "ledger.artifact.schema.json"), read, "ledger.artifact")
	var art artifactRead
	if err := json.Unmarshal(read, &art); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if art.ID != captureArtifactID {
		t.Fatalf("id = %q, want the asked-for id echoed", art.ID)
	}
	if art.Truncated == nil || *art.Truncated != string(content.TruncSuppressed) {
		t.Fatalf("truncated = %v, want %q — the read must NAME what retention kept", art.Truncated, content.TruncSuppressed)
	}
	if art.Body != "" || art.ByteLen != 0 {
		t.Fatalf("a refusal kept a body: %d bytes", art.ByteLen)
	}
	if art.MediaType != "application/vt" {
		t.Fatalf("mediaType = %q, want the capture's own media type echoed", art.MediaType)
	}
}

// TestLedgerArtifact_AnUnknownIDIsStillInvalidParams pins the boundary the
// named state must not blur: an id no capture ever named, on a store that
// keeps everything, is still invalid params — the same answer a restored
// block has always read as a hole. Suppressed means a capture was refused,
// never that the id was mistyped.
func TestLedgerArtifact_AnUnknownIDIsStillInvalidParams(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	aRecordedCommand(t, db, "ls -la")

	_, rpcErr := artifactCall(t, conn, unknownArtifactID, 1)
	if rpcErr == nil {
		t.Fatal("an unknown id answered a result, want invalid params")
	}
	if rpcErr.Code != -32602 {
		t.Fatalf("code = %d, want -32602", rpcErr.Code)
	}
}

// TestLedgerArtifact_TheSummaryReadsAsTheSummary is criterion 1's read leg:
// a capture stored WITH the producer's cap marker reads back the summary
// body AND the word "cap" — never an empty body, and never silent about what
// it is.
func TestLedgerArtifact_TheSummaryReadsAsTheSummary(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "make all")

	capMark := content.TruncCap
	body := `{"state":"settled","departed":[],"summary":true}`
	if _, err := db.Ledger().CaptureOutput(t.Context(), content.CaptureOutput{
		EntryID: entryID, ArtifactID: captureArtifactID,
		MediaType: content.MediaJSON, CaptureMethod: content.CaptureTerminalCells,
		CaptureVersion: 1, Truncated: &capMark,
		Seq: 1, Body: []byte(body),
	}); err != nil {
		t.Fatalf("CaptureOutput: %v", err)
	}

	read, readErr := artifactCall(t, conn, captureArtifactID, 1)
	if readErr != nil {
		t.Fatalf("ledger.artifact: %+v", readErr)
	}
	validateJSON(t, loadSchema(t, "ledger.artifact.schema.json"), read, "ledger.artifact")
	var art artifactRead
	if err := json.Unmarshal(read, &art); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if art.Truncated == nil || *art.Truncated != string(content.TruncCap) {
		t.Fatalf("truncated = %v, want %q — a summary that does not say so is the defect", art.Truncated, content.TruncCap)
	}
	if art.Body != body || art.ByteLen != int64(len(body)) {
		t.Fatalf("body = %q/%d bytes, want the summary itself (%d bytes)", art.Body, art.ByteLen, len(body))
	}
}

// TestLedgerArtifact_TheWholeReadsAsWhole is criterion 3's read leg: with
// retention on and nothing degraded, the ordinary read answers the whole
// body with truncated null — exactly what it answered before this bead.
func TestLedgerArtifact_TheWholeReadsAsWhole(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "ls -la")

	const body = "\x1b[31mred\x1b[0m\nplain"
	if _, err := db.Ledger().CaptureOutput(t.Context(), content.CaptureOutput{
		EntryID: entryID, ArtifactID: captureArtifactID,
		MediaType: content.MediaVT, CaptureMethod: content.CaptureTerminalCells,
		CaptureVersion: 1,
		Seq:            1, Body: []byte(body),
	}); err != nil {
		t.Fatalf("CaptureOutput: %v", err)
	}

	read, readErr := artifactCall(t, conn, captureArtifactID, 1)
	if readErr != nil {
		t.Fatalf("ledger.artifact: %+v", readErr)
	}
	validateJSON(t, loadSchema(t, "ledger.artifact.schema.json"), read, "ledger.artifact")
	var art artifactRead
	if err := json.Unmarshal(read, &art); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if art.Truncated != nil {
		t.Fatalf("truncated = %q, want null — an uncut read names nothing", *art.Truncated)
	}
	if !strings.Contains(art.Body, "plain") || art.ByteLen != int64(len(body)) {
		t.Fatalf("body = %q/%d bytes, want the whole capture", art.Body, art.ByteLen)
	}
}
