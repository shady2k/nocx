//go:build nocx_local_ssh

package sshsvc_test

// THE CONNECTION TABLE, AND THE LINK EACH OBSERVATION BELONGS TO
// (nocx-oalo1.6.2; design §5.10's seventh scenario).
//
// A coordinator that restarts rebuilds what it keeps per connection — the far
// helper's entry, the host's mode, the person's consent — from the helper's
// connection table, and it has to include the connections NO PANE rides: a
// tunnel, a probe lease, the far helper's own lane. Until the `connections` op
// existed the helper told a coordinator only about the connections its own pans
// had asked for, so a zero-pane connection was invisible and its entry was
// never rebuilt.
//
// Both halves are asserted here, on the same stand as the rest of the package: a
// REAL helper, a REAL coordinator client over a real socket, a REAL sshd and the
// switchable proxy in between.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/hostconn/hostconntest"
	"github.com/shady2k/nocx/internal/sshtest"
	"github.com/shady2k/nocx/internal/waittest"
)

// TestTheConnectionsAnswerSatisfiesItsContract is the third check
// contracts/README.md names: the REAL result, off the REAL socket, against the
// frozen schema. A payload the test built would prove the struct is well formed
// and nothing about what the helper sends.
func TestTheConnectionsAnswerSatisfiesItsContract(t *testing.T) {
	schema := loadHelperSchema(t, "ssh.connections.schema.json")
	paramsSchema := loadHelperSchema(t, "ssh.connections.params.schema.json")
	if err := validateHelperJSON(paramsSchema, []byte(`{}`)); err != nil {
		t.Fatalf("the op's own empty params do not satisfy its contract: %v", err)
	}
	if err := validateHelperJSON(paramsSchema, []byte(`{"destination":{}}`)); err == nil {
		t.Fatal("the params contract accepted a field: the shape is empty and must stay empty")
	}

	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	stand := newStand(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})
	destination := destinationAt(t, proxy.Addr())
	ctx := host.WithConnection(context.Background(), stand.helper)
	stand.openShell(t, destination)

	waittest.WaitFor(t, "the connection to be reported", func() bool {
		var result proto.ConnectionsResult
		if err := stand.client.Call(ctx, proto.ServiceSSH, proto.OpConnections, proto.ConnectionsParams{}, &result); err != nil {
			return false
		}
		return len(result.Connections) == 1
	})
	var result proto.ConnectionsResult
	if err := stand.client.Call(ctx, proto.ServiceSSH, proto.OpConnections, proto.ConnectionsParams{}, &result); err != nil {
		t.Fatalf("call connections: %v", err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal the answer: %v", err)
	}
	if err := validateHelperJSON(schema, raw); err != nil {
		t.Fatalf("the helper's connections answer does not satisfy ssh.connections: %v\n%s", err, raw)
	}
}

// hasEvent reports whether any collected observation matches.
func hasEvent(events []proto.ConnectionStateEvent, match func(proto.ConnectionStateEvent) bool) bool {
	for _, ev := range events {
		if match(ev) {
			return true
		}
	}
	return false
}

// TestAConnectionsEntryCarriesItsDestinationAndItsHostKey is the shape a
// coordinator cannot rebuild an entry without: the connection id is a digest,
// so it names a connection but cannot be dialled again, and the host key is what
// the host's mode and the person's consent are stored under.
func TestAConnectionsEntryCarriesItsDestinationAndItsHostKey(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	stand := newStand(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})
	destination := destinationAt(t, proxy.Addr())
	ctx := host.WithConnection(context.Background(), stand.helper)

	connectionID, err := stand.client.ConnectionID(ctx, destination)
	if err != nil {
		t.Fatalf("name the connection: %v", err)
	}
	stand.openShell(t, destination)
	waittest.WaitFor(t, "the connection to be usable", func() bool {
		summaries, cerr := stand.client.Connections(ctx)
		if cerr != nil {
			return false
		}
		for _, summary := range summaries {
			if summary.Connection.ConnectionID == connectionID && summary.HostKeyFingerprint != "" {
				return true
			}
		}
		return false
	})

	summaries, err := stand.client.Connections(ctx)
	if err != nil {
		t.Fatalf("ask for the connection table: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("the table holds %d connection(s), want 1: %+v", len(summaries), summaries)
	}
	entry := summaries[0]
	if entry.Connection.ConnectionID != connectionID {
		t.Fatalf("the entry names %q, want %q", entry.Connection.ConnectionID, connectionID)
	}
	if entry.Connection.State != proto.ConnectionConnected {
		t.Fatalf("the entry reads %q, want connected", entry.Connection.State)
	}
	if entry.Connection.Generation == 0 {
		t.Fatal("the entry names no link generation: a coordinator could not tell a replaced link from a fresh one")
	}
	if entry.Destination == nil {
		t.Fatal("the entry carries no destination: a digest cannot be dialled and this is all a restart redials from")
	}
	if entry.Destination.Host != destination.Host || entry.Destination.Port != destination.Port || entry.Destination.User != destination.User {
		t.Fatalf("the entry's destination is %+v, want the one the connection was acquired for", *entry.Destination)
	}
	if entry.Destination.Identity.Auth != proto.SSHAuthPassword {
		t.Fatalf("the entry's destination authenticates as %q, want the credential it was acquired with", entry.Destination.Identity.Auth)
	}
	if entry.HostKeyFingerprint != f.hostKeyFingerprint() {
		t.Fatalf("the entry's host key is %q, want the key this connection was verified against (%q)",
			entry.HostKeyFingerprint, f.hostKeyFingerprint())
	}
}

// TestARestartedCoordinatorSeesAConnectionNoPaneRides is design §5.10's seventh
// scenario through the shipped op: the helper keeps the supervisor, a second
// coordinator connection asks what this helper holds, and a connection with no
// pane on it is in the answer — and its observations follow, without anything
// having named its destination on the new connection.
func TestARestartedCoordinatorSeesAConnectionNoPaneRides(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	d := newDaemon(t, discardLogger(), clock)

	first := d.connect(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})
	restarted := d.connect(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	})

	destination := destinationAt(t, proxy.Addr())
	ctx := host.WithConnection(context.Background(), first.helper)
	connectionID, err := first.client.ConnectionID(ctx, destination)
	if err != nil {
		t.Fatalf("name the connection: %v", err)
	}

	// A CONNECTION WITH NO PANE: the far-side listeners a pane's lifecycle loop
	// rides. It is the shape a tunnel has — a pooled reference and no shell —
	// and it is the shape the old `connection`-only subscription could never
	// show a restarted coordinator.
	listeners, err := first.paneAt(t, destination)
	if err != nil {
		t.Fatalf("open a connection with no pane: %v", err)
	}
	t.Cleanup(func() { _ = listeners.Close() })

	// THE FIRST COORDINATOR GOES. The helper keeps the supervisor and the
	// forward, which is the state the scenario is about.
	first.stop()

	restartedCtx := host.WithConnection(context.Background(), restarted.helper)
	summaries, err := restarted.client.Connections(restartedCtx)
	if err != nil {
		t.Fatalf("the restarted coordinator asks for the table: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("the restart sees %d connection(s), want the one with no pane: %+v", len(summaries), summaries)
	}
	if got := summaries[0].Connection.ConnectionID; got != connectionID {
		t.Fatalf("the restart sees connection %q, want %q", got, connectionID)
	}
	if got := summaries[0].Connection.State; got != proto.ConnectionConnected {
		t.Fatalf("the connection with no pane reads %q after the restart, want connected", got)
	}

	// AND IT IS KEPT CURRENT: the answer is also a subscription, so a state the
	// restarted coordinator never asked about by destination arrives anyway.
	events := collect(restarted)
	proxy.SilenceEstablished()
	clock.Advance(30 * time.Second)
	waittest.WaitForDetail(t, "the restarted coordinator to be told the connection stalled",
		func() string { return eventsDetail(events.all()) },
		func() bool {
			clock.Advance(time.Second)
			for _, ev := range events.all() {
				if ev.ConnectionID == connectionID && ev.State == proto.ConnectionStalled {
					return true
				}
			}
			return false
		})
}

// TestTheGenerationRidesTheEventAndMovesWithTheLinkNotTheStage is the pair the
// number exists to separate, over the wire: a connection that stalled and
// answered again is still the link the coordinator already knew, and a
// connection that was closed and redialled is not.
func TestTheGenerationRidesTheEventAndMovesWithTheLinkNotTheStage(t *testing.T) {
	const password = "correct horse battery staple"
	f := newFixture(t, password, newSigner(t))
	proxy := sshtest.NewFaultProxy(t, f.addr)
	clock := hostconntest.New()
	stand := newStandOnClock(t, &coordinator{
		password: password, verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint(),
	}, discardLogger(), clock)
	destination := destinationAt(t, proxy.Addr())
	ctx := host.WithConnection(context.Background(), stand.helper)

	if _, err := stand.client.ConnectionID(ctx, destination); err != nil {
		t.Fatalf("name the connection: %v", err)
	}
	events := collect(stand)

	// A plain shell keeps the connection open through the silence, so the stall
	// and its recovery really are one transport.
	holder, err := stand.carrier.Acquire(ctx, supervisedSpec(t, f, destination, true))
	if err != nil {
		t.Fatalf("hold the connection: %v", err)
	}
	t.Cleanup(holder.Release)
	waittest.WaitFor(t, "the connected observation", func() bool {
		_, ok := events.find(proto.ConnectionConnected)
		return ok
	})
	connected, _ := events.find(proto.ConnectionConnected)
	first := connected.Generation
	if first == 0 {
		t.Fatal("a connected observation carries generation 0")
	}

	proxy.SilenceEstablished()
	clock.Advance(30 * time.Second)
	waittest.WaitForDetail(t, "the stalled observation",
		func() string { return eventsDetail(events.all()) },
		func() bool {
			_, ok := events.find(proto.ConnectionStalled)
			return ok
		})
	stalled, _ := events.find(proto.ConnectionStalled)
	proxy.Pass()
	waittest.WaitForDetail(t, "the return to connected",
		func() string { return eventsDetail(events.all()) },
		func() bool {
			return hasEvent(events.all(), func(ev proto.ConnectionStateEvent) bool {
				return ev.State == proto.ConnectionConnected && ev.Seq > stalled.Seq
			})
		})
	// NOTHING MOVED: a stall and its recovery are one link.
	for _, ev := range events.all() {
		if ev.Generation == 0 {
			continue
		}
		if ev.Generation != first {
			t.Fatalf("a stall and its recovery moved the generation (%d, want %d): they are one link", ev.Generation, first)
		}
	}

	// AND A NEW LINK: the transport is closed under its holder — the
	// reconnect's first move — and the shell's next acquire dials afresh.
	if _, err = stand.client.Disconnect(ctx, destination); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	waittest.WaitFor(t, "the reconnecting observation", func() bool {
		return hasEvent(events.all(), func(ev proto.ConnectionStateEvent) bool {
			return ev.State == proto.ConnectionReconnecting
		})
	})
	next, err := stand.carrier.Acquire(ctx, supervisedSpec(t, f, destination, true))
	if err != nil {
		t.Fatalf("Acquire after the disconnect: %v", err)
	}
	t.Cleanup(next.Release)
	waittest.WaitForDetail(t, "the second link's connected observation",
		func() string { return eventsDetail(events.all()) },
		func() bool {
			for _, ev := range events.all() {
				if ev.State == proto.ConnectionConnected && ev.Generation > first {
					return true
				}
			}
			return false
		})
	previous := first
	for _, ev := range events.all() {
		if ev.Generation == 0 {
			// Observations taken before this connection had a link at all; zero
			// says exactly that and is not a link's identity.
			continue
		}
		if ev.Generation < previous {
			t.Fatalf("generation went backwards on the wire: %d after %d", ev.Generation, previous)
		}
		previous = ev.Generation
	}
}
