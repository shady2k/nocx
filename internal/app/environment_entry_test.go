package app

import (
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
)

// fakeEnvEntryObserver counts every ObserveEnvironmentEntry call and keeps
// the entry each one named.
type fakeEnvEntryObserver struct {
	n       int
	entries []string
}

func (f *fakeEnvEntryObserver) ObserveEnvironmentEntry(entry string) {
	f.n++
	f.entries = append(f.entries, entry)
}

func (f *fakeEnvEntryObserver) Accept(ingest func() error, _ *lifecycle.Complete) error {
	return ingest()
}

// noopEmitter is the decorator's inner emitter: this file's own criterion is
// what the decorator ADDS, so the real emitter's own behaviour is out of
// scope and stubbed to nothing.
type noopEmitter struct{}

func (noopEmitter) PublishLifecycle(lifecyclepub.Fact) {}

// silentPort is the lifecycle transport's outbound side: what the kernel
// mints (accepts, grants) goes nowhere, because this file's criterion is
// about the emitter, not about what a shell would do with an accept.
type silentPort struct{}

func (silentPort) Send(lifecycle.Envelope) error { return nil }

// TestEnvironmentEntryEmitterFiresOnlyWhenTheStackGrowsPastTheFirstDomain
// pins nocx-2v80t.3.21's decorator against a REAL kernel and publisher — the
// same emitter seam app.go wires lifecyclePub.SetEmitter through — over
// ONE transport for both the parent and the child, which is the shape a
// LOCAL nested shell (sudo, su) has and an ssh child does not (its own
// listener is a transport of its own); the decorator does not care which,
// because it reads the lane's stack, never the transport.
func TestEnvironmentEntryEmitterFiresOnlyWhenTheStackGrowsPastTheFirstDomain(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	registry := newEnvironmentEntryRegistry()
	obs := &fakeEnvEntryObserver{}
	pub.SetEmitter(newEnvironmentEntryEmitter(noopEmitter{}, pub, registry))

	const transportID = lifecycle.TransportID("t-under-test")
	const lane = lifecycle.LaneID("lane-under-test")
	if err := pub.BindTransport(transportID, silentPort{}); err != nil {
		t.Fatalf("bind transport: %v", err)
	}
	h, err := pub.RequestDomain(lane, nil, transportID)
	if err != nil {
		t.Fatalf("request parent domain: %v", err)
	}
	registry.register(t.Context(), lane, obs)

	env := func(seq uint64, dom lifecycle.DomainID, epoch uint64, capability lifecycle.Capability, evt lifecycle.Event) lifecycle.Envelope {
		return lifecycle.Envelope{
			Version: lifecycle.ProtocolVersion, Lane: lane, Domain: dom,
			Epoch: epoch, Sequence: seq, Capability: capability, Event: evt,
		}
	}

	// The parent's own hello: depth 0 -> 1, the lane's first domain
	// integrating — not an entry into anything.
	if err = pub.Ingest(transportID, env(1, h.Domain, h.Epoch, h.Capability,
		lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "/bin/fake"}})); err != nil {
		t.Fatalf("parent hello: %v", err)
	}
	if obs.n != 0 {
		t.Fatalf("the lane's first domain establishing fired %d entries, want none", obs.n)
	}

	// The parent suspends, handing the lane off: no entry yet, because the
	// child has said nothing.
	if err = pub.Ingest(transportID, env(2, h.Domain, h.Epoch, h.Capability,
		lifecycle.Event{Kind: lifecycle.KindDomainSuspended, DomainSuspended: &lifecycle.DomainSuspendedEvent{}})); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if obs.n != 0 {
		t.Fatalf("suspend alone fired %d entries, want none", obs.n)
	}

	// The child domain's own hello: depth 1 -> 2, the entry this decorator
	// exists to raise.
	childH, err := pub.RequestDomain(lane, &h.Domain, transportID)
	if err != nil {
		t.Fatalf("request child domain: %v", err)
	}
	if err = pub.Ingest(transportID, env(1, childH.Domain, childH.Epoch, childH.Capability,
		lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "/bin/fake"}})); err != nil {
		t.Fatalf("child hello: %v", err)
	}
	if obs.n != 1 {
		t.Fatalf("the child's hello fired %d entries, want exactly 1", obs.n)
	}
	// The entry is named by the child domain that took the lane — the
	// identity the helper seals one interval per (nocx-2v80t.3.28).
	if obs.entries[0] != string(childH.Domain) {
		t.Fatalf("the entry was named %q, want the child domain %q", obs.entries[0], childH.Domain)
	}

	// The child closes and the parent reactivates: a SHRINK, never counted.
	if err = pub.Ingest(transportID, env(2, childH.Domain, childH.Epoch, childH.Capability,
		lifecycle.Event{Kind: lifecycle.KindDomainClosed, DomainClosed: &lifecycle.DomainClosedEvent{}})); err != nil {
		t.Fatalf("child close: %v", err)
	}
	if err = pub.Ingest(transportID, env(3, h.Domain, h.Epoch, h.Capability,
		lifecycle.Event{Kind: lifecycle.KindDomainActivated, DomainActivated: &lifecycle.DomainActivatedEvent{}})); err != nil {
		t.Fatalf("parent activate: %v", err)
	}
	if obs.n != 1 {
		t.Fatalf("the parent reactivating fired %d more entries, want still 1", obs.n)
	}
}

// A lane with no registered observer is a safe no-op: a pane this file's
// registry never learned about (a headless coordinator, a test harness)
// must not panic when its stack grows.
func TestEnvironmentEntryEmitterWithNoRegisteredObserverIsANoOp(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	registry := newEnvironmentEntryRegistry()
	pub.SetEmitter(newEnvironmentEntryEmitter(noopEmitter{}, pub, registry))

	const transportID = lifecycle.TransportID("t-under-test")
	const lane = lifecycle.LaneID("lane-under-test")
	if err := pub.BindTransport(transportID, silentPort{}); err != nil {
		t.Fatalf("bind transport: %v", err)
	}
	h, err := pub.RequestDomain(lane, nil, transportID)
	if err != nil {
		t.Fatalf("request parent domain: %v", err)
	}
	// Deliberately no registry.register call.

	env := func(seq uint64, dom lifecycle.DomainID, epoch uint64, capability lifecycle.Capability, evt lifecycle.Event) lifecycle.Envelope {
		return lifecycle.Envelope{
			Version: lifecycle.ProtocolVersion, Lane: lane, Domain: dom,
			Epoch: epoch, Sequence: seq, Capability: capability, Event: evt,
		}
	}
	if err = pub.Ingest(transportID, env(1, h.Domain, h.Epoch, h.Capability,
		lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "/bin/fake"}})); err != nil {
		t.Fatalf("parent hello: %v", err)
	}
	if err = pub.Ingest(transportID, env(2, h.Domain, h.Epoch, h.Capability,
		lifecycle.Event{Kind: lifecycle.KindDomainSuspended, DomainSuspended: &lifecycle.DomainSuspendedEvent{}})); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	childH, err := pub.RequestDomain(lane, &h.Domain, transportID)
	if err != nil {
		t.Fatalf("request child domain: %v", err)
	}
	if err = pub.Ingest(transportID, env(1, childH.Domain, childH.Epoch, childH.Capability,
		lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "/bin/fake"}})); err != nil {
		t.Fatalf("child hello: %v", err)
	}
}
