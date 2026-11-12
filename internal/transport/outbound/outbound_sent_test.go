package outbound

import (
	"sync"
	"testing"
	"time"
)

// A stored-row response holds its slot until the frame has LEFT the socket,
// not until the handler has handed it over (owner, 2026-11-10). The pump is
// held mid-write here, so "handed over" and "sent" are separate moments and
// the callback is the only witness of the second.
func TestResponseSentCallbackFiresOnlyAfterTheWriteReturns(t *testing.T) {
	f := newFakeSocket(true)
	c := New(f, Config{})
	defer c.Close()

	var (
		mu   sync.Mutex
		sent bool
	)
	called := make(chan struct{})
	if err := c.TryEnqueueResponseFrameSent(TextMessage, []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), func() {
		mu.Lock()
		sent = true
		mu.Unlock()
		close(called)
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	select {
	case <-f.writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never started the write")
	}
	select {
	case <-called:
		t.Fatal("the callback fired while the write was still in the socket")
	case <-time.After(100 * time.Millisecond):
	}
	mu.Lock()
	fired := sent
	mu.Unlock()
	if fired {
		t.Fatal("the callback fired while the write was still in the socket")
	}

	close(f.release)
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("the callback never fired after the write returned")
	}
}

// A connection that closes with a sent-callback frame still queued must run
// the callback: the slot belongs to the response, and a response that will
// never be written has no slot to hold (the refusal answer's own line: the
// server neither queues without bound nor keeps capacity nobody will spend).
func TestResponseSentCallbackFiresWhenTheConnectionClosesBeforeTheWrite(t *testing.T) {
	f := newFakeSocket(true)
	c := New(f, Config{})
	if err := c.TryEnqueue(TextMessage, []byte("first")); err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	select {
	case <-f.writeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("pump never started its first write")
	}

	called := make(chan struct{})
	if err := c.TryEnqueueResponseFrameSent(TextMessage, []byte(`{"jsonrpc":"2.0","id":2,"result":{}}`), func() {
		close(called)
	}); err != nil {
		t.Fatalf("enqueue sent-callback frame: %v", err)
	}
	c.Close()
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the connection left the sent callback unfired")
	}
}

// A successful enqueue and Close are one decision. If enqueue wins, Close must
// drain the frame and run its witness; if Close wins, the enqueue is refused
// and the wrapper runs the witness itself. A success whose frame is left in a
// queue nobody drains would leak the stored-row slot.
func TestSuccessfulEnqueueRacingCloseAlwaysRunsItsCompletion(t *testing.T) {
	for i := 0; i < 1500; i++ {
		c := New(newFakeSocket(false), Config{})
		sent := make(chan struct{})
		var success bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			success = c.TryEnqueueResponseFrameSent(TextMessage, []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`), func() {
				close(sent)
			}) == nil
		}()
		go func() {
			defer wg.Done()
			c.Close()
		}()
		wg.Wait()
		select {
		case <-sent:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: enqueue returned success=%v but its completion never ran", i, success)
		}
	}
}
