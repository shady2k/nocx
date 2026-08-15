package transport

// history.record (nocx-rtg0.13) — the write half of the history family.
// The frontend sends a completed command's facts over the control plane
// (AD-1 as amended); the handler hands them to the store. These tests drive
// the real handler through the real socket, so the wire is a party to the
// contract: history.record followed by history.query is the same round trip
// a user's terminal makes.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
)

// fakeRecordHistoryDB is a real-behaving in-memory ContentDB: Add stores,
// Query serves, so a record-then-query round trip through the socket proves
// the handler persisted what it was handed. The real store's policy gating
// (history.enabled off → no row) is the store's own behaviour, already
// asserted in internal/content; this fake deliberately stores everything.
type fakeRecordHistoryDB struct {
	mu      sync.Mutex
	nextID  int64
	records []content.CommandRecord
}

func newFakeRecordHistoryDB() *fakeRecordHistoryDB {
	return &fakeRecordHistoryDB{nextID: 1}
}

func (f *fakeRecordHistoryDB) CommandHistory() content.CommandHistoryRepository { return f }
func (f *fakeRecordHistoryDB) Conversations() content.ConversationRepository    { return nil }
func (f *fakeRecordHistoryDB) Backup(_ context.Context, _ string) error {
	return content.ErrNotImplemented
}
func (f *fakeRecordHistoryDB) Close() error { return nil }
func (f *fakeRecordHistoryDB) RestorePrivate(_ context.Context, _ []content.Conversation, _ []content.CommandRecord) error {
	return content.ErrNotImplemented
}
func (f *fakeRecordHistoryDB) Ledger() content.LedgerRepository { return nil }

func (f *fakeRecordHistoryDB) Add(_ context.Context, record content.CommandRecord) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record.ID = f.nextID
	f.nextID++
	f.records = append(f.records, record)
	return record.ID, nil
}

func (f *fakeRecordHistoryDB) RewriteRedaction(_ context.Context, _ int64, _ content.Redaction, _ string) error {
	return nil
}

func (f *fakeRecordHistoryDB) List(_ context.Context, limit int) ([]content.CommandRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]content.CommandRecord, 0, len(f.records))
	for i := len(f.records) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, f.records[i])
	}
	return out, nil
}

func (f *fakeRecordHistoryDB) GetByID(_ context.Context, id int64) (*content.CommandRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.records {
		if f.records[i].ID == id {
			r := f.records[i]
			return &r, nil
		}
	}
	return nil, nil
}

func (f *fakeRecordHistoryDB) FindByPrefix(_ context.Context, _ string, limit int) ([]content.CommandRecord, error) {
	return f.List(context.Background(), limit)
}

func (f *fakeRecordHistoryDB) Query(_ context.Context, scope content.Scope, cwd, host string, limit int, before *int64, _ string) (content.HistoryPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var entries []content.CommandRecord
	for i := len(f.records) - 1; i >= 0; i-- {
		r := f.records[i]
		switch scope {
		case content.ScopeDirectory:
			if r.Cwd != cwd || r.Host != host {
				continue
			}
		case content.ScopeHost:
			if r.Host != host {
				continue
			}
		}
		if before != nil && r.ID >= *before {
			continue
		}
		entries = append(entries, r)
		if len(entries) >= limit {
			break
		}
	}
	return content.HistoryPage{
		Entries:   entries,
		Exhausted: len(entries) < len(f.records),
		HasRows:   len(f.records) > 0,
	}, nil
}

func (f *fakeRecordHistoryDB) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

func recordParams(overrides map[string]any) map[string]any {
	p := map[string]any{
		"command":   "ls -la",
		"cwd":       "/srv/api",
		"host":      "",
		"status":    "success",
		"exitCode":  0,
		"startedAt": int64(1_750_000_000_000),
		"endedAt":   int64(1_750_000_000_001),
		"trusted":   true,
	}
	for k, v := range overrides {
		p[k] = v
	}
	return p
}

// ── the write path ────────────────────────────────────────────────────────

// A completed command's facts, sent over the real socket, land in the store:
// the record round trip is record → Add → query, and the query answers
// source=store with the row — the same seam the recall panel uses.
func TestHistoryRecord_ThenQuerySeesTheRow(t *testing.T) {
	db := newFakeRecordHistoryDB()
	ws, stop := newHistoryWSServer(t, db)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "history.record", recordParams(nil), 1)
	if resp.Error != nil {
		t.Fatalf("record error: %+v", resp.Error)
	}
	if db.count() != 1 {
		t.Fatalf("store holds %d rows, want 1", db.count())
	}

	got := decodeHistoryResult(t, vaultCall(t, conn, "history.query", map[string]any{
		"scope": "directory", "cwd": "/srv/api", "host": "", "limit": 50,
	}, 2))
	if got.Source != "store" {
		t.Fatalf("source = %q, want store", got.Source)
	}
	if len(got.Entries) != 1 || got.Entries[0].Command != "ls -la" {
		t.Fatalf("entries = %+v, want the recorded command", got.Entries)
	}
	if got.Entries[0].Status != "success" || got.Entries[0].ExitCode == nil || *got.Entries[0].ExitCode != 0 {
		t.Fatalf("entry does not carry the recorded facts: %+v", got.Entries[0])
	}
}

// The record carries the full fact set the ledger derived — cwd, host, exit
// code, timestamps, trust — each field verified in the store, not guessed
// from the request echo.
func TestHistoryRecord_PersistsEveryFact(t *testing.T) {
	db := newFakeRecordHistoryDB()
	ws, stop := newHistoryWSServer(t, db)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{
		"command":   "sleep 10",
		"cwd":       "/tmp",
		"host":      "prod.example.com",
		"status":    "interrupted",
		"exitCode":  137,
		"startedAt": int64(1_750_000_000_000),
		"endedAt":   int64(1_750_000_000_100),
		"trusted":   true,
	}), 1)
	if resp.Error != nil {
		t.Fatalf("record error: %+v", resp.Error)
	}

	rows, err := db.List(context.Background(), 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Command != "sleep 10" || r.Cwd != "/tmp" || r.Host != "prod.example.com" {
		t.Fatalf("record = %+v, want the sent facts", r)
	}
	if r.Status != content.StatusInterrupted || r.ExitCode == nil || *r.ExitCode != 137 {
		t.Fatalf("status/exitCode = %q/%v, want interrupted/137", r.Status, r.ExitCode)
	}
	if r.StartedAt == nil || *r.StartedAt != 1_750_000_000_000 || r.EndedAt == nil || *r.EndedAt != 1_750_000_000_100 {
		t.Fatalf("timestamps = %v/%v, want 1750000000000/1750000000100", r.StartedAt, r.EndedAt)
	}
	if !r.Trusted {
		t.Fatal("trusted = false, want true")
	}
}

// A record whose command is empty or whitespace-only is rejected: the store
// must never hold a row that is not a command.
func TestHistoryRecord_RejectsEmptyCommand(t *testing.T) {
	ws, stop := newHistoryWSServer(t, newFakeRecordHistoryDB())
	defer stop()
	conn := connectWS(t, ws)

	for _, cmd := range []string{"", "   ", "\t\n"} {
		resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{"command": cmd}), 1)
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("command %q: error = %+v, want -32602", cmd, resp.Error)
		}
	}
}

// An unknown status is rejected: the closed set in command-ledger.ts is the
// only vocabulary the store understands.
func TestHistoryRecord_RejectsUnknownStatus(t *testing.T) {
	ws, stop := newHistoryWSServer(t, newFakeRecordHistoryDB())
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{"status": "crashed"}), 1)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("error = %+v, want -32602", resp.Error)
	}
}

// A performance.now()-shaped timestamp (milliseconds since page load) is
// rejected: the store reads ended_at as Unix epoch milliseconds and sweeps
// anything below retention, so a 1970 timestamp is deleted the moment it is
// written. The wrong clock must surface as an error the renderer can log,
// never as a row that vanishes (nocx-rtg0.16). Each field is checked
// independently, and the message names the field that failed.
func TestHistoryRecord_RejectsPerformanceNowTimestamps(t *testing.T) {
	ws, stop := newHistoryWSServer(t, newFakeRecordHistoryDB())
	defer stop()
	conn := connectWS(t, ws)

	cases := []struct {
		name   string
		params map[string]any
		field  string
	}{
		{
			name:   "endedAt at page-load milliseconds (the rtg0.16 repro)",
			params: map[string]any{"startedAt": int64(755), "endedAt": int64(757)},
			field:  "startedAt",
		},
		{
			name:   "endedAt alone is page-load milliseconds",
			params: map[string]any{"endedAt": int64(757)},
			field:  "endedAt",
		},
		{
			name:   "startedAt alone is page-load milliseconds",
			params: map[string]any{"startedAt": int64(755)},
			field:  "startedAt",
		},
		{
			name:   "one second before the 2020-01-01 floor",
			params: map[string]any{"startedAt": int64(1_577_836_799_999), "endedAt": int64(1_750_000_000_000)},
			field:  "startedAt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := vaultCall(t, conn, "history.record", recordParams(tc.params), 1)
			if resp.Error == nil || resp.Error.Code != -32602 {
				t.Fatalf("error = %+v, want -32602", resp.Error)
			}
			if !strings.Contains(resp.Error.Message, tc.field) {
				t.Fatalf("error message %q does not name the field %q", resp.Error.Message, tc.field)
			}
		})
	}
}

// The paired acceptance (rule: for every "returns an error when…" there is
// an "and on a normal input it succeeds"): a record whose timestamps are
// ordinary epoch milliseconds — including exactly the 2020-01-01 floor —
// is accepted and lands in the store. A null timestamp stays valid too:
// the ledger only stamps what it observed, and the schema keeps both
// fields nullable.
func TestHistoryRecord_AcceptsEpochTimestamps(t *testing.T) {
	db := newFakeRecordHistoryDB()
	ws, stop := newHistoryWSServer(t, db)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{
		"startedAt": int64(1_577_836_800_000), // 2020-01-01T00:00:00Z exactly
		"endedAt":   int64(1_750_000_000_000),
	}), 1)
	if resp.Error != nil {
		t.Fatalf("record error at the epoch floor: %+v", resp.Error)
	}
	if db.count() != 1 {
		t.Fatalf("store holds %d rows, want 1", db.count())
	}

	// Null timestamps are a valid record (nothing observed yet).
	if _, err := db.Add(context.Background(), content.CommandRecord{
		Command: "null-times", Cwd: "/", Host: "", Status: content.StatusRunning,
	}); err != nil {
		t.Fatalf("null-timestamp record rejected by the store: %v", err)
	}
}

// Garbage params are rejected, not interpreted.
func TestHistoryRecord_RejectsGarbageParams(t *testing.T) {
	ws, stop := newHistoryWSServer(t, newFakeRecordHistoryDB())
	defer stop()
	conn := connectWS(t, ws)

	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "history.record", "params": "not-an-object", "id": 1,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, req); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := readVaultResult(t, conn)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("error = %+v, want -32602", resp.Error)
	}
}

// With no store wired the request is accepted and recorded nowhere — the
// same state where history.query answers source=session.
func TestHistoryRecord_NoStoreIsAcceptedAndRecordsNothing(t *testing.T) {
	ws, stop := newHistoryWSServer(t, nil)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "history.record", recordParams(nil), 1)
	if resp.Error != nil {
		t.Fatalf("record error: %+v", resp.Error)
	}
	got := decodeHistoryResult(t, vaultCall(t, conn, "history.query", map[string]any{"scope": "everywhere"}, 2))
	if got.Source != "session" {
		t.Fatalf("source = %q, want session", got.Source)
	}
}

// A store that fails to Add is an error the caller can act on, never a
// silent drop: broken and unavailable must not collapse into each other.
func TestHistoryRecord_StoreErrorIsRPCError(t *testing.T) {
	db := &fakeHistoryDB{} // Add returns ErrNotImplemented
	ws, stop := newHistoryWSServer(t, db)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "history.record", recordParams(nil), 1)
	if resp.Error == nil || resp.Error.Code != -32603 {
		t.Fatalf("error = %+v, want -32603", resp.Error)
	}
}

// The invariant part 2 exists to protect, end to end: a command carrying a
// vault reference is recorded with the reference INTACT. Mask leaves
// {{secret:NAME}} alone, so nothing is masked, the row stores the line
// byte for byte, and a command that moves to another machine still resolves
// that machine's secret.
func TestHistoryRecord_StoresReferenceUnchanged(t *testing.T) {
	db := newFakeRecordHistoryDB()
	ws, stop := newHistoryWSServer(t, db)
	defer stop()
	conn := connectWS(t, ws)

	command := `curl -H "Authorization: Bearer {{secret:OPENAI}}" https://api`
	resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{
		"command": command,
	}), 1)
	if resp.Error != nil {
		t.Fatalf("record error: %+v", resp.Error)
	}
	var ack struct {
		MaskedCount int `json:"maskedCount"`
	}
	if err := json.Unmarshal(resp.Result, &ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.MaskedCount != 0 {
		t.Fatalf("maskedCount = %d, want 0 — a reference is not a secret", ack.MaskedCount)
	}

	recs, err := db.List(context.Background(), 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 || recs[0].Command != command {
		t.Fatalf("stored command = %q, want the reference intact byte for byte (%q)", recs[0].Command, command)
	}
}

// ── the contract ──────────────────────────────────────────────────────────

// The DTO's own conformance: field tags, nil-slice-as-null, and the
// never-null maskedKinds. The handler always sends the facts it computed, so
// the zero-value struct is not a shape the wire produces — the empty shape
// is maskedCount 0 with maskedKinds [].
func TestHistoryRecord_DTOConformsToContract(t *testing.T) {
	schema := loadSchema(t, "history.record.schema.json")
	cases := map[string]historyRecordResponse{
		"nothing masked": {
			MaskedCount:   0,
			MaskedKinds:   []string{},
			Redactions:    []redactionWire{},
			Captures:      []captureWire{},
			MaskedCommand: "echo hi",
		},
		"two kinds": {
			MaskedCount:   2,
			MaskedKinds:   []string{"openai", "jwt"},
			MaskedCommand: `curl -H "Authorization: Bearer sk-p...7890" https://api`,
			Redactions: []redactionWire{
				{Kind: "openai", Start: 10, End: 21, Prefix: "sk-p", Suffix: "7890"},
			},
			Captures: []captureWire{},
		},
		"with an offer": {
			MaskedCount:   1,
			MaskedKinds:   []string{"openai"},
			EntryID:       "7",
			MaskedCommand: `curl -H "Authorization: Bearer sk-p...7890" https://api`,
			Redactions:    []redactionWire{{Kind: "openai", Start: 10, End: 21, Prefix: "sk-p", Suffix: "7890"}},
			Captures: []captureWire{{
				ID: "cap_abc", EntryID: "7",
				Redaction:     redactionWire{Kind: "openai", Start: 10, End: 21, Prefix: "sk-p", Suffix: "7890"},
				SuggestedName: "openrouter.ai",
			}},
		},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateJSON(t, schema, raw, "history.record DTO")
		})
	}
}

// The real result off the real socket satisfies the schema — not a payload
// the test itself built. An extra field in the ack would fail here even
// though the DTO test would stay green. The command carries a real key
// shape, so the facts off the socket are real: one masked secret of kind
// openai, and the durable row holds the masked command, never the key.
func TestHistoryRecord_OverTheWireConformsToContract(t *testing.T) {
	schema := loadSchema(t, "history.record.schema.json")
	db := newFakeRecordHistoryDB()
	ws, stop := newHistoryWSServer(t, db)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{
		"command": `curl -H "Authorization: Bearer sk-proj-abcdef1234567890" https://api`,
	}), 1)
	if resp.Error != nil {
		t.Fatalf("record error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "history.record result (real socket)")

	var got struct {
		MaskedCount   int      `json:"maskedCount"`
		MaskedKinds   []string `json:"maskedKinds"`
		MaskedCommand string   `json:"maskedCommand"`
		Captures      []struct {
			ID            string `json:"id"`
			SuggestedName string `json:"suggestedName"`
		} `json:"captures"`
	}
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if got.MaskedCount != 1 || len(got.MaskedKinds) != 1 || got.MaskedKinds[0] != "openai" {
		t.Errorf("ack facts = %d %v, want 1 [openai]", got.MaskedCount, got.MaskedKinds)
	}
	if got.MaskedCommand != `curl -H "Authorization: Bearer sk-p...7890" https://api` {
		t.Errorf("maskedCommand = %q, want the masked command the row keeps", got.MaskedCommand)
	}
	if strings.Contains(got.MaskedCommand, "sk-proj-abcdef1234567890") {
		t.Errorf("maskedCommand carries the raw key: %q", got.MaskedCommand)
	}
	if len(got.Captures) != 1 || got.Captures[0].ID == "" || got.Captures[0].SuggestedName == "" {
		t.Errorf("captures = %+v, want one offer carrying its id and suggested name", got.Captures)
	}
	recs, listErr := db.List(context.Background(), 1)
	if listErr != nil {
		t.Fatalf("list: %v", listErr)
	}
	if len(recs) != 1 || recs[0].Command != `curl -H "Authorization: Bearer sk-p...7890" https://api` {
		t.Errorf("stored command = %+v, want the masked one", recs)
	}
	if strings.Contains(recs[0].Command, "sk-proj-abcdef1234567890") {
		t.Errorf("the raw key reached the store: %q", recs[0].Command)
	}
}

// readVaultResult reads one JSON-RPC response off the socket — the
// raw-message variant of vaultCall, for requests built by hand.
func readVaultResult(t *testing.T, conn *websocket.Conn) *vaultRPCResult {
	t.Helper()
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var resp vaultRPCResult
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return &resp
}
