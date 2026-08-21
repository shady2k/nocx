package apisend

// Design §12.1 and the cancellation boundary of §7.1, asked of THE RUN
// rather than of a part.
//
// The four calls a bare send makes — resolve, connect, handshake, read the
// body — are covered in sender_failure_test.go. Two things are not, and both
// are properties of the whole executor rather than of any piece of it:
//
//  1. THE POOL LEASE. Acquiring it is an external call like any other, and
//     the failure that matters is not "an error came back" (client_test.go
//     has that) but that NOTHING WENT OUT: a route the user chose for its
//     bastion must never degrade into this machine's own interface (§6.5).
//
//  2. CANCELLATION, which is stated honestly rather than implied away.
//     tunnelConn.Dial takes no context, so a blocked remote dial cannot be
//     interrupted and no test here will pretend otherwise. Two claims are
//     asserted and they are deliberately not the same claim:
//
//     — the bounded dial deadline ends the run, and the connection that
//     arrives after it is CLOSED and never produces a run;
//     — a run cancelled while its dial is in flight produces NO RUN and
//     puts nothing on the wire. Its late connection is NOT closed by this
//     layer, because net/http does not give up on the dial when the
//     request that started it is cancelled — measured, not assumed, and
//     written down at the test rather than hidden behind an assertion
//     nothing performs.
//
// Nothing below counts goroutines. runtime.NumGoroutine is not an
// observation — it is polluted by runtime goroutines that have nothing to do
// with this package and it answers differently depending on when it is
// asked, which is the timing dependence AGENTS.md forbids outright. The
// observables used instead are: the connection the adapter closed
// (recordingConn.closed), the server's own record of what reached it, the
// lease's own dial count, and the Response the caller was handed.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/apicoll"
)

// leasePool is the pool as the environment's route table reaches it: an
// acquire that can refuse. It wraps the fake pool the dialer tests already
// use rather than declaring a second one — one behaviour, one owner.
type leasePool struct {
	*fakePool
	refuse error
	// block, when non-nil, is installed on EVERY lease this pool grants.
	// It has to be the pool's rather than a lease the test acquired for
	// itself: instanceFor takes its own lease through the route table, so a
	// block installed on any other one would be a block on a lease nothing
	// dials.
	block chan struct{}

	// last is written on the goroutine that runs the send (the route table
	// is called from instanceFor) and read by the test, so it is behind the
	// pool's own mutex. -race is the assertion that this is not decoration.
	last *fakeLease
}

func (p *leasePool) setLast(l *fakeLease) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = l
}

func (p *leasePool) lastLease() *fakeLease {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

func newLeasePool() *leasePool { return &leasePool{fakePool: newFakePool()} }

// routes is the table the sender is given. It is where the lease is taken,
// which is why a refusal here is the failure this file is about: the sender
// never sees a route at all, so there is nothing for it to fall back to.
func (p *leasePool) routes(dialTimeout time.Duration) Routes {
	return func(_ context.Context, routeID string) (Route, error) {
		if p.refuse != nil {
			return nil, p.refuse
		}
		l := p.acquire(routeID)
		l.blockOn = p.block
		p.setLast(l)
		return sshRoute(NewSSHDialer(l, dialTimeout)), nil
	}
}

// countingServer records what actually arrived. "The server was never
// reached" is the assertion every refusal below is really making, and it can
// only be made by something on the other end that counts.
func countingServer(t *testing.T, body string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// onceClose closes c the first time the returned function is called, so a
// test can release a blocked dial mid-way and still defer the release for
// the paths that fail before reaching it.
func onceClose(c chan struct{}) func() {
	var once sync.Once
	return func() { once.Do(func() { close(c) }) }
}

// noRun is the "and never produces a run" half, spelled once. A run is a
// Response; a cancelled or refused send must hand back the zero value of
// one, not a partly filled one that a surface would render as a result.
func noRun(t *testing.T, got Response, what string) {
	t.Helper()
	if !reflect.DeepEqual(got, Response{}) {
		t.Errorf("%s produced a run: %+v", what, got)
	}
}

// ─── 1. the pool lease refused ─────────────────────────────────────────────

// TestSend_APoolThatRefusesALeaseSendsNothingAtAll. A pool with no
// connection for this environment — the tunnel was never opened, the
// profile is gone, the pool is exhausted — refuses the acquire. The send
// must fail, and the assertion that carries the weight is the SERVER'S:
// nothing arrived. A fallback here would put a production request on this
// machine's own interface, around the bastion the user chose, and it would
// look like a success.
func TestSend_APoolThatRefusesALeaseSendsNothingAtAll(t *testing.T) {
	srv, hits := countingServer(t, "reached directly")
	pool := newLeasePool()
	pool.refuse = errors.New("no connection in the pool for this environment")

	got, err := New(WithRoutes(pool.routes(time.Minute))).
		Send(context.Background(), apicollGet(srv.URL), Key{RouteID: "prod"})
	if !errors.Is(err, pool.refuse) {
		t.Fatalf("Send: err = %v, want the pool's refusal", err)
	}
	noRun(t, got, "a refused lease")
	if n := hits.Load(); n != 0 {
		t.Fatalf("the server was reached %d times without a lease — the request went around the bastion", n)
	}
	if n := pool.connectionCount(); n != 0 {
		t.Errorf("%d connections were opened although the lease was refused", n)
	}
}

// TestSend_APoolThatGrantsALeaseSendsThroughIt is the pair, on THE SAME POOL
// with the refusal taken off: what the test above measures is the refusal
// and nothing else. The lease's own dial count is what says the bytes went
// through the tunnel rather than beside it.
func TestSend_APoolThatGrantsALeaseSendsThroughIt(t *testing.T) {
	srv, hits := countingServer(t, "through the tunnel")
	pool := newLeasePool()
	pool.refuse = errors.New("no connection in the pool for this environment")
	c := New(WithRoutes(pool.routes(time.Minute)))

	if _, err := c.Send(context.Background(), apicollGet(srv.URL), Key{RouteID: "prod"}); err == nil {
		t.Fatal("Send succeeded while the pool was refusing; the pair below would prove nothing")
	}

	pool.refuse = nil
	got, err := c.Send(context.Background(), apicollGet(srv.URL), Key{RouteID: "prod"})
	if err != nil {
		t.Fatalf("Send with a granted lease: %v", err)
	}
	if got.Status != http.StatusOK || got.Text != "through the tunnel" {
		t.Fatalf("status %d body %q, want 200 and the body", got.Status, got.Text)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("the server was reached %d times, want exactly 1", n)
	}
	if l := pool.lastLease(); l == nil || l.dialCount() != 1 {
		t.Errorf("the lease was dialled %v times, want 1 — the bytes must go through the tunnel", l)
	}
	if n := pool.connectionCount(); n != 1 {
		t.Errorf("%d connections opened, want 1 — a lease references a pooled connection (AD-7)", n)
	}
}

// ─── 2. the bounded dial deadline ──────────────────────────────────────────

// TestSend_ABoundedDialDeadlineEndsTheRun. A user pressing Send passes a
// context with no deadline, so nothing but the dial timeout can end a dial
// into a bastion that has stopped answering — the context cannot reach past
// tunnelConn.Dial's signature.
//
// The millisecond here is not a race the test can lose. The far side is
// blocked on a channel this test owns and never completes, so the timeout is
// the only outcome available however slow or fast the machine is; a longer
// value would only make the test slower, never more correct.
func TestSend_ABoundedDialDeadlineEndsTheRun(t *testing.T) {
	srv, hits := countingServer(t, "unreachable")
	pool := newLeasePool()
	pool.block = make(chan struct{})
	unblock := onceClose(pool.block)
	defer unblock()
	c := New(WithRoutes(pool.routes(time.Millisecond)))

	got, err := c.Send(context.Background(), apicollGet(srv.URL), Key{RouteID: "prod"})
	if !errors.Is(err, ErrSSHDialTimeout) {
		t.Fatalf("Send: err = %v, want ErrSSHDialTimeout", err)
	}
	noRun(t, got, "a dial that timed out")
	if n := hits.Load(); n != 0 {
		t.Errorf("the server was reached %d times by a run whose dial timed out", n)
	}

	// And the other half of the sentence: the connection the far side
	// finally opens belongs to nobody, so the adapter closes it. The bound
	// is the adapter's own timer, so this is the path on which closeLate
	// runs under a send — see the cancellation test below for the path on
	// which it does not, and why.
	unblock()
	waitFor(t, "the late connection to be closed", func() bool {
		conn := pool.lastLease().conn()
		return conn != nil && conn.closed.Load()
	})
	if n := hits.Load(); n != 0 {
		t.Errorf("the server was reached %d times by the late connection of a timed-out dial", n)
	}
}

// TestSend_ADialThatAnswersWithinTheDeadlineProducesARun is the pair: the
// same bound, the same route, a far side that answers.
func TestSend_ADialThatAnswersWithinTheDeadlineProducesARun(t *testing.T) {
	srv, hits := countingServer(t, "answered")
	pool := newLeasePool()

	got, err := New(WithRoutes(pool.routes(time.Minute))).
		Send(context.Background(), apicollGet(srv.URL), Key{RouteID: "prod"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Status != http.StatusOK || got.Text != "answered" {
		t.Fatalf("status %d body %q, want 200 answered", got.Status, got.Text)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("the server was reached %d times, want 1", n)
	}
}

// ─── 3. a connection that arrives after cancellation ───────────────────────

// TestSend_AConnectionArrivingAfterCancellationNeverProducesARun.
//
// THE BOUNDARY, STATED RATHER THAN IMPLIED AWAY. Two layers are in play and
// they guarantee different things, so the test says which is which:
//
//   - The adapter's guarantee is that a connection arriving after the dial
//     has been given up on is closed and handed to nobody. It fires when the
//     DIAL is given up on — its own timer (the test above) or a dial context
//     that is done (ssh_dialer_test.go).
//   - net/http does NOT make a cancelled request's dial one of those.
//     Measured on go1.26.5 with a route that reports the context it was
//     dialled with: the request context is cancelled, Send returns at once,
//     and the DIAL context stays live — the transport keeps the dial for
//     whatever asks next and parks the connection in its idle pool.
//
// So at the level of a run the closing is not this layer's to claim, and a
// test asserting it here would be asserting something no code performs. What
// IS guaranteed at this level, and is what the user is owed, is asserted
// instead: the cancelled send produces NO RUN, and nothing of the request
// ever reaches the far side — before the late connection arrives or after.
func TestSend_AConnectionArrivingAfterCancellationNeverProducesARun(t *testing.T) {
	srv, hits := countingServer(t, "must never be served")
	pool := newLeasePool()
	pool.block = make(chan struct{})
	c := New(WithRoutes(pool.routes(time.Minute)))

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		resp Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, sendErr := c.Send(ctx, apicollGet(srv.URL), Key{RouteID: "prod"})
		done <- result{resp: r, err: sendErr}
	}()

	// Cancel only once the remote dial is genuinely in flight; cancelling
	// earlier would be answered by the pre-dial check and there would be no
	// late connection for this test to be about.
	waitFor(t, "the remote dial to be in flight", func() bool {
		return pool.lastLease() != nil && pool.lastLease().dialCount() == 1
	})
	cancel()

	got := <-done
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Send: err = %v, want context.Canceled", got.err)
	}
	noRun(t, got.resp, "a cancelled send")
	if n := hits.Load(); n != 0 {
		t.Fatalf("the server was reached %d times by a cancelled run", n)
	}

	// Let the far side answer, and wait on the connection itself rather than
	// on any duration. Once it exists, the run is long over: it produced no
	// Response, it was dialled once and not again, and the server still says
	// nothing arrived.
	close(pool.block)
	waitFor(t, "the late connection to arrive", func() bool { return pool.lastLease().conn() != nil })
	if n := pool.lastLease().dialCount(); n != 1 {
		t.Errorf("the lease was dialled %d times for one cancelled run, want 1", n)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the server was reached %d times through the connection that arrived after cancellation", n)
	}
}

// TestSend_ARunThatIsNotCancelledKeepsItsConnection is the pair to the
// closing above: the adapter closes a LATE connection, never the one a live
// run is using. Without this, "closed on cancellation" would be satisfied by
// an adapter that closed every connection it ever opened.
func TestSend_ARunThatIsNotCancelledKeepsItsConnection(t *testing.T) {
	srv, hits := countingServer(t, "served")
	pool := newLeasePool()

	got, err := New(WithRoutes(pool.routes(time.Minute))).
		Send(context.Background(), apicollGet(srv.URL), Key{RouteID: "prod"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Text != "served" || hits.Load() != 1 {
		t.Fatalf("body %q, %d hits; want the served body and one hit", got.Text, hits.Load())
	}
	if conn := pool.lastLease().conn(); conn == nil {
		t.Fatal("no connection was recorded for a send that completed")
	} else if conn.closed.Load() {
		t.Error("the connection of a completed run was closed as though the run had been cancelled")
	}
}

// ─── 4. cancelling in flight leaves no half-written run ────────────────────

// TestSend_CancellingMidBodyLeavesNoHalfWrittenRun. The body is where a
// partial result is actually possible: the status line and the headers have
// already arrived, and a sender that returned what it had would hand a
// surface a run showing 200 and half a payload — indistinguishable, once
// rendered, from a server that sent exactly that.
//
// The wait is on the server's own signal that it has written and flushed,
// never on a duration.
func TestSend_CancellingMidBodyLeavesNoHalfWrittenRun(t *testing.T) {
	wrote := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "64")
		_, _ = io.WriteString(w, "the first half")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(wrote)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		resp Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r, err := New().Send(ctx, apicollGet(srv.URL), Key{})
		done <- result{resp: r, err: err}
	}()

	<-wrote
	cancel()

	got := <-done
	if got.err == nil {
		t.Fatal("a send cancelled mid-body returned no error")
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", got.err)
	}
	// Which STEP reports it — the round trip or the body read — is not
	// asserted, and deliberately: the response has been written and flushed
	// by the server, but whether the transport has yet handed it to Do is
	// the transport's own scheduling. Asserting one of the two would be a
	// test that depends on timing, which AGENTS.md forbids. The claim that
	// matters is the same either way and is made below.
	noRun(t, got.resp, "a send cancelled mid-body")
}

// TestSend_TheSameBodyReadToTheEndProducesTheWholeRun is that pair: the
// identical exchange, not cancelled. It is what makes the test above a
// statement about cancellation rather than about a body of 64 bytes.
func TestSend_TheSameBodyReadToTheEndProducesTheWholeRun(t *testing.T) {
	const body = "the first halfand the second half, making sixty-four bytes......."
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	got, err := New().Send(context.Background(), apicollGet(srv.URL), Key{})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Status != http.StatusOK || got.Text != body {
		t.Fatalf("status %d body %q, want 200 and the whole body", got.Status, got.Text)
	}
	if got.Truncated {
		t.Error("Truncated = true for a body far below the ceiling")
	}
}

// TestSend_ARequestTheSenderCannotBuildNeverReachesTheRoute is the cheapest
// half of "no half-written run": a send that fails before the network is
// touched must also hand back nothing, and must not take a lease to do it.
func TestSend_ARequestTheSenderCannotBuildNeverReachesTheRoute(t *testing.T) {
	pool := newLeasePool()
	got, err := New(WithRoutes(pool.routes(time.Minute))).Send(context.Background(),
		apicoll.Request{Method: http.MethodGet, URL: "not-an-absolute-url"}, Key{RouteID: "prod"})
	if err == nil {
		t.Fatal("Send succeeded on a request that is not an absolute URL")
	}
	noRun(t, got, "a request that could not be built")
	if n := pool.leaseCount(); n != 0 {
		t.Errorf("%d leases were taken for a request that never left the sender", n)
	}
}
