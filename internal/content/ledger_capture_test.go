package content_test

// CaptureOutput — the ONE write path for a frozen block's body (nocx-2f0f,
// design §4).
//
// Two properties carry this file. The first is that a capture is IDEMPOTENT:
// the renderer mints the artifact id and the socket drops, so the same body
// arrives twice and must be stored once. The second is that refusing to store
// is not an error — output retention off, or an entry marked sensitive, is a
// block that keeps its row and keeps no body, exactly the shape
// RecordCompleted uses for history.enabled.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
)

const capturedBody = "\x1b[31mred\x1b[0m\nplain"

// aCapture is THE factory for a capture in this package's tests, for the
// reason aCompletedCommand is one: a struct literal in a test keeps compiling
// when the type gains a required field, and goes on asserting a shape the
// product no longer writes.
func aCapture(entryID, artifactID string) content.CaptureOutput {
	cols, rows := 80, 24
	return content.CaptureOutput{
		EntryID:        entryID,
		ArtifactID:     artifactID,
		MediaType:      content.MediaVT,
		CaptureMethod:  content.CaptureTerminalCells,
		CaptureVersion: 1,
		TerminalCols:   &cols,
		TerminalRows:   &rows,
		Seq:            1,
		Body:           []byte(capturedBody),
	}
}

func recordOne(t *testing.T, led content.LedgerRepository, intent string) string {
	t.Helper()
	id, err := led.RecordCompleted(context.Background(), aCompletedCommand(intent))
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	return id
}

func bodyOf(t *testing.T, led content.LedgerRepository, artifactID string) string {
	t.Helper()
	art, err := led.Artifact(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("Artifact(%q): %v", artifactID, err)
	}
	if art == nil {
		t.Fatalf("Artifact(%q) is nil — the capture stored nothing", artifactID)
	}
	var sb strings.Builder
	for _, c := range art.Chunks {
		sb.Write(c)
	}
	return sb.String()
}

// The headline: a body arrives and the block has one, with the provenance the
// capture was taken under.
func TestCaptureOutput_StoresTheBodyWithItsProvenance(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "ls -la")
	in := aCapture(entryID, "00000000-0000-7000-8000-0000000000a1")

	if _, err := led.CaptureOutput(ctx, in); err != nil {
		t.Fatalf("CaptureOutput: %v", err)
	}

	if got := bodyOf(t, led, in.ArtifactID); got != capturedBody {
		t.Fatalf("body = %q, want %q", got, capturedBody)
	}
	art, _ := led.Artifact(ctx, in.ArtifactID)
	if art.CaptureMethod != content.CaptureTerminalCells || art.CaptureVersion != 1 {
		t.Fatalf("provenance = %q/%d, want terminal-cells/1", art.CaptureMethod, art.CaptureVersion)
	}
	if art.TerminalCols == nil || *art.TerminalCols != 80 {
		t.Fatal("the terminal width the serializer saw was not recorded")
	}
	if art.ByteLen != int64(len(capturedBody)) {
		t.Fatalf("byte_len = %d, want %d", art.ByteLen, len(capturedBody))
	}
	// It BELONGS to the entry (ADR-0040) and records the entry's own
	// execution as its provenance — the one RecordCompleted wrote in the
	// same transaction as the entry. An artifact against somebody else's
	// block is a body attributed to a command that did not print it.
	entry, _ := led.Entry(ctx, entryID)
	if art.EntryID != entryID {
		t.Fatalf("artifact belongs to block %q, want %q", art.EntryID, entryID)
	}
	if len(entry.Executions) != 1 || art.ExecutionID == nil || *art.ExecutionID != entry.Executions[0].ID {
		t.Fatalf("artifact names execution %v, want the entry's own", art.ExecutionID)
	}
}

// The retry after a lost ack. This is why the artifact id is the renderer's
// and why the chunk carries its seq: without both, the second delivery of one
// body doubles it.
func TestCaptureOutput_IsIdempotentOnArtifactAndSeq(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	in := aCapture(recordOne(t, led, "ls -la"), "00000000-0000-7000-8000-0000000000a2")

	if _, err := led.CaptureOutput(ctx, in); err != nil {
		t.Fatalf("first capture: %v", err)
	}
	if _, err := led.CaptureOutput(ctx, in); err != nil {
		t.Fatalf("replayed capture: %v", err)
	}

	art, _ := led.Artifact(ctx, in.ArtifactID)
	if len(art.Chunks) != 1 {
		t.Fatalf("chunks = %d, want 1 — a replay must write nothing", len(art.Chunks))
	}
	if art.ByteLen != int64(len(capturedBody)) {
		t.Fatalf("byte_len = %d, want %d — a replay must not move it", art.ByteLen, len(capturedBody))
	}
}

// A body larger than one message arrives in pieces, and the pieces are the
// caller's to number. Out of order is the interesting case: the socket does
// not promise arrival order across calls, and the read must not depend on it.
func TestCaptureOutput_ChunksJoinInSeqOrderWhateverOrderTheyArriveIn(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "cat big.log")
	const id = "00000000-0000-7000-8000-0000000000a3"

	second := aCapture(entryID, id)
	second.Seq, second.Body = 2, []byte("second")
	if _, err := led.CaptureOutput(ctx, second); err != nil {
		t.Fatalf("capture seq 2: %v", err)
	}
	first := aCapture(entryID, id)
	first.Seq, first.Body = 1, []byte("first ")
	if _, err := led.CaptureOutput(ctx, first); err != nil {
		t.Fatalf("capture seq 1: %v", err)
	}

	if got := bodyOf(t, led, id); got != "first second" {
		t.Fatalf("body = %q, want %q", got, "first second")
	}
}

// The same id asking for something else is a conflict, never an overwrite —
// the rule every client-minted id in this store follows.
func TestCaptureOutput_RefusesTheSameIDForADifferentMediaType(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	in := aCapture(recordOne(t, led, "ls"), "00000000-0000-7000-8000-0000000000a4")
	if _, err := led.CaptureOutput(ctx, in); err != nil {
		t.Fatalf("first capture: %v", err)
	}

	other := in
	other.MediaType = content.MediaText
	other.Body = []byte("something else")
	if _, err := led.CaptureOutput(ctx, other); !errors.Is(err, content.ErrIDConflict) {
		t.Fatalf("err = %v, want ErrIDConflict", err)
	}
	if got := bodyOf(t, led, in.ArtifactID); got != capturedBody {
		t.Fatalf("body = %q — a refused capture must change nothing", got)
	}
}

// An entry nothing carries. The FK would refuse it anyway; the point is that
// it is refused by NAME, because "no such entry" and "the store is broken"
// are different answers and the transport maps them differently.
func TestCaptureOutput_RefusesAnUnknownEntry(t *testing.T) {
	_, led := newLedger(t)
	in := aCapture("00000000-0000-7000-8000-00000000dead", "00000000-0000-7000-8000-0000000000a5")
	if _, err := led.CaptureOutput(context.Background(), in); !errors.Is(err, content.ErrNoSuchEntry) {
		t.Fatalf("err = %v, want ErrNoSuchEntry", err)
	}
}

// Output retention off: the command keeps its row and keeps no BODY, and the
// call SUCCEEDS. An error here would surface in front of a person who turned
// the setting off on purpose. What the store records instead is the refusal
// itself — a zero-byte artifact whose truncated says "suppressed" — so that
// reading the capture later answers "nothing is kept" as the named state it
// is, rather than an error about an id that does not exist. The read half of
// the write answer (nocx-2v80t.2.6).
func TestCaptureOutput_RecordsTheRefusalWhenOutputRetentionIsOff(t *testing.T) {
	ctx := context.Background()
	policy := content.NewPolicy()
	policy.SetOutputEnabled(false)
	dir := t.TempDir()
	db, err := content.Open(ctx, content.Config{
		Path: dir + "/content.db", Key: testKey(), Budget: testBudget, Policy: policy,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()

	entryID := recordOne(t, led, "ls")
	in := aCapture(entryID, "00000000-0000-7000-8000-0000000000a6")
	stance, captureErr := led.CaptureOutput(ctx, in)
	if captureErr != nil {
		t.Fatalf("CaptureOutput with output retention off: %v, want nil", captureErr)
	}
	if stance != content.SessionOutputRetentionOff {
		t.Fatalf("stance = %q while output retention is off, want %q", stance, content.SessionOutputRetentionOff)
	}
	refusalMarkerOf(t, led, in, entryID)
}

// A sensitive entry keeps no body either, by the same shape — and the refusal
// is recorded the same way, so a later read names it instead of answering an
// error about an id that never was. Nothing sets that column today —
// RecordCompleted defaults every entry to normal — so the marker half of this
// check is currently unreachable through the product, and it is written now
// because the alternative is remembering it on the day sensitivity becomes
// settable.
func TestCaptureOutput_RecordsTheRefusalForASensitiveEntry(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)

	rec := aCompletedCommand("aws configure")
	rec.Sensitivity = content.SensitivitySensitive
	entryID, err := led.RecordCompleted(ctx, rec)
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}

	in := aCapture(entryID, "00000000-0000-7000-8000-0000000000a7")
	stance, captureErr := led.CaptureOutput(ctx, in)
	if captureErr != nil {
		t.Fatalf("CaptureOutput for a sensitive entry: %v, want nil", captureErr)
	}
	if stance != content.SessionOutputSensitive {
		t.Fatalf("stance = %q for a sensitive entry, want %q", stance, content.SessionOutputSensitive)
	}
	refusalMarkerOf(t, led, in, entryID)
}

// The ceiling on ONE artifact, checked inside the transaction against what
// the artifact already holds. The wire bounds a single message; this is what
// stops a caller assembling an illegal artifact out of legal chunks.
func TestCaptureOutput_RefusesAnArtifactPastTheCeiling(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "cat enormous.log")
	const id = "00000000-0000-7000-8000-0000000000a8"

	first := aCapture(entryID, id)
	first.Body = make([]byte, content.MaxArtifactBytes-10)
	if _, err := led.CaptureOutput(ctx, first); err != nil {
		t.Fatalf("a body under the ceiling was refused: %v", err)
	}

	second := aCapture(entryID, id)
	second.Seq, second.Body = 2, make([]byte, 11)
	if _, err := led.CaptureOutput(ctx, second); !errors.Is(err, content.ErrArtifactTooLarge) {
		t.Fatalf("err = %v, want ErrArtifactTooLarge", err)
	}
	art, _ := led.Artifact(ctx, id)
	if art.ByteLen != int64(content.MaxArtifactBytes-10) {
		t.Fatalf("byte_len = %d — a refused chunk must change nothing", art.ByteLen)
	}
}

// A CRITICAL environment keeps the command and not what it printed (design
// §7.4, and the epic's own acceptance). It is the third refusal that is not
// an error, and it is read from the observation the execution PINNED rather
// than from the environment's latest: what matters is what was true when the
// command ran, not what somebody marked afterwards.
func TestCaptureOutput_RecordsTheRefusalForACriticalEnvironment(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	if err := led.EnsureEnvironment(ctx, content.Environment{
		ID: "local", Kind: content.EnvLocal,
	}); err != nil {
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: "local", Criticality: content.CriticalityCritical,
	}); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}

	entryID := recordOne(t, led, "kubectl apply -f prod.yaml")
	in := aCapture(entryID, "00000000-0000-7000-8000-0000000000a9")
	stance, err := led.CaptureOutput(ctx, in)
	if err != nil {
		t.Fatalf("CaptureOutput in a critical environment: %v, want nil", err)
	}
	if stance != content.SessionOutputCritical {
		t.Fatalf("stance = %q in a critical environment, want %q", stance, content.SessionOutputCritical)
	}
	refusalMarkerOf(t, led, in, entryID)
	// The command itself is still recorded: criticality decides what is kept
	// ABOUT a command, never whether it happened.
	if e, _ := led.Entry(ctx, entryID); e == nil {
		t.Fatal("the entry went with the output")
	}
}

// AN ACTION ENTRY HAS A BODY, AND IT IS THE TOOL'S RESULT (nocx-hp8p2.13).
// ADR-0040's tree gives every block kind an artifact and drew `action` with
// none, so "show me what that call returned" had nothing to reach. The write
// is the same one a command's output takes — one path, so retention,
// sensitivity and criticality decide the same way for both — and what comes
// back out is what the answer's expansion draws.
func TestCaptureOutput_AnActionEntryKeepsItsToolResult(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	if err := led.EnsureEnvironment(ctx, content.Environment{ID: "local", Kind: content.EnvLocal}); err != nil {
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: "local", Criticality: content.CriticalityRoutine,
	}); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}
	entryID := submitAction(t, led, "00000000-0000-7000-8000-0000000000c1", "session.read", content.EffectObserve, nil)
	if _, err := led.StartExecution(ctx, content.StartExecution{
		EntryID: entryID, Attempt: 1,
	}); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}

	const result = `{"sessionId":"pane-a","text":"load 1.00"}`
	in := content.CaptureOutput{
		EntryID:    entryID,
		ArtifactID: "00000000-0000-7000-8000-0000000000b1",
		// Text the TOOL produced; nobody read it off a terminal grid.
		MediaType:      content.MediaText,
		CaptureMethod:  content.CaptureRawOutput,
		CaptureVersion: 1,
		Seq:            1,
		Body:           []byte(result),
	}
	stance, err := led.CaptureOutput(ctx, in)
	if err != nil {
		t.Fatalf("CaptureOutput: %v", err)
	}
	if stance != content.SessionOutputKept {
		t.Fatalf("stance = %q, want %q — the tool result was not stored on a store that retains output", stance, content.SessionOutputKept)
	}
	if got := bodyOf(t, led, in.ArtifactID); got != result {
		t.Fatalf("body = %q, want %q", got, result)
	}
	// And it is reachable from the ENTRY, which is the handle
	// agent.runToolCall sends: ledger.get lists the artifacts of an entry,
	// and the renderer asks for this one by media type.
	entry, err := led.Entry(ctx, entryID)
	if err != nil || entry == nil {
		t.Fatalf("Entry(%q) = %v, %v", entryID, entry, err)
	}
	art, err := led.Artifact(ctx, in.ArtifactID)
	if err != nil || art == nil {
		t.Fatalf("Artifact(%q) = %v, %v", in.ArtifactID, art, err)
	}
	if art.EntryID != entryID || art.MediaType != content.MediaText ||
		art.CaptureMethod != content.CaptureRawOutput {
		t.Fatalf("artifact = entry %q %q/%q, want the action entry's own text body",
			art.EntryID, art.MediaType, art.CaptureMethod)
	}
}

// The answer says WHICH happened — its own contract's promise ("the answer
// says which happened", ledger.go). A caller that cannot tell output-off
// from a sensitive entry from a critical environment can stop sending, but
// never says why it stopped, which is the silent success this store refuses
// to answer with. The stance is the one vocabulary the question "would
// output produced right now be kept" has; these members extend it rather
// than minting a second answer beside it.
func TestCaptureOutput_AnswerNamesTheRefusal(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	if err := led.EnsureEnvironment(ctx, content.Environment{ID: "local", Kind: content.EnvLocal}); err != nil {
		t.Fatalf("EnsureEnvironment: %v", err)
	}
	if _, err := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: "local", Criticality: content.CriticalityCritical,
	}); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}

	critical := recordOne(t, led, "kubectl apply -f prod.yaml")
	stance, err := led.CaptureOutput(ctx, aCapture(critical, "00000000-0000-7000-8000-0000000000d1"))
	if err != nil || stance != content.SessionOutputCritical {
		t.Fatalf("critical environment: stance = %q, err = %v; want %q, nil", stance, err, content.SessionOutputCritical)
	}

	sensitiveRec := aCompletedCommand("aws configure")
	sensitiveRec.Sensitivity = content.SensitivitySensitive
	sensitive, err := led.RecordCompleted(ctx, sensitiveRec)
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	stance, err = led.CaptureOutput(ctx, aCapture(sensitive, "00000000-0000-7000-8000-0000000000d2"))
	if err != nil || stance != content.SessionOutputSensitive {
		t.Fatalf("sensitive entry: stance = %q, err = %v; want %q, nil", stance, err, content.SessionOutputSensitive)
	}

	// The paired positive lives in its OWN store: the critical observation
	// above is pinned by every execution this store writes from now on, so
	// an ordinary capture needs an environment nobody marked.
	_, fresh := newLedger(t)
	ordinary := recordOne(t, fresh, "ls -la")
	in := aCapture(ordinary, "00000000-0000-7000-8000-0000000000d3")
	stance, err = fresh.CaptureOutput(ctx, in)
	if err != nil || stance != content.SessionOutputKept {
		t.Fatalf("ordinary capture: stance = %q, err = %v; want %q, nil", stance, err, content.SessionOutputKept)
	}
	if got := bodyOf(t, fresh, in.ArtifactID); got != capturedBody {
		t.Fatalf("body = %q, want %q", got, capturedBody)
	}
}

// Output retention off names ITSELF, and the paired positive lives beside
// it: the same capture on a store that retains output answers kept.
func TestCaptureOutput_AnswerNamesOutputOff(t *testing.T) {
	ctx := context.Background()
	policy := content.NewPolicy()
	policy.SetOutputEnabled(false)
	db, err := content.Open(ctx, content.Config{
		Path: t.TempDir() + "/content.db", Key: testKey(), Budget: testBudget, Policy: policy,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()

	entryID := recordOne(t, led, "ls")
	in := aCapture(entryID, "00000000-0000-7000-8000-0000000000d4")
	stance, captureErr := led.CaptureOutput(ctx, in)
	if captureErr != nil || stance != content.SessionOutputRetentionOff {
		t.Fatalf("output off: stance = %q, err = %v; want %q, nil", stance, captureErr, content.SessionOutputRetentionOff)
	}
	refusalMarkerOf(t, led, in, entryID)
}

// refusalMarkerOf is the shape every refusal records, asserted once: the
// answer is DATA, not a body — a zero-byte artifact whose truncated says
// "suppressed" (the store's own word for "capture was refused by policy"),
// carrying the real entry and execution provenance so the read that answers
// it is the read of THIS command's capture. The marker is what makes
// ledger.artifact answer "nothing is kept" instead of an error about an id
// that does not exist.
func refusalMarkerOf(t *testing.T, led content.LedgerRepository, in content.CaptureOutput, entryID string) {
	t.Helper()
	art, err := led.Artifact(context.Background(), in.ArtifactID)
	if err != nil {
		t.Fatalf("Artifact(%q): %v", in.ArtifactID, err)
	}
	if art == nil {
		t.Fatalf("the refusal recorded no marker at %q — a read of the capture would answer unknown-id, not the state", in.ArtifactID)
	}
	if art.ByteLen != 0 || len(art.Chunks) != 0 {
		t.Fatalf("the marker at %q carries a body (%d bytes, %d chunks) — a refusal keeps no body", art.ID, art.ByteLen, len(art.Chunks))
	}
	if art.Truncated == nil || *art.Truncated != content.TruncSuppressed {
		t.Fatalf("marker truncated = %v, want %q", art.Truncated, content.TruncSuppressed)
	}
	if art.CaptureMethod != content.CaptureNone {
		t.Fatalf("marker capture method = %q, want %q — nothing was captured", art.CaptureMethod, content.CaptureNone)
	}
	if art.MediaType != in.MediaType {
		t.Fatalf("marker media type = %q, want the asked-for %q", art.MediaType, in.MediaType)
	}
	if art.EntryID != entryID {
		t.Fatalf("marker belongs to block %q, want %q", art.EntryID, entryID)
	}
	entry, _ := led.Entry(context.Background(), entryID)
	if entry == nil {
		t.Fatalf("Entry(%q) is nil", entryID)
	}
	if len(entry.Executions) != 1 || art.ExecutionID == nil || *art.ExecutionID != entry.Executions[0].ID {
		t.Fatalf("marker names execution %v, want the entry's own", art.ExecutionID)
	}
}

// The marker is idempotent the way a body is: the same refused capture
// asking again (a retried ask whose first ack was lost) writes nothing the
// second time.
func TestCaptureOutput_TheRefusalMarkerIsIdempotent(t *testing.T) {
	ctx := context.Background()
	policy := content.NewPolicy()
	policy.SetOutputEnabled(false)
	db, err := content.Open(ctx, content.Config{
		Path: t.TempDir() + "/content.db", Key: testKey(), Budget: testBudget, Policy: policy,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()

	in := aCapture(recordOne(t, led, "ls"), "00000000-0000-7000-8000-0000000000e1")
	if _, capErr := led.CaptureOutput(ctx, in); capErr != nil {
		t.Fatalf("first capture: %v", capErr)
	}
	if _, replayErr := led.CaptureOutput(ctx, in); replayErr != nil {
		t.Fatalf("replayed capture: %v, want the replay to stay an answer", replayErr)
	}
	refusalMarkerOf(t, led, in, mustEntryIDOf(t, led, in.EntryID))
}

// A body can never land on a refusal marker's id. The reachable flip is a
// retried ask under a policy that changed between the tries: the first ask
// was refused (the marker), the retry arrives with retention back on. A
// marker and a body are different objects, and this store never overwrites
// one id with another — silently appending a body under truncated=suppressed
// would be a row that lies both ways.
func TestCaptureOutput_ABodyCannotLandOnARefusalMarker(t *testing.T) {
	ctx := context.Background()
	policy := content.NewPolicy()
	policy.SetOutputEnabled(false)
	db, err := content.Open(ctx, content.Config{
		Path: t.TempDir() + "/content.db", Key: testKey(), Budget: testBudget, Policy: policy,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()

	in := aCapture(recordOne(t, led, "ls"), "00000000-0000-7000-8000-0000000000e2")
	if _, capErr := led.CaptureOutput(ctx, in); capErr != nil {
		t.Fatalf("refused capture: %v", capErr)
	}
	policy.SetOutputEnabled(true)
	if _, err := led.CaptureOutput(ctx, in); !errors.Is(err, content.ErrIDConflict) {
		t.Fatalf("body onto a marker: err = %v, want ErrIDConflict", err)
	}
	refusalMarkerOf(t, led, in, mustEntryIDOf(t, led, in.EntryID))
}

// And the mirror: a body stored while retention was on stays a body when a
// later ask for the same id is refused — the marker never overwrites it, and
// the read still answers the body that exists.
func TestCaptureOutput_ARefusalNeverOverwritesAStoredBody(t *testing.T) {
	ctx := context.Background()
	policy := content.NewPolicy()
	db, err := content.Open(ctx, content.Config{
		Path: t.TempDir() + "/content.db", Key: testKey(), Budget: testBudget, Policy: policy,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()

	in := aCapture(recordOne(t, led, "ls"), "00000000-0000-7000-8000-0000000000e3")
	if _, capErr := led.CaptureOutput(ctx, in); capErr != nil {
		t.Fatalf("kept capture: %v", capErr)
	}
	policy.SetOutputEnabled(false)
	stance, err := led.CaptureOutput(ctx, in)
	if err != nil {
		t.Fatalf("refused replay: %v, want nil (a refusal, not a failure)", err)
	}
	if stance != content.SessionOutputRetentionOff {
		t.Fatalf("stance = %q, want outputOff", stance)
	}
	if got := bodyOf(t, led, in.ArtifactID); got != capturedBody {
		t.Fatalf("body = %q, want %q — the refusal must not have touched it", got, capturedBody)
	}
}

// mustEntryIDOf re-answers the entry an input names, so the marker assertion
// can check provenance without the caller threading the id twice.
func mustEntryIDOf(t *testing.T, led content.LedgerRepository, entryID string) string {
	t.Helper()
	entry, err := led.Entry(context.Background(), entryID)
	if err != nil || entry == nil {
		t.Fatalf("Entry(%q) = %v, %v", entryID, entry, err)
	}
	return entry.ID
}
