package client_test

// The downlink's STOP, at the seam the composition wires (the delivery
// context). The lifetime fix moves whose context that is — the hosted
// session's, not the opening connection's — and this file pins what must hold
// when it fires: the delivery is attempted nowhere, the loss is LOGGED,
// and the kernel's execution state is exactly what it set when it accepted
// the completion. Who cancels the context is the composition's decision
// (internal/app's helper_hosted.go and the readopt pass); the downlink's own
// answer to the cancellation — decide before every attempt, never reach the
// wire, log the loss with its cause — is what is judged here.

import (
	"context"
	"encoding/hex"
	"errors"
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
// kernel accepts afterwards is delivered nowhere, logged lost, and leaves the
// attempt exactly as the kernel set it.
func TestADownlinkStoppedByItsContextReportsAndLeavesTheKernelState(t *testing.T) {
	c, rec, pub := completionStand(t, nil)
	logCtx, sl := downlinkLog(t)
	ctx, stop := context.WithCancel(logCtx)

	downlink := client.NewCompletionDownlink(c, ctx)
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
	if lost := lostLines(sl); len(lost) != 1 {
		t.Fatalf("the completion accepted after the downlink stopped logged %d losses, want exactly 1", len(lost))
	}
	if err := awaitLost(t, sl); !errors.Is(err, context.Canceled) {
		t.Fatalf("the loss's cause is %v, want the session's end", err)
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

// LifecycleEntered is never exercised by this file's schedules — they drive
// completions only — so it need not share the blocking behaviour above.
func (b *blockingSender) LifecycleEntered(ctx context.Context, p proto.LifecycleEnteredParams) error {
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
// The window between the check before an attempt and its write is not
// closed further on purpose: serialising the write against the cancellation
// would hold a lock across a network call to buy a helper answering "no such
// session" to an op nobody is waiting for.
func TestADeliveryOnTheWireFinishesAndTheNextOneNeverStarts(t *testing.T) {
	sender := &blockingSender{entered: make(chan struct{}), release: make(chan struct{})}
	logCtx, sl := downlinkLog(t)
	ctx, stop := context.WithCancel(logCtx)
	defer stop()

	downlink := client.NewCompletionDownlink(sender, ctx)
	downlink.Bind(client.HostSessionID{Generation: "gen-1", Session: "sess-1"})

	code := 0
	first := [32]byte{1, 1, 1}
	acceptCompletion(t, downlink, first, &code)

	// THE FIRST DELIVERY IS INSIDE THE CALL. The session ends underneath it.
	<-sender.entered
	stop()
	close(sender.release)

	// THE NEXT ONE, after the end, begins nowhere.
	acceptCompletion(t, downlink, [32]byte{2, 2, 2}, &code)

	got := sender.delivered()
	if len(got) != 1 {
		t.Fatalf("%d deliveries reached the carrier, want only the one already on the wire: %+v", len(got), got)
	}
	if got[0].Nonce != hex.EncodeToString(first[:]) {
		t.Fatalf("the delivered completion carries nonce %q, want the first one's", got[0].Nonce)
	}
	if lost := lostLines(sl); len(lost) != 1 {
		t.Fatalf("the completion accepted after the end logged %d losses, want exactly the one", len(lost))
	}
	if err := awaitLost(t, sl); !errors.Is(err, context.Canceled) {
		t.Fatalf("the loss's cause is %v, want the session's end", err)
	}
}

// deadlineSender records the context every delivery is made under.
type deadlineSender struct {
	mu        sync.Mutex
	deadlines []time.Time
	hadNone   int
	got       chan struct{}
}

func (d *deadlineSender) LifecycleComplete(ctx context.Context, _ proto.LifecycleCompleteParams) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if at, ok := ctx.Deadline(); ok {
		d.deadlines = append(d.deadlines, at)
	} else {
		d.hadNone++
	}
	d.got <- struct{}{}
	return nil
}

// LifecycleEntered records its deadline the same way: this schedule cares
// about the bound every delivery carries, not which op carries it.
func (d *deadlineSender) LifecycleEntered(ctx context.Context, _ proto.LifecycleEnteredParams) error {
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
// one worker delivers the queue head first, so an unbounded call is not one
// slow completion — it is every later boundary for that pane parked behind
// it until the session ends, and then the ingest that accepts them once the
// queue is full. A helper that holds its connection open and never answers
// is exactly the shape that does it; a bounded attempt is abandoned and
// tried again instead.
func TestEveryDeliveryIsBoundedSoAWedgedHelperCannotStallTheLifecycleStream(t *testing.T) {
	sender := &deadlineSender{got: make(chan struct{}, 1)}
	ctx, _ := downlinkLog(t)
	downlink := client.NewCompletionDownlink(sender, ctx)
	downlink.Bind(client.HostSessionID{Generation: "gen-1", Session: "sess-1"})

	code := 0
	acceptCompletion(t, downlink, [32]byte{3, 3, 3}, &code)
	select {
	case <-sender.got:
	case <-time.After(5 * time.Second):
		t.Fatal("the accepted completion never reached the carrier")
	}

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

// acceptCompletion is one completion the kernel accepted, through the
// downlink's own acceptance seam.
func acceptCompletion(t *testing.T, d *client.CompletionDownlink, fence [32]byte, exit *int) {
	t.Helper()
	if err := d.Accept(func() error { return nil }, &lifecycle.Complete{Fence: lifecycle.FenceNonce(fence), ExitCode: exit}); err != nil {
		t.Fatalf("accept: %v", err)
	}
}
