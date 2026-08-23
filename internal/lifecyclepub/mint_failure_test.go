package lifecyclepub

import (
	"errors"
	"io"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
)

// failingReader is a randomness source that cannot deliver.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy on this machine") }

var _ io.Reader = failingReader{}

type mintTestPort struct{}

func (mintTestPort) Send(lifecycle.Envelope) error { return nil }

// helloForMint is the shell's hello, the event that makes the kernel emit an
// accept — which is what begins an establishment.
func helloForMint() lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindHello, Hello: &lifecycle.Hello{Shell: "bash"}}
}

func newPublisherForMintTest(t *testing.T) (*Publisher, lifecycle.DomainHandle) {
	t.Helper()
	p := New(lifecycle.New(lifecycle.Options{}))
	if err := p.BindTransport("T", mintTestPort{}); err != nil {
		t.Fatalf("BindTransport: %v", err)
	}
	h, err := p.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatalf("RequestDomain: %v", err)
	}
	return p, h
}

func mintHelloEnvelope(h lifecycle.DomainHandle) lifecycle.Envelope {
	return lifecycle.Envelope{
		Version:    lifecycle.ProtocolVersion,
		Lane:       "L",
		Domain:     h.Domain,
		Epoch:      h.Epoch,
		Capability: h.Capability,
		Sequence:   1,
		Event:      helloForMint(),
	}
}

// An establishment whose generation cannot be minted is not begun (nocx-s16k8).
//
// The generation is not an authenticator, which is why the read was tolerated
// (`_, _ = rand.Read(b)`). It is a discriminator against a stale actor — the
// value that stops a late acknowledgement from a previous episode releasing
// the accept of a newer one — and it is compared with `!=`. With every
// generation the same zero string, the one thing it exists to reject is the
// one thing that gets through, and a superseded timer cancels a live
// establishment. It is echoed by the far side too, so it must be unguessable
// as well as distinct.
//
// Fail closed: no generation, no establishment recorded, and Ingest says so.
func TestIngest_AFailedRandomReadBeginsNoEstablishment(t *testing.T) {
	p, h := newPublisherForMintTest(t)

	prev := randReader
	randReader = failingReader{}
	defer func() { randReader = prev }()

	if err := p.Ingest("T", mintHelloEnvelope(h)); err == nil {
		t.Fatal("Ingest accepted a hello whose generation could not be minted; want an error")
	}
	p.mu.Lock()
	pending, gens := len(p.pending), len(p.gen)
	p.mu.Unlock()
	if pending != 0 {
		t.Errorf("%d pending establishment(s) recorded for a generation that was never minted", pending)
	}
	if gens != 0 {
		t.Errorf("%d generation(s) recorded", gens)
	}
}

// And the paired success AGENTS.md asks for next to every failure test: on an
// ordinary machine the establishment begins and its generation is not empty.
func TestIngest_OnAnOrdinaryMachineTheEstablishmentBegins(t *testing.T) {
	p, h := newPublisherForMintTest(t)

	if err := p.Ingest("T", mintHelloEnvelope(h)); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pending) != 1 {
		t.Fatalf("%d pending establishments, want 1", len(p.pending))
	}
	for _, pend := range p.pending {
		if pend.gen == "est-" || pend.gen == "" {
			t.Errorf("the generation carries no random part: %q", pend.gen)
		}
	}
}
