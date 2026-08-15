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
	if res.QuestionID == "" {
		t.Fatal("SubmitAgentAsk returned an empty question id")
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
	if e.Kind != content.EntryAgent {
		t.Errorf("frame entry kind = %q, want %q", e.Kind, content.EntryAgent)
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
	q, err := led.Entry(ctx, res.QuestionID)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if q == nil {
		t.Fatal("the question entry does not exist")
	}
	if q.Kind != content.EntryAgent {
		t.Errorf("question kind = %q, want agent", q.Kind)
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

	// Two edges: the references edge (question → frame, region carried) and
	// the caused-by edge from the answer entry (design §5 — the answer is
	// an entry joined to the question, streamed in).
	edges, err := led.Edges(ctx, res.QuestionID)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	var ref, caused *content.Edge
	for i := range edges {
		switch edges[i].Rel {
		case content.RelReferences:
			ref = &edges[i]
		case content.RelCausedBy:
			caused = &edges[i]
		}
	}
	if ref == nil {
		t.Fatalf("edges = %+v, want a references edge", edges)
	}
	if ref.From != res.QuestionID || ref.To != frameID {
		t.Errorf("references edge = %+v, want question → frame", ref)
	}
	var region content.FrameRegion
	if unmarshalErr := json.Unmarshal([]byte(ref.Payload), &region); unmarshalErr != nil {
		t.Fatalf("edge payload = %q, want the region JSON: %v", ref.Payload, unmarshalErr)
	}
	if region.RowStart != 0 || region.RowEnd != 2 {
		t.Errorf("region = %+v, want rows [0,2)", region)
	}
	if caused == nil {
		t.Fatalf("edges = %+v, want a caused-by edge from the answer", edges)
	}
	if caused.To != res.QuestionID {
		t.Errorf("caused-by edge = %+v, want → the question", caused)
	}

	// The answer entry exists: an entry in the flow, with its container
	// execution (not a run) and the empty, open answer artifact.
	ans, err := led.Entry(ctx, res.AnswerEntryID)
	if err != nil || ans == nil {
		t.Fatalf("answer entry: %v (err %v)", ans, err)
	}
	if ans.Kind != content.EntryAgent || ans.Intent != content.AnswerIntent {
		t.Errorf("answer entry kind/intent = %q/%q, want agent/answer", ans.Kind, ans.Intent)
	}
	if ans.Phase != content.PhaseOpen || ans.Status != content.EntryPending {
		t.Errorf("answer entry phase/status = %q/%q, want open/pending", ans.Phase, ans.Status)
	}
	if len(ans.Executions) != 1 {
		t.Fatalf("answer executions = %d, want 1", len(ans.Executions))
	}
	if ans.Executions[0].State != nil {
		t.Errorf("answer execution state = %v, want nil (not a run)", ans.Executions[0].State)
	}
	if len(ans.Executions[0].Artifacts) != 1 {
		t.Fatalf("answer artifacts = %d, want 1", len(ans.Executions[0].Artifacts))
	}
	if ans.Executions[0].Artifacts[0].ID != res.AnswerArtifactID {
		t.Errorf("answer artifact id = %q, want %q", ans.Executions[0].Artifacts[0].ID, res.AnswerArtifactID)
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
	if res2.QuestionID != res1.QuestionID {
		t.Errorf("replay question id = %q, want %q", res2.QuestionID, res1.QuestionID)
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
	q, err := led.Entry(ctx, res.QuestionID)
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

	q2, err := led2.Entry(ctx, res.QuestionID)
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

	q2, err := led2.Entry(ctx, res.QuestionID)
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
		{content.TermUserKilled, content.RunCancelled},
		{content.TermInterrupted, content.RunInterrupted},
		{content.TermFailed, content.RunFailed},
		{content.TermTimeout, content.RunFailed},
		{content.TermTransportGone, content.RunFailed},
		{content.TermAgentDeclined, content.RunFailed},
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
		q, err := led.Entry(ctx, res.QuestionID)
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
	_, led, path := newLedgerAt(t)
	ctx := context.Background()
	keyHex := hex.EncodeToString(testKey())

	if err := led.CreateWorkspace(ctx, content.Workspace{ID: "workspace:real", Name: "real"}); err != nil {
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
	edges, err := led.Edges(ctx, res.QuestionID)
	if err != nil {
		t.Fatalf("Edges: %v", err)
	}
	for _, e := range edges {
		if e.Rel == content.RelReferences {
			t.Fatalf("general question recorded a references edge: %+v", edges)
		}
	}
	// The run still exists (the question is recorded and answerable): the
	// question carries a prepared run, and the ANSWER entry carries the
	// artifact the streamed deltas will land in — the same shape as a
	// referenced ask, minus the edges.
	q, err := led.Entry(ctx, res.QuestionID)
	if err != nil || q == nil {
		t.Fatalf("question entry: %v (err %v)", q, err)
	}
	if len(q.Executions) != 1 || q.Executions[0].State == nil || *q.Executions[0].State != content.RunPrepared {
		t.Fatalf("question run = %+v, want one prepared execution", q.Executions)
	}
	ans, err := led.Entry(ctx, res.AnswerEntryID)
	if err != nil || ans == nil {
		t.Fatalf("answer entry: %v (err %v)", ans, err)
	}
	if len(ans.Executions) != 1 || len(ans.Executions[0].Artifacts) != 1 {
		t.Fatalf("answer executions = %+v, want one artifact", ans.Executions)
	}
}
