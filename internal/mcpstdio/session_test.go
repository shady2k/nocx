package mcpstdio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// THE DEFECT THIS FILE EXISTS FOR (nocx-tlaft). With a long poll outstanding,
// the call a coordinator reaches for to see what the poll is doing was the one
// call that could not run: Serve chained every request behind the one before
// it, and every request dialled its own endpoint connection — where the
// endpoint admits a peer once per connection and the session's caller slot is
// held for the connection's life. So the second call either queued forever or
// was refused as an active caller, and the coordinator lost the tool it needed
// exactly when it needed it.
//
// Two properties are asserted here, and they are independent: calls in flight
// together do not wait for each other, and they share ONE connection.

// twoTools is the catalogue every test here works from: a name the endpoint
// holds open, and a name it answers at once.
const twoTools = `{"tools":[{"name":"alpha.first","summary":"first","params":{"type":"object"},"result":{"type":"object"}},{"name":"alpha.second","summary":"second","params":{"type":"object"},"result":{"type":"object"}}]}`

// stdioDriver drives the adapter the way a client does: one line in, one line
// out, with both boundaries under the test's control. The batch helper in
// mcpstdio_test.go hands the whole session over in a single write, which
// cannot express "send this, wait for that, then send the next" — and what
// this file is about is what the adapter does while a request is outstanding.
type stdioDriver struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan []byte
	ended chan error
}

func startStdio(t *testing.T, socket string) *stdioDriver {
	t.Helper()
	input, inputWriter := io.Pipe()
	output, outputWriter := io.Pipe()
	driver := &stdioDriver{
		t:     t,
		in:    inputWriter,
		lines: make(chan []byte, 32),
		ended: make(chan error, 1),
	}
	go func() {
		err := Serve(context.Background(), input, outputWriter, socket)
		_ = outputWriter.Close()
		driver.ended <- err
	}()
	go func() {
		reader := bufio.NewReader(output)
		for {
			line, err := reader.ReadBytes('\n')
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				driver.lines <- trimmed
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = inputWriter.Close()
		_ = output.Close()
	})
	return driver
}

func (d *stdioDriver) send(id int, method, params string) {
	d.t.Helper()
	d.write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params))
}

func (d *stdioDriver) notify(method, params string) {
	d.t.Helper()
	d.write(fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":%s}`, method, params))
}

func (d *stdioDriver) write(line string) {
	d.t.Helper()
	if _, err := io.WriteString(d.in, line+"\n"); err != nil {
		d.t.Fatalf("write %s: %v", line, err)
	}
}

// next reads the adapter's next response line.
func (d *stdioDriver) next() map[string]json.RawMessage {
	d.t.Helper()
	select {
	case line := <-d.lines:
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(line, &envelope); err != nil {
			d.t.Fatalf("adapter wrote %q: %v", line, err)
		}
		return envelope
	case err := <-d.ended:
		d.t.Fatalf("the adapter ended before answering: %v", err)
	case <-time.After(5 * time.Second):
		d.t.Fatal("the adapter wrote no response")
	}
	return nil
}

// nextID reads the next response and requires it to be this one.
func (d *stdioDriver) nextID(want int) map[string]json.RawMessage {
	d.t.Helper()
	envelope := d.next()
	if got := string(envelope["id"]); got != strconv.Itoa(want) {
		d.t.Fatalf("response id = %s, want %d: %v", got, want, envelope)
	}
	return envelope
}

// quiet asserts the adapter writes nothing for a window. The window is the
// OBSERVATION, and the assertion it guards is the count or the answer that
// follows: an adapter that races the state it should have waited for produces
// its line inside this window, and one that waits produces nothing and simply
// pays the window.
func (d *stdioDriver) quiet(window time.Duration) {
	d.t.Helper()
	select {
	case line := <-d.lines:
		d.t.Fatalf("the adapter answered while it should have been waiting: %s", line)
	case <-time.After(window):
	}
}

// toolText returns the text of a tool result and refuses a refused call.
func toolText(t *testing.T, envelope map[string]json.RawMessage) string {
	t.Helper()
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(envelope["result"], &result); err != nil {
		t.Fatalf("decode result of %v: %v", envelope, err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("tool call was refused: %v", envelope)
	}
	return result.Content[0].Text
}

func initialization(d *stdioDriver) {
	d.t.Helper()
	d.send(0, "initialize", `{"protocolVersion":"2025-11-25"}`)
	d.nextID(0)
	d.send(1, "tools/list", `{}`)
	d.nextID(1)
}

// scriptedEndpoint serves every request a connection carries, each on its own
// goroutine — the shape the real endpoint has and the adapter now depends on:
// a call a test holds open does not stop the connection from carrying the
// next one, and no request is dropped because the fake stopped reading.
type scriptedEndpoint struct {
	listener *net.UnixListener

	mu    sync.Mutex
	dials int

	answer func(net.Conn, rpcEnvelope)
}

func startScriptedEndpoint(t *testing.T, answer func(net.Conn, rpcEnvelope)) *scriptedEndpoint {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(t.TempDir(), "endpoint.sock"), Net: "unix"})
	if err != nil {
		t.Fatalf("listen on endpoint socket: %v", err)
	}
	endpoint := &scriptedEndpoint{listener: listener, answer: answer}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			endpoint.mu.Lock()
			endpoint.dials++
			endpoint.mu.Unlock()
			go endpoint.serve(conn)
		}
	}()
	return endpoint
}

func (e *scriptedEndpoint) socket() string { return e.listener.Addr().String() }

// dialCount is how many connections the adapter made. It is the count that
// catches a regression back to one connection per call, which a timing could
// not.
func (e *scriptedEndpoint) dialCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dials
}

func (e *scriptedEndpoint) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	for {
		request, err := readJSONLine(reader)
		if err != nil {
			return
		}
		go e.answer(conn, request)
	}
}

// A CALL IN FLIGHT MUST NOT BE A WALL. The reported failure (owner,
// 2026-09-11): with a wait outstanding for minutes, the call that asks what
// the session is doing sat for 120 seconds and ended when the connection
// closed. Here the endpoint holds one call and answers the next one while it
// is still held.
func TestASecondCallIsAnsweredWhileTheFirstIsHeld(t *testing.T) {
	held := make(chan struct{}, 1)
	release := make(chan struct{})
	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		switch request.Method {
		case "tools.catalogue":
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(twoTools)})
		case "alpha.first":
			held <- struct{}{}
			<-release
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"first":true}`)})
		default:
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"second":true}`)})
		}
	})
	driver := startStdio(t, endpoint.socket())
	initialization(driver)

	driver.send(2, "tools/call", `{"name":"alpha.first"}`)
	<-held
	driver.send(3, "tools/call", `{"name":"alpha.second"}`)
	if text := toolText(t, driver.nextID(3)); text != `{"second":true}` {
		t.Fatalf("the second call answered %s", text)
	}

	close(release)
	if text := toolText(t, driver.nextID(2)); text != `{"first":true}` {
		t.Fatalf("the held call answered %s", text)
	}
}

// ONE CONNECTION, AND ANSWERS MATCHED BY ID. The endpoint answers what it
// finishes first, so with two calls in flight the second answer can arrive
// before the first. The adapter must hand each to its own caller, and it must
// have asked for both over the one connection the session holds — a second
// dial is what the authority interval forbids.
func TestOverlappingCallsShareOneConnectionAndAnswerByID(t *testing.T) {
	firstSeen := make(chan struct{})
	secondSeen := make(chan struct{})
	secondAnswered := make(chan struct{})
	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		switch request.Method {
		case "tools.catalogue":
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(twoTools)})
		case "alpha.first":
			close(firstSeen)
			<-secondAnswered
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"first":true}`)})
		case "alpha.second":
			close(secondSeen)
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"second":true}`)})
			close(secondAnswered)
		}
	})
	driver := startStdio(t, endpoint.socket())
	initialization(driver)

	driver.send(2, "tools/call", `{"name":"alpha.first"}`)
	<-firstSeen
	driver.send(3, "tools/call", `{"name":"alpha.second"}`)
	<-secondSeen
	if got := endpoint.dialCount(); got != 1 {
		t.Fatalf("endpoint connections = %d, want 1: calls in flight together share the session's connection", got)
	}

	answers := make(map[string]string, 2)
	for range 2 {
		envelope := driver.next()
		answers[string(envelope["id"])] = toolText(t, envelope)
	}
	if answers["2"] != `{"first":true}` || answers["3"] != `{"second":true}` {
		t.Fatalf("answers by id = %v, want each caller to have its own", answers)
	}
}

// A CALL ARRIVING WHILE A LIST IS STILL RUNNING USES THAT LIST'S CATALOGUE.
// The list is the only state a later call reads, so it is the only thing a
// call waits for — and it must wait rather than load a catalogue of its own
// beside it.
func TestACallWaitsForTheListInFlightAndUsesItsCatalogue(t *testing.T) {
	listSeen := make(chan struct{})
	listRelease := make(chan struct{})
	var catalogues atomic.Int64
	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		if request.Method == "tools.catalogue" {
			if catalogues.Add(1) == 1 {
				close(listSeen)
				<-listRelease
			}
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(twoTools)})
			return
		}
		writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(fmt.Sprintf(`{"ran":%q}`, request.Method))})
	})
	driver := startStdio(t, endpoint.socket())
	driver.send(0, "initialize", `{"protocolVersion":"2025-11-25"}`)
	driver.nextID(0)

	driver.send(1, "tools/list", `{}`)
	<-listSeen
	driver.send(2, "tools/call", `{"name":"alpha.first"}`)
	driver.quiet(250 * time.Millisecond)

	close(listRelease)
	list := driver.nextID(1)
	if list["error"] != nil || !strings.Contains(string(list["result"]), "alpha.first") {
		t.Fatalf("the list did not answer with the catalogue: %v", list)
	}
	if text := toolText(t, driver.nextID(2)); text != `{"ran":"alpha.first"}` {
		t.Fatalf("the call answered %s", text)
	}
	if got := catalogues.Load(); got != 1 {
		t.Fatalf("endpoint catalogue requests = %d, want 1: the call loaded its own catalogue instead of using the list's", got)
	}
}

// CANCELLING ONE CALL IS NOT CANCELLING THE OTHER. The adapter used to tie the
// connection's life to the caller's context, which on a shared connection
// would take every sibling down with the cancelled call.
func TestCancellingOneCallLeavesItsSiblingAlone(t *testing.T) {
	held := make(chan struct{}, 2)
	release := make(chan struct{})
	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		switch request.Method {
		case "tools.catalogue":
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(twoTools)})
		case "alpha.first":
			held <- struct{}{}
			<-release
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"first":true}`)})
		case "alpha.second":
			held <- struct{}{}
			<-release
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"second":true}`)})
		}
	})
	driver := startStdio(t, endpoint.socket())
	initialization(driver)

	driver.send(2, "tools/call", `{"name":"alpha.first"}`)
	driver.send(3, "tools/call", `{"name":"alpha.second"}`)
	<-held
	<-held

	driver.notify("notifications/cancelled", `{"requestId":2,"reason":"no longer needed"}`)
	// The notification travels the same pipe as the requests, so it is
	// PROCESSED in that order — but the write returns as soon as a reader has
	// the bytes, which is before the adapter has handled them. A request the
	// adapter answers itself is the fence: its answer can only follow the
	// notification, because one read loop handles both in order.
	driver.send(9, "not.a.method", `{}`)
	driver.nextID(9)
	// The endpoint answers both calls anyway. MCP carries no response for a
	// cancelled request, so what the adapter must write next is the sibling's
	// answer — and nothing at all for the cancelled id.
	close(release)
	if text := toolText(t, driver.nextID(3)); text != `{"second":true}` {
		t.Fatalf("the sibling answered %s after its own call was cancelled", text)
	}
	driver.quiet(250 * time.Millisecond)
}

// THE ENDPOINT CLOSES AN IDLE CONNECTION, and that is ordinary: its read
// deadline is eleven minutes wide while nothing is in flight, so a coordinator
// that has been quiet finds its connection gone the next time it calls. That
// call reconnects; an endpoint that went idle is not an endpoint that went
// away, and "endpoint unavailable" would put the failure in front of the one
// caller who could not have caused it.
func TestTheNextCallRedialsWhenTheEndpointClosedTheIdleConnection(t *testing.T) {
	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		if request.Method == "tools.catalogue" {
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(twoTools)})
			_ = conn.Close()
			return
		}
		writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"after":true}`)})
	})
	driver := startStdio(t, endpoint.socket())
	initialization(driver)

	driver.send(2, "tools/call", `{"name":"alpha.second"}`)
	if text := toolText(t, driver.nextID(2)); text != `{"after":true}` {
		t.Fatalf("the call after an idle close answered %s", text)
	}
	if got := endpoint.dialCount(); got != 2 {
		t.Fatalf("endpoint connections = %d, want 2: the closed connection and the dial the next call made", got)
	}
}

// A DIAL THAT GENUINELY FAILS IS NAMED, and it is the same answer the wire has
// always carried for it.
func TestADialThatFailsIsNamedEndpointUnavailable(t *testing.T) {
	link := newEndpointLink(filepath.Join(t.TempDir(), "missing.sock"), &net.Dialer{})
	_, _, err := link.call(context.Background(), "alpha.first", json.RawMessage(`{}`))
	if !errors.Is(err, ErrEndpointUnavailable) {
		t.Fatalf("call error = %v, want %v", err, ErrEndpointUnavailable)
	}
}

// firstWriteFailsDialer hands out one connection whose every write fails — an
// endpoint that is already gone while this process has not been told — and the
// real dial for every connection after it.
type firstWriteFailsDialer struct {
	inner Dialer
	mu    sync.Mutex
	calls int
}

func (d *firstWriteFailsDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.calls++
	first := d.calls == 1
	d.mu.Unlock()
	if !first {
		return d.inner.DialContext(ctx, network, address)
	}
	conn, peer := net.Pipe()
	_ = peer.Close()
	_ = conn.Close()
	return conn, nil
}

func (d *firstWriteFailsDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// A WRITE THAT FAILS IS NOT A LOST CALL. The connection can be gone in the
// instant between the endpoint's read deadline expiring and this process
// learning that it is: the write is the first thing to find out. The call
// dials again instead of reporting the endpoint, because a request the write
// never delivered cannot have run anything twice.
func TestACallWhoseWriteFailsRedialsRatherThanLosingTheCall(t *testing.T) {
	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"after":true}`)})
	})
	dialer := &firstWriteFailsDialer{inner: &net.Dialer{}}
	link := newEndpointLink(endpoint.socket(), dialer)
	t.Cleanup(link.close)

	result, upstream, err := link.call(context.Background(), "alpha.first", json.RawMessage(`{}`))
	if err != nil || upstream != nil {
		t.Fatalf("call after a failed write: result=%s upstream=%v err=%v", result, upstream, err)
	}
	if string(result) != `{"after":true}` {
		t.Fatalf("call result = %s", result)
	}
	if got := dialer.count(); got != 2 {
		t.Fatalf("dials = %d, want 2: the connection that failed the write and the one the retry made", got)
	}
}
