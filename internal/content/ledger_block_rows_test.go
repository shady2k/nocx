package content_test

// Block rows — the streamed output a command's block stores (nocx-2v80t.3.7).
//
// The keep decision moved to the command's authenticated start, so the store
// opens the block BEFORE any row exists and the three rules CaptureOutput
// applies at capture time (output retention off, a sensitive entry, a
// critical environment) now answer "may anything be written at all". A rule
// that says no must leave NOTHING — not an empty artifact, not a first
// chunk — and each refusal here is paired with an ordinary command that is
// kept, because a gate that refuses everything also writes nothing.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
)

// aTextRow builds one physical line with one cell per grapheme of text, so a
// stored block's rows can be read back and compared as the text they carry.
func aTextRow(text string) emulator.Row {
	row := emulator.Row{Cells: make([]emulator.Cell, 0, len(text))}
	for _, r := range text {
		row.Cells = append(row.Cells, emulator.Cell{
			Grapheme: string(r), Width: emulator.WidthNarrow, HasText: true,
		})
	}
	return row
}

// textOfRows reads a stored block back the way a client does: the artifact's
// chunks joined in seq order, one JSON line per row, the `from` index and the
// row's text riding together.
func textOfRows(t *testing.T, led content.LedgerRepository, artifactID string) string {
	t.Helper()
	art, err := led.Artifact(context.Background(), artifactID)
	if err != nil {
		t.Fatalf("Artifact(%s): %v", artifactID, err)
	}
	if art == nil {
		t.Fatalf("no artifact carries %s", artifactID)
	}
	var body bytes.Buffer
	for _, c := range art.Chunks {
		body.Write(c)
	}
	return body.String()
}

// storedBlockRows reads a stored block back the way a client does: one JSON
// line per row, each parsed into the frame vocabulary. The rows this test
// file builds (aTextRow) are one narrow hasText cell per grapheme with no
// wide or blank cell anywhere, so the wire's own `text` field IS the row's
// text already — nothing here needs to rejoin cells or consult `marks`,
// which is the compact shape's own point.
type storedBlockLine struct {
	From uint64 `json:"from"`
	Row  struct {
		Text string `json:"text"`
	} `json:"row"`
}

func storedBlockRows(t *testing.T, led content.LedgerRepository, artifactID string) []struct {
	From uint64
	Text string
} {
	t.Helper()
	body := textOfRows(t, led, artifactID)
	var out []struct {
		From uint64
		Text string
	}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if line == "" {
			continue
		}
		var parsed storedBlockLine
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("parse stored line: %v\nline: %s", err, line)
		}
		out = append(out, struct {
			From uint64
			Text string
		}{parsed.From, parsed.Row.Text})
	}
	return out
}

// blockRowsArtifactOf finds the block rows artifact an entry's run carries,
// or nil — through the same read a client's ledger.get makes.
func blockRowsArtifactOf(t *testing.T, led content.LedgerRepository, entryID string) *content.Artifact {
	t.Helper()
	e, err := led.Entry(context.Background(), entryID)
	if err != nil {
		t.Fatalf("Entry(%s): %v", entryID, err)
	}
	if e == nil {
		return nil
	}
	for _, ex := range e.Executions {
		for i := range ex.Artifacts {
			if ex.Artifacts[i].MediaType == content.MediaBlockRows {
				art, err := led.Artifact(context.Background(), ex.Artifacts[i].ID)
				if err != nil {
					t.Fatalf("Artifact(%s): %v", ex.Artifacts[i].ID, err)
				}
				return art
			}
		}
	}
	return nil
}

const (
	keptArtifact  = "00000000-0000-7000-8000-00000000b001"
	keptArtifact2 = "00000000-0000-7000-8000-00000000b002"
)

func TestBlockRowsText_ReadsStyledWideRows(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "printf 'A中\\nnext'")
	if opened, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{
		EntryID: entryID, ArtifactID: keptArtifact,
	}); err != nil || opened != keptArtifact {
		t.Fatalf("OpenBlockOutput = %q, err %v; want %q", opened, err, keptArtifact)
	}
	style := emulator.Style{Attributes: emulator.AttrBold}
	rows := []emulator.Row{{
		Cells: []emulator.Cell{
			{Grapheme: "A", Width: emulator.WidthNarrow, HasText: true, Style: style},
			{Grapheme: "中", Width: emulator.WidthWide, HasText: true, Style: style},
			{Width: emulator.WidthSpacerTail, Style: style},
		},
	}, aTextRow("next"), aTextRow("")}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 0, Rows: rows,
	}); err != nil {
		t.Fatalf("AppendBlockRows: %v", err)
	}
	if _, err := led.CloseBlockRows(ctx, content.CloseBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact,
	}); err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	artifact := blockRowsArtifactOf(t, led, entryID)
	if artifact == nil {
		t.Fatal("block rows artifact missing")
	}
	got, err := content.BlockRowsText(artifact.Chunks)
	if err != nil {
		t.Fatalf("BlockRowsText: %v", err)
	}
	if got != "A中\nnext\n" {
		t.Fatalf("BlockRowsText = %q, want %q", got, "A中\nnext\n")
	}
}

// THE GATE, rule by rule. Each refusal leaves no artifact of any kind on the
// entry, and each is paired with an ordinary command whose block opens.
func TestOpenBlockOutput_DecidesAtTheStart(t *testing.T) {
	t.Run("an ordinary command is kept", func(t *testing.T) {
		ctx := context.Background()
		_, led := newLedger(t)
		entryID := recordOne(t, led, "ls -la")

		artifactID, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact})
		if err != nil {
			t.Fatalf("OpenBlockOutput: %v", err)
		}
		if artifactID != keptArtifact {
			t.Fatalf("OpenBlockOutput id = %q, want the caller's id back", artifactID)
		}
		art := blockRowsArtifactOf(t, led, entryID)
		if art == nil {
			t.Fatal("a kept command's block opened no rows artifact")
		}
		if art.MediaType != content.MediaBlockRows || art.State != content.ArtifactOpen {
			t.Fatalf("opened artifact = %s/%s, want %s/open", art.MediaType, art.State, content.MediaBlockRows)
		}
		if art.CaptureMethod != content.CaptureTerminalCells {
			t.Fatalf("capture method = %q, want terminal-cells: the rows came off the emulator, not off a serialization", art.CaptureMethod)
		}
	})

	t.Run("output retention off writes nothing", func(t *testing.T) {
		ctx := context.Background()
		policy := content.NewPolicy()
		policy.SetOutputEnabled(false)
		_, led := newLedgerWithPolicy(t, policy)
		entryID := recordOne(t, led, "ls")

		artifactID, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact})
		if err != nil {
			t.Fatalf("OpenBlockOutput with output retention off: %v, want nil", err)
		}
		if artifactID != "" {
			t.Fatalf("id = %q, want the empty-id signal", artifactID)
		}
		if art := blockRowsArtifactOf(t, led, entryID); art != nil {
			t.Fatalf("a refused block left artifact %s behind — not even an empty one may be written", art.ID)
		}
		// The paired kept command, same store, toggle back on: the gate is
		// the setting, not the store being broken.
		policy.SetOutputEnabled(true)
		otherID := recordOne(t, led, "ls later")
		if artifactID, err = led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: otherID, ArtifactID: keptArtifact2}); err != nil || artifactID == "" {
			t.Fatalf("the kept pair: id=%q err=%v, want the block to open", artifactID, err)
		}
	})

	t.Run("a sensitive entry writes nothing", func(t *testing.T) {
		ctx := context.Background()
		_, led := newLedger(t)
		rec := aCompletedCommand("read the api key")
		rec.Sensitivity = content.SensitivitySensitive
		entryID, err := led.RecordCompleted(ctx, rec)
		if err != nil {
			t.Fatalf("RecordCompleted: %v", err)
		}

		artifactID, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact})
		if err != nil || artifactID != "" {
			t.Fatalf("id=%q err=%v, want the empty-id signal and no error", artifactID, err)
		}
		if art := blockRowsArtifactOf(t, led, entryID); art != nil {
			t.Fatalf("a sensitive command left artifact %s behind", art.ID)
		}
		// The paired ordinary command is kept.
		otherID := recordOne(t, led, "read the readme")
		if artifactID, err = led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: otherID, ArtifactID: keptArtifact2}); err != nil || artifactID == "" {
			t.Fatalf("the kept pair: id=%q err=%v, want the block to open", artifactID, err)
		}
	})

	t.Run("a critical environment writes nothing", func(t *testing.T) {
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
		entryID := recordOne(t, led, "kubectl apply -f prod.yaml")

		artifactID, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact})
		if err != nil || artifactID != "" {
			t.Fatalf("id=%q err=%v, want the empty-id signal and no error", artifactID, err)
		}
		if art := blockRowsArtifactOf(t, led, entryID); art != nil {
			t.Fatalf("a critical environment left artifact %s behind", art.ID)
		}
		// The command itself is still recorded — criticality decides what is
		// kept ABOUT a command, never whether it happened.
		if e, _ := led.Entry(ctx, entryID); e == nil {
			t.Fatal("the entry went with the output")
		}
		// The paired routine command is kept.
		if _, obsErr := led.RecordObservation(ctx, content.Observation{
			EnvironmentID: "local", Criticality: content.CriticalityRoutine,
		}); obsErr != nil {
			t.Fatalf("RecordObservation routine: %v", obsErr)
		}
		otherID := recordOne(t, led, "kubectl get pods")
		if artifactID, err = led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: otherID, ArtifactID: keptArtifact2}); err != nil || artifactID == "" {
			t.Fatalf("the kept pair: id=%q err=%v, want the block to open", artifactID, err)
		}
	})
}

// A re-open of the same block — the open whose ack was lost, tried again —
// finds the artifact it wrote and is not a second one.
func TestOpenBlockOutput_IsIdempotentOnTheSameBlock(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "ls")

	first, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	again, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("replayed open: %v", err)
	}
	if again != first {
		t.Fatalf("replayed open id = %q, want the first block %q", again, first)
	}
	// The same id naming a DIFFERENT entry's block is a conflict, never an
	// overwrite — the store does not rewrite one id with another object.
	otherID := recordOne(t, led, "ls elsewhere")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: otherID, ArtifactID: keptArtifact}); !errors.Is(err, content.ErrIDConflict) {
		t.Fatalf("err = %v, want ErrIDConflict", err)
	}
}

// The whole of what a command printed arrives as rows and comes back out as
// rows, in order, each carrying the absolute index it departed at.
func TestAppendBlockRows_RowsComeBackInTheOrderTheyArrived(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "printf 'a\\nb\\nc\\n'")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}

	err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 0,
		Rows: []emulator.Row{aTextRow("first screen line"), aTextRow("second")},
	})
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	err = led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 2,
		Rows: []emulator.Row{aTextRow("third")},
	})
	if err != nil {
		t.Fatalf("second append: %v", err)
	}

	rows := storedBlockRows(t, led, keptArtifact)
	if len(rows) != 3 {
		t.Fatalf("stored body holds %d rows, want 3", len(rows))
	}
	wantTexts := []string{"first screen line", "second", "third"}
	for i, want := range wantTexts {
		if rows[i].From != uint64(i) { //nolint:gosec // a row index, not a byte count
			t.Fatalf("row %d carries from=%d, want %d", i, rows[i].From, i)
		}
		if rows[i].Text != want {
			t.Fatalf("row %d reads %q, want %q", i, rows[i].Text, want)
		}
	}

	summary, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	if summary.DroppedRows != 0 || summary.LostRows != 0 {
		t.Fatalf("summary = %+v, want nothing dropped and nothing lost", summary)
	}
}

// A row index that jumps must say where the rows went. A jump with no
// accounting is a hole nobody named; a jump with LostRows is the emulator
// telling us it pruned rows, and the block carries that count.
func TestAppendBlockRows_ARowJumpMustExplainItself(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "long command")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 0,
		Rows: []emulator.Row{aTextRow("one"), aTextRow("two")},
	}); err != nil {
		t.Fatalf("first append: %v", err)
	}

	// A replay from behind the cursor is the past arriving again.
	err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 1,
		Rows: []emulator.Row{aTextRow("two again")},
	})
	if !errors.Is(err, content.ErrBlockRowsDiscontinuous) {
		t.Fatalf("replay err = %v, want ErrBlockRowsDiscontinuous", err)
	}
	// A jump the caller does not account for is a hole nobody named.
	err = led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 5,
		Rows: []emulator.Row{aTextRow("five")},
	})
	if !errors.Is(err, content.ErrBlockRowsDiscontinuous) {
		t.Fatalf("unexplained jump err = %v, want ErrBlockRowsDiscontinuous", err)
	}
	// The same jump WITH the emulator's count is accepted, and the loss is
	// carried to the block's summary.
	if err = led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 5, LostRows: 3,
		Rows: []emulator.Row{aTextRow("five")},
	}); err != nil {
		t.Fatalf("explained jump: %v", err)
	}
	summary, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	if summary.LostRows != 3 {
		t.Fatalf("summary.LostRows = %d, want 3", summary.LostRows)
	}
	// And the two verdicts stay distinct: the cap took nothing here, and
	// the block must not report the emulator's loss as the cap's doing.
	if summary.DroppedRows != 0 {
		t.Fatalf("summary.DroppedRows = %d, want 0 — loss is not eviction", summary.DroppedRows)
	}
	art := blockRowsArtifactOf(t, led, entryID)
	if art.Truncated != nil {
		t.Fatalf("Truncated = %q, want nil — loss is not a cap verdict", *art.Truncated)
	}
}

// A CONTINUOUS append — FromRow already equal to the block's own NextRow —
// takes no continuity check on LostRows at all (the discontinuity switch
// only fires for FromRow < NextRow or FromRow > NextRow), so a caller can
// claim more lost rows than the block's own span can ever explain without
// its FromRow ever jumping. Closing such a block used to compute its
// DroppedRows as an UNSIGNED span-minus-lost-minus-stored that underflowed
// — roughly 2^64 rows reported missing for a block that stored every row it
// was ever given (stage review nocx-2v80t.3.15, finding 10). The fix
// reports zero rather than a wrapped lie whenever the chunks hold more than
// the span-minus-loss arithmetic can account for.
func TestCloseBlockRows_AnOverclaimedLossNeverUnderflowsTheDropCount(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "long command")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 0,
		Rows: []emulator.Row{aTextRow("one"), aTextRow("two")},
	}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	// FromRow(2) equals the block's own NextRow(2): a perfectly continuous
	// append, and LostRows here claims ten rows that this append's own
	// FromRow does not name a gap for.
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 2, LostRows: 10,
		Rows: []emulator.Row{aTextRow("three")},
	}); err != nil {
		t.Fatalf("continuous append with an overclaimed loss: %v", err)
	}
	summary, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	if summary.DroppedRows != 0 {
		t.Fatalf("summary.DroppedRows = %d, want 0 — every row this block was given is stored, "+
			"so nothing about ITS OWN arithmetic explains a drop (an unsigned underflow used to report ~2^64)",
			summary.DroppedRows)
	}
	kept := storedBlockRows(t, led, keptArtifact)
	if len(kept) != 3 || kept[0].Text != "one" || kept[1].Text != "two" || kept[2].Text != "three" {
		t.Fatalf("the stored rows = %+v, want one, two, three — every row this test appended", kept)
	}
}

// THE CAP, both ends. The per-command bound drops the middle of a long block
// — the invocation in the head, the errors in the tail — and the block
// records how many rows the cap took. A small command is stored whole.
func TestAppendBlockRows_TheCapDropsTheMiddleAndCountsIt(t *testing.T) {
	ctx := context.Background()
	policy := content.NewPolicy()
	policy.SetOutputCapBytes(4 << 10) // 4 KiB: a few screens at most
	_, led := newLedgerWithPolicy(t, policy)
	entryID := recordOne(t, led, "yes | head")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}

	const total = 400
	rows := make([]emulator.Row, 0, total)
	for i := 0; i < total; i++ {
		rows = append(rows, aTextRow(strings.Repeat("x", 64)+fmt.Sprintf(" row %04d", i)))
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 0, Rows: rows,
	}); err != nil {
		t.Fatalf("append %d rows: %v", total, err)
	}

	summary, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	if summary.DroppedRows == 0 || summary.DroppedRows >= total {
		t.Fatalf("summary.DroppedRows = %d, want the cap to have taken some but not all of %d", summary.DroppedRows, total)
	}

	art := blockRowsArtifactOf(t, led, entryID)
	if art == nil {
		t.Fatal("the capped block stored no artifact")
	}
	if art.Truncated == nil || *art.Truncated != content.TruncCap {
		t.Fatalf("Truncated = %v, want cap", art.Truncated)
	}
	kept := storedBlockRows(t, led, keptArtifact)
	// The head survives: the invocation's first row is still the first.
	if len(kept) == 0 || kept[0].From != 0 || !strings.Contains(kept[0].Text, "row 0000") {
		t.Fatalf("the cap took the head — %d rows survive and the first is %+v", len(kept), kept)
	}
	// The tail survives: the last row is still the last.
	last := fmt.Sprintf("row %04d", total-1)
	if !strings.Contains(kept[len(kept)-1].Text, last) {
		t.Fatalf("the cap took the tail — the last surviving row reads %q", kept[len(kept)-1].Text)
	}
	// The count and the chunks agree: the rows missing from the body are
	// exactly the rows the block says the cap took.
	if dropped := total - len(kept); uint64(dropped) != summary.DroppedRows { //nolint:gosec // a row count, not a byte count
		t.Fatalf("the body is short %d rows but the block records %d — the count and the chunks disagree", dropped, summary.DroppedRows)
	}
	// The middle is what is gone: the survivors are the head run and the
	// tail run, and a row from between them is in neither.
	for _, r := range kept {
		if strings.Contains(r.Text, "row 0100") {
			t.Fatal("a row the middle should have lost is still stored")
		}
	}
}

// The cap holds ACROSS appends: a second delivery landing after the first
// already evicted must not let the walk's arithmetic mistake a tail chunk
// for head — the ends are roles of surviving seq order, and this is the
// test that fails if they ever stop being derivable from the survivors.
func TestAppendBlockRows_TheCapHoldsAcrossAppends(t *testing.T) {
	ctx := context.Background()
	policy := content.NewPolicy()
	policy.SetOutputCapBytes(4 << 10)
	_, led := newLedgerWithPolicy(t, policy)
	entryID := recordOne(t, led, "two deliveries")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}

	batch := func(from, n int) []emulator.Row {
		rows := make([]emulator.Row, 0, n)
		for i := 0; i < n; i++ {
			rows = append(rows, aTextRow(strings.Repeat("y", 64)+fmt.Sprintf(" row %04d", from+i)))
		}
		return rows
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 0, Rows: batch(0, 200),
	}); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 200, Rows: batch(200, 200),
	}); err != nil {
		t.Fatalf("second append after eviction: %v", err)
	}

	summary, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	kept := storedBlockRows(t, led, keptArtifact)
	if len(kept) == 0 {
		t.Fatal("nothing survived two capped appends")
	}
	// The ends: the block's own first row, and the newest row printed.
	if !strings.Contains(kept[0].Text, "row 0000") {
		t.Fatalf("the first surviving row reads %q, want the block's beginning", kept[0].Text)
	}
	if last := fmt.Sprintf("row %04d", 399); !strings.Contains(kept[len(kept)-1].Text, last) {
		t.Fatalf("the last surviving row reads %q, want %s", kept[len(kept)-1].Text, last)
	}
	// The bound, and the count agreeing with it.
	art := blockRowsArtifactOf(t, led, entryID)
	if art == nil || art.ByteLen > int64(4<<10)+int64(2*16<<10) {
		t.Fatalf("byte_len = %d, want the block bounded near the cap it was given", art.ByteLen)
	}
	if dropped := 400 - len(kept); uint64(dropped) != summary.DroppedRows { //nolint:gosec // a row count, not a byte count
		t.Fatalf("the body is short %d rows but the block records %d", dropped, summary.DroppedRows)
	}
}

func TestAppendBlockRows_ASmallCommandIsStoredWhole(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "echo small")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 0,
		Rows: []emulator.Row{aTextRow("the whole output")},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	summary, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	if summary.DroppedRows != 0 {
		t.Fatalf("summary.DroppedRows = %d, want 0 — the cap never acted here", summary.DroppedRows)
	}
	art := blockRowsArtifactOf(t, led, entryID)
	if art.Truncated != nil {
		t.Fatalf("Truncated = %q, want nil: a block the cap did not touch is not truncated", *art.Truncated)
	}
}

// A silent command still has a block: it opens, closes, and reads back as an
// empty body, which is a real answer and not an absence.
func TestCloseBlockRows_ACommandThatPrintedNothingStillHasABlock(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "true")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	summary, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	if summary.DroppedRows != 0 || summary.LostRows != 0 {
		t.Fatalf("summary = %+v, want zeros", summary)
	}
	if body := textOfRows(t, led, keptArtifact); body != "" {
		t.Fatalf("body = %q, want empty", body)
	}
	art := blockRowsArtifactOf(t, led, entryID)
	if art.State != content.ArtifactSealed {
		t.Fatalf("state = %q, want sealed", art.State)
	}
}

// A close is the block's one seal. A second close is the same answer, and an
// append after it has nowhere to go.
func TestCloseBlockRows_SealsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "echo once")
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: keptArtifact}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 0, Rows: []emulator.Row{aTextRow("body")},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	first, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("first close: %v", err)
	}
	again, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: keptArtifact})
	if err != nil {
		t.Fatalf("replayed close: %v", err)
	}
	if again != first {
		t.Fatalf("replayed close = %+v, want the first summary %+v", again, first)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 1, Rows: []emulator.Row{aTextRow("late")},
	}); !errors.Is(err, content.ErrBlockNotOpen) {
		t.Fatalf("append after close err = %v, want ErrBlockNotOpen", err)
	}
}

// Every id is untrusted, and an append against a block that was never opened
// on this entry is refused rather than conjured.
func TestAppendBlockRows_RefusesAnUnknownEntryAndAnUnopenedBlock(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: "00000000-0000-7000-8000-00000000dead", ArtifactID: keptArtifact, FromRow: 0,
		Rows: []emulator.Row{aTextRow("x")},
	}); !errors.Is(err, content.ErrNoSuchEntry) {
		t.Fatalf("unknown entry err = %v, want ErrNoSuchEntry", err)
	}
	entryID := recordOne(t, led, "echo hi")
	err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: keptArtifact, FromRow: 0, Rows: []emulator.Row{aTextRow("x")},
	})
	if !errors.Is(err, content.ErrBlockNotOpen) {
		t.Fatalf("unopened block err = %v, want ErrBlockNotOpen", err)
	}
}
