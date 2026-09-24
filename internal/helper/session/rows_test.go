package session

// The row bridge's own checks (nocx-2v80t.3.6): the runtime streams real
// departed rows, the bridge carries them to the bound subscriber in order,
// the payloads it produces satisfy their contracts, and the
// confirmed-written mark moves only where it may. The runtime is the real
// one over the real emulator; the sink is a test double because the wire is
// the one thing this file does not exercise (the over-the-wire half lives in
// internal/helper/client).

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// rowsSink is a Sink that records the rows-plane frames the pump sends, in
// order, and keeps the raw payloads for the contract checks.
type rowsSink struct {
	mu     sync.Mutex
	rows   []proto.OutputRowsFrame
	ends   []proto.IntervalEndFrame
	clears []proto.ClearBoundaryFrame
	rawRow [][]byte
}

func (s *rowsSink) SendSessionData(proto.SessionFrame) error   { return nil }
func (s *rowsSink) SendLifecycleData(proto.SessionFrame) error { return nil }
func (s *rowsSink) SendNotification(proto.Notification) error  { return nil }
func (s *rowsSink) SendScreenFrame(proto.ScreenDataFrame) error {
	return nil
}

func (s *rowsSink) SendOutputRows(f proto.OutputRowsFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, f)
	s.rawRow = append(s.rawRow, f.Payload)
	return nil
}

func (s *rowsSink) SendIntervalEnd(f proto.IntervalEndFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ends = append(s.ends, f)
	return nil
}

func (s *rowsSink) SendClearBoundary(f proto.ClearBoundaryFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clears = append(s.clears, f)
	return nil
}

func (s *rowsSink) rowFrames() []proto.OutputRowsFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]proto.OutputRowsFrame(nil), s.rows...)
}

func (s *rowsSink) clearFrames() []proto.ClearBoundaryFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]proto.ClearBoundaryFrame(nil), s.clears...)
}

func (s *rowsSink) endFrames() []proto.IntervalEndFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]proto.IntervalEndFrame(nil), s.ends...)
}

// rowsBridgeSession wires a hostSession the way finishSpawn does, minus the
// wire: a real runtime, the bridge bound to it, the pump running, and one
// bound subscriber whose sink is the recording double.
func rowsBridgeSession(t *testing.T, cols, rows int) (*hostSession, *sessionruntime.Session, *rowsSink) {
	t.Helper()
	proc := newRawReaderFakeProcess()
	rt := pumpRuntime(t, proc)
	hs := &hostSession{
		id:       proto.HostSessionID{Generation: "testhash", Session: "0123456789abcdef0123456789abcdef"},
		raw:      mintRaw(t),
		proc:     proc,
		win:      newWindow(2 * creditLimit),
		runtime:  rt,
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:      time.Now,
		subs:     make(map[proto.SubscriberID]*subscriber),
		rowWake:  make(chan struct{}, 1),
		rowsDone: make(chan struct{}),
	}
	sink := &rowsSink{}
	hs.subs["coord-1"] = &subscriber{
		id:   "coord-1",
		raw:  mintRaw(t),
		sink: sink,
	}
	rt.SetRowStream(&rowBridge{hs: hs})
	go hs.serveRows()
	t.Cleanup(func() { close(hs.rowsDone) })
	return hs, rt, sink
}

func mintRaw(t *testing.T) [16]byte {
	t.Helper()
	var raw [16]byte
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	return raw
}

// rowsFeed is one ingest of n numbered lines starting at from.
func rowsFeed(t *testing.T, rt *sessionruntime.Session, from, n int) {
	t.Helper()
	var sb strings.Builder
	for i := from; i < from+n; i++ {
		fmt.Fprintf(&sb, "L%06d\r\n", i)
	}
	if err := rt.Ingest([]byte(sb.String())); err != nil {
		t.Fatalf("ingest %d lines: %v", n, err)
	}
}

// loadRowSchema compiles one rows-plane schema with every helper schema and
// the frame contract registered, so the cross-directory $refs into
// session.frame.schema.json resolve locally.
func loadRowSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	dirs := []string{"../../../contracts/helper", "../../../contracts"}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".schema.json") {
				continue
			}
			f, openErr := os.Open(filepath.Join(dir, e.Name())) //nolint:gosec // test-only path under contracts/
			if openErr != nil {
				t.Fatalf("open %s: %v", e.Name(), openErr)
			}
			doc, parseErr := jsonschema.UnmarshalJSON(f)
			_ = f.Close()
			if parseErr != nil {
				t.Fatalf("parse %s: %v", e.Name(), parseErr)
			}
			id := "https://nocx.local/contracts/helper/" + e.Name()
			if dir == "../../../contracts" {
				id = "https://nocx.local/contracts/" + e.Name()
			}
			if addErr := c.AddResource(id, doc); addErr != nil {
				t.Fatalf("add %s: %v", e.Name(), addErr)
			}
		}
	}
	s, err := c.Compile("https://nocx.local/contracts/helper/" + name)
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	return s
}

func validateRowSchema(t *testing.T, s *jsonschema.Schema, raw []byte) {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("parse the payload: %v", err)
	}
	if err := s.Validate(doc); err != nil {
		t.Fatalf("the bridge's own payload does not satisfy its contract:\n%v\n\npayload was:\n%s", err, raw)
	}
}

// Thirty numbered lines on a twenty-four row screen: seven rows leave, in
// order, once each; the interval's end marker follows them, stops at row 7
// and carries the screen as the boundary sat on it. The payloads the pump
// produced satisfy their schemas — the check a test-built payload cannot
// make, made here against the bridge's own output.
//
// The fence sits mid-feed, with more of the SAME row's bytes ("tail") right
// after it and nothing to flush in between — the shape nocx.bash's own
// PROMPT_COMMAND writes (the fence, then 133;D, 133;A, OSC 7, then the
// visible PS1 text, all back to back). The runtime feeds the emulator up to
// the fence's own end before it ever asks for a screen (nocx-2v80t.3.12), so
// the closing screen this boundary carries is the screen exactly as the
// fence left it — "prompt", not "prompttail" — and "tail"'s own scroll,
// which happens strictly AFTER the fence, belongs to the interval that
// follows rather than to this one: EndRow stops at 7, not 8, and the row it
// departs (still-undeparted flood content, unrelated to "tail" or "prompt")
// streams separately, ahead of the boundary that excludes it.
func TestTheBridgeCarriesRowsInOrderAndTheEndAfterThem(t *testing.T) {
	_, rt, sink := rowsBridgeSession(t, 80, 24)

	rowsFeed(t, rt, 0, 30)
	nonce := sessionruntime.FenceNonce{}
	for i := range nonce {
		nonce[i] = 0xAB
	}
	fenceHex := fmt.Sprintf("%x", nonce) // the spelling a real fence carries it in
	// The fence rides the STREAM the way a real command writes it: the
	// emulator's own scanner sights it mid-ingest, and the completion that
	// follows joins the sighting.
	var sb strings.Builder
	fmt.Fprintf(&sb, "prompt\x1b]1337;NOCX_FENCE;%s\x07tail\r\n", fenceHex)
	if err := rt.Ingest([]byte(sb.String())); err != nil {
		t.Fatalf("ingest the fence: %v", err)
	}
	rt.Completed(rt.Incarnation(), nonce, 0)

	waitForRows(t, sink, 2, 1)

	// Two batches: the flood's seven departures, then one more row the
	// trailing "tail\r\n" scrolls off — a row the flood itself had not yet
	// departed, unrelated to "tail" or "prompt", and NOT this boundary's:
	// it streams because it left the screen, but the end marker below
	// excludes it from the interval that just closed.
	frames := sink.rowFrames()
	if len(frames) != 2 {
		t.Fatalf("the pump sent %d row frames, want 2", len(frames))
	}
	if frames[0].FromRow != 0 || frames[1].FromRow != 7 {
		t.Fatalf("the frames name FromRow %d and %d, want 0 and 7 — the index never skips", frames[0].FromRow, frames[1].FromRow)
	}
	if string(frames[0].Payload) == "" || string(frames[1].Payload) == "" {
		t.Fatal("a frame carries no payload")
	}

	ends := sink.endFrames()
	if len(ends) != 1 {
		t.Fatalf("the pump sent %d end markers, want 1", len(ends))
	}
	if ends[0].EndRow != 7 {
		t.Fatalf("the end marker stops at row %d, want 7 — bytes the SAME feed wrote after the fence (\"tail\") belong to the interval that follows, never to this one (nocx-2v80t.3.12)", ends[0].EndRow)
	}
	var closing struct {
		Closing []struct {
			Text string `json:"text"`
		} `json:"closing"`
	}
	if err := json.Unmarshal(ends[0].Payload, &closing); err != nil {
		t.Fatalf("decode the end marker's own payload: %v", err)
	}
	if len(closing.Closing) == 0 {
		t.Fatal("the end marker's closing screen carries no rows")
	}
	if last := closing.Closing[len(closing.Closing)-1].Text; last != "prompt" {
		t.Fatalf(`the closing screen's last row reads %q, want "prompt": `+
			`"tail", written after the fence in the same feed, leaked into this interval's own closing screen`, last)
	}

	// The real payloads, against their contracts.
	rowSchema := loadRowSchema(t, "session.output-rows.schema.json")
	validateRowSchema(t, rowSchema, frames[0].Payload)
	endSchema := loadRowSchema(t, "session.interval-end.schema.json")
	validateRowSchema(t, endSchema, ends[0].Payload)
}

// waitForClear waits until the pump has delivered at least n clear-boundary
// frames — an observable state, never a duration.
func waitForClear(t *testing.T, sink *rowsSink, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := len(sink.clearFrames()); got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pump never delivered %d clear-boundary frames", n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestTheBridgeCarriesAClearBoundary (nocx-2v80t.3.17): a real erase — ED3,
// the sequence `clear` emits — reaches the sink as its own frame, over the
// real runtime and the real bridge, with a payload that satisfies its
// contract. The check a test-built payload cannot make: this is the
// bridge's own output, not a document the test assembled.
func TestTheBridgeCarriesAClearBoundary(t *testing.T) {
	_, rt, sink := rowsBridgeSession(t, 80, 24)

	rowsFeed(t, rt, 0, 30) // real scrollback behind it, the state `clear` erases
	waitForRows(t, sink, 1, 0)

	if err := rt.Ingest([]byte("\x1b[H\x1b[2J\x1b[3J")); err != nil {
		t.Fatalf("ingest the clear sequence: %v", err)
	}
	waitForClear(t, sink, 1)

	frames := sink.clearFrames()
	if len(frames) != 1 {
		t.Fatalf("the pump sent %d clear-boundary frames, want 1", len(frames))
	}
	if len(frames[0].Payload) == 0 {
		t.Fatal("the clear-boundary frame carries no payload")
	}
	schema := loadRowSchema(t, "session.clear-boundary.schema.json")
	validateRowSchema(t, schema, frames[0].Payload)
}

// TestErasingTheDisplayAloneCarriesNoClearBoundary: ED2 with no ED3 — a
// full-screen program redrawing its own view — must never reach the wire as
// a clear boundary, the paired acceptance criterion nocx-2v80t.3.17 names by
// name.
func TestErasingTheDisplayAloneCarriesNoClearBoundary(t *testing.T) {
	_, rt, sink := rowsBridgeSession(t, 80, 24)

	rowsFeed(t, rt, 0, 30)
	waitForRows(t, sink, 1, 0)

	if err := rt.Ingest([]byte("\x1b[H\x1b[2J")); err != nil {
		t.Fatalf("ingest ED2 alone: %v", err)
	}
	// ED2 homes the cursor, so the screen is no longer full: enough lines to
	// fill it again and overflow once more, waited for as the observable
	// proof that the ED2 ingest above was fully processed (queued in the
	// SAME ordered FIFO a clear boundary would have ridden) before this
	// assertion runs — never a fixed sleep.
	rowsFeed(t, rt, 1000, 30)
	waitForRows(t, sink, 2, 0)

	if got := len(sink.clearFrames()); got != 0 {
		t.Fatalf("ED2 alone produced %d clear-boundary frames, want 0", got)
	}
}

// The confirmed-written mark is the helper's one record of what the
// coordinator holds: it advances on the acknowledgement, a stale
// acknowledgement is answered rather than refused, and one ahead of what the
// session ever departed is refused by name.
func TestTheConfirmedMarkAdvancesOnlyWhereItMay(t *testing.T) {
	hs, rt, sink := rowsBridgeSession(t, 80, 24)

	rowsFeed(t, rt, 0, 30)
	waitForRows(t, sink, 1, 0)

	if err := hs.confirmRows(sink, "coord-1", 4); err != nil {
		t.Fatalf("confirm rows through 4: %v", err)
	}
	if got := rowsPending(t, hs); got != 3 {
		t.Fatalf("rows pending after confirming 4 of 7 is %d, want 3", got)
	}
	// A stale redelivery of the same acknowledgement: answered, because the
	// mark is a floor and the floor did not move.
	if err := hs.confirmRows(sink, "coord-1", 4); err != nil {
		t.Fatalf("a stale confirmation of 4: %v", err)
	}
	if got := rowsPending(t, hs); got != 3 {
		t.Fatalf("a stale confirmation moved the mark: %d pending, want 3", got)
	}
	// Ahead of what the session ever departed: refused by name.
	if err := hs.confirmRows(sink, "coord-1", 8); err == nil {
		t.Fatal("confirming rows the session never departed was accepted")
	} else if err != ErrConfirmAhead {
		t.Fatalf("confirming ahead named %v, want ErrConfirmAhead", err)
	}

	// Depart more; now the mark may move there.
	rowsFeed(t, rt, 30, 1)
	waitForRows(t, sink, 2, 0)
	if err := hs.confirmRows(sink, "coord-1", 8); err != nil {
		t.Fatalf("confirm rows through 8: %v", err)
	}
	if got := rowsPending(t, hs); got != 0 {
		t.Fatalf("rows pending after confirming everything is %d, want 0", got)
	}

	// A subscriber the session does not know: refused, the way an ack for a
	// departed reader is.
	if err := hs.confirmRows(sink, "nobody", 0); err != ErrNotAttached {
		t.Fatalf("a confirmation from an unknown subscriber named %v, want ErrNotAttached", err)
	}
}

// rowsPending is the test's own read of what the mark leaves unconfirmed,
// computed from the same two fields the production handlers keep.
func rowsPending(t *testing.T, hs *hostSession) uint64 {
	t.Helper()
	hs.mu.Lock()
	defer hs.mu.Unlock()
	departed := hs.runtime.DepartedRowCount()
	if hs.rowsConfirmed >= departed {
		return 0
	}
	return departed - hs.rowsConfirmed
}

// waitForRows waits until the pump has delivered at least nRows row frames
// and nEnds end markers — an observable state, never a duration.
func waitForRows(t *testing.T, sink *rowsSink, nRows, nEnds int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		sink.mu.Lock()
		gotRows, gotEnds := len(sink.rows), len(sink.ends)
		sink.mu.Unlock()
		if gotRows >= nRows && gotEnds >= nEnds {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pump never delivered %d row frames and %d end markers (has %d and %d)", nRows, nEnds, gotRows, gotEnds)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
