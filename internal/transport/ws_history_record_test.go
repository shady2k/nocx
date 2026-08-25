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
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

// fakeRecordHistoryDB is a real-behaving in-memory ContentDB over the LEDGER
// seam history.record and history.query use since nocx-rtg0.19:
// RecordCompleted stores and mints the entry id, QueryEntries serves, so a
// record-then-query round trip through the socket proves the handler
// persisted what it was handed. The real store's policy gating
// (history.enabled off → no row) is the store's own behaviour, already
// asserted in internal/content; this fake deliberately stores everything.
//
// RecordCompleted, QueryEntries and ListEntries are the only methods
// exercised — the embedded nil content.LedgerRepository turns any other call
// into a loud panic rather than a quiet zero value.
type fakeRecordHistoryDB struct {
	content.LedgerRepository
	mu      sync.Mutex
	nextID  int64
	records []content.LedgerEntrySummary
	// addErr makes the durable write fail, so the handler's honesty about a
	// refusing store is exercised rather than assumed.
	addErr error
}

func newFakeRecordHistoryDB() *fakeRecordHistoryDB {
	return &fakeRecordHistoryDB{}
}

func (f *fakeRecordHistoryDB) Conversations() content.ConversationRepository { return nil }
func (f *fakeRecordHistoryDB) Backup(_ context.Context, _ string) error {
	return content.ErrNotImplemented
}
func (f *fakeRecordHistoryDB) Close() error                      { return nil }
func (f *fakeRecordHistoryDB) Ledger() content.LedgerRepository  { return f }
func (f *fakeRecordHistoryDB) Layout() content.LayoutRepository  { return nil }
func (f *fakeRecordHistoryDB) APIRuns() content.APIRunRepository { return nil }

// RecordCompleted mints the entry id the backend owns (the renderer sends
// none) and keeps the row the way the store does: the intent, its resolved
// environment, and the payload column carrying both sparse readers' keys.
func (f *fakeRecordHistoryDB) RecordCompleted(_ context.Context, in content.CompletedCommand) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return "", f.addErr
	}
	f.nextID++
	id := fakeEntryID(f.nextID)
	env := in.Env
	f.records = append(f.records, content.LedgerEntrySummary{
		ID:            id,
		IngestSeq:     f.nextID,
		EnvironmentID: env.ID,
		Environment:   &env,
		Cwd:           in.Cwd,
		// The SOURCE the handler was handed, not a constant: the entry's
		// entries.source is where who-submitted lives (design §3.1,
		// nocx-iadtt), so a fake that hardcoded `user` here could not report
		// the source being dropped.
		Source:     in.Source,
		Intent:     in.Intent,
		Phase:      content.PhaseClosed,
		Status:     in.Status,
		StartedAt:  in.StartedAt,
		EndedAt:    in.EndedAt,
		DurationMs: in.DurationMs,
		Payload:    in.Payload,
	})
	return id, nil
}

func (f *fakeRecordHistoryDB) ListEntries(_ context.Context, limit int) ([]content.LedgerEntrySummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]content.LedgerEntrySummary, 0, len(f.records))
	for i := len(f.records) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, f.records[i])
	}
	return out, nil
}

func (f *fakeRecordHistoryDB) QueryEntries(_ context.Context, q content.LedgerQuery) (content.LedgerPage, error) {
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
	return content.LedgerPage{
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

// entries is the stored rows, newest first, for assertions about what landed.
func (f *fakeRecordHistoryDB) entries(t *testing.T, limit int) []content.LedgerEntrySummary {
	t.Helper()
	rows, err := f.ListEntries(context.Background(), limit)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	return rows
}

// fakeEntryID mints a UUID-shaped id the way the backend does. The SHAPE is
// deliberately not a decimal: nothing on this wire may parse the handle, and
// an id that happened to look like a number is how the interim table's rowid
// stayed readable for a year after it stopped being the key.
func fakeEntryID(n int64) string {
	return fmt.Sprintf("0192f0aa-0000-7000-8000-%012d", n)
}

func recordParams(overrides map[string]any) map[string]any {
	p := map[string]any{
		"command":   "ls -la",
		"cwd":       "/srv/api",
		"host":      "",
		"source":    "user",
		"status":    "success",
		"exitCode":  0,
		"startedAt": int64(1_750_000_000_000),
		"endedAt":   int64(1_750_000_000_001),
		"trusted":   true,
		"paneId":    "pane-1",
	}
	for k, v := range overrides {
		p[k] = v
	}
	return p
}

// ── the write path ────────────────────────────────────────────────────────

// A completed command's facts, sent over the real socket, land in the store:
// the record round trip is record → RecordCompleted → query, and the query
// answers source=store with the row — the same seam the recall panel uses.
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

// The record carries the full fact set the renderer derived — cwd, host,
// status, exit code, timestamps — each field verified in the store, not
// guessed from the request echo. The exit code is read back with the store's
// OWN reader (content.ShellExitCodeOf) rather than a second decoder written
// here: the column has one owner per key and this is it.
//
// `trusted` is deliberately not among them. ADR-0024 deleted the boolean, so
// content.CompletedCommand has no field for it and the ledger's shell arm
// carries only exitCode; the wire still accepts the parameter and nothing
// durable is derived from it.
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

	rows := db.entries(t, 10)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Environment == nil {
		t.Fatal("the row carries no environment — it cannot say where it ran")
	}
	if r.Intent != "sleep 10" || r.Cwd != "/tmp" || r.Environment.Host() != "prod.example.com" {
		t.Fatalf("record = %+v, want the sent facts", r)
	}
	exit, err := content.ShellExitCodeOf(r.Payload)
	if err != nil {
		t.Fatalf("shell arm: %v", err)
	}
	if r.Status != content.EntryInterrupted || exit == nil || *exit != 137 {
		t.Fatalf("status/exitCode = %q/%v, want interrupted/137", r.Status, exit)
	}
	if r.StartedAt == nil || *r.StartedAt != 1_750_000_000_000 || r.EndedAt == nil || *r.EndedAt != 1_750_000_000_100 {
		t.Fatalf("timestamps = %v/%v, want 1750000000000/1750000000100", r.StartedAt, r.EndedAt)
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

// The author is required, and it is the entries.kind vocabulary: 'shell'
// and 'agent' are the two command-bearing kinds. A request without an
// author is malformed — the renderer mints it at submit (design §3.1,
// nocx-iadtt) — and 'action' (the ledger's third kind) can never be a
// command's author: an action has no block and no command line.
func TestHistoryRecord_RejectsMissingOrUnknownSource(t *testing.T) {
	ws, stop := newHistoryWSServer(t, newFakeRecordHistoryDB())
	defer stop()
	conn := connectWS(t, ws)

	missing := recordParams(nil)
	delete(missing, "source")
	resp := vaultCall(t, conn, "history.record", missing, 1)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("missing source: error = %+v, want -32602", resp.Error)
	}

	for _, src := range []string{"robot", "action", ""} {
		resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{"source": src}), 1)
		if resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatalf("source %q: error = %+v, want -32602", src, resp.Error)
		}
	}
}

// The ack carries the source the record was accepted under, over the real
// socket: the renderer minted it at submit, and the ack's echo is how it
// verifies the backend kept the fact — the two sides never derive the same
// thing twice (design §3.1, nocx-iadtt).
func TestHistoryRecord_AckCarriesTheSource(t *testing.T) {
	db := newFakeRecordHistoryDB()
	ws, stop := newHistoryWSServer(t, db)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{
		"source": "assistant",
	}), 1)
	if resp.Error != nil {
		t.Fatalf("record error: %+v", resp.Error)
	}
	var ack struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(resp.Result, &ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.Source != "assistant" {
		t.Fatalf("ack source = %q, want assistant", ack.Source)
	}
}

// The handler writes the author into durable history, and a restart still
// sees it through both the ledger projection (entries.kind) and the
// command-history read model. That proves the wire has not outrun the store.
func TestHistoryRecord_PersistsSourceThroughRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := content.Config{
		Path: filepath.Join(dir, "content.db"),
		Key:  []byte("0123456789abcdef0123456789abcdef"),
		Budget: content.Budget{
			RetentionBytes:   1 << 20,
			DiskCeilingBytes: 2 << 20,
			CompactionFloor:  0.8,
		},
		Logger: log.NewSlogAdapter(nil),
	}
	db, err := content.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open content db: %v", err)
	}
	ws, stop := newHistoryWSServer(t, db)
	conn := connectWS(t, ws)

	for _, tc := range []struct {
		source  string
		command string
	}{
		{source: "user", command: "shell-cmd"},
		{source: "assistant", command: "agent-cmd"},
	} {
		resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{
			"source":  tc.source,
			"command": tc.command,
		}), 1)
		if resp.Error != nil {
			t.Fatalf("%s record error: %+v", tc.source, resp.Error)
		}
	}

	stop()
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close content db: %v", closeErr)
	}

	db2, err := content.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("reopen content db: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })

	entries, err := db2.Ledger().ListEntries(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("ledger entries = %d, want 2", len(entries))
	}
	if entries[0].Kind != content.EntryShell || entries[0].Intent != "agent-cmd" || entries[0].Source != content.SourceAssistant {
		t.Fatalf("newest ledger entry = %+v, want agent-cmd under the assistant source", entries[0])
	}
	if entries[1].Kind != content.EntryShell || entries[1].Intent != "shell-cmd" || entries[1].Source != content.SourceUser {
		t.Fatalf("older ledger entry = %+v, want shell-cmd under the user source", entries[1])
	}
}

// The handler surfaces the store's write failure rather than pretending the
// record landed. That keeps the wire honest when the durable home rejects.
func TestHistoryRecord_SurfacesStoreAddError(t *testing.T) {
	db := newFakeRecordHistoryDB()
	db.addErr = errors.New("boom")
	ws, stop := newHistoryWSServer(t, db)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "history.record", recordParams(map[string]any{
		"source":  "assistant",
		"command": "agent-cmd",
	}), 1)
	if resp.Error == nil || resp.Error.Code != -32603 || !strings.Contains(resp.Error.Message, "boom") {
		t.Fatalf("store error = %+v, want -32603 with boom", resp.Error)
	}
	if db.count() != 0 {
		t.Fatalf("store count = %d, want 0", db.count())
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

	// Null timestamps are a valid record (nothing observed yet), and the
	// check goes through the SAME socket rather than straight at the store:
	// the rejection above is the handler's, so the paired acceptance has to
	// be the handler's too.
	null := recordParams(nil)
	null["startedAt"] = nil
	null["endedAt"] = nil
	null["command"] = "null-times"
	if resp := vaultCall(t, conn, "history.record", null, 2); resp.Error != nil {
		t.Fatalf("null-timestamp record refused: %+v", resp.Error)
	}
	rows := db.entries(t, 10)
	if len(rows) != 2 {
		t.Fatalf("store holds %d rows, want 2", len(rows))
	}
	if rows[0].StartedAt != nil || rows[0].EndedAt != nil {
		t.Fatalf("timestamps = %v/%v, want both null — the ledger only stamps what it observed",
			rows[0].StartedAt, rows[0].EndedAt)
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
// same state where history.query answers source=unavailable, because there
// is no store to have answered.
func TestHistoryRecord_NoStoreIsAcceptedAndRecordsNothing(t *testing.T) {
	ws, stop := newHistoryWSServer(t, nil)
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "history.record", recordParams(nil), 1)
	if resp.Error != nil {
		t.Fatalf("record error: %+v", resp.Error)
	}
	got := decodeHistoryResult(t, vaultCall(t, conn, "history.query", map[string]any{"scope": "everywhere"}, 2))
	if got.Source != "unavailable" {
		t.Fatalf("source = %q, want unavailable", got.Source)
	}
}

// A store that fails to record is an error the caller can act on, never a
// silent drop: broken and unavailable must not collapse into each other.
func TestHistoryRecord_StoreErrorIsRPCError(t *testing.T) {
	db := &fakeHistoryDB{} // RecordCompleted returns ErrNotImplemented
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

	recs := db.entries(t, 1)
	if len(recs) != 1 || recs[0].Intent != command {
		t.Fatalf("stored command = %q, want the reference intact byte for byte (%q)", recs[0].Intent, command)
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
			Source:        "user",
			Redactions:    []redactionWire{},
			Captures:      []captureWire{},
			MaskedCommand: "echo hi",
		},
		"two kinds": {
			MaskedCount:   2,
			MaskedKinds:   []string{"openai"},
			Source:        "assistant",
			MaskedCommand: `curl -H "Authorization: Bearer sk-p...7890" https://api`,
			Redactions: []redactionWire{
				{Kind: "openai", Start: 10, End: 21, Prefix: "sk-p", Suffix: "7890"},
			},
			Captures: []captureWire{},
		},
		"with an offer": {
			MaskedCount:   1,
			MaskedKinds:   []string{"openai"},
			EntryID:       "0192f0aa-0000-7000-8000-000000000007",
			Source:        "user",
			MaskedCommand: `curl -H "Authorization: Bearer sk-p...7890" https://api`,
			Redactions:    []redactionWire{{Kind: "openai", Start: 10, End: 21, Prefix: "sk-p", Suffix: "7890"}},
			Captures: []captureWire{{
				ID: "cap_abc", EntryID: "0192f0aa-0000-7000-8000-000000000007",
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
		"source":  "assistant",
	}), 1)
	if resp.Error != nil {
		t.Fatalf("record error: %+v", resp.Error)
	}
	validateJSON(t, schema, resp.Result, "history.record result (real socket)")

	var got struct {
		Source        string   `json:"source"`
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
	if got.Source != "assistant" {
		t.Errorf("ack source = %q, want assistant — the minted fact rides the wire both ways", got.Source)
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
	recs := db.entries(t, 1)
	if len(recs) != 1 || recs[0].Intent != `curl -H "Authorization: Bearer sk-p...7890" https://api` {
		t.Errorf("stored command = %+v, want the masked one", recs)
	}
	if strings.Contains(recs[0].Intent, "sk-proj-abcdef1234567890") {
		t.Errorf("the raw key reached the store: %q", recs[0].Intent)
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

// ── the loss policy, as a product contract (nocx-rtg0.10) ────────────────

// N failing writes say so ONCE, not N times — which is the whole difference
// between a product stating a degrade and a product shouting about every
// symptom of it. Commands keep running throughout: history.record answering
// an error is not the terminal's problem, and the renderer already treats a
// dropped record exactly like nothing to show.
func TestHistoryRecord_AFailingStoreIsAnnouncedOncePerEpisode(t *testing.T) {
	db := &fakeHistoryDB{} // RecordCompleted returns ErrNotImplemented
	status := NewHistoryStatus()
	announcements := 0
	status.AddListener(func() { announcements++ })

	ws, stop := newHistoryWSServer(t, db, WithHistoryStatus(status))
	defer stop()
	conn := connectWS(t, ws)

	const commands = 5
	for i := range commands {
		resp := vaultCall(t, conn, "history.record", recordParams(nil), i+1)
		// Every one of them answers. A command that ran is never left
		// without a reply because the store is broken.
		if resp.Error == nil || resp.Error.Code != -32603 {
			t.Fatalf("record %d: error = %+v, want -32603", i, resp.Error)
		}
	}

	if announcements != 1 {
		t.Fatalf("the surface announced %d times for %d failing writes, want exactly 1",
			announcements, commands)
	}
	if status.Available() {
		t.Fatal("durable history reports available while every write is failing")
	}
}

// And the episode CLOSES on the first write that lands, because an interval
// with no closing event is a notice that never goes away.
func TestHistoryRecord_TheEpisodeEndsWhenAWriteLands(t *testing.T) {
	db := newFakeRecordHistoryDB()
	status := NewHistoryStatus()
	status.Raise(HistoryDegradeWriteFailed, "the store was refusing writes")

	ws, stop := newHistoryWSServer(t, db, WithHistoryStatus(status))
	defer stop()
	conn := connectWS(t, ws)

	resp := vaultCall(t, conn, "history.record", recordParams(nil), 1)
	if resp.Error != nil {
		t.Fatalf("record: %+v", resp.Error)
	}
	if !status.Available() {
		t.Fatal("durable history still reports unavailable after a write landed")
	}
}

// A STARTUP DEGRADE IS NOT CLOSED BY A RUNTIME SUCCESS. An episode is ended
// by the event that ends IT: one recorded command does not disprove "the
// content key could not be read", and clearing that would erase a sentence
// that is still true and that nothing else would ever say again.
func TestHistoryRecord_ASuccessDoesNotEraseADifferentDegrade(t *testing.T) {
	db := newFakeRecordHistoryDB()
	status := NewHistoryStatus()
	status.Raise(HistoryDegradeNoKey, "contentkey: open salt: is a directory")

	ws, stop := newHistoryWSServer(t, db, WithHistoryStatus(status))
	defer stop()
	conn := connectWS(t, ws)

	if resp := vaultCall(t, conn, "history.record", recordParams(nil), 1); resp.Error != nil {
		t.Fatalf("record: %+v", resp.Error)
	}
	if status.Available() {
		t.Fatal("a successful write cleared a degrade it did not open")
	}
}
