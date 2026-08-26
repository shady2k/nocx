package content_test

// Acceptance tests for the ask transaction (nocx-f4s5): agent.captureFrame
// ingests a frame first and mints a backend frame id; agent.ask records the
// frame reference, the question and a PENDING run in ONE transaction; run
// state is durable and on the wire; on start every non-terminal run becomes
// interrupted; an attempt is written down before the tool is invoked.
//
// Design §5, §7; ADR-0019 (one ledger), ADR-0029 (the identity vocabulary),
// AD-7 (ids are backend-minted). These tests ARE the first production-shaped
// callers of the ledger v1 write path outside the store's own tests.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	sqlite3 "github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/adiantum"

	"github.com/shady2k/nocx/internal/content"
)

// ── fixtures ─────────────────────────────────────────────────────────────

// captureOne records one live frame and returns its id.
func captureOne(t *testing.T, led content.LedgerRepository, sessionID string) string {
	t.Helper()
	ctx := context.Background()
	res, err := led.CaptureFrame(ctx, liveFrame(sessionID, "first"))
	if err != nil {
		t.Fatalf("CaptureFrame: %v", err)
	}
	if res.Replayed {
		t.Fatalf("first capture unexpectedly replayed")
	}
	if res.FrameID == "" {
		t.Fatal("CaptureFrame returned an empty frame id")
	}
	return res.FrameID
}

// liveFrame is one 2x2 live frame whose rows spell "ab" / "cd".
func liveFrame(sessionID, text string) content.CaptureFrame {
	return content.CaptureFrame{
		CaptureID: "capture-" + text,
		Client:    "test-client",
		Env:       content.Environment{ID: "local", Kind: content.EnvLocal},
		SessionID: new(sessionID),
		Cwd:       "/repo",
		Source:    content.FrameLive,
		Rows: []content.FrameRow{
			{Kind: "cells", Cells: []content.FrameCell{
				{Char: "a", Attrs: content.FrameAttrs{Bold: true}},
				{Char: "b", Attrs: content.FrameAttrs{}},
			}},
			{Kind: "cells", Cells: []content.FrameCell{
				{Char: "c", Attrs: content.FrameAttrs{}},
				{Char: "d", Attrs: content.FrameAttrs{Fg: new("red")}},
			}},
		},
		Cursor:   &content.FrameCursor{Line: 5, Col: 1},
		Identity: &content.FrameIdentity{Buffer: content.BufferIdentity{Kind: "normal"}, Cols: 2, Rows: 24, Generation: 3},
		Range:    &content.FrameRange{Start: 10, End: 12},
	}
}

func frozenFrame(sessionID string) content.CaptureFrame {
	return content.CaptureFrame{
		CaptureID:         "capture-frozen",
		Client:            "test-client",
		Env:               content.Environment{ID: "local", Kind: content.EnvLocal},
		SessionID:         new(sessionID),
		Cwd:               "/repo",
		Source:            content.FrameFrozen,
		Rows:              []content.FrameRow{{Kind: "text", Text: "line one"}, {Kind: "text", Text: "line two"}},
		SerializerVersion: new(1),
	}
}

// askOne records one ask referencing the given frames; returns the result.
var askCounter int64

func nextAskID() string {
	return fmt.Sprintf("ask-%d", atomic.AddInt64(&askCounter, 1))
}

func askOne(t *testing.T, led content.LedgerRepository, sessionID string, refs ...content.AgentReference) content.AgentAskResult {
	t.Helper()
	ctx := context.Background()
	res, err := led.SubmitAgentAsk(ctx, content.AgentAsk{
		ID:         nextAskID(),
		Client:     "test-client",
		Env:        content.Environment{ID: "local", Kind: content.EnvLocal},
		SessionID:  new(sessionID),
		Cwd:        "/repo",
		Question:   "what does this screen mean?",
		References: refs,
	})
	if err != nil {
		t.Fatalf("SubmitAgentAsk: %v", err)
	}
	if res.Replayed {
		t.Fatalf("first ask unexpectedly replayed")
	}
	if res.RunID == 0 {
		t.Fatal("SubmitAgentAsk returned run id 0")
	}
	if res.EntryID == "" {
		t.Fatal("SubmitAgentAsk returned an empty entry id")
	}
	return res
}

// askIn records one ask ANCHORED TO A PANE — the shape every block has
// (nocx-4em1z). askOne above is the same call without one, kept for the tests
// that are about the run rather than about restore.
func askIn(t *testing.T, led content.LedgerRepository, sessionID, paneID string, refs ...content.AgentReference) content.AgentAskResult {
	t.Helper()
	res, err := led.SubmitAgentAsk(context.Background(), content.AgentAsk{
		ID:         nextAskID(),
		Client:     "test-client",
		Env:        content.Environment{ID: "local", Kind: content.EnvLocal},
		SessionID:  new(sessionID),
		PaneID:     new(paneID),
		Cwd:        "/repo",
		Question:   "what does this screen mean?",
		References: refs,
	})
	if err != nil {
		t.Fatalf("SubmitAgentAsk in pane %q: %v", paneID, err)
	}
	return res
}

func fullRegion() content.FrameRegion {
	return content.FrameRegion{RowStart: 0, RowEnd: 2}
}

// ── captureFrame: the frame lands, with provenance ───────────────────────

// The frame entry, its execution and the artifact with its chunks all land
// in the ONE ledger, and the frame text reads back unchanged (ADR-0019 §6:
// derived text is an artifact with provenance, never a string in a message).
func TestCaptureFrame_LandsAsEntryWithArtifactAndProvenance(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	frameID := captureOne(t, led, "session-a")

	e, err := led.Entry(ctx, frameID)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if e == nil {
		t.Fatal("the frame entry does not exist")
	}
	if e.Kind != content.EntryFrame {
		t.Errorf("frame entry kind = %q, want %q", e.Kind, content.EntryFrame)
	}
	if e.Intent != content.FrameIntent {
		t.Errorf("frame entry intent = %q, want %q", e.Intent, content.FrameIntent)
	}
	if e.Phase != content.PhaseClosed || e.Status != content.EntrySuccess {
		t.Errorf("frame entry phase/status = %q/%q, want closed/success (a capture is a fact, complete at ingest)",
			e.Phase, e.Status)
	}
	if e.SessionID == nil || *e.SessionID != "session-a" {
		t.Errorf("frame entry session = %v, want session-a", e.SessionID)
	}
	// CRITERION 5 — a captured frame is kind='frame' AND its source is the
	// immediate subject that asked for it: the renderer's captureFrame is a
	// person selecting blocks, so a live frame lands as the person's
	// capture (SourceUser), never conflated into "author". The kind says
	// WHAT the row is; the source says WHO asked.
	if e.Source != content.SourceUser {
		t.Errorf("frame entry source = %q, want user (the ask gesture), not conflated into an author", e.Source)
	}

	if len(e.Executions) != 1 {
		t.Fatalf("frame executions = %d, want exactly one", len(e.Executions))
	}
	ex := e.Executions[0]
	if ex.State != nil {
		t.Errorf("frame execution state = %v, want nil — a capture is not a run", ex.State)
	}
	if ex.TerminationReason == nil || *ex.TerminationReason != content.TermCompleted {
		t.Errorf("frame execution termination = %v, want completed", ex.TerminationReason)
	}

	if len(ex.Artifacts) != 1 {
		t.Fatalf("frame artifacts = %d, want exactly one", len(ex.Artifacts))
	}
	a := ex.Artifacts[0]
	if a.MediaType != content.MediaVT {
		t.Errorf("frame media type = %q, want %q", a.MediaType, content.MediaVT)
	}
	if a.State != content.ArtifactSealed {
		t.Errorf("artifact state = %q, want sealed — the frame is complete at ingest", a.State)
	}
	if a.CaptureMethod != content.CaptureTerminalCells {
		t.Errorf("capture method = %q, want terminal-cells", a.CaptureMethod)
	}
	if a.TerminalCols == nil || *a.TerminalCols != 2 || a.TerminalRows == nil || *a.TerminalRows != 24 {
		t.Errorf("terminal geometry = %v x %v, want 2 x 24", a.TerminalCols, a.TerminalRows)
	}

	got, err := led.Artifact(ctx, a.ID)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	body := ""
	for _, chunk := range got.Chunks {
		body += string(chunk)
	}
	if body != "ab\ncd" {
		t.Errorf("frame text = %q, want %q (the cells' chars, rows joined)", body, "ab\ncd")
	}
	if got.ByteLen != int64(len(body)) {
		t.Errorf("byte_len = %d, want %d (logical content bytes)", got.ByteLen, len(body))
	}
	if got.ChunkCount != len(got.Chunks) {
		t.Errorf("chunk count = %d, want %d", got.ChunkCount, len(got.Chunks))
	}

	// The provenance payload records the capture identity and the cursor —
	// what a later comparison (ADR-0029) will compare against.
	var prov struct {
		RowCount int                    `json:"rowCount"`
		Cols     *int                   `json:"cols"`
		Cursor   *content.FrameCursor   `json:"cursor"`
		Identity *content.FrameIdentity `json:"identity"`
		Range    *content.FrameRange    `json:"range"`
	}
	if err := json.Unmarshal([]byte(got.Payload), &prov); err != nil {
		t.Fatalf("decode frame payload: %v", err)
	}
	if prov.RowCount != 2 || prov.Cols == nil || *prov.Cols != 2 {
		t.Errorf("provenance rowCount/cols = %d/%v, want 2/2", prov.RowCount, prov.Cols)
	}
	if prov.Cursor == nil || prov.Cursor.Line != 5 || prov.Cursor.Col != 1 {
		t.Errorf("provenance cursor = %+v, want line 5 col 1", prov.Cursor)
	}
	if prov.Identity == nil || prov.Identity.Generation != 3 {
		t.Errorf("provenance identity = %+v, want generation 3", prov.Identity)
	}
	if prov.Range == nil || prov.Range.Start != 10 || prov.Range.End != 12 {
		t.Errorf("provenance range = %+v, want [10,12)", prov.Range)
	}
}

// A frozen frame is TEXT rows, records source=frozen and the serializer
// version, and has no cursor — the two sources are never substituted.
func TestCaptureFrame_FrozenSourceRecordsItsOwnProvenance(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	res, err := led.CaptureFrame(ctx, frozenFrame("session-a"))
	if err != nil {
		t.Fatalf("CaptureFrame: %v", err)
	}
	e, err := led.Entry(ctx, res.FrameID)
	if err != nil || e == nil {
		t.Fatalf("Entry: %v (err %v)", e, err)
	}
	if len(e.Executions) != 1 || len(e.Executions[0].Artifacts) != 1 {
		t.Fatalf("expected one execution with one artifact")
	}
	a := e.Executions[0].Artifacts[0]
	if a.MediaType != content.MediaText {
		t.Errorf("frozen media type = %q, want text/plain", a.MediaType)
	}
	if a.CaptureMethod != content.CaptureSerializedHTML {
		t.Errorf("frozen capture method = %q, want serialized-html", a.CaptureMethod)
	}
	if a.CaptureVersion != 1 {
		t.Errorf("capture_version = %d, want 1 (the serializer version)", a.CaptureVersion)
	}
	got, err := led.Artifact(ctx, a.ID)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	body := ""
	for _, chunk := range got.Chunks {
		body += string(chunk)
	}
	if body != "line one\nline two" {
		t.Errorf("frozen text = %q, want the text rows joined", body)
	}
	var prov struct {
		Cols   *int                 `json:"cols"`
		Cursor *content.FrameCursor `json:"cursor"`
	}
	if err := json.Unmarshal([]byte(got.Payload), &prov); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if prov.Cols != nil {
		t.Errorf("frozen provenance cols = %v, want null (a row range is full-width)", prov.Cols)
	}
	if prov.Cursor != nil {
		t.Errorf("frozen provenance cursor = %+v, want null", prov.Cursor)
	}
}

// CRITERION 5 — the frame's source is the immediate asker: a person's
// capture is user; a readScreen capture is assistant (the renderer's read
// tool is a different producer whose durable frames stamp SourceAssistant).
// The two must never conflate into one "author".
func TestLedgerFrame_ReadScreenSourceIsTheAssistant(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	// The readScreen tool's frame: the same frozen shape (rows, no cursor)
	id, err := led.CaptureFrame(ctx, content.CaptureFrame{
		CaptureID:         "readScreen-1",
		Client:            "test-client",
		Env:               content.Environment{ID: "local", Kind: content.EnvLocal},
		SessionID:         new("session-a"),
		Cwd:               "/repo",
		Source:            content.FrameFrozen,
		Subject:           content.SourceAssistant, // the read tool's answers stamp assistant
		Rows:              []content.FrameRow{{Kind: "text", Text: "line one"}},
		SerializerVersion: new(1),
	})
	if err != nil {
		t.Fatalf("CaptureFrame: %v", err)
	}
	e, err := led.Entry(ctx, id.FrameID)
	if err != nil || e == nil {
		t.Fatalf("Entry: %v (nil=%v)", err, e == nil)
	}
	if e.Kind != content.EntryFrame {
		t.Fatalf("kind = %q, want frame", e.Kind)
	}
	if e.Source != content.SourceAssistant {
		t.Fatalf("source = %q, want assistant — readScreen's capture is the assistant's, never the person's", e.Source)
	}
}

// ── captureFrame idempotency ─────────────────────────────────────────────

// A replay of the same capture returns the original backend-minted id and
// creates nothing new; the same capture key with different content is
// refused — otherwise a lost response aliases two captures.
func TestCaptureFrame_ReplayReturnsOriginalId_ConflictRefuses(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	first := liveFrame("session-a", "first")
	res1, err := led.CaptureFrame(ctx, first)
	if err != nil {
		t.Fatalf("first CaptureFrame: %v", err)
	}

	res2, err := led.CaptureFrame(ctx, first)
	if err != nil {
		t.Fatalf("replay CaptureFrame: %v", err)
	}
	if res2.FrameID != res1.FrameID {
		t.Errorf("replay frame id = %q, want %q", res2.FrameID, res1.FrameID)
	}
	if !res2.Replayed {
		t.Error("replay did not report Replayed")
	}

	// Same capture key, different content: refused, never aliased.
	second := first
	second.Rows[0].Cells[0].Char = "z"
	if _, conflictErr := led.CaptureFrame(ctx, second); !errors.Is(conflictErr, content.ErrIDConflict) {
		t.Errorf("conflicting replay error = %v, want ErrIDConflict", conflictErr)
	}

	// Exactly one frame entry exists.
	entries, err := led.ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("entries = %d, want exactly 1 after a replay", len(entries))
	}
}

// ── ask: one transaction, identities first ───────────────────────────────

// The ask transaction records the question entry, the references edges (with
// the region) and a PENDING run in ONE atomic create — the invariant "the
// run record exists from before the ask returns until the run terminalizes"
// holds at both ends: immediately after the ask the run is durable in state
// prepared, and nothing can observe a question without its run.
func TestSubmitAgentAsk_RecordsQuestionReferencesAndPendingRun(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	frameID := captureOne(t, led, "session-a")

	res := askOne(t, led, "session-a", content.AgentReference{FrameID: frameID, Region: fullRegion()})

	// The question is an entry of kind=agent with the question as its intent.
	q, err := led.Entry(ctx, res.EntryID)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if q == nil {
		t.Fatal("the question entry does not exist")
	}
	if q.Kind != content.EntryAsk {
		t.Errorf("question kind = %q, want ask", q.Kind)
	}
	if q.Intent != "what does this screen mean?" {
		t.Errorf("question intent = %q, want the question text", q.Intent)
	}
	// Open + pending: the run is recorded but the model has not been called.
	if q.Phase != content.PhaseOpen || q.Status != content.EntryPending {
		t.Errorf("question phase/status = %q/%q, want open/pending", q.Phase, q.Status)
	}

	// The run: the backend-minted execution row, state prepared, lane agent.
	if len(q.Executions) != 1 {
		t.Fatalf("question executions = %d, want exactly one run", len(q.Executions))
	}
	run := q.Executions[0]
	if run.ID != res.RunID {
		t.Errorf("run id = %d, want %d", run.ID, res.RunID)
	}
	if run.State == nil || *run.State != content.RunPrepared {
		t.Errorf("run state = %v, want prepared", run.State)
	}
	if run.Lane == nil || *run.Lane != "agent" {
		t.Errorf("run lane = %v, want agent", run.Lane)
	}

	// One edge: the references edge (question → frame, region carried).
	// Design §5 had a second — a caused-by from an answer entry of its own —
	// and there is no such entry (nocx-4em1z) and no such relation
	// (ADR-0040): containment is a column now.
	edges, err := led.Edges(ctx, res.EntryID)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	var ref *content.Edge
	for i := range edges {
		if edges[i].Rel == content.RelReferences {
			ref = &edges[i]
		}
	}
	if ref == nil {
		t.Fatalf("edges = %+v, want a references edge", edges)
	}
	if ref.From != res.EntryID || ref.To != frameID {
		t.Errorf("references edge = %+v, want question → frame", ref)
	}
	var region content.FrameRegion
	if unmarshalErr := json.Unmarshal([]byte(ref.Payload), &region); unmarshalErr != nil {
		t.Fatalf("edge payload = %q, want the region JSON: %v", ref.Payload, unmarshalErr)
	}
	if region.RowStart != 0 || region.RowEnd != 2 {
		t.Errorf("region = %+v, want rows [0,2)", region)
	}
	// NOTHING CONTAINS THE TURN, because there is no second entry it could
	// be the answer of: a turn is ONE entry whose body is its CHILDREN
	// (nocx-4em1z as amended by ADR-0040). The tree is free for what actually
	// needs it — an action, a command, a run of prose, each drawn inside its
	// turn.
	//
	// AND THE ASK OPENS NO BODY. It used to open one text/plain artifact on
	// the run, which made the stored unit the whole answer while the drawn
	// unit was a run of prose; the run opens a `text` child per piece now, so
	// a turn that has not streamed a word carries nothing at all. An empty
	// artifact here would be the claim that something was printed.
	turn, err := led.Entry(ctx, res.EntryID)
	if err != nil || turn == nil {
		t.Fatalf("turn entry: %v (err %v)", turn, err)
	}
	if turn.Phase != content.PhaseOpen || turn.Status != content.EntryPending {
		t.Errorf("turn phase/status = %q/%q, want open/pending", turn.Phase, turn.Status)
	}
	if len(turn.Executions) != 1 {
		t.Fatalf("turn executions = %d, want the one run", len(turn.Executions))
	}
	if turn.Executions[0].State == nil {
		t.Errorf("the turn's execution has no run state — it IS the run")
	}
	if len(turn.Executions[0].Artifacts) != 0 {
		t.Fatalf("the ask opened %d artifacts, want none — the answer's body is its `text` children (ADR-0040): %+v",
			len(turn.Executions[0].Artifacts), turn.Executions[0].Artifacts)
	}
	// The paired success, so "none" is not passing because the read is
	// broken: a run of prose opened under this turn IS there, with a body.
	prose, err := led.OpenProse(ctx, res.EntryID, res.RunID)
	if err != nil {
		t.Fatalf("OpenProse under the fresh turn: %v", err)
	}
	body, err := led.Artifact(ctx, prose.ArtifactID)
	if err != nil || body == nil {
		t.Fatalf("the prose block's body: %v (nil=%v)", err, body == nil)
	}
	if body.EntryID != prose.EntryID {
		t.Errorf("the prose body belongs to %q, want the block %q", body.EntryID, prose.EntryID)
	}
}

// The ask is idempotent on (id, client, digest): a replay returns the
// ORIGINAL run id and creates no second run — the bead's "a retry duplicates
// both" is the defect this exists to prevent.
func TestSubmitAgentAsk_ReplayReturnsOriginalRun(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	frameID := captureOne(t, led, "session-a")
	ask := content.AgentAsk{
		ID:         "ask-1",
		Client:     "test-client",
		Env:        content.Environment{ID: "local", Kind: content.EnvLocal},
		SessionID:  new("session-a"),
		Cwd:        "/repo",
		Question:   "what does this screen mean?",
		References: []content.AgentReference{{FrameID: frameID, Region: fullRegion()}},
	}

	res1, err := led.SubmitAgentAsk(ctx, ask)
	if err != nil {
		t.Fatalf("first ask: %v", err)
	}
	res2, err := led.SubmitAgentAsk(ctx, ask)
	if err != nil {
		t.Fatalf("replay ask: %v", err)
	}
	if res2.RunID != res1.RunID {
		t.Errorf("replay run id = %d, want %d", res2.RunID, res1.RunID)
	}
	if res2.EntryID != res1.EntryID {
		t.Errorf("replay entry id = %q, want %q", res2.EntryID, res1.EntryID)
	}
	if !res2.Replayed {
		t.Error("replay did not report Replayed")
	}

	// Exactly one question entry and one run.
	entries, err := led.ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	var questions int
	for _, en := range entries {
		if en.Intent == "what does this screen mean?" {
			questions++
		}
	}
	if questions != 1 {
		t.Errorf("question entries = %d, want exactly 1 after a replay", questions)
	}

	// Same id, different content: refused, never aliased.
	other := ask
	other.Question = "a different question"
	if _, err := led.SubmitAgentAsk(ctx, other); !errors.Is(err, content.ErrIDConflict) {
		t.Errorf("conflicting replay error = %v, want ErrIDConflict", err)
	}
}

// The atomic create: a failing ask leaves NOTHING — no question entry, no
// run, no edges. The interval has no one-sided states.
func TestSubmitAgentAsk_AtomicCreateLeavesNothingOnFailure(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	frameID := captureOne(t, led, "session-a")

	// The frame belongs to session-a; asking from session-b is rejected —
	// "an ask naming a frame from another session is rejected" (§5).
	_, err := led.SubmitAgentAsk(ctx, content.AgentAsk{
		ID:         "ask-1",
		Client:     "test-client",
		Env:        content.Environment{ID: "local", Kind: content.EnvLocal},
		SessionID:  new("session-b"),
		Cwd:        "/repo",
		Question:   "what does this screen mean?",
		References: []content.AgentReference{{FrameID: frameID, Region: fullRegion()}},
	})
	if !errors.Is(err, content.ErrFrameSessionMismatch) {
		t.Fatalf("cross-session ask error = %v, want ErrFrameSessionMismatch", err)
	}

	// Nothing landed: no question entry, no run, no edge.
	entries, err := led.ListEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 { // just the frame
		t.Errorf("entries = %d, want exactly 1 (the frame) — the failed ask left nothing", len(entries))
	}
	edges, err := led.Edges(ctx, "ask-1")
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("edges = %d, want 0", len(edges))
	}
}

// A reference to a frame id that does not exist, or to an id that is not a
// frame, is rejected.
func TestSubmitAgentAsk_UnknownOrNonFrameReferenceRejected(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	captureOne(t, led, "session-a")
	shellID := submitIntents(t, led, "echo hi")[0]

	_, err := led.SubmitAgentAsk(ctx, content.AgentAsk{
		ID:         "ask-1",
		Client:     "test-client",
		Env:        content.Environment{ID: "local", Kind: content.EnvLocal},
		SessionID:  new("session-a"),
		Cwd:        "/repo",
		Question:   "what does this screen mean?",
		References: []content.AgentReference{{FrameID: "no-such-frame", Region: fullRegion()}},
	})
	if !errors.Is(err, content.ErrFrameNotFound) {
		t.Fatalf("unknown frame error = %v, want ErrFrameNotFound", err)
	}

	// A shell entry is not a frame.
	_, err = led.SubmitAgentAsk(ctx, content.AgentAsk{
		ID:         "ask-2",
		Client:     "test-client",
		Env:        content.Environment{ID: "local", Kind: content.EnvLocal},
		SessionID:  new("session-a"),
		Cwd:        "/repo",
		Question:   "what does this screen mean?",
		References: []content.AgentReference{{FrameID: shellID, Region: fullRegion()}},
	})
	if !errors.Is(err, content.ErrNotAFrame) {
		t.Fatalf("non-frame reference error = %v, want ErrNotAFrame", err)
	}
}

// Regions are validated against the STORED frame: rows within the frame's
// own rows, columns within its geometry; a frozen frame (no columns) takes
// row ranges only. An out-of-bounds rectangle is reachable from the renderer
// and must be refused, never truncated.
func TestSubmitAgentAsk_RegionBoundsValidatedAgainstStoredFrame(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	frameID := captureOne(t, led, "session-a") // 2 rows x 2 cols
	frozenID, err := led.CaptureFrame(ctx, frozenFrame("session-a"))
	if err != nil {
		t.Fatalf("CaptureFrame frozen: %v", err)
	}

	base := content.AgentAsk{
		ID:        "ask-1",
		Client:    "test-client",
		Env:       content.Environment{ID: "local", Kind: content.EnvLocal},
		SessionID: new("session-a"),
		Cwd:       "/repo",
		Question:  "what does this screen mean?",
	}

	cases := []struct {
		name string
		ref  content.AgentReference
		ok   bool
	}{
		{"row range over the frame end", content.AgentReference{FrameID: frameID, Region: content.FrameRegion{RowStart: 1, RowEnd: 3}}, false},
		{"negative row", content.AgentReference{FrameID: frameID, Region: content.FrameRegion{RowStart: -1, RowEnd: 1}}, false},
		{"empty row range", content.AgentReference{FrameID: frameID, Region: content.FrameRegion{RowStart: 1, RowEnd: 1}}, false},
		{"column beyond geometry", content.AgentReference{FrameID: frameID, Region: content.FrameRegion{RowStart: 0, RowEnd: 1, ColStart: new(1), ColEnd: new(3)}}, false},
		{"negative column", content.AgentReference{FrameID: frameID, Region: content.FrameRegion{RowStart: 0, RowEnd: 1, ColStart: new(-1), ColEnd: new(1)}}, false},
		{"frozen frame with a column span", content.AgentReference{FrameID: frozenID.FrameID, Region: content.FrameRegion{RowStart: 0, RowEnd: 1, ColStart: new(0), ColEnd: new(1)}}, false},
		{"frozen frame full-width row range", content.AgentReference{FrameID: frozenID.FrameID, Region: content.FrameRegion{RowStart: 0, RowEnd: 2}}, true},
		{"live frame sub-rectangle", content.AgentReference{FrameID: frameID, Region: content.FrameRegion{RowStart: 0, RowEnd: 1, ColStart: new(0), ColEnd: new(1)}}, true},
	}

	for i, tc := range cases {
		ask := base
		ask.ID = "ask-case-" + string(rune('a'+i))
		ask.References = []content.AgentReference{tc.ref}
		_, err := led.SubmitAgentAsk(ctx, ask)
		if tc.ok && err != nil {
			t.Errorf("%s: error = %v, want success", tc.name, err)
		}
		if !tc.ok && !errors.Is(err, content.ErrRegionOutOfBounds) {
			t.Errorf("%s: error = %v, want ErrRegionOutOfBounds", tc.name, err)
		}
	}
}

// ── run state: durable, terminalized by the sweep ────────────────────────

// Recovery is two lines, not a machine (design §4.2): on start, every
// non-terminal run becomes interrupted — the block says so and the user asks
// again. Nothing is retried. The run record exists from before the ask
// returns (state prepared) until the sweep terminalizes it (state
// interrupted) — both ends of the interval asserted.
func TestStartupSweep_InterruptsNonTerminalRuns(t *testing.T) {
	db, led, path := newLedgerAt(t)
	ctx := context.Background()
	frameID := captureOne(t, led, "session-a")
	res := askOne(t, led, "session-a", content.AgentReference{FrameID: frameID, Region: fullRegion()})

	// First end of the interval: immediately after the ask, the run is
	// durable and prepared — the model has not been called.
	q, err := led.Entry(ctx, res.EntryID)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if q.Executions[0].State == nil || *q.Executions[0].State != content.RunPrepared {
		t.Fatalf("run state before restart = %v, want prepared", q.Executions[0].State)
	}

	// The backend restarts: close the store and reopen the file.
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	db2, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db2.Close() }()
	led2 := db2.Ledger()

	q2, err := led2.Entry(ctx, res.EntryID)
	if err != nil {
		t.Fatalf("Entry after reopen: %v", err)
	}
	run := q2.Executions[0]
	if run.State == nil || *run.State != content.RunInterrupted {
		t.Errorf("run state after restart = %v, want interrupted", run.State)
	}
	if run.TerminationReason == nil || *run.TerminationReason != content.TermInterrupted {
		t.Errorf("termination reason after restart = %v, want interrupted", run.TerminationReason)
	}
	if run.EndedAt == nil {
		t.Error("interrupted run has no ended_at — an interrupted run has an end")
	}
	// The block says so: the entry is closed with status interrupted.
	if q2.Phase != content.PhaseClosed || q2.Status != content.EntryInterrupted {
		t.Errorf("entry phase/status after restart = %q/%q, want closed/interrupted", q2.Phase, q2.Status)
	}

	// The frame is untouched by the sweep: it was already closed and its
	// execution is not a run.
	f, err := led2.Entry(ctx, frameID)
	if err != nil {
		t.Fatalf("Entry frame: %v", err)
	}
	if f.Phase != content.PhaseClosed || f.Status != content.EntrySuccess {
		t.Errorf("frame entry after restart = %q/%q, want closed/success", f.Phase, f.Status)
	}
}

// A terminalized run is not re-interrupted by the sweep, and a shell entry
// still closes as unknown — the agent vocabulary does not leak into kinds
// that never had it.
func TestStartupSweep_LeavesTerminalRunsAndShellEntriesAlone(t *testing.T) {
	db, led, path := newLedgerAt(t)
	ctx := context.Background()
	frameID := captureOne(t, led, "session-a")
	res := askOne(t, led, "session-a", content.AgentReference{FrameID: frameID, Region: fullRegion()})

	// Terminalize the run the way the model half will: FinishExecution.
	if err := led.FinishExecution(ctx, res.RunID, content.FinishExecution{
		EndedAt:           1234,
		TerminationReason: content.TermCompleted,
		Status:            content.EntrySuccess,
	}); err != nil {
		t.Fatalf("FinishExecution: %v", err)
	}

	// A shell entry, left open.
	shellID := submitIntents(t, led, "echo hi")[0]

	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	db2, err := reopenStore(t, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db2.Close() }()
	led2 := db2.Ledger()

	q2, err := led2.Entry(ctx, res.EntryID)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if q2.Executions[0].State == nil || *q2.Executions[0].State != content.RunCompleted {
		t.Errorf("terminalized run state after restart = %v, want completed (the sweep must not touch it)", q2.Executions[0].State)
	}

	sh, err := led2.Entry(ctx, shellID)
	if err != nil {
		t.Fatalf("Entry shell: %v", err)
	}
	if sh.Status != content.EntryUnknown {
		t.Errorf("shell entry status after restart = %q, want unknown — only agent runs say interrupted", sh.Status)
	}
}

// ── FinishExecution maps the run's termination to the wire state ─────────

// The terminal set on the wire is completed|cancelled|failed|interrupted;
// the mapping from the execution's termination reason is owned here so the
// model half never invents a second one.
func TestFinishExecution_MapsTerminationToRunState(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	frameID := captureOne(t, led, "session-a")

	cases := []struct {
		reason content.TerminationReason
		want   content.RunState
	}{
		{content.TermCompleted, content.RunCompleted},
		{content.TermInterrupted, content.RunInterrupted},
		{content.TermUserKilled, content.RunCancelled},
		{content.TermFailed, content.RunFailed},
		{content.TermTimeout, content.RunFailed},
		{content.TermTransportGone, content.RunFailed},
		{content.TermAgentDeclined, content.RunFailed},
		{content.TermInactivity, content.RunFailed},
		{content.TermOutputBudget, content.RunFailed},
	}
	for i, tc := range cases {
		res := askOne(t, led, "session-a", content.AgentReference{FrameID: frameID, Region: fullRegion()})
		if err := led.FinishExecution(ctx, res.RunID, content.FinishExecution{
			EndedAt:           int64(i),
			TerminationReason: tc.reason,
			Status:            content.EntryFailure,
		}); err != nil {
			t.Fatalf("FinishExecution(%s): %v", tc.reason, err)
		}
		q, err := led.Entry(ctx, res.EntryID)
		if err != nil {
			t.Fatalf("Entry: %v", err)
		}
		if q.Executions[0].State == nil || *q.Executions[0].State != tc.want {
			t.Errorf("reason %s → state %v, want %s", tc.reason, q.Executions[0].State, tc.want)
		}
	}
}

// sessionWorkspace reads one session's workspace_id and payload through the
// keyed VFS (the seam has no session read — the ledger surface only writes
// restore keys), to prove the capture ensure never re-parents or re-marks a
// recorded session.
func sessionWorkspace(t *testing.T, path, keyHex, sessionID string) (string, string) {
	t.Helper()
	db, err := driver.Open("file:"+path+"?vfs=adiantum", func(c *sqlite3.Conn) error {
		return c.Exec("PRAGMA hexkey='" + keyHex + "'")
	})
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer func() { _ = db.Close() }()
	var ws, payload string
	if err := db.QueryRowContext(context.Background(),
		`SELECT workspace_id, payload FROM sessions WHERE id = ?`, sessionID).Scan(&ws, &payload); err != nil {
		t.Fatalf("read session workspace: %v", err)
	}
	return ws, payload
}

// A session the ledger ALREADY recorded under a real workspace keeps it —
// the capture ensure is ON CONFLICT DO NOTHING and never re-parents an
// existing restore key. The pre-cutover default workspace only ever
// receives sessions nobody recorded yet.
func TestCaptureFrame_PreservesAnExistingSessionsWorkspace(t *testing.T) {
	db, led, path := newLedgerAt(t)
	ctx := context.Background()
	keyHex := hex.EncodeToString(testKey())

	if _, err := db.Layout().CreateWorkspace(ctx, content.Workspace{ID: "workspace:real", Name: "real"},
		aTab("tab-real", "workspace:real"), aPane("pane-real", "tab-real", "/srv")); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := led.CreateSession(ctx, content.Session{ID: "session-recorded", WorkspaceID: "workspace:real"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	frame := liveFrame("session-recorded", "first")
	if _, err := led.CaptureFrame(ctx, frame); err != nil {
		t.Fatalf("CaptureFrame over a recorded session: %v", err)
	}
	if ws, payload := sessionWorkspace(t, path, keyHex, "session-recorded"); ws != "workspace:real" {
		t.Errorf("session workspace after capture = %q, want %q — the ensure re-parented a recorded session", ws, "workspace:real")
	} else if strings.Contains(payload, "ensure") {
		t.Errorf("recorded session payload = %q, want no ensure marker — the ensure re-marked someone else's session", payload)
	}

	// An UNRECORDED session lands under the default, marked as the ensure's
	// synthetic child — the documented pre-cutover shape, and the marker
	// the cutover's re-parenting migration selects on.
	if _, err := led.CaptureFrame(ctx, liveFrame("session-fresh", "fresh")); err != nil {
		t.Fatalf("CaptureFrame over a fresh session: %v", err)
	}
	ws, payload := sessionWorkspace(t, path, keyHex, "session-fresh")
	if ws != content.DefaultWorkspaceID {
		t.Errorf("fresh session workspace = %q, want %q", ws, content.DefaultWorkspaceID)
	}
	if payload != `{"ensure":"agent-capture"}` {
		t.Errorf("fresh session payload = %q, want the ensure marker", payload)
	}
}

// ── read helpers ─────────────────────────────────────────────────────────

func reopenStore(t *testing.T, path string) (content.ContentDB, error) {
	t.Helper()
	db, err := content.Open(context.Background(), content.Config{
		Path:   path,
		Key:    testKey(),
		Budget: testBudget,
		Logger: nil,
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// ── the general question (nocx-4wtlh): zero references is a legal ask ────

// ⌘Enter is the whole gesture for a question that is not about a block: the
// transaction records the question, its pending run and the answer entry in
// ONE commit with NO references edges — the model is asked the question and
// nothing else.
func TestSubmitAgentAsk_GeneralQuestionWithoutReferences(t *testing.T) {
	_, led := newLedger(t)
	res := askOne(t, led, "session-a")

	ctx := context.Background()
	edges, err := led.Edges(ctx, res.EntryID)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	for _, e := range edges {
		if e.Rel == content.RelReferences {
			t.Fatalf("general question recorded a references edge: %+v", edges)
		}
	}
	// The run still exists (the question is recorded and answerable): the
	// turn carries a prepared run and no body at all — the same shape as a
	// referenced ask, minus the edges. The deltas will land in the `text`
	// children the run opens as it writes them (ADR-0040), so there is
	// nothing to carry here yet.
	q, err := led.Entry(ctx, res.EntryID)
	if err != nil || q == nil {
		t.Fatalf("turn entry: %v (err %v)", q, err)
	}
	if len(q.Executions) != 1 || q.Executions[0].State == nil || *q.Executions[0].State != content.RunPrepared {
		t.Fatalf("turn run = %+v, want one prepared execution", q.Executions)
	}
	if len(q.Executions[0].Artifacts) != 0 {
		t.Fatalf("turn artifacts = %+v, want none — the ask opens no body", q.Executions[0].Artifacts)
	}
	// The paired success on the same store: the run's first piece of prose
	// opens under this turn perfectly well.
	if _, err := led.OpenProse(ctx, res.EntryID, res.RunID); err != nil {
		t.Fatalf("OpenProse under a general question: %v", err)
	}
}

// ── the approval moves (nocx-z9hj4 link 3) ────────────────────────────────

// The run state machine knows three non-terminal moves: prepared → streaming
// (the ask starts), streaming → awaiting_approval (the policy or the egress
// gate suspended the run BEFORE the provider was reached), and
// awaiting_approval → streaming (the person answered and the run streams
// again). The state a reconnecting renderer reads must be able to say a
// question is outstanding — a suspended run must never rest in streaming,
// indistinguishable from a run mid-answer.
func TestTransitionRun_ApprovalMoves(t *testing.T) {
	_, led := newLedger(t)
	ctx := context.Background()
	res := askOne(t, led, "session-a")

	if err := led.TransitionRun(ctx, res.RunID, content.RunStreaming); err != nil {
		t.Fatalf("prepared → streaming refused: %v", err)
	}
	if err := led.TransitionRun(ctx, res.RunID, content.RunAwaitingApproval); err != nil {
		t.Fatalf("streaming → awaiting_approval refused: %v", err)
	}
	st, err := led.RunState(ctx, res.RunID)
	if err != nil || st == nil || *st != content.RunAwaitingApproval {
		t.Fatalf("RunState = %v (err %v), want awaiting_approval", st, err)
	}
	// The resume: the person answered, the run streams again.
	if err := led.TransitionRun(ctx, res.RunID, content.RunStreaming); err != nil {
		t.Fatalf("awaiting_approval → streaming refused: %v", err)
	}
	// Terminal moves still belong to FinishAgentRun — a transition to a
	// terminal state via TransitionRun stays refused from any state.
	if err := led.TransitionRun(ctx, res.RunID, content.RunCompleted); err == nil {
		t.Fatal("awaiting_approval → completed via TransitionRun accepted — terminal moves belong to FinishAgentRun")
	}
	if err := led.FinishAgentRun(ctx, res.RunID, content.FinishAgentRun{
		State: content.RunCompleted, TerminationReason: content.TermCompleted, EndedAt: 1,
	}); err != nil {
		t.Fatalf("FinishAgentRun from awaiting_approval: %v", err)
	}
}

// prepared → awaiting_approval is NOT a move: a run that never streamed has
// nothing to suspend — the machine only suspends a streaming run.
func TestTransitionRun_SkipToApprovalRefused(t *testing.T) {
	_, led := newLedger(t)
	res := askOne(t, led, "session-a")
	if err := led.TransitionRun(context.Background(), res.RunID, content.RunAwaitingApproval); err == nil {
		t.Fatal("prepared → awaiting_approval accepted — the machine only suspends a streaming run")
	}
}

// ── one turn, one entry (nocx-4em1z) ──────────────────────────────────────

// A TURN IS A BLOCK, and a block is ONE entry: the question is its intent and
// the answer is its body, exactly as a command line and its output are.
//
// Before this it was two entries joined by a caused-by edge (assistant design
// §5), and nothing depended on the second one except its id being a routing
// address — which the turn's own id serves. Two rows for one block cost the
// restore path a fold it should never have had to do: the question entry had
// no body of its own to draw, and the answer entry had no question in its
// header.
//
// §5's stated reason is untouched by this and is asserted below: the answer is
// an ARTIFACT with provenance, not a string in a column (ADR-0019 §6).
//
// ADR-0040 amends the OTHER half. "One entry" stays true of the turn's
// IDENTITY — one row routes the deltas, carries the grant and is what a
// restore reads back — and what is dropped is "its own body is the answer".
// The turn's body is its `text` CHILDREN now, one per run of prose, so the
// ask writes one entry and NO artifact, and the bodies below are the ones the
// run opened rather than one the ask handed it.
func TestAnAskIsOneEntryWhoseBodyIsItsProseChildren(t *testing.T) {
	ctx := context.Background()
	db, led, _ := newLedgerAt(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	envReady(t, led, "local")

	before, err := led.ListEntries(ctx, 100)
	if err != nil {
		t.Fatalf("ListEntries before: %v", err)
	}
	ask := askIn(t, led, "session-1", "pane-1")

	after, err := led.ListEntries(ctx, 100)
	if err != nil {
		t.Fatalf("ListEntries after: %v", err)
	}
	if len(after)-len(before) != 1 {
		t.Fatalf("one ask wrote %d entries, want exactly 1: %+v", len(after)-len(before), after)
	}

	turn, err := led.Entry(ctx, ask.EntryID)
	if err != nil || turn == nil {
		t.Fatalf("Entry(%s) = %+v, %v", ask.EntryID, turn, err)
	}
	// The question is the entry's own intent — the header of the block a
	// person reads, not a row of its own.
	if turn.Intent != "what does this screen mean?" {
		t.Errorf("the turn's intent = %q, want the question", turn.Intent)
	}
	// The ask opened NO body. The run opens one per piece of prose; a turn
	// that has not streamed a word has printed nothing, and an empty artifact
	// would say otherwise.
	var runBodies []content.Artifact
	for _, ex := range turn.Executions {
		runBodies = append(runBodies, ex.Artifacts...)
	}
	if len(runBodies) != 0 {
		t.Fatalf("the ask left %d artifacts on the run, want none (ADR-0040): %+v", len(runBodies), runBodies)
	}

	// The paired success, and it is the assertion §5's reason survives in: a
	// run of prose is an ARTIFACT with provenance, text/plain and never
	// application/vt, which is exactly what tells a restored block to render
	// prose rather than a grid.
	prose, err := led.OpenProse(ctx, ask.EntryID, ask.RunID)
	if err != nil {
		t.Fatalf("OpenProse: %v", err)
	}
	body, err := led.Artifact(ctx, prose.ArtifactID)
	if err != nil || body == nil {
		t.Fatalf("Artifact(prose) = %+v, %v", body, err)
	}
	if body.MediaType != content.MediaText {
		t.Errorf("the prose body's media type = %q, want %q", body.MediaType, content.MediaText)
	}
	if body.ExecutionID != nil {
		t.Errorf("the prose body names execution %d — prose was printed, not attempted", *body.ExecutionID)
	}
	// And the block it belongs to is a `text` child of this turn, seated.
	kids, err := led.Caused(ctx, ask.EntryID)
	if err != nil || len(kids) != 1 {
		t.Fatalf("the turn's children = %+v (%v), want the one run of prose", kids, err)
	}
	if kids[0].EntryID != prose.EntryID || kids[0].Kind != content.EntryText || kids[0].Position != 0 {
		t.Fatalf("the prose child = %+v, want %s as text at seat 0", kids[0], prose.EntryID)
	}
	// Nothing anywhere under this turn carries a terminal body: a turn has no
	// grid and never will.
	for _, ex := range turn.Executions {
		for _, a := range ex.Artifacts {
			if a.MediaType == content.MediaVT {
				t.Errorf("a turn carries a terminal body (%q) — nothing may write one", a.ID)
			}
		}
	}
	if body.MediaType == content.MediaVT {
		t.Errorf("a run of prose carries a terminal body — nothing may write one")
	}
}

// ── the turn's duration is a fact the CLOSE holds (nocx-hoeq3) ────────────

// A restored turn draws a duration chip, and the only clock that can fill it
// is this one: the renderer measures a shell command because the backend may
// not read the stream (AD-6), but a turn's whole lifecycle is the backend's —
// the run's start is written at submit and its end arrives here. Nothing else
// can answer "how long did the assistant take".
//
// Before this the close wrote neither the entry's end nor its duration, so
// every restored turn came off the wire with `durationMs: null`. That was
// invisible while a turn drew no duration chip at all; the chip made it a
// visible "0ms" (nocx-hoeq3), which is a different fact from "unknown" and a
// wrong one — the duration WAS known and was being dropped.
func TestFinishAgentRun_RecordsTheTurnsDuration(t *testing.T) {
	ctx := context.Background()
	db, led, _ := newLedgerAt(t)
	aPaneUnder(t, db, "ws-1", "tab-1", "pane-1")
	envReady(t, led, "local")
	ask := askIn(t, led, "session-1", "pane-1")

	// The interval OPENS at submit: the run row carries the start, and it is
	// the start the close must measure from — not the moment of the close.
	open, err := led.Entry(ctx, ask.EntryID)
	if err != nil || open == nil {
		t.Fatalf("Entry(%s) = %+v, %v", ask.EntryID, open, err)
	}
	var startedAt int64
	for _, ex := range open.Executions {
		if ex.State != nil && ex.StartedAt != nil {
			startedAt = *ex.StartedAt
		}
	}
	if startedAt == 0 {
		t.Fatalf("the pending run carries no start: %+v", open.Executions)
	}

	err = led.FinishAgentRun(ctx, ask.RunID, content.FinishAgentRun{
		State:             content.RunCompleted,
		TerminationReason: content.TermCompleted,
		EndedAt:           startedAt + 1500,
	})
	if err != nil {
		t.Fatalf("FinishAgentRun: %v", err)
	}

	turn, err := led.Entry(ctx, ask.EntryID)
	if err != nil || turn == nil {
		t.Fatalf("Entry(%s) after close = %+v, %v", ask.EntryID, turn, err)
	}
	if turn.DurationMs == nil {
		t.Fatalf("the closed turn carries no duration — a restored turn can only read 'unknown' for a time the ledger held all along")
	}
	if *turn.DurationMs != 1500 {
		t.Errorf("the turn's duration = %dms, want 1500ms (end − the run's own start)", *turn.DurationMs)
	}
	// And its ends, so the row is self-consistent: a duration whose interval
	// has no start and no finish is a number nothing can be checked against.
	if turn.StartedAt == nil || *turn.StartedAt != startedAt {
		t.Errorf("the turn's start = %v, want the run's own start %d", turn.StartedAt, startedAt)
	}
	if turn.EndedAt == nil || *turn.EndedAt != startedAt+1500 {
		t.Errorf("the turn's end = %v, want %d", turn.EndedAt, startedAt+1500)
	}

	// And through the read the RESTORE actually makes: ledger.query pages
	// this statement, and a duration the entry holds but the page drops
	// would reach the renderer as the same null the bug wrote.
	page, err := led.QueryEntries(ctx, content.LedgerQuery{
		Scope: content.ScopeEverywhere, PaneID: "pane-1", Limit: 10,
	})
	if err != nil {
		t.Fatalf("QueryEntries: %v", err)
	}
	var found bool
	for _, row := range page.Entries {
		if row.ID != ask.EntryID {
			continue
		}
		found = true
		if row.DurationMs == nil || *row.DurationMs != 1500 {
			t.Errorf("the paged turn's duration = %v, want 1500ms", row.DurationMs)
		}
	}
	if !found {
		t.Fatalf("the turn is not in its own pane's page: %+v", page.Entries)
	}
}
