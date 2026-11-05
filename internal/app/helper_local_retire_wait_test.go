package app

// The local route's wait for a retiring generation is bounded (nocx-398ca).
//
// retryableLocalHelperOpen retries a `draining` refusal only after the old
// client ends, and waitForClientLoss was the wait for that event. An event
// with no bound is not one: a daemon can refuse new spawns while an admission
// it already granted is still finishing — and that admission may be a spawn
// waiting on a person at a password prompt — so the connection can stay open
// for minutes while the open holds one of the execution lane's eight permits.
// Past the bound the open reports the refusal it already has.

import (
	"context"
	"io"
	"testing"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
)

// TestWaitingForARetiringLocalHelperIsBounded builds a real client whose
// helper handshakes and then stays connected, and requires the wait to end on
// its own bound rather than on the caller's context (which a WS request's
// does not close by itself).
func TestWaitingForARetiringLocalHelperIsBounded(t *testing.T) {
	previous := helperRetireWaitTimeout
	helperRetireWaitTimeout = 150 * time.Millisecond
	t.Cleanup(func() { helperRetireWaitTimeout = previous })

	peer := func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, syntheticArtifactHash, "instance-1", discardLogger(t))
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
	lane := newFakeLaneConn(peer)
	t.Cleanup(func() { _ = lane.Close() })
	c, err := helperclient.Dial(context.Background(), helperclient.Config{
		Exec:       lane,
		ExpectHash: syntheticArtifactHash,
		Log:        discardLogger(t),
	})
	if err != nil {
		t.Fatalf("dialing the stand's helper: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	opener := &localHelperOpener{}
	start := time.Now()
	err = opener.waitForClientLoss(context.Background(), c)
	if err == nil {
		t.Fatal("the wait for a helper whose connection never ended returned success")
	}
	if elapsed := time.Since(start); elapsed < helperRetireWaitTimeout {
		t.Fatalf("the wait returned after %s, before its own bound of %s", elapsed, helperRetireWaitTimeout)
	}
}
