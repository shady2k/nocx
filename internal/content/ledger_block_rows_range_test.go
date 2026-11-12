package content_test

// A stored-row read that a reader can page (owner, 2026-11-10, nocx-t7xhe).
//
// The full artifact is what a block used to demand, and a reconnect that asks
// five hundred blocks at once puts five hundred whole bodies on one socket.
// The range read is the store's half of the answer: one page of a rows
// artifact, plus the facts a reader needs to reserve the whole block's height
// before any row arrives.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
)

func appendRows(t *testing.T, led content.LedgerRepository, entryID, artifactID string, from int, texts ...string) {
	t.Helper()
	rows := make([]emulator.Row, 0, len(texts))
	for _, text := range texts {
		rows = append(rows, aTextRow(text))
	}
	if err := led.AppendBlockRows(context.Background(), content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifactID, FromRow: uint64(from), Rows: rows, //nolint:gosec // test fixture
	}); err != nil {
		t.Fatalf("AppendBlockRows(%d): %v", from, err)
	}
}

func seedRows(t *testing.T, count int) (content.LedgerRepository, string, string) {
	t.Helper()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "seq 1 25")
	if opened, err := led.OpenBlockOutput(context.Background(), content.OpenBlockOutput{
		EntryID: entryID, ArtifactID: keptArtifact,
	}); err != nil || opened != keptArtifact {
		t.Fatalf("OpenBlockOutput = %q, %v", opened, err)
	}
	texts := make([]string, 0, count)
	for i := 0; i < count; i++ {
		texts = append(texts, fmt.Sprintf("row-%02d", i))
	}
	appendRows(t, led, entryID, keptArtifact, 0, texts...)
	if _, err := led.CloseBlockRows(context.Background(), content.CloseBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact,
	}); err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	return led, entryID, keptArtifact
}

func textsOfRange(t *testing.T, got *content.ArtifactRange) []string {
	t.Helper()
	var body strings.Builder
	for _, chunk := range got.Artifact.Chunks {
		body.Write(chunk)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSuffix(body.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var parsed struct {
			From uint64 `json:"from"`
			Row  struct {
				Text string `json:"text"`
			} `json:"row"`
		}
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("parse range line: %v", err)
		}
		out = append(out, parsed.Row.Text)
	}
	return out
}

func TestArtifactRangeReturnsOnePageWithItsCoordinates(t *testing.T) {
	led, _, artifactID := seedRows(t, 25)

	got, err := led.ArtifactRange(context.Background(), artifactID, 10, 5)
	if err != nil {
		t.Fatalf("ArtifactRange: %v", err)
	}
	if got == nil {
		t.Fatal("ArtifactRange = nil")
	}
	if got.RowCount != 25 || got.FirstRow != 0 || got.LastRow != 24 || got.EndRow != 25 {
		t.Fatalf("whole-artifact coordinates = count %d first %d last %d end %d, want 25/0/24/25",
			got.RowCount, got.FirstRow, got.LastRow, got.EndRow)
	}
	if len(got.RetainedRanges) != 1 || got.RetainedRanges[0] != (content.RowRange{From: 0, To: 25}) {
		t.Fatalf("retained ranges = %+v, want one run [0,25)", got.RetainedRanges)
	}
	if got.RangeStart != 10 || got.RangeCount != 5 {
		t.Fatalf("range = start %d count %d, want 10/5", got.RangeStart, got.RangeCount)
	}
	if !got.HasMoreBefore || !got.HasMoreAfter {
		t.Fatalf("page 10..15 of 25 claims moreBefore=%v moreAfter=%v, want both true",
			got.HasMoreBefore, got.HasMoreAfter)
	}
	want := []string{"row-10", "row-11", "row-12", "row-13", "row-14"}
	if have := textsOfRange(t, got); strings.Join(have, ",") != strings.Join(want, ",") {
		t.Fatalf("page = %v, want %v", have, want)
	}
	// The metadata is the WHOLE artifact's, not the page's: a reader drawing
	// a block needs the artifact's own byte size and state.
	full, err := led.Artifact(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	if got.Artifact.ByteLen != full.ByteLen || got.Artifact.State != full.State {
		t.Fatalf("page metadata = byteLen %d state %s, want %d/%s",
			got.Artifact.ByteLen, got.Artifact.State, full.ByteLen, full.State)
	}
}

func TestArtifactRangePagesPastTheEndAndToTheEnd(t *testing.T) {
	led, _, artifactID := seedRows(t, 25)

	got, err := led.ArtifactRange(context.Background(), artifactID, 20, 100)
	if err != nil {
		t.Fatalf("ArtifactRange: %v", err)
	}
	if got.RangeStart != 20 || got.RangeCount != 5 || got.HasMoreAfter || !got.HasMoreBefore {
		t.Fatalf("tail page = start %d count %d after %v before %v, want 20/5/false/true",
			got.RangeStart, got.RangeCount, got.HasMoreAfter, got.HasMoreBefore)
	}

	past, err := led.ArtifactRange(context.Background(), artifactID, 99, 10)
	if err != nil {
		t.Fatalf("ArtifactRange past the end: %v", err)
	}
	if past.RangeCount != 0 || past.RangeStart != 25 || !past.HasMoreBefore || past.HasMoreAfter {
		t.Fatalf("past-end page = start %d count %d before %v after %v, want 25/0/true/false",
			past.RangeStart, past.RangeCount, past.HasMoreBefore, past.HasMoreAfter)
	}
}

func TestArtifactRangeRefusesANonRowsArtifact(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	envReady(t, led, "local")
	ids := submitIntents(t, led, "a body")
	entryID := ids[0]
	execID, err := led.StartExecution(ctx, content.StartExecution{EntryID: entryID})
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	artID, err := led.AppendArtifact(ctx, content.AppendArtifact{
		EntryID: entryID, ExecutionID: &execID, ID: "aaaaaaaa-0000-7000-8000-0000000000aa",
		MediaType: content.MediaText, CaptureMethod: content.CaptureRawOutput,
	})
	if err != nil {
		t.Fatalf("AppendArtifact: %v", err)
	}
	if err := led.AppendChunk(ctx, artID, 1, []byte("hello")); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	if _, err := led.ArtifactRange(ctx, artID, 0, 10); !errors.Is(err, content.ErrArtifactNotRows) {
		t.Fatalf("ArtifactRange(non-rows) = %v, want ErrArtifactNotRows", err)
	}
}

func TestCloseStoresRowCountAndFirstRowForTheHeightReservation(t *testing.T) {
	led, entryID, artifactID := seedRows(t, 25)
	art := blockRowsArtifactOf(t, led, entryID)
	if art == nil {
		t.Fatal("no rows artifact")
	}
	if art.ID != artifactID {
		t.Fatalf("artifact = %s, want %s", art.ID, artifactID)
	}
	var payload struct {
		RowCount *uint64 `json:"rowCount"`
		FirstRow *uint64 `json:"firstRow"`
	}
	if err := json.Unmarshal([]byte(art.Payload), &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.RowCount == nil || *payload.RowCount != 25 {
		t.Fatalf("payload rowCount = %v, want 25", payload.RowCount)
	}
	// Absent is the zero fact the payload schema already defines: a block
	// that begins at row 0 carries no firstRow key.
	if payload.FirstRow != nil && *payload.FirstRow != 0 {
		t.Fatalf("payload firstRow = %v, want 0", *payload.FirstRow)
	}
}
