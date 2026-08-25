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

// Output retention off: the command keeps its row and keeps no body, and the
// call SUCCEEDS. An error here would surface in front of a person who turned
// the setting off on purpose.
func TestCaptureOutput_StoresNothingWhenOutputRetentionIsOff(t *testing.T) {
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

	in := aCapture(recordOne(t, led, "ls"), "00000000-0000-7000-8000-0000000000a6")
	stored, captureErr := led.CaptureOutput(ctx, in)
	if captureErr != nil {
		t.Fatalf("CaptureOutput with output retention off: %v, want nil", captureErr)
	}
	if stored {
		t.Fatal("stored = true while output retention is off")
	}
	art, err := led.Artifact(ctx, in.ArtifactID)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if art != nil {
		t.Fatal("an artifact was stored while output retention is off")
	}
}

// A sensitive entry keeps no body either, by the same shape. Nothing sets
// that column today — RecordCompleted defaults every entry to normal — so
// this check is currently unreachable through the product, and it is written
// now because the alternative is remembering it on the day sensitivity
// becomes settable.
func TestCaptureOutput_StoresNothingForASensitiveEntry(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)

	rec := aCompletedCommand("aws configure")
	rec.Sensitivity = content.SensitivitySensitive
	entryID, err := led.RecordCompleted(ctx, rec)
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}

	in := aCapture(entryID, "00000000-0000-7000-8000-0000000000a7")
	stored, captureErr := led.CaptureOutput(ctx, in)
	if captureErr != nil {
		t.Fatalf("CaptureOutput for a sensitive entry: %v, want nil", captureErr)
	}
	if stored {
		t.Fatal("stored = true for a sensitive entry")
	}
	art, _ := led.Artifact(ctx, in.ArtifactID)
	if art != nil {
		t.Fatal("a sensitive command's output was stored")
	}
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
func TestCaptureOutput_StoresNothingForACriticalEnvironment(t *testing.T) {
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
	stored, err := led.CaptureOutput(ctx, in)
	if err != nil {
		t.Fatalf("CaptureOutput in a critical environment: %v, want nil", err)
	}
	if stored {
		t.Fatal("stored = true in a critical environment")
	}
	art, _ := led.Artifact(ctx, in.ArtifactID)
	if art != nil {
		t.Fatal("a critical environment's output was stored")
	}
	// The command itself is still recorded: criticality decides what is kept
	// ABOUT a command, never whether it happened.
	if e, _ := led.Entry(ctx, entryID); e == nil {
		t.Fatal("the entry went with the output")
	}
}
