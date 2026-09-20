package client_test

// The downlink's STOP, at the seam the composition wires (the delivery
// context). The lifetime fix moves whose context that is — the hosted
// session's, not the opening connection's — and this file pins what must hold
// when it fires: the delivery is attempted nowhere, the failure is REPORTED,
// and the kernel's execution state is exactly what it set when it accepted
// the completion. Who cancels the context is the composition's decision
// (internal/app's helper_hosted.go and the readopt pass); the downlink's own
// answer to the cancellation — decide at dispatch, never reach the wire, tell
// the report seam — is what is judged here.

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
)

// TestADownlinkStoppedByItsContextReportsAndLeavesTheKernelState: the hosted
// session ended, the downlink's delivery context is cancelled — the stop the
// composition now hangs off the session's own end — and a completion the
// kernel accepts afterwards is delivered nowhere, reported, and leaves the
// attempt exactly as the kernel set it.
func TestADownlinkStoppedByItsContextReportsAndLeavesTheKernelState(t *testing.T) {
	c, rec, pub := completionStand(t, nil)
	ctx, stop := context.WithCancel(context.Background())

	var reported []error
	downlink := client.NewCompletionDownlink(c, ctx, func(err error) { reported = append(reported, err) })
	observing := client.NewCompletionObservingKernel(pub, downlink)

	spawned, err := c.Spawn(ctx, proto.SpawnParams{
		Cols: 80, Rows: 24,
		Lifecycle: &proto.LifecycleLaunch{Lane: "lane-under-test", Domain: "dom-under-test", Epoch: 7, Capability: hex.EncodeToString(make([]byte, 32))},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	downlink.Bind(spawned.HostSessionID)

	// THE HOSTED SESSION ENDED: the delivery context is cancelled. This is
	// the whole of the downlink's stop — nothing else about the channel, the
	// client or the kernel changed.
	stop()

	att, _, fence := driveAcceptedCompletion(t, observing, pub)

	if got := rec.received(); len(got) != 0 {
		t.Fatalf("%d completions crossed the wire after the downlink stopped, want none: %+v", len(got), got)
	}
	if len(reported) != 1 || reported[0] == nil {
		t.Fatalf("the delivery after the downlink stopped reported %v, want the cancellation", reported)
	}
	state, ok := pub.Attempt(att.ID)
	if !ok || state.State != lifecycle.AttemptCompleted || state.ExitCode == nil || *state.ExitCode != 7 {
		t.Fatalf("the kernel's attempt is %+v, want AttemptCompleted with exit 7 — the stopped downlink must not touch it", state)
	}
	if state.Fence != lifecycle.FenceNonce(fence) {
		t.Fatalf("the kernel's attempt carries fence %x, want the one it accepted", state.Fence)
	}
}
