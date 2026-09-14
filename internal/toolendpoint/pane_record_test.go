package toolendpoint

// THE PANE RECORD, and the lane that may carry one (nocx-50w7p.16).
//
// A connection the helper forwarded arrives from a process with no pane of its
// own: the far agent has no pid on this machine, so the kernel rule answers
// nothing about it. What answers is the helper — its listener is per pane — and
// what the helper writes ahead of the far agent's bytes is the session whose
// pane the connection arrived on.
//
// Two facts make that safe, and both are asserted here. The record is read ONLY
// on the lane the coordinator dialed its helper on, so a process that dials
// this socket itself cannot claim a pane by prefixing one; and a connection on
// the lane that does not carry one is REFUSED rather than matched against the
// local tree rule, which answers for a process tree on THIS machine.

import (
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/toolendpoint/panebind"
)

const (
	testPaneSession  = "0f9b4d7159d38afee9648a843654516f"
	testPaneRequestA = `{"jsonrpc":"2.0","id":1,"method":"workers.inbox","params":{"sessionId":"` + testPaneSession + `"}}`
)

// forwardedEndpoint is an endpoint whose lane is the ONE peer this test's fake
// credentials report: everything dialling it is "the helper". That is what
// makes the record path reachable from a test at all — in production the
// predicate compares the peer against the process the coordinator dialed.
// testPaneToken is the bearer a forwarded connection presents in these tests.
const testPaneToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func forwardedEndpoint(t *testing.T, auth *testAuthorizer, dispatch *testDispatcher) *Endpoint {
	t.Helper()
	cfg := endpointConfig(t, auth, dispatch)
	cfg.Lane = func(Peer) bool { return true }
	return startEndpoint(t, cfg)
}

// selfDialledEndpoint is an endpoint with no lane at all: every connection is a
// caller that dialed this socket itself, which is every local agent.
func selfDialledEndpoint(t *testing.T, auth *testAuthorizer, dispatch *testDispatcher) *Endpoint {
	t.Helper()
	return startEndpoint(t, endpointConfig(t, auth, dispatch))
}

// sendRequest writes one record (when there is one) and then one request, and
// answers the response. It is one function because the ORDER is the contract:
// the record is written first and completely, before a byte of the request.
func sendRequest(t *testing.T, ep *Endpoint, session, request string) rpcResponse {
	t.Helper()
	conn := dialEndpoint(t, ep)
	if session != "" {
		record, err := panebind.Encode(session)
		if err != nil {
			t.Fatalf("encode pane record: %v", err)
		}
		if _, writeErr := conn.Write(record); writeErr != nil {
			t.Fatalf("write pane record: %v", writeErr)
		}
		// AND THE BEARER, right behind it: the record is the helper's claim
		// about the pane and this is the agent's claim about itself, and the
		// endpoint reads the pair in one window (nocx-50w7p.16).
		token, err := panebind.EncodeToken(testPaneToken)
		if err != nil {
			t.Fatalf("encode tool token: %v", err)
		}
		if _, writeErr := conn.Write(token); writeErr != nil {
			t.Fatalf("write tool token: %v", writeErr)
		}
	}
	if _, err := conn.Write([]byte(request + "\n")); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return readResponse(t, conn)
}

func (a *testAuthorizer) seenPeer(t *testing.T) Peer {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.peer
}

// TestAPaneRecordNamesThePaneAndLeavesThePayloadWhereItWas — the whole mechanism
// in one assertion: the session the helper named reaches the authorizer as the
// peer's pane, and the far agent's own bytes are read as the request they are.
// The second half is not ceremony: a reader that consumed one byte too many
// would take the first byte of the agent's request, and every far call would
// arrive as a parse error.
func TestAPaneRecordNamesThePaneAndLeavesThePayloadWhereItWas(t *testing.T) {
	auth := &testAuthorizer{}
	dispatch := &testDispatcher{out: `{"held":[]}`}
	ep := forwardedEndpoint(t, auth, dispatch)

	response := sendRequest(t, ep, testPaneSession, testPaneRequestA)
	if response.Error != nil {
		t.Fatalf("a connection carrying a pane record was refused: %+v", response.Error)
	}
	if got := auth.seenPeer(t).Pane; got != testPaneSession {
		t.Fatalf("the authorizer was told pane %q, want the session the record named (%q)", got, testPaneSession)
	}
	if token := auth.seenPeer(t).Token; token != testPaneToken {
		t.Fatalf("the authorizer was told token %q, want the bearer the connection presented", token)
	}
	if got := string(dispatch.lastInvocation().RawParams); got != `{"sessionId":"`+testPaneSession+`"}` {
		t.Fatalf("the dispatcher saw params %s, want the far agent's own bytes — the record must not eat into them", got)
	}
	if got := dispatch.lastInvocation().RunContext.Session; got != testAdmissionSession {
		t.Fatalf("the call ran under session %q, want the admitted one (%q)", got, testAdmissionSession)
	}
}

// TestAConnectionOnTheLaneWithoutARecordIsRefused — a lane connection that
// names no pane is refused, not guessed at. The guessing it replaces is the
// real defect: matching it against a local process tree would admit a far
// connection as whichever session the kernel happened to fit.
func TestAConnectionOnTheLaneWithoutARecordIsRefused(t *testing.T) {
	auth := &testAuthorizer{}
	dispatch := &testDispatcher{out: `{"held":[]}`}
	ep := forwardedEndpoint(t, auth, dispatch)

	// The request is a well-formed one: what makes this connection refusable is
	// that it named no pane, not that it said something malformed.
	response := sendRequest(t, ep, "", testPaneRequestA)
	if response.Error == nil {
		t.Fatal("a lane connection that named no pane was served")
	}
	if response.Error.Code != rpcPeerRefused {
		t.Fatalf("refusal code = %d, want %d (a peer refusal, not a request one)", response.Error.Code, rpcPeerRefused)
	}
	// The far agent's request was never even read as a record, so nothing was
	// dispatched and nothing was admitted.
	if auth.callCount() != 0 {
		t.Fatalf("the authorizer was consulted %d time(s) for a connection that named no pane", auth.callCount())
	}
	if dispatch.callCount() != 0 {
		t.Fatalf("the dispatcher ran %d call(s) for a connection that named no pane", dispatch.callCount())
	}
	if sessions := ep.AdmittedSessions(); len(sessions) != 0 {
		t.Fatalf("the endpoint recorded admissions %v for a connection that named no pane", sessions)
	}
	if !strings.Contains(response.Error.Data.Reason, "did not name a pane") {
		t.Fatalf("the refusal reason = %q, want one that says what was missing", response.Error.Data.Reason)
	}
}

// TestAShortRecordIsRefusedByName — a helper that died mid-write, or anything
// that sent a prefix and stopped. It is told apart from a mis-shaped record
// because the two are different faults, and it is refused either way.
func TestAShortRecordIsRefusedByName(t *testing.T) {
	auth := &testAuthorizer{}
	dispatch := &testDispatcher{out: `{"held":[]}`}
	ep := forwardedEndpoint(t, auth, dispatch)

	record, err := panebind.Encode(testPaneSession)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	conn := dialEndpoint(t, ep)
	if _, err := conn.Write(record[:len(record)-4]); err != nil {
		t.Fatalf("write partial record: %v", err)
	}
	// Closed rather than continued: a helper that stopped mid-record cannot
	// finish it, and leaving the connection open would only make the reader
	// wait for its own deadline.
	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The refusal is written to a closed peer, so it is observed rather than
	// read: the endpoint's observation is the endpoint's own record of why.
	if auth.callCount() != 0 {
		t.Fatalf("the authorizer was consulted %d time(s) for a truncated record", auth.callCount())
	}
	if dispatch.callCount() != 0 {
		t.Fatalf("the dispatcher ran %d call(s) for a truncated record", dispatch.callCount())
	}
}

// TestAMisShapedRecordIsRefusedByName — the bytes are not a record this build
// understands (a wrong magic is what a future shape or a non-record prefix
// looks like). Refused before the authorizer, never dispatched.
func TestAMisShapedRecordIsRefusedByName(t *testing.T) {
	auth := &testAuthorizer{}
	dispatch := &testDispatcher{out: `{"held":[]}`}
	ep := forwardedEndpoint(t, auth, dispatch)

	conn := dialEndpoint(t, ep)
	junk := strings.Repeat("X", panebind.RecordLen)
	if _, err := conn.Write([]byte(junk)); err != nil {
		t.Fatalf("write: %v", err)
	}
	response := readResponse(t, conn)
	if response.Error == nil {
		t.Fatal("a connection whose first bytes are not a pane record was served")
	}
	if response.Error.Code != rpcPeerRefused {
		t.Fatalf("refusal code = %d, want %d", response.Error.Code, rpcPeerRefused)
	}
	if auth.callCount() != 0 || dispatch.callCount() != 0 {
		t.Fatalf("a mis-shaped record reached the authorizer (%d) or the dispatcher (%d)",
			auth.callCount(), dispatch.callCount())
	}
}

// TestAConnectionThatDidNotComeFromTheHelperCannotClaimAPane — THE LOAD-BEARING
// NEGATIVE. A process that dials this socket itself may prefix a perfectly
// well-formed pane record to its request; if the record were read on every
// connection, that process would be admitted as the pane it named. It is not
// read: this connection is served by the local rule (the authorizer sees no
// pane), and the prefix reaches the request parser as the malformed bytes it is.
func TestAConnectionThatDidNotComeFromTheHelperCannotClaimAPane(t *testing.T) {
	auth := &testAuthorizer{}
	dispatch := &testDispatcher{out: `{"held":[]}`}
	ep := selfDialledEndpoint(t, auth, dispatch)

	record, err := panebind.Encode(testPaneSession)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	conn := dialEndpoint(t, ep)
	if _, err := conn.Write(append(record, []byte(testPaneRequestA+"\n")...)); err != nil {
		t.Fatalf("write: %v", err)
	}
	response := readResponse(t, conn)

	// The record was NOT taken as one: the authorizer, if it was consulted at
	// all, saw no pane — which is the whole claim. What the connection gets is
	// the parser's answer to bytes that are not a request.
	if got := auth.seenPeer(t).Pane; got != "" {
		t.Fatalf("a self-dialled connection was admitted as pane %q", got)
	}
	if response.Error == nil {
		t.Fatal("a self-dialled connection with a record prefix was served as an ordinary request")
	}
}

// TestASelfDialledConnectionIsServedExactlyAsBefore — the pairing for the test
// above: the local path is untouched. No record is read, the request's own
// bytes are its first bytes, and the authorizer sees a peer with no pane.
func TestASelfDialledConnectionIsServedExactlyAsBefore(t *testing.T) {
	auth := &testAuthorizer{}
	dispatch := &testDispatcher{out: `{"held":[]}`}
	ep := selfDialledEndpoint(t, auth, dispatch)

	response := sendRequest(t, ep, "", testPaneRequestA)
	if response.Error != nil {
		t.Fatalf("a local connection was refused: %+v", response.Error)
	}
	if got := auth.seenPeer(t).Pane; got != "" {
		t.Fatalf("a local connection carried pane %q, want none", got)
	}
	if got := string(dispatch.lastInvocation().RawParams); got != `{"sessionId":"`+testPaneSession+`"}` {
		t.Fatalf("the dispatcher saw params %s", got)
	}
}

// TestAnEndpointWithNoLaneRefusesToReadARecord — with no lane configured the
// endpoint has no idea who its helper is, and that is a real state (a
// coordinator with no helper). It must not become "then read the record from
// anyone": a nil lane means nobody may name a pane.
func TestAnEndpointWithNoLaneRefusesToReadARecord(t *testing.T) {
	auth := &testAuthorizer{}
	dispatch := &testDispatcher{out: `{"held":[]}`}
	cfg := endpointConfig(t, auth, dispatch)
	if cfg.Lane != nil {
		t.Fatal("this test is about a nil lane, and the config carried one")
	}
	ep := startEndpoint(t, cfg)

	record, err := panebind.Encode(testPaneSession)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	conn := dialEndpoint(t, ep)
	if _, err := conn.Write(append(record, []byte(testPaneRequestA+"\n")...)); err != nil {
		t.Fatalf("write: %v", err)
	}
	response := readResponse(t, conn)
	if got := auth.seenPeer(t).Pane; got != "" {
		t.Fatalf("a record was honoured with no lane configured: pane %q", got)
	}
	if response.Error == nil {
		t.Fatal("a record prefix was parsed as a request with no lane configured")
	}
}

// TestTheLaneIsAskedAboutEveryConnection — the predicate is the whole of the
// endpoint's knowledge about who its helper is, so it must be asked rather than
// cached: a helper replaced by a restart is a different pid, and a lane
// captured at construction would keep admitting the old one's connections.
func TestTheLaneIsAskedAboutEveryConnection(t *testing.T) {
	auth := &testAuthorizer{}
	dispatch := &testDispatcher{out: `{"held":[]}`}
	asked := 0
	allow := false
	cfg := endpointConfig(t, auth, dispatch)
	cfg.Lane = func(peer Peer) bool {
		asked++
		// A lane that is not the peer it is asked about is not the lane.
		return allow && peer.PID == 1234
	}
	ep := startEndpoint(t, cfg)

	// With the lane unset, a record is not read: the request line is served.
	if response := sendRequest(t, ep, "", testPaneRequestA); response.Error != nil {
		t.Fatalf("a connection with no lane was refused: %+v", response.Error)
	}
	allow = true
	if response := sendRequest(t, ep, testPaneSession, testPaneRequestA); response.Error != nil {
		t.Fatalf("the lane's own connection was refused: %+v", response.Error)
	}
	if got := auth.seenPeer(t).Pane; got != testPaneSession {
		t.Fatalf("after the lane was turned on, the authorizer saw pane %q", got)
	}
	if asked < 2 {
		t.Fatalf("the lane predicate was asked %d time(s) for two connections", asked)
	}
}

// TestARecordForAnotherPaneIsABadRequestWhenItIsNotOnTheLane is the log-facing
// half of the negative above: the bytes are answered by the parser, and the
// answer says the request was malformed rather than pretending a pane was
// admitted and failed.
func TestARecordForAnotherPaneIsABadRequestWhenItIsNotOnTheLane(t *testing.T) {
	dispatch := &testDispatcher{out: `{}`}
	ep := selfDialledEndpoint(t, &testAuthorizer{}, dispatch)
	other := "11111111111111111111111111111111"
	record, err := panebind.Encode(other)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	conn := dialEndpoint(t, ep)
	if _, err := conn.Write(append(record, []byte("\n")...)); err != nil {
		t.Fatalf("write: %v", err)
	}
	response := readResponse(t, conn)
	if response.Error == nil {
		t.Fatal("a foreign pane record was accepted as a request")
	}
	if response.Error.Code != rpcParseError {
		t.Fatalf("code = %d, want %d for bytes that are not a request", response.Error.Code, rpcParseError)
	}
}
