package transport

// Round 8 (nocx-zg3k3.5.3): the re-adopting stream re-binds the block's
// floor with its cursor, a resend delivery reaching below the floor is
// prepended BEFORE anything is confirmed, and the acknowledgement never
// claims a span the artifact does not hold.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
)

func TestFreshFirstAppendAboveZeroDoesNotConfirmTheMissingHead(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))
	attempt := startsACommand(t, e, pub, lane, h, 2, "printf first append")

	// The store accepts this first batch at row 6 and records [6,10), but it
	// does not hold the prefix [0,6). A helper watermark at 10 would reclaim
	// that still-owed prefix, so this append must not confirm yet.
	suffix := []emulator.Row{aStreamRow("R7"), aStreamRow("R8"), aStreamRow("R9"), aStreamRow("R10")}
	if up, confirm := e.ws.BlockRowsArrived(session.ID(sid), 6, 0, suffix, ""); confirm || up != 0 {
		t.Fatalf("first append at row 6 answered up=%d confirm=%v, want no confirmation before the head is stored", up, confirm)
	}

	// The resend's head reaches below the artifact's actual first row. It
	// prepends [0,6), then may confirm the now-contiguous [0,10) span.
	head := []emulator.Row{aStreamRow("R1"), aStreamRow("R2"), aStreamRow("R3"), aStreamRow("R4"), aStreamRow("R5"), aStreamRow("R6"), aStreamRow("R7"), aStreamRow("R8")}
	if up, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, head, ""); !confirm || up != 10 {
		t.Fatalf("completed head answered up=%d confirm=%v, want the full durable cursor 10", up, confirm)
	}

	stored := streamRows(t, db, attempt)
	if len(stored) != 10 {
		t.Fatalf("artifact holds %d rows, want contiguous [0,10): %+v", len(stored), stored)
	}
	wantFrom := uint64(0)
	for _, row := range stored {
		if row.From != wantFrom {
			t.Fatalf("stored row starts at %d, want %d", row.From, wantFrom)
		}
		wantFrom++
	}
}

func TestAResentHeadBelowTheBlockFloorPrependsBeforeItConfirms(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()
	if envErr := led.EnsureEnvironment(ctx, content.Environment{ID: "local", Kind: content.EnvLocal}); envErr != nil {
		t.Fatalf("EnsureEnvironment: %v", envErr)
	}
	if _, obsErr := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: "local", Confidence: "{}",
		Criticality: content.CriticalityRoutine, Payload: "{}",
	}); obsErr != nil {
		t.Fatalf("RecordObservation: %v", obsErr)
	}
	sid := "sess-floor-1"
	const attempt = "att-floor-1"
	colour := "#000000"
	if _, wsErr := db.Layout().CreateWorkspace(ctx, content.Workspace{ID: "ws-floor", Name: "floor", Colour: &colour, Position: 0},
		content.Tab{ID: "tab-floor", WorkspaceID: "ws-floor", Position: 0, Layout: "column"},
		content.Pane{ID: "pane-floor", TabID: "tab-floor", Kind: "local", SizeShare: 1, Cwd: "/repo"}); wsErr != nil {
		t.Fatalf("CreateWorkspace: %v", wsErr)
	}
	if sessErr := led.CreateSession(ctx, content.Session{ID: sid, WorkspaceID: "ws-floor"}); sessErr != nil {
		t.Fatalf("CreateSession: %v", sessErr)
	}
	if _, submitErr := led.Submit(ctx, content.SubmitEntry{
		ID: attempt, Client: "c1", EnvironmentID: "local", Kind: content.EntryShell,
		SessionID: &sid, Cwd: "/repo", Intent: "head loses the race",
	}); submitErr != nil {
		t.Fatalf("Submit: %v", submitErr)
	}
	if _, startErr := led.StartExecution(ctx, content.StartExecution{EntryID: attempt}); startErr != nil {
		t.Fatalf("StartExecution: %v", startErr)
	}
	artifact := "00000000-0000-7000-8000-0000000000aa"
	if _, openErr := led.OpenBlockOutput(ctx, content.OpenBlockOutput{
		EntryID: attempt, ArtifactID: artifact,
	}); openErr != nil {
		t.Fatalf("OpenBlockOutput: %v", openErr)
	}
	// The block's first delivery began at absolute 6: [6..10) held, the
	// command's R1..R6 departed before the block opened.
	rows := []emulator.Row{aStreamRow("R7"), aStreamRow("R8"), aStreamRow("R9"), aStreamRow("R10")}
	if appendErr := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: attempt, ArtifactID: artifact, FromRow: 6, Rows: rows,
	}); appendErr != nil {
		t.Fatalf("the first delivery: %v", appendErr)
	}

	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithContentDB(db))
	ws.AttachBlockRows(session.ID(sid))

	// The resend's head: [0..9) — [0..6) below the floor, [6..9) held.
	var resent []emulator.Row
	for i := 1; i <= 9; i++ {
		resent = append(resent, aStreamRow("R"+strings.Repeat("x", 0)+string(rune('0'+i))))
	}
	written, confirm := ws.BlockRowsArrived(session.ID(sid), 0, 0, resent, "")
	if !confirm {
		t.Fatalf("the head delivery was not confirmed: the mark would never cover it")
	}
	if written != 10 {
		t.Fatalf("the ack reports %d, want the artifact's cursor 10 — the whole span it now holds", written)
	}

	// The artifact holds [0..10): the prepended head first.
	art, err := led.Artifact(ctx, artifact)
	if err != nil {
		t.Fatalf("Artifact: %v", err)
	}
	var lines []struct {
		From uint64 `json:"from"`
		Row  struct {
			Text string `json:"text"`
		} `json:"row"`
	}
	var body []byte
	for _, c := range art.Chunks {
		body = append(body, c...)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		var l struct {
			From uint64 `json:"from"`
			Row  struct {
				Text string `json:"text"`
			} `json:"row"`
		}
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatalf("decode a stored line %q: %v", line, err)
		}
		lines = append(lines, l)
	}
	if len(lines) != 10 {
		t.Fatalf("the artifact holds %d rows, want the head and the first delivery's 10", len(lines))
	}
	if lines[0].From != 0 || !strings.Contains(lines[0].Row.Text, "R1") {
		t.Fatalf("the artifact's first row is from=%d %q, want the prepended head's R1 at 0", lines[0].From, lines[0].Row.Text)
	}
	if lines[6].From != 6 || !strings.Contains(lines[6].Row.Text, "R7") {
		t.Fatalf("row 6 is from=%d %q, want the first delivery's R7", lines[6].From, lines[6].Row.Text)
	}
}

func TestAResentHeadHeldUntilItReachesTheFloor(t *testing.T) {
	// Round 9 (nocx-zg3k3.5.3): the loaded R33 run — the resend's batches
	// landed INSIDE the gap: [0,32) refused against a floor of 57, [32,57)
	// prepended, and the second batch's ack leapt over the refused [0,32).
	// The chain holds the parts until they reach the floor; nothing held
	// is ever confirmed.
	ctx := context.Background()
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(dir, "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()
	if envErr := led.EnsureEnvironment(ctx, content.Environment{ID: "local", Kind: content.EnvLocal}); envErr != nil {
		t.Fatalf("EnsureEnvironment: %v", envErr)
	}
	if _, obsErr := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: "local", Confidence: "{}",
		Criticality: content.CriticalityRoutine, Payload: "{}",
	}); obsErr != nil {
		t.Fatalf("RecordObservation: %v", obsErr)
	}
	sid := "sess-floor-2"
	colour := "#000000"
	if _, wsErr := db.Layout().CreateWorkspace(ctx, content.Workspace{ID: "ws-floor2", Name: "floor2", Colour: &colour, Position: 0},
		content.Tab{ID: "tab-floor2", WorkspaceID: "ws-floor2", Position: 0, Layout: "column"},
		content.Pane{ID: "pane-floor2", TabID: "tab-floor2", Kind: "local", SizeShare: 1, Cwd: "/repo"}); wsErr != nil {
		t.Fatalf("CreateWorkspace: %v", wsErr)
	}
	if sessErr := led.CreateSession(ctx, content.Session{ID: sid, WorkspaceID: "ws-floor2"}); sessErr != nil {
		t.Fatalf("CreateSession: %v", sessErr)
	}
	const attempt = "att-floor-2"
	if _, submitErr := led.Submit(ctx, content.SubmitEntry{
		ID: attempt, Client: "c1", EnvironmentID: "local", Kind: content.EntryShell,
		SessionID: &sid, Cwd: "/repo", Intent: "batches inside the gap",
	}); submitErr != nil {
		t.Fatalf("Submit: %v", submitErr)
	}
	if _, startErr := led.StartExecution(ctx, content.StartExecution{EntryID: attempt}); startErr != nil {
		t.Fatalf("StartExecution: %v", startErr)
	}
	artifact := "00000000-0000-7000-8000-0000000000bb"
	if _, openErr := led.OpenBlockOutput(ctx, content.OpenBlockOutput{
		EntryID: attempt, ArtifactID: artifact,
	}); openErr != nil {
		t.Fatalf("OpenBlockOutput: %v", openErr)
	}
	// The block's first delivery began at absolute 6: [6..10) held.
	rows := []emulator.Row{aStreamRow("R7"), aStreamRow("R8"), aStreamRow("R9"), aStreamRow("R10")}
	if appendErr := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: attempt, ArtifactID: artifact, FromRow: 6, Rows: rows,
	}); appendErr != nil {
		t.Fatalf("the first delivery: %v", appendErr)
	}

	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithContentDB(db))
	ws.AttachBlockRows(session.ID(sid))

	// Batch 1: [0..3) — inside the gap, short of the floor. Held, never
	// confirmed: the ack must not leap over rows the artifact lacks.
	if written, confirm := ws.BlockRowsArrived(session.ID(sid), 0, 0,
		[]emulator.Row{aStreamRow("R1"), aStreamRow("R2"), aStreamRow("R3")}, ""); confirm {
		t.Fatalf("batch 1 confirmed while the chain is short of the floor (written %d) — the leap again", written)
	}
	// Batch 2: [3..6) — the chain now reaches the floor: it all joins in
	// one prepend, and the ack covers the artifact's whole held span.
	if written, confirm := ws.BlockRowsArrived(session.ID(sid), 3, 0,
		[]emulator.Row{aStreamRow("R4"), aStreamRow("R5"), aStreamRow("R6")}, ""); !confirm || written != 10 {
		t.Fatalf("batch 2 = (%d, %v), want the chain joined and the artifact's cursor 10", written, confirm)
	}

	// The artifact holds [0..10), head first.
	art, artErr := led.Artifact(ctx, artifact)
	if artErr != nil {
		t.Fatalf("Artifact: %v", artErr)
	}
	var body []byte
	for _, c := range art.Chunks {
		body = append(body, c...)
	}
	got := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(got) != 10 {
		t.Fatalf("the artifact holds %d rows, want the chain and the first delivery's 10", len(got))
	}
	if !strings.Contains(got[0], "R1") {
		t.Fatalf("the artifact's first row is %q, want the held head's R1", got[0])
	}
}

// THE BATCH THAT COMPLETES THE HEAD MAY ALSO CARRY THE TAIL (nocx-zg3k3.5.11,
// ci-linux with-secret-service on PR #255: sealed at 39 rows of 300). The
// helper's resend walks in 32-row batches, and nothing aligns them with the
// block's floor or its cursor: on that run the floor was 161 and the cursor
// 177, so the batch [160,192) held the head's last row, sixteen rows the
// artifact already had, and fifteen it did not. That batch completed the
// chain and was still dropped — "the chain is still short of the floor" —
// so the head never joined, [177,192) was never stored or confirmed and never
// offered again, and every batch after it met the store at 177 and was
// refused. The batch joins the head AND appends its new tail, and the next
// batch continues the block.
func TestAResendBatchThatCompletesTheHeadAlsoAppendsItsTail(t *testing.T) {
	ctx := context.Background()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	db, err := content.Open(ctx, content.Config{
		Path:   filepath.Join(t.TempDir(), "content.db"),
		Key:    key,
		Budget: content.Budget{RetentionBytes: 1 << 30, DiskCeilingBytes: 2 << 30, CompactionFloor: 0.8},
		Logger: log.NewSlogAdapter(nil),
	})
	if err != nil {
		t.Fatalf("content.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	led := db.Ledger()
	if envErr := led.EnsureEnvironment(ctx, content.Environment{ID: "local", Kind: content.EnvLocal}); envErr != nil {
		t.Fatalf("EnsureEnvironment: %v", envErr)
	}
	if _, obsErr := led.RecordObservation(ctx, content.Observation{
		EnvironmentID: "local", Confidence: "{}", Criticality: content.CriticalityRoutine, Payload: "{}",
	}); obsErr != nil {
		t.Fatalf("RecordObservation: %v", obsErr)
	}
	sid := "sess-floor-3"
	colour := "#000000"
	if _, wsErr := db.Layout().CreateWorkspace(ctx, content.Workspace{ID: "ws-floor3", Name: "floor3", Colour: &colour, Position: 0},
		content.Tab{ID: "tab-floor3", WorkspaceID: "ws-floor3", Position: 0, Layout: "column"},
		content.Pane{ID: "pane-floor3", TabID: "tab-floor3", Kind: "local", SizeShare: 1, Cwd: "/repo"}); wsErr != nil {
		t.Fatalf("CreateWorkspace: %v", wsErr)
	}
	if sessErr := led.CreateSession(ctx, content.Session{ID: sid, WorkspaceID: "ws-floor3"}); sessErr != nil {
		t.Fatalf("CreateSession: %v", sessErr)
	}
	const attempt = "att-floor-3"
	if _, submitErr := led.Submit(ctx, content.SubmitEntry{
		ID: attempt, Client: "c1", EnvironmentID: "local", Kind: content.EntryShell,
		SessionID: &sid, Cwd: "/repo", Intent: "the batch straddles the floor and the cursor",
	}); submitErr != nil {
		t.Fatalf("Submit: %v", submitErr)
	}
	if _, startErr := led.StartExecution(ctx, content.StartExecution{EntryID: attempt}); startErr != nil {
		t.Fatalf("StartExecution: %v", startErr)
	}
	artifact := "00000000-0000-7000-8000-0000000000cc"
	if _, openErr := led.OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: attempt, ArtifactID: artifact}); openErr != nil {
		t.Fatalf("OpenBlockOutput: %v", openErr)
	}
	row := func(i int) emulator.Row { return aStreamRow("R" + strconv.Itoa(i)) }
	span := func(from, to int) []emulator.Row {
		var out []emulator.Row
		for i := from; i < to; i++ {
			out = append(out, row(i))
		}
		return out
	}
	// The previous coordinator stored [6,10): its head departed before the
	// block opened, and it went away before the rest.
	if appendErr := led.AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: attempt, ArtifactID: artifact, FromRow: 6, Rows: span(6, 10),
	}); appendErr != nil {
		t.Fatalf("the first delivery: %v", appendErr)
	}

	ws := NewWSServer(log.NewSlogAdapter(nil), newRegWithStub(log.NewSlogAdapter(nil)), WithContentDB(db))
	ws.AttachBlockRows(session.ID(sid))

	// The resend, in batches that align with neither the floor nor the
	// cursor: [0,4) short of the floor; [4,13) completes the head, repeats
	// [6,10), and carries [10,13) the artifact lacks; then [13,16).
	if _, confirm := ws.BlockRowsArrived(session.ID(sid), 0, 0, span(0, 4), ""); confirm {
		t.Fatal("the batch short of the floor was confirmed")
	}
	if written, confirm := ws.BlockRowsArrived(session.ID(sid), 4, 0, span(4, 13), ""); !confirm || written != 13 {
		t.Fatalf("the batch that completes the head and carries the tail = (%d, %v), want (13, true)", written, confirm)
	}
	if written, confirm := ws.BlockRowsArrived(session.ID(sid), 13, 0, span(13, 16), ""); !confirm || written != 16 {
		t.Fatalf("the next batch = (%d, %v), want it to continue the block to 16", written, confirm)
	}
	art, artErr := led.Artifact(ctx, artifact)
	if artErr != nil {
		t.Fatalf("Artifact: %v", artErr)
	}
	var body []byte
	for _, c := range art.Chunks {
		body = append(body, c...)
	}
	got := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(got) != 16 {
		t.Fatalf("the artifact holds %d rows, want [0,16) whole", len(got))
	}
	for i, line := range got {
		if !strings.Contains(line, `"R`+strconv.Itoa(i)+`"`) && !strings.Contains(line, "R"+strconv.Itoa(i)) {
			t.Fatalf("row %d is %s, want R%d", i, line, i)
		}
	}
}
