package transport

// ledger.artifact's paged read and its internal retry (ADR-y45sp41c): the
// request-shaped half of the fix for the 64-frame response queue overflowing
// on a reconnect that asks every block at once.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/shady2k/nocx/internal/capability"
	"github.com/shady2k/nocx/internal/content"
	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/log"
)

// fakeLedgerService answers only the two artifact reads a paged handler can
// make. The embedded nil satisfies the rest of capability.LedgerService: a
// handler that reached another method would panic, which is what a test seam
// is for.
type fakeLedgerService struct {
	capability.LedgerService
	art  *content.Artifact
	page *content.ArtifactRange
}

func (f *fakeLedgerService) Artifact(context.Context, string) (*content.Artifact, error) {
	return f.art, nil
}

func (f *fakeLedgerService) ArtifactRange(context.Context, string, uint64, int) (*content.ArtifactRange, error) {
	return f.page, nil
}

type fakeLedgerOp struct {
	svc  *fakeLedgerService
	runs int
	mu   sync.Mutex
}

func (f *fakeLedgerOp) Disposition() capability.Disposition {
	return capability.Excluded("test")
}

func (f *fakeLedgerOp) Run(ctx context.Context, fn func(context.Context, capability.LedgerService) error) error {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
	return fn(ctx, f.svc)
}

func (f *fakeLedgerOp) ran() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

// artifactResponder records one response and, when the handler uses the
// sent-aware path, hands the witness to the test instead of running it.
type artifactResponder struct {
	mu     sync.Mutex
	result json.RawMessage
	rpcErr *RPCError
	sent   func()
}

func (r *artifactResponder) TryResult(_ json.RawMessage, result json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = result
	return nil
}

func (r *artifactResponder) TryError(_ json.RawMessage, err RPCError) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rpcErr = &err
	return nil
}

func (r *artifactResponder) TryNotify(string, json.RawMessage) error { return nil }

func (r *artifactResponder) TryResultSent(_ json.RawMessage, result json.RawMessage, sent func()) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.result = result
	r.sent = sent
	return nil
}

func (r *artifactResponder) finishSent() {
	r.mu.Lock()
	sent := r.sent
	r.mu.Unlock()
	if sent != nil {
		sent()
	}
}

func pageOf(count int) *content.ArtifactRange {
	art := &content.Artifact{ID: "art-1", MediaType: content.MediaBlockRows, ByteLen: 4096}
	return &content.ArtifactRange{
		Artifact: art, FirstRow: 0, LastRow: uint64(count - 1), RowCount: count, //nolint:gosec // test fixture
		RangeStart: 0, RangeCount: count, HasMoreBefore: false, HasMoreAfter: false,
	}
}

func artifactRequest(id int, extra string) jsonrpcRequest {
	params := fmt.Sprintf(`{"id":"art-1"%s}`, extra)
	return jsonrpcRequest{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprint(id)), Method: "ledger.artifact", Params: json.RawMessage(params)}
}

func TestRowReadSlotRefusalIsImmediateAndCarriesTheRetryHint(t *testing.T) {
	flow := newRowReadFlow(1)
	if !flow.tryAcquire() {
		t.Fatal("the first slot was not free")
	}
	op := &fakeLedgerOp{svc: &fakeLedgerService{page: pageOf(10)}}
	rec := &artifactResponder{}
	h := ledgerReadHandlers{op: op, r: rec, rows: flow}

	h.handleArtifact(context.Background(), artifactRequest(1, `,"fromRow":0,"rowLimit":5`))

	if rec.rpcErr == nil {
		t.Fatalf("no refusal was written; result = %s", rec.result)
	}
	if rec.rpcErr.Code != RowReadDeferredCode {
		t.Fatalf("refusal code = %d, want %d", rec.rpcErr.Code, RowReadDeferredCode)
	}
	data, ok := rec.rpcErr.Data.(rowReadDeferredData)
	if !ok {
		t.Fatalf("refusal data = %T, want rowReadDeferredData", rec.rpcErr.Data)
	}
	if data.RetryAfterMs != RowReadRetryAfterMs {
		t.Fatalf("retryAfterMs = %d, want %d", data.RetryAfterMs, RowReadRetryAfterMs)
	}
	if op.ran() != 0 {
		t.Fatalf("the store was asked %d times for a request that had no slot", op.ran())
	}
}

func TestStoredRowSlotIsHeldUntilTheResponseHasLeft(t *testing.T) {
	flow := newRowReadFlow(1)
	op := &fakeLedgerOp{svc: &fakeLedgerService{page: pageOf(10)}}
	rec := &artifactResponder{}
	h := ledgerReadHandlers{op: op, r: rec, rows: flow}

	// First page: answered, and its slot is NOT released by the handler.
	h.handleArtifact(context.Background(), artifactRequest(1, `,"fromRow":0,"rowLimit":5`))
	if rec.result == nil {
		t.Fatalf("the first page produced no result: %+v", rec.rpcErr)
	}
	if flow.tryAcquire() {
		t.Fatal("the slot was free while the response had not left the socket")
	}

	// A second page is refused at once, without asking the store.
	h.handleArtifact(context.Background(), artifactRequest(2, `,"fromRow":5,"rowLimit":5`))
	if rec.rpcErr == nil || rec.rpcErr.Code != RowReadDeferredCode {
		t.Fatalf("the second request = %+v, want the retry refusal", rec.rpcErr)
	}

	// The witness runs when the socket write ends: the slot comes back and
	// the next request is served.
	rec.finishSent()
	if !flow.tryAcquire() {
		t.Fatal("the slot did not return after the response left")
	}
	flow.release()
}

func TestWholeBodyReadDoesNotTakeARowSlot(t *testing.T) {
	flow := newRowReadFlow(1)
	if !flow.tryAcquire() {
		t.Fatal("the only slot was not free")
	}
	op := &fakeLedgerOp{svc: &fakeLedgerService{
		art: &content.Artifact{ID: "art-1", MediaType: content.MediaText, ByteLen: 5, Chunks: [][]byte{[]byte("hello")}},
	}}
	rec := &artifactResponder{}
	h := ledgerReadHandlers{op: op, r: rec, rows: flow}

	h.handleArtifact(context.Background(), artifactRequest(1, ""))

	if rec.rpcErr != nil {
		t.Fatalf("a whole-body read was refused: %+v", rec.rpcErr)
	}
	if !strings.Contains(string(rec.result), `"body":"hello"`) {
		t.Fatalf("result = %s", rec.result)
	}
	flow.release()
}

// ── over the real socket ──────────────────────────────────────────────────

func aStoredRow(text string) emulator.Row {
	row := emulator.Row{Cells: make([]emulator.Cell, 0, len(text))}
	for _, r := range text {
		row.Cells = append(row.Cells, emulator.Cell{Grapheme: string(r), Width: emulator.WidthNarrow, HasText: true})
	}
	return row
}

func seedStoredRows(t *testing.T, db content.ContentDB, count int) (entryID, artifactID string) {
	t.Helper()
	ctx := context.Background()
	pane := "pane-rows"
	entryID, err := db.Ledger().RecordCompleted(ctx, content.CompletedCommand{
		Client: "test-client",
		Env:    content.Environment{ID: "local", Kind: content.EnvLocal},
		PaneID: &pane, Cwd: "/repo", Intent: "seq 1 25",
		Status: content.EntrySuccess, Source: content.SourceUser,
	})
	if err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	artifactID = "11111111-0000-7000-8000-000000000001"
	if got, err := db.Ledger().OpenBlockOutput(ctx, content.OpenBlockOutput{EntryID: entryID, ArtifactID: artifactID}); err != nil || got != artifactID {
		t.Fatalf("OpenBlockOutput = %q, %v", got, err)
	}
	rows := make([]emulator.Row, 0, count)
	for i := 0; i < count; i++ {
		rows = append(rows, aStoredRow(fmt.Sprintf("row-%02d", i)))
	}
	if err := db.Ledger().AppendBlockRows(ctx, content.AppendBlockRows{
		EntryID: entryID, ArtifactID: artifactID, FromRow: 0, Rows: rows,
	}); err != nil {
		t.Fatalf("AppendBlockRows: %v", err)
	}
	if _, err := db.Ledger().CloseBlockRows(ctx, content.CloseBlockRows{EntryID: entryID, ArtifactID: artifactID}); err != nil {
		t.Fatalf("CloseBlockRows: %v", err)
	}
	return entryID, artifactID
}

type artifactWireReply struct {
	ID            string  `json:"id"`
	MediaType     string  `json:"mediaType"`
	Body          string  `json:"body"`
	Truncated     *string `json:"truncated"`
	ByteLen       int64   `json:"byteLen"`
	FirstRow      *uint64 `json:"firstRow"`
	LastRow       *uint64 `json:"lastRow"`
	RowCount      int     `json:"rowCount"`
	RangeStart    *uint64 `json:"rangeStart"`
	RangeCount    int     `json:"rangeCount"`
	HasMoreBefore bool    `json:"hasMoreBefore"`
	HasMoreAfter  bool    `json:"hasMoreAfter"`
}

func TestLedgerArtifactPageOverTheWireConformsAndCarriesCoordinates(t *testing.T) {
	db := newLedgerStore(t)
	ws, stop := newLedgerWSServer(t, log.NewSlogAdapter(nil), db)
	defer stop()
	conn := connectWS(t, ws)
	_, artifactID := seedStoredRows(t, db, 25)

	resp := vaultCall(t, conn, "ledger.artifact", map[string]any{
		"id": artifactID, "fromRow": 10, "rowLimit": 5,
	}, 2)
	if resp.Error != nil {
		t.Fatalf("ledger.artifact: %+v", resp.Error)
	}
	if err := validateJSONErr(loadSchema(t, "ledger.artifact.schema.json"), resp.Result); err != nil {
		t.Fatalf("over-the-wire result does not conform: %v\nraw: %s", err, resp.Result)
	}
	var got artifactWireReply
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RowCount != 25 || got.FirstRow == nil || *got.FirstRow != 0 || got.LastRow == nil || *got.LastRow != 24 {
		t.Fatalf("whole-artifact coordinates = %+v", got)
	}
	if got.RangeStart == nil || *got.RangeStart != 10 || got.RangeCount != 5 {
		t.Fatalf("page coordinates = %+v", got)
	}
	if !got.HasMoreBefore || !got.HasMoreAfter {
		t.Fatalf("page of 25 at 10..15 claims moreBefore=%v moreAfter=%v", got.HasMoreBefore, got.HasMoreAfter)
	}
	var texts []string
	for _, line := range strings.Split(strings.TrimSuffix(got.Body, "\n"), "\n") {
		var parsed struct {
			Row struct {
				Text string `json:"text"`
			} `json:"row"`
		}
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			t.Fatalf("decode stored line: %v", err)
		}
		texts = append(texts, parsed.Row.Text)
	}
	if strings.Join(texts, ",") != "row-10,row-11,row-12,row-13,row-14" {
		t.Fatalf("page body = %v", texts)
	}
	// The socket itself is still open: the refusal path exists so that a
	// full queue is never answered by closing the connection.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":3,"method":"ledger.query","params":{"scope":"everywhere"}}`)); err != nil {
		t.Fatalf("connection did not survive the paged read: %v", err)
	}
}
