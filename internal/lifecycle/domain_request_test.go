package lifecycle

import (
	"errors"
	"testing"
)

// requestEvt builds a domain_request event.
func requestEvt(rid RequestID, env, host, user string, port int) Event {
	return Event{Kind: KindDomainRequest, DomainRequest: &DomainRequest{
		RequestID: rid, Env: env, Host: host, User: user, Port: port,
	}}
}

// TestDomainRequestProducesGrantEcho is the wire shape the adapters and the
// shell depend on: a validated domain_request produces exactly one outbound
// grant, addressed to the PARENT (the frame the parent's connection routes
// by), echoing the request id and the environment context, and mutates
// nothing — the parent stays active until it suspends itself.
func TestDomainRequestProducesGrantEcho(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	hA := establish(t, k, "T", tp, L, nil)

	outs, err := k.Ingest("T", env(L, hA, 2, requestEvt("r-dom-0", EnvSSH, "box.example.com", "alice", 2222)))
	if err != nil {
		t.Fatalf("domain_request: %v", err)
	}
	if len(outs) != 1 {
		t.Fatalf("expected exactly one outbound (the grant), got %d", len(outs))
	}
	g := outs[0]
	if g.Envelope.Event.Kind != KindDomainGrant || g.Envelope.Event.DomainGrant == nil {
		t.Fatalf("outbound must be a domain_grant, got %+v", g.Envelope.Event)
	}
	// The grant addresses the parent: the adapter routes it to the parent's
	// connection by this tuple.
	if g.Envelope.Domain != hA.Domain || g.Envelope.Epoch != hA.Epoch {
		t.Fatalf("grant must be addressed to the parent, got lane=%s dom=%s epoch=%d", g.Envelope.Lane, g.Envelope.Domain, g.Envelope.Epoch)
	}
	// By the tuple alone: the grant carries no bearer of its own back down
	// the inherited descriptor (nocx-aqz7o).
	if g.Envelope.Capability != (Capability{}) {
		t.Fatalf("grant carries the parent's capability back to the shell")
	}
	grant := g.Envelope.Event.DomainGrant
	if grant.RequestID != "r-dom-0" || grant.Env != EnvSSH || grant.Host != "box.example.com" ||
		grant.User != "alice" || grant.Port != 2222 {
		t.Fatalf("grant must echo the request context, got %+v", grant)
	}
	// The answer fields are the publisher seam's to fill: empty here.
	if grant.Domain != "" || grant.Epoch != 0 || grant.Bootstrap != "" {
		t.Fatalf("kernel grant must carry no answer yet, got %+v", grant)
	}
	// Nothing moved: the parent is still active and on top, ready to receive
	// the grant and then suspend itself (protocol doc §9).
	st := mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hA.Domain, "", []DomainID{hA.Domain})
	if dA, _ := k.Domain(hA.Domain); dA.State != DomainEstablished {
		t.Fatalf("parent must stay established after the request, got %v", dA.State)
	}
}

// TestDomainRequestValidation rejects malformed requests without moving any
// state, mirroring the shell-side fail-open: a request that cannot be
// answered is refused outright, never answered with a half-built bootstrap.
func TestDomainRequestValidation(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	hA := establish(t, k, "T", tp, L, nil)

	cases := []struct {
		name string
		evt  Event
		want error
	}{
		{"unknown env", requestEvt("r-0", "docker", "", "", 0), ErrBadRequest},
		{"ssh without host", requestEvt("r-1", EnvSSH, "", "", 0), ErrBadRequest},
		{"port out of range", requestEvt("r-2", EnvSSH, "h", "", 70000), ErrBadRequest},
		{"missing request id", requestEvt("", EnvSudo, "", "", 0), ErrRequestIDShape},
		{"request id with quotes", requestEvt(`r-"x"`, EnvSudo, "", "", 0), ErrRequestIDShape},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := mustState(t, k, L)
			if _, err := k.Ingest("T", env(L, hA, 2, tc.evt)); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if after := mustState(t, k, L); !statesEqual(before, after) {
				t.Fatalf("rejected request mutated the lane: %+v -> %+v", before, after)
			}
		})
	}
}

// TestDomainRequestFromSuspendedDomainRejected: only the ACTIVE domain may
// request a child — a suspended parent has already yielded the lane, and a
// child minted over it would race the activation that restores it.
func TestDomainRequestFromSuspendedDomainRejected(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	hA := establish(t, k, "T", tp, L, nil)
	mustIngest(t, k, "T", env(L, hA, 2, suspendEvt()))
	hB := establish(t, k, "T", tp, L, &hA.Domain) // child active
	if _, err := k.Ingest("T", env(L, hA, 3, requestEvt("r-0", EnvSudo, "", "", 0))); !errors.Is(err, ErrDomainInactive) {
		t.Fatalf("request from a suspended parent must be rejected, got %v", err)
	}
	if _, err := k.Ingest("T", env(L, hB, 2, requestEvt("r-1", EnvSudo, "", "", 0))); err != nil {
		t.Fatalf("request from the active child must be accepted, got %v", err)
	}
}

// TestDomainRequestGrantThenStillbornChild is the failure interval the brief
// names as the one most likely to bite: the parent requests a child, the
// grant is delivered, the parent suspends and execs the child — but the
// child never establishes (sudo policy refused, no forwarding, a hostile
// shell). The parent must still be able to activate at its next prompt, and
// a late frame from the stillborn child must be rejected against the
// restored parent.
func TestDomainRequestGrantThenStillbornChild(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	hA := establish(t, k, "T", tp, L, nil)

	// The parent requests and receives the grant (the publisher seam mints;
	// the kernel test mints directly, exactly as the seam would).
	outs, err := k.Ingest("T", env(L, hA, 2, requestEvt("r-0", EnvSudo, "", "", 0)))
	if err != nil {
		t.Fatalf("domain_request: %v", err)
	}
	mustDeliver(t, k, outs)
	mustIngest(t, k, "T", env(L, hA, 3, suspendEvt()))
	// The child is minted but never hellos: Pending, never pushed.
	hB, err := k.RequestDomain(L, &hA.Domain, "T")
	if err != nil {
		t.Fatalf("RequestDomain child: %v", err)
	}
	st := mustState(t, k, L)
	assertState(t, st, LifecycleNative, "", "", []DomainID{hA.Domain})

	// The child command failed (or never ran). The parent resumes at its
	// next prompt boundary and activates: the stillborn child is not on the
	// stack, so the activation is legal and restores the parent.
	mustIngest(t, k, "T", env(L, hA, 4, activateEvt()))
	st = mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hA.Domain, "", []DomainID{hA.Domain})

	// A late hello from the stillborn child is rejected against the active
	// parent: it can neither establish over an active parent nor preempt it.
	if _, err := k.Ingest("T", env(L, hB, 1, helloEvt("bash"))); !errors.Is(err, ErrParentActive) {
		t.Fatalf("late stillborn hello must be rejected, got %v", err)
	}
	st = mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hA.Domain, "", []DomainID{hA.Domain})
}

// TestDomainRequestFullChildLifecycle is the happy interval with both ends
// named: the parent owns the stream through the grant, then suspends; the
// child owns it from its hello until its close; only the parent's
// authenticated activation restores it, and stale child events after the
// restoration are rejected.
func TestDomainRequestFullChildLifecycle(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	hA := establish(t, k, "T", tp, L, nil)

	outs, err := k.Ingest("T", env(L, hA, 2, requestEvt("r-0", EnvSu, "", "", 0)))
	if err != nil {
		t.Fatalf("domain_request: %v", err)
	}
	mustDeliver(t, k, outs)
	mustIngest(t, k, "T", env(L, hA, 3, suspendEvt()))
	hB, err := k.RequestDomain(L, &hA.Domain, "T")
	if err != nil {
		t.Fatalf("RequestDomain child: %v", err)
	}
	mustIngest(t, k, "T", env(L, hB, 1, helloEvt("bash")))
	st := mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hB.Domain, "", []DomainID{hA.Domain, hB.Domain})

	// The child closes; the parent is NOT restored by the close alone.
	mustIngest(t, k, "T", env(L, hB, 2, closeEvt()))
	st = mustState(t, k, L)
	assertState(t, st, LifecycleNative, "", "", []DomainID{hA.Domain})

	// Only an authenticated activation restores the parent.
	mustIngest(t, k, "T", env(L, hA, 4, activateEvt()))
	st = mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hA.Domain, "", []DomainID{hA.Domain})

	// Stale child events arriving after the restoration are rejected.
	for _, evt := range []Event{startEvt(nil, "whoami"), promptReadyEvt(), closeEvt()} {
		if _, ingestErr := k.Ingest("T", env(L, hB, 3, evt)); !errors.Is(ingestErr, ErrDomainNotLive) {
			t.Fatalf("stale child event must be rejected, got %v", ingestErr)
		}
	}
	st = mustState(t, k, L)
	assertState(t, st, LifecyclePromptReady, hA.Domain, "", []DomainID{hA.Domain})
}

// TestDomainGrantNotInbound: the grant is kernel-originated; a shell sending
// one is a protocol violation rejected before any state is consulted.
func TestDomainGrantNotInbound(t *testing.T) {
	k, _, _ := newTestKernel()
	tp := &fakePort{}
	if err := k.BindTransport("T", tp); err != nil {
		t.Fatal(err)
	}
	const L = LaneID("L")
	hA := establish(t, k, "T", tp, L, nil)
	evt := Event{Kind: KindDomainGrant, DomainGrant: &DomainGrant{RequestID: "r-0"}}
	if _, err := k.Ingest("T", env(L, hA, 2, evt)); !errors.Is(err, ErrIllegalEvent) {
		t.Fatalf("inbound grant must be rejected as illegal, got %v", err)
	}
}
