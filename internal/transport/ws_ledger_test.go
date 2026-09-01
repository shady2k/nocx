package transport

// ledger.open / ledger.bind / ledger.close over the REAL socket into the REAL
// store (nocx-rtg0.3). These are the bead's acceptance assertions, written
// from the brief rather than from the implementation: the phases walk
// forwards and only forwards, a close for an id nobody opened creates exactly
// one row from its envelope, every event is idempotent by (id, phase), and
// every store call this path makes has a test where that call fails.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
)

// ── harness ───────────────────────────────────────────────────────────────

// newLedgerWSServer wires a WSServer over a REAL content store with a caller
// supplied logger — the ledger's drop rule is "dropped AND LOGGED", so the
// log is part of the contract and the test has to be able to read it. Same
// shape as newAgentWSServer, which cannot take a logger.
func newLedgerWSServer(t *testing.T, logger log.Logger, db content.ContentDB) (*WSServer, func()) {
	t.Helper()
	ctx := context.Background()
	ws := NewWSServer(logger, newRegWithStub(logger), WithContentDB(db))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ws, func() { _ = ws.Stop(ctx) }
}

// newLedgerStore opens a real, keyed content store in a temp dir.
func newLedgerStore(t *testing.T) content.ContentDB {
	t.Helper()
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(context.Background(), content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// ledgerEnv builds the immutable envelope every ledger event repeats.
func ledgerEnv(sid, id, intent string, clientSeq int) map[string]any {
	return map[string]any{
		"id":          id,
		"sessionId":   sid,
		"cwd":         "/repo",
		"kind":        "shell",
		"intent":      intent,
		"sensitivity": "normal",
		"clientSeq":   clientSeq,
	}
}

func ledgerBindEnv(sid, id, intent string, clientSeq int) map[string]any {
	env := ledgerEnv(sid, id, intent, clientSeq)
	env["attemptId"] = "attempt-" + id
	return env
}

type ledgerAck struct {
	ID          string `json:"id"`
	ClientSeq   int64  `json:"clientSeq"`
	Seq         int64  `json:"seq"`
	SubmittedAt int64  `json:"submittedAt"`
	Phase       string `json:"phase"`
	Outcome     string `json:"outcome"`
}

// ledgerCall sends one ledger.* request and decodes the ack.
func ledgerCall(t *testing.T, conn *websocket.Conn, method string, params map[string]any, id int) (ledgerAck, *jsonrpcErrorObj) {
	t.Helper()
	raw := jsonrpcCallWithID(t, conn, method, params, id)
	var env struct {
		Result ledgerAck        `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %s response: %v\nraw: %s", method, err, raw)
	}
	return env.Result, env.Error
}

func mustEntry(t *testing.T, db content.ContentDB, id string) *content.LedgerEntry {
	t.Helper()
	e, err := db.Ledger().Entry(context.Background(), id)
	if err != nil {
		t.Fatalf("Entry(%q): %v", id, err)
	}
	if e == nil {
		t.Fatalf("no ledger row carries id %q", id)
	}
	return e
}

func entryCount(t *testing.T, db content.ContentDB) int {
	t.Helper()
	rows, err := db.Ledger().ListEntries(context.Background(), 1000)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	return len(rows)
}

// ── the happy path: the phases walk forwards ──────────────────────────────

// A user runs a command: the renderer opens the entry at submit, binds it at
// OSC 133 C and closes it at D. Off the real socket, into the real store, the
// row walks open → bound → closed and takes its final status.
func TestLedgerOpenBindClose_WalksThePhasesOverTheWire(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	env := ledgerEnv(sid, "entry-1", "make test", 1)

	ack, errObj := ledgerCall(t, conn, "ledger.open", map[string]any{"envelope": env}, 2)
	if errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	if ack.Outcome != "applied" || ack.Phase != "open" {
		t.Fatalf("open ack = %+v, want applied/open", ack)
	}
	if ack.Seq <= 0 {
		t.Fatalf("open ack seq = %d, want the backend-assigned ingest_seq", ack.Seq)
	}
	if ack.SubmittedAt <= 0 {
		t.Fatalf("open ack submittedAt = %d, want the store's wall clock", ack.SubmittedAt)
	}
	if ack.ClientSeq != 1 {
		t.Fatalf("open ack clientSeq = %d, want the envelope's 1 echoed back", ack.ClientSeq)
	}
	if row := mustEntry(t, db, "entry-1"); row.Phase != content.PhaseOpen || row.Status != content.EntryPending {
		t.Fatalf("after open: phase=%q status=%q, want open/pending", row.Phase, row.Status)
	}

	env2 := ledgerBindEnv(sid, "entry-1", "make test", 2)
	ack, errObj = ledgerCall(t, conn, "ledger.bind", map[string]any{"envelope": env2}, 3)
	if errObj != nil {
		t.Fatalf("ledger.bind error: %+v", errObj)
	}
	if ack.Outcome != "applied" || ack.Phase != "bound" {
		t.Fatalf("bind ack = %+v, want applied/bound", ack)
	}
	row := mustEntry(t, db, "entry-1")
	if row.Phase != content.PhaseBound {
		t.Fatalf("after bind: phase=%q, want bound", row.Phase)
	}
	if len(row.Executions) != 1 {
		t.Fatalf("after bind: %d executions, want exactly 1", len(row.Executions))
	}

	env3 := ledgerEnv(sid, "entry-1", "make test", 3)
	ack, errObj = ledgerCall(t, conn, "ledger.close", map[string]any{
		"envelope":   env3,
		"status":     "failure",
		"facts":      map[string]any{"terminationReason": "failed"},
		"durationMs": 2300,
	}, 4)
	if errObj != nil {
		t.Fatalf("ledger.close error: %+v", errObj)
	}
	if ack.Outcome != "applied" || ack.Phase != "closed" {
		t.Fatalf("close ack = %+v, want applied/closed", ack)
	}
	row = mustEntry(t, db, "entry-1")
	if row.Phase != content.PhaseClosed || row.Status != content.EntryFailure {
		t.Fatalf("after close: phase=%q status=%q, want closed/failure", row.Phase, row.Status)
	}
	if len(row.Executions) != 1 {
		t.Fatalf("after close: %d executions, want exactly 1", len(row.Executions))
	}
	ex := row.Executions[0]
	if ex.EndedAt == nil {
		t.Fatal("after close: the execution has no ended_at")
	}
	if ex.TerminationReason == nil || *ex.TerminationReason != content.TermFailed {
		t.Fatalf("after close: termination reason = %v, want failed", ex.TerminationReason)
	}
	if entryCount(t, db) != 1 {
		t.Fatalf("the whole cycle wrote %d rows, want exactly 1", entryCount(t, db))
	}
}

// ── §6.3 rule 3: a close for an unknown id creates its row ────────────────

// The open was lost (a socket that dropped between submit and ledger.open).
// The close carries the whole immutable envelope, so the row is created
// closed from it — environment, cwd, kind and intent all come from the
// envelope, and exactly ONE row appears.
func TestLedgerClose_ForAnIdNeverOpened_CreatesExactlyOneClosedRow(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	ack, errObj := ledgerCall(t, conn, "ledger.close", map[string]any{
		"envelope":   ledgerEnv(sid, "orphan-1", "git push", 7),
		"status":     "success",
		"facts":      map[string]any{"terminationReason": "completed"},
		"durationMs": 120,
	}, 2)
	if errObj != nil {
		t.Fatalf("ledger.close error: %+v", errObj)
	}
	if ack.Outcome != "applied" || ack.Phase != "closed" {
		t.Fatalf("close ack = %+v, want applied/closed", ack)
	}

	if n := entryCount(t, db); n != 1 {
		t.Fatalf("a close for an unknown id created %d rows, want exactly 1", n)
	}
	row := mustEntry(t, db, "orphan-1")
	if row.Phase != content.PhaseClosed || row.Status != content.EntrySuccess {
		t.Fatalf("phase=%q status=%q, want closed/success", row.Phase, row.Status)
	}
	if row.Cwd != "/repo" {
		t.Fatalf("cwd = %q, want the envelope's /repo", row.Cwd)
	}
	if row.Kind != content.EntryShell {
		t.Fatalf("kind = %q, want the envelope's shell", row.Kind)
	}
	if row.Intent != "git push" {
		t.Fatalf("intent = %q, want the envelope's text", row.Intent)
	}
	// The environment is derived by the BACKEND from the session, never
	// minted by the renderer (AD-7, environmentForSession): a local session
	// is the local environment's derived id.
	wantEnv := content.EnvironmentIDFor(content.EnvLocal, "")
	if row.EnvironmentID != wantEnv {
		t.Fatalf("environmentId = %q, want the backend-derived %q", row.EnvironmentID, wantEnv)
	}
	if len(row.Executions) != 1 {
		t.Fatalf("%d executions, want exactly 1 — a closed entry has a run", len(row.Executions))
	}
}

// ── the two close paths record the same facts (nocx-rtg0.23) ──────────────

// closedFacts is everything a close is supposed to leave on the entry. ended
// is a presence flag, not a value: the store stamps the execution's end from
// its OWN wall clock, so two closes a few milliseconds apart legitimately
// differ there and nowhere else.
type closedFacts struct {
	Phase      content.Phase
	Status     content.EntryStatus
	Cwd        string
	Kind       content.EntryKind
	Intent     string
	StartedAt  *int64
	DurationMs *int64
	Payload    string
	Executions int
	Ended      bool
}

func factsOf(t *testing.T, db content.ContentDB, id string) closedFacts {
	t.Helper()
	row := mustEntry(t, db, id)
	return closedFacts{
		Phase: row.Phase, Status: row.Status, Cwd: row.Cwd, Kind: row.Kind,
		Intent: row.Intent, StartedAt: row.StartedAt, DurationMs: row.DurationMs,
		Payload: row.Payload, Executions: len(row.Executions), Ended: row.EndedAt != nil,
	}
}

// The defect this bead fixes, stated as the user's outcome: whether the open
// arrived or was lost, the closed row says the same thing. Both paths are
// asserted against ONE expected row, because a test that exercised only one
// of them could not have reported that they differed.
func TestLedgerClose_BothPathsRecordTheSameFacts(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	closeParams := func(id string, clientSeq int) map[string]any {
		return map[string]any{
			"envelope":   ledgerEnv(sid, id, "make test", clientSeq),
			"status":     "failure",
			"facts":      map[string]any{"terminationReason": "failed", "exitCode": 2},
			"durationMs": 2300,
			"startedAt":  1_750_000_047_700,
		}
	}

	// Path 1: the open arrived, so the row already exists when the close lands.
	if _, errObj := ledgerCall(t, conn, "ledger.open",
		map[string]any{"envelope": ledgerEnv(sid, "twin-open", "make test", 1)}, 2); errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	if _, errObj := ledgerCall(t, conn, "ledger.close", closeParams("twin-open", 2), 3); errObj != nil {
		t.Fatalf("ledger.close on the open row: %+v", errObj)
	}

	// Path 2: the open was lost, so the close creates its own row.
	if _, errObj := ledgerCall(t, conn, "ledger.close", closeParams("twin-orphan", 1), 4); errObj != nil {
		t.Fatalf("ledger.close creating its row: %+v", errObj)
	}

	want := closedFacts{
		Phase: content.PhaseClosed, Status: content.EntryFailure, Cwd: "/repo",
		Kind: content.EntryShell, Intent: "make test",
		StartedAt: i64(1_750_000_047_700), DurationMs: i64(2300),
		// One column, two writers, and the close merges rather than assigns
		// (nocx-rtg0.24): the open's redaction receipt — empty here, because
		// `make test` carries no secret — and the close's shell arm. The
		// string is compared byte for byte because the point of this test is
		// that the two paths agree, and "agree" includes how the payload was
		// composed.
		Payload:    `{"masking":{"maskedCount":0,"maskedKinds":[],"redactions":[]},"kind":"shell","v":1,"exitCode":2}`,
		Executions: 1, Ended: true,
	}
	for _, id := range []string{"twin-open", "twin-orphan"} {
		got := factsOf(t, db, id)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\n got  %s\n want %s", id, showFacts(got), showFacts(want))
		}
	}
}

func i64(n int64) *int64 { return &n }

func showFacts(f closedFacts) string {
	started, duration := "nil", "nil"
	if f.StartedAt != nil {
		started = strconv.FormatInt(*f.StartedAt, 10)
	}
	if f.DurationMs != nil {
		duration = strconv.FormatInt(*f.DurationMs, 10)
	}
	return fmt.Sprintf("phase=%s status=%s cwd=%s kind=%s intent=%q startedAt=%s durationMs=%s payload=%s execs=%d ended=%v",
		f.Phase, f.Status, f.Cwd, f.Kind, f.Intent, started, duration, f.Payload, f.Executions, f.Ended)
}

// Re-delivery of a close is a no-op, and now that a close carries facts, "no
// op" has to mean the facts too: counted rows and executions, and the stored
// facts identical before and after the second delivery.
func TestLedgerCloseSentTwice_ChangesNothing(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	params := map[string]any{
		"envelope":   ledgerEnv(sid, "twice-1", "make test", 1),
		"status":     "success",
		"facts":      map[string]any{"terminationReason": "completed", "exitCode": 0},
		"durationMs": 700,
		"startedAt":  1_750_000_047_700,
	}
	if _, errObj := ledgerCall(t, conn, "ledger.close", params, 2); errObj != nil {
		t.Fatalf("first close: %+v", errObj)
	}
	first := factsOf(t, db, "twice-1")

	ack, errObj := ledgerCall(t, conn, "ledger.close", params, 3)
	if errObj != nil {
		t.Fatalf("second close: %+v", errObj)
	}
	if ack.Outcome != "replay" {
		t.Fatalf("second close outcome = %q, want replay", ack.Outcome)
	}
	if n := entryCount(t, db); n != 1 {
		t.Fatalf("two closes produced %d rows, want exactly 1", n)
	}
	if got := factsOf(t, db, "twice-1"); !reflect.DeepEqual(got, first) {
		t.Fatalf("the re-delivery rewrote the row:\n got  %s\n was  %s", showFacts(got), showFacts(first))
	}
	if first.Executions != 1 {
		t.Fatalf("%d executions, want exactly 1", first.Executions)
	}
}

// ── the cutover checklist: what command_history answers today ─────────────

// nocx-rtg0.19 deletes command_history and makes history.query answer from the
// ledger. That is only safe if the ledger can answer what the interim table's
// read path returns (sqlite.go's recordCols). This test walks those twelve
// columns one by one against a real closed row. Four of them it cannot answer,
// and each says so here rather than being discovered during the cutover.
func TestLedgerAnswers_TheCommandHistoryReadPath(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	const secret = "sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ" //nolint:gosec // a synthetic detector fixture
	intent := "deploy --token=" + secret
	if _, errObj := ledgerCall(t, conn, "ledger.open",
		map[string]any{"envelope": ledgerEnv(sid, "cutover-1", intent, 1)}, 2); errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	if _, errObj := ledgerCall(t, conn, "ledger.close", map[string]any{
		"envelope":   ledgerEnv(sid, "cutover-1", intent, 2),
		"status":     "failure",
		"facts":      map[string]any{"terminationReason": "failed", "exitCode": 127},
		"durationMs": 4200,
		"startedAt":  1_750_000_047_700,
	}, 3); errObj != nil {
		t.Fatalf("ledger.close error: %+v", errObj)
	}
	row := mustEntry(t, db, "cutover-1")

	// 1. id — the row's stable handle. command_history's is an INTEGER rowid;
	//    the ledger's is the client-minted entry id, with ingest_seq as the
	//    total order a page is cut on.
	if row.ID != "cutover-1" || row.IngestSeq <= 0 {
		t.Fatalf("id/ingest_seq = %q/%d", row.ID, row.IngestSeq)
	}
	// 2. command — entries.intent, masked by the same owner history.record uses.
	if row.Intent == "" || strings.Contains(row.Intent, secret) {
		t.Fatalf("intent = %q, want the masked command text", row.Intent)
	}
	// 3. cwd — entries.cwd.
	if row.Cwd != "/repo" {
		t.Fatalf("cwd = %q", row.Cwd)
	}
	// 4. host — NOT a column here. The ledger names WHERE as an environment
	//    identity, and environmentForSession derives that identity from
	//    session.Host() — the same string command_history stores ("" for
	//    local). So the fact is present and the mapping is deterministic…
	if row.EnvironmentID != content.EnvironmentIDFor(content.EnvLocal, "") {
		t.Fatalf("environmentId = %q, want the id derived from the session's host", row.EnvironmentID)
	}
	// …but resolving an environment id BACK to its endpoint has no seam:
	// LedgerRepository exposes no environment read. A host rung can be
	// answered by hashing the host into an id (as above); rendering a row's
	// host cannot. FINDING for nocx-rtg0.19.
	// 5. status — entries.status, a superset of command_history's vocabulary.
	if row.Status != content.EntryFailure {
		t.Fatalf("status = %q", row.Status)
	}
	// 6. exit_code — the shell arm of the kind payload (design §3.3).
	var payload struct {
		Kind     string `json:"kind"`
		V        int    `json:"v"`
		ExitCode *int   `json:"exitCode"`
	}
	if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
		t.Fatalf("payload %q is not JSON: %v", row.Payload, err)
	}
	if payload.Kind != "shell" || payload.V != 1 || payload.ExitCode == nil || *payload.ExitCode != 127 {
		t.Fatalf("payload = %s, want the shell arm carrying exit code 127", row.Payload)
	}
	// 7. started_at — entries.started_at, the wall clock the close carried.
	if row.StartedAt == nil || *row.StartedAt != 1_750_000_047_700 {
		t.Fatalf("started_at = %v", row.StartedAt)
	}
	// 8. ended_at — entries.ended_at, the store's own stamp at the close.
	if row.EndedAt == nil || *row.EndedAt < epochFloor {
		t.Fatalf("ended_at = %v, want the store's wall clock", row.EndedAt)
	}
	// 9. trusted — deliberately absent, and NOT a gap. ADR-0024 deleted
	//    `trusted` as a field crossing to history.record; command_history's
	//    column has been written 0 ever since, because the renderer stopped
	//    sending it. The ledger does not resurrect the laundering.
	// 10-12. masked_count / masked_kinds / redactions — ANSWERABLE since
	//    nocx-rtg0.24. The receipt rides entries.payload (no schemaVersion
	//    bump, so no user pays for a rebuild twice), and the write path that
	//    turns one of those spans into a vault reference is
	//    LedgerRepository.RewriteRedaction, keyed by the entry's UUIDv7
	//    rather than by command_history's rowid.
	receipt, err := content.EntryMaskingOf(row.Payload)
	if err != nil {
		t.Fatalf("EntryMaskingOf(%q): %v", row.Payload, err)
	}
	if receipt.MaskedCount != 1 {
		t.Fatalf("maskedCount = %d, want 1 — the row says what was masked out of it", receipt.MaskedCount)
	}
	if len(receipt.MaskedKinds) != 1 {
		t.Fatalf("maskedKinds = %v, want the one kind the detector named", receipt.MaskedKinds)
	}
	if len(receipt.Redactions) != 1 {
		t.Fatalf("redactions = %+v, want the one span the mask left", receipt.Redactions)
	}
	if len(row.Executions) != 1 {
		t.Fatalf("%d executions, want 1", len(row.Executions))
	}
}

// ── §6.3 rule 2: phase is monotonic ───────────────────────────────────────

// A bind that arrives after the close (the outbox replayed out of order)
// leaves the row closed, answers `dropped`, and says so in the log. Never
// applied, and never silent.
func TestLedgerBindAfterClose_IsDroppedAndLogged(t *testing.T) {
	db := newLedgerStore(t)
	var buf syncBuffer
	logger := log.NewSlogAdapter(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ws, stop := newLedgerWSServer(t, logger, db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	if _, errObj := ledgerCall(t, conn, "ledger.close", map[string]any{
		"envelope": ledgerEnv(sid, "late-1", "ls", 3),
		"status":   "success",
		"facts":    map[string]any{"terminationReason": "completed"},
	}, 2); errObj != nil {
		t.Fatalf("ledger.close error: %+v", errObj)
	}

	ack, errObj := ledgerCall(t, conn, "ledger.bind",
		map[string]any{"envelope": ledgerBindEnv(sid, "late-1", "ls", 2)}, 3)
	if errObj != nil {
		t.Fatalf("ledger.bind error: %+v", errObj)
	}
	if ack.Outcome != "dropped" {
		t.Fatalf("late bind outcome = %q, want dropped", ack.Outcome)
	}
	if ack.Phase != "closed" {
		t.Fatalf("late bind reported phase %q, want the unchanged closed", ack.Phase)
	}
	if row := mustEntry(t, db, "late-1"); row.Phase != content.PhaseClosed {
		t.Fatalf("after the late bind: phase=%q, want closed", row.Phase)
	}
	if logged := buf.String(); !strings.Contains(logged, "late-1") ||
		!strings.Contains(strings.ToLower(logged), "phase") {
		t.Fatalf("the dropped event was not logged:\n%s", logged)
	}
}

// The outbox's worst case, and the one the rejected workaround would have
// broken: the original open, replayed after the row is already closed. It is
// answered `dropped` — never ErrIDConflict, which is invalid-params and would
// have the outbox discarding an entry it was right to retry — and the close's
// facts survive it untouched.
func TestLedgerOpenReplayedAfterClose_IsDroppedNotAConflict(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	open := map[string]any{"envelope": ledgerEnv(sid, "replay-1", "make test", 1)}
	if _, errObj := ledgerCall(t, conn, "ledger.open", open, 2); errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	if _, errObj := ledgerCall(t, conn, "ledger.close", map[string]any{
		"envelope":   ledgerEnv(sid, "replay-1", "make test", 2),
		"status":     "success",
		"facts":      map[string]any{"terminationReason": "completed", "exitCode": 0},
		"durationMs": 1200,
		"startedAt":  1_750_000_047_700,
	}, 3); errObj != nil {
		t.Fatalf("ledger.close error: %+v", errObj)
	}
	closed := factsOf(t, db, "replay-1")

	ack, errObj := ledgerCall(t, conn, "ledger.open", open, 4)
	if errObj != nil {
		t.Fatalf("the replayed open was refused: %+v", errObj)
	}
	if ack.Outcome != "dropped" || ack.Phase != "closed" {
		t.Fatalf("replayed open ack = %+v, want dropped/closed", ack)
	}
	if got := factsOf(t, db, "replay-1"); !reflect.DeepEqual(got, closed) {
		t.Fatalf("the replayed open rewrote the closed row:\n got  %s\n was  %s", showFacts(got), showFacts(closed))
	}
}

// An open that arrives after the bind is the same rule one rung lower.
func TestLedgerOpenAfterBind_IsDropped(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	if _, errObj := ledgerCall(t, conn, "ledger.bind",
		map[string]any{"envelope": ledgerBindEnv(sid, "back-1", "ls", 1)}, 2); errObj != nil {
		t.Fatalf("ledger.bind error: %+v", errObj)
	}
	ack, errObj := ledgerCall(t, conn, "ledger.open",
		map[string]any{"envelope": ledgerEnv(sid, "back-1", "ls", 0)}, 3)
	if errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	if ack.Outcome != "dropped" || ack.Phase != "bound" {
		t.Fatalf("late open ack = %+v, want dropped/bound", ack)
	}
}

// ── §6.3 rule 4: re-delivery in the same phase is a no-op ─────────────────

// Every event sent twice. Asserted by COUNTING rows and executions, never by
// the absence of an error: a second Submit that quietly aliased a second
// intent would raise no error at all.
func TestLedgerEventsSentTwice_ProduceOneRowAndOneExecution(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	open := map[string]any{"envelope": ledgerEnv(sid, "dup-1", "echo hi", 1)}
	bind := map[string]any{"envelope": ledgerBindEnv(sid, "dup-1", "echo hi", 2)}
	closeP := map[string]any{
		"envelope": ledgerEnv(sid, "dup-1", "echo hi", 3),
		"status":   "success",
		"facts":    map[string]any{"terminationReason": "completed"},
	}

	id := 2
	send := func(method string, p map[string]any, wantOutcome string) {
		t.Helper()
		ack, errObj := ledgerCall(t, conn, method, p, id)
		id++
		if errObj != nil {
			t.Fatalf("%s error: %+v", method, errObj)
		}
		if ack.Outcome != wantOutcome {
			t.Fatalf("%s outcome = %q, want %q", method, ack.Outcome, wantOutcome)
		}
	}

	send("ledger.open", open, "applied")
	send("ledger.open", open, "replay")
	send("ledger.bind", bind, "applied")
	send("ledger.bind", bind, "replay")
	send("ledger.close", closeP, "applied")
	send("ledger.close", closeP, "replay")

	if n := entryCount(t, db); n != 1 {
		t.Fatalf("six events produced %d rows, want exactly 1", n)
	}
	row := mustEntry(t, db, "dup-1")
	if len(row.Executions) != 1 {
		t.Fatalf("six events produced %d executions, want exactly 1", len(row.Executions))
	}
	if row.Phase != content.PhaseClosed {
		t.Fatalf("phase = %q, want closed", row.Phase)
	}
}

// ── the interval, with both ends ──────────────────────────────────────────

// "A row exists from the moment its open is accepted until it is closed or
// deleted." Both ends are asserted: the row is present and NOT closed for the
// whole open span, closed by the close, and gone only when the entry is
// deleted — there is no fourth exit (design §4.3).
func TestLedgerEntry_ExistsFromOpenUntilClosedOrDeleted(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)
	ctx := context.Background()

	if _, errObj := ledgerCall(t, conn, "ledger.open",
		map[string]any{"envelope": ledgerEnv(sid, "span-1", "sleep 1", 1)}, 2); errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	// The start of the interval, and every point inside it: present, not closed.
	for _, when := range []string{"after open", "after bind"} {
		if when == "after bind" {
			if _, errObj := ledgerCall(t, conn, "ledger.bind",
				map[string]any{"envelope": ledgerBindEnv(sid, "span-1", "sleep 1", 2)}, 3); errObj != nil {
				t.Fatalf("ledger.bind error: %+v", errObj)
			}
		}
		row := mustEntry(t, db, "span-1")
		if row.Phase == content.PhaseClosed {
			t.Fatalf("%s: the row is already closed", when)
		}
	}
	// The closing event.
	if _, errObj := ledgerCall(t, conn, "ledger.close", map[string]any{
		"envelope": ledgerEnv(sid, "span-1", "sleep 1", 3),
		"status":   "interrupted",
		"facts":    map[string]any{"terminationReason": "user-killed"},
	}, 4); errObj != nil {
		t.Fatalf("ledger.close error: %+v", errObj)
	}
	if row := mustEntry(t, db, "span-1"); row.Phase != content.PhaseClosed {
		t.Fatalf("after close: phase = %q, want closed", row.Phase)
	}
	// The other end: deletion, the only thing that removes the row.
	if err := db.Ledger().DeleteEntry(ctx, "span-1"); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	got, err := db.Ledger().Entry(ctx, "span-1")
	if err != nil {
		t.Fatalf("Entry after delete: %v", err)
	}
	if got != nil {
		t.Fatal("the row survived its deletion")
	}
}

// ── secrets: the ledger is a durable writer of command text ───────────────

// history.record masks at the wire because the durable row must never carry a
// credential. ledger.open writes the same text to the same database, so it
// masks through the SAME owner — a second durable writer that did not would
// be the whole reason that rule exists.
func TestLedgerOpen_MasksTheIntentBeforeItIsDurable(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	const secret = "sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ" //nolint:gosec // a synthetic detector fixture
	if _, errObj := ledgerCall(t, conn, "ledger.open",
		map[string]any{"envelope": ledgerEnv(sid, "sec-1", "export OPENAI_API_KEY="+secret, 1)}, 2); errObj != nil {
		t.Fatalf("ledger.open error: %+v", errObj)
	}
	row := mustEntry(t, db, "sec-1")
	if strings.Contains(row.Intent, secret) {
		t.Fatalf("the raw credential reached the durable intent: %q", row.Intent)
	}
}

// ── params: every reachable field is refused, never repaired ──────────────

func TestLedgerEvents_RefuseUnusableParams(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	cases := []struct {
		name   string
		method string
		params map[string]any
	}{
		{"no envelope", "ledger.open", map[string]any{}},
		{"empty id", "ledger.open", map[string]any{"envelope": ledgerEnv(sid, "", "ls", 1)}},
		{"unknown kind", "ledger.open", map[string]any{"envelope": func() map[string]any {
			e := ledgerEnv(sid, "k-1", "ls", 1)
			e["kind"] = "sorcery"
			return e
		}()}},
		{"unknown sensitivity", "ledger.open", map[string]any{"envelope": func() map[string]any {
			e := ledgerEnv(sid, "k-2", "ls", 1)
			e["sensitivity"] = "secretish"
			return e
		}()}},
		{"negative clientSeq", "ledger.open", map[string]any{"envelope": ledgerEnv(sid, "k-3", "ls", -1)}},
		{"empty cwd", "ledger.open", map[string]any{"envelope": func() map[string]any {
			e := ledgerEnv(sid, "k-4", "ls", 1)
			e["cwd"] = "  "
			return e
		}()}},
		{"oversized intent", "ledger.open", map[string]any{"envelope": ledgerEnv(sid, "k-5", strings.Repeat("x", maxRecordCommandRunes+1), 1)}},
		{"unknown sessionId", "ledger.open", map[string]any{"envelope": ledgerEnv("deadbeefdeadbeefdeadbeefdeadbeef", "k-6", "ls", 1)}},
		{"unknown status", "ledger.close", map[string]any{
			"envelope": ledgerEnv(sid, "k-7", "ls", 1),
			"status":   "cromulent",
			"facts":    map[string]any{"terminationReason": "completed"},
		}},
		{"unknown termination reason", "ledger.close", map[string]any{
			"envelope": ledgerEnv(sid, "k-8", "ls", 1),
			"status":   "success",
			"facts":    map[string]any{"terminationReason": "vanished"},
		}},
		{"missing termination reason", "ledger.close", map[string]any{
			"envelope": ledgerEnv(sid, "k-9", "ls", 1),
			"status":   "success",
		}},
		{"unknown interactivity", "ledger.bind", map[string]any{
			"envelope": ledgerBindEnv(sid, "k-10", "ls", 1),
			"facts":    map[string]any{"interactivity": "telepathy"},
		}},
		{"missing attempt identity", "ledger.bind", map[string]any{
			"envelope": ledgerEnv(sid, "k-14", "ls", 1),
		}},
		{"negative durationMs", "ledger.close", map[string]any{
			"envelope":   ledgerEnv(sid, "k-11", "ls", 1),
			"status":     "success",
			"facts":      map[string]any{"terminationReason": "completed"},
			"durationMs": -5,
		}},
		// The wrong clock, refused at the wire by the same floor
		// history.record uses: a performance.now() reading lands in 1970 and
		// the row is swept microseconds after it is written (nocx-rtg0.16).
		{"startedAt from the wrong clock", "ledger.close", map[string]any{
			"envelope":  ledgerEnv(sid, "k-12", "ls", 1),
			"status":    "success",
			"facts":     map[string]any{"terminationReason": "completed"},
			"startedAt": 1200,
		}},
		// An exit code is a SHELL fact (design §3.2: it is not hoisted to the
		// top level precisely so other kinds do not carry nulls). Refused on
		// another kind rather than accepted and dropped.
		{"exitCode on a non-shell kind", "ledger.close", map[string]any{
			"envelope": func() map[string]any {
				e := ledgerEnv(sid, "k-13", "fix the deploy", 1)
				e["kind"] = "agent"
				return e
			}(),
			"status": "success",
			"facts":  map[string]any{"terminationReason": "completed", "exitCode": 0},
		}},
	}

	id := 2
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errObj := ledgerCall(t, conn, tc.method, tc.params, id)
			id++
			if errObj == nil {
				t.Fatalf("%s accepted %s", tc.method, tc.name)
			}
			if errObj.Code != -32602 {
				t.Fatalf("%s answered code %d, want -32602 (%s)", tc.method, errObj.Code, errObj.Message)
			}
		})
	}
	if n := entryCount(t, db); n != 0 {
		t.Fatalf("refused requests wrote %d rows, want 0", n)
	}
}

// The method must not exist at all when no content store is wired: the
// caller's next move is to stop calling it, not to fix its arguments.
func TestLedgerEvents_WithoutAContentStore_AreMethodNotFound(t *testing.T) {
	ctx := context.Background()
	logger := log.NewSlogAdapter(nil)
	ws := NewWSServer(logger, newRegWithStub(logger))
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = ws.Stop(ctx) }()
	conn := connectWS(t, ws)
	sid := openLocalSession(t, conn)

	_, errObj := ledgerCall(t, conn, "ledger.open",
		map[string]any{"envelope": ledgerEnv(sid, "nostore-1", "ls", 1)}, 2)
	if errObj == nil {
		t.Fatal("ledger.open answered without a content store")
	}
	if errObj.Code != -32601 {
		t.Fatalf("code = %d, want -32601 (%s)", errObj.Code, errObj.Message)
	}
}

// ── the failure paths: one per store call this handler makes ──────────────

// failingLedgerDB is a real store with ONE ledger method replaced by a
// failure. Everything else is the real path, so the assertion after the
// failure is about the state the real store was actually left in.
type failingLedgerDB struct {
	content.ContentDB
	failOn string
	err    error
}

func (f *failingLedgerDB) Ledger() content.LedgerRepository {
	return &failingLedger{LedgerRepository: f.ContentDB.Ledger(), failOn: f.failOn, err: f.err}
}

type failingLedger struct {
	content.LedgerRepository
	failOn string
	err    error
}

func (l *failingLedger) CreateSession(ctx context.Context, sess content.Session) error {
	if l.failOn == "CreateSession" {
		return l.err
	}
	return l.LedgerRepository.CreateSession(ctx, sess)
}

func (l *failingLedger) Entry(ctx context.Context, id string) (*content.LedgerEntry, error) {
	if l.failOn == "Entry" {
		return nil, l.err
	}
	return l.LedgerRepository.Entry(ctx, id)
}

func (l *failingLedger) EnsureEnvironment(ctx context.Context, env content.Environment) error {
	if l.failOn == "EnsureEnvironment" {
		return l.err
	}
	return l.LedgerRepository.EnsureEnvironment(ctx, env)
}

func (l *failingLedger) RecordObservation(ctx context.Context, obs content.Observation) (int64, error) {
	if l.failOn == "RecordObservation" {
		return 0, l.err
	}
	return l.LedgerRepository.RecordObservation(ctx, obs)
}

func (l *failingLedger) Submit(ctx context.Context, in content.SubmitEntry) (content.SubmitResult, error) {
	if l.failOn == "Submit" {
		return content.SubmitResult{}, l.err
	}
	return l.LedgerRepository.Submit(ctx, in)
}

func (l *failingLedger) StartExecution(ctx context.Context, in content.StartExecution) (int64, error) {
	if l.failOn == "StartExecution" {
		return 0, l.err
	}
	return l.LedgerRepository.StartExecution(ctx, in)
}

// RecordCompleted is history.record's own store call — the second durable
// writer of a finished command (ws_ledger_notify.go). It is here rather than
// in a double of its own so "the store refuses this write" is one fixture for
// both writers.
func (l *failingLedger) RecordCompleted(ctx context.Context, in content.CompletedCommand) (string, error) {
	if l.failOn == "RecordCompleted" {
		return "", l.err
	}
	return l.LedgerRepository.RecordCompleted(ctx, in)
}

func (l *failingLedger) FinishExecution(ctx context.Context, execID int64, end content.FinishExecution) error {
	if l.failOn == "FinishExecution" {
		return l.err
	}
	return l.LedgerRepository.FinishExecution(ctx, execID, end)
}

// Every external call this handler makes has a test where that call fails.
// The event is refused — never half-acknowledged — and the assertion names
// what is true on disk afterwards, because a partial write is what the next
// start has to recover from.
func TestLedgerEvents_EveryStoreCallFails(t *testing.T) {
	boom := errors.New("store is on fire")

	cases := []struct {
		failOn string
		// afterOpen is what the entry looks like once ledger.open has been
		// refused with this call failing.
		wantRowAfterOpen bool
	}{
		{failOn: "Entry", wantRowAfterOpen: false},
		{failOn: "EnsureEnvironment", wantRowAfterOpen: false},
		{failOn: "RecordObservation", wantRowAfterOpen: false},
		{failOn: "Submit", wantRowAfterOpen: false},
	}

	for _, tc := range cases {
		t.Run("open/"+tc.failOn, func(t *testing.T) {
			real := newLedgerStore(t)
			db := &failingLedgerDB{ContentDB: real, failOn: tc.failOn, err: boom}
			ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
			defer stop()
			conn := connectWS(t, ws)
			sid := openLocalSession(t, conn)

			_, errObj := ledgerCall(t, conn, "ledger.open",
				map[string]any{"envelope": ledgerEnv(sid, "fail-1", "ls", 1)}, 2)
			if errObj == nil {
				t.Fatalf("ledger.open succeeded with %s failing", tc.failOn)
			}
			if errObj.Code != -32603 {
				t.Fatalf("code = %d, want -32603 (%s)", errObj.Code, errObj.Message)
			}
			row, err := real.Ledger().Entry(context.Background(), "fail-1")
			if err != nil {
				t.Fatalf("Entry: %v", err)
			}
			if (row != nil) != tc.wantRowAfterOpen {
				t.Fatalf("row present = %v, want %v", row != nil, tc.wantRowAfterOpen)
			}
		})
	}

	// StartExecution fails on the bind. The entry keeps its row and stays
	// OPEN: the intent is not lost, and the startup sweep closes it unknown
	// at the next start (design §4.3) — that is the recovery, and it needs
	// the row to still be there.
	t.Run("bind/StartExecution", func(t *testing.T) {
		real := newLedgerStore(t)
		db := &failingLedgerDB{ContentDB: real, failOn: "StartExecution", err: boom}
		ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
		defer stop()
		conn := connectWS(t, ws)
		sid := openLocalSession(t, conn)

		if _, errObj := ledgerCall(t, conn, "ledger.open",
			map[string]any{"envelope": ledgerEnv(sid, "fail-2", "ls", 1)}, 2); errObj != nil {
			t.Fatalf("ledger.open error: %+v", errObj)
		}
		_, errObj := ledgerCall(t, conn, "ledger.bind",
			map[string]any{"envelope": ledgerBindEnv(sid, "fail-2", "ls", 2)}, 3)
		if errObj == nil {
			t.Fatal("ledger.bind succeeded with StartExecution failing")
		}
		row := mustEntry(t, real, "fail-2")
		if row.Phase != content.PhaseOpen {
			t.Fatalf("phase = %q, want the unchanged open", row.Phase)
		}
		if len(row.Executions) != 0 {
			t.Fatalf("%d executions, want 0 — StartExecution is one transaction", len(row.Executions))
		}
	})

	// FinishExecution fails on the close. The run stays live and the entry
	// stays bound; nothing is reported closed that is not.
	t.Run("close/FinishExecution", func(t *testing.T) {
		real := newLedgerStore(t)
		db := &failingLedgerDB{ContentDB: real, failOn: "FinishExecution", err: boom}
		ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
		defer stop()
		conn := connectWS(t, ws)
		sid := openLocalSession(t, conn)

		for _, m := range []struct {
			method string
			params map[string]any
		}{
			{"ledger.open", map[string]any{"envelope": ledgerEnv(sid, "fail-3", "ls", 1)}},
			{"ledger.bind", map[string]any{"envelope": ledgerBindEnv(sid, "fail-3", "ls", 2)}},
		} {
			if _, errObj := ledgerCall(t, conn, m.method, m.params, 2); errObj != nil {
				t.Fatalf("%s error: %+v", m.method, errObj)
			}
		}
		_, errObj := ledgerCall(t, conn, "ledger.close", map[string]any{
			"envelope":   ledgerEnv(sid, "fail-3", "ls", 3),
			"status":     "success",
			"facts":      map[string]any{"terminationReason": "completed", "exitCode": 0},
			"durationMs": 900,
			"startedAt":  1_750_000_047_700,
		}, 4)
		if errObj == nil {
			t.Fatal("ledger.close succeeded with FinishExecution failing")
		}
		row := mustEntry(t, real, "fail-3")
		if row.Phase != content.PhaseBound {
			t.Fatalf("phase = %q, want the unchanged bound", row.Phase)
		}
		if row.Status == content.EntrySuccess {
			t.Fatal("the entry took the close's status while the close failed")
		}
		// And none of the close's facts leaked past the failure: a closed
		// entry with a half-written payload is the state nothing could
		// recover from, because it looks exactly like a finished one. The
		// payload the open wrote — the redaction receipt — is untouched and
		// carries no shell arm, which is what "the close wrote nothing"
		// looks like now that both writers share the column.
		openPayload := `{"masking":{"maskedCount":0,"maskedKinds":[],"redactions":[]}}`
		if row.Payload != openPayload || row.DurationMs != nil || row.StartedAt != nil || row.EndedAt != nil {
			t.Fatalf("a failed close left facts behind: payload=%s duration=%v started=%v ended=%v",
				row.Payload, row.DurationMs, row.StartedAt, row.EndedAt)
		}
	})
}

// ── the host a row ran on, writer and reader together (nocx-rtg0.25) ──────

// openSSHSession opens a session through the profile resolver and returns
// its id. The resolver names the host; the stub factory answers the dial.
func openSSHSession(t *testing.T, conn *websocket.Conn, id int) string {
	t.Helper()
	resp := jsonrpcCallWithID(t, conn, "open", map[string]any{
		"cols": 80, "rows": 24, "xpixel": 0, "ypixel": 0,
		"kind": "ssh", "profileId": "ssh:test:1",
	}, id)
	var r struct {
		Result struct {
			SessionID string `json:"sessionId"`
		} `json:"result"`
		Error *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		t.Fatalf("open ssh unmarshal: %v\nraw: %s", err, resp)
	}
	if r.Error != nil || r.Result.SessionID == "" {
		t.Fatalf("open ssh: %+v\nraw: %s", r.Error, resp)
	}
	return r.Result.SessionID
}

// environmentForSession decides where a session is and writes it; the
// ledger's Environment.Host() reads it back. The two are asserted together,
// off the real socket into the real store, because each is green alone while
// they disagree — a read that answers "" for everything matches the local
// row exactly, and only the ssh row can report it. This is the field
// history.query's contract calls host, which nocx-rtg0.19 must keep
// answering once command_history is gone.
func TestLedgerEntry_SaysWhichHostItRanOn(t *testing.T) {
	const sshHost = "build.example.com"
	logger := log.NewSlogAdapter(nil)
	db := newLedgerStore(t)
	reg := newRegWithStub(logger)
	reg.WithSSHFactory(&stubSSHFactory{
		connectFn: func(_ context.Context, _ string, _ ...ssh.ConnectOption) (ssh.Channel, error) {
			return ssh.NewStubChannel(logger), nil
		},
	})
	ws := NewWSServer(logger, reg,
		WithContentDB(db),
		WithProfileResolver(&fakeResolver{
			resolveFn: func(string) (string, *ssh.ConnectConfig, error) {
				return sshHost, &ssh.ConnectConfig{User: "alice", Port: 22}, nil
			},
		}),
	)
	ctx := context.Background()
	if err := ws.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ws.Stop(ctx) })
	conn := connectWS(t, ws)

	localSID := openLocalSession(t, conn)
	sshSID := openSSHSession(t, conn, 2)

	for i, c := range []struct {
		entryID string
		sid     string
		want    string
	}{
		{"local-entry", localSID, ""},
		{"ssh-entry", sshSID, sshHost},
	} {
		_, errObj := ledgerCall(t, conn, "ledger.open",
			map[string]any{"envelope": ledgerEnv(c.sid, c.entryID, "make test", 1)}, 10+i)
		if errObj != nil {
			t.Fatalf("ledger.open %s: %+v", c.entryID, errObj)
		}
		row := mustEntry(t, db, c.entryID)
		if row.Environment == nil {
			t.Fatalf("%s resolved no environment — the row cannot say which host it ran on", c.entryID)
		}
		if got := row.Environment.Host(); got != c.want {
			t.Fatalf("%s host = %q, want %q", c.entryID, got, c.want)
		}
		if row.Environment.ID != row.EnvironmentID {
			t.Fatalf("%s resolved environment %q for environment_id %q",
				c.entryID, row.Environment.ID, row.EnvironmentID)
		}
	}

	// And the timeline read answers for both rows in its one query.
	rows, err := db.Ledger().ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	hosts := map[string]string{}
	for _, row := range rows {
		if row.Environment == nil {
			t.Fatalf("ListEntries row %q resolved no environment", row.ID)
		}
		hosts[row.ID] = row.Environment.Host()
	}
	if hosts["local-entry"] != "" || hosts["ssh-entry"] != sshHost {
		t.Fatalf("ListEntries hosts = %v, want local %q and ssh %q", hosts, "", sshHost)
	}
}
