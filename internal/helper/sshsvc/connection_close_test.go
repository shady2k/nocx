//go:build nocx_local_ssh

package sshsvc_test

import (
	"context"
	"testing"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

func TestCoordinatorCloseReleasesLogicalPaneHolds(t *testing.T) {
	f := newFixture(t, "pw", newSigner(t))
	s := newStand(t, &coordinator{password: "pw", verdict: proto.HostKeyTrusted, fingerprint: f.hostKeyFingerprint()})
	destination := destinationAt(t, f.addr)
	if _, err := s.client.BindConnection(context.Background(), destination, "closed-coordinator-pane", "auto"); err != nil {
		t.Fatal(err)
	}
	// The host lifecycle calls the same observer when Serve returns. A direct
	// call lets the test inspect the released record through its public op.
	s.service.ConnectionClosed(s.helper)
	var result proto.ReleaseConnectionResult
	ctx := host.WithConnection(context.Background(), s.helper)
	if err := s.client.Call(ctx, proto.ServiceSSH, proto.OpReleaseConnection, proto.ReleaseConnectionParams{PaneID: "closed-coordinator-pane"}, &result); err != nil {
		t.Fatal(err)
	}
	if result.Released {
		t.Fatal("coordinator close retained its logical pane hold")
	}
	s.service.ConnectionClosed(s.helper)
}
