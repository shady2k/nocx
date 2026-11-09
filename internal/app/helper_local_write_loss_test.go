package app

// A frame that could not be written is a loss the local open retries
// (nocx-ouru5.1.8).
//
// The helper client's write is the first half of every call, and it is where a
// released daemon is usually found out: the peer is gone, the read pump has not
// reported anything yet, and the write fails. The local helper opener retries
// exactly one class of failure — ErrLost — because a request that never reached
// the daemon may be asked again. Before this the write failure arrived as a
// plain error, so an open that met a dead connection gave up on the spot and
// the pane it was opening mounted its failure card: a replacement tab with no
// composer, which is the owner's report.
//
// The lane here starts failing its WRITES on demand and keeps its read side
// open, so the write is the only observer of the loss and this test is about
// the join the two packages owe each other, not about a race between them.

import (
	"context"
	"fmt"
	"io"
	"sync"
	"syscall"
	"testing"
	"time"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/host"
)

// writesFail is a lane whose frames start failing on demand.
type writesFail struct {
	lane   *fakeLaneConn
	mu     sync.Mutex
	broken bool
}

func (w *writesFail) breakWrites() {
	w.mu.Lock()
	w.broken = true
	w.mu.Unlock()
}

func (w *writesFail) Stdin() io.WriteCloser { return w }
func (w *writesFail) Stdout() io.Reader     { return w.lane.Stdout() }
func (w *writesFail) Stderr() io.Reader     { return w.lane.Stderr() }
func (w *writesFail) Start(command string) error {
	return w.lane.Start(command)
}
func (w *writesFail) Wait() (int, error)    { return w.lane.Wait() }
func (w *writesFail) Done() <-chan struct{} { return w.lane.Done() }
func (w *writesFail) LostErr() error        { return w.lane.LostErr() }
func (w *writesFail) Close() error          { return w.lane.Close() }

func (w *writesFail) Write(p []byte) (int, error) {
	w.mu.Lock()
	broken := w.broken
	w.mu.Unlock()
	if broken {
		return 0, syscall.EPIPE
	}
	return w.lane.stdin.Write(p)
}

func TestAFrameThatCannotBeWrittenIsRetriedByTheLocalHelperOpen(t *testing.T) {
	peer := func(in io.Reader, out io.Writer) int {
		h := host.New(in, out, syntheticArtifactHash, "instance-1", discardLogger(t))
		if err := h.Serve(context.Background()); err != nil {
			return 1
		}
		return 0
	}
	lane := &writesFail{lane: newFakeLaneConn(peer)}
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

	lane.breakWrites()

	callErr := c.Call(context.Background(), "session", "spawn", map[string]any{"cwd": "/"}, nil)
	if callErr == nil {
		t.Fatal("a call over a lane that takes no frames answered successfully")
	}
	wrapped := fmt.Errorf("open a pane on this machine's helper: %w", callErr)
	if !retryableLocalHelperOpen(wrapped) {
		t.Fatalf("a frame that could not be written must be retried by the local open, got %v", callErr)
	}

	// AND THE LANE THE LOST CLIENT RODE IS ENDED BY THE CLIENT ITSELF
	// (nocx-ouru5.1.8). The retry above dials a fresh connection and this
	// stand's client is dropped, exactly as the opener drops it
	// (`dropIfLost`, hostHelper.invalidate) — a lost client is never reused.
	// What must not survive that is the lane: this stand's peer IS the helper
	// process, so a lane nobody closed is a helper still serving after every
	// test has ended, which is the leak ci-linux reported against this very
	// test. The bound is on a hang, not on the assertion.
	select {
	case <-lane.lane.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the lane a lost client rode never ended: its peer is still serving a connection the open has already given up on")
	}
}
