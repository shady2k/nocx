package outbound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/waittest"
)

// fakeSocket is the Socket seam for tests. When block is true, WriteMessage
// signals writeStarted (the pump is mid-write) and then blocks until
// release is closed — the deterministic way to hold the pump while the
// queue fills.
type fakeSocket struct {
	mu           sync.Mutex
	writes       []Frame
	writeStarted chan struct{} // buffered 1; signaled on entry to WriteMessage when blocking
	release      chan struct{} // closed to unblock; nil when not blocking
	closed       atomic.Bool
}

func newFakeSocket(block bool) *fakeSocket {
	f := &fakeSocket{}
	if block {
		f.writeStarted = make(chan struct{}, 1)
		f.release = make(chan struct{})
	}
	return f
}

func (f *fakeSocket) WriteMessage(msgType int, data []byte) error {
	if f.release != nil {
		select {
		case f.writeStarted <- struct{}{}:
		default:
		}
		<-f.release
	}
	f.mu.Lock()
	f.writes = append(f.writes, Frame{MsgType: msgType, Data: append([]byte(nil), data...)})
	f.mu.Unlock()
	return nil
}

func (f *fakeSocket) SetWriteDeadline(time.Time) error { return nil }

func (f *fakeSocket) ReadMessage() (int, []byte, error) { return 0, nil, errors.New("no reads") }

func (f *fakeSocket) Close() error {
	f.closed.Store(true)
	if f.release != nil {
		select {
		case <-f.release:
		default:
			close(f.release)
		}
	}
	return nil
}

func (f *fakeSocket) got() []Frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Frame(nil), f.writes...)
}

// waitWrites waits until the fake has recorded n writes.
func waitWrites(t *testing.T, f *fakeSocket, n int) []Frame {
	t.Helper()
	waittest.WaitForTimeoutDetail(t, fmt.Sprintf("%d writes", n), 5*time.Second,
		func() string { return fmt.Sprintf("got %d", len(f.got())) },
		func() bool { return len(f.got()) >= n })
	return f.got()
}

// fillQueue builds a Conn on a blocking fake socket, then fills its queue
// while the pump is held mid-write on the first frame. The returned fake's
// release channel unblocks the pump. The connection has exactly depth
// frames queued when this returns.
func fillQueue(t *testing.T, depth int) (*Conn, *fakeSocket) {
	t.Helper()
	f := newFakeSocket(true)
	c := New(f, Config{QueueDepth: depth})
	// First frame: the pump dequeues it and blocks mid-write.
	if err := c.TryEnqueue(TextMessage, []byte("first")); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	select {
	case <-f.writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never started its first write")
	}
	// The pump is stuck on "first"; every remaining slot is free. Fill it.
	for i := 0; i < depth; i++ {
		if err := c.TryEnqueue(TextMessage, []byte("fill")); err != nil {
			t.Fatalf("enqueue fill %d: %v", i, err)
		}
	}
	return c, f
}

func TestPumpWritesQueuedFramesInOrder(t *testing.T) {
	f := newFakeSocket(false)
	c := New(f, Config{})
	defer c.Close()

	payloads := [][]byte{[]byte("one"), []byte("two"), []byte("three")}
	for _, p := range payloads {
		if err := c.TryEnqueue(TextMessage, p); err != nil {
			t.Fatalf("enqueue %q: %v", p, err)
		}
	}
	writes := waitWrites(t, f, 3)
	for i, w := range writes {
		if string(w.Data) != string(payloads[i]) {
			t.Fatalf("write %d = %q, want %q", i, w.Data, payloads[i])
		}
	}
}

func TestQueueFullMarksStalledAndDropsFrame(t *testing.T) {
	c, f := fillQueue(t, 4)
	defer c.Close()

	if c.Stalled() {
		t.Fatal("connection marked stalled before any overflow")
	}
	if err := c.TryEnqueue(TextMessage, []byte("busy")); !errors.Is(err, ErrStalled) {
		t.Fatalf("overflow enqueue err = %v, want ErrStalled", err)
	}
	if !c.Stalled() {
		t.Fatal("connection not marked outbound-stalled after overflow")
	}
	if c.StallCount() != 1 {
		t.Fatalf("StallCount = %d, want 1", c.StallCount())
	}

	// Unblock the pump: it must deliver the queued frames AND the stall
	// notice through the reserved slot.
	close(f.release)
	noticed := func() bool {
		for _, fr := range f.got() {
			if strings.Contains(string(fr.Data), "outbound.stalled") {
				return true
			}
		}
		return false
	}
	waittest.WaitForTimeoutDetail(t, "stall notice and queued frames", 5*time.Second,
		func() string {
			return fmt.Sprintf("stall notice written = %v; got %d writes", noticed(), len(f.got()))
		},
		func() bool { return noticed() && len(f.got()) >= 6 })
}

func TestOverflowBeyondReservedSlotCloses(t *testing.T) {
	c, f := fillQueue(t, 2)
	defer c.Close()

	if err := c.TryEnqueue(TextMessage, []byte("busy1")); !errors.Is(err, ErrStalled) {
		t.Fatalf("first overflow err = %v, want ErrStalled", err)
	}
	if f.closed.Load() {
		t.Fatal("connection closed while the reserved slot could still take the notice")
	}

	// Second overflow while the first notice is still undelivered (the
	// pump is stuck on "first"): the reserved slot is occupied, so the
	// policy closes the WebSocket.
	if err := c.TryEnqueue(TextMessage, []byte("busy2")); !errors.Is(err, ErrStalled) {
		t.Fatalf("second overflow err = %v, want ErrStalled", err)
	}
	if !f.closed.Load() {
		t.Fatal("connection not closed when the reserved slot was occupied")
	}
	if err := c.TryEnqueue(TextMessage, []byte("after")); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("enqueue after close err = %v, want ErrConnClosed", err)
	}
}

func TestStallClearsWhenQueueAcceptsAgain(t *testing.T) {
	var transitions []bool
	var mu sync.Mutex
	c, f := fillQueue(t, 2)
	c.onStall = func(stalled bool) {
		mu.Lock()
		transitions = append(transitions, stalled)
		mu.Unlock()
	}
	defer c.Close()

	if err := c.TryEnqueue(TextMessage, []byte("busy")); !errors.Is(err, ErrStalled) {
		t.Fatalf("overflow err = %v, want ErrStalled", err)
	}
	if !c.Stalled() {
		t.Fatal("not stalled after overflow")
	}

	close(f.release)
	// Wait for the pump to drain everything (first + notice + 2 fills):
	// only then is the queue guaranteed to accept a frame again.
	waitWrites(t, f, 4)
	if err := c.TryEnqueue(TextMessage, []byte("again")); err != nil {
		t.Fatalf("enqueue after drain: %v", err)
	}
	if c.Stalled() {
		t.Fatal("stall flag not cleared after the queue accepted a frame")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(transitions) != 2 || transitions[0] != true || transitions[1] != false {
		t.Fatalf("OnStall transitions = %v, want [true false]", transitions)
	}
}

func TestWaitForRoomUnblocksWhenPumpDrains(t *testing.T) {
	c, f := fillQueue(t, 2)
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waited := make(chan error, 1)
	go func() { waited <- c.WaitForRoom(ctx) }()

	select {
	case err := <-waited:
		t.Fatalf("WaitForRoom returned while the queue was full: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(f.release)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("WaitForRoom after drain: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForRoom never unblocked after the pump drained")
	}
}

func TestWaitForRoomHonorsContext(t *testing.T) {
	c, _ := fillQueue(t, 2)
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() { waited <- c.WaitForRoom(ctx) }()
	cancel()

	select {
	case err := <-waited:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitForRoom err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForRoom never honored ctx cancellation")
	}
}

func TestBudgetExhaustionTripsStallPolicy(t *testing.T) {
	budget := NewBudget(10)
	f := newFakeSocket(true)
	c := New(f, Config{QueueDepth: 4, Budget: budget})
	defer c.Close()
	if err := c.TryEnqueue(TextMessage, []byte("123456")); err != nil {
		t.Fatalf("enqueue under budget: %v", err)
	}
	select {
	case <-f.writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never started")
	}
	// The pump holds "123456" (its reservation was released at dequeue,
	// so the budget stands at 0). "abcdef" reserves 6; the next 6-byte
	// frame exceeds the 10-byte cap.
	if err := c.TryEnqueue(TextMessage, []byte("abcdef")); err != nil {
		t.Fatalf("enqueue at half budget: %v", err)
	}
	if err := c.TryEnqueue(TextMessage, []byte("ghijkl")); !errors.Is(err, ErrStalled) {
		t.Fatalf("enqueue over budget err = %v, want ErrStalled", err)
	}
	if !c.Stalled() {
		t.Fatal("budget exhaustion did not mark the connection stalled")
	}
	close(f.release)
}

func TestBudgetBytesReleasedOnDequeue(t *testing.T) {
	budget := NewBudget(10)
	f := newFakeSocket(true)
	c := New(f, Config{QueueDepth: 4, Budget: budget})
	defer c.Close()
	if err := c.TryEnqueue(TextMessage, []byte("123456")); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	select {
	case <-f.writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never started")
	}
	if err := c.TryEnqueue(TextMessage, []byte("abcdef")); err != nil {
		t.Fatalf("enqueue queued: %v", err)
	}
	// The pump is blocked mid-write on "123456"; "abcdef" is queued with
	// 6 bytes reserved. Unblock: the pump dequeues "abcdef" and must
	// release its reservation, freeing the full budget again.
	close(f.release)
	waitWrites(t, f, 2)
	if err := c.TryEnqueue(TextMessage, []byte("0123456789")); err != nil {
		t.Fatalf("budget not released on dequeue: %v", err)
	}
}

func TestBudgetReleasedOnClose(t *testing.T) {
	budget := NewBudget(10)
	f := newFakeSocket(true)
	c := New(f, Config{QueueDepth: 4, Budget: budget})
	// Pump takes "123456" (released at dequeue), blocks mid-write.
	if err := c.TryEnqueue(TextMessage, []byte("123456")); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	select {
	case <-f.writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never started")
	}
	// Queue holds one 6-byte frame (budget reservation outstanding).
	if err := c.TryEnqueue(TextMessage, []byte("abcdef")); err != nil {
		t.Fatalf("enqueue queued: %v", err)
	}
	c.Close()
	// The budget must be back to zero: a fresh connection on the same
	// budget can enqueue a full 10 bytes.
	other := New(newFakeSocket(false), Config{Budget: budget})
	defer other.Close()
	if err := other.TryEnqueue(TextMessage, []byte("0123456789")); err != nil {
		t.Fatalf("budget not released on close: %v", err)
	}
}

func TestCloseStopsPumpAndRejectsFrames(t *testing.T) {
	f := newFakeSocket(false)
	c := New(f, Config{})
	defer c.Close()
	if err := c.TryEnqueue(TextMessage, []byte("x")); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	waitWrites(t, f, 1)
	c.Close()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not exit after Close")
	}
	if err := c.TryEnqueue(TextMessage, []byte("y")); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("enqueue after close err = %v, want ErrConnClosed", err)
	}
	if !f.closed.Load() {
		t.Fatal("Close did not close the socket")
	}
}

func TestOneStuckConnectionDoesNotDelayAnother(t *testing.T) {
	healthy := newFakeSocket(false)
	c1, _ := fillQueue(t, 2)
	c2 := New(healthy, Config{QueueDepth: 64})
	defer c1.Close()
	defer c2.Close()

	// Stall c1 thoroughly.
	if err := c1.TryEnqueue(TextMessage, []byte("busy")); !errors.Is(err, ErrStalled) {
		t.Fatalf("c1 overflow err = %v", err)
	}

	// c2 must keep delivering while c1 is wedged.
	start := time.Now()
	for i := 0; i < 10; i++ {
		if err := c2.TryEnqueue(TextMessage, []byte("tick")); err != nil {
			t.Fatalf("c2 enqueue %d: %v", i, err)
		}
	}
	waitWrites(t, healthy, 10)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("healthy connection delayed by a stuck one: %v", elapsed)
	}
}

// ── responses: reserved capacity, never silence ─────────────────────────

// A JSON-RPC response is the other half of a promise: dropping it strands
// the caller forever. A saturated data queue (PTY output, say) must not be
// able to consume the capacity a response needs — this is the e2e failure
// (the Commits list never populating) reproduced at the queue level.
func TestResponseSurvivesSaturatedDataQueue(t *testing.T) {
	c, f := fillQueue(t, 4)
	defer c.Close()

	resp := []byte(`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`)
	// The pump is stuck mid-write on "first" and all 4 queue slots hold
	// "fill" frames: the refreshable data plane is saturated. The response
	// must still be accepted — it has reserved capacity of its own.
	if err := c.TryEnqueueResponse(resp); err != nil {
		t.Fatalf("response enqueue on a saturated data queue: %v", err)
	}
	if c.Stalled() {
		t.Fatal("a response accepted into reserved capacity must not mark the data queue stalled")
	}

	close(f.release)
	writes := waitWrites(t, f, 6)
	if !bytes.Equal(writes[1].Data, resp) {
		t.Fatalf("response not written ahead of the queued data; write 1 = %q", writes[1].Data)
	}
	for _, w := range writes[2:] {
		if string(w.Data) != "fill" {
			t.Fatalf("queued data frame lost or reordered: %q", w.Data)
		}
	}
}

// If even the reserved response capacity is exhausted, the connection must
// close — never drop the response: the renderer's disconnect/reconnect
// surface then rejects the caller's pending promise. Silence is the one
// outcome that must not survive.
func TestResponseOverflowClosesConnection(t *testing.T) {
	f := newFakeSocket(true)
	c := New(f, Config{})
	defer c.Close()
	if err := c.TryEnqueue(TextMessage, []byte("first")); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	select {
	case <-f.writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never started its first write")
	}
	// The pump is stuck, so the response queue cannot drain. Fill it.
	for i := range DefaultResponseQueueDepth {
		if err := c.TryEnqueueResponse([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)); err != nil {
			t.Fatalf("fill response %d: %v", i, err)
		}
	}
	if err := c.TryEnqueueResponse([]byte(`{"jsonrpc":"2.0","id":2,"result":{}}`)); !errors.Is(err, ErrStalled) {
		t.Fatalf("overflow response err = %v, want ErrStalled", err)
	}
	if !f.closed.Load() {
		t.Fatal("connection not closed on response overflow — a dropped response would strand the caller forever")
	}
	if err := c.TryEnqueueResponse([]byte(`{"jsonrpc":"2.0","id":3,"result":{}}`)); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("response after close err = %v, want ErrConnClosed", err)
	}
}

// The process-wide byte budget applies to responses too; when it cannot
// hold one, the same never-silence rule closes the connection.
func TestResponseBudgetExhaustionClosesConnection(t *testing.T) {
	f := newFakeSocket(false)
	c := New(f, Config{Budget: NewBudget(5)})
	defer c.Close()
	if err := c.TryEnqueueResponse([]byte("123456")); !errors.Is(err, ErrStalled) {
		t.Fatalf("response over budget err = %v, want ErrStalled", err)
	}
	if !f.closed.Load() {
		t.Fatal("connection not closed when the budget cannot hold a response")
	}
}
