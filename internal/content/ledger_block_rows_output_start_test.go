package content_test

// nocx-4y82c.1.11: where a block's output begins is recorded on its artifact
// when the helper's ordered output mark applies — one idempotent write, the
// first mark winning — and it survives the seal, because a reader of the
// frozen block needs the same coordinate the open one had.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
)

func blockPayloadOutputStart(t *testing.T, led content.LedgerRepository, artifact string) *uint64 {
	t.Helper()
	art, err := led.Artifact(context.Background(), artifact)
	if err != nil || art == nil {
		t.Fatalf("read artifact %s: %v (%v)", artifact, err, art)
	}
	var payload struct {
		OutputStartRow *uint64 `json:"outputStartRow"`
	}
	if err := json.Unmarshal([]byte(art.Payload), &payload); err != nil {
		t.Fatalf("decode payload: %v (raw %s)", err, art.Payload)
	}
	return payload.OutputStartRow
}

func TestTheRecordedOutputStartIsWrittenOnceAndSurvivesTheSeal(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "printf output")
	artifact := "00000000-0000-7000-8000-0000000000fd"
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: artifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	// The echo streams first, exactly as on main.
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifact, FromRow: 0, Rows: []emulator.Row{aTextRow("$ printf output")},
	}); err != nil {
		t.Fatalf("echo delivery: %v", err)
	}
	if err := led.RecordBlockRowsOutputStart(ctx, content.RecordBlockRowsOutputStart{
		EntryID: entryID, ArtifactID: artifact, Row: 1,
	}); err != nil {
		t.Fatalf("RecordBlockRowsOutputStart: %v", err)
	}
	if got := blockPayloadOutputStart(t, led, artifact); got == nil || *got != 1 {
		t.Fatalf("recorded start = %v, want 1", got)
	}
	// The first mark wins: a later one is another command's.
	if err := led.RecordBlockRowsOutputStart(ctx, content.RecordBlockRowsOutputStart{
		EntryID: entryID, ArtifactID: artifact, Row: 7,
	}); err != nil {
		t.Fatalf("second RecordBlockRowsOutputStart: %v", err)
	}
	if got := blockPayloadOutputStart(t, led, artifact); got == nil || *got != 1 {
		t.Fatalf("recorded start after a second mark = %v, want the first, 1", got)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifact, FromRow: 1, Rows: []emulator.Row{aTextRow("output")},
	}); err != nil {
		t.Fatalf("output delivery: %v", err)
	}
	if _, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: artifact}); err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	if got := blockPayloadOutputStart(t, led, artifact); got == nil || *got != 1 {
		t.Fatalf("recorded start after the seal = %v, want 1 kept", got)
	}
	// The body read drops the echo, and keeps the output.
	art, err := led.Artifact(ctx, artifact)
	if err != nil || art == nil {
		t.Fatalf("read artifact: %v (%v)", err, art)
	}
	body, err := content.BlockRowsTextAfter(art.Chunks, content.BlockRowsOutputStart(art.Payload))
	if err != nil {
		t.Fatalf("BlockRowsTextAfter: %v", err)
	}
	if body != "output" {
		t.Fatalf("body read = %q, want the output only", body)
	}
	// A sealed block has nothing left to delimit: a mark after it is ignored.
	if err := led.RecordBlockRowsOutputStart(ctx, content.RecordBlockRowsOutputStart{
		EntryID: entryID, ArtifactID: artifact, Row: 0,
	}); err != nil {
		t.Fatalf("RecordBlockRowsOutputStart after the seal: %v", err)
	}
	if got := blockPayloadOutputStart(t, led, artifact); got == nil || *got != 1 {
		t.Fatalf("recorded start after a mark past the seal = %v, want 1", got)
	}
}

func TestTheRecordedOutputStartAppliesToASealedArtifact(t *testing.T) {
	// A seal can land before the mark reaches the coordinator (the end marker
	// and the mark travel on the rows plane, the seal on the lifecycle one).
	// Recording N is METADATA, not part of the sealed span, so it still
	// applies — idempotently, the first value winning — instead of being
	// ordered against the seal (nocx-4y82c.1.11 review 5).
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "printf output")
	artifact := "00000000-0000-7000-8000-0000000000fc"
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: artifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifact, FromRow: 0, Rows: []emulator.Row{aTextRow("$ printf output"), aTextRow("output")},
	}); err != nil {
		t.Fatalf("delivery: %v", err)
	}
	if _, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: artifact}); err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	if got := blockPayloadOutputStart(t, led, artifact); got != nil {
		t.Fatalf("recorded start before the mark = %v, want none", got)
	}
	if err := led.RecordBlockRowsOutputStart(ctx, content.RecordBlockRowsOutputStart{
		EntryID: entryID, ArtifactID: artifact, Row: 1,
	}); err != nil {
		t.Fatalf("RecordBlockRowsOutputStart after the seal: %v", err)
	}
	if got := blockPayloadOutputStart(t, led, artifact); got == nil || *got != 1 {
		t.Fatalf("recorded start after the seal = %v, want 1", got)
	}
	// A later mark does not overwrite it, sealed or not.
	if err := led.RecordBlockRowsOutputStart(ctx, content.RecordBlockRowsOutputStart{
		EntryID: entryID, ArtifactID: artifact, Row: 9,
	}); err != nil {
		t.Fatalf("second RecordBlockRowsOutputStart: %v", err)
	}
	if got := blockPayloadOutputStart(t, led, artifact); got == nil || *got != 1 {
		t.Fatalf("recorded start after a second mark = %v, want the first, 1", got)
	}
	art, err := led.Artifact(ctx, artifact)
	if err != nil || art == nil {
		t.Fatalf("read artifact: %v (%v)", err, art)
	}
	body, err := content.BlockRowsTextAfter(art.Chunks, content.BlockRowsOutputStart(art.Payload))
	if err != nil {
		t.Fatalf("BlockRowsTextAfter: %v", err)
	}
	if body != "output" {
		t.Fatalf("body read = %q, want the output only", body)
	}
}
