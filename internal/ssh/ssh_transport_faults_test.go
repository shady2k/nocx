package ssh

// What the product must be able to say about a connection, per network
// condition — stated as tests rather than as prose, over the seam the product
// actually uses (Connect + WithKeepalive + WithLivenessObserver).
//
// The four conditions and the answer each one demands:
//
//	server answers            → say nothing; a healthy link is silent
//	server answers LATE       → say "not responding", do NOT end the session
//	slow server recovers      → say "responding again"
//	server stops answering    → say "not responding", then END the session
//	wire cut                  → end the session; no prober involved
//
// The middle three are the ones this file exists for. They were unstageable
// before faultProxy, and every one of them is a shape a person hits: a laptop
// that suspended, a host under load, a NAT that dropped the flow.

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/shady2k/nocx/internal/log"
)

// livenessLog records what the prober told the product, in order.
type livenessLog struct {
	mu sync.Mutex
	at []bool
}

func (l *livenessLog) observe(r Reachability) {
	responsive := r.Responsive
	l.mu.Lock()
	defer l.mu.Unlock()
	l.at = append(l.at, responsive)
}

func (l *livenessLog) snapshot() []bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]bool(nil), l.at...)
}

func (l *livenessLog) sawUnresponsive() bool {
	for _, r := range l.snapshot() {
		if !r {
			return true
		}
	}
	return false
}

// connectThroughFault opens a real session through a switchable network and
// returns the channel plus the relay that decides what the network is doing.
//
// The keepalive interval is short so a test states a NUMBER OF PROBES rather
// than a duration: countMax=3 at 50ms means the give-up is due within ~150ms,
// and a second of waiting is twenty times that. Nothing here waits on a
// duration to decide an outcome — every assertion polls for an observable.
func connectThroughFault(t *testing.T, obs LivenessObserver) (Channel, *faultProxy, *testSSHServer) {
	t.Helper()
	srv := startTestSSHServer(t)
	t.Cleanup(srv.close)

	proxy := newFaultProxy(t, srv.addr)
	host, portStr, err := net.SplitHostPort(proxy.addr)
	if err != nil {
		t.Fatalf("split proxy addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	client, err := NewReal(log.NewSlogAdapter(nil), WithKnownHostsFile(writeKnownHosts(t, srv, proxy.addr)))
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ch, err := client.Connect(
		context.Background(), host,
		WithPort(port),
		WithUser("test"),
		WithAuthMethods([]gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)}),
		WithKeepalive(50*time.Millisecond, 3),
		WithLivenessObserver(obs),
		WithTimeout(10*time.Second),
	)
	if err != nil {
		t.Fatalf("Connect through fault proxy: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	return ch, proxy, srv
}

// waitFor polls for an observable rather than sleeping for a duration: a test
// that needs a slow machine to pass is broken on a fast one too (AGENTS.md).
func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Logf("timed out waiting for %s", what)
	return false
}

func channelEnded(ch Channel) func() bool {
	return func() bool {
		select {
		case <-ch.Done():
			return true
		default:
			return false
		}
	}
}

// A healthy link is never reported UNRESPONSIVE. It does report a round trip
// on every probe, and that is not noise reaching a person: the prober's job is
// to measure, and internal/session grades the measurement and republishes only
// when the grade changes, so a healthy connection still puts nothing on the
// wire and nothing on the screen. The silence that matters is asserted where
// it is owned; asserting it here would pin the wrong layer.
func TestTransportFault_HealthyLinkIsNeverCalledUnresponsive(t *testing.T) {
	var seen livenessLog
	ch, _, _ := connectThroughFault(t, seen.observe)

	// Ten intervals of a link nobody is interfering with.
	time.Sleep(500 * time.Millisecond)

	if seen.sawUnresponsive() {
		t.Fatalf("a healthy connection was reported unresponsive: %v", seen.snapshot())
	}
	if channelEnded(ch)() {
		t.Fatal("a healthy connection ended on its own")
	}
}

// THE LAPTOP THAT SLEPT. The socket is open, writes succeed, nothing comes
// back. The product must say the host is not answering and then end the
// session — the two halves of nocx-iarf9's vocabulary, in that order.
//
// This is the one that fails today: the prober blocks inside its first probe,
// so neither half ever happens and the pane sits on a dead pipe in silence.
func TestTransportFault_SilentDeathIsReportedThenEndsTheSession(t *testing.T) {
	var seen livenessLog
	ch, proxy, _ := connectThroughFault(t, seen.observe)

	proxy.blackhole()

	if !waitFor(t, "the host to be reported unresponsive", 5*time.Second, seen.sawUnresponsive) {
		t.Errorf("a host that stopped answering was never reported unresponsive (reports: %v)", seen.snapshot())
	}
	if !waitFor(t, "the session to end", 5*time.Second, channelEnded(ch)) {
		t.Fatalf("a host that stopped answering never ended the session (reports: %v)", seen.snapshot())
	}
}

// THE LOADED SERVER. It answers, just late. That is not a loss and must not
// end anything — a tab killed because the far host was busy is lost work.
func TestTransportFault_SlowServerDoesNotEndTheSession(t *testing.T) {
	var seen livenessLog
	ch, proxy, _ := connectThroughFault(t, seen.observe)

	// The delay is applied in EACH direction, so a probe's round trip is
	// twice it: 60ms against a 50ms interval (late — it cannot finish inside
	// its own tick) and well inside the 150ms budget (alive). That gap is the
	// whole statement: slow is not gone.
	proxy.slow(30 * time.Millisecond)
	time.Sleep(time.Second)

	if channelEnded(ch)() {
		t.Fatalf("a server that answered late was treated as dead (reports: %v)", seen.snapshot())
	}
}

// AND IT DOES NOT COME BACK, DELIBERATELY. A host that went silent has had
// its connection closed, so a host that starts answering again finds nothing
// to answer on — the pane must be given a NEW session, which is the reconnect
// work and not the prober's job.
//
// This is not a limitation anyone chose freely: a probe parked inside
// x/crypto holds globalSentMu, so the next tick's probe would block on the
// mutex rather than on the network. There is no retry to give it. Closing is
// the only thing that frees it, and after closing there is nothing left to
// retry against.
//
// WHICH LEAVES A GAP THIS TEST EXISTS TO NAME. The recoverable half of
// nocx-iarf9's vocabulary — "not responding" that later becomes "responding
// again" — is now reachable only from a probe that ERRORS while the transport
// still works, which is rare. The condition a person actually meets is a host
// that answers LATE, and lateness is not a failed probe: it is a round-trip
// time. So the revisable statement has to be driven by RTT, and until it is,
// the product can say "gone" and cannot say "struggling".
func TestTransportFault_SilentHostIsNotRecoverableInPlace(t *testing.T) {
	var seen livenessLog
	ch, proxy, _ := connectThroughFault(t, seen.observe)

	proxy.blackhole()
	if !waitFor(t, "the session to end", 5*time.Second, channelEnded(ch)) {
		t.Fatalf("a silent host never ended the session (reports: %v)", seen.snapshot())
	}
	proxy.pass()

	// The host answers again; the session stays ended. Nothing revives it,
	// and nothing should: the transport it ran on is closed.
	if !channelEnded(ch)() {
		t.Error("a closed session reopened itself when the host came back")
	}
}

// THE WIRE CUT. The loud loss, staged from the middle of the wire. It needs no
// prober at all and is the path that already works — kept here so a change to
// the prober that breaks it is caught by the same table.
func TestTransportFault_CutWireEndsTheSession(t *testing.T) {
	var seen livenessLog
	ch, proxy, _ := connectThroughFault(t, seen.observe)

	proxy.cut()

	if !waitFor(t, "the session to end", 5*time.Second, channelEnded(ch)) {
		t.Fatalf("a cut wire did not end the session (reports: %v)", seen.snapshot())
	}
}

var _ = fmt.Sprintf

// A SLOW host must be reportable, and the only thing that can report it is
// the prober: it is the one place in nocx that measures a round trip to the
// far end. Without this, "the server is struggling" has no source at all and
// the product can say only "gone" (see the comment above
// TestTransportFault_SilentHostIsNotRecoverableInPlace).
func TestTransportFault_SlowHostReportsItsRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var slowest time.Duration
	obs := func(r Reachability) {
		mu.Lock()
		defer mu.Unlock()
		if r.RoundTrip > slowest {
			slowest = r.RoundTrip
		}
	}
	ch, proxy, _ := connectThroughFault(t, obs)
	proxy.slow(30 * time.Millisecond)

	// The delay is applied in each direction, so a probe's round trip is
	// about 60ms. Anything materially above one interval proves the number
	// is measured rather than invented; the exact value is the machine's.
	ok := waitFor(t, "a round trip to be measured", 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slowest >= 40*time.Millisecond
	})
	if !ok {
		mu.Lock()
		got := slowest
		mu.Unlock()
		t.Fatalf("slowest observed round trip = %v, want >= 40ms — the prober is not reporting how long the far end took", got)
	}
	if channelEnded(ch)() {
		t.Error("a slow host lost its session")
	}
}
