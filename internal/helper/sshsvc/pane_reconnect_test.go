//go:build nocx_local_ssh

package sshsvc_test

// A pane's reconnect, at the fault harness's layer (design §5.3, §5.4;
// nocx-oalo1.5.4): the offer the backend publishes, and the two mechanics it
// names — the connection closed first, and a new shell at the same endpoint.
//
// The policy itself is asserted where it lives (internal/panedriver): whether
// the backend retries by itself is that package's decision, and this file drives
// its DECISION against the real helper's observations. What only this stand can
// show is that the offer appears from real events within the clock's thirty
// seconds, and that "close the socket first" really produces a fresh dial
// instead of a shell parked behind the dead flow.

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/sshtest"
	"github.com/shady2k/nocx/internal/waittest"
)

// reconnectStand is the 5.3 stand plus the coordinator's policy driver, and the
// pane's binding to the connection the helper named.
type reconnectStand struct {
	*stand
	proxy   *sshtest.FaultProxy
	clock   *hostconntest.Clock
	driver  *panedriver.Driver
	snapMu  sync.Mutex
	snaps   []panedriver.Snapshot
	actions atomic.Int32
}

func newReconnectStand(t *testing.T, clock *hostconntest.Clock, policy string) *reconnectStand {
	t.Helper()
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	rs := &reconnectStand{stand: stand, proxy: proxy, clock: clock}
	rs.driver = panedriver.New("instance-1", log.NewSlogAdapter(nil),
		panedriver.WithPolicy(func() string { return policy }))
	rs.driver.SetPublisher(func(s panedriver.Snapshot) {
		rs.snapMu.Lock()
		rs.snaps = append(rs.snaps, s)
		rs.snapMu.Unlock()
	})
	rs.driver.SetAction(func(string, uint64) error {
		rs.actions.Add(1)
		return nil
	})
	return rs
}

// lastSnapshot answers the newest observation for a pane.
func (r *reconnectStand) lastSnapshot(paneID string) (panedriver.Snapshot, bool) {
	r.snapMu.Lock()
	defer r.snapMu.Unlock()
	for i := len(r.snaps) - 1; i >= 0; i-- {
		if r.snaps[i].PaneID == paneID {
			return r.snaps[i], true
		}
	}
	return panedriver.Snapshot{}, false
}

// findSnapshot answers the first observation matching a predicate.
func (r *reconnectStand) findSnapshot(match func(panedriver.Snapshot) bool) (panedriver.Snapshot, bool) {
	r.snapMu.Lock()
	defer r.snapMu.Unlock()
	for _, snap := range r.snaps {
		if match(snap) {
			return snap, true
		}
	}
	return panedriver.Snapshot{}, false
}

// TestTheOfferNamesTheShellWithinThirtySecondsAndTheReconnectDialsAnew is the
// task's first harness case, end to end at this layer: a silenced plain shell,
// an offer naming one shell inside the injected clock's thirty seconds, and the
// two moves the offer stands for — the connection closed, then a new shell at
// the same endpoint.
func TestTheOfferNamesTheShellWithinThirtySecondsAndTheReconnectDialsAnew(t *testing.T) {
	clock := hostconntest.New()
	rs := newReconnectStand(t, clock, panedriver.PolicyAsk)
	destination := destinationAt(t, rs.proxy.Addr())
	ctx := host.WithConnection(context.Background(), rs.helper)

	connectionID, err := rs.client.ConnectionID(ctx, destination)
	if err != nil {
		t.Fatalf("name the connection: %v", err)
	}
	rs.driver.Bind("pane-1", connectionID)

	go func() {
		for ev := range rs.connEvents {
			rs.driver.ApplyFrom(rs.peerInstance, ev)
		}
	}()

	shell := rs.openShell(t, destination)
	waittest.WaitForDetail(t, "the connected observation",
		func() string {
			snap, _ := rs.lastSnapshot("pane-1")
			return "last snapshot: " + snap.Transport.State
		},
		func() bool {
			snap, ok := rs.lastSnapshot("pane-1")
			return ok && snap.Transport.State == proto.ConnectionConnected
		})
	if snap, _ := rs.lastSnapshot("pane-1"); snap.Reconnect != nil {
		t.Fatalf("a healthy pane offers %+v", snap.Reconnect)
	}

	// The flow stops answering; new connections still pass. The offer must
	// arrive inside the policy's thirty seconds, naming this person's shell.
	rs.proxy.SilenceEstablished()
	start := clock.Now()
	waittest.WaitForDetail(t, "the offer",
		func() string {
			snap, _ := rs.lastSnapshot("pane-1")
			return "last snapshot: " + snap.Transport.State
		},
		func() bool {
			snap, ok := rs.findSnapshot(func(s panedriver.Snapshot) bool {
				return s.PaneID == "pane-1" && s.Transport.State == proto.ConnectionStalled && s.Reconnect != nil
			})
			if ok && snap.Reconnect.EndsShells != 1 {
				t.Fatalf("the offer names %d shell(s), want 1", snap.Reconnect.EndsShells)
			}
			if !ok {
				// ONE SECOND AT A TIME: the reading the assertion above takes
				// is the policy's, not the poll's.
				clock.Advance(time.Second)
			}
			return ok
		})
	if elapsed := time.Duration(clock.Now() - start); elapsed > 30*time.Second {
		t.Fatalf("the offer took %v of simulated time, want within 30s", elapsed)
	}

	// THE PRESS. The backend's first move closes the connection, and the second
	// asks for a shell — which must DIAL, not join the stalled flow.
	closed, err := rs.client.Disconnect(ctx, destination)
	if err != nil {
		t.Fatalf("close the connection: %v", err)
	}
	if !closed {
		t.Fatal("the helper reported no connection to close for a live stalled connection")
	}
	// And it is idempotent: a second ask finds nothing to close.
	if again, againErr := rs.client.Disconnect(ctx, destination); againErr != nil || again {
		t.Fatalf("second disconnect = %v, %v; want false, nil", again, againErr)
	}
	waittest.WaitFor(t, "the old shell's transport to end", func() bool {
		select {
		case <-shell.Done():
			return true
		default:
			return false
		}
	})
	rs.proxy.Pass()
	fresh, err := rs.service.OpenShell(ctx, sshsvc.ShellSpec{
		Destination: destination, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open a shell after the reconnect: %v", err)
	}
	defer func() { _ = fresh.Close() }()
	if fresh == shell {
		t.Fatal("the reconnect returned the same shell: it never dialed")
	}
	// The new shell is usable: it is a real channel on a fresh connection.
	waittest.WaitFor(t, "the new connection to reach connected", func() bool {
		snap, ok := rs.lastSnapshot("pane-1")
		return ok && snap.Transport.State == proto.ConnectionConnected
	})
}

// TestUnderNeverADropOffersTheButtonAndNothingReconnectsByItself is DONE WHEN's
// second case: the policy changes what the BACKEND does, not whether the person
// may choose.
func TestUnderNeverADropOffersTheButtonAndNothingReconnectsByItself(t *testing.T) {
	clock := hostconntest.New()
	rs := newReconnectStand(t, clock, panedriver.PolicyNever)
	destination := destinationAt(t, rs.proxy.Addr())
	ctx := host.WithConnection(context.Background(), rs.helper)

	connectionID, err := rs.client.ConnectionID(ctx, destination)
	if err != nil {
		t.Fatalf("name the connection: %v", err)
	}
	rs.driver.Bind("pane-1", connectionID)
	go func() {
		for ev := range rs.connEvents {
			rs.driver.ApplyFrom(rs.peerInstance, ev)
		}
	}()

	shell := rs.openShell(t, destination)
	waittest.WaitFor(t, "the connected observation", func() bool {
		snap, ok := rs.lastSnapshot("pane-1")
		return ok && snap.Transport.State == proto.ConnectionConnected
	})

	// The link is cut: the transport ended, and no retry may happen by itself.
	rs.proxy.Cut()
	waittest.WaitForDetail(t, "the dropped observation",
		func() string {
			snap, _ := rs.lastSnapshot("pane-1")
			return "last snapshot: " + snap.Transport.State
		},
		func() bool {
			_, ok := rs.findSnapshot(func(s panedriver.Snapshot) bool {
				return s.PaneID == "pane-1" && s.Transport.State == proto.ConnectionReconnecting && s.Reconnect != nil &&
					s.Transport.Cause == "lost-link"
			})
			return ok
		})
	if got := rs.actions.Load(); got != 0 {
		t.Fatalf("policy never ran %d reconnect(s) by itself", got)
	}
	_ = shell
}

// TestTheDisconnectShapesConformToTheirFrozenContract pins the two shapes the
// reconnect added to the helper ABI (nocx-oalo1.5.4): the params a coordinator
// sends and the answer it decodes, against the schemas that freeze them.
func TestTheDisconnectShapesConformToTheirFrozenContract(t *testing.T) {
	paramsSchema := loadHelperSchema(t, "ssh.disconnect.params.schema.json")
	resultSchema := loadHelperSchema(t, "ssh.disconnect.schema.json")

	params, err := json.Marshal(proto.DisconnectParams{Destination: destinationAt(t, "127.0.0.1:22")})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if schemaErr := validateHelperJSON(paramsSchema, params); schemaErr != nil {
		t.Fatalf("disconnect params do not satisfy their contract: %v\n%s", schemaErr, params)
	}
	result, err := json.Marshal(proto.DisconnectResult{ConnectionID: "0123456789abcdef0123456789abcdef", Closed: true})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if err := validateHelperJSON(resultSchema, result); err != nil {
		t.Fatalf("disconnect result does not satisfy its contract: %v\n%s", err, result)
	}
}

// TestTheConnectionStateContractAdmitsOnlyPossibleShapes is finding 6 of the
// 5.3 review: the frozen helper ABI must not accept a state carrying a field
// its state cannot own, or a payload that satisfies it can produce a renderer
// payload the stricter contract forbids.
func TestTheConnectionStateContractAdmitsOnlyPossibleShapes(t *testing.T) {
	schema := loadHelperSchema(t, "ssh.connection-state.schema.json")
	valid := []proto.ConnectionStateEvent{
		{ConnectionID: "conn-1", Seq: 1, State: proto.ConnectionOpening, Stage: "tcp"},
		{ConnectionID: "conn-1", Seq: 2, State: proto.ConnectionWaitingPerson, Person: "password"},
		{ConnectionID: "conn-1", Seq: 3, State: proto.ConnectionConnected, PlainShells: 1},
		{ConnectionID: "conn-1", Seq: 4, State: proto.ConnectionStalled, StateMs: 45000, PlainShells: 1, LookAnsweredAt: "2026-11-14T07:00:00Z"},
		{ConnectionID: "conn-1", Seq: 5, State: proto.ConnectionReconnecting, Attempt: 2, Cause: "lost-link"},
		{ConnectionID: "conn-1", Seq: 6, State: proto.ConnectionFailed, Cause: "host-key-changed"},
		{ConnectionID: "conn-1", Seq: 7, State: proto.ConnectionEnded, Cause: "released"},
	}
	for i, ev := range valid {
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal %d: %v", i, err)
		}
		if err := validateHelperJSON(schema, raw); err != nil {
			t.Fatalf("a possible %s was refused: %v\n%s", ev.State, err, raw)
		}
	}

	invalid := []proto.ConnectionStateEvent{
		{ConnectionID: "conn-1", Seq: 1, State: proto.ConnectionOpening},
		{ConnectionID: "conn-1", Seq: 2, State: proto.ConnectionConnected, Stage: "tcp"},
		{ConnectionID: "conn-1", Seq: 3, State: proto.ConnectionConnected, Cause: "released"},
		{ConnectionID: "conn-1", Seq: 4, State: proto.ConnectionWaitingPerson, Stage: "tcp"},
		{ConnectionID: "conn-1", Seq: 5, State: proto.ConnectionReconnecting, Attempt: 1},
		{ConnectionID: "conn-1", Seq: 6, State: proto.ConnectionFailed},
		{ConnectionID: "conn-1", Seq: 7, State: proto.ConnectionStalled, Stage: "tcp"},
	}
	for i, ev := range invalid {
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal %d: %v", i, err)
		}
		if err := validateHelperJSON(schema, raw); err == nil {
			t.Fatalf("an impossible %s was admitted: %s", ev.State, raw)
		}
	}
}
