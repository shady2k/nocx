//go:build nocx_local_ssh

package sshsvc_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/monoclock"
	"github.com/shady2k/nocx/internal/panedriver"
	"github.com/shady2k/nocx/internal/sshtest"
	"github.com/shady2k/nocx/internal/waittest"
)

// reconnectClock records timer registration AFTER the timer is armed. A test
// advances once from an observation, then waits for its result without moving
// time again: a slow worker cannot consume the simulated policy budget.
type reconnectClock struct {
	*hostconntest.Clock
	mu     sync.Mutex
	timers []time.Duration
}

func (c *reconnectClock) After(d time.Duration) <-chan monoclock.Nanos {
	ch := c.Clock.After(d)
	c.mu.Lock()
	c.timers = append(c.timers, d)
	c.mu.Unlock()
	return ch
}

func (c *reconnectClock) timerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// retryTimers excludes the connection guard's 15-second cadence. Each initial
// backoff is at most 1.2 seconds, including its jitter.
func (c *reconnectClock) retryTimers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, d := range c.timers {
		if d > 0 && d <= 1200*time.Millisecond {
			count++
		}
	}
	return count
}

// This stand uses the real helper subscription, shell opener and supervisor
// reconnect entry. There is no replacement action or synthetic loss event.
type reconnectStand struct {
	*stand
	fixture      *fixture
	proxy        *sshtest.FaultProxy
	clock        *reconnectClock
	destination  proto.SSHDestination
	ctx          context.Context
	driver       *panedriver.Driver
	mu           sync.Mutex
	snaps        []panedriver.Snapshot
	seq          uint64
	restored     chan *sshsvc.ShellChannel
	reopenErrors chan error
	generations  map[string]uint64
	connectionID string
}

func newReconnectStand(t *testing.T, policy string, panes ...string) *reconnectStand {
	t.Helper()
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := &reconnectClock{Clock: hostconntest.New()}
	s := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted,
		fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	r := &reconnectStand{
		stand: s, fixture: f, proxy: proxy, clock: clock,
		destination: destinationAt(t, proxy.Addr()), restored: make(chan *sshsvc.ShellChannel, 8), reopenErrors: make(chan error, 8), generations: make(map[string]uint64),
	}
	r.ctx = host.WithConnection(context.Background(), s.helper)
	r.driver = panedriver.New(s.peerInstance, nil)
	r.driver.SetAction(func(_ context.Context, _ string, _ uint64) error {
		shell, err := r.service.OpenShell(r.ctx, sshsvc.ShellSpec{Destination: r.destination, Cols: 80, Rows: 24})
		if err != nil {
			r.reopenErrors <- err
			return err
		}
		r.restored <- shell
		return nil
	})
	t.Cleanup(func() {
		for {
			select {
			case shell := <-r.restored:
				_ = shell.Close()
			default:
				return
			}
		}
	})
	r.driver.SetPublisher(func(snap panedriver.Snapshot) {
		r.mu.Lock()
		r.snaps = append(r.snaps, snap)
		r.mu.Unlock()
	})
	id, err := s.client.ConnectionID(r.ctx, r.destination)
	if err != nil {
		t.Fatalf("subscribe to the pane's connection: %v", err)
	}
	for _, pane := range panes {
		bound, bindErr := r.client.BindConnection(r.ctx, r.destination, pane, policy)
		if bindErr != nil {
			t.Fatalf("retain pane connection through helper: %v", bindErr)
		}
		if bound != id {
			t.Fatalf("binding names %q, subscription names %q", bound, id)
		}
		r.generations[pane] = r.driver.Bind(pane, id)
		t.Cleanup(func() { _ = r.client.ReleaseConnection(r.ctx, pane) })
	}
	r.connectionID = id
	go func() {
		for ev := range s.connEvents {
			r.mu.Lock()
			r.seq = ev.Seq
			r.mu.Unlock()
			r.driver.ApplyFrom(s.peerInstance, ev)
		}
	}()
	return r
}

func (r *reconnectStand) sequence() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq
}

func (r *reconnectStand) awaitRestored(t *testing.T) *sshsvc.ShellChannel {
	t.Helper()
	select {
	case shell := <-r.restored:
		t.Cleanup(func() { _ = shell.Close() })
		return shell
	case err := <-r.reopenErrors:
		t.Fatalf("reopen the pane's shell on return: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("no fresh shell opened on connection return")
	}
	return nil
}

func (r *reconnectStand) latest(pane string) panedriver.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.snaps) - 1; i >= 0; i-- {
		if r.snaps[i].PaneID == pane {
			return r.snaps[i]
		}
	}
	return panedriver.Snapshot{}
}

func (r *reconnectStand) awaitState(t *testing.T, pane, state string) panedriver.Snapshot {
	t.Helper()
	waittest.WaitForDetail(t, state, func() string { return r.latest(pane).Transport.State },
		func() bool { return r.latest(pane).Transport.State == state })
	return r.latest(pane)
}

func (r *reconnectStand) open(t *testing.T, pane string) *sshsvc.ShellChannel {
	t.Helper()
	shell := r.openShell(t, r.destination)
	r.driver.AdoptSession(pane, r.connectionID, "shell-"+pane, r.peerInstance, r.generations[pane])
	return shell
}

func TestTheOfferNamesTheShellWithinThirtySecondsAndTheReconnectDialsAnew(t *testing.T) {
	r := newReconnectStand(t, "ask", "pane-1")
	shell := r.open(t, "pane-1")
	healthy := r.awaitState(t, "pane-1", proto.ConnectionConnected)
	if healthy.Reconnect != nil {
		t.Fatalf("healthy shell offers %+v", healthy.Reconnect)
	}
	if err := r.client.RegisterReconnect(r.ctx, r.destination, r.sequence()); err == nil {
		t.Fatal("a healthy connection accepted reconnect without an offer")
	}
	waittest.WaitFor(t, "the probe timer to be armed", func() bool { return r.clock.timerCount() > 0 })
	r.proxy.SilenceEstablished()
	start := r.clock.Now()
	r.clock.Advance(hostconn.ProbeInterval + hostconn.ProbeDeadline)
	stalled := r.awaitState(t, "pane-1", proto.ConnectionStalled)
	if stalled.Reconnect == nil || stalled.Reconnect.EndsShells != 1 {
		t.Fatalf("offer = %+v, want one shell", stalled.Reconnect)
	}
	if elapsed := time.Duration(r.clock.Now() - start); elapsed > 30*time.Second {
		t.Fatalf("offer took %v, want at most 30s", elapsed)
	}
	// The look-only dial publishes another observation for this same stall.
	// Wait for that evidence before taking the press's sequence, so the test
	// does not race a valid newer offer with its earlier stalled snapshot.
	waittest.WaitFor(t, "the stalled connection's look evidence", func() bool {
		return r.latest("pane-1").Transport.HasLook
	})
	before := r.fixture.connections()
	if err := r.client.RegisterReconnect(r.ctx, r.destination, r.sequence()); err != nil {
		t.Fatalf("register reconnect through the helper: %v", err)
	}
	waittest.WaitFor(t, "old shell to end after socket close", func() bool {
		select {
		case <-shell.Done():
			return true
		default:
			return false
		}
	})
	r.awaitState(t, "pane-1", proto.ConnectionConnected)
	fresh := r.awaitRestored(t)
	if fresh == shell {
		t.Fatal("reconnect returned the old shell")
	}
	if got := r.fixture.connections(); got != before+1 {
		t.Fatalf("dials = %d, want %d", got, before+1)
	}
}

func TestAskAndNeverRestAtEndedWithoutRedialAfterSeveralBackoffSteps(t *testing.T) {
	for _, policy := range []string{"ask", "never"} {
		t.Run(policy, func(t *testing.T) {
			r := newReconnectStand(t, policy, "pane-1")
			r.open(t, "pane-1")
			r.awaitState(t, "pane-1", proto.ConnectionConnected)
			before := r.fixture.connections()
			timers := r.clock.retryTimers()
			r.proxy.Cut()
			ended := r.awaitState(t, "pane-1", proto.ConnectionEnded)
			if ended.Transport.Cause != "lost-link" || ended.Reconnect == nil {
				t.Fatalf("ended pane = %+v, want lost-link with an offer", ended)
			}
			if got := r.clock.retryTimers(); got != timers {
				t.Fatalf("%s armed a retry timer after ended", policy)
			}
			for range 4 {
				r.clock.Advance(15 * time.Second)
				if got := r.fixture.connections(); got != before {
					t.Fatalf("%s redialed after ended: dials=%d, want %d", policy, got, before)
				}
			}
		})
	}
}

func TestAutoReconnectDialsOnceForSiblingPanesAndOpensFreshShells(t *testing.T) {
	r := newReconnectStand(t, "auto", "pane-1", "pane-2")
	first, second := r.open(t, "pane-1"), r.open(t, "pane-2")
	r.awaitState(t, "pane-1", proto.ConnectionConnected)
	r.awaitState(t, "pane-2", proto.ConnectionConnected)
	before := r.fixture.connections()
	timerCount := r.clock.retryTimers()
	r.proxy.Cut()
	r.awaitState(t, "pane-1", proto.ConnectionReconnecting)
	waittest.WaitFor(t, "retry timer armed", func() bool { return r.clock.retryTimers() > timerCount })
	r.clock.Advance(15 * time.Second)
	r.awaitState(t, "pane-1", proto.ConnectionConnected)
	r.awaitState(t, "pane-2", proto.ConnectionConnected)
	freshFirst, freshSecond := r.awaitRestored(t), r.awaitRestored(t)
	if freshFirst == first || freshSecond == second {
		t.Fatal("a pane reused its lost shell")
	}
	if got := r.fixture.connections(); got != before+1 {
		t.Fatalf("two panes made %d extra dials, want one", got-before)
	}
}

func TestAutoReconnectRetriesAfterAnUnsuccessfulDial(t *testing.T) {
	r := newReconnectStand(t, "auto", "pane-1")
	r.open(t, "pane-1")
	r.awaitState(t, "pane-1", proto.ConnectionConnected)
	before := r.fixture.connections()
	r.proxy.Cut()
	r.proxy.Refuse()
	r.awaitState(t, "pane-1", proto.ConnectionReconnecting)
	waittest.WaitFor(t, "first retry timer", func() bool { return r.clock.retryTimers() >= 1 })
	r.clock.Advance(time.Second)
	waittest.WaitFor(t, "failed retry increases the attempt", func() bool {
		s := r.latest("pane-1")
		return s.Transport.State == proto.ConnectionReconnecting && s.Transport.Attempt >= 2
	})
	waittest.WaitFor(t, "next retry armed after failure", func() bool { return r.clock.retryTimers() >= 2 })
	r.proxy.Pass()
	r.clock.Advance(2 * time.Second)
	r.awaitState(t, "pane-1", proto.ConnectionConnected)
	r.awaitRestored(t)
	if got := r.fixture.connections(); got != before+1 {
		t.Fatalf("successful redials = %d, want one", got-before)
	}
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
