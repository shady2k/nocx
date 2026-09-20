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
	"sync"
	"testing"
	"time"

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

// blockingSender is one pane's carrier with a hand on it: the first delivery
// is held inside the call until the test lets it go, which is what makes the
// boundary below observable at all rather than a race the test wins by
// scheduling luck.
type blockingSender struct {
	entered chan struct{}
	release chan struct{}

	mu   sync.Mutex
	sent []proto.LifecycleCompleteParams
}

func (b *blockingSender) LifecycleComplete(ctx context.Context, p proto.LifecycleCompleteParams) error {
	b.mu.Lock()
	first := len(b.sent) == 0
	b.sent = append(b.sent, p)
	b.mu.Unlock()
	if first {
		close(b.entered)
		<-b.release
	}
	return nil
}

func (b *blockingSender) delivered() []proto.LifecycleCompleteParams {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]proto.LifecycleCompleteParams(nil), b.sent...)
}

// TestADeliveryOnTheWireFinishesAndTheNextOneNeverStarts states the stop's
// boundary with both ends, which "cancelled before dispatch" above does not:
// a delivery that had already BEGUN when the session ended runs to its own
// end — it was dispatched legally and nothing here can recall bytes already
// written — and the next one does not begin at all.
//
// The window between the dispatch check and the write is not closed further
// on purpose (deliver says why): serialising the write against the
// cancellation would hold a lock across a network call to buy a helper
// answering "no such session" to an op nobody is waiting for. This test is
// what that decision is measured against, so it fails the day the check
// moves out of dispatch.
func TestADeliveryOnTheWireFinishesAndTheNextOneNeverStarts(t *testing.T) {
	sender := &blockingSender{entered: make(chan struct{}), release: make(chan struct{})}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	var reported []error
	var reportMu sync.Mutex
	downlink := client.NewCompletionDownlink(sender, ctx, func(err error) {
		reportMu.Lock()
		reported = append(reported, err)
		reportMu.Unlock()
	})
	downlink.Bind(client.HostSessionID{Generation: "gen-1", Session: "sess-1"})

	code := 0
	first := [32]byte{1, 1, 1}
	onTheWire := make(chan struct{})
	go func() {
		defer close(onTheWire)
		downlink.Observe(first, &code)
	}()

	// THE FIRST DELIVERY IS INSIDE THE CALL. The session ends underneath it.
	<-sender.entered
	stop()
	close(sender.release)
	<-onTheWire

	// THE NEXT ONE, after the end, begins nowhere.
	downlink.Observe([32]byte{2, 2, 2}, &code)

	got := sender.delivered()
	if len(got) != 1 {
		t.Fatalf("%d deliveries reached the carrier, want only the one already on the wire: %+v", len(got), got)
	}
	if got[0].Nonce != hex.EncodeToString(first[:]) {
		t.Fatalf("the delivered completion carries nonce %q, want the first one's", got[0].Nonce)
	}
	reportMu.Lock()
	defer reportMu.Unlock()
	if len(reported) != 1 || reported[0] == nil {
		t.Fatalf("the completion refused after the end reported %v, want exactly the one cancellation", reported)
	}
}

// deadlineSender records the context every delivery is made under.
type deadlineSender struct {
	mu        sync.Mutex
	deadlines []time.Time
	hadNone   int
}

func (d *deadlineSender) LifecycleComplete(ctx context.Context, _ proto.LifecycleCompleteParams) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if at, ok := ctx.Deadline(); ok {
		d.deadlines = append(d.deadlines, at)
	} else {
		d.hadNone++
	}
	return nil
}

// TestEveryDeliveryIsBoundedSoAWedgedHelperCannotStallTheLifecycleStream:
// deliver runs on the adapter's ingest goroutine, synchronously under the
// kernel's Ingest, so an unbounded call is not one slow completion — it is
// every later lifecycle event for that pane parked behind it until the
// session ends. A helper that holds its connection open and never answers is
// exactly the shape that does it.
func TestEveryDeliveryIsBoundedSoAWedgedHelperCannotStallTheLifecycleStream(t *testing.T) {
	sender := &deadlineSender{}
	downlink := client.NewCompletionDownlink(sender, context.Background(), nil)
	downlink.Bind(client.HostSessionID{Generation: "gen-1", Session: "sess-1"})

	code := 0
	downlink.Observe([32]byte{3, 3, 3}, &code)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.hadNone != 0 || len(sender.deadlines) != 1 {
		t.Fatalf("%d delivery(ies) ran with no deadline at all and %d with one: every delivery must be bounded",
			sender.hadNone, len(sender.deadlines))
	}
	// The bound is the downlink's own; the test pins that there IS one and
	// that it is a wait a person would accept, not the constant's value.
	if until := time.Until(sender.deadlines[0]); until <= 0 || until > time.Minute {
		t.Fatalf("the delivery's deadline is %v away, want a bounded wait on this side of a minute", until)
	}
}
