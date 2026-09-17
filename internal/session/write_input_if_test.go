package session

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/ssh"
)

// gatedChannel is a channel whose Write parks until the test releases it, so a
// test can hold the write queue AT THE WRITE instead of racing for that
// window: everything already recorded happened, and everything after the
// release is decided by what the queue does next.
type gatedChannel struct {
	mu      sync.Mutex
	got     []string
	entered chan struct{} // one token per Write that has begun
	gate    chan struct{} // closed by the test to let writes through
	done    chan struct{}
}

func newGatedChannel() *gatedChannel {
	return &gatedChannel{
		got:     nil,
		entered: make(chan struct{}, 64),
		gate:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (c *gatedChannel) Read(p []byte) (int, error) { return 0, io.EOF }

func (c *gatedChannel) Write(p []byte) (int, error) {
	// The announcement is best-effort: a channel that blocked here would park
	// the writer on the test's bookkeeping rather than on the gate.
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-c.gate
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, string(p))
	return len(p), nil
}

func (c *gatedChannel) release() { close(c.gate) }

func (c *gatedChannel) wrote(what string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, g := range c.got {
		if g == what {
			return true
		}
	}
	return false
}

func (c *gatedChannel) Close() error                                      { return nil }
func (c *gatedChannel) Done() <-chan struct{}                             { return c.done }
func (c *gatedChannel) Resize(_ context.Context, _, _, _, _ uint16) error { return nil }
func (c *gatedChannel) ShellIntegrationReason() ssh.RefusalReason         { return ssh.ReasonNone }

// TestWriteInputIf_DiscardsWhenTheconditionGoneAtTheWrite is finding 1 of the
// review of 1e899f6a, and it is the whole reason this verb exists: a caller
// that resolved an attempt and enqueued a byte has NOT serialized that byte
// against the attempt's closure, and the queue may hold it long enough for the
// addressee to go. The byte must be discarded, never written into whatever
// holds the terminal next.
//
// The window is built, not raced for: the channel parks inside its first write,
// so the second item sits in the queue while the test decides what happened to
// its addressee.
func TestWriteInputIf_DiscardsWhenTheConditionGoneAtTheWrite(t *testing.T) {
	ch := newGatedChannel()
	reg, sess := openWith(t, ch)
	defer func() { _ = reg.Close(sess.ID()) }()

	// Park the writer inside the first item.
	if !sess.EnqueueWrite([]byte("first")) {
		t.Fatal("the queue refused the first item")
	}
	select {
	case <-ch.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the channel never began the first write")
	}

	// The gated byte is queued while the writer is parked.
	var holds bool
	var holdsMu sync.Mutex
	holdsMu.Lock()
	holds = true
	holdsMu.Unlock()
	type outcome struct {
		written bool
		err     error
	}
	got := make(chan outcome, 1)
	go func() {
		written, err := sess.WriteInputIf(context.Background(), []byte{0x03}, func() bool {
			holdsMu.Lock()
			defer holdsMu.Unlock()
			return holds
		})
		got <- outcome{written: written, err: err}
	}()

	// Its addressee leaves BEFORE the write. Nothing about the caller's
	// earlier resolution can see this; only the write-time check can.
	holdsMu.Lock()
	holds = false
	holdsMu.Unlock()
	ch.release()

	select {
	case out := <-got:
		if out.err != nil {
			t.Fatalf("WriteInputIf: %v", out.err)
		}
		if out.written {
			t.Fatal("the byte was reported written after its addressee had gone")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WriteInputIf never answered")
	}
	if ch.wrote("\x03") {
		t.Fatal("an interrupt was written into a terminal whose command had gone")
	}
}

// The paired positive: while the condition holds, the byte is written and the
// caller is told it was.
func TestWriteInputIf_WritesWhileTheConditionHolds(t *testing.T) {
	ch := newGatedChannel()
	ch.release()
	reg, sess := openWith(t, ch)
	defer func() { _ = reg.Close(sess.ID()) }()

	written, err := sess.WriteInputIf(context.Background(), []byte{0x03}, func() bool { return true })
	if err != nil {
		t.Fatalf("WriteInputIf: %v", err)
	}
	if !written {
		t.Fatal("the byte was not reported written with the condition holding")
	}
	if !ch.wrote("\x03") {
		t.Fatal("the byte never reached the channel")
	}
}

// And a refusal is told apart from a discarded byte: the same verb, with the
// queue genuinely full — the writer is PROVED parked inside its first write
// before the rest of the queue is filled, so nothing can drain while the count
// below is taken — answers an error rather than "discarded". The two outcomes
// mean different things to the caller that must report them.
func TestWriteInputIf_RefusesARefusedQueue(t *testing.T) {
	ch := newGatedChannel()
	reg, sess := openWith(t, ch)
	defer func() { _ = reg.Close(sess.ID()) }()

	// One filler, and the proof that writeLoop has taken it and is blocked in
	// the channel: from here the queue cannot drain, so filling it to its
	// refusal is a stable state rather than a race.
	if !sess.EnqueueWrite([]byte("filler-0")) {
		t.Fatal("the first filler was refused with a queue that had room")
	}
	select {
	case <-ch.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never began the first filler")
	}
	filled := 1
	for {
		if !sess.EnqueueWrite([]byte("filler-" + strconv.Itoa(filled))) {
			break
		}
		filled++
		if filled > 4096 {
			t.Fatal("the write queue never filled")
		}
	}

	// A bounded context, so a setup that was not actually full FAILS on the
	// assertion below instead of waiting for a write the gate is holding.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	written, err := sess.WriteInputIf(ctx, []byte{0x03}, func() bool { return true })
	if written {
		t.Fatal("a refused byte was reported written")
	}
	if !errors.Is(err, ErrInputRefused) {
		t.Fatalf("a full queue answered %v, want ErrInputRefused", err)
	}

	// Release, and wait for the drain to have reached the LAST accepted
	// filler: the queue is FIFO, so its arrival proves every filler before it
	// has been written and the session can be closed without a parked writer.
	ch.release()
	deadline := time.Now().Add(10 * time.Second)
	for !ch.wrote("filler-" + strconv.Itoa(filled-1)) {
		if time.Now().After(deadline) {
			t.Fatalf("the queue never drained to filler-%d", filled-1)
		}
		time.Sleep(time.Millisecond)
	}
}
