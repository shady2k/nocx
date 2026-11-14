//go:build nocx_local_ssh

package sshsvc_test

// A pane's connection reaches the coordinator from the moment it opens
// (design §5.3, §5.5, §5.6; nocx-oalo1.5.3).
//
// These are the fault harness's half of the task's DONE WHEN, on the same stand
// as the rest of this package: a REAL helper, a REAL coordinator client over a
// real socket, a REAL sshd, and the switchable proxy in between. Time is the
// injected clock's, so "stalled within thirty seconds" is thirty simulated
// seconds and never a sleep.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshsvc"
	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/sshtest"
	"github.com/shady2k/nocx/internal/waittest"
)

// connLog collects what the helper said about one or more connections, in the
// order it arrived.
type connLog struct {
	mu     sync.Mutex
	events []proto.ConnectionStateEvent
}

func (l *connLog) add(ev proto.ConnectionStateEvent) {
	l.mu.Lock()
	l.events = append(l.events, ev)
	l.mu.Unlock()
}

func (l *connLog) all() []proto.ConnectionStateEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]proto.ConnectionStateEvent(nil), l.events...)
}

func (l *connLog) find(state string) (proto.ConnectionStateEvent, bool) {
	for _, ev := range l.all() {
		if ev.State == state {
			return ev, true
		}
	}
	return proto.ConnectionStateEvent{}, false
}

// collect drains the stand's observations for the rest of the test.
func collect(stand *stand) *connLog {
	log := &connLog{}
	go func() {
		for ev := range stand.connEvents {
			log.add(ev)
		}
	}()
	return log
}

// awaitConnEvent reads the next observation, bounded by the test's own wait and
// not by the simulated clock.
func awaitConnEvent(t *testing.T, stand *stand) proto.ConnectionStateEvent {
	t.Helper()
	select {
	case ev := <-stand.connEvents:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no connection observation arrived")
		return proto.ConnectionStateEvent{}
	}
}

// TestAPanesConnectionIsReportedBeforeItsShellExists is design §5.5 and the
// task's first harness case: a held catalogue dials nothing, the helper names
// the pane's connection, and its first observation is an OPENING stage while
// the shell channel does not exist yet.
func TestAPanesConnectionIsReportedBeforeItsShellExists(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), hostconntest.New())

	destination := destinationAt(t, proxy.Addr())
	ctx := host.WithConnection(context.Background(), stand.helper)

	// The identity the coordinator binds the pane to, asked BEFORE anything
	// dials: the op is a derivation, and the pane needs the binding first.
	connectionID, err := stand.client.ConnectionID(ctx, destination)
	if err != nil {
		t.Fatalf("name the pane's connection: %v", err)
	}
	if connectionID == "" {
		t.Fatal("the connection op answered no id")
	}

	// The spawner's order: subscribe, then open. The subscription is what
	// makes the dial's stages visible.
	stop, err := stand.service.Watch(ctx, destination)
	if err != nil {
		t.Fatalf("watch the pane's connection: %v", err)
	}
	defer stop()

	// Hold the SSH version exchange: the TCP connection is up, the handshake
	// has not started, and no shell can exist.
	proxy.HoldBanner()
	type opened struct {
		shell *sshsvc.ShellChannel
		err   error
	}
	done := make(chan opened, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		shell, oerr := stand.service.OpenShell(ctx, sshsvc.ShellSpec{
			Destination: destination, Cols: 80, Rows: 24,
		})
		done <- opened{shell: shell, err: oerr}
	}()
	<-started

	first := awaitConnEvent(t, stand)
	if first.State != proto.ConnectionOpening {
		t.Fatalf("the first observation is %q, want opening", first.State)
	}
	if first.Stage == "" {
		t.Fatal("the first opening carries no stage")
	}
	if first.ConnectionID != connectionID {
		t.Fatalf("the observation names %q, the op answered %q", first.ConnectionID, connectionID)
	}
	select {
	case got := <-done:
		if got.shell != nil {
			_ = got.shell.Close()
		}
		t.Fatalf("the shell existed before the first observation (open returned %v)", got.err)
	default:
		// The dial is still held: exactly the window the pane exists for.
	}

	// The banner is released and the dial finishes. The pane is told the
	// connection is usable, and only then does the shell exist.
	proxy.Release()
	log := collect(stand)
	waittest.WaitFor(t, "the connected observation", func() bool {
		_, ok := log.find(proto.ConnectionConnected)
		return ok
	})
	var got opened
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the shell never opened after the banner was released")
	}
	if got.err != nil {
		t.Fatalf("open the shell: %v", got.err)
	}
	_ = got.shell.Close()
}

// TestABlackholedConnectionReadsStalledWithinThirtySeconds is Q3's harness
// case: fifteen seconds to the probe's deadline plus the send it waited on, and
// the answer that returns puts it back to connected.
func TestABlackholedConnectionReadsStalledWithinThirtySeconds(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)

	destination := destinationAt(t, proxy.Addr())
	ctx := host.WithConnection(context.Background(), stand.helper)
	stop, err := stand.service.Watch(ctx, destination)
	if err != nil {
		t.Fatalf("watch the pane's connection: %v", err)
	}
	defer stop()
	log := collect(stand)

	shell := stand.openShell(t, destination)
	defer func() { _ = shell.Close() }()
	waittest.WaitFor(t, "the healthy connection", func() bool {
		_, ok := log.find(proto.ConnectionConnected)
		return ok
	})

	// THE FLOW STOPS ANSWERING AND THE SOCKET STAYS OPEN (Q3). The proxy's
	// established-flow silence is the blackhole a person meets: every write
	// succeeds and nothing comes back, while the host still accepts NEW
	// connections — which is what makes the look-only dial evidence and the
	// held reply able to answer when the flow returns. FaultProxy.Blackhole
	// discards bytes outright, so a return could never answer the probe it
	// swallowed; that switch is for a connection that is simply gone.
	proxy.SilenceEstablished()
	stalledAt := clock.Now()
	waittest.WaitForDetail(t, "the stalled observation",
		func() string { return eventsDetail(log.all()) },
		func() bool {
			if _, ok := log.find(proto.ConnectionStalled); ok {
				return true
			}
			// ONE SECOND AT A TIME, so the reading the assertion below takes is
			// the policy's and not the poll's: a fifteen-second leap would
			// charge the test for its own granularity.
			clock.Advance(time.Second)
			return false
		})
	if elapsed := time.Duration(clock.Now() - stalledAt); elapsed > 30*time.Second {
		t.Fatalf("stalled after %v of simulated time, want within 30s", elapsed)
	}
	stalled, _ := log.find(proto.ConnectionStalled)
	if stalled.PlainShells != 1 {
		t.Fatalf("stalled says plainShells=%d, want 1: closing it would end this shell", stalled.PlainShells)
	}

	// And the far side answers again: the reply the silence held is delivered,
	// and the parked probe is the evidence (design §5.3's "the parked probe
	// is what notices the far side the moment it answers").
	proxy.Pass()
	waittest.WaitForDetail(t, "the return to connected",
		func() string { return eventsDetail(log.all()) },
		func() bool {
			events := log.all()
			seenStall := false
			for _, ev := range events {
				if ev.State == proto.ConnectionStalled {
					seenStall = true
				}
				if seenStall && ev.State == proto.ConnectionConnected {
					return true
				}
			}
			return false
		})
}

// TestAHealthyOpenReportsItsStagesThenConnectedAndNothingElse is the ordinary
// case, and the last assertion is the one that keeps it ordinary: no failed, no
// stalled, no reconnecting.
func TestAHealthyOpenReportsItsStagesThenConnectedAndNothingElse(t *testing.T) {
	schema := loadHelperSchema(t, "ssh.connection-state.schema.json")
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)

	destination := destinationAt(t, proxy.Addr())
	ctx := host.WithConnection(context.Background(), stand.helper)
	connectionID, err := stand.client.ConnectionID(ctx, destination)
	if err != nil {
		t.Fatalf("name the connection: %v", err)
	}
	stop, err := stand.service.Watch(ctx, destination)
	if err != nil {
		t.Fatalf("watch the connection: %v", err)
	}
	defer stop()
	log := collect(stand)

	// A LITTLE LATENCY, so a measurement has a value at all: a loopback probe
	// answers in microseconds and the wire's unit is a millisecond, which is
	// the same statement as "nothing measured" (design §5.6's quality field).
	// Ten milliseconds each way is a slow-but-ordinary link.
	proxy.Slow(10 * time.Millisecond)
	shell := stand.openShell(t, destination)
	defer func() { _ = shell.Close() }()
	waittest.WaitFor(t, "the connected observation", func() bool {
		_, ok := log.find(proto.ConnectionConnected)
		return ok
	})

	events := log.all()
	reachedConnected := false
	stages := 0
	var lastSeq uint64
	for i, ev := range events {
		if ev.ConnectionID != connectionID {
			t.Fatalf("event %d names %q, want %q", i, ev.ConnectionID, connectionID)
		}
		if i > 0 && ev.Seq <= lastSeq {
			t.Fatalf("event %d has seq %d after %d", i, ev.Seq, lastSeq)
		}
		lastSeq = ev.Seq
		switch ev.State {
		case proto.ConnectionOpening:
			if reachedConnected {
				t.Fatalf("an opening stage arrived after connected: %+v", ev)
			}
			stages++
		case proto.ConnectionConnected:
			reachedConnected = true
		default:
			t.Fatalf("a healthy open reported %q: %+v", ev.State, ev)
		}
	}
	if stages < 2 {
		t.Fatalf("%d opening stage(s) in %v, want the dial's path", stages, events)
	}
	// THE MEASUREMENT RIDES THE SAME NOTIFICATION (design §5.6's `quality`):
	// one probe interval later a round trip has been taken and is on the wire
	// with its grade. It is a measurement and never a state, so the state stays
	// connected throughout.
	clock.Advance(hostconn.ProbeInterval)
	waittest.WaitForDetail(t, "a measured observation",
		func() string { return eventsDetail(log.all()) },
		func() bool {
			for _, ev := range log.all() {
				if ev.State == proto.ConnectionConnected && ev.RoundTripMs > 0 {
					return true
				}
			}
			clock.Advance(time.Second)
			return false
		})
	connected, _ := log.find(proto.ConnectionConnected)
	raw := mustMarshalEvent(t, connected)
	if err := validateHelperJSON(schema, raw); err != nil {
		t.Fatalf("the connected observation does not satisfy ssh.connection-state: %v\n%s", err, raw)
	}
}

// eventsDetail renders what the helper said, for a wait that failed.
func eventsDetail(events []proto.ConnectionStateEvent) string {
	detail := "events:"
	for _, ev := range events {
		detail += fmt.Sprintf(" %s(%s,%s,seq=%d,ms=%d)", ev.State, ev.Stage, ev.Cause, ev.Seq, ev.StateMs)
	}
	return detail
}

// mustMarshalEvent is the wire's own encoding, so the contract check looks at
// what a coordinator would decode.
func mustMarshalEvent(t *testing.T, ev proto.ConnectionStateEvent) []byte {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal connection event: %v", err)
	}
	return raw
}
