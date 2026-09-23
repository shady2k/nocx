package transport

// The block rows stream's contract (nocx-2v80t.3.7): rows that leave the
// screen become the authenticated command's block, in order, and stop at the
// interval's authenticated end. What is NOT tested here is the helper's own
// half — until both halves merge there is no real source of rows, so these
// tests drive the WSServer's stream API the way the app wiring will, and the
// one criterion that says "over the real helper" waits for the merge, with
// the over-the-wire read-back test below proving the stored form end to end
// in the meantime.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
	"github.com/shady2k/nocx/internal/session"
)

func aStreamRow(text string) emulator.Row {
	row := emulator.Row{Cells: make([]emulator.Cell, 0, len(text))}
	for _, r := range text {
		row.Cells = append(row.Cells, emulator.Cell{
			Grapheme: string(r), Width: emulator.WidthNarrow, HasText: true,
		})
	}
	return row
}

func blockRowsBody(t *testing.T, db content.ContentDB, entryID string) string {
	t.Helper()
	row, err := db.Ledger().Entry(context.Background(), entryID)
	if err != nil {
		t.Fatalf("Entry(%s): %v", entryID, err)
	}
	if row == nil {
		t.Fatalf("no entry %s", entryID)
	}
	for _, ex := range row.Executions {
		for i := range ex.Artifacts {
			if ex.Artifacts[i].MediaType != content.MediaBlockRows {
				continue
			}
			art, err := db.Ledger().Artifact(context.Background(), ex.Artifacts[i].ID)
			if err != nil {
				t.Fatalf("Artifact: %v", err)
			}
			if art == nil {
				t.Fatal("metadata named an artifact the store does not hold")
			}
			var body strings.Builder
			for _, c := range art.Chunks {
				body.Write(c)
			}
			return body.String()
		}
	}
	return ""
}

// startsACommand submits the command the way a typed command really flows —
// the submit opens the entry and the shell's authenticated start attaches to
// it — and answers the entry the attempt rides (the attempt id IS the entry
// id; the store writes them as one row).
func startsACommand(t *testing.T, e *lifecycleTestEnv, pub *lifecyclepub.Publisher, lane lifecycle.LaneID, h lifecycle.DomainHandle, seq uint64, command string) string {
	t.Helper()
	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
		lifecycleSubmitParams(string(h.Domain), command), 41))
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, seq, lifecycleStartEvt(nil, command)))
	return got.ID
}

// streamRows reads a stored block the way a client does: the artifact body
// parsed line by line, each row's text rejoined from its cells. A cell-per-
// grapheme row never carries a contiguous string, so a substring assert on
// the body is an assertion about nothing.
func streamRows(t *testing.T, db content.ContentDB, entryID string) []struct {
	From uint64
	Text string
} {
	t.Helper()
	body := blockRowsBody(t, db, entryID)
	var out []struct {
		From uint64
		Text string
	}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if line == "" {
			continue
		}
		// Cells are positional tuples; decode positionally.
		var loose struct {
			From uint64          `json:"from"`
			Row  json.RawMessage `json:"row"`
		}
		if err := json.Unmarshal([]byte(line), &loose); err != nil {
			t.Fatalf("parse stored line: %v\nline: %s", err, line)
		}
		var row struct {
			Cells [][]any `json:"cells"`
		}
		if err := json.Unmarshal(loose.Row, &row); err != nil {
			t.Fatalf("parse stored row: %v", err)
		}
		var text strings.Builder
		for _, cell := range row.Cells {
			g, _ := cell[0].(string)
			text.WriteString(g)
		}
		out = append(out, struct {
			From uint64
			Text string
		}{loose.From, text.String()})
	}
	return out
}

// The opposite event order is also load-bearing: ledger submit/start and the
// authenticated OPEN complete before the helper offers rows, so delivery
// appends immediately to the already-selected block.
func TestBlockRowsArrived_AppendsToTheAuthenticatedCommand(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "printf 'one\\ntwo\\n'")

	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
		aStreamRow("one"), aStreamRow("two"),
	})
	if !confirm || written != 1 {
		t.Fatalf("ack = (%d, %v), want rows 0..1 confirmed", written, confirm)
	}
	kept := streamRows(t, db, attempt)
	if len(kept) != 2 {
		t.Fatalf("stored block holds %d rows, want 2", len(kept))
	}
	wantTexts := []string{"one", "two"}
	for i, want := range wantTexts {
		if kept[i].From != uint64(i) { //nolint:gosec // a row index, not a byte count
			t.Fatalf("row %d carries from=%d", i, kept[i].From)
		}
		if kept[i].Text != want {
			t.Fatalf("row %d reads %q, want %q", i, kept[i].Text, want)
		}
	}
}

// If the authenticated open fact beats ledger persistence, rows must stay
// unacknowledged until the bind retry opens the durable block. The helper has
// no separate copy after confirmation, so acknowledging this interval while
// current is nil would lose it permanently.
func TestBlockRowsArrived_BeforeLedgerBindIsHeldUntilRetry(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	var confirmed []uint64
	e.ws.AttachBlockRowsWithConfirmation(session.ID(sid), func(upTo uint64) {
		confirmed = append(confirmed, upTo)
	})

	const command = "printf before-bind"
	got := decodeSubmitAttemptResult(t, jsonrpcCallWithID(t, e.conn, "lifecycle.submitAttempt",
		lifecycleSubmitParams(string(h.Domain), command), 42))
	e.ws.blockStream.openAttemptFor(e.ws, session.ID(sid), got.ID)
	e.ws.blockStream.mu.Lock()
	current := e.ws.blockStream.current[session.ID(sid)]
	waiting := e.ws.blockStream.waiting[session.ID(sid)]
	e.ws.blockStream.mu.Unlock()
	if current != nil || waiting != got.ID {
		t.Fatalf("pre-bind open state = current:%v waiting:%q, want no block and waiting for %q", current != nil, waiting, got.ID)
	}

	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
		aStreamRow("before-bind"),
	})
	if confirm {
		t.Fatalf("pre-bind rows were acknowledged through %d; the helper cannot replay them", written)
	}

	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleStartEvt(nil, command)))

	kept := streamRows(t, db, got.ID)
	if len(kept) != 1 || kept[0].Text != "before-bind" {
		t.Fatalf("bind retry stored rows = %+v, want the held pre-bind row", kept)
	}
	if len(confirmed) != 1 || confirmed[0] != 0 {
		t.Fatalf("deferred confirmation = %v, want [0]", confirmed)
	}
}

// An authenticated end can resolve while the bind retry is flushing rows.
// The close must park behind that flush, or the artifact can seal before the
// deferred rows reach the store.
func TestBlockRowsCloseWaitsForDeferredRows(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))
	attempt := startsACommand(t, e, pub, lane, h, 2, "printf deferred")

	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("seed")}); !confirm {
		t.Fatal("seed row was not confirmed")
	}
	existing := streamRows(t, db, attempt)
	from := existing[len(existing)-1].From + 1
	e.ws.blockStream.mu.Lock()
	block := e.ws.blockStream.current[session.ID(sid)]
	pending := []pendingRows{{from: from, rows: []emulator.Row{aStreamRow("deferred")}}}
	e.ws.blockStream.pending[session.ID(sid)] = pending
	e.ws.blockStream.flushing[session.ID(sid)] = true
	e.ws.blockStream.mu.Unlock()

	e.ws.closeBlockRows(session.ID(sid), attempt, from+1, []emulator.Row{aStreamRow("closing")}, "deferred-close")
	e.ws.blockStream.mu.Lock()
	parked := len(e.ws.blockStream.pendingCloses[session.ID(sid)])
	stillOpen := e.ws.blockStream.open[session.ID(sid)][attempt] != nil
	e.ws.blockStream.mu.Unlock()
	if parked != 1 || !stillOpen {
		t.Fatalf("close gate state = parked:%d open:%v, want parked:1 open:true", parked, stillOpen)
	}
	e.ws.blockStream.mu.Lock()
	delete(e.ws.blockStream.pending, session.ID(sid))
	e.ws.blockStream.mu.Unlock()

	e.ws.blockStream.flushPendingRows(e.ws, session.ID(sid), block, pending, nil)
	kept := streamRows(t, db, attempt)
	if len(kept) != 3 || kept[0].Text != "seed" || kept[1].Text != "deferred" || kept[2].Text != "closing" {
		t.Fatalf("flush-before-close rows = %+v, want seed, deferred, closing", kept)
	}
	row, err := db.Ledger().Entry(context.Background(), attempt)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	for _, ex := range row.Executions {
		for _, artifact := range ex.Artifacts {
			if artifact.MediaType != content.MediaBlockRows {
				continue
			}
			stored, err := db.Ledger().Artifact(context.Background(), artifact.ID)
			if err != nil {
				t.Fatalf("Artifact: %v", err)
			}
			if stored.State != content.ArtifactSealed {
				t.Fatalf("deferred block state = %q, want sealed", stored.State)
			}
		}
	}
}

// THE REFUSAL. Output retention off: the command keeps its row, the rows
// that follow are confirmed (the helper's mark moves past rows nobody will
// ever want) and NOTHING is stored — not even a first chunk.
func TestBlockRowsArrived_RefusedCommandConfirmsButStoresNothing(t *testing.T) {
	policy := content.NewPolicy()
	policy.SetOutputEnabled(false)
	db := newLedgerStoreWithPolicy(t, policy)
	e, pub, lane, h, sid, _ := newLifecycleLedgerEnvWithStore(t, db)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make secret")

	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("classified")})
	if !confirm || written != 0 {
		t.Fatalf("ack = (%d, %v), want the delivery confirmed", written, confirm)
	}
	if body := blockRowsBody(t, db, attempt); body != "" {
		t.Fatalf("a refused command stored %q — not even a first chunk may be written", body)
	}
}

// THE END, completion first. The fence the kernel accepted resolves the end
// marker: the closing rows go in, the block seals, and the attached client
// hears both that it grew and that it closed.
func TestBlockIntervalEnded_SealsWhenTheCompletionArrivedFirst(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("working")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	fence := lifecycleFence(0x44)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))

	// The end marker arrives AFTER its completion and must still be
	// authenticated by it.
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 1, []emulator.Row{aStreamRow("final screen")})

	kept := streamRows(t, db, attempt)
	if len(kept) != 2 || kept[1].From != 1 || kept[1].Text != "final screen" {
		t.Fatalf("the sealed block's rows = %+v, want the streamed row and the closing screen", kept)
	}
	row, err := db.Ledger().Entry(context.Background(), attempt)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	for _, ex := range row.Executions {
		for i := range ex.Artifacts {
			if ex.Artifacts[i].MediaType == content.MediaBlockRows {
				art, _ := db.Ledger().Artifact(context.Background(), ex.Artifacts[i].ID)
				if art.State != content.ArtifactSealed {
					t.Fatalf("the block is %q, want sealed", art.State)
				}
			}
		}
	}
	// The attached client heard about both.
	deadline := time.Now().Add(wantWithin)
	if _, err := awaitFrame(e.conn, deadline, isNotification("block.grew")); err != nil {
		t.Fatalf("no block.grew reached the subscriber: %v", err)
	}
	if _, err := awaitFrame(e.conn, deadline, isNotification("block.closed")); err != nil {
		t.Fatalf("no block.closed reached the subscriber: %v", err)
	}
}

// THE END, end marker first — the order ADR-0024 decision 7 says is real.
// The fence has not been seen yet, so the end waits (bounded) for its
// completion, and the completion resolves it: one meeting, either order.
func TestBlockIntervalEnded_WaitsForTheCompletionThatNamesIt(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("working")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	fence := lifecycleFence(0x45)
	// The end arrives while the kernel holds no fence for anyone.
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 1, []emulator.Row{aStreamRow("final screen")})
	if body := blockRowsBody(t, db, attempt); strings.Contains(body, "final screen") {
		t.Fatal("an unauthenticated end marker appended the closing rows")
	}

	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	kept := streamRows(t, db, attempt)
	found := false
	for _, r := range kept {
		if r.Text == "final screen" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the parked end never resolved; rows = %+v", kept)
	}
}

// A TEARDOWN with no end marker seals what arrived. The interval's own end
// never will come; the rows already stored are the block's truth.
func TestDetachBlockRows_SealsAnUnendedBlock(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{aStreamRow("partial")}); !confirm {
		t.Fatal("the streamed row was not confirmed")
	}

	e.ws.DetachBlockRows(session.ID(sid))
	kept := streamRows(t, db, attempt)
	if len(kept) != 1 || kept[0].Text != "partial" {
		t.Fatalf("the sealed block lost what arrived: %+v", kept)
	}
	// And a detached session's rows are nobody's: the stream is inert.
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 5, 0, []emulator.Row{aStreamRow("late")}); confirm {
		t.Fatal("a detached session's stream still answered")
	}
}

// THE INERT TODAY. Without a rows source the attempt-fact hook opens
// nothing: a block that opens and can never close is the defect, so until
// the halves merge and the wiring lands, no start may open one.
func TestBlockRowsStream_IsInertWithoutASource(t *testing.T) {
	e, pub, lane, h, _, db := newLifecycleLedgerEnv(t, true)

	attempt := startsACommand(t, e, pub, lane, h, 2, "make watch")
	if body := blockRowsBody(t, db, attempt); body != "" {
		t.Fatalf("a block opened with no rows source to feed it:\n%s", body)
	}
	if _, confirm := e.ws.BlockRowsArrived(lifecycleSessionOf(t, e), 0, 0, []emulator.Row{aStreamRow("x")}); confirm {
		t.Fatal("rows were confirmed with no source attached")
	}
}

// Rows outside an authenticated attempt are prompt scroll, not the next
// command's output. They keep the original drop-and-confirm outcome.
func TestBlockRowsArrived_NoAttemptRowsAreConfirmedAndDropped(t *testing.T) {
	e, _, _, _, sid, _ := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	written, confirm := e.ws.BlockRowsArrived(session.ID(sid), 7, 0, []emulator.Row{aStreamRow("prompt")})
	if !confirm || written != 7 {
		t.Fatalf("no-attempt rows ack = (%d, %v), want (7, true)", written, confirm)
	}
	e.ws.blockStream.mu.Lock()
	defer e.ws.blockStream.mu.Unlock()
	if len(e.ws.blockStream.pending[session.ID(sid)]) != 0 {
		t.Fatal("no-attempt rows were retained for a future command")
	}
}

// lifecycleSessionOf is the session id the env's lane is registered to.
func lifecycleSessionOf(t *testing.T, e *lifecycleTestEnv) session.ID {
	t.Helper()
	e.ws.lifecycleMu.Lock()
	defer e.ws.lifecycleMu.Unlock()
	for _, id := range e.ws.lifecycleLanes {
		return id
	}
	t.Fatal("the env registered no lane")
	return ""
}

// THE READ-BACK, OVER THE WIRE (AGENTS.md rule 5's third check): rows the
// store holds come back through the EXISTING read path — ledger.get's
// metadata and ledger.artifact's body — off a real socket, and the body
// parses line by line into the vocabulary the contract declares. The
// command here is driven through the real submit-and-authenticated-start
// path, so what the wire answers is what the coordinator stored, not a
// payload the test assembled.
func TestBlockRowsReadBack_OverTheWireConformsToContract(t *testing.T) {
	e, pub, lane, h, sid, db := newLifecycleLedgerEnv(t, true)
	e.ws.AttachBlockRows(session.ID(sid))

	attempt := startsACommand(t, e, pub, lane, h, 2, "printf 'a\\nb\\n'")
	if _, confirm := e.ws.BlockRowsArrived(session.ID(sid), 0, 0, []emulator.Row{
		aStreamRow("a"), aStreamRow("b"),
	}); !confirm {
		t.Fatal("the streamed rows were not confirmed")
	}
	fence := lifecycleFence(0x46)
	mustLifecycleIngest(t, pub, "T", lifecycleEnv(lane, h, 3, lifecycleCompleteEvt(lifecycle.AttemptID(attempt), 0, fence)))
	e.ws.BlockIntervalEnded(session.ID(sid), fence, 2, []emulator.Row{aStreamRow("done")})

	// The artifact id, off ledger.get's metadata (what a restored block's
	// client reads first).
	getResp := jsonrpcCallWithID(t, e.conn, "ledger.get", map[string]any{"id": attempt}, 51)
	getSchema := loadSchema(t, "ledger.get.schema.json")
	var getEnvelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(getResp, &getEnvelope); err != nil {
		t.Fatalf("ledger.get response: %v", err)
	}
	if getEnvelope.Error != nil {
		t.Fatalf("ledger.get refused the block's entry: %+v", getEnvelope.Error)
	}
	var getResult map[string]any
	if err := json.Unmarshal(getEnvelope.Result, &getResult); err != nil {
		t.Fatalf("ledger.get result: %v", err)
	}
	if err := getSchema.Validate(getResult); err != nil {
		t.Fatalf("ledger.get does not conform to its contract: %v", err)
	}

	// The body, off ledger.artifact.
	artifactID := rowsArtifactID(t, db, attempt)
	artResp := jsonrpcCallWithID(t, e.conn, "ledger.artifact", map[string]any{"id": artifactID}, 52)
	artSchema := loadSchema(t, "ledger.artifact.schema.json")
	rowsSchema := loadSchema(t, "ledger.blockRows.schema.json")
	var artEnvelope struct {
		Result json.RawMessage  `json:"result"`
		Error  *jsonrpcErrorObj `json:"error"`
	}
	if err := json.Unmarshal(artResp, &artEnvelope); err != nil {
		t.Fatalf("ledger.artifact response: %v", err)
	}
	if artEnvelope.Error != nil {
		t.Fatalf("ledger.artifact refused the block's body: %+v", artEnvelope.Error)
	}
	var artResult map[string]any
	if err := json.Unmarshal(artEnvelope.Result, &artResult); err != nil {
		t.Fatalf("ledger.artifact result: %v", err)
	}
	if err := artSchema.Validate(artResult); err != nil {
		t.Fatalf("ledger.artifact does not conform to its contract: %v", err)
	}
	body, _ := artResult["body"].(string)
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("the body holds %d lines, want the two streamed rows and the closing screen", len(lines))
	}
	for i, line := range lines {
		var doc any
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		if err := rowsSchema.Validate(doc); err != nil {
			t.Fatalf("stored line %d does not conform to ledger.blockRows.schema.json: %v", i, err)
		}
	}
}

func rowsArtifactID(t *testing.T, db content.ContentDB, entryID string) string {
	t.Helper()
	row, err := db.Ledger().Entry(context.Background(), entryID)
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if row == nil {
		t.Fatalf("no entry %s", entryID)
	}
	for _, ex := range row.Executions {
		for i := range ex.Artifacts {
			if ex.Artifacts[i].MediaType == content.MediaBlockRows {
				return ex.Artifacts[i].ID
			}
		}
	}
	t.Fatalf("entry %s carries no block rows artifact", entryID)
	return ""
}
