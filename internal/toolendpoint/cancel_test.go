package toolendpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
	"github.com/shady2k/nocx/internal/content"
)

// THE DEFECT THIS FILE EXISTS FOR (nocx-98qho). A call its caller has stopped
// waiting for goes on executing in the backend. The endpoint derives a
// request's context from the CONNECTION, and one connection now carries every
// call of a stdio session (nocx-tlaft), so closing it to cancel one call would
// take its siblings with it. The missing piece is a message that names ONE
// request on the connection that carries it, and a per-request context that
// message can end.
//
// Every assertion here is an EVENT rather than a duration: the executor reports
// which way it ended, and "cancelled" can only come from the context.

// callEnd is how one executor ended, reported by the dispatcher below.
type callEnd struct {
	method string
	cause  string
	err    error
}

// cancellingDispatcher is the tool surface these tests need: two calls only
// their context or the test can end, one call with a bound of its own, and one
// that answers at once. It reports every end, because "the executor stopped"
// is the fact the defect is about and a returned answer alone cannot state it
// — a held call answers when the test releases it, cancelled or not.
type cancellingDispatcher struct {
	started chan string
	ended   chan callEnd
	release chan struct{}
}

func newCancellingDispatcher() *cancellingDispatcher {
	return &cancellingDispatcher{
		started: make(chan string, 8),
		ended:   make(chan callEnd, 8),
		release: make(chan struct{}),
	}
}

// pollBound is the long poll's own bound: what a cancelled call must not wait
// for. An hour is far past any test's failure bound, so an executor released
// here was released by its context and by nothing else.
const pollBound = time.Hour

func (d *cancellingDispatcher) Dispatch(inv assistant.ToolInvocation) (string, error) {
	switch inv.Method {
	case "alpha.first", "alpha.second":
		d.started <- inv.Method
		select {
		case <-inv.Context.Done():
			d.ended <- callEnd{method: inv.Method, cause: "cancelled", err: inv.Context.Err()}
			return "", inv.Context.Err()
		case <-d.release:
			d.ended <- callEnd{method: inv.Method, cause: "released"}
			return `{"held":true}`, nil
		}
	case "alpha.poll":
		d.started <- inv.Method
		select {
		case <-inv.Context.Done():
			d.ended <- callEnd{method: inv.Method, cause: "cancelled", err: inv.Context.Err()}
			return "", inv.Context.Err()
		case <-time.After(pollBound):
			d.ended <- callEnd{method: inv.Method, cause: "deadline"}
			return `{"polled":true}`, nil
		}
	default:
		return `{"screen":true}`, nil
	}
}

func (d *cancellingDispatcher) Catalogue(content.Grant) []agenttools.Tool { return nil }

// startedCall and endedCall wait for the dispatcher's own report. The bound is
// a FAILURE bound and not a deadline anything waits for: both are events, and a
// test that never sees one says which event is missing instead of parking until
// go test's own timeout prints a goroutine dump.
func startedCall(t *testing.T, dispatch *cancellingDispatcher) string {
	t.Helper()
	select {
	case method := <-dispatch.started:
		return method
	case <-time.After(5 * time.Second):
		t.Fatal("no call started")
		return ""
	}
}

func endedCall(t *testing.T, dispatch *cancellingDispatcher) callEnd {
	t.Helper()
	select {
	case ended := <-dispatch.ended:
		return ended
	case <-time.After(5 * time.Second):
		t.Fatal("no call ended: the cancel did not reach its request context")
		return callEnd{}
	}
}

func startCancellingEndpoint(t *testing.T, dispatch *cancellingDispatcher) *Endpoint {
	t.Helper()
	return startEndpoint(t, Config{
		Dir:      t.TempDir(),
		Peers:    testPeers{uid: 1000, pid: 1234},
		Owner:    testOwner{uid: 1000},
		SelfUID:  1000,
		Auth:     &testAuthorizer{},
		Dispatch: dispatch,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

// endpointClient keeps ONE reader for the connection. The helpers in
// endpoint_test.go build a reader per response, which is fine for responses
// that arrive one at a time and would swallow a second answer that arrived
// with the first.
type endpointClient struct {
	t      *testing.T
	conn   net.Conn
	reader *bufio.Reader
}

func dialClient(t *testing.T, endpoint *Endpoint) *endpointClient {
	t.Helper()
	conn, err := net.Dial("unix", endpoint.SocketPath())
	if err != nil {
		t.Fatalf("dial endpoint: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &endpointClient{t: t, conn: conn, reader: bufio.NewReader(conn)}
}

func (c *endpointClient) request(id int, method, params string) {
	c.t.Helper()
	c.write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, id, method, params))
}

func (c *endpointClient) notify(method, params string) {
	c.t.Helper()
	c.write(fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"params":%s}`, method, params))
}

func (c *endpointClient) write(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.conn, line+"\n"); err != nil {
		c.t.Fatalf("write %s: %v", line, err)
	}
}

func (c *endpointClient) response() rpcResponse {
	c.t.Helper()
	if err := c.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		c.t.Fatalf("set read deadline: %v", err)
	}
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	var response rpcResponse
	if err := json.Unmarshal(line, &response); err != nil {
		c.t.Fatalf("decode response %q: %v", line, err)
	}
	return response
}

// A CALL CANCELLED ON THE WIRE STOPS, AND ITS SIBLING DOES NOT.
//
// Two calls are held on one connection. The cancel names the first, so the
// first must end as cancelled — and the second must still be held afterwards,
// which is why it is released only once the first has been observed stopping.
// A connection closed to cancel one call would take both, and that is the
// defect this message exists to avoid (nocx-tlaft).
func TestACancelledCallStopsAtTheEndpointAndLeavesItsSibling(t *testing.T) {
	dispatch := newCancellingDispatcher()
	client := dialClient(t, startCancellingEndpoint(t, dispatch))

	client.request(1, "alpha.first", `{}`)
	if method := startedCall(t, dispatch); method != "alpha.first" {
		t.Fatalf("the first call started as %q", method)
	}
	client.request(2, "alpha.second", `{}`)
	if method := startedCall(t, dispatch); method != "alpha.second" {
		t.Fatalf("the second call started as %q", method)
	}

	client.notify("calls.cancel", `{"requestId":1}`)

	ended := endedCall(t, dispatch)
	if ended.method != "alpha.first" || ended.cause != "cancelled" {
		t.Fatalf("the cancelled call ended as %+v, want alpha.first cancelled", ended)
	}
	if !errors.Is(ended.err, context.Canceled) {
		t.Fatalf("the cancelled call's context ended with %v, want context.Canceled", ended.err)
	}

	// THE SIBLING WAS NOT DISTURBED, read as state: releasing it now makes it
	// answer, and an answer is only possible if its context was still live.
	close(dispatch.release)
	if ended := endedCall(t, dispatch); ended.method != "alpha.second" || ended.cause != "released" {
		t.Fatalf("the sibling ended as %+v, want it to run to its own answer", ended)
	}

	answers := make(map[string]rpcResponse, 2)
	for range 2 {
		answer := client.response()
		answers[string(answer.ID)] = answer
	}
	if answer := answers["2"]; answer.Error != nil || string(answer.Result) != `{"held":true}` {
		t.Fatalf("the sibling answered %+v, want its own result", answer)
	}
	// THE CALLER STILL LEARNS WHAT HAPPENED TO ITS OWN CALL. The adapter drops
	// this answer — MCP carries none for a cancelled request — but the
	// endpoint answers every request it read, and "call abandoned" is the
	// sentence that says the context ended rather than the tool failing.
	if answer := answers["1"]; answer.Error == nil || answer.Error.Message != "call abandoned" {
		t.Fatalf("the cancelled call answered %+v, want the abandoned-call refusal", answer)
	}
}

// A LONG POLL RELEASES ON THE CANCEL, NOT ON ITS OWN BOUND. The call here holds
// a bound of an hour, which is what a poll waits for when nobody cancels it;
// the only other way out is its context. Before the wire carried a cancel, this
// call ran to its deadline with the caller long gone.
func TestACancelledLongPollReleasesBeforeItsOwnBound(t *testing.T) {
	dispatch := newCancellingDispatcher()
	client := dialClient(t, startCancellingEndpoint(t, dispatch))

	client.request(1, "alpha.poll", `{}`)
	if method := startedCall(t, dispatch); method != "alpha.poll" {
		t.Fatalf("the poll started as %q", method)
	}

	client.notify("calls.cancel", `{"requestId":1}`)

	if ended := endedCall(t, dispatch); ended.cause != "cancelled" {
		t.Fatalf("the long poll ended as %+v, want cancellation rather than its own bound", ended)
	}
}

// A CANCEL THAT NAMES NOTHING IS SWALLOWED, AND THE CONNECTION GOES ON. A
// notification carries no id, and an id-less error on this socket means the
// CONNECTION was refused to the adapter — so a malformed or late cancel must
// be read and dropped, never answered. The three shapes below are the ones
// that occur: a request that already answered, params that are not an object,
// and no request id at all.
func TestACancelThatNamesNothingIsSwallowed(t *testing.T) {
	dispatch := newCancellingDispatcher()
	client := dialClient(t, startCancellingEndpoint(t, dispatch))

	client.request(1, "alpha.screen", `{}`)
	if answer := client.response(); answer.Error != nil || string(answer.Result) != `{"screen":true}` {
		t.Fatalf("the first call answered %+v", answer)
	}
	client.notify("calls.cancel", `{"requestId":1}`)
	client.notify("calls.cancel", `"not an object"`)
	client.notify("calls.cancel", `{}`)
	client.notify("calls.cancel", `{"requestId":null}`)

	client.request(2, "alpha.screen", `{}`)
	answer := client.response()
	if string(answer.ID) != "2" {
		t.Fatalf("the connection answered %+v before the next call, want only its own answer", answer)
	}
	if answer.Error != nil || string(answer.Result) != `{"screen":true}` {
		t.Fatalf("the call after the cancels answered %+v, want the connection intact", answer)
	}
}
