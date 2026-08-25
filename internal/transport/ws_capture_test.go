package transport

// The capture seam over the real socket: a submitted credential becomes a
// pending capture, saving rewrites the linked history row to a vault
// reference, and the suppression rules (already pending, already saved,
// dismissed, superseded, expired) behave the way the contract pasted in
// internal/credential/capture.go says they must. The registry's clock is
// injected so expiry is reachable without sleeping.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/gorilla/websocket"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/credential"
	"github.com/shady2k/nocx/internal/log"
)

// captureFakeDB is a real-behaving in-memory ContentDB for the capture round
// over the LEDGER seam (nocx-rtg0.19): RecordCompleted mints ids,
// QueryEntries serves, RewriteRedaction actually replaces the span in the
// stored intent and drops the segment from the receipt in entries.payload —
// the fake the record/save/query round trip needs. A fake that stopped
// behaving would turn this round trip into a green test of nothing.
//
// Those three plus ListEntries are the only methods exercised — the embedded
// nil content.LedgerRepository turns any other call into a loud panic rather
// than a quiet zero value.
type captureFakeDB struct {
	content.LedgerRepository
	mu      sync.Mutex
	nextID  int64
	records []content.LedgerEntrySummary
}

func newCaptureFakeDB() *captureFakeDB { return &captureFakeDB{} }

func (f *captureFakeDB) Conversations() content.ConversationRepository { return nil }
func (f *captureFakeDB) Backup(_ context.Context, _ string) error      { return content.ErrNotImplemented }
func (f *captureFakeDB) Close() error                                  { return nil }
func (f *captureFakeDB) Ledger() content.LedgerRepository              { return f }
func (f *captureFakeDB) Layout() content.LayoutRepository              { return nil }
func (f *captureFakeDB) APIRuns() content.APIRunRepository             { return nil }

func (f *captureFakeDB) RecordCompleted(_ context.Context, in content.CompletedCommand) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fakeEntryID(f.nextID)
	env := in.Env
	f.records = append(f.records, content.LedgerEntrySummary{
		ID:            id,
		IngestSeq:     f.nextID,
		EnvironmentID: env.ID,
		Environment:   &env,
		Cwd:           in.Cwd,
		Kind:          content.EntryShell,
		Intent:        in.Intent,
		Phase:         content.PhaseClosed,
		Status:        in.Status,
		StartedAt:     in.StartedAt,
		EndedAt:       in.EndedAt,
		Payload:       in.Payload,
	})
	return id, nil
}

// RewriteRedaction is the settlement the save round trip turns on, and it is
// kept honest: the receipt is read and written back with the store's OWN
// codecs (content.EntryMaskingOf / content.WithEntryMasking), so a fake that
// drifted from the payload contract fails here instead of agreeing with
// itself. Not matching any current segment is the idempotent no-op a retried
// save relies on.
func (f *captureFakeDB) RewriteRedaction(_ context.Context, entryID string, span content.Redaction, reference string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.records {
		r := &f.records[i]
		if r.ID != entryID {
			continue
		}
		masking, err := content.EntryMaskingOf(r.Payload)
		if err != nil {
			return err
		}
		matched := false
		kept := make([]content.Redaction, 0, len(masking.Redactions))
		for _, red := range masking.Redactions {
			if red.Start == span.Start && red.End == span.End && red.Kind == span.Kind {
				matched = true
				continue
			}
			kept = append(kept, red)
		}
		if !matched {
			return nil // idempotent no-op
		}
		masking.Redactions = kept
		payload, err := content.WithEntryMasking(r.Payload, masking)
		if err != nil {
			return err
		}
		r.Intent = r.Intent[:span.Start] + reference + r.Intent[span.End:]
		r.Payload = payload
		return nil
	}
	return content.ErrNotFound
}

func (f *captureFakeDB) ListEntries(_ context.Context, limit int) ([]content.LedgerEntrySummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]content.LedgerEntrySummary, 0, limit)
	for i := len(f.records) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, f.records[i])
	}
	return out, nil
}

func (f *captureFakeDB) QueryEntries(_ context.Context, q content.LedgerQuery) (content.LedgerPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var entries []content.LedgerEntrySummary
	for i := len(f.records) - 1; i >= 0; i-- {
		r := f.records[i]
		switch q.Scope {
		case content.ScopeDirectory:
			if r.Cwd != q.Cwd || r.EnvironmentID != q.EnvironmentID {
				continue
			}
		case content.ScopeHost:
			if r.EnvironmentID != q.EnvironmentID {
				continue
			}
		}
		if q.BeforeID != "" && r.ID >= q.BeforeID {
			continue
		}
		entries = append(entries, r)
		if len(entries) >= q.Limit {
			break
		}
	}
	return content.LedgerPage{Entries: entries, HasRows: len(f.records) > 0, Exhausted: true}, nil
}

// storedRow is one row of the capture fake as these tests read it: the
// durable intent and the live redaction list, decoded with the SAME reader
// the wire uses. The receipt is not a column any more — it rides
// entries.payload — so a test that peeked at a field would be reading a
// shape the store does not have.
type storedRow struct {
	ID         string
	Command    string
	Redactions []content.Redaction
}

func (f *captureFakeDB) rows(t *testing.T, limit int) []storedRow {
	t.Helper()
	entries, err := f.ListEntries(context.Background(), limit)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	out := make([]storedRow, 0, len(entries))
	for _, e := range entries {
		masking, maskErr := content.EntryMaskingOf(e.Payload)
		if maskErr != nil {
			t.Fatalf("entry %s payload: %v", e.ID, maskErr)
		}
		out = append(out, storedRow{ID: e.ID, Command: e.Intent, Redactions: masking.Redactions})
	}
	return out
}

// newCaptureWSServer wires a WSServer over the capture fake with an
// injected registry (clock-controllable) and a fake vault whose resolved
// name is fixed so the tests can assert what the save actually used.
func newCaptureWSServer(t *testing.T, db content.ContentDB, clock *time.Time) (*WSServer, *fakeVaultLifecycle, func()) {
	t.Helper()
	caps, err := credential.NewCaptureRegistry()
	if err != nil {
		t.Fatalf("NewCaptureRegistry: %v", err)
	}
	fv := &fakeVaultLifecycle{resolvedName: "openrouter.ai", createNamedID: "sec:v1:file:abc123"}
	opts := []WSServerOption{WithContentDB(db), WithVaultLifecycle(fv), WithCaptureRegistry(caps)}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), opts...)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, fv, func() { _ = ws.Stop(ctx) }
}

// Same server, handing back the registry instead of the vault fake — the
// tests that need to trigger a destruction event directly.
func newCaptureWSServerWithRegistry(
	t *testing.T, db content.ContentDB, clock *time.Time,
) (*WSServer, *credential.CaptureRegistry, func()) {
	t.Helper()
	caps, err := credential.NewCaptureRegistry()
	if err != nil {
		t.Fatalf("NewCaptureRegistry: %v", err)
	}
	fv := &fakeVaultLifecycle{resolvedName: "openrouter.ai", createNamedID: "sec:v1:file:abc123"}
	opts := []WSServerOption{WithContentDB(db), WithVaultLifecycle(fv), WithCaptureRegistry(caps)}
	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), opts...)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, caps, func() { _ = ws.Stop(ctx) }
}

type recordAck struct {
	MaskedCount int             `json:"maskedCount"`
	MaskedKinds []string        `json:"maskedKinds"`
	EntryID     string          `json:"entryId"`
	Redactions  []redactionWire `json:"redactions"`
	Captures    []struct {
		ID            string        `json:"id"`
		EntryID       string        `json:"entryId"`
		Redaction     redactionWire `json:"redaction"`
		SuggestedName string        `json:"suggestedName"`
	} `json:"captures"`
}

func TestSecretsDetect_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "secrets.detect.schema.json")
	cases := map[string]secretsDetectResponse{
		"no findings": {Revision: 7, Findings: []secretsDetectFinding{}},
		"one finding": {
			Revision: 3,
			Findings: []secretsDetectFinding{
				{Kind: "openai", Start: 10, End: 30, ValueStart: 10, ValueEnd: 30, SuggestedName: "openrouter.ai"},
			},
		},
		"structural value bounds": {
			Revision: 3,
			Findings: []secretsDetectFinding{
				{Kind: "env-assignment", Start: 0, End: 40, ValueStart: 16, ValueEnd: 40, SuggestedName: "openai-key"},
			},
		},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "secrets.detect DTO")
		})
	}
}

func TestSecretsDetect_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "secrets.detect.schema.json")
	ws, stop := newHistoryWSServer(t, nil)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	line := `echo "🔥" && curl -H "Authorization: Bearer sk-proj-abcdef1234567890" https://api`
	resp := vaultCall(t, conn, "secrets.detect", map[string]any{"line": line, "revision": 42}, 1)
	if resp.Error != nil {
		t.Fatalf("detect error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "secrets.detect result (real socket)")

	var got struct {
		Revision int64 `json:"revision"`
		Findings []struct {
			Kind          string `json:"kind"`
			Start         int    `json:"start"`
			End           int    `json:"end"`
			ValueStart    int    `json:"valueStart"`
			ValueEnd      int    `json:"valueEnd"`
			SuggestedName string `json:"suggestedName"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Revision != 42 {
		t.Errorf("revision = %d, want the echo 42", got.Revision)
	}
	if len(got.Findings) != 1 || got.Findings[0].Kind != "openai" {
		t.Fatalf("findings = %+v, want one openai", got.Findings)
	}
	if got.Findings[0].SuggestedName == "" {
		t.Errorf("suggestedName = %q, want the backend's SuggestName (a host or a kind)", got.Findings[0].SuggestedName)
	}
	if got.Findings[0].ValueStart != got.Findings[0].Start || got.Findings[0].ValueEnd != got.Findings[0].End {
		t.Errorf("value bounds = [%d,%d), want the whole-match rule's span [%d,%d)",
			got.Findings[0].ValueStart, got.Findings[0].ValueEnd, got.Findings[0].Start, got.Findings[0].End)
	}
	// UTF-16: the emoji before the token is 2 units, so the token's unit
	// offset is its byte offset minus 2.
	wantStart := strings.Index(line, "sk-proj") - 2
	if got.Findings[0].Start != wantStart {
		t.Errorf("start = %d, want %d (UTF-16, not bytes)", got.Findings[0].Start, wantStart)
	}
	// The finding slices the line the way JS would: UTF-16 units, not
	// bytes — slicing the Go string with the unit offsets would cut into
	// the emoji's bytes.
	units := utf16.Encode([]rune(line))
	if slice := string(utf16.Decode(units[got.Findings[0].Start:got.Findings[0].End])); !strings.HasPrefix(slice, "sk-proj") {
		t.Errorf("finding does not slice the line as JS would: %q", slice)
	}
}

// TestHistoryRecord_CaptureSaveFlow is the round's spine over the real
// socket: submit a command carrying a key, get a capture id back, save it,
// and read the history row as a reference.
func TestHistoryRecord_CaptureSaveFlow(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, fv, stop := newCaptureWSServer(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	ack := recordAndDecode(t, conn, `curl -H "Authorization: Bearer sk-proj-abcdef1234567890" https://openrouter.ai/api`, 1)
	if len(ack.Captures) != 1 {
		t.Fatalf("captures = %+v, want one offer", ack.Captures)
	}
	capID := ack.Captures[0].ID
	if ack.Captures[0].SuggestedName != "openrouter.ai" {
		t.Errorf("suggestedName = %q, want openrouter.ai (the host)", ack.Captures[0].SuggestedName)
	}
	if ack.EntryID == "" {
		t.Fatal("entryId is empty, want the stable row id")
	}

	// Save over the wire.
	resp := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": capID}, 2)
	if resp.Error != nil {
		t.Fatalf("captureSave error: %+v", resp.Error)
	}
	var saved struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resp.Result, &saved); err != nil {
		t.Fatalf("decode save: %v", err)
	}
	if saved.Name != "openrouter.ai" {
		t.Errorf("saved name = %q, want the real name", saved.Name)
	}
	if fv.createNamedCalled == 0 {
		t.Fatal("the vault create was never reached")
	}

	// The row is a reference now.
	recs := db.rows(t, 10)
	if len(recs) != 1 || !strings.Contains(recs[0].Command, "{{secret:openrouter.ai}}") {
		t.Fatalf("stored command = %q, want the reference", recs[0].Command)
	}
	if len(recs[0].Redactions) != 0 {
		t.Errorf("redactions = %+v, want the saved segment gone", recs[0].Redactions)
	}
	if strings.Contains(recs[0].Command, "sk-proj") {
		t.Errorf("the raw key reached the store: %q", recs[0].Command)
	}
}

// TestHistoryRecord_CaptureSaveRetryIsIdempotent: a lost response retries
// with the same capture id — the same name comes back and the vault create
// runs once.
func TestHistoryRecord_CaptureSaveRetryIsIdempotent(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, fv, stop := newCaptureWSServer(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	ack := recordAndDecode(t, conn, "TOKEN=abcdefghijklmnopqrstuvwxyz123456", 1)
	capID := ack.Captures[0].ID

	first := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": capID}, 2)
	second := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": capID}, 3)
	if first.Error != nil || second.Error != nil {
		t.Fatalf("save errors: %v / %v", first.Error, second.Error)
	}
	var a, b struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(first.Result, &a)
	_ = json.Unmarshal(second.Result, &b)
	if a.Name != b.Name {
		t.Errorf("retry name = %q, first = %q — the idempotent outcome must match", b.Name, a.Name)
	}
	if fv.createNamedCalled != 1 {
		t.Errorf("vault create ran %d times, want exactly once", fv.createNamedCalled)
	}
}

// TestHistoryRecord_DestroyedCaptureSavesNothing: the capture is destroyed
// (here by the transport-wide destruction the contract lists), the save is
// refused, and the row keeps its structured redaction — a masked history
// entry is never left pointing at a half-save.
func TestHistoryRecord_DestroyedCaptureSavesNothing(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, caps, stop := newCaptureWSServerWithRegistry(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	ack := recordAndDecode(t, conn, "TOKEN=abcdefghijklmnopqrstuvwxyz123456", 1)
	capID := ack.Captures[0].ID
	if len(ack.Redactions) != 1 {
		t.Fatalf("redactions = %+v, want the structured segment", ack.Redactions)
	}

	caps.DestroyAll()
	resp := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": capID}, 2)
	if resp.Error == nil {
		t.Fatal("save of a destroyed capture must fail")
	}
	if resp.Error.Code != -32010 {
		t.Errorf("error code = %d, want -32010 (capture unknown)", resp.Error.Code)
	}
	// The masked row is untouched: the redaction segment is still there.
	recs := db.rows(t, 10)
	if len(recs) != 1 || len(recs[0].Redactions) != 1 {
		t.Fatalf("row after refused save = %+v, want the structured redaction intact", recs)
	}
	if !strings.Contains(recs[0].Command, "TOKEN=abcd...3456") {
		t.Errorf("command = %q, want the masked form", recs[0].Command)
	}
}

// TestHistoryRecord_AlreadySavedStoresTheReference: a key saved this
// session is re-submitted — the row stores the existing reference
// automatically and nothing is offered.
func TestHistoryRecord_AlreadySavedStoresTheReference(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, _, stop := newCaptureWSServer(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	line := `curl -H "Authorization: Bearer sk-proj-abcdef1234567890" https://openrouter.ai/api`
	ack1 := recordAndDecode(t, conn, line, 1)
	if len(ack1.Captures) != 1 {
		t.Fatalf("first record captures = %+v", ack1.Captures)
	}
	if resp := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": ack1.Captures[0].ID}, 2); resp.Error != nil {
		t.Fatalf("save: %+v", resp.Error)
	}

	ack2 := recordAndDecode(t, conn, line, 3)
	if len(ack2.Captures) != 0 {
		t.Fatalf("re-submit of a saved value must not offer, got %+v", ack2.Captures)
	}
	recs := db.rows(t, 10)
	if len(recs) != 2 {
		t.Fatalf("rows = %d, want 2", len(recs))
	}
	if !strings.Contains(recs[0].Command, "{{secret:openrouter.ai}}") {
		t.Errorf("second row = %q, want the existing reference stored automatically", recs[0].Command)
	}
	if len(recs[0].Redactions) != 0 {
		t.Errorf("second row redactions = %+v, want none (the reference replaced the segment)", recs[0].Redactions)
	}
}

// TestHistoryRecord_AlreadyPendingLinks: the same key re-run links to the
// existing capture — one save repairs both masked rows.
func TestHistoryRecord_AlreadyPendingLinks(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, _, stop := newCaptureWSServer(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	line := "TOKEN=abcdefghijklmnopqrstuvwxyz123456"
	ack1 := recordAndDecode(t, conn, line, 1)
	ack2 := recordAndDecode(t, conn, line, 2)
	if len(ack1.Captures) != 1 || len(ack2.Captures) != 1 {
		t.Fatalf("captures = %+v / %+v, want one each", ack1.Captures, ack2.Captures)
	}
	if ack2.Captures[0].ID != ack1.Captures[0].ID {
		t.Errorf("second capture = %s, want linked to the first %s", ack2.Captures[0].ID, ack1.Captures[0].ID)
	}
	// One save repairs both rows.
	if resp := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": ack1.Captures[0].ID}, 3); resp.Error != nil {
		t.Fatalf("save: %+v", resp.Error)
	}
	recs := db.rows(t, 10)
	if len(recs) != 2 {
		t.Fatalf("rows = %d, want 2", len(recs))
	}
	for i, r := range recs {
		if !strings.Contains(r.Command, "{{secret:") {
			t.Errorf("row %d = %q, want the reference after the linked save", i, r.Command)
		}
	}
}

// TestHistoryRecord_LaterSubmissionsLeaveOlderCapturesAlone: an offer waits
// to be answered. It used to die at the next submission, which meant that
// running one more command before deciding — the ordinary thing to do —
// lost it for good. Both shapes of "next command" are exercised: one
// carrying its own key, and one carrying none.
func TestHistoryRecord_LaterSubmissionsLeaveOlderCapturesAlone(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, _, stop := newCaptureWSServer(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	ack1 := recordAndDecode(t, conn, "TOKEN=abcdefghijklmnopqrstuvwxyz123456", 1)
	if len(ack1.Captures) != 1 {
		t.Fatalf("ack1 captures = %d, want the one offer", len(ack1.Captures))
	}
	recordAndDecode(t, conn, `curl -H "Authorization: Bearer sk-proj-abcdef1234567890" https://api`, 2)
	recordAndDecode(t, conn, "ls -la", 3)

	resp := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": ack1.Captures[0].ID}, 4)
	if resp.Error != nil {
		t.Fatalf("save after two later commands = %+v, want the offer still answerable", resp.Error)
	}
}

// TestHistoryRecord_CaptureDismissOverTheWire: dismiss, then save is
// refused as consumed, and the row keeps its structured redaction.
func TestHistoryRecord_CaptureDismissOverTheWire(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, _, stop := newCaptureWSServer(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	ack := recordAndDecode(t, conn, "TOKEN=abcdefghijklmnopqrstuvwxyz123456", 1)
	if resp := vaultCall(t, conn, "secrets.captureDismiss", map[string]any{"captureId": ack.Captures[0].ID}, 2); resp.Error != nil {
		t.Fatalf("dismiss: %+v", resp.Error)
	}
	resp := vaultCall(t, conn, "secrets.captureSave", map[string]any{"captureId": ack.Captures[0].ID}, 3)
	if resp.Error == nil || resp.Error.Code != -32011 {
		t.Fatalf("save after dismiss = %+v, want -32011 (consumed)", resp.Error)
	}
	recs := db.rows(t, 10)
	if len(recs) != 1 || len(recs[0].Redactions) != 1 {
		t.Fatalf("row after dismiss = %+v, want the structured redaction intact", recs)
	}
}

// TestHistoryRecord_RedactionOffsetsAreUTF16: Cyrillic before the
// credential shifts byte and unit offsets apart; the wire must carry the
// units.
func TestHistoryRecord_RedactionOffsetsAreUTF16(t *testing.T) {
	clock := time.Unix(1_750_000_000, 0)
	db := newCaptureFakeDB()
	ws, _, stop := newCaptureWSServer(t, db, &clock)
	defer stop()
	conn := connectWS(t, ws)
	defer func() { _ = conn.Close() }()

	line := "выполнить TOKEN=abcdefghijklmnopqrstuvwxyz123456"
	ack := recordAndDecode(t, conn, line, 1)
	if len(ack.Redactions) != 1 {
		t.Fatalf("redactions = %+v, want one", ack.Redactions)
	}
	// "выполнить " is 10 UTF-16 units but 19 bytes, and the segment is the
	// VALUE's mask, which starts after TOKEN=: unit 16, byte 25.
	if ack.Redactions[0].Start != 16 {
		t.Errorf("redaction start = %d, want 16 (UTF-16 units)", ack.Redactions[0].Start)
	}
	if ack.Redactions[0].Prefix != "abcd" || ack.Redactions[0].Suffix != "3456" {
		t.Errorf("prefix/suffix = %q/%q, want the mask's head/tail", ack.Redactions[0].Prefix, ack.Redactions[0].Suffix)
	}
	// The row itself stores byte offsets — the store slices bytes.
	recs := db.rows(t, 10)
	if len(recs) != 1 || recs[0].Redactions[0].Start != 25 {
		t.Errorf("stored redaction start = %+v, want byte offset 25", recs[0].Redactions)
	}
}

// recordAndDecode records one command over the socket and decodes the ack.
func recordAndDecode(t *testing.T, conn *websocket.Conn, line string, id int) recordAck {
	t.Helper()
	resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{"command": line}), id)
	if resp.Error != nil {
		t.Fatalf("history.record error: %+v", resp.Error)
	}
	var ack recordAck
	if err := json.Unmarshal(resp.Result, &ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	return ack
}
