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

// ENDING AN INTERVAL IS EXACT, AND ONE SESSION'S IS NOT ANOTHER'S.
//
// Retiring an interval is the closing event ADR-0058 names: a shared
// connection is admitted once, so nothing else ends a grant that has been
// withdrawn. It has to close exactly the connections admitted under THAT
// interval and leave every other one working — a revoke that dropped a
// colleague's work in flight would be a defect of its own, and one that closed
// nothing is the security hole this exists for.
//
// The epoch is what makes "that interval" sayable. A session withdrawn and
// enrolled again holds two intervals with identical approval, and a retirement
// that closed "whatever is live for this session" would take the new
// connection with the old one's (nocx-9mn6z).

// sessionAuthorizer admits each connection into the session this test hands it,
// in the order the endpoint admits them, publishing under the epoch the test
// set. Admit runs once per connection, which is what makes a connection's
// session and epoch stable for the connection's whole life.
type sessionAuthorizer struct {
	mu       sync.Mutex
	sessions []string
	admitted int
	// epoch is the interval every admission is published under. Zero means the
	// first, which is what most tests want; a test about a RETIRED interval
	// sets it to one that has already ended.
	epoch AdmissionEpoch
	// refusals counts admissions the endpoint refused through publish, so a
	// test can tell "the authorizer decided to admit" from "the connection was
	// admitted". Read it through refusalCount: the increment happens on the
	// serve goroutine.
	refusals int
}

// refusalCount is the locked read of refusals. The write happens on the
// endpoint's serve goroutine, so an unlocked read here would be a race the
// detector is entitled to report — and one it would only report sometimes.
func (a *sessionAuthorizer) refusalCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refusals
}

func (a *sessionAuthorizer) Admit(_ Peer, publish func(session string, epoch AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error) {
	a.mu.Lock()
	session := ""
	if a.admitted < len(a.sessions) {
		session = a.sessions[a.admitted]
	}
	a.admitted++
	epoch := a.epoch
	if epoch == 0 {
		epoch = 1
	}
	a.mu.Unlock()

	if !publish(session, epoch) {
		a.mu.Lock()
		a.refusals++
		a.mu.Unlock()
		return assistant.ToolInvocation{}, nil, ErrNotEnrolled
	}
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

func TestRetiringAnIntervalEndsOneSessionsConnectionAndNoOther(t *testing.T) {
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

	if closed := endpoint.RetireAdmissions("session-1", 1); closed != 1 {
		t.Fatalf("RetireAdmissions = %d, want 1", closed)
	}
	expectClosed(t, first)
	awaitAdmitted(t, endpoint, "session-2")

	// THE OTHER SESSION KEEPS WORKING. Its connection was not closed, and its
	// admission is still the one the endpoint holds.
	callOver(t, second, "still-mine")
	awaitAdmitted(t, endpoint, "session-2")

	// An unknown session closes nothing, which is the answer a withdraw that
	// lost its race with a session teardown needs.
	if closed := endpoint.RetireAdmissions("session-nobody", 1); closed != 0 {
		t.Fatalf("RetireAdmissions for an unknown session = %d, want 0", closed)
	}

	if closed := endpoint.RetireAdmissions("session-2", 1); closed != 1 {
		t.Fatalf("RetireAdmissions = %d, want 1", closed)
	}
	expectClosed(t, second)
	awaitAdmitted(t, endpoint)
}

// A PUBLICATION NAMING A RETIRED EPOCH IS REFUSED, even though the authorizer
// decided to admit and the session may hold a live interval again by then.
//
// This is the gap the review found (nocx-9mn6z, HIGH 2): the decision to admit
// and the recording of that admission were two steps, and a withdrawal landing
// between them found nothing registered to close — so the connection went on
// serving a grant nobody held. Decided under an epoch and published against its
// floor, the late publication is refused instead.
func TestAPublicationNamingARetiredEpochIsRefused(t *testing.T) {
	auth := &sessionAuthorizer{sessions: []string{"session-1"}, epoch: 1}
	cfg := endpointConfig(t, &testAuthorizer{}, &testDispatcher{out: `{"held":[]}`})
	cfg.Auth = auth
	endpoint := startEndpoint(t, cfg)

	// The interval ended before the connection was decided. Nothing was ever
	// registered under it, which is exactly the state the race produces.
	if closed := endpoint.RetireAdmissions("session-1", 1); closed != 0 {
		t.Fatalf("retiring an interval with no connection closed %d, want 0", closed)
	}

	conn := dialEndpoint(t, endpoint)
	response, err := holdingsOver(t, conn, "late")
	if err == nil && response.Error == nil {
		t.Fatalf("a connection admitted under a retired epoch was served: %+v", response)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a connection under a retired epoch was neither served nor refused: %v", err)
	}
	if auth.refusalCount() != 1 {
		t.Fatalf("the endpoint refused the publication %d times, want 1", auth.refusalCount())
	}
	if admitted := endpoint.AdmittedSessions(); len(admitted) != 0 {
		t.Fatalf("admitted sessions = %v, want none: the epoch was retired", admitted)
	}
}

// AND THE SAME DECISION UNDER A LIVE EPOCH IS ADMITTED, which is the pair that
// makes the refusal above about the epoch rather than about the authorizer.
func TestAPublicationNamingALiveEpochIsAdmitted(t *testing.T) {
	auth := &sessionAuthorizer{sessions: []string{"session-1"}, epoch: 2}
	cfg := endpointConfig(t, &testAuthorizer{}, &testDispatcher{out: `{"held":[]}`})
	cfg.Auth = auth
	endpoint := startEndpoint(t, cfg)

	// Interval 1 is over; interval 2 is the session's live one. A retirement of
	// the FIRST must not refuse a decision taken in the second, which is what a
	// floor that only ever moves forward buys.
	if closed := endpoint.RetireAdmissions("session-1", 1); closed != 0 {
		t.Fatalf("retiring the ended interval closed %d, want 0", closed)
	}

	conn := dialEndpoint(t, endpoint)
	callOver(t, conn, "live")
	awaitAdmitted(t, endpoint, "session-1")

	// Retiring it now closes it, so the connection was admitted under the live
	// epoch and not merely tolerated.
	if closed := endpoint.RetireAdmissions("session-1", 2); closed != 1 {
		t.Fatalf("RetireAdmissions = %d, want 1", closed)
	}
	expectClosed(t, conn)
}

// A CALL IN FLIGHT IS CANCELED BY THE RETIREMENT. Closing the connection is what
// ends the request contexts derived from it, and "the next mutation from an
// agent a person just turned off" is exactly a call that was running when the
// answer went away.
func TestARetiredIntervalCancelsTheCallInFlightOnItsConnection(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	dispatch := &testDispatcher{out: `{"held":[]}`, started: started, cancelled: cancelled, release: release}
	auth := &sessionAuthorizer{sessions: []string{"session-1"}, epoch: 1}
	cfg := endpointConfig(t, &testAuthorizer{}, dispatch)
	cfg.Auth = auth
	endpoint := startEndpoint(t, cfg)

	conn := dialEndpoint(t, endpoint)
	if _, err := io.WriteString(conn, `{"jsonrpc":"2.0","id":"held","method":"workers.spawn","params":{"command":"claude","task":"hold this call"}}`+"\n"); err != nil {
		t.Fatalf("write the call that stays in flight: %v", err)
	}
	awaitAdmitted(t, endpoint, "session-1")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never reached the dispatcher")
	}

	endpoint.RetireAdmissions("session-1", 1)

	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the retired interval left the call in flight: its context was never canceled")
	}
}

// refusedAuthorizer is a session authorizer that refuses, so a test can watch
// what the endpoint records for a connection it did NOT admit.
type refusedAuthorizer struct{ err error }

func (a refusedAuthorizer) Admit(Peer, func(string, AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error) {
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

// grantingWithoutPublishing is an authorizer that returns a grant and never
// records it — the one shape the endpoint cannot close, because nothing knows
// which interval the connection belongs to.
type grantingWithoutPublishing struct{}

func (grantingWithoutPublishing) Admit(Peer, func(string, AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error) {
	return assistant.ToolInvocation{
		Context:    context.Background(),
		RunContext: agenttools.RunContext{Session: "session-1"},
	}, func() {}, nil
}

// THE ENDPOINT GRANTS NOTHING IT DID NOT PUBLISH. An authorizer that admitted
// without recording would produce a connection no retirement can close, which is
// the defect this whole mechanism exists for, so the connection is refused
// rather than served.
func TestAnUnpublishedAdmissionIsRefused(t *testing.T) {
	cfg := endpointConfig(t, &testAuthorizer{}, &testDispatcher{out: `{"held":[]}`})
	cfg.Auth = grantingWithoutPublishing{}
	endpoint := startEndpoint(t, cfg)
	conn := dialEndpoint(t, endpoint)

	response, err := holdingsOver(t, conn, "unpublished")
	if err == nil && response.Error == nil {
		t.Fatalf("an unpublished admission was served: %+v", response)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("an unpublished admission was neither served nor refused: %v", err)
	}
	if admitted := endpoint.AdmittedSessions(); len(admitted) != 0 {
		t.Fatalf("admitted sessions = %v, want none", admitted)
	}
}

// publishedThenFailed publishes the admission and then returns an error — the
// shape a decision has when it records itself before the grant is complete and
// cannot finish.
type publishedThenFailed struct{ err error }

func (a publishedThenFailed) Admit(_ Peer, publish func(session string, epoch AdmissionEpoch) bool) (assistant.ToolInvocation, func(), error) {
	publish("session-1", 1)
	return assistant.ToolInvocation{}, nil, a.err
}

// A DECISION THAT PUBLISHED AND THEN FAILED LEAVES NOTHING BEHIND. The record is
// not conditional on the decision succeeding: this serve loop never starts, so
// nothing else removes it, and the endpoint would go on reporting a session as
// admitted with no connection behind it.
func TestAPublishedAdmissionThatThenFailsLeavesNoRecord(t *testing.T) {
	cfg := endpointConfig(t, &testAuthorizer{}, &testDispatcher{})
	cfg.Auth = publishedThenFailed{err: ErrSessionCallerActive}
	endpoint := startEndpoint(t, cfg)
	conn := dialEndpoint(t, endpoint)

	response, err := holdingsOver(t, conn, "published-then-failed")
	if err == nil && response.Error == nil {
		t.Fatalf("a failed admission was served: %+v", response)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("a failed admission was neither served nor refused: %v", err)
	}
	if admitted := endpoint.AdmittedSessions(); len(admitted) != 0 {
		t.Fatalf("admitted sessions = %v, want none: the decision failed after publishing", admitted)
	}
}
