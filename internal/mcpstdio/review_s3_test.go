package mcpstdio

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

type shortCancelConn struct{ net.Conn }

func (c shortCancelConn) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestShortCancelWriteRetiresConnection(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	c := newEndpointConn(shortCancelConn{a})
	defer c.close()
	c.cancelRequest(3)
	if !c.gone() {
		t.Fatal("short cancel frame left a corrupted connection alive")
	}
}

func TestCancelBoundsWaitingForSiblingWrite(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	c := newEndpointConn(a)
	defer c.close()
	c.writeTurn.Lock()
	done := make(chan struct{})
	go func() { c.cancelRequest(3); close(done) }()
	// The same total lock-and-write bound is used in production.
	select {
	case <-done:
	case <-time.After(cancelWriteWindow + time.Second):
		c.writeTurn.Unlock()
		<-done
		t.Fatal("cancel waited beyond its bound for sibling write")
	}
	c.writeTurn.Unlock()
	if !c.gone() {
		t.Fatal("undeliverable cancel did not retire connection")
	}
}

func TestCancelledResponseQueuedBehindOutputWritesNothing(t *testing.T) {
	for _, kind := range []string{"success", "upstream-error", "tool-error"} {
		t.Run(kind, func(t *testing.T) {
			out := &recordingWriter{written: make(chan struct{}, 4)}
			w := &lineWriter{output: out}
			ctx, cancel := context.WithCancel(context.Background())
			// Holding output admission models an unrelated frame already writing.
			w.mu.Lock()
			flight := &callFlight{}
			observed := &observedWaitContext{Context: ctx, waiting: make(chan struct{})}
			scoped := w.forCall(observed, flight)
			done := make(chan error, 1)
			go func() {
				switch kind {
				case "success":
					done <- scoped.toolResult(json.RawMessage(`2`), json.RawMessage(`{}`), true)
				case "upstream-error":
					done <- scoped.errorResponse(json.RawMessage(`2`), -32603, "broken", "broken")
				default:
					done <- scoped.toolError(json.RawMessage(`2`), "broken")
				}
			}()
			<-observed.waiting
			flight.mu.Lock()
			flight.cancelled = true
			cancel()
			flight.mu.Unlock()
			w.mu.Unlock()
			<-done
			if out.String() != "" {
				t.Fatal("cancelled response left output after cancellation won")
			}
		})
	}
}

func TestProgressStopInterruptsBlockedOutput(t *testing.T) {
	clock := newProgressClock()
	reader, output := io.Pipe()
	defer func() { _ = reader.Close() }()
	entered := make(chan struct{})
	writer := &observedOutput{WriteCloser: output, entered: entered}
	server := &Server{progressTicks: clock.seam}
	ctx, cancel := context.WithCancel(context.Background())
	p := server.startProgress(ctx, &lineWriter{output: writer}, json.RawMessage(`"tok"`))
	clock.tick()
	<-entered
	cancel()
	done := make(chan struct{})
	go func() { p.stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("progress.stop leaked a sender in blocked output")
	}
	clock.waitStopped(t)
}

type observedOutput struct {
	io.WriteCloser
	entered   chan struct{}
	once      sync.Once
	closed    chan struct{}
	closeOnce sync.Once
}

func (o *observedOutput) Close() error {
	err := o.WriteCloser.Close()
	if o.closed != nil {
		o.closeOnce.Do(func() { close(o.closed) })
	}
	return err
}

func (o *observedOutput) Write(p []byte) (int, error) {
	o.once.Do(func() { close(o.entered) })
	return o.WriteCloser.Write(p)
}

func TestProgressQueuedAtOutputStopsWithoutWriting(t *testing.T) {
	output := &recordingWriter{written: make(chan struct{}, 4)}
	writer := &lineWriter{output: output}
	writer.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	observed := &observedWaitContext{Context: ctx, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- writer.progress(observed, ctx, json.RawMessage(`"tok"`), 1) }()
	<-observed.waiting
	cancel()
	<-done
	writer.mu.Unlock()
	if output.String() != "" {
		t.Fatal("queued progress wrote after cancellation won admission")
	}
}

func TestActiveCallRemovalIsOwnedAndDuplicateIsRefused(t *testing.T) {
	calls := newCalls()
	id := json.RawMessage(`2`)
	old := calls.add(id, func() {}, &callFlight{})
	if calls.add(id, func() {}, &callFlight{}) != nil {
		t.Fatal("duplicate live id replaced its owner")
	}
	calls.remove(id, old)
	fresh := calls.add(id, func() {}, &callFlight{})
	calls.remove(id, old)
	if calls.calls["2"] != fresh {
		t.Fatal("previous invocation removed reused id")
	}
}

// observedWaitContext signals the admission select, after frame marshaling.
type observedWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

type failingCancelConn struct{ net.Conn }

func (c failingCancelConn) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestFailedCancelWriteRetiresConnection(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	c := newEndpointConn(failingCancelConn{a})
	defer c.close()
	c.cancelRequest(2)
	if !c.gone() {
		t.Fatal("failed cancellation left connection alive")
	}
}

func TestCancelBoundsBlockedWrite(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	c := newEndpointConn(a)
	defer c.close()
	done := make(chan struct{})
	go func() { c.cancelRequest(2); close(done) }()
	select {
	case <-done:
	case <-time.After(cancelWriteWindow + time.Second):
		t.Fatal("cancel write outlived bound")
	}
	if !c.gone() {
		t.Fatal("blocked cancel write left connection alive")
	}
}

func TestProgressStopBoundsStalledOutput(t *testing.T) {
	clock := newProgressClock()
	reader, output := io.Pipe()
	defer func() { _ = reader.Close() }()
	writer := &observedOutput{WriteCloser: output, entered: make(chan struct{}), closed: make(chan struct{})}
	p := (&Server{progressTicks: clock.seam}).startProgress(context.Background(), &lineWriter{output: writer}, json.RawMessage(`"tok"`))
	clock.tick()
	<-writer.entered
	done := make(chan struct{})
	go func() { p.stop(); close(done) }()
	select {
	case <-writer.closed:
	case <-time.After(outputWriteWindow + time.Second):
		t.Fatal("stalled output watchdog never closed stream")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("progress stop did not join interrupted write")
	}
}
