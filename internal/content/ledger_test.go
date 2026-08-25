package content_test

// Acceptance tests for schema v1 of the one authoritative ledger
// (nocx-rtg0.2), through the public seam: content.Open → ContentDB.Ledger()
// → LedgerRepository. ADR-0019, ADR-0020, design §5.2.
//
// These tests ARE the only callers of the v1 write path until nocx-rtg0.3
// cuts the wire over to ledger.* — stated loudly in the task report, not
// hidden here.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"

	"github.com/shady2k/nocx/internal/content"
)

// ── fixtures ─────────────────────────────────────────────────────────────

// newLedger opens a fresh store and returns the public ledger seam.
func newLedger(t *testing.T) (content.ContentDB, content.LedgerRepository) {
	t.Helper()
	db, _ := newTestStore(t)
	return db, db.Ledger()
}

// newLedgerAt also hands back the file path, for reopen tests.
func newLedgerAt(t *testing.T) (content.ContentDB, content.LedgerRepository, string) {
	t.Helper()
	db, dir := newTestStore(t)
	return db, db.Ledger(), filepath.Join(dir, "content.db")
}

// rawLedger runs statements against the encrypted file the way Open does,
// bypassing the seam — the only way to write a string into duration_ms or an
// unknown enum value, which is exactly what the STRICT and CHECK tests must
// do (a Go-typed caller cannot express them).
func rawLedger(t *testing.T, path, keyHex string, stmts ...string) error {
	t.Helper()
	db, err := driver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + keyHex + "'")
	})
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	for _, s := range stmts {
		if _, err := db.ExecContext(context.Background(), s); err != nil {
			return err
		}
	}
	return nil
}

// envReady records an environment plus its first observation, the state every
// entry needs before an execution can start.
func envReady(t *testing.T, led content.LedgerRepository, id string) {
	t.Helper()
	ctx := context.Background()
	if err := led.EnsureEnvironment(ctx, content.Environment{
		ID: id, Kind: content.EnvLocal,
	}); err != nil {
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: id, Criticality: content.CriticalityRoutine, Payload: `{"branch":"main"}`,
	}); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}
}

// submitIntents records one entry per intent under a fresh client-minted id.
func submitIntents(t *testing.T, led content.LedgerRepository, intents ...string) []string {
	t.Helper()
	ctx := context.Background()
	envReady(t, led, "local")
	ids := make([]string, 0, len(intents))
	for _, intent := range intents {
		id := fmt.Sprintf("00000000-0000-7000-8000-%012d", len(ids))
		res, err := led.Submit(ctx, content.SubmitEntry{
			ID: id, Client: "test-client", EnvironmentID: "local",
			Cwd: "/repo", Kind: content.EntryShell, Intent: intent,
		})
		if err != nil {
			t.Fatalf("Submit %q: %v", intent, err)
		}
		if res.Replayed {
			t.Fatalf("Submit %q unexpectedly replayed", intent)
		}
		ids = append(ids, id)
	}
	return ids
}

func strPtr(s string) *string { return &s }
func i64Ptr(n int64) *int64   { return &n }

// ── acceptance: two entries in the same millisecond ──────────────────────

// The whole reason ingest_seq exists (design §3.2): wall-clock milliseconds
// are not a key because two windows submit in the same millisecond. The
// store stamps submitted_at for display; ordering comes from the counter.
func TestSameMillisecondEntriesGetDistinctOrderedIngestSeq(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	res1, err := led.Submit(ctx, content.SubmitEntry{
		ID: "00000000-0000-7000-8000-000000000001", Client: "win-a",
		EnvironmentID: "local", Cwd: "/repo", Kind: content.EntryShell, Intent: "first",
	})
	if err != nil {
		t.Fatalf("Submit first: %v", err)
	}
	res2, err := led.Submit(ctx, content.SubmitEntry{
		ID: "00000000-0000-7000-8000-000000000002", Client: "win-b",
		EnvironmentID: "local", Cwd: "/repo", Kind: content.EntryShell, Intent: "second",
	})
	if err != nil {
		t.Fatalf("Submit second: %v", err)
	}

	if res1.IngestSeq == res2.IngestSeq {
		t.Fatalf("two submissions got the same ingest_seq %d", res1.IngestSeq)
	}
	if res1.IngestSeq >= res2.IngestSeq {
		t.Fatalf("ingest_seq is not submission order: first=%d second=%d", res1.IngestSeq, res2.IngestSeq)
	}
	if res1.SubmittedAt == 0 || res2.SubmittedAt == 0 {
		t.Fatal("submitted_at was not stamped by the store")
	}

	// A query returns them in submission order (newest first, like the
	// history contract), ordered by ingest_seq — never by wall clock.
	page, err := led.ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("ListEntries = %d rows, want 2", len(page))
	}

	if page[0].Intent != "second" || page[1].Intent != "first" {
		t.Fatalf("ListEntries order = [%q, %q], want [second, first]", page[0].Intent, page[1].Intent)
	}
	if page[0].IngestSeq <= page[1].IngestSeq {
		t.Fatalf("ListEntries seq order = [%d, %d], want newest first", page[0].IngestSeq, page[1].IngestSeq)
	}
}

// ── acceptance: STRICT rejects a string in duration_ms ───────────────────

// STRICT tables reject a string in an INTEGER column at the SQLite level —
// the reason to have a schema is that it says no. The seam cannot express
// the bad value (Go types it), so the INSERT is raw, like the schema-reset
// tests' fabrication. The environment is seeded so the FK cannot mask the
// datatype rejection.
func TestStrictRejectsStringInDurationMs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	db, err := content.Open(context.Background(), content.Config{
		Path: path, Key: testKey(), Budget: testBudget,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	keyHex := hex.EncodeToString(testKey())

	err = rawLedger(t, path, keyHex,
		`INSERT INTO environments (id, kind, first_seen) VALUES ('env', 'local', 1)`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			phase, status, submitted_at, duration_ms)
		VALUES ('bad', 1, 'c', 'd', 'env', '/', 'shell', 'user', 'x', 'open', 'pending', 1, 'not-a-number')`,
	)
	if err == nil {
		t.Fatal("a string in duration_ms was accepted — STRICT is not in force")
	}
}

// The paired success: an ordinary integer duration writes and reads back.
func TestStrictAcceptsIntegerDurationMs(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	dur := int64(1234)
	res, err := led.Submit(ctx, content.SubmitEntry{
		ID: "00000000-0000-7000-8000-000000000010", Client: "c",
		EnvironmentID: "local", Cwd: "/repo", Kind: content.EntryShell, Intent: "timed",
		DurationMs: &dur,
	})
	if err != nil {
		t.Fatalf("Submit with a duration: %v", err)
	}
	e, err := led.Entry(ctx, res.ID)
	if err != nil || e == nil {
		t.Fatalf("Entry: %v (nil=%v)", err, e == nil)
	}
	if e.DurationMs == nil || *e.DurationMs != 1234 {
		t.Fatalf("DurationMs = %v, want 1234", e.DurationMs)
	}
}

// ── acceptance: deleting an entry cascades to edges and artifacts ─────────

func TestDeleteEntryCascadesToEdgesAndArtifacts(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	ids := submitIntents(t, led, "source", "target")
	src, dst := ids[0], ids[1]

	if err := led.AddEdge(ctx, content.Edge{From: src, To: dst, Rel: content.RelRerunOf}); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: src})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	artID, err := led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: src, ExecutionID: &execID, ID: "aaaaaaaa-0000-7000-8000-000000000001",
		MediaType: content.MediaText, CaptureMethod: content.CaptureRawOutput,
	})
	if err != nil {
		t.Fatalf("AppendArtifact: %v", err)
	}
	if err = led.AppendChunk(ctx, artID, 1, []byte("hello ")); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	if err = led.AppendChunk(ctx, artID, 2, []byte("world")); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}

	if err = led.DeleteEntry(ctx, src); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}

	// The entry is gone, its edge is gone from BOTH directions, and the
	// artifact — with its chunks — is gone with the execution cascade.
	e, err := led.Entry(ctx, src)
	if err != nil || e != nil {
		t.Fatalf("Entry after delete = %v (err %v), want nil", e, err)
	}
	edges, err := led.Edges(ctx, dst)
	if err != nil {
		t.Fatalf("Edges(dst): %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("edges referencing the deleted entry survived: %+v", edges)
	}
	if a, err := led.Artifact(ctx, artID); err != nil || a != nil {
		t.Fatalf("Artifact after delete = %v (err %v), want nil (chunks cascade with it)", a, err)
	}
	// The untouched endpoint is still there.
	if e, err := led.Entry(ctx, dst); err != nil || e == nil {
		t.Fatalf("the other entry was caught in the cascade: %v (nil=%v)", err, e == nil)
	}
}

// Deleting a missing entry is idempotent — the cascade has nothing to do.
func TestDeleteEntryIsIdempotentForMissingEntry(t *testing.T) {
	_, led := newLedger(t)
	if err := led.DeleteEntry(context.Background(), "no-such-entry"); err != nil {
		t.Fatalf("DeleteEntry(missing) = %v, want nil", err)
	}
}

// ── acceptance: artifacts attach to an execution, not to the entry ───────

// A rerun, a retry, a takeover and an infrastructure failure are executions
// of the SAME entry (ADR-0020 §4) — so one entry with three executions must
// keep three separate artifact sets, never one merged one.
func TestArtifactsAttachToExecutionNotEntry(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	entryID := "00000000-0000-7000-8000-000000000020"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "retried command",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	var execIDs []int64
	for i := range 3 {
		id, err := led.StartExecution(ctx, content.StartExecution{EntryID: entryID, Attempt: i + 1})
		if err != nil {
			t.Fatalf("StartExecution %d: %v", i, err)
		}
		execIDs = append(execIDs, id)
	}
	for i, eid := range execIDs {
		if _, err := led.AppendArtifact(ctx, content.AppendArtifact{
			EntryID:     entryID,
			ExecutionID: &eid,
			ID:          fmt.Sprintf("bbbbbbbb-0000-7000-8000-%012d", i),
			MediaType:   content.MediaText,
		}); err != nil {
			t.Fatalf("AppendArtifact %d: %v", i, err)
		}
	}

	e, err := led.Entry(ctx, entryID)
	if err != nil || e == nil {
		t.Fatalf("Entry: %v (nil=%v)", err, e == nil)
	}
	if len(e.Executions) != 3 {
		t.Fatalf("executions = %d, want 3", len(e.Executions))
	}
	for i, ex := range e.Executions {
		if ex.ID != execIDs[i] {
			t.Fatalf("execution %d: id = %d, want %d", i, ex.ID, execIDs[i])
		}
		if len(ex.Artifacts) != 1 {
			t.Fatalf("execution %d: artifacts = %d, want exactly its own 1", i, len(ex.Artifacts))
		}
		want := fmt.Sprintf("bbbbbbbb-0000-7000-8000-%012d", i)
		if ex.Artifacts[0].ID != want {
			t.Fatalf("execution %d owns artifact %q, want %q — the sets merged", i, ex.Artifacts[0].ID, want)
		}
	}
}

// ── acceptance: the entry pins the observation current at execution time ──

// The amendment's exact failure: mutable observation facts (branch,
// privilege, criticality) must be captured AT EXECUTION TIME, or old rows
// get reinterpreted with today's facts. A later observation must not move
// what the entry reads back.
func TestEntryPinsObservationAtExecutionTime(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()

	if err := led.EnsureEnvironment(ctx, content.Environment{
		ID: "prod", Kind: content.EnvSSH, Endpoint: strPtr("deploy@prod.example.com:22"),
	}); err != nil {
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	// v1: the environment as it was when the run started.
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: "prod", Criticality: content.CriticalityCritical,
		Payload: `{"branch":"release-1","privilege":"root"}`,
	}); err != nil {
		t.Fatalf("RecordObservation v1: %v", err)
	}
	entryID := "00000000-0000-7000-8000-000000000030"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "c", EnvironmentID: "prod", Cwd: "/srv",
		Kind: content.EntryShell, Intent: "deploy",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := led.StartExecution(ctx, content.StartExecution{EntryID: entryID}); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}

	// v2: the branch moved after the run started. This must not leak back.
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: "prod", Criticality: content.CriticalityRoutine,
		Payload: `{"branch":"feature-x","privilege":"user"}`,
	}); err != nil {
		t.Fatalf("RecordObservation v2: %v", err)
	}

	e, err := led.Entry(ctx, entryID)
	if err != nil || e == nil {
		t.Fatalf("Entry: %v (nil=%v)", err, e == nil)
	}
	if len(e.Executions) != 1 {
		t.Fatalf("executions = %d, want 1", len(e.Executions))
	}
	obs := e.Executions[0].Observation
	if obs.Version != 1 {
		t.Fatalf("pinned observation version = %d, want 1 — the entry was reinterpreted with today's facts", obs.Version)
	}
	if obs.Criticality != content.CriticalityCritical {
		t.Fatalf("pinned criticality = %q, want critical", obs.Criticality)
	}
	if obs.Payload != `{"branch":"release-1","privilege":"root"}` {
		t.Fatalf("pinned payload = %q, want the v1 facts", obs.Payload)
	}
}

// ── idempotency keys: bound to client and content digest ─────────────────

func TestSubmitReplayIsIdempotentAndConflictRefused(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	id := "00000000-0000-7000-8000-000000000040"
	in := content.SubmitEntry{
		ID: id, Client: "client-a", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "kubectl get pods",
	}
	first, err := led.Submit(ctx, in)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// The exact replay (same id, same client, same content) is a no-op that
	// returns the original row — a lost response retried must not duplicate.
	replay, err := led.Submit(ctx, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.Replayed {
		t.Fatal("replay was not marked Replayed")
	}
	if replay.IngestSeq != first.IngestSeq {
		t.Fatalf("replay seq = %d, want the original %d", replay.IngestSeq, first.IngestSeq)
	}
	page, err := led.ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("after a replay there are %d rows, want 1 — a replay aliased a new intent", len(page))
	}

	// The same id with DIFFERENT content would alias two intents: refused.
	changed := in
	changed.Intent = "kubectl delete pods"
	if _, err := led.Submit(ctx, changed); !errors.Is(err, content.ErrIDConflict) {
		t.Fatalf("same id, different intent = %v, want ErrIDConflict", err)
	}
	// The same id from a DIFFERENT client is also a different submission.
	otherClient := in
	otherClient.Client = "client-b"
	if _, err := led.Submit(ctx, otherClient); !errors.Is(err, content.ErrIDConflict) {
		t.Fatalf("same id, different client = %v, want ErrIDConflict", err)
	}
}

// The client identity is required: without it the binding is meaningless.
func TestSubmitRequiresClient(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	_, err := led.Submit(ctx, content.SubmitEntry{
		ID: "00000000-0000-7000-8000-000000000041", Client: "",
		EnvironmentID: "local", Cwd: "/repo", Kind: content.EntryShell, Intent: "x",
	})
	if err == nil {
		t.Fatal("Submit without a client succeeded — the idempotency binding is unbound")
	}
	// The paired success, so the pair lives together.
	in := content.SubmitEntry{
		ID: "00000000-0000-7000-8000-000000000042", Client: "c",
		EnvironmentID: "local", Cwd: "/repo", Kind: content.EntryShell, Intent: "x",
	}
	if _, err := led.Submit(ctx, in); err != nil {
		t.Fatalf("Submit with a client failed: %v", err)
	}
}

// ── crash-safe ingest_seq: the counter is one transaction with the row ────

// The partial-failure enumeration for Submit (a two-store procedure: counter
// plus entry): step 2 fails — what is true on disk is NOTHING, the counter
// included. The next submission starts at 1, and a deleted entry's sequence
// is never reused.
func TestFailedSubmitRollsBackTheSequenceCounter(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	// The entry INSERT fails on the environment FK — AFTER the counter
	// increment. One transaction must roll both back.
	_, err := led.Submit(ctx, content.SubmitEntry{
		ID: "00000000-0000-7000-8000-000000000050", Client: "c",
		EnvironmentID: "no-such-environment", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "doomed",
	})
	if err == nil {
		t.Fatal("Submit with a missing environment succeeded")
	}

	res, err := led.Submit(ctx, content.SubmitEntry{
		ID: "00000000-0000-7000-8000-000000000051", Client: "c",
		EnvironmentID: "local", Cwd: "/repo", Kind: content.EntryShell, Intent: "first",
	})
	if err != nil {
		t.Fatalf("Submit after the failed one: %v", err)
	}
	if res.IngestSeq != 1 {
		t.Fatalf("ingest_seq after a rolled-back attempt = %d, want 1 — the counter and the row split", res.IngestSeq)
	}

	// Deletion never reuses a sequence: monotonic means monotonic.
	if err = led.DeleteEntry(ctx, res.ID); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	res2, err := led.Submit(ctx, content.SubmitEntry{
		ID: "00000000-0000-7000-8000-000000000052", Client: "c",
		EnvironmentID: "local", Cwd: "/repo", Kind: content.EntryShell, Intent: "second",
	})
	if err != nil {
		t.Fatalf("Submit after delete: %v", err)
	}
	if res2.IngestSeq != 2 {
		t.Fatalf("ingest_seq after a delete = %d, want 2 (no reuse)", res2.IngestSeq)
	}
}

// The counter lives in the file, not in memory: a reopen continues the
// sequence — the property a crash-safe assignment exists to provide.
func TestIngestSeqSurvivesReopen(t *testing.T) {
	db, led, path := newLedgerAt(t)
	ctx := context.Background()
	envReady(t, led, "local")

	submitIntents(t, led, "one")
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := content.Open(context.Background(), content.Config{
		Path: path, Key: testKey(), Budget: testBudget,
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()
	led2 := again.Ledger()
	envReady(t, led2, "local")
	res, err := led2.Submit(ctx, content.SubmitEntry{
		ID: "00000000-0000-7000-8000-000000000060", Client: "c",
		EnvironmentID: "local", Cwd: "/repo", Kind: content.EntryShell, Intent: "after-reopen",
	})
	if err != nil {
		t.Fatalf("Submit after reopen: %v", err)
	}
	if res.IngestSeq != 2 {
		t.Fatalf("ingest_seq after reopen = %d, want 2 — the counter reset", res.IngestSeq)
	}
}

// ── startup sweep (spec §4.3): nothing open survives a restart ───────────

func TestReopenClosesOpenAndBoundEntriesAsUnknown(t *testing.T) {
	db, led, path := newLedgerAt(t)
	ctx := context.Background()
	envReady(t, led, "local")

	// One entry that never ran (open), one that started but never finished
	// (bound), one fully closed — the sweep's three cases.
	openID := "00000000-0000-7000-8000-000000000070"
	boundID := "00000000-0000-7000-8000-000000000071"
	closedID := "00000000-0000-7000-8000-000000000072"
	submit := func(id string) {
		t.Helper()
		if _, err := led.Submit(ctx, content.SubmitEntry{
			ID: id, Client: "c", EnvironmentID: "local", Cwd: "/repo",
			Kind: content.EntryShell, Intent: "cmd " + id,
		}); err != nil {
			t.Fatalf("Submit %s: %v", id, err)
		}
	}
	submit(openID)
	submit(boundID)
	submit(closedID)

	// boundID starts and never finishes; closedID starts and finishes cleanly.
	boundExec, err := led.StartExecution(ctx, content.StartExecution{EntryID: boundID})
	if err != nil {
		t.Fatalf("StartExecution boundID: %v", err)
	}
	closedExec, err := led.StartExecution(ctx, content.StartExecution{EntryID: closedID})
	if err != nil {
		t.Fatalf("StartExecution closedID: %v", err)
	}
	if err = led.FinishExecution(ctx, closedExec, content.FinishExecution{
		EndedAt: 1_750_000_000_000, TerminationReason: content.TermCompleted,
		Status: content.EntrySuccess,
	}); err != nil {
		t.Fatalf("FinishExecution closedID: %v", err)
	}
	_ = boundExec

	if err = db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := content.Open(context.Background(), content.Config{
		Path: path, Key: testKey(), Budget: testBudget,
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()
	led2 := again.Ledger()

	// The sweep closed both stragglers as unknown — through the entries_open
	// partial index the spec gave the sweep.
	for _, id := range []string{openID, boundID} {
		e, rowErr := led2.Entry(ctx, id)
		if rowErr != nil || e == nil {
			t.Fatalf("Entry(%s) after reopen: %v (nil=%v)", id, rowErr, e == nil)
		}
		if e.Phase != content.PhaseClosed || e.Status != content.EntryUnknown {
			t.Fatalf("entry %s after reopen = %s/%s, want closed/unknown — the startup sweep did not run", id, e.Phase, e.Status)
		}
	}
	// A finished entry is untouched by the sweep.
	e2, err := led2.Entry(ctx, closedID)
	if err != nil || e2 == nil {
		t.Fatalf("Entry(closedID) after reopen: %v (nil=%v)", err, e2 == nil)
	}
	if e2.Phase != content.PhaseClosed || e2.Status != content.EntrySuccess {
		t.Fatalf("the closed entry was rewritten by the sweep: %s/%s", e2.Phase, e2.Status)
	}
}

// ── sessions: restore key, never recall filter (ADR-0019 §5) ─────────────

func TestDeleteSessionSetsEntrySessionNull(t *testing.T) {
	db, led := newLedger(t)
	ctx := context.Background()

	// The workspace is LayoutRepository's (nocx-isoph.1); the ledger only
	// references it.
	if _, err := db.Layout().CreateWorkspace(ctx, content.Workspace{ID: "ws-1", Name: "work"},
		aTab("tab-1", "ws-1"), aPane("pane-1", "tab-1", "/srv")); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := led.CreateSession(ctx, content.Session{ID: "sess-1", WorkspaceID: "ws-1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	envReady(t, led, "local")
	sess := "sess-1"
	entryID := "00000000-0000-7000-8000-000000000080"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "in a tab", SessionID: &sess,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	e, err := led.Entry(ctx, entryID)
	if err != nil || e == nil || e.SessionID == nil || *e.SessionID != "sess-1" {
		t.Fatalf("Entry: %v (nil=%v, session=%v)", err, e == nil, entrySession(e))
	}

	if err = led.DeleteSession(ctx, "sess-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	// The entry outlives its session: row intact, reference null.
	e, err = led.Entry(ctx, entryID)
	if err != nil || e == nil {
		t.Fatalf("Entry after session delete: %v (nil=%v) — the entry did not outlive its session", err, e == nil)
	}
	if e.SessionID != nil {
		t.Fatalf("session_id = %q after delete, want NULL (ON DELETE SET NULL)", *e.SessionID)
	}
}

func entrySession(e *content.LedgerEntry) any {
	if e == nil || e.SessionID == nil {
		return nil
	}
	return *e.SessionID
}

// A session needs its workspace; the FK is the check.
func TestCreateSessionRequiresWorkspace(t *testing.T) {
	db, led := newLedger(t)
	ctx := context.Background()
	err := led.CreateSession(ctx, content.Session{ID: "sess-orphan", WorkspaceID: "no-workspace"})
	if err == nil {
		t.Fatal("session under a missing workspace succeeded")
	}
	if _, err := db.Layout().CreateWorkspace(ctx, content.Workspace{ID: "ws-2", Name: "work"},
		aTab("tab-2", "ws-2"), aPane("pane-2", "tab-2", "/srv")); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := led.CreateSession(ctx, content.Session{ID: "sess-2", WorkspaceID: "ws-2"}); err != nil {
		t.Fatalf("session under a real workspace failed: %v", err)
	}
}

// ── executions: pin or fail, and the five outcomes are recorded ──────────

// An execution cannot start when the environment has no observation: there
// is nothing to pin, and an unpinned run would be reinterpreted later with
// today's facts.
func TestStartExecutionRequiresAnObservationToPin(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	if err := led.EnsureEnvironment(ctx, content.Environment{ID: "local", Kind: content.EnvLocal}); err != nil {
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	entryID := "00000000-0000-7000-8000-000000000090"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "no obs yet",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := led.StartExecution(ctx, content.StartExecution{EntryID: entryID}); err == nil {
		t.Fatal("StartExecution without an observation succeeded")
	}
	// The paired success: record the observation, then the run starts and
	// pins it.
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: "local", Criticality: content.CriticalityRoutine,
	}); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}
	if _, err := led.StartExecution(ctx, content.StartExecution{EntryID: entryID}); err != nil {
		t.Fatalf("StartExecution with an observation failed: %v", err)
	}
}

func TestStartExecutionOnMissingEntryFails(t *testing.T) {
	_, led := newLedger(t)
	if _, err := led.StartExecution(context.Background(),
		content.StartExecution{EntryID: "no-such-entry"}); err == nil {
		t.Fatal("StartExecution on a missing entry succeeded")
	}
}

// The five outcomes one status plus exit code cannot separate (ADR-0020 §4):
// the termination reason is recorded on the run, the status on the entry.
func TestFinishExecutionRecordsTerminationReason(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	entryID := "00000000-0000-7000-8000-000000000091"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "hang forever",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	execID, err := led.StartExecution(ctx, content.StartExecution{
		EntryID: entryID, LeaseDeadline: i64Ptr(1_750_000_100_000),
		Interactivity: content.InteractivityTTY,
	})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	if err = led.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt: 1_750_000_050_000, TerminationReason: content.TermTimeout,
		Status: content.EntryFailure,
	}); err != nil {
		t.Fatalf("FinishExecution: %v", err)
	}

	e, err := led.Entry(ctx, entryID)
	if err != nil || e == nil || len(e.Executions) != 1 {
		t.Fatalf("Entry: %v (nil=%v, execs=%d)", err, e == nil, execCount(e))
	}
	ex := e.Executions[0]
	if ex.TerminationReason == nil || *ex.TerminationReason != content.TermTimeout {
		t.Fatalf("termination reason = %v, want timeout — executor-timeout is a distinct outcome", ex.TerminationReason)
	}
	if ex.EndedAt == nil || *ex.EndedAt != 1_750_000_050_000 {
		t.Fatalf("ended_at = %v, want the finish time", ex.EndedAt)
	}
	if e.Phase != content.PhaseClosed || e.Status != content.EntryFailure {
		t.Fatalf("entry after finish = %s/%s, want closed/failure", e.Phase, e.Status)
	}
}

func execCount(e *content.LedgerEntry) int {
	if e == nil {
		return -1
	}
	return len(e.Executions)
}

// ── a close records the entry's terminal facts (nocx-rtg0.23) ────────────

// openBoundEntry submits one entry and starts its run, returning the entry id
// and the execution id — the state a close on an ALREADY-OPEN row starts from,
// which is the state that had nowhere to put an exit code or a duration.
func openBoundEntry(t *testing.T, led content.LedgerRepository, id string) (string, int64) {
	t.Helper()
	ctx := context.Background()
	envReady(t, led, "local")
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "make test", Payload: "{}",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: id})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	return id, execID
}

// The defect this bead fixes: for an entry whose row already exists, a close
// had nowhere to put the shell payload or the duration. FinishExecution now
// writes the entry's terminal facts in the same transaction as the run's.
func TestFinishExecutionPersistsTheEntrysTerminalFacts(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	entryID, execID := openBoundEntry(t, led, "00000000-0000-7000-8000-000000000110")

	before, err := led.Entry(ctx, entryID)
	if err != nil || before == nil {
		t.Fatalf("Entry before close: %v (nil=%v)", err, before == nil)
	}
	if before.DurationMs != nil || before.EndedAt != nil {
		t.Fatalf("an open row already carries terminal facts: duration=%v ended=%v", before.DurationMs, before.EndedAt)
	}

	if err = led.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt: 1_750_000_050_000, TerminationReason: content.TermFailed,
		Status: content.EntryFailure, StartedAt: i64Ptr(1_750_000_047_700),
		DurationMs: i64Ptr(2300), Payload: strPtr(content.ShellPayloadJSON(intPtr(2))),
	}); err != nil {
		t.Fatalf("FinishExecution: %v", err)
	}

	e, err := led.Entry(ctx, entryID)
	if err != nil || e == nil {
		t.Fatalf("Entry: %v (nil=%v)", err, e == nil)
	}
	if e.DurationMs == nil || *e.DurationMs != 2300 {
		t.Fatalf("duration_ms = %v, want 2300 — the close's measured duration", e.DurationMs)
	}
	if e.EndedAt == nil || *e.EndedAt != 1_750_000_050_000 {
		t.Fatalf("ended_at = %v, want the close's end", e.EndedAt)
	}
	if e.StartedAt == nil || *e.StartedAt != 1_750_000_047_700 {
		t.Fatalf("started_at = %v, want the start the close carried", e.StartedAt)
	}
	if code := shellExitCode(t, e.Payload); code == nil || *code != 2 {
		t.Fatalf("payload = %s, want the shell arm carrying exitCode 2", e.Payload)
	}
}

// A close that carries no start does not erase one the row already knew, and
// a close that carries no duration does not erase one either: the close fills
// what is missing, it never overwrites what is known.
func TestFinishExecutionKeepsFactsTheRowAlreadyHeld(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")
	entryID := "00000000-0000-7000-8000-000000000111"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "make test",
		StartedAt: i64Ptr(1_750_000_000_000), DurationMs: i64Ptr(99), Payload: "{}",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: entryID})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	if err = led.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt: 1_750_000_050_000, TerminationReason: content.TermCompleted,
		Status: content.EntrySuccess,
	}); err != nil {
		t.Fatalf("FinishExecution: %v", err)
	}
	e, err := led.Entry(ctx, entryID)
	if err != nil || e == nil {
		t.Fatalf("Entry: %v (nil=%v)", err, e == nil)
	}
	if e.StartedAt == nil || *e.StartedAt != 1_750_000_000_000 {
		t.Fatalf("started_at = %v, want the start the row already held", e.StartedAt)
	}
	if e.DurationMs == nil || *e.DurationMs != 99 {
		t.Fatalf("duration_ms = %v, want the duration the row already held", e.DurationMs)
	}
	if e.Payload != "{}" {
		t.Fatalf("payload = %s, want the untouched default — a close with no payload writes none", e.Payload)
	}
}

// The interval, both ends named: an entry has a duration FROM its close UNTIL
// it is deleted. Before the close there is none; after it there is one, and it
// survives a restart; the only event that removes it is DeleteEntry — the same
// "no fourth exit" the phase has (design §4.3).
func TestEntryHasADurationFromItsCloseUntilItIsDeleted(t *testing.T) {
	db, led, path := newLedgerAt(t)
	ctx := context.Background()
	entryID, execID := openBoundEntry(t, led, "00000000-0000-7000-8000-000000000112")

	for _, when := range []string{"after submit", "after bind"} {
		e, err := led.Entry(ctx, entryID)
		if err != nil || e == nil {
			t.Fatalf("Entry %s: %v (nil=%v)", when, err, e == nil)
		}
		if e.DurationMs != nil {
			t.Fatalf("%s: the row already has a duration %d — the interval opened early", when, *e.DurationMs)
		}
	}

	// The opening event.
	if err := led.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt: 1_750_000_050_000, TerminationReason: content.TermCompleted,
		Status: content.EntrySuccess, DurationMs: i64Ptr(1500),
		Payload: strPtr(content.ShellPayloadJSON(intPtr(0))),
	}); err != nil {
		t.Fatalf("FinishExecution: %v", err)
	}
	e, err := led.Entry(ctx, entryID)
	if err != nil || e == nil || e.DurationMs == nil || *e.DurationMs != 1500 {
		t.Fatalf("after close: duration = %v, want 1500", entryDuration(e))
	}

	// Inside the interval: a restart does not lose it.
	if err = db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	again, err := content.Open(ctx, content.Config{Path: path, Key: testKey(), Budget: testBudget})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()
	led2 := again.Ledger()
	e, err = led2.Entry(ctx, entryID)
	if err != nil || e == nil || e.DurationMs == nil || *e.DurationMs != 1500 {
		t.Fatalf("after reopen: duration = %v, want the durable 1500", entryDuration(e))
	}

	// The closing event, and the only one.
	if err = led2.DeleteEntry(ctx, entryID); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	gone, err := led2.Entry(ctx, entryID)
	if err != nil {
		t.Fatalf("Entry after delete: %v", err)
	}
	if gone != nil {
		t.Fatal("the row survived its deletion, so the duration did too")
	}
}

func entryDuration(e *content.LedgerEntry) any {
	if e == nil || e.DurationMs == nil {
		return nil
	}
	return *e.DurationMs
}

// The entry's UPDATE is the one new external call a close makes, and it can
// fail: a status outside the CHECK is exactly what a caller bypassing the
// wire's validation would write. When it does, NOTHING of the close survives —
// not a half-written payload, not an ended run, not a moved phase. The close
// is one transaction or it is a lie.
func TestFinishExecutionFailingLeavesNoHalfWrittenClose(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	entryID, execID := openBoundEntry(t, led, "00000000-0000-7000-8000-000000000113")

	err := led.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt: 1_750_000_050_000, TerminationReason: content.TermCompleted,
		Status: "cromulent", DurationMs: i64Ptr(4200),
		Payload: strPtr(content.ShellPayloadJSON(intPtr(0))),
	})
	if err == nil {
		t.Fatal("a close with a status outside the CHECK succeeded")
	}

	e, entryErr := led.Entry(ctx, entryID)
	if entryErr != nil || e == nil {
		t.Fatalf("Entry after the failed close: %v (nil=%v)", entryErr, e == nil)
	}
	if e.Phase != content.PhaseBound || e.Status != content.EntryPending {
		t.Fatalf("entry = %s/%s, want the unchanged bound/pending", e.Phase, e.Status)
	}
	if e.Payload != "{}" {
		t.Fatalf("payload = %s after a failed close — half of the close is durable", e.Payload)
	}
	if e.DurationMs != nil || e.EndedAt != nil {
		t.Fatalf("duration=%v ended=%v after a failed close, want neither", e.DurationMs, e.EndedAt)
	}
	if len(e.Executions) != 1 {
		t.Fatalf("%d executions, want 1", len(e.Executions))
	}
	if ex := e.Executions[0]; ex.EndedAt != nil || ex.TerminationReason != nil {
		t.Fatalf("the run was ended by a close that failed: ended=%v reason=%v", ex.EndedAt, ex.TerminationReason)
	}
}

// The property the rejected workaround would have broken. Routing the exit
// code and the duration through Submit on a LATER event would change the
// content digest the idempotency key is bound to, so every outbox replay of
// the original open would come back ErrIDConflict. The facts go through
// FinishExecution precisely so this stays true.
func TestSubmitReplayAfterACloseIsStillAReplay(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	entryID, execID := openBoundEntry(t, led, "00000000-0000-7000-8000-000000000114")
	first, err := led.Entry(ctx, entryID)
	if err != nil || first == nil {
		t.Fatalf("Entry: %v (nil=%v)", err, first == nil)
	}

	if err = led.FinishExecution(ctx, execID, content.FinishExecution{
		EndedAt: 1_750_000_050_000, TerminationReason: content.TermCompleted,
		Status: content.EntrySuccess, StartedAt: i64Ptr(1_750_000_047_700),
		DurationMs: i64Ptr(2300), Payload: strPtr(content.ShellPayloadJSON(intPtr(0))),
	}); err != nil {
		t.Fatalf("FinishExecution: %v", err)
	}

	// The very submission that opened the row, re-delivered after the close.
	res, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "make test", Payload: "{}",
	})
	if errors.Is(err, content.ErrIDConflict) {
		t.Fatal("the close changed the idempotency digest: a replay of the open is now a conflict")
	}
	if err != nil {
		t.Fatalf("Submit replay: %v", err)
	}
	if !res.Replayed {
		t.Fatal("the replay was not reported as one")
	}
	if res.IngestSeq != first.IngestSeq {
		t.Fatalf("replay ingest_seq = %d, want the original %d", res.IngestSeq, first.IngestSeq)
	}
	// And the close's facts are still there — the replay changed nothing.
	e, err := led.Entry(ctx, entryID)
	if err != nil || e == nil || e.DurationMs == nil || *e.DurationMs != 2300 {
		t.Fatalf("after the replay: duration = %v, want the close's 2300", entryDuration(e))
	}
}

func intPtr(n int) *int { return &n }

// shellExitCode reads the exit code out of an entry's kind payload the way
// nocx-rtg0.19's read path will have to: the shell arm of design §3.3.
func shellExitCode(t *testing.T, payload string) *int {
	t.Helper()
	var p struct {
		Kind     string `json:"kind"`
		V        int    `json:"v"`
		ExitCode *int   `json:"exitCode"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("payload %q is not JSON: %v", payload, err)
	}
	if p.Kind != "shell" || p.V != 1 {
		t.Fatalf("payload = %s, want the versioned shell arm", payload)
	}
	return p.ExitCode
}

// ── authority grant recorded on the run (ADR-0020 §5) ────────────────────

func TestExecutionRecordsGrantWithScopes(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	entryID := "00000000-0000-7000-8000-000000000100"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryAsk, Intent: "fix the deploy",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	grantPolicy := presetAutonomous()
	if _, err := led.StartExecution(ctx, content.StartExecution{
		EntryID: entryID, Lane: strPtr("lane-1"), Executor: strPtr("agent:eino"),
		Grant: &content.Grant{
			Version: 2, ExpiresAt: 1_750_000_200_000,
			Policy: grantPolicy,
			Effects: []content.Effect{
				content.EffectObserve,
				content.EffectMutateReversible,
			},
			Scopes: []content.GrantScope{
				{Kind: content.ResourceEnvironment, ID: "local"},
				{Kind: content.ResourceSession, ID: "sess-1"},
			},
		},
	}); err != nil {
		t.Fatalf("StartExecution with grant: %v", err)
	}

	e, err := led.Entry(ctx, entryID)
	if err != nil || e == nil || len(e.Executions) != 1 {
		t.Fatalf("Entry: %v (nil=%v)", err, e == nil)
	}
	g := e.Executions[0].Grant
	if g == nil {
		t.Fatal("grant missing from the run — 'this run held a grant' must be a query, not a reconstruction")
	}
	if g.Version != 2 || g.ExpiresAt != 1_750_000_200_000 {
		t.Fatalf("grant = %+v, want version 2 expiring 1750000200000", g)
	}
	for _, e := range []content.Effect{
		content.EffectObserve, content.EffectMutateReversible, content.EffectMutateDestructive,
		content.EffectPrivilegeChange, content.EffectDisclose, content.EffectCrossBoundary,
		content.EffectDelegate,
	} {
		if got, want := g.Policy.DecisionFor(e), grantPolicy.DecisionFor(e); got != want {
			t.Fatalf("roundtripped policy decides %s = %s, want %s (the autonomous matrix recorded on the run)", e, got, want)
		}
	}
	if len(g.Effects) != 2 || g.Effects[0] != content.EffectMutateReversible || g.Effects[1] != content.EffectObserve {
		t.Fatalf("grant effects = %v, want [mutate-reversible observe] (stored ORDER BY effect)", g.Effects)
	}
	if len(g.Scopes) != 2 || g.Scopes[0].Kind != content.ResourceEnvironment || g.Scopes[1].Kind != content.ResourceSession {
		t.Fatalf("grant scopes = %+v, want environment + session", g.Scopes)
	}

	// A run without a grant carries none.
	plainEntry := "00000000-0000-7000-8000-000000000101"
	if _, err = led.Submit(ctx, content.SubmitEntry{
		ID: plainEntry, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "plain",
	}); err != nil {
		t.Fatalf("Submit plain: %v", err)
	}
	if _, err = led.StartExecution(ctx, content.StartExecution{EntryID: plainEntry}); err != nil {
		t.Fatalf("StartExecution plain: %v", err)
	}
	pe, err := led.Entry(ctx, plainEntry)
	if err != nil || pe == nil {
		t.Fatalf("Entry plain: %v", err)
	}
	if pe.Executions[0].Grant != nil {
		t.Fatal("a run started without a grant recorded one")
	}
}

// ── edges: the relation primitive ────────────────────────────────────────

func TestEdgesRoundTripBothDirections(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	ids := submitIntents(t, led, "failed", "retried", "worked")

	if err := led.AddEdge(ctx, content.Edge{From: ids[0], To: ids[1], Rel: content.RelRerunOf}); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	if err := led.AddEdge(ctx, content.Edge{From: ids[1], To: ids[2], Rel: content.RelSupersedes}); err != nil {
		t.Fatalf("AddEdge 2: %v", err)
	}

	edges, err := led.Edges(ctx, ids[1])
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("Edges = %d, want 2 (one in each direction)", len(edges))
	}
}

// An edge endpoint must exist; the FK is the check.
func TestAddEdgeToMissingEntryFails(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	ids := submitIntents(t, led, "real")

	err := led.AddEdge(ctx, content.Edge{From: ids[0], To: "ghost", Rel: content.RelCites})
	if err == nil {
		t.Fatal("edge to a missing entry succeeded")
	}
	// Paired success.
	if err := led.AddEdge(ctx, content.Edge{From: ids[0], To: ids[0], Rel: content.RelCites}); err != nil {
		t.Fatalf("edge between real entries failed: %v", err)
	}
}

// ── artifacts: provenance, chunks, byte_len, rejections ──────────────────

// The capture provenance (ADR-0019 §6) rides the artifact: how the text was
// taken, at what stream position, with which dimensions and where the gaps
// are. It must read back unchanged.
func TestArtifactProvenanceRoundTrips(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	entryID := "00000000-0000-7000-8000-000000000110"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "build",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: entryID})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	cols, rows := 120, 40
	off, end := int64(2048), int64(4096)
	stream := content.StreamCombined
	artID, err := led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: entryID, ExecutionID: &execID, ID: "cccccccc-0000-7000-8000-000000000001",
		MediaType: content.MediaText, CaptureMethod: content.CaptureTerminalCells,
		CaptureVersion: 3, TerminalCols: &cols, TerminalRows: &rows,
		Stream: &stream, ByteOffset: &off, ByteEnd: &end, Encoding: "utf-8",
		Gaps: []content.Gap{{Start: 100, End: 200, Reason: "scrollback dropped"}},
	})
	if err != nil {
		t.Fatalf("AppendArtifact: %v", err)
	}

	a, err := led.Artifact(ctx, artID)
	if err != nil || a == nil {
		t.Fatalf("Artifact: %v (nil=%v)", err, a == nil)
	}
	if a.CaptureMethod != content.CaptureTerminalCells || a.CaptureVersion != 3 {
		t.Fatalf("capture = %s v%d, want terminal-cells v3", a.CaptureMethod, a.CaptureVersion)
	}
	if a.TerminalCols == nil || *a.TerminalCols != 120 || a.TerminalRows == nil || *a.TerminalRows != 40 {
		t.Fatalf("terminal dims = %vx%v, want 120x40", a.TerminalCols, a.TerminalRows)
	}
	if a.Stream == nil || *a.Stream != content.StreamCombined {
		t.Fatalf("stream = %v, want combined", a.Stream)
	}
	if a.ByteOffset == nil || *a.ByteOffset != 2048 || a.ByteEnd == nil || *a.ByteEnd != 4096 {
		t.Fatalf("byte span = %v..%v, want 2048..4096", a.ByteOffset, a.ByteEnd)
	}
	if len(a.Gaps) != 1 || a.Gaps[0].Start != 100 || a.Gaps[0].End != 200 {
		t.Fatalf("gaps = %+v, want [{100 200 scrollback dropped}]", a.Gaps)
	}
}

// Chunks maintain byte_len as logical content bytes: the sum of the bodies.
func TestAppendChunkMaintainsByteLen(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	entryID := "00000000-0000-7000-8000-000000000111"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "produce output",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: entryID})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	artID, err := led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: entryID, ExecutionID: &execID, ID: "cccccccc-0000-7000-8000-000000000002",
		MediaType: content.MediaText,
	})
	if err != nil {
		t.Fatalf("AppendArtifact: %v", err)
	}
	a, err := led.Artifact(ctx, artID)
	if err != nil || a == nil {
		t.Fatalf("Artifact before chunks: %v", err)
	}
	if a.ByteLen != 0 {
		t.Fatalf("fresh artifact byte_len = %d, want 0", a.ByteLen)
	}
	if err = led.AppendChunk(ctx, artID, 1, []byte("0123456789")); err != nil {
		t.Fatalf("AppendChunk 1: %v", err)
	}
	if err = led.AppendChunk(ctx, artID, 2, []byte("abcdef")); err != nil {
		t.Fatalf("AppendChunk 2: %v", err)
	}
	a, err = led.Artifact(ctx, artID)
	if err != nil || a == nil {
		t.Fatalf("Artifact after chunks: %v", err)
	}
	if a.ByteLen != 16 {
		t.Fatalf("byte_len = %d, want 16 (logical content bytes, the retention budget's unit)", a.ByteLen)
	}
	if a.ChunkCount != 2 {
		t.Fatalf("chunk count = %d, want 2", a.ChunkCount)
	}
	if len(a.Chunks) != 2 || string(a.Chunks[0]) != "0123456789" || string(a.Chunks[1]) != "abcdef" {
		t.Fatalf("chunks = %q, want [0123456789 abcdef] in seq order", a.Chunks)
	}
}

// An artifact needs a real BLOCK, a real execution when it names one, and a
// closed media type; a chunk needs a real artifact. The FK and the CHECK are
// the checks.
func TestArtifactWritesValidateTheirTargets(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")

	entryID := "00000000-0000-7000-8000-000000000112"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: entryID, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "x",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: entryID})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}

	ghost := int64(999_999)
	if _, err = led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: entryID, ExecutionID: &ghost, ID: "cccccccc-0000-7000-8000-000000000003",
		MediaType: content.MediaText,
	}); err == nil {
		t.Fatal("artifact under a missing execution succeeded")
	}
	if _, err = led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: "no-such-entry", ExecutionID: &execID, ID: "cccccccc-0000-7000-8000-000000000006",
		MediaType: content.MediaText,
	}); err == nil {
		t.Fatal("artifact under a missing block succeeded")
	}
	if _, err = led.AppendArtifact(ctx, content.AppendArtifact{
		ExecutionID: &execID, ID: "cccccccc-0000-7000-8000-000000000007",
		MediaType: content.MediaText,
	}); err == nil {
		t.Fatal("artifact with no block at all succeeded — an artifact belongs to its block")
	}
	if _, err = led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: entryID, ExecutionID: &execID, ID: "cccccccc-0000-7000-8000-000000000004",
		MediaType: "image/png",
	}); err == nil {
		t.Fatal("artifact with a media type outside the closed set succeeded")
	}
	// Paired successes.
	artID, err := led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: entryID, ExecutionID: &execID, ID: "cccccccc-0000-7000-8000-000000000005",
		MediaType: content.MediaText,
	})
	if err != nil {
		t.Fatalf("artifact under a real execution failed: %v", err)
	}
	if err := led.AppendChunk(ctx, "no-such-artifact", 1, []byte("x")); err == nil {
		t.Fatal("chunk under a missing artifact succeeded")
	}
	if err := led.AppendChunk(ctx, artID, 1, []byte("x")); err != nil {
		t.Fatalf("chunk under a real artifact failed: %v", err)
	}
}

// ── the closed enums reject unknown values at the schema level ───────────

// CHECK constraints close every enum; a value outside the set is refused by
// SQLite before any Go code could see it. The paired successes — the valid
// values — are the ordinary writes every other test in this file performs.
func TestClosedEnumsRejectUnknownValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	db, err := content.Open(context.Background(), content.Config{
		Path: path, Key: testKey(), Budget: testBudget,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	keyHex := hex.EncodeToString(testKey())

	// Parents for the FK-satisfying rows; every failing statement below is
	// self-contained against them.
	if err := rawLedger(t, path, keyHex,
		`INSERT INTO environments (id, kind, first_seen) VALUES ('env', 'local', 1)`,
		`INSERT INTO environment_observations (environment_id, version, observed_at, criticality)
			VALUES ('env', 1, 1, 'routine')`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			phase, status, submitted_at) VALUES ('a', 1, 'c', 'd', 'env', '/', 'shell', 'user', 'x', 'open', 'pending', 1)`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			phase, status, submitted_at) VALUES ('b', 2, 'c', 'd', 'env', '/', 'shell', 'user', 'x', 'open', 'pending', 1)`,
		`INSERT INTO executions (entry_id, environment_obs_id) VALUES ('a', 1)`,
	); err != nil {
		t.Fatalf("seed rows: %v", err)
	}

	cases := []struct {
		name string
		sql  string
	}{
		{"entries.kind", `INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			phase, status, submitted_at) VALUES ('k1', 3, 'c', 'd', 'env', '/', 'banana', 'user', 'x', 'open', 'pending', 1)`},
		{"entries.phase", `INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			phase, status, submitted_at) VALUES ('p1', 3, 'c', 'd', 'env', '/', 'shell', 'user', 'x', 'weird', 'pending', 1)`},
		{"entries.status", `INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			phase, status, submitted_at) VALUES ('s1', 3, 'c', 'd', 'env', '/', 'shell', 'user', 'x', 'open', 'done', 1)`},
		{"entries.sensitivity", `INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			phase, status, submitted_at, sensitivity) VALUES ('se1', 3, 'c', 'd', 'env', '/', 'shell', 'user', 'x', 'open', 'pending', 1, 'secret')`},
		{"edges.rel", `INSERT INTO edges (from_id, to_id, rel) VALUES ('a', 'b', 'related-to')`},
		{"environments.kind", `INSERT INTO environments (id, kind, first_seen) VALUES ('e2', 'docker', 1)`},
		{"environment_observations.criticality", `INSERT INTO environment_observations
			(environment_id, version, observed_at, criticality) VALUES ('env', 2, 1, 'high')`},
		{"executions.interactivity", `INSERT INTO executions (entry_id, environment_obs_id, interactivity)
			VALUES ('b', 1, 'full')`},
		{"executions.termination_reason", `INSERT INTO executions (entry_id, environment_obs_id, termination_reason)
			VALUES ('b', 1, 'exploded')`},
		{"authority_grants.policy", `INSERT INTO authority_grants
			(execution_id, version, issued_at, expires_at, policy) VALUES (1, 1, 1, 2, 'yolo')`},
		{"artifacts.media_type", `INSERT INTO artifacts (id, entry_id, execution_id, media_type)
			VALUES ('art', 'a', 1, 'text/html')`},
		{"artifacts.truncated", `INSERT INTO artifacts (id, entry_id, execution_id, media_type, truncated)
			VALUES ('art2', 'a', 1, 'text/plain', 'clipped')`},
		{"artifacts.capture_method", `INSERT INTO artifacts (id, entry_id, execution_id, media_type, capture_method)
			VALUES ('art3', 'a', 1, 'text/plain', 'telepathy')`},
	}
	for _, c := range cases {
		if err := rawLedger(t, path, keyHex, c.sql); err == nil {
			t.Errorf("%s: an unknown value was accepted", c.name)
		}
	}
}

// ── the source and the seat are the database's (criteria 4, 7) ───────────

// The two CHECKs this task added are the schema's, and a raw statement is
// the only way to test them: a Go-typed caller cannot express an unknown
// source, and Submit refuses to write a seatless root before the engine
// ever sees it. Each refusal gets its paired success on an ordinary row.
func TestSchemaRefusesBadSourceAndSeatlessRoots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	db, err := content.Open(context.Background(), content.Config{
		Path: path, Key: testKey(), Budget: testBudget,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	keyHex := hex.EncodeToString(testKey())
	if err := rawLedger(t, path, keyHex,
		`INSERT INTO environments (id, kind, first_seen) VALUES ('env', 'local', 1)`,
	); err != nil {
		t.Fatalf("seed environment: %v", err)
	}

	refused := []struct {
		name string
		sql  string
	}{
		{"a source outside the enum", `INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			 phase, status, submitted_at) VALUES ('s1', 1, 'c', 'd', 'env', '/', 'shell', 'robot', 'x', 'open', 'pending', 1)`},
		{"a root that holds a seat", `INSERT INTO entries
			(id, ingest_seq, client, digest, environment_id, parent_id, pos, cwd, kind, source, intent,
			 phase, status, submitted_at) VALUES ('s2', 2, 'c', 'd', 'env', NULL, 5, '/', 'shell', 'user', 'x', 'open', 'pending', 1)`},
	}
	for _, c := range refused {
		if err := rawLedger(t, path, keyHex, c.sql); err == nil {
			t.Errorf("%s: the schema accepted it", c.name)
		}
	}

	// THE PAIRED SUCCESSES (criterion 7): an ordinary row with a valid
	// source, and a child that legitimately holds a seat — both land.
	if err := rawLedger(t, path, keyHex,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			phase, status, submitted_at) VALUES ('ok1', 3, 'c', 'd', 'env', '/', 'shell', 'assistant', 'x', 'open', 'pending', 1)`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, cwd, kind, source, intent,
			phase, status, submitted_at) VALUES ('ok2', 4, 'c', 'd', 'env', '/', 'shell', 'user', 'x', 'open', 'pending', 1)`,
		`INSERT INTO entries (id, ingest_seq, client, digest, environment_id, parent_id, pos, cwd, kind, source, intent,
			phase, status, submitted_at) VALUES ('ok3', 5, 'c', 'd', 'env', 'ok2', 0, '/', 'shell', 'user', 'x', 'open', 'pending', 1)`,
	); err != nil {
		t.Fatalf("the paired successes were refused: %v", err)
	}
}

// ── restore: a multi-row procedure fails as a whole ──────────────────────

// ── closed store ─────────────────────────────────────────────────────────

// Every external call has a failing test: a closed store refuses every
// ledger mutation with ErrClosed, before the writer is even invoked.
func TestLedgerRejectsAllWritesAfterClose(t *testing.T) {
	db, led := newLedger(t)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	writes := []struct {
		name string
		do   func() error
	}{
		{"CreateSession", func() error {
			return led.CreateSession(ctx, content.Session{ID: "s", WorkspaceID: "w"})
		}},
		{"DeleteSession", func() error { return led.DeleteSession(ctx, "s") }},
		{"EnsureEnvironment", func() error {
			return led.EnsureEnvironment(ctx, content.Environment{ID: "e", Kind: content.EnvLocal})
		}},
		{"RecordObservation", func() error {
			_, err := led.RecordObservation(ctx, content.Observation{EnvironmentID: "e"})
			return err
		}},
		{"Submit", func() error {
			_, err := led.Submit(ctx, content.SubmitEntry{ID: "i", Client: "c"})
			return err
		}},
		{"DeleteEntry", func() error { return led.DeleteEntry(ctx, "i") }},
		{"StartExecution", func() error {
			_, err := led.StartExecution(ctx, content.StartExecution{EntryID: "i"})
			return err
		}},
		{"FinishExecution", func() error {
			return led.FinishExecution(ctx, 1, content.FinishExecution{})
		}},
		{"AppendArtifact", func() error {
			_, err := led.AppendArtifact(ctx, content.AppendArtifact{
				ID: "a", EntryID: "i", MediaType: content.MediaText,
			})
			return err
		}},
		{"AppendChunk", func() error { return led.AppendChunk(ctx, "a", 1, []byte("x")) }},
		{"AddEdge", func() error {
			return led.AddEdge(ctx, content.Edge{From: "i", To: "i", Rel: content.RelCites})
		}},
		{"AddCause", func() error {
			_, err := led.AddCause(ctx, "i", "i")
			return err
		}},
	}
	for _, w := range writes {
		if err := w.do(); !errors.Is(err, content.ErrClosed) {
			t.Errorf("%s after Close = %v, want ErrClosed", w.name, err)
		}
	}
}

// ── two writers, one file: both submits land (nocx-rtg0.18) ───────────────

// Within one store the writer goroutine serializes submits; across two
// stores (two windows, two processes) each has its own writer, so two
// deferred transactions can read the same snapshot and the loser's upgrade
// dies with SQLITE_BUSY_SNAPSHOT, which busy_timeout does not repair. The
// fix is BEGIN IMMEDIATE (the ncruces driver maps LevelSerializable to it):
// the second writer waits on the write lock and reads a fresh snapshot.
// This test asserts the contract the review named — both stores' submits
// land, with distinct, gap-free ingest_seq — with two real store instances
// on one file, not by reasoning about locks.
func TestTwoStoresSubmitConcurrentlyWithoutLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "content.db")
	cfg := content.Config{
		Path: path, Key: testKey(), Budget: testBudget,
	}
	ctx := context.Background()

	// openAt opens a store on the shared file, failing the test on error —
	// every call site is a Fatalf site, so no error survives to shadow
	// another.
	openAt := func() content.ContentDB {
		t.Helper()
		db, openErr := content.Open(ctx, cfg)
		if openErr != nil {
			t.Fatalf("Open: %v", openErr)
		}
		return db
	}

	// Bootstrap the schema once and close it, so the two stores below open a
	// current file instead of racing each other through Open's creation path.
	boot := openAt()
	if closeErr := boot.Close(); closeErr != nil {
		t.Fatalf("bootstrap Close: %v", closeErr)
	}

	a := openAt()
	defer func() { _ = a.Close() }()
	b := openAt()
	defer func() { _ = b.Close() }()

	if envErr := a.Ledger().EnsureEnvironment(ctx, content.Environment{ID: "local", Kind: content.EnvLocal}); envErr != nil {
		t.Fatalf("EnsureEnvironment: %v", envErr)
	}

	const perStore = 40
	errs := make(chan error, 2)
	seqs := make(chan int64, 2*perStore)
	submit := func(db content.ContentDB, base int) {
		led := db.Ledger()
		for i := range perStore {
			res, subErr := led.Submit(ctx, content.SubmitEntry{
				ID:     fmt.Sprintf("00000000-0000-7000-8000-%012d", base+i),
				Client: "client", EnvironmentID: "local", Cwd: "/repo",
				Kind: content.EntryShell, Intent: fmt.Sprintf("cmd-%d", base+i),
			})
			if subErr != nil {
				errs <- subErr
				return
			}
			seqs <- res.IngestSeq
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); submit(a, 0) }()
	go func() { defer wg.Done(); submit(b, perStore) }()
	wg.Wait()
	close(errs)
	for subErr := range errs {
		t.Fatalf("concurrent Submit: %v — one of the two writers lost", subErr)
	}
	close(seqs)
	var submitted []int64
	for s := range seqs {
		submitted = append(submitted, s)
	}
	if len(submitted) != 2*perStore {
		t.Fatalf("successful submits = %d, want %d", len(submitted), 2*perStore)
	}

	// Both stores closed before the final read: the read is of the file,
	// not of one store's in-memory view.
	if closeErr := a.Close(); closeErr != nil {
		t.Fatalf("Close A: %v", closeErr)
	}
	if closeErr := b.Close(); closeErr != nil {
		t.Fatalf("Close B: %v", closeErr)
	}
	again := openAt()
	defer func() { _ = again.Close() }()
	page, listErr := again.Ledger().ListEntries(ctx, 2*perStore)
	if listErr != nil {
		t.Fatalf("ListEntries: %v", listErr)
	}
	if len(page) != 2*perStore {
		t.Fatalf("rows on disk = %d, want %d — a concurrent submit was lost", len(page), 2*perStore)
	}
	back := make([]int64, 0, len(page))
	for _, e := range page {
		back = append(back, e.IngestSeq)
	}
	sort.Slice(back, func(i, j int) bool { return back[i] < back[j] })
	for i, seq := range back {
		if seq != int64(i+1) {
			t.Fatalf("ingest_seq on disk = %v, want exactly 1..%d — a gap or a duplicate", back, 2*perStore)
		}
	}
}

// ── acceptance: an entry says which host it ran on (nocx-rtg0.25) ─────────

// history.query's contract declares host on every entry; command_history
// held it as a column and the ledger holds entries.environment_id instead —
// the id environmentForSession derives from the session. FINDING rows for a
// host has always worked (EnvironmentIDFor hashes a host forward), and
// SAYING which host a found row ran on did not exist at all.
//
// Both a local and an ssh entry are asserted here because either one alone
// passes while the other is wrong: local's host IS the empty string, so a
// read that answers "" for everything is green on the local entry and
// silently empty on every remote one.
func TestAnEntrySaysWhichHostItRanOn(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()

	const remoteHost = "build.example.com"
	localEnv := content.EnvironmentIDFor(content.EnvLocal, "")
	sshEnv := content.EnvironmentIDFor(content.EnvSSH, remoteHost)

	// The two environments exactly as environmentForSession mints them: a
	// local session carries no endpoint, an ssh session carries its host.
	if err := led.EnsureEnvironment(ctx, content.Environment{
		ID: localEnv, Kind: content.EnvLocal,
	}); err != nil {
		t.Fatalf("EnsureEnvironment local: %v", err)
	}
	if err := led.EnsureEnvironment(ctx, content.Environment{
		ID: sshEnv, Kind: content.EnvSSH, Endpoint: strPtr(remoteHost),
	}); err != nil {
		t.Fatalf("EnsureEnvironment ssh: %v", err)
	}

	localID := "00000000-0000-7000-8000-000000000101"
	sshID := "00000000-0000-7000-8000-000000000102"
	for _, s := range []content.SubmitEntry{
		{
			ID: localID, Client: "c", EnvironmentID: localEnv, Cwd: "/repo",
			Kind: content.EntryShell, Intent: "make test",
		},
		{
			ID: sshID, Client: "c", EnvironmentID: sshEnv, Cwd: "/srv",
			Kind: content.EntryShell, Intent: "systemctl restart nocx",
		},
	} {
		if _, err := led.Submit(ctx, s); err != nil {
			t.Fatalf("Submit %q: %v", s.Intent, err)
		}
	}

	want := map[string]string{localID: "", sshID: remoteHost}
	wantKind := map[string]content.EnvironmentKind{
		localID: content.EnvLocal, sshID: content.EnvSSH,
	}

	// The recall read answers it.
	for id, host := range want {
		e, err := led.Entry(ctx, id)
		if err != nil || e == nil {
			t.Fatalf("Entry(%q): %v (nil=%v)", id, err, e == nil)
		}
		if e.Environment == nil {
			t.Fatalf("Entry(%q) resolved no environment — the row cannot say which host it ran on", id)
		}
		if got := e.Environment.Host(); got != host {
			t.Fatalf("Entry(%q) host = %q, want %q", id, got, host)
		}
		if e.Environment.Kind != wantKind[id] {
			t.Fatalf("Entry(%q) kind = %q, want %q", id, e.Environment.Kind, wantKind[id])
		}
		if e.Environment.ID != e.EnvironmentID {
			t.Fatalf("Entry(%q) resolved environment %q for environment_id %q",
				id, e.Environment.ID, e.EnvironmentID)
		}
	}

	// And so does the timeline read the query in nocx-rtg0.20 is built on.
	page, err := led.ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("ListEntries = %d rows, want 2", len(page))
	}
	for _, row := range page {
		host, ok := want[row.ID]
		if !ok {
			t.Fatalf("unexpected row %q", row.ID)
		}
		if row.Environment == nil {
			t.Fatalf("ListEntries row %q resolved no environment", row.ID)
		}
		if got := row.Environment.Host(); got != host {
			t.Fatalf("ListEntries row %q host = %q, want %q", row.ID, got, host)
		}
	}
}

// An entry whose environment row is gone answers "unknown", never "local".
// The FK on entries.environment_id makes this state unreachable through the
// seam, so the row is removed the only way it can be — on a connection with
// foreign_keys OFF, the way a hand-edited or half-restored file would arrive
// — and the read must not invent the empty string, which is a real answer
// meaning "the local machine".
func TestEntryWithNoEnvironmentRowSaysUnknownRatherThanLocal(t *testing.T) {
	db, led, path := newLedgerAt(t)
	ctx := context.Background()
	envReady(t, led, "local")

	id := "00000000-0000-7000-8000-000000000110"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "make test",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// One statement, so the pragma and the delete cannot land on two
	// different pooled connections: foreign_keys is per connection.
	if err := rawLedger(t, path, hex.EncodeToString(testKey()),
		`PRAGMA foreign_keys=OFF; DELETE FROM environments WHERE id = 'local';`); err != nil {
		t.Fatalf("delete the environment row: %v", err)
	}

	again, err := content.Open(ctx, content.Config{Path: path, Key: testKey(), Budget: testBudget})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = again.Close() }()

	e, err := again.Ledger().Entry(ctx, id)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if e == nil {
		t.Fatal("the entry itself vanished — a missing environment must not drop the row")
	}
	if e.EnvironmentID != "local" {
		t.Fatalf("EnvironmentID = %q, want the id the row still carries", e.EnvironmentID)
	}
	if e.Environment != nil {
		t.Fatalf("Entry resolved %+v for an environment row that is gone — want nil (unknown)", e.Environment)
	}

	page, err := again.Ledger().ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("ListEntries = %d rows, want the 1 row whose environment is gone", len(page))
	}
	if page[0].Environment != nil {
		t.Fatalf("ListEntries resolved %+v for an environment row that is gone — want nil", page[0].Environment)
	}
}

// The read path's one external call is the query itself, and a closed store
// is how it fails: both reads report the failure rather than answering with
// an empty page (a page that cannot be told from "no history").
func TestLedgerReadsAfterCloseReportTheFailure(t *testing.T) {
	db, led := newLedger(t)
	ctx := context.Background()
	envReady(t, led, "local")
	id := "00000000-0000-7000-8000-000000000120"
	if _, err := led.Submit(ctx, content.SubmitEntry{
		ID: id, Client: "c", EnvironmentID: "local", Cwd: "/repo",
		Kind: content.EntryShell, Intent: "make test",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if e, err := led.Entry(ctx, id); err == nil {
		t.Fatalf("Entry after Close = (%v, nil), want an error", e)
	}
	page, err := led.ListEntries(ctx, 10)
	if err == nil {
		t.Fatalf("ListEntries after Close = (%v, nil), want an error", page)
	}
	if page != nil {
		t.Fatalf("ListEntries after Close returned %d rows alongside its error", len(page))
	}
}
