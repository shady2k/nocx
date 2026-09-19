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
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/storage/storagetest"
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
	return startStdioServing(t, func(input io.Reader, output io.Writer) error {
		return Serve(context.Background(), input, output, socket, discardLogger)
	})
}

// startStdioDialing is startStdio with the adapter's own dialer in the test's
// hands, for a test whose precondition is something only the ADAPTER's side of
// the connection can see.
func startStdioDialing(t *testing.T, socket string, dialer Dialer) *stdioDriver {
	t.Helper()
	server, err := New(Config{Socket: socket, Dialer: dialer, Logger: discardLogger})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return startStdioServing(t, func(input io.Reader, output io.Writer) error {
		return server.Serve(context.Background(), input, output)
	})
}

func startStdioServing(t *testing.T, serve func(io.Reader, io.Writer) error) *stdioDriver {
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
		err := serve(input, outputWriter)
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
	// onEnd is called when a connection's read loop stops, which is how a test
	// observes that the CLIENT closed a socket: the descriptor's life is
	// invisible from this side, and its survival is exactly what leaves the
	// endpoint holding an admission interval nobody will use.
	onEnd func(net.Conn)
}

// startScriptedEndpointOption adjusts the fake before it starts accepting, so a
// test that needs to observe something the fake does not otherwise report can
// say so without every other test paying for it.
type startScriptedEndpointOption func(*scriptedEndpoint)

// withEndObserver reports each connection whose read loop stopped.
func withEndObserver(observe func(net.Conn)) startScriptedEndpointOption {
	return func(e *scriptedEndpoint) { e.onEnd = observe }
}

func startScriptedEndpoint(t *testing.T, answer func(net.Conn, rpcEnvelope), options ...startScriptedEndpointOption) *scriptedEndpoint {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(storagetest.SocketDir(t), "endpoint.sock"), Net: "unix"})
	if err != nil {
		t.Fatalf("listen on endpoint socket: %v", err)
	}
	endpoint := &scriptedEndpoint{listener: listener, answer: answer}
	for _, option := range options {
		option(endpoint)
	}
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
	defer func() {
		_ = conn.Close()
		if e.onEnd != nil {
			e.onEnd(conn)
		}
	}()
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
//
// Every assertion here is positive: the list answers, the call answers, and
// the endpoint was asked for the catalogue once. What a duration would have
// added — that the call had its chance to bypass the list first — is not
// evidence a test can hold without depending on timing, and the rule that
// makes the call wait is proved deterministically in
// TestACancelledWriterDoesNotOpenItsTurn and the gate it drives.
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
	// answer — and the id check is what proves it: a response for the cancelled
	// call would have to arrive here to be seen at all.
	close(release)
	if text := toolText(t, driver.nextID(3)); text != `{"second":true}` {
		t.Fatalf("the sibling answered %s after its own call was cancelled", text)
	}
	driver.send(4, "tools/call", `{"name":"alpha.second"}`)
	if text := toolText(t, driver.nextID(4)); text != `{"second":true}` {
		t.Fatalf("the session answered %s after a cancellation", text)
	}
}

// interruptConn is a socket whose write deadline behaves the way a real one
// does — it fails the write it is in force for — and whose two moments are the
// TEST's rather than the scheduler's.
//
// The window it exists to hold open is the one CI failed in (nocx-xn63t.6.14).
// There, a cancelled caller's interrupt was armed before its write and fired
// after it: the callback is asynchronous, and the goroutine that armed it need
// only be descheduled between the write and the disarm that would have
// withdrawn it, so the deadline it set was in force on the SHARED connection
// with no write of its own left to interrupt. The next writer paid for it — a
// sibling of the cancellation, whose whole frame was rejected with i/o timeout
// and never reached the endpoint, and which then waited for a connection that
// had never errored.
//
// Here the first frame is held INSIDE the write, so a cancellation can be made
// to land while that write is in flight instead of being raced for; and the
// CLEARING of the deadline is held too, which is the state CI reached by luck:
// a deadline armed by a cancelled call and not yet withdrawn, with a sibling
// wanting to write. An interrupt armed after the write had returned would have
// been withdrawn by stop and nothing would be armed at all, which is why the
// write is held rather than the cancellation delayed.
type interruptConn struct {
	mu sync.Mutex
	// armed is a write deadline in the past, in force until it is cleared.
	armed bool
	// writes counts the frames this connection was asked to take.
	writes int
	// armedWrites counts frames a deadline SOMEONE ELSE armed refused. It is
	// the defect as a number, and it must stay zero.
	armedWrites int

	// inFlight takes the first frame, which is then held until the interrupt
	// arrives — the moment a cancellation can be made to land in.
	inFlight chan []byte
	// interrupt is closed when a deadline is set into the past.
	interrupt chan struct{}
	// clearing is signalled when a clear arrives, before it is let through.
	clearing chan struct{}
	// released lets that clear through.
	released chan struct{}
	// frames carries the frames that left, for the reader to answer.
	frames chan []byte
	closed chan struct{}

	// arming and closing are once each, and they are TWO values: the arm closes
	// the interrupt channel while the write is in flight, and a Close that
	// could not close the connection afterwards would leave the reader parked
	// in Read for the rest of the run.
	armOnce   sync.Once
	closeOnce sync.Once
}

func newInterruptConn() *interruptConn {
	return &interruptConn{
		inFlight:  make(chan []byte, 1),
		interrupt: make(chan struct{}),
		clearing:  make(chan struct{}, 1),
		released:  make(chan struct{}),
		frames:    make(chan []byte, 8),
		closed:    make(chan struct{}),
	}
}

func (c *interruptConn) Write(p []byte) (int, error) {
	frame := append([]byte(nil), p...)

	c.mu.Lock()
	c.writes++
	first := c.writes == 1
	c.mu.Unlock()

	if first {
		// HELD UNTIL INTERRUPTED, so the caller's cancellation lands in the
		// write rather than after it.
		c.inFlight <- frame
		<-c.interrupt
		return 0, os.ErrDeadlineExceeded
	}

	c.mu.Lock()
	armed := c.armed
	if armed {
		c.armedWrites++
	}
	c.mu.Unlock()
	if armed {
		return 0, os.ErrDeadlineExceeded
	}
	c.frames <- frame
	return len(frame), nil
}

// Read answers the frame that left, the way the endpoint answers a request: on
// the connection, with that request's own id.
func (c *interruptConn) Read(p []byte) (int, error) {
	select {
	case frame := <-c.frames:
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(frame, &request); err != nil {
			return 0, err
		}
		answer, err := json.Marshal(rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"answered":true}`)})
		if err != nil {
			return 0, err
		}
		return copy(p, append(answer, '\n')), nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *interruptConn) SetWriteDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		// THE CLEAR IS HELD. This is the window: the cancelled caller has
		// armed the connection's deadline and has not withdrawn it.
		select {
		case c.clearing <- struct{}{}:
		default:
		}
		<-c.released
		c.mu.Lock()
		c.armed = false
		c.mu.Unlock()
		return nil
	}
	c.mu.Lock()
	c.armed = true
	c.mu.Unlock()
	c.armOnce.Do(func() { close(c.interrupt) })
	return nil
}

func (c *interruptConn) attemptsWhileArmed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.armedWrites
}

// writeCalls is how many frames the connection was ASKED to take, refused or
// not: a call whose caller cancelled it must not reach the write at all.
func (c *interruptConn) writeCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes
}

func (c *interruptConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *interruptConn) LocalAddr() net.Addr             { return stuckAddr{} }
func (c *interruptConn) RemoteAddr() net.Addr            { return stuckAddr{} }
func (c *interruptConn) SetDeadline(time.Time) error     { return nil }
func (c *interruptConn) SetReadDeadline(time.Time) error { return nil }

// A CALLER'S INTERRUPT IS ITS OWN WRITE'S AND NOBODY ELSE'S (nocx-xn63t.6.14).
// The deadline an interrupt sets belongs to the CONNECTION while the intent
// behind it belongs to one write, and one connection carries every call of the
// session: so a cancellation that lands after its own frame is out must not
// cost a sibling anything. Under the defect the sibling's frame was refused by
// a deadline it had never set, never reached the endpoint, and left the caller
// waiting for a connection that had never errored — the CI report, where a
// tools/call after a cancellation was never answered.
//
// Every order here is a handoff, not a duration: the cancelled call is caught
// inside its write, the deadline is caught before it is withdrawn, and the
// sibling is given its chance by a scheduling point rather than by a wait.
func TestACancelledCallsInterruptCannotFailASiblingsWrite(t *testing.T) {
	conn := newInterruptConn()
	link := newEndpointLink("shared.sock", fixedDialer{conn: conn}, "", discardLogger)
	t.Cleanup(link.close)

	// A SCHEDULING POINT, NOT A DURATION, and the same one this file already
	// uses: a goroutine started just before a yield runs next, as far as it can
	// go — to its blocking point — which is what puts the sibling's attempt
	// inside the window rather than after it.
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	cancelled, cancel := context.WithCancel(context.Background())
	go func() { _, _, _ = link.call(cancelled, "alpha.first", json.RawMessage(`{}`)) }()
	<-conn.inFlight // the cancelled call is inside its write
	cancel()        // so its interrupt is armed while that write is still in flight
	<-conn.interrupt
	<-conn.clearing // the cancelled caller has reached its clear, and is held there

	// THE SIBLING, with nothing to do with the cancellation, wants to write on
	// the same connection while that deadline is in force.
	type outcome struct {
		result json.RawMessage
		err    error
	}
	sibling := make(chan outcome, 1)
	go func() {
		result, _, err := link.call(context.Background(), "alpha.second", json.RawMessage(`{}`))
		sibling <- outcome{result: result, err: err}
	}()
	runtime.Gosched()

	// NOBODY ELSE'S WRITE MAY MEET A DEADLINE IT DID NOT ARM. Read as state:
	// the sibling has run as far as it can — to the connection under the
	// defect, to the write turn under the fix.
	if got := conn.attemptsWhileArmed(); got != 0 {
		t.Fatalf("a sibling's write was refused by the deadline a cancelled call armed on the shared connection: %d frame(s)", got)
	}

	close(conn.released)
	select {
	case got := <-sibling:
		if got.err != nil {
			t.Fatalf("the call beside a cancellation failed with %v: a cancellation is not its sibling's failure (nocx-tlaft)", got.err)
		}
		if string(got.result) != `{"answered":true}` {
			t.Fatalf("the call beside a cancellation answered %s", got.result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call beside a cancellation never answered")
	}
}

// A CALLER CANCELLED WHILE IT WAITS FOR THE TURN SENDS NOTHING. The turn is
// what a write waits on, so it is a place a caller can be called off — and the
// request it was waiting to write is one its caller no longer wants. The
// connection would carry it into a mutation (the endpoint runs what it is
// asked), and MCP carries no response for a cancelled request, so the write is
// refused for the same reason the link refuses one that arrives cancelled
// (nocx-xn63t.6.14).
//
// The turn is held BY THE TEST, so the call is queued on it by construction and
// nothing here rests on which goroutine the scheduler runs: whenever the
// cancellation lands, the send either sees it before it writes or it does not,
// and the two are told apart by whether the connection was asked for the frame
// at all.
func TestACallCancelledWhileItWaitsForTheTurnSendsNothing(t *testing.T) {
	raw := newInterruptConn()
	conn := newEndpointConn(raw)
	t.Cleanup(func() { conn.close() })

	conn.writeTurn.Lock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := conn.send(ctx, 1, "alpha.second", json.RawMessage(`{}`))
		done <- err
	}()
	cancel() // called off while it waits for the turn

	// The turn comes free, and the connection's deadline is let go with it (no
	// other caller is holding it in this test).
	close(raw.released)
	conn.writeTurn.Unlock()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a call cancelled while it waited for the connection's turn returned %v, want the cancellation: the turn was taken and a write begun", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a call cancelled while it waited for the connection's turn never returned")
	}
	if got := raw.writeCalls(); got != 0 {
		t.Fatalf("the connection was asked to take %d frame(s) from a call its caller had cancelled", got)
	}
}

// THE ENDPOINT CLOSES AN IDLE CONNECTION, and that is ordinary: its read
// deadline is eleven minutes wide while nothing is in flight, so a coordinator
// that has been quiet finds its connection gone the next time it calls. That
// call reconnects; an endpoint that went idle is not an endpoint that went
// away, and "endpoint unavailable" would put the failure in front of the one
// caller who could not have caused it.
//
// The close is ORDERED against what the client does, and the order is taken
// from the ADAPTER's side: the next request is written only after the
// adapter's own read of the connection has ended. Without the ordering the
// test would accept a run in which the client's request went out before the
// close landed — it would then be proving the retry, or nothing at all, rather
// than that a closed connection is re-dialled.
//
// THE ENDPOINT'S OWN "I CLOSED IT" IS NOT THAT ORDER. Measured: the fake's
// Close had returned and its read loop had ended, and the adapter's next frame
// was still accepted whole — then its read failed with "connection reset by
// peer". So a Go Close can return before the kernel releases the socket; the
// likely holder is the runtime's own epoll, which since Linux 6.10 keeps a
// reference to a file while it polls it. A request written in that window is
// accepted and then reset — the ambiguous case the adapter deliberately does not
// retry, since it cannot tell it from an endpoint that read the request and
// died. It failed CI that way, and 3 runs in 9,000 under load. After the
// adapter has read the end of the connection, the socket is gone on both
// sides and no write can land in it.
func TestTheNextCallRedialsWhenTheEndpointClosedTheIdleConnection(t *testing.T) {
	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		if request.Method == "tools.catalogue" {
			writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(twoTools)})
			_ = conn.Close()
			return
		}
		writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"after":true}`)})
	})
	dialer := &readEndObservingDialer{ended: make(chan struct{}, 1)}
	driver := startStdioDialing(t, endpoint.socket(), dialer)
	initialization(driver)

	select {
	case <-dialer.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the adapter never read the end of the connection the endpoint closed")
	}
	driver.send(2, "tools/call", `{"name":"alpha.second"}`)
	if text := toolText(t, driver.nextID(2)); text != `{"after":true}` {
		t.Fatalf("the call after an idle close answered %s", text)
	}
	if got := endpoint.dialCount(); got != 2 {
		t.Fatalf("endpoint connections = %d, want 2: the closed connection and the dial the next call made", got)
	}
}

// readEndObservingDialer dials the real socket and reports the first time the
// adapter's read of a connection it made comes back with an error — the
// moment the adapter itself has learned the connection is over.
type readEndObservingDialer struct {
	dialer net.Dialer
	ended  chan struct{}
}

func (d *readEndObservingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.dialer.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &readEndObservingConn{Conn: conn, ended: d.ended}, nil
}

type readEndObservingConn struct {
	net.Conn
	ended chan struct{}
}

func (c *readEndObservingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		select {
		case c.ended <- struct{}{}:
		default:
		}
	}
	return n, err
}

// A DIAL THAT GENUINELY FAILS IS NAMED, and it is the same answer the wire has
// always carried for it.
func TestADialThatFailsIsNamedEndpointUnavailable(t *testing.T) {
	link := newEndpointLink(filepath.Join(storagetest.SocketDir(t), "missing.sock"), &net.Dialer{}, "", discardLogger)
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
	link := newEndpointLink(endpoint.socket(), dialer, "", discardLogger)
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

// shortWriteConn is a connection that takes all but the LAST BYTE of a frame and
// reports no error at all, which a socket is allowed to do when its buffer
// fills. It is the one write failure that says nothing about the connection's
// life: nothing whole left this process, so the request cannot have run and a
// retry is safe — and the socket has not errored, so the reader has nothing to
// settle.
type shortWriteConn struct {
	closed chan struct{}
	once   sync.Once
}

func (c *shortWriteConn) Write(p []byte) (int, error) { return len(p) - 1, nil }

func (c *shortWriteConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *shortWriteConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *shortWriteConn) LocalAddr() net.Addr  { return stuckAddr{} }
func (c *shortWriteConn) RemoteAddr() net.Addr { return stuckAddr{} }

func (c *shortWriteConn) SetDeadline(time.Time) error     { return nil }
func (c *shortWriteConn) SetReadDeadline(time.Time) error { return nil }
func (c *shortWriteConn) SetWriteDeadline(time.Time) error {
	return nil
}

// shortWriteDialer hands out one connection that cuts the first frame short,
// then the real dial for every connection after it.
type shortWriteDialer struct {
	inner Dialer
	mu    sync.Mutex
	calls int
}

func (d *shortWriteDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.calls++
	first := d.calls == 1
	d.mu.Unlock()
	if !first {
		return d.inner.DialContext(ctx, network, address)
	}
	return &shortWriteConn{closed: make(chan struct{})}, nil
}

func (d *shortWriteDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// A FRAME THE KERNEL CUT SHORT IS RETRIED, NOT WAITED ON. A write that
// delivered nothing is evidence about the CONNECTION only when the socket is
// what failed, and this one reported no error at all: the reader has nothing to
// settle, so a caller waiting for the connection to settle waits until the
// endpoint closes it for its own reasons — the never-answering call of
// nocx-xn63t.6.14, one step away from where CI met it. Nothing whole left the
// process, so the request cannot have run: it is dialled again, by the rule
// that retries any frame that never arrived.
//
// The context is a bound on the failure, not on the work: with the retry the
// call answers at once, and without it the caller must not be left parked for
// the length of a test binary's alarm to say so.
func TestAFrameCutShortIsRetriedRatherThanWaitedOn(t *testing.T) {
	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"after":true}`)})
	})
	dialer := &shortWriteDialer{inner: &net.Dialer{}}
	link := newEndpointLink(endpoint.socket(), dialer, "", discardLogger)
	t.Cleanup(link.close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, upstream, err := link.call(ctx, "alpha.first", json.RawMessage(`{}`))
	if err != nil || upstream != nil {
		t.Fatalf("call after a frame the kernel cut short: result=%s upstream=%v err=%v dials=%d", result, upstream, err, dialer.count())
	}
	if string(result) != `{"after":true}` {
		t.Fatalf("call result = %s", result)
	}
	if got := dialer.count(); got != 2 {
		t.Fatalf("dials = %d, want 2: the connection that cut the frame short and the one the retry made", got)
	}
}

// ── the defects an adversarial read of the first round found (nocx-tlaft) ───

// A WRITER THAT NEVER RAN OPENED NOTHING. Hold a list, queue a second list,
// cancel the second before it runs, and the request that follows must still
// wait for the FIRST: the cancelled list wrote no catalogue, and a turn opened
// on its behalf lets a later request read the session as if it had finished.
//
// This is the one ordering rule a test can hold without depending on timing:
// it drives the gate the read loop drives, and the assertions are the state of
// two channels at a moment the test chooses.
func TestACancelledWriterDoesNotOpenItsTurn(t *testing.T) {
	gate := newSessionGate()
	_, leaveA := gate.enter(true)
	_, leaveB := gate.enter(true)
	behindB, _ := gate.enter(false)

	// A SCHEDULING POINT, NOT A DURATION. One processor and one yield let the
	// cancelled writer run as far as it can before this test looks: to its
	// blocking wait under the fix, to the end under the defect. What is read
	// off the gate afterwards is state, so the two are told apart without the
	// test waiting on a clock.
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	bLeft := make(chan struct{})
	go func() {
		leaveB(false)
		close(bLeft)
	}()
	runtime.Gosched()

	// A's turn is still open, so nothing queued behind the cancelled B may run,
	// and B's own turn may not have opened.
	select {
	case <-behindB:
		t.Fatal("a request behind a cancelled writer ran while the writer it was queued behind was still running")
	default:
	}
	select {
	case <-bLeft:
		t.Fatal("the cancelled writer opened its turn before the writer it was queued behind finished")
	default:
	}

	// The writer it was queued behind finishing is what opens both turns.
	leaveA(true)
	select {
	case <-behindB:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never opened after the writer A was queued behind finished")
	}
	select {
	case <-bLeft:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled writer never finished leaving")
	}
}

// A CALLER THAT ALREADY CANCELLED IS NOT SENT ANYTHING — and does not even
// connect. On the parent a fresh DialContext(ctx) refused this before a byte
// left the process; a shared connection has no dial to ask, so the refusal is
// its own step, and without it a cancelled mutation runs at the endpoint.
//
// The dialer ignores the context deliberately: a real net.Dialer would refuse a
// cancelled dial by itself and hide whether this adapter refuses on its own, so
// the dial count here is the only thing that can answer it. Nothing is written
// either way, because nothing is connected.
func TestACancelledCallIsNeverSentToTheEndpoint(t *testing.T) {
	dialer := &countingDialer{conn: newDeliveringFailureConn()}
	link := newEndpointLink("ignored.sock", dialer, "", discardLogger)
	t.Cleanup(link.close)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := link.call(ctx, "alpha.first", json.RawMessage(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call error = %v, want %v", err, context.Canceled)
	}
	if got := dialer.count(); got != 0 {
		t.Fatalf("dials = %d, want 0: a cancelled call must not even connect", got)
	}
}

// stuckConn is a connection whose writes never drain: the peer stopped reading,
// which parks that caller and every other writer behind the same descriptor.
// It fails a write only when a write deadline is set, which is the one thing a
// context cannot reach.
type stuckConn struct {
	writing  chan struct{}
	deadline chan struct{}
	closed   chan struct{}
	once     sync.Once
	fired    sync.Once
}

func newStuckConn() *stuckConn {
	return &stuckConn{
		writing:  make(chan struct{}, 1),
		deadline: make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

func (c *stuckConn) Write([]byte) (int, error) {
	select {
	case c.writing <- struct{}{}:
	default:
	}
	select {
	case <-c.deadline:
		return 0, os.ErrDeadlineExceeded
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *stuckConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *stuckConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *stuckConn) LocalAddr() net.Addr  { return stuckAddr{} }
func (c *stuckConn) RemoteAddr() net.Addr { return stuckAddr{} }

func (c *stuckConn) SetDeadline(time.Time) error     { return nil }
func (c *stuckConn) SetReadDeadline(time.Time) error { return nil }

func (c *stuckConn) SetWriteDeadline(deadline time.Time) error {
	if deadline.IsZero() {
		return nil
	}
	c.fired.Do(func() { close(c.deadline) })
	return nil
}

type stuckAddr struct{}

func (stuckAddr) Network() string { return "stuck" }
func (stuckAddr) String() string  { return "stuck" }

// fixedDialer hands out one connection and never dials again.
type fixedDialer struct{ conn net.Conn }

func (d fixedDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return d.conn, nil
}

// A WRITE TO A PEER THAT STOPPED READING MUST NOT PARK ITS CALLER. The select
// that watches this caller's context is reached only AFTER the write, so a
// context cannot answer this; a write deadline can, and on a shared connection
// the difference is every other writer behind the same descriptor.
func TestAnInterruptedWriteDoesNotParkTheCaller(t *testing.T) {
	conn := newStuckConn()
	link := newEndpointLink("stuck.sock", fixedDialer{conn: conn}, "", discardLogger)
	t.Cleanup(link.close)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := link.call(ctx, "alpha.first", json.RawMessage(`{}`))
		done <- err
	}()
	select {
	case <-conn.writing:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never reached the write")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupted call error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a write to a peer that stopped reading parked its caller")
	}
}

// A READER THAT STOPS MUST TAKE THE SOCKET WITH IT. The connection is the
// endpoint's admission interval (ADR-0058), so leaving the descriptor open on a
// response the adapter cannot frame keeps the interval — and the session's
// caller slot — alive for a session that will never use it, and leaves the
// endpoint writing into a connection nobody is reading.
func TestAFailedReadClosesTheSocket(t *testing.T) {
	closed := make(chan struct{}, 1)
	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		if request.Method == "tools.catalogue" {
			// A line this adapter cannot frame is what its reader dies on.
			_, _ = io.WriteString(conn, "this is not a response\n")
			return
		}
	}, withEndObserver(func(net.Conn) {
		select {
		case closed <- struct{}{}:
		default:
		}
	}))
	driver := startStdio(t, endpoint.socket())
	driver.send(0, "initialize", `{"protocolVersion":"2025-11-25"}`)
	driver.nextID(0)
	driver.send(1, "tools/list", `{}`)
	envelope := driver.nextID(1)
	if envelope["error"] == nil {
		t.Fatalf("a response the adapter could not frame was answered as a result: %v", envelope)
	}

	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the endpoint never saw the connection close: the socket outlived the reader that gave up on it")
	}
}

// TWO LOADS IN FLIGHT ARE TWO ANSWERS, and the one the endpoint gave LAST is
// the one the session keeps. A call's own refresh can be held at the endpoint
// while a tools/list redials, loads and installs; letting the older answer land
// afterwards leaves every later membership check reading a catalogue the
// endpoint has already replaced.
func TestAStaleCatalogueLoadDoesNotUnseatANewerOne(t *testing.T) {
	stale := make(chan struct{})
	release := make(chan struct{})
	var catalogues atomic.Int64
	catalogue := func(conn net.Conn, request rpcEnvelope, tools string) {
		writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"tools":[` + tools + `]}`)})
	}
	tool := func(name string) string {
		return `{"name":"` + name + `","summary":"` + name + `","params":{"type":"object"},"result":{"type":"object"}}`
	}
	first := tool("alpha.one")
	current := tool("alpha.one") + "," + tool("alpha.two")

	endpoint := startScriptedEndpoint(t, func(conn net.Conn, request rpcEnvelope) {
		if request.Method == "tools.catalogue" {
			if catalogues.Add(1) == 1 {
				// The call's own refresh, held until the list has installed.
				close(stale)
				<-release
				catalogue(conn, request, first)
				return
			}
			catalogue(conn, request, current)
			return
		}
		writeJSONLine(t, conn, rpcEnvelope{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(fmt.Sprintf(`{"ran":%q}`, request.Method))})
	})
	driver := startStdio(t, endpoint.socket())
	driver.send(0, "initialize", `{"protocolVersion":"2025-11-25"}`)
	driver.nextID(0)

	// A call with no catalogue yet loads one of its own, and it is held.
	driver.send(2, "tools/call", `{"name":"alpha.one"}`)
	<-stale
	// While it is held, a list loads and installs the CURRENT catalogue.
	driver.send(1, "tools/list", `{}`)
	driver.nextID(1)
	// Only now does the older load answer, and it must not be installed.
	close(release)
	if text := toolText(t, driver.nextID(2)); text != `{"ran":"alpha.one"}` {
		t.Fatalf("the held call answered %s", text)
	}

	// The catalogue the endpoint answered LAST is the one in force: asking for
	// a tool only the current one offers costs no refresh, because membership
	// was already answered.
	driver.send(3, "tools/call", `{"name":"alpha.two"}`)
	if text := toolText(t, driver.nextID(3)); text != `{"ran":"alpha.two"}` {
		t.Fatalf("the call after the stale install answered %s", text)
	}
	if got := catalogues.Load(); got != 2 {
		t.Fatalf("endpoint catalogue requests = %d, want 2: a stale load was installed over a newer one, so membership had to be re-asked", got)
	}
}

// deliveringFailureConn delivers every byte it is given and still reports a
// failure. Write's second result is what says whether a retry is safe, and a
// net.Conn is free to answer this way — so the retry contract cannot rest on
// "an error means nothing arrived".
type deliveringFailureConn struct {
	once   sync.Once
	closed chan struct{}
}

func newDeliveringFailureConn() *deliveringFailureConn {
	return &deliveringFailureConn{closed: make(chan struct{})}
}

func (c *deliveringFailureConn) Write(p []byte) (int, error) {
	return len(p), errors.New("the whole frame left and the answer is unknown")
}

func (c *deliveringFailureConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *deliveringFailureConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *deliveringFailureConn) LocalAddr() net.Addr  { return stuckAddr{} }
func (c *deliveringFailureConn) RemoteAddr() net.Addr { return stuckAddr{} }

func (c *deliveringFailureConn) SetDeadline(time.Time) error     { return nil }
func (c *deliveringFailureConn) SetReadDeadline(time.Time) error { return nil }
func (c *deliveringFailureConn) SetWriteDeadline(time.Time) error {
	return nil
}

// countingDialer counts dials and always hands back the same connection.
type countingDialer struct {
	conn  net.Conn
	mu    sync.Mutex
	calls int
}

func (d *countingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return d.conn, nil
}

func (d *countingDialer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// A WRITE THAT DELIVERED THE WHOLE FRAME IS NOT RETRIED. The endpoint may hold
// that request, and the tools on the far side are not all idempotent: running a
// mutation twice is how one call becomes two. The retry is for the frame that
// never arrived, and the only thing that can tell the two apart is what the
// write itself reported.
func TestAWriteThatDeliveredTheWholeFrameIsNotRetried(t *testing.T) {
	dialer := &countingDialer{conn: newDeliveringFailureConn()}
	link := newEndpointLink("ambiguous.sock", dialer, "", discardLogger)
	t.Cleanup(link.close)

	_, _, err := link.call(context.Background(), "alpha.first", json.RawMessage(`{}`))
	if !errors.Is(err, ErrEndpointUnavailable) {
		t.Fatalf("call error = %v, want %v", err, ErrEndpointUnavailable)
	}
	if got := dialer.count(); got != 1 {
		t.Fatalf("dials = %d, want 1: a frame that left whole may have run, so it must not be sent again", got)
	}
}

// releasedConn is a connection whose write waits for the test, so the moment
// between "the request is out" and "the caller waits for its answer" can be
// built rather than raced for.
type releasedConn struct {
	writing chan struct{}
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
	fired   sync.Once
}

func newReleasedConn() *releasedConn {
	return &releasedConn{
		writing: make(chan struct{}, 1),
		release: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (c *releasedConn) Write([]byte) (int, error) {
	c.fired.Do(func() { close(c.writing) })
	<-c.release
	return 0, nil
}

func (c *releasedConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *releasedConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *releasedConn) LocalAddr() net.Addr  { return stuckAddr{} }
func (c *releasedConn) RemoteAddr() net.Addr { return stuckAddr{} }

func (c *releasedConn) SetDeadline(time.Time) error     { return nil }
func (c *releasedConn) SetReadDeadline(time.Time) error { return nil }
func (c *releasedConn) SetWriteDeadline(time.Time) error {
	return nil
}

// A CANCELLED CALL NEVER RETURNS AN ANSWER, whichever way the select falls.
//
// The window is small and real: the caller is parked on a select over its own
// answer and its own cancellation, and both ready at once is a coin flip. The
// state is built here rather than raced for — the write is held open, the answer
// is put in the reply channel, and the cancellation lands first — so every
// iteration reaches the select with both cases ready. Fifty iterations are how a
// coin flip becomes evidence.
func TestACancelledCallNeverReturnsAnAnswer(t *testing.T) {
	for iteration := range 50 {
		raw := newReleasedConn()
		conn := newEndpointConn(raw)
		link := &endpointLink{socket: "held.sock", dialer: fixedDialer{conn: raw}, conn: conn}
		ctx, cancel := context.WithCancel(context.Background())
		type outcome struct {
			result json.RawMessage
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, _, err := link.call(ctx, "alpha.first", json.RawMessage(`{}`))
			done <- outcome{result: result, err: err}
		}()
		<-raw.writing
		cancel()
		// The answer is waiting before the caller can look at it.
		conn.mu.Lock()
		for _, reply := range conn.pending {
			reply <- endpointAnswer{result: json.RawMessage(`{"late":true}`)}
		}
		conn.mu.Unlock()
		close(raw.release)

		got := <-done
		link.close()
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("iteration %d: a cancelled call returned %s (err %v)", iteration, got.result, got.err)
		}
	}
}
