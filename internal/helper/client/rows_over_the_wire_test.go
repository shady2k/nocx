package client_test

// The streamed block output's acceptance (nocx-2v80t.3.6), end to end: a
// real command on a real PTY, inside the real session service, prints three
// screens and writes its render fence; the coordinator's side — THIS client,
// on the attachment it holds — receives exactly the rows that left the
// screen, once each, in order, and then the interval's end marker. The
// authenticated half arrives through the lifecycle downlink the way the
// coordinator's kernel delivers it, and the confirmed-written mark is
// advanced over the same socket.
//
// Nothing here is mocked below the ABI: the service, the host, the framing
// and the client's decode are all real, and nothing waits on a duration —
// every wait ends on an observable delivery.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/emulator"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// rowEvent is one delivery in arrival order: the stream's order is the
// attribution, so the recorder keeps it.
type rowEvent struct {
	rows *client.OutputRows
	end  *client.IntervalEnd
}

// rowsRecorder collects what the observers fire, in arrival order. The
// observers run on the connection's read loop, so they only record and
// return — a blocking observer would stall the read loop and wedge the
// stream it is listening to.
type rowsRecorder struct {
	mu     sync.Mutex
	events []rowEvent
}

func newRowsRecorder() *rowsRecorder {
	return &rowsRecorder{}
}

func (r *rowsRecorder) onRows(rows client.OutputRows) {
	r.mu.Lock()
	r.events = append(r.events, rowEvent{rows: &rows})
	r.mu.Unlock()
}

func (r *rowsRecorder) onEnd(end client.IntervalEnd) {
	r.mu.Lock()
	r.events = append(r.events, rowEvent{end: &end})
	r.mu.Unlock()
}

// delivered counts the arrivals so far.
func (r *rowsRecorder) delivered() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// hasEnd reports whether an end marker has arrived.
func (r *rowsRecorder) hasEnd() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.end != nil {
			return true
		}
	}
	return false
}

// interval answers the interval's own rows — indexed by absolute position,
// in arrival order — the end marker, and every batch that arrived after it.
func (r *rowsRecorder) interval() (in []string, end client.IntervalEnd, after []client.OutputRows, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.events {
		if e.end == nil {
			continue
		}
		end = *e.end
		ok = true
		for _, prev := range r.events[:i] {
			if prev.rows == nil {
				continue
			}
			for j, row := range prev.rows.Rows {
				idx := prev.rows.FromRow + uint64(j) // #nosec G115 -- j is a slice index, never negative
				if idx < end.EndRow {
					in = append(in, rowTextWire(row))
				}
			}
		}
		for _, later := range r.events[i+1:] {
			if later.rows != nil {
				after = append(after, *later.rows)
			}
		}
		return in, end, after, true
	}
	return nil, client.IntervalEnd{}, nil, false
}

// waitForEnd waits until the stream has delivered an end marker — the one
// event this acceptance closes on.
func waitForEnd(t *testing.T, r *rowsRecorder) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !r.hasEnd() {
		if time.Now().After(deadline) {
			t.Fatalf("the stream never delivered an end marker (has %d events)", r.delivered())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// rowTextWire reads one decoded row's text the way a reader does: the
// graphemes that carry text, trailing blanks dropped.
func rowTextWire(row emulator.Row) string {
	var sb strings.Builder
	for _, c := range row.Cells {
		if c.Grapheme != "" {
			sb.WriteString(c.Grapheme)
		}
	}
	return strings.TrimRight(sb.String(), " ")
}

// clientID projects the wire identity onto the client's own: this boundary
// deliberately duplicates the shape rather than exposing proto types above
// it.
func clientID(id proto.HostSessionID) client.HostSessionID {
	return client.HostSessionID{Generation: string(id.Generation), Session: id.Session}
}

// rlen counts the row batches the recorder has, for failure context.
func rlen(r *rowsRecorder) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// fenceNonceFrom decodes the test's nonce spelling into the runtime's type,
// the comparison the end marker is judged by.
func fenceNonceFrom(t *testing.T, hexStr string) sessionruntime.FenceNonce {
	t.Helper()
	raw, err := hex.DecodeString(hexStr)
	if err != nil || len(raw) != 32 {
		t.Fatalf("the test's own nonce spelling is broken: %q", hexStr)
	}
	var n sessionruntime.FenceNonce
	copy(n[:], raw)
	return n
}

// typeText types one line of text through the one-shot write path: read the
// screen, mint a target for its first row, spend it on the text. The shell
// may redraw its prompt between the read and the commit — stale_target is
// the write path's answer for exactly that, and a fresh read is the way
// through, the same answer the coordinator's own caller gives.
func typeText(t *testing.T, c *client.Client, id proto.HostSessionID, text string) (proto.IntentResult, error) {
	t.Helper()
	var result proto.IntentResult
	for attempt := 0; attempt < 10; attempt++ {
		snap, err := c.Snapshot(context.Background(), clientID(id))
		if err != nil {
			return proto.IntentResult{}, err
		}
		target, err := c.Target(context.Background(), proto.TargetParams{
			Session:    id,
			SnapshotID: snap.SnapshotID,
			Kind:       "region",
			First:      0,
			Last:       0,
		})
		if err != nil {
			return proto.IntentResult{}, err
		}
		result, err = c.Intent(context.Background(), proto.IntentParams{
			Session:     id,
			Token:       target.Token,
			AccessEpoch: snap.AccessEpoch,
			CommitBy:    int64(monoclock.Now()) + int64(5*time.Second),
			Kind:        "text",
			Payload:     []byte(text),
		})
		if err != nil {
			return proto.IntentResult{}, err
		}
		if result.State == "executed" {
			return result, nil
		}
	}
	return result, nil
}

func TestARowsStreamReachesTheCoordinatorInOrderThenTheEnd(t *testing.T) {
	c := hostedSessions(t)

	// Three screens of numbered lines. The shell is pinned by the harness;
	// the command rides the one-shot write path the way the coordinator
	// really types.
	const floodLines = 72
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(0x30 + i)
	}
	nonceHex := hex.EncodeToString(nonce)
	var cmd strings.Builder
	cmd.WriteString("stty -echo 2>/dev/null; ")
	for i := 0; i < floodLines; i++ {
		fmt.Fprintf(&cmd, "echo L%06d; ", i)
	}
	fmt.Fprintf(&cmd, "printf '\\033]1337;NOCX_FENCE;%s\\007'; ", nonceHex)
	cmd.WriteString("\r")

	// One session at 80x24: the flood departs almost everything it printed.
	in := proto.SpawnParams{Cwd: "/", Cols: 80, Rows: 24, WindowBytes: 1 << 20, IdempotencyKey: "pane-rows-01"}
	var spawnRaw json.RawMessage
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpSpawn, in, &spawnRaw); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	var spawned proto.SpawnResult
	if err := json.Unmarshal(spawnRaw, &spawned); err != nil {
		t.Fatalf("decode spawn: %v", err)
	}

	rec := newRowsRecorder()
	attached, err := c.Attach(context.Background(), proto.AttachParams{
		Session:    spawned.Entry.Session,
		Subscriber: "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	attached.OnOutputRows(rec.onRows)
	attached.OnIntervalEnd(rec.onEnd)

	floodResult, err := typeText(t, c, spawned.Entry.Session, cmd.String())
	if err != nil {
		t.Fatalf("typing the command: %v", err)
	}
	if floodResult.State != "executed" {
		t.Fatalf("the command never reached the shell: state=%s refusal=%+v", floodResult.State, floodResult.Refusal)
	}

	// The authenticated half arrives through the lifecycle downlink BEFORE
	// the fence is ever sighted — the completion-first order ADR-0024
	// decision 7 names as ordinary, and the one that keeps this test free
	// of the bounded missing-fence wait: when the fence byte arrives, the
	// join is instant and the end marker streams after the interval's rows.
	lc := proto.LifecycleCompleteParams{
		Session:     spawned.Entry.Session,
		Incarnation: proto.Incarnation{Session: spawned.Entry.Session.Session, Generation: 1},
		Nonce:       nonceHex,
		ExitCode:    nil,
	}
	if err := c.Call(context.Background(), proto.ServiceSession, proto.OpLifecycleComplete, lc, nil); err != nil {
		t.Fatalf("lifecycle-complete: %v", err)
	}

	// The acceptance: the rows the flood departed, once each, in order, and
	// then the end marker — with the closing screen the boundary sat on.
	waitForEnd(t, rec)
	inRows, end, after, ok := rec.interval()
	if !ok {
		t.Fatalf("no end marker ever arrived; %d row batches streamed first", rlen(rec))
	}
	// The flood's departed rows are the tail of the interval: whatever the
	// shell echoed of the command departed first, and every flood line after
	// the twenty-third pushes one row off. The LAST 49 of the interval's
	// rows are therefore exactly L000000..L000048, in order.
	const wantFlood = floodLines - 23
	if len(inRows) < wantFlood {
		t.Fatalf("the interval streamed %d rows, want at least the flood's %d departed", len(inRows), wantFlood)
	}
	tail := inRows[len(inRows)-wantFlood:]
	for i, got := range tail {
		if want := fmt.Sprintf("L%06d", i); got != want {
			t.Fatalf("the flood's departed row %d reads %q, want %s — the rows must arrive in order", i, got, want)
		}
	}
	if end.EndRow != uint64(len(inRows)) { // #nosec G115 -- small and positive
		t.Fatalf("the end marker stops at row %d, want %d — the interval's rows stop exactly there", end.EndRow, len(inRows))
	}
	if end.Nonce != fenceNonceFrom(t, nonceHex) {
		t.Fatalf("the end marker names a different meeting than %s", nonceHex)
	}
	if end.Closing == nil {
		t.Fatal("the end marker carries no closing screen")
	}
	// The closing screen is the screen as the boundary JOINED — the flood's
	// tail is on it, and so is whatever the shell printed after the fence in
	// the same breath the pump read: the capture is one instant of the real
	// stream, not a curated one.
	sawTail := false
	for _, row := range end.Closing {
		if rowTextWire(row) == fmt.Sprintf("L%06d", floodLines-1) {
			sawTail = true
		}
	}
	if !sawTail {
		t.Fatalf("the closing screen does not hold the flood's last line L%06d", floodLines-1)
	}
	// Rows after the end marker (the shell's next prompt) exist only at
	// indices from EndRow up: the boundary is where the interval's rows stop.
	for _, b := range after {
		if b.FromRow < end.EndRow {
			t.Fatalf("a post-end batch names FromRow %d, below the boundary %d", b.FromRow, end.EndRow)
		}
	}

	// The confirmed-written mark: everything received is confirmed, and a
	// mark ahead of what departed is refused.
	if err := attached.ConfirmWritten(context.Background(), end.EndRow); err != nil {
		t.Fatalf("confirm written through %d: %v", end.EndRow, err)
	}
	if err := attached.ConfirmWritten(context.Background(), end.EndRow+1); err == nil {
		t.Fatal("confirming rows the session never departed was accepted")
	}
}
