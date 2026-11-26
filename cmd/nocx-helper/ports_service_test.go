package main

// The ports service is registered by EVERY build (nocx-oalo1.13.2), and that is
// asserted through the function the daemon's accept loop calls rather than by
// reading services.go: a stand that registered the service itself would keep
// passing on a build that had stopped registering it.
//
// The other half of the answer is what makes this a check rather than a
// count — the op is served, over the real handshake, by the real client — and
// the state it answers is `available` with no listeners, because the stand's
// host has ss and not much else.

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestTheHelperServesPortsOnEveryBuild(t *testing.T) {
	seam, err := holdSSHClient(discardLog())
	if err != nil {
		t.Fatalf("holdSSHClient: %v", err)
	}
	defer seam.release()

	c := standUpHelper(t, seam)

	var sample proto.PortsSampleResult
	if err := c.Call(context.Background(), proto.ServicePorts, proto.OpPortsSample, proto.PortsSampleParams{}, &sample); err != nil {
		t.Fatalf("%s.%s: %v", proto.ServicePorts, proto.OpPortsSample, err)
	}
	if sample.State != proto.PortsStateAvailable {
		t.Fatalf("sample state = %q (%s), want available", sample.State, sample.Classification)
	}
	if sample.Listeners == nil || sample.ProbesTried == nil {
		t.Fatalf("sample lists = %+v / %+v, want [] rather than nil over the wire", sample.Listeners, sample.ProbesTried)
	}
	if sample.Probe != "ss" {
		t.Fatalf("probe = %q, want ss", sample.Probe)
	}
}
