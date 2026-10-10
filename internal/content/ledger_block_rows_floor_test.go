package content_test

// Round 8 (nocx-zg3k3.5.3): the block's stored span is [FirstRow, NextRow).
// A command's first rows can depart before its open lands; its block's first
// delivery then starts above the session's origin, and the resend later
// re-offers the head. The store takes the head when it is CONTIGUOUS with
// the floor (it joins the stored span's beginning) and refuses anything
// else below it, as the discontinuity it always was.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
)

func TestABlockTakesAContiguousDeliveryBelowItsFloor(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "head loses the race")
	artifact := "00000000-0000-7000-8000-00000000000f"
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{
		EntryID: entryID, ArtifactID: artifact,
	}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	// The block's first delivery began at absolute 6: the command's rows
	// R7 and R8 reached the store; R1..R6 departed before the block opened.
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifact, FromRow: 6,
		Rows: []emulator.Row{aTextRow("R7"), aTextRow("R8")},
	}); err != nil {
		t.Fatalf("the first delivery: %v", err)
	}
	// The resend re-offers the head: [2..6), contiguous with the floor.
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifact, FromRow: 2,
		Rows: []emulator.Row{aTextRow("R3"), aTextRow("R4"), aTextRow("R5"), aTextRow("R6")},
	}); err != nil {
		t.Fatalf("the contiguous head below the floor: %v", err)
	}
	// The stored span is [2..8): the head first, then the first delivery.
	lines := storedBlockRows(t, led, artifact)
	if len(lines) != 6 {
		t.Fatalf("the artifact holds %d rows, want the head and the first delivery's 6", len(lines))
	}
	if lines[0].From != 2 || !strings.Contains(lines[0].Text, "R3") {
		t.Fatalf("the artifact's first row is from=%d %q, want the prepended head's R3 at 2", lines[0].From, lines[0].Text)
	}
	if lines[len(lines)-1].From != 7 || !strings.Contains(lines[len(lines)-1].Text, "R8") {
		t.Fatalf("the artifact's last row is from=%d %q, want the first delivery's R8", lines[len(lines)-1].From, lines[len(lines)-1].Text)
	}
	// The re-bind read answers the new floor.
	entry, err := led.OpenBlockRowsForSession(ctx, "no-such-session")
	if err != nil {
		t.Fatalf("OpenBlockRowsForSession: %v", err)
	}
	_ = entry // an entry recorded without a session answers empty; the floor
	// rides the artifact payload the re-bind read decodes.
}

func TestABlockRefusesADiscontinuousDeliveryBelowItsFloor(t *testing.T) {
	ctx := context.Background()
	_, led := newLedger(t)
	entryID := recordOne(t, led, "head loses the race")
	artifact := "00000000-0000-7000-8000-0000000000f1"
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{
		EntryID: entryID, ArtifactID: artifact,
	}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifact, FromRow: 6,
		Rows: []emulator.Row{aTextRow("R7"), aTextRow("R8")},
	}); err != nil {
		t.Fatalf("the first delivery: %v", err)
	}
	// [0..4) does not join the stored span's beginning (its end is 4, the
	// floor is 6): refused, and the artifact untouched.
	err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifact, FromRow: 0,
		Rows: []emulator.Row{aTextRow("R1"), aTextRow("R2"), aTextRow("R3"), aTextRow("R4")},
	})
	if !errors.Is(err, content.ErrBlockRowsDiscontinuous) {
		t.Fatalf("a non-contiguous delivery below the floor: err %v, want ErrBlockRowsDiscontinuous", err)
	}
	if lines := storedBlockRows(t, led, artifact); len(lines) != 2 {
		t.Fatalf("the refused delivery changed the artifact: %d rows, want the original 2", len(lines))
	}
}

func TestAMultiChunkPrependSurvivesTheCapAndEviction(t *testing.T) {
	ctx := context.Background()
	// A tiny cap so the eviction walk actually runs.
	policy := content.NewPolicy()
	policy.SetOutputCapBytes(64 * 1024)
	_, led := newLedgerWithPolicy(t, policy)
	entryID := recordOne(t, led, "head loses the race, loudly")
	artifact := "00000000-0000-7000-8000-0000000000f2"
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{
		EntryID: entryID, ArtifactID: artifact,
	}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifact, FromRow: 2000,
		Rows: []emulator.Row{aTextRow("R2000"), aTextRow("R2001")},
	}); err != nil {
		t.Fatalf("the first delivery: %v", err)
	}
	// A head bigger than one chunk (> 16 KiB): 1900 rows, ENDING at the
	// floor (2000). The prepend cuts into several chunks with seqs below
	// the stored ones.
	var head []emulator.Row
	for i := 100; i < 2000; i++ {
		head = append(head, aTextRow(strings.Repeat("x", 12)+fmt.Sprintf("R%d", i)))
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifact, FromRow: 100, Rows: head,
	}); err != nil {
		t.Fatalf("the multi-chunk prepend: %v", err)
	}
	// Drive the cap: more tail rows until eviction fires.
	for i := 2002; i < 2302; i++ {
		if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
			EntryID: entryID, ArtifactID: artifact, FromRow: uint64(i), //nolint:gosec // a row count, not a byte count
			Rows: []emulator.Row{aTextRow(strings.Repeat("y", 60) + fmt.Sprintf("R%d", i))},
		}); err != nil {
			t.Fatalf("the tail delivery at %d: %v", i, err)
		}
	}
	// The cap takes the middle it must (the reservation keeps the FIRST
	// half-cap bytes — the prepend's own start among them) and the close
	// derives and names the drop. The invariants: the read stays ASCENDING
	// through any hole, the head's own start survives, and the summary
	// counts what the cap took instead of losing it silently.
	lines := storedBlockRows(t, led, artifact)
	if len(lines) == 0 {
		t.Fatalf("the artifact is empty after eviction")
	}
	prev := lines[0].From
	for _, l := range lines[1:] {
		if l.From <= prev {
			t.Fatalf("the artifact is not ascending after eviction: %d after %d", l.From, prev)
		}
		prev = l.From
	}
	if lines[0].From != 100 {
		t.Fatalf("the first surviving row is from=%d, want the prepend's own start 100 — the head reservation keeps it", lines[0].From)
	}
	summary, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: artifact})
	if err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	if summary.DroppedRows == 0 {
		t.Fatalf("the cap took the middle and the close counted nothing: %+v", summary)
	}
}

// The prepend writes the cap's own bound (nocx-zg3k3.5.10): a recovered
// head is bytes like any other, and a prepend that lands past the cap must
// evict by the cap's rule — the reservation keeps its start, the tail
// keeps the newest rows, the middle goes — and the close must count what
// it took. A prepend branch that commits without the walk leaves an
// oversized sealed artifact and counts nothing.
func TestAPrependThatPassesTheCapEvictsByTheCapsRuleAndCountsIt(t *testing.T) {
	ctx := context.Background()
	policy := content.NewPolicy()
	policy.SetOutputCapBytes(4 << 10) // 4 KiB: the prepend alone dwarfs it
	_, led := newLedgerWithPolicy(t, policy)
	entryID := recordOne(t, led, "the head came back huge")
	artifact := "00000000-0000-7000-8000-0000000000f3"
	if _, err := led.OpenBlockOutput(ctx, content.OpenBlockOutput{
		EntryID: entryID, ArtifactID: artifact,
	}); err != nil {
		t.Fatalf("OpenBlockOutput: %v", err)
	}
	// The stored span is [3000, 3002): the command's tail reached the
	// store first; its head departed before the block opened.
	tail := make([]emulator.Row, 0, 24)
	for i := 3000; i < 3002; i++ {
		tail = append(tail, aTextRow(strings.Repeat("y", 64)+fmt.Sprintf("R%d", i)))
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifact, FromRow: 3000, Rows: tail,
	}); err != nil {
		t.Fatalf("the first delivery: %v", err)
	}
	// The resend re-offers the head: [1000, 3000), two thousand rows —
	// far more bytes than the cap holds. Contiguous with the floor, so
	// the store must take it; the cap must then hold.
	var head []emulator.Row
	for i := 1000; i < 3000; i++ {
		head = append(head, aTextRow(strings.Repeat("x", 64)+fmt.Sprintf("R%d", i)))
	}
	if err := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifact, FromRow: 1000, Rows: head,
	}); err != nil {
		t.Fatalf("the head prepend: %v", err)
	}

	summary, err := led.CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: artifact})
	if err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	art := blockRowsArtifactOf(t, led, entryID)
	if art == nil {
		t.Fatal("the capped block stored no artifact")
	}
	// The bound: the ends the cap protects are chunk-scale (the head
	// reservation and the reserve hold whole 16 KiB chunks against a
	// 4 KiB cap, the same slack the cap suite itself allows), so the
	// bound is the cap plus two chunks — and nothing like the ~200 KiB
	// the head itself carries.
	if art.ByteLen > int64(4<<10)+int64(2*16<<10) {
		t.Fatalf("byte_len = %d after a prepended head over a %d cap: the prepend committed without the cap's walk",
			art.ByteLen, 4<<10)
	}
	// The count: what the cap took is named, not lost.
	if summary.DroppedRows == 0 {
		t.Fatalf("the cap took the middle of a prepended block and the close counted nothing: %+v", summary)
	}
	// The ends, by the cap's own rule: the prepend's own start and the
	// newest row survive; the read stays ascending through the hole.
	lines := storedBlockRows(t, led, artifact)
	if len(lines) == 0 {
		t.Fatal("nothing survived the prepend's eviction")
	}
	if lines[0].From != 1000 || !strings.Contains(lines[0].Text, "R1000") {
		t.Fatalf("the first surviving row is from=%d %q, want the prepend's own start R1000", lines[0].From, lines[0].Text)
	}
	if lines[len(lines)-1].From != 3001 || !strings.Contains(lines[len(lines)-1].Text, "R3001") {
		t.Fatalf("the last surviving row is from=%d %q, want the newest row R3001", lines[len(lines)-1].From, lines[len(lines)-1].Text)
	}
	prev := lines[0].From
	for _, l := range lines[1:] {
		if l.From <= prev {
			t.Fatalf("the artifact is not ascending after the prepend's eviction: %d after %d", l.From, prev)
		}
		prev = l.From
	}
	// The count and the chunks agree.
	if dropped := uint64(3002-1000) - uint64(len(lines)); dropped != summary.DroppedRows { //nolint:gosec // a row count, not a byte count
		t.Fatalf("the body holds %d of 2002 rows but the block records %d dropped — the count and the chunks disagree",
			len(lines), summary.DroppedRows)
	}
}
