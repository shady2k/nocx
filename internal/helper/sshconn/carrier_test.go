//go:build nocx_local_ssh

package sshconn

// The carrier's translation layer, asserted where it can be asserted without a
// server: which of the library's errors means "a person must decide", which
// means "no retry can help", and which means "the transport is gone and the
// next attempt may work".
//
// This is the table nocx-oalo1.4.2's DONE WHEN names — a refused sign-in and a
// changed host key read failed with no retry, a transport error reads
// reconnecting — and the reason it is a table rather than four assertions at
// four call sites: the supervisor never sees an x/crypto type, so this is the
// only place the mapping exists.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/ssh"
)

func TestDialFailureMapsTheLibrarysErrors(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		event hostconn.Event
	}{
		{
			name:  "an unknown host key is a question for a person",
			err:   &ssh.ErrUnknownHostKey{Addr: "host:22", Fingerprint: "SHA256:x"},
			event: hostconn.Event{Kind: hostconn.NeedsPerson, Wait: hostconn.WaitHostKey},
		},
		{
			name:  "a changed host key is a failure no retry may touch",
			err:   &ssh.ErrHostKeyMismatch{Addr: "host:22", Fingerprint: "SHA256:x"},
			event: hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseHostKeyChanged},
		},
		{
			name:  "a refused sign-in is a failure no retry may touch",
			err:   &ssh.ErrAuthFailed{User: "dev", Host: "host", Err: errors.New("unable to authenticate")},
			event: hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseAuthRejected},
		},
		{
			name:  "nothing to offer is this side's configuration, not the host's answer",
			err:   &ssh.ErrNoAuthMethod{User: "dev", Host: "host", Mode: "publicKey"},
			event: hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseNoAuthMethod},
		},
		{
			name:  "a transport that did not come up may be retried",
			err:   fmt.Errorf("dial 10.0.0.1:22: %w", errors.New("connection refused")),
			event: hostconn.Event{Kind: hostconn.Lost, Cause: hostconn.CauseLostLink},
		},
	}
	for _, c := range cases {
		if got := dialFailure(c.err); got != c.event {
			t.Errorf("%s: reads %v, want %v", c.name, got, c.event)
		}
	}
}

// TestAFailedDialIsNotRefusedWithoutEvidence is the boundary the owner's report
// was about: a failure nobody classified must never become "the far side
// refused", because that is a sentence that sends a person to look at a host.
func TestAFailedDialIsNotRefusedWithoutEvidence(t *testing.T) {
	got := dialFailure(errors.New("read tcp 127.0.0.1:5000->10.0.0.1:22: read: operation timed out"))
	if got.Kind == hostconn.Refused {
		t.Fatalf("an unclassified transport failure reads as a refusal: %v", got)
	}
}

// TestThePlanKeyIsThePoolsOwnDerivation is nocx-oalo1.4.4's "one function
// derives the connection key": the value the supervisor keys its state by IS
// the value the pool keys its table by, for the destination and for every hop.
func TestThePlanKeyIsThePoolsOwnDerivation(t *testing.T) {
	spec := ssh.PooledSpec{
		Host: "target.example", Port: 2222, User: "dev", Identity: "cred-7",
		Route: []ssh.PooledHop{
			{Host: "bastion.example", Port: 22, User: "jump", Identity: "jump-key"},
			{Host: "inner.example", Port: 22, User: "hop", Identity: "hop-key"},
		},
	}
	plan := PlanFor(spec)
	if plan.Key != spec.Key() {
		t.Fatalf("the supervisor keys by %+v and the pool by %+v: two derivations of one connection", plan.Key, spec.Key())
	}
	want := ssh.RouteKeys(spec.Route)
	if len(plan.Route) != len(want) {
		t.Fatalf("the plan carries %d hop keys, want %d", len(plan.Route), len(want))
	}
	for i := range want {
		if plan.Route[i] != want[i] {
			t.Fatalf("hop %d: the supervisor keys by %+v and the pool by %+v", i, plan.Route[i], want[i])
		}
	}

	// And the components are the ones that decide "same connection": the same
	// endpoint and account, a different principal, is a different key.
	other := spec
	other.Identity = "cred-8"
	if other.Key() == spec.Key() {
		t.Fatal("two principals on one endpoint derive one key: a connection would be shared across credentials")
	}
	// A different route is a different key too.
	routed := spec
	routed.Route = spec.Route[:1]
	if routed.Key() == spec.Key() {
		t.Fatal("two routes to one endpoint derive one key: a connection would be shared across bastions")
	}
}
