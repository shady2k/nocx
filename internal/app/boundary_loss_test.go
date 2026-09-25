package app

// A boundary the kernel accepted and the helper will never hear of reaches
// the transport's block stream (nocx-2v80t.3.29): the pane's completion
// downlink, built the way the hosted and re-adopted routes build it, reports
// the loss through boundaryLossTo to the session the downlink was bound to —
// the session the same pane's block rows stream under.

import (
	"context"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
)

// refusingSender answers every completion with the helper's refusal, and
// every environment entry the same way.
type refusingSender struct{}

func (refusingSender) LifecycleComplete(context.Context, proto.LifecycleCompleteParams) error {
	return &client.RefusalError{Code: "no_such_session", Message: "no such session"}
}

func (refusingSender) LifecycleEntered(context.Context, proto.LifecycleEnteredParams) error {
	return &client.RefusalError{Code: "no_such_session", Message: "no such session"}
}

func TestALostBoundaryReachesTheBlockStreamOfItsSession(t *testing.T) {
	const helperSession = "0123456789abcdef0123456789abcdef"
	sink := &fakeSink{lostCh: make(chan struct{}, 4)}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	downlink := client.NewCompletionDownlink(refusingSender{}, ctx, boundaryLossTo(sink))
	downlink.Bind(client.HostSessionID{Generation: "gen", Session: helperSession})

	fence := lifecycle.FenceNonce{0x71}
	if err := downlink.Accept(func() error { return nil }, &lifecycle.Complete{Fence: fence}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	downlink.ObserveEnvironmentEntry("dom-child")
	for i := range 2 {
		select {
		case <-sink.lostCh:
		case <-time.After(5 * time.Second):
			t.Fatalf("%d of 2 lost boundaries reached the block stream", i)
		}
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	want := []lostBoundary{
		{sid: session.ID(helperSession), nonce: [32]byte(fence)},
		{sid: session.ID(helperSession), nonce: [32]byte{}},
	}
	if len(sink.lost) != 2 || sink.lost[0] != want[0] || sink.lost[1] != want[1] {
		t.Fatalf("the block stream was told %+v, want %+v", sink.lost, want)
	}
}

// Paired: an end marker the helper settled without its fence reaches the
// block stream saying so, and an ordinary one says it is whole.
func TestAnEndMarkersNoFenceReachesTheBlockStream(t *testing.T) {
	sink := &fakeSink{answer: func(uint64, int) (uint64, bool) { return 0, false }}
	src := &fakeSource{}
	stop := bindBlockRowsTo(context.Background(), sink, "s1", src, &gatedConfirmer{asked: make(chan uint64, 1), release: make(chan struct{})})
	defer stop()

	src.deliverEnd(client.IntervalEnd{Nonce: sessionruntime.FenceNonce{1}, EndRow: 3, NoFence: true})
	src.deliverEnd(client.IntervalEnd{Nonce: sessionruntime.FenceNonce{2}, EndRow: 4})

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.ends) != 2 || !sink.ends[0].NoFence || sink.ends[1].NoFence {
		t.Fatalf("the block stream was handed %+v, want the first settled without its fence and the second whole", sink.ends)
	}
}

// A pane whose rows are not streamed routes no loss anywhere.
func TestNoBlockStreamRoutesNoLoss(t *testing.T) {
	if boundaryLossTo(nil) != nil {
		t.Fatal("a nil block stream was given a loss route")
	}
}
