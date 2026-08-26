package transport

// ledger.capture over the real socket (nocx-2f0f, design §4).
//
// The method exists so a frozen block's body reaches the store, and the two
// properties worth testing at this seam are the ones the store cannot check
// for itself: what an UNTRUSTED caller may send, and that the ack the
// renderer reads is the shape the contract promises.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/log"
)

const (
	captureArtifactID = "0198f2b0-0000-7000-8000-00000000c001"
	otherArtifactID   = "0198f2b0-0000-7000-8000-00000000c002"
)

// aRecordedCommand puts one finished command in the store the way
// history.record does, and hands back the entry id a capture hangs on. It
// goes through RecordCompleted rather than open/bind/close over the wire
// because the artifact needs an EXECUTION, and that method writes the entry
// and its run in one transaction — which is the state a real capture meets.
func aRecordedCommand(t *testing.T, db content.ContentDB, intent string) string {
	t.Helper()
	id, err := db.Ledger().RecordCompleted(context.Background(), content.CompletedCommand{
		Client: "test-client",
		Env:    content.Environment{ID: "local", Kind: content.EnvLocal},
		Cwd:    "/repo",
		Intent: intent,
		Status: content.EntrySuccess,
		Source: content.SourceUser,
	})
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	return id
}

// newLedgerStoreWithPolicy is newLedgerStore with the History policy handed
// in — the one thing that decides whether a body is kept at all.
func newLedgerStoreWithPolicy(t *testing.T, policy *content.Policy) content.ContentDB {
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
		Policy: policy,
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func captureParams(entryID, artifactID string, seq int, body string) map[string]any {
	return map[string]any{
		"entryId": entryID, "artifactId": artifactID,
		"mediaType": "application/vt", "captureVersion": 1,
		"terminalCols": 80, "terminalRows": 24,
		"seq": seq, "body": body,
	}
}

type captureAck struct {
	ArtifactID string `json:"artifactId"`
	Stored     bool   `json:"stored"`
}

func captureCall(t *testing.T, conn *websocket.Conn, params map[string]any, id int) (json.RawMessage, *jsonrpcErrorObj) {
	t.Helper()
	raw := jsonrpcCallWithID(t, conn, "ledger.capture", params, id)
	var env struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode ledger.capture response: %v\nraw: %s", err, raw)
	}
	return env.Result, env.Error
}

// THE test of a contract: the REAL result off the REAL socket, not a payload
// the test built (AGENTS.md testing rule 5).
func TestLedgerCapture_OverTheWireConformsToContract(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "ls -la")

	result, rpcErr := captureCall(t, conn, captureParams(entryID, captureArtifactID, 1, "\x1b[31mred\x1b[0m"), 1)
	if rpcErr != nil {
		t.Fatalf("ledger.capture: %+v", rpcErr)
	}
	validateJSON(t, loadSchema(t, "ledger.capture.schema.json"), result, "ledger.capture")

	var ack captureAck
	if err := json.Unmarshal(result, &ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.ArtifactID != captureArtifactID || !ack.Stored {
		t.Fatalf("ack = %+v, want the minted id and stored", ack)
	}
}

// The body reaches the store, and reaches it whole.
func TestLedgerCapture_StoresTheBodyItWasSent(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "cat notes")

	if _, rpcErr := captureCall(t, conn, captureParams(entryID, captureArtifactID, 1, "first "), 1); rpcErr != nil {
		t.Fatalf("chunk 1: %+v", rpcErr)
	}
	if _, rpcErr := captureCall(t, conn, captureParams(entryID, captureArtifactID, 2, "second"), 2); rpcErr != nil {
		t.Fatalf("chunk 2: %+v", rpcErr)
	}

	art, err := db.Ledger().Artifact(context.Background(), captureArtifactID)
	if err != nil || art == nil {
		t.Fatalf("Artifact: %v", err)
	}
	var sb strings.Builder
	for _, c := range art.Chunks {
		sb.Write(c)
	}
	if sb.String() != "first second" {
		t.Fatalf("body = %q, want %q", sb.String(), "first second")
	}
	// The provenance the METHOD owns, not the caller: a renderer that could
	// name its own capture method could claim a fidelity it did not have.
	if art.CaptureMethod != content.CaptureTerminalCells {
		t.Fatalf("capture_method = %q, want terminal-cells", art.CaptureMethod)
	}
}

// A retry after a lost ack. Same answer, one body.
func TestLedgerCapture_ARetryIsNotASecondBody(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "ls")
	params := captureParams(entryID, captureArtifactID, 1, "once")

	for i, id := range []int{1, 2} {
		result, rpcErr := captureCall(t, conn, params, id)
		if rpcErr != nil {
			t.Fatalf("capture %d: %+v", i, rpcErr)
		}
		var ack captureAck
		_ = json.Unmarshal(result, &ack)
		if !ack.Stored {
			t.Fatalf("capture %d answered stored=false", i)
		}
	}
	art, _ := db.Ledger().Artifact(context.Background(), captureArtifactID)
	if len(art.Chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(art.Chunks))
	}
}

// What an untrusted caller may not send. Each of these would otherwise become
// a row somebody has to explain later.
func TestLedgerCapture_RefusesWhatItCannotBelieve(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "ls")

	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"an artifact id that is not a UUIDv7", func(p map[string]any) { p["artifactId"] = "artifact-1" }},
		{"a media type this method does not carry", func(p map[string]any) { p["mediaType"] = "text/markdown" }},
		{"a seq below one", func(p map[string]any) { p["seq"] = 0 }},
		{"an empty entry id", func(p map[string]any) { p["entryId"] = "" }},
		{"a capture version below one", func(p map[string]any) { p["captureVersion"] = 0 }},
		{"a body above the per-chunk ceiling", func(p map[string]any) {
			p["body"] = strings.Repeat("x", maxCaptureChunkBytes+1)
		}},
		{"a truncation reason that is not one", func(p map[string]any) { p["truncated"] = "shrugged" }},
		{"a derivedFrom that is not a UUIDv7", func(p map[string]any) { p["derivedFrom"] = "the-other-one" }},
	}
	for i, c := range cases {
		p := captureParams(entryID, captureArtifactID, 1, "body")
		c.mutate(p)
		_, rpcErr := captureCall(t, conn, p, 10+i)
		if rpcErr == nil || rpcErr.Code != -32602 {
			t.Fatalf("%s: err = %+v, want -32602", c.name, rpcErr)
		}
	}
}

// An entry nothing carries is a fact about the REQUEST — invalid params, not
// a server fault, and never a silent success that leaves the renderer
// believing the body is safe.
func TestLedgerCapture_RefusesAnUnknownEntry(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)

	_, rpcErr := captureCall(t, conn, captureParams("no-such-entry", captureArtifactID, 1, "body"), 1)
	if rpcErr == nil || rpcErr.Code != -32602 {
		t.Fatalf("err = %+v, want -32602", rpcErr)
	}
}

// Output retention off: the call SUCCEEDS and says the body was not kept, so
// the renderer stops sending chunks instead of pushing a body nobody stores.
func TestLedgerCapture_SaysWhenTheBodyIsNotKept(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetOutputEnabled(false)
	db := newLedgerStoreWithPolicy(t, policy)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	entryID := aRecordedCommand(t, db, "ls")

	result, rpcErr := captureCall(t, conn, captureParams(entryID, captureArtifactID, 1, "body"), 1)
	if rpcErr != nil {
		t.Fatalf("ledger.capture: %+v", rpcErr)
	}
	var ack captureAck
	if err := json.Unmarshal(result, &ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.Stored {
		t.Fatal("stored = true while output retention is off")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// The epic's happy path (nocx-2f0f): the output of a past command is still
// readable.
// ═══════════════════════════════════════════════════════════════════════════
//
// WHY THIS TEST EXISTS at all, when every unit below it passes. `deadcode`
// cannot report that a feature is wired: on an interface-first tree RTA
// counts every method a live interface value can hold as reachable, so a
// write path with no caller is invisible to the ratchet. That is the shape
// nocx-rtg0 shipped once — an encrypted store, a key lifecycle, a retention
// policy, and ContentDB.Add with no caller outside its own tests. This is
// the check that watches the feature happen instead.
//
// The restart is REAL: the store is closed and reopened from the same file,
// so the bytes are read back through the encrypted file rather than out of a
// process that still remembers writing them.
func TestCapturedOutputSurvivesAStoreRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "content.db")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	open := func() content.ContentDB {
		db, err := content.Open(context.Background(), content.Config{
			Path:   path,
			Key:    key,
			Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
			Logger: log.NewSlogAdapter(nil),
		})
		if err != nil {
			t.Fatalf("content.Open: %v", err)
		}
		return db
	}

	const bodyA = "\x1b[32mPASS\x1b[0m\n12 tests"
	const bodyB = "\x1b[31mFAIL\x1b[0m\nsomething else entirely"
	var entryA, entryB string

	func() {
		db := open()
		defer func() { _ = db.Close() }()
		ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
		defer stop()
		conn := connectWS(t, ws)

		entryA = aRecordedCommand(t, db, "make test")
		entryB = aRecordedCommand(t, db, "make lint")
		if _, rpcErr := captureCall(t, conn, captureParams(entryA, captureArtifactID, 1, bodyA), 1); rpcErr != nil {
			t.Fatalf("capture A: %+v", rpcErr)
		}
		if _, rpcErr := captureCall(t, conn, captureParams(entryB, otherArtifactID, 1, bodyB), 2); rpcErr != nil {
			t.Fatalf("capture B: %+v", rpcErr)
		}
	}()

	// The application restarts. Nothing in the first process survives it.
	db := open()
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	got := artifactBody(t, db, captureArtifactID)
	if got != bodyA {
		t.Fatalf("body after the restart = %q, want %q", got, bodyA)
	}
	// A's artifact holds A's body and NONE of B's. Two blocks captured back
	// to back is the case a boundary bug shows up in, and it is the one the
	// old byte-stream design spent three sections on.
	if strings.Contains(got, "FAIL") || strings.Contains(got, "entirely") {
		t.Fatalf("entry A's artifact carries entry B's output:\n%q", got)
	}
	if b := artifactBody(t, db, otherArtifactID); b != bodyB {
		t.Fatalf("entry B's body = %q, want %q", b, bodyB)
	}

	// And the entry still names it: the restore path reads the artifact
	// through the entry, so an artifact nothing points at is unreachable
	// however intact its bytes are.
	entry, err := db.Ledger().Entry(ctx, entryA)
	if err != nil || entry == nil {
		t.Fatalf("Entry(%q): %v", entryA, err)
	}
	found := false
	for _, ex := range entry.Executions {
		for _, a := range ex.Artifacts {
			if a.ID == captureArtifactID {
				found = true
				if a.CaptureMethod != content.CaptureTerminalCells || a.CaptureVersion != 1 {
					t.Fatalf("provenance after the restart = %q/%d", a.CaptureMethod, a.CaptureVersion)
				}
			}
		}
	}
	if !found {
		t.Fatal("the entry does not name its artifact after the restart")
	}
}

// artifactBody joins one artifact's chunks in seq order.
func artifactBody(t *testing.T, db content.ContentDB, id string) string {
	t.Helper()
	art, err := db.Ledger().Artifact(context.Background(), id)
	if err != nil {
		t.Fatalf("Artifact(%q): %v", id, err)
	}
	if art == nil {
		t.Fatalf("no artifact carries id %q — the capture did not survive", id)
	}
	var sb strings.Builder
	for _, c := range art.Chunks {
		sb.Write(c)
	}
	return sb.String()
}
