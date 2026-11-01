package client

// A client this side closes leaves nothing watching its lane (nocx-1ebrs.66.1.29).
//
// SocketConn's Done reports a loss that is NOT ours and never closes on our own
// Close — that is deliberate, and it means a loss watcher keyed on the lane
// alone outlives every client the coordinator closes: one goroutine per pane
// close, per helper retirement, per shutdown, for the life of the process. The
// count is the whole assertion, and it cannot be satisfied by a recomputation.

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/helper/host"
)

// laneWatchers counts the goroutines a Dial leaves watching one lane: the loss
// watcher and the exit-status watcher, both spelled Dial.funcN.
func laneWatchers() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return bytes.Count(buf[:n], []byte("client.Dial.func"))
		}
		buf = make([]byte, 2*len(buf))
	}
}

func TestAClosedClientLeavesNothingWatchingItsLane(t *testing.T) {
	const hash = "testhash"
	before := laneWatchers()

	ours, theirs := net.Pipe()
	t.Cleanup(func() { _ = theirs.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	daemon := host.New(theirs, theirs, hash, "instance-1", logger)
	go func() { _ = daemon.Serve(context.Background()) }()

	c, err := Dial(context.Background(), Config{
		Exec:        NewSocketConn(ours),
		ExpectHash:  hash,
		SentinelTTL: 5 * time.Second,
		Log:         logger,
	})
	if err != nil {
		t.Fatalf("dial the socket daemon: %v", err)
	}
	if got := laneWatchers(); got <= before {
		t.Fatalf("a dialled lane left %d watcher(s), no more than the %d before it — the test would prove nothing", got, before)
	}

	// THIS SIDE CLOSES while the daemon is still serving: that is the state a
	// coordinator's own detach meets a live helper in, and the one SocketConn
	// deliberately does not report as loss.
	if err := c.Close(); err != nil {
		t.Fatalf("close the client: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		left := laneWatchers() - before
		if left <= 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutine(s) are still watching a lane this side closed; Close must end the loss watcher", left)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
