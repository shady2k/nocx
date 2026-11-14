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

// A write is the publication interval, not just the preceding admission check.
func TestFix2ProgressWriteSerializesCancellationElection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	flight := &callFlight{}
	calls := newCalls()
	calls.add(json.RawMessage(`2`), cancel, flight)
	out := &fix2BlockedOutput{entered: make(chan struct{}), closing: make(chan struct{}), closed: make(chan struct{}), releaseClose: make(chan struct{})}
	writer := (&lineWriter{output: out}).forCall(ctx, flight)
	written := make(chan error, 1)
	go func() { written <- writer.progress(ctx, ctx, json.RawMessage(`"tok"`), 1) }()
	<-out.entered
	cancelled := make(chan struct{})
	go func() { calls.cancel("2"); close(cancelled) }()
	<-ctx.Done()
	<-out.closing
	// Cancellation may interrupt this write, but cannot win its election while
	// publication still holds the flight. The output is closed to unblock it.
	if flight.mu.TryLock() {
		elected := flight.cancelled
		flight.mu.Unlock()
		close(out.releaseClose)
		<-written
		<-cancelled
		calls.sends.Wait()
		t.Fatalf("cancellation elected=%v while progress publication was active", elected)
	}
	close(out.releaseClose)
	select {
	case <-written:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not interrupt progress output")
	}
	<-cancelled
	calls.sends.Wait()
}

type fix2BlockedOutput struct {
	entered      chan struct{}
	closing      chan struct{}
	releaseClose chan struct{}
	closed       chan struct{}
	once         sync.Once
}

func (o *fix2BlockedOutput) Write([]byte) (int, error) {
	close(o.entered)
	<-o.closed
	return 0, io.ErrClosedPipe
}

func (o *fix2BlockedOutput) Close() error {
	o.once.Do(func() { close(o.closing); <-o.releaseClose; close(o.closed) })
	return nil
}

func TestFix2CancelledFlightWithLiveContextCannotPublish(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "progress", true: "response"}[terminal], func(t *testing.T) {
			flight := &callFlight{}
			flight.cancel()
			ctx := context.Background()
			output := &recordingWriter{written: make(chan struct{}, 4)}
			writer := (&lineWriter{output: output}).forCall(ctx, flight)
			err := writer.writeFrame(map[string]any{"jsonrpc": "2.0"}, terminal)
			if err == nil || output.String() != "" {
				t.Fatalf("cancelled flight published with live context: err=%v output=%q", err, output.String())
			}
			if ctx.Err() != nil || flight.terminal {
				t.Fatal("test did not isolate cancellation admission from terminal/context")
			}
		})
	}
}

// The client can observe a frame before Write returns. Reusing its ID then
// must reach the endpoint without waiting for the previous handler's defers.
func TestFix2RequestIDRetiredBeforeResponsePublication(t *testing.T) {
	reached := make(chan struct{})
	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		close(reached)
		writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"tools":[]}`)})
	})
	server, _ := New(Config{Socket: endpoint.listener.Addr().String()})
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	driver := startStdioServing(t, func(input io.Reader, output io.WriteCloser) error {
		return server.Serve(context.Background(), input, &fix2PublishedOutput{WriteCloser: output, entered: entered, release: release})
	})
	driver.send(2, "unsupported", `{}`)
	driver.nextID(2)
	<-entered
	driver.send(2, "tools/list", `{}`)
	select {
	case <-reached:
	case <-time.After(time.Second):
		t.Fatal("reused ID was dropped while previous response Write had not returned")
	}
	close(release)
	if response := driver.nextID(2); response["result"] == nil {
		t.Fatalf("reused request did not answer: %v", response)
	}
}

type fix2PublishedOutput struct {
	io.WriteCloser
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (o *fix2PublishedOutput) Write(p []byte) (int, error) {
	n, err := o.WriteCloser.Write(p)
	o.once.Do(func() { close(o.entered); <-o.release })
	return n, err
}

func TestFix2LiveDuplicateRequestGetsErrorWithoutReplacingOwner(t *testing.T) {
	held, release := make(chan struct{}), make(chan struct{})
	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		close(held)
		<-release
		writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"tools":[]}`)})
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	driver := startStdio(t, endpoint.listener.Addr().String())
	driver.send(2, "tools/list", `{}`)
	<-held
	driver.send(2, "unsupported", `{}`)
	response := driver.nextID(2)
	var failure struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(response["error"], &failure) != nil || failure.Code != -32600 {
		t.Fatalf("duplicate did not receive invalid-request error: %v", response)
	}
	close(release)
	original := driver.nextID(2)
	if original["result"] == nil {
		t.Fatalf("duplicate replaced original owner: %v", original)
	}
}

// Done blocks only while the interruption hook is installed, after admission.
// Cancelling there must not let an immediately writable output accept a frame.
type fix2SetupContext struct {
	context.Context
	entered chan struct{}
	release chan struct{}
}

func (c *fix2SetupContext) Done() <-chan struct{} {
	close(c.entered)
	<-c.release
	return c.Context.Done()
}

func TestFix2CancellationDuringProgressSetupWritesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	setup := &fix2SetupContext{Context: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	output := &recordingWriter{written: make(chan struct{}, 4)}
	writer := (&lineWriter{output: output}).forCall(ctx, &callFlight{})
	done := make(chan error, 1)
	go func() { done <- writer.progress(ctx, setup, json.RawMessage(`"tok"`), 1) }()
	<-setup.entered
	cancel()
	close(setup.release)
	<-done
	if output.String() != "" {
		t.Fatalf("progress published after cancellation during setup: %q", output.String())
	}
}
