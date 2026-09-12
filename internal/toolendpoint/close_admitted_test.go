package toolendpoint

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/agenttools"
	"github.com/shady2k/nocx/internal/assistant"
)

// ONE SESSION'S INTERVAL IS NOT ANOTHER'S.
//
// Closing the connections admitted for a session is the closing event ADR-0058
// names: a shared connection is admitted once, so nothing else ends a grant that
// has been withdrawn. It has to close exactly that session's connections and
// leave every other one working — a revoke that dropped a colleague's work in
// flight would be a defect of its own, and one that closed nothing is the
// security hole this exists for.

// sessionAuthorizer admits every peer into the session this test hands it, in
// the order the endpoint admits them, so the endpoint's per-session bookkeeping
// has something to key on. Admit runs once per connection, which is what makes a
// connection's session stable for the connection's whole life.
type sessionAuthorizer struct {
	mu       sync.Mutex
	sessions []string
	admitted int
}

func (a *sessionAuthorizer) Admit(Peer) (assistant.ToolInvocation, func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	session := ""
	if a.admitted < len(a.sessions) {
		session = a.sessions[a.admitted]
	}
	a.admitted++
	return assistant.ToolInvocation{
		Context:    context.Background(),
		RunContext: agenttools.RunContext{Session: session},
	}, func() {}, nil
}

// callOver requires the call to have been admitted and answered. It is used on
// connections whose admission is already established, so anything else is a
// failure of the thing under test.
func callOver(t *testing.T, conn net.Conn, id string) {
	t.Helper()
	response, err := holdingsOver(t, conn, id)
	if err != nil {
		t.Fatalf("call %s: %v", id, err)
	}
	if response.Error != nil {
		t.Fatalf("call %s refused: %+v", id, response.Error)
	}
}

// holdingsOver writes one request and reads the answer. The write error is
// RETURNED rather than fatal: an endpoint that refuses an admission writes the
// refusal and closes, so the write can be the thing that finds the connection
// already gone — which is the same outcome as reading the refusal, and not a
// failure of the endpoint.
func holdingsOver(t *testing.T, conn net.Conn, id string) (rpcResponse, error) {
	t.Helper()
	request := `{"jsonrpc":"2.0","id":"` + id + `","method":"workers.holdings","params":{}}` + "\n"
	if _, err := io.WriteString(conn, request); err != nil {
		return rpcResponse{}, err
	}
	return readResponse(t, conn), nil
}

// expectClosed requires the socket to have ENDED. A read that times out is a
// connection still open, so accepting any error here would let a close that
// never happened pass as one that did — which is exactly the shape this whole
// mechanism protects against.
func expectClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err == nil {
		t.Fatalf("the connection that should have ended answered %q", line)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("the connection was left open: the read only expired")
	}
}

// awaitAdmitted waits for the endpoint's view of its admissions to settle. Each
// serve loop removes its own bookkeeping as it ends, so this waits on an
// observable state change rather than on a duration.
func awaitAdmitted(t *testing.T, endpoint *Endpoint, want ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := endpoint.AdmittedSessions()
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("admitted sessions = %v, want %v", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCloseAdmittedEndsOneSessionsIntervalAndNoOther(t *testing.T) {
	auth := &sessionAuthorizer{sessions: []string{"session-1", "session-2"}}
	cfg := endpointConfig(t, &testAuthorizer{}, &testDispatcher{out: `{"held":[]}`})
	cfg.Auth = auth
	endpoint := startEndpoint(t, cfg)

	first := dialEndpoint(t, endpoint)
	// A request is what makes the admission observable: the endpoint admits on
	// accept, and the answer proves the bookkeeping is in place before it is
	// asserted on.
	callOver(t, first, "first")
	awaitAdmitted(t, endpoint, "session-1")

	second := dialEndpoint(t, endpoint)
	callOver(t, second, "second")
	awaitAdmitted(t, endpoint, "session-1", "session-2")

	if closed := endpoint.CloseAdmitted("session-1"); closed != 1 {
		t.Fatalf("CloseAdmitted = %d, want 1", closed)
	}
	expectClosed(t, first)
	awaitAdmitted(t, endpoint, "session-2")

	// THE OTHER SESSION KEEPS WORKING. Its connection was not closed, and its
	// admission is still the one the endpoint holds.
	callOver(t, second, "still-mine")
	awaitAdmitted(t, endpoint, "session-2")

	// An unknown session closes nothing, which is the answer a withdraw that
	// lost its race with a session teardown needs.
	if closed := endpoint.CloseAdmitted("session-nobody"); closed != 0 {
		t.Fatalf("CloseAdmitted for an unknown session = %d, want 0", closed)
	}

	if closed := endpoint.CloseAdmitted("session-2"); closed != 1 {
		t.Fatalf("CloseAdmitted = %d, want 1", closed)
	}
	expectClosed(t, second)
	awaitAdmitted(t, endpoint)
}

// refusedAuthorizer is a session authorizer that refuses, so a test can watch
// what the endpoint records for a connection it did NOT admit.
type refusedAuthorizer struct{ err error }

func (a refusedAuthorizer) Admit(Peer) (assistant.ToolInvocation, func(), error) {
	return assistant.ToolInvocation{}, nil, a.err
}

// A REFUSED CONNECTION IS NOT AN ADMITTED ONE. Recording the session of a peer
// the authorizer turned away would make it closable by a session that never
// admitted it — and, worse, would report an interval as live when the answer
// behind it was refused.
func TestARefusedConnectionIsNeverRecordedAsAdmitted(t *testing.T) {
	cfg := endpointConfig(t, &testAuthorizer{}, &testDispatcher{})
	cfg.Auth = refusedAuthorizer{err: ErrNotEnrolled}
	endpoint := startEndpoint(t, cfg)
	conn := dialEndpoint(t, endpoint)

	// The refusal is what this test expects, so the answer is read without
	// judging it and the claim is about what the endpoint RECORDED. A write
	// that failed is the same outcome — the connection was already refused and
	// closed — but a read that merely expired is not: it would mean the peer
	// was neither admitted nor refused.
	response, err := holdingsOver(t, conn, "refused")
	if err == nil && response.Error == nil {
		t.Fatalf("a refused peer was answered with a result: %+v", response)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a refused peer was neither refused nor disconnected: %v", err)
	}

	if admitted := endpoint.AdmittedSessions(); len(admitted) != 0 {
		t.Fatalf("admitted sessions = %v, want none: the peer was refused", admitted)
	}
}
