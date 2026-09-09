package lifecyclepub_test

import (
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclepub"
)

func requestEvt(rid lifecycle.RequestID, env, host, user string, port int) lifecycle.Event {
	return lifecycle.Event{Kind: lifecycle.KindDomainRequest, DomainRequest: &lifecycle.DomainRequest{
		RequestID: rid, Env: env, Host: host, User: user, Port: port,
	}}
}

// grantFrom returns the first domain_grant the port received, failing the
// test when none arrived.
func grantFrom(t *testing.T, port *recordingPort) lifecycle.Envelope {
	t.Helper()
	for i := range port.sent {
		if port.sent[i].Event.Kind == lifecycle.KindDomainGrant {
			return port.sent[i]
		}
	}
	t.Fatalf("no grant delivered to the port; sent kinds=%v", port.kinds())
	return lifecycle.Envelope{}
}

// TestPublisherGrantEnrichedAndDelivered is the composition seam the shell
// depends on: a validated domain_request produces exactly one grant on the
// parent's port, addressed to the parent, carrying the request echo, the
// child's identity (minted by the builder through the kernel — the kernel
// stays the sole minter) and the opaque bootstrap. The child is a real
// Pending domain under the parent.
func TestPublisherGrantEnrichedAndDelivered(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	var pub *lifecyclepub.Publisher
	pub = lifecyclepub.New(k, lifecyclepub.WithGrantBuilder(func(req lifecyclepub.GrantRequest) (lifecyclepub.GrantBootstrap, error) {
		h, err := pub.RequestDomain(req.Lane, &req.Parent, "T")
		if err != nil {
			return lifecyclepub.GrantBootstrap{}, err
		}
		return lifecyclepub.GrantBootstrap{Domain: h.Domain, Epoch: h.Epoch, Bootstrap: "opaque-sudo-launch"}, nil
	}))
	r := &recorder{}
	pub.SetEmitter(r)
	port := &recordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	mustAckEstablishment(t, pub, r, "L", h)

	mustIngest(t, pub, "T", env("L", h, 2, requestEvt("r-dom-1-0", lifecycle.EnvSudo, "", "", 0)))

	grant := grantFrom(t, port)
	// The grant addresses the PARENT: the adapter routes it to the parent's
	// connection by this tuple.
	if grant.Domain != h.Domain || grant.Epoch != h.Epoch {
		t.Fatalf("grant must be addressed to the parent, got dom=%s epoch=%d", grant.Domain, grant.Epoch)
	}
	// By the tuple, and by nothing secret (nocx-aqz7o).
	if grant.Capability != (lifecycle.Capability{}) {
		t.Fatal("the grant carries the parent's capability back down the inherited descriptor")
	}
	g := grant.Event.DomainGrant
	if g == nil {
		t.Fatal("grant payload missing")
	}
	if g.RequestID != "r-dom-1-0" || g.Env != lifecycle.EnvSudo || g.Bootstrap != "opaque-sudo-launch" {
		t.Fatalf("grant payload wrong: %+v", g)
	}
	// The child exists, minted under the parent on the same transport.
	child, ok := k.Domain(g.Domain)
	if !ok {
		t.Fatalf("child domain %s not minted", g.Domain)
	}
	if child.Parent == nil || *child.Parent != h.Domain {
		t.Fatalf("child must carry the parent, got %+v", child.Parent)
	}
	if child.Epoch != g.Epoch || child.State != lifecycle.DomainPending {
		t.Fatalf("child must be Pending at its minted epoch, got state=%v epoch=%d", child.State, child.Epoch)
	}
}

// TestPublisherGrantRefusedDeliversEmptyBootstrap: a builder failure is the
// honest refusal — the parent receives the grant echo with an empty
// bootstrap and runs its command conventionally; nothing is minted, and the
// pump does not panic.
func TestPublisherGrantRefusedDeliversEmptyBootstrap(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k, lifecyclepub.WithGrantBuilder(func(req lifecyclepub.GrantRequest) (lifecyclepub.GrantBootstrap, error) {
		return lifecyclepub.GrantBootstrap{}, lifecycle.ErrBadRequest
	}))
	r := &recorder{}
	pub.SetEmitter(r)
	port := &recordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	mustAckEstablishment(t, pub, r, "L", h)

	mustIngest(t, pub, "T", env("L", h, 2, requestEvt("r-dom-1-0", lifecycle.EnvSSH, "box", "", 22)))

	grant := grantFrom(t, port)
	g := grant.Event.DomainGrant
	if g.Bootstrap != "" || g.Domain != "" || g.Epoch != 0 {
		t.Fatalf("refused grant must be the empty-bootstrap echo, got %+v", g)
	}
	if g.RequestID != "r-dom-1-0" || g.Env != lifecycle.EnvSSH || g.Host != "box" {
		t.Fatalf("refused grant must echo the request, got %+v", g)
	}
}

// TestPublisherGrantWithoutBuilderDeliversEcho: no builder wired (tests, or
// a server without the composition seam) answers every request with the
// empty-bootstrap refusal — the parent stays conventional, never hung.
func TestPublisherGrantWithoutBuilderDeliversEcho(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	pub := lifecyclepub.New(k)
	r := &recorder{}
	pub.SetEmitter(r)
	port := &recordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	h, err := pub.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	mustIngest(t, pub, "T", env("L", h, 1, helloEvt()))
	mustAckEstablishment(t, pub, r, "L", h)

	mustIngest(t, pub, "T", env("L", h, 2, requestEvt("r-dom-1-0", lifecycle.EnvSu, "", "", 0)))

	grant := grantFrom(t, port)
	g := grant.Event.DomainGrant
	if g.Bootstrap != "" || g.RequestID != "r-dom-1-0" {
		t.Fatalf("no-builder grant must be the empty-bootstrap echo, got %+v", g)
	}
}

// TestPublisherPublishesTheChildsDestination is nocx-ax79: inside a nested
// ssh, nothing on screen says which machine the next command will run on.
// The cwd chip reads home/pi, which is indistinguishable from a local
// /home/pi, and the answer lives only in the user's memory.
//
// The information exists and is authenticated — domain_request carries the
// destination and nothing else (ADR-0025) — but it stopped at the grant
// seam, so the renderer had no source for it and showed none (see the note
// on `host` in frontend/src/lifecycle/domain-environment.ts). Publishing it
// is inside decision 7, not against it: what may never cross are the
// capability and raw frames; a schema-checked fact is the whole point of the
// boundary. It carries no authority — the domain id and epoch remain the
// only authority the renderer gets.
func TestPublisherPublishesTheChildsDestination(t *testing.T) {
	k := lifecycle.New(lifecycle.Options{})
	var pub *lifecyclepub.Publisher
	var child lifecycle.DomainHandle
	pub = lifecyclepub.New(k, lifecyclepub.WithGrantBuilder(func(req lifecyclepub.GrantRequest) (lifecyclepub.GrantBootstrap, error) {
		h, err := pub.RequestDomain(req.Lane, &req.Parent, "T")
		if err != nil {
			return lifecyclepub.GrantBootstrap{}, err
		}
		child = h
		return lifecyclepub.GrantBootstrap{Domain: h.Domain, Epoch: h.Epoch, Bootstrap: "opaque-ssh-launch"}, nil
	}))
	r := &recorder{}
	pub.SetEmitter(r)
	port := &recordingPort{}
	if err := pub.BindTransport("T", port); err != nil {
		t.Fatal(err)
	}
	parent, err := pub.RequestDomain("L", nil, "T")
	if err != nil {
		t.Fatal(err)
	}
	mustIngest(t, pub, "T", env("L", parent, 1, helloEvt()))
	mustAckEstablishment(t, pub, r, "L", parent)

	// The parent asks for a child at pi@192.168.0.93, suspends, and the far
	// shell establishes the child.
	mustIngest(t, pub, "T", env("L", parent, 2, requestEvt("r-dom-1-0", lifecycle.EnvSSH, "192.168.0.93", "pi", 22)))
	mustIngest(t, pub, "T", env("L", parent, 3, lifecycle.Event{
		Kind: lifecycle.KindDomainSuspended, DomainSuspended: &lifecycle.DomainSuspendedEvent{},
	}))
	mustIngest(t, pub, "T", env("L", child, 1, helloEvt()))
	mustAckEstablishment(t, pub, r, "L", child)

	// Every fact naming the child names where the child IS.
	var named int
	for _, f := range r.all() {
		if f.Domain != string(child.Domain) {
			continue
		}
		named++
		if f.Destination == nil {
			t.Fatalf("a fact naming the child carries no destination: %+v", f)
		}
		if f.Destination.Host != "192.168.0.93" || f.Destination.User != "pi" || f.Destination.Port != 22 {
			t.Fatalf("destination = %+v, want pi@192.168.0.93:22", f.Destination)
		}
	}
	if named == 0 {
		t.Fatalf("no fact ever named the child; facts=%+v", r.all())
	}

	// And the parent's own facts carry none: it is the local machine, and a
	// destination on it would be exactly the lie this bead is about.
	for _, f := range r.all() {
		if f.Domain == string(parent.Domain) && f.Destination != nil {
			t.Fatalf("the local parent must carry no destination, got %+v", f.Destination)
		}
	}
}
