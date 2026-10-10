package client

import (
	"context"
	"slices"
	"testing"

	"github.com/shady2k/nocx/internal/lifecycle"
	"github.com/shady2k/nocx/internal/lifecyclecommit"
)

// A COMPLETION ACCEPTED INSIDE A LIFECYCLE FRAME WAITS FOR ITS COMMIT
// (ADR-0077 decision 12). The helper seals the interval a completion names
// once it is told, so a completion from a frame that is never stored — the
// next coordinator applies that frame again and tells the helper then — may
// not be told now. It keeps its place in the queue meanwhile, so what was
// accepted after it still lands after it, and a frame that fails takes its
// completion out without holding up the rest.
func TestACompletionFromAFrameIsDeliveredOnlyIfTheFrameIsStored(t *testing.T) {
	spy := newSpy()
	dl, _ := newTestDownlink(t, spy)
	dl.Bind(HostSessionID{Session: "0123456789abcdef0123456789abcdef"})
	acceptIn := func(ctx context.Context, fence byte) {
		t.Helper()
		if err := dl.Accept(ctx, func() error { return nil }, &lifecycle.Complete{Fence: lifecycle.FenceNonce{fence}}); err != nil {
			t.Fatalf("accept: %v", err)
		}
	}

	failed, failedFrame := lifecyclecommit.Begin(context.Background())
	acceptIn(failed, 1)
	acceptIn(context.Background(), 2) // accepted after it, outside any frame
	failedFrame.End(false)

	stored, storedFrame := lifecyclecommit.Begin(context.Background())
	acceptIn(stored, 3)
	acceptIn(context.Background(), 4)
	storedFrame.End(true)

	awaitLanded(t, spy, 3)
	want := [][32]byte{{2}, {3}, {4}}
	if got := spy.delivered(); !slices.Equal(got, want) {
		t.Fatalf("delivered %v, want the failed frame's completion dropped and the rest in acceptance order: %v", got, want)
	}
}
