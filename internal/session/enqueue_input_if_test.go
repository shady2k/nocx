package session

import (
	"context"
	"io"
	"strconv"
	"strings"
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

// enqueueAndWait queues p with its condition and waits for the writer's
// verdict. queued=false is the queue's own refusal, with no verdict to wait
// for. The wait is the TEST's: the verb itself never waits.
func enqueueAndWait(t *testing.T, sess Session, p []byte, holds func() bool) (queued, written bool, err error) {
	t.Helper()
	type verdict struct {
		written bool
		err     error
	}
	got := make(chan verdict, 1)
	if !sess.EnqueueInputIf(p, holds, func(w bool, e error) { got <- verdict{written: w, err: e} }) {
		return false, false, nil
	}
	select {
	case v := <-got:
		return true, v.written, v.err
	case <-time.After(5 * time.Second):
		t.Fatal("EnqueueInputIf never settled")
		return true, false, nil
	}
}

// TestEnqueueInputIf_DiscardsWhenTheConditionGoneAtTheWrite is finding 1 of the
// review of 1e899f6a, and it is the whole reason this verb exists: a caller
// that resolved an attempt and enqueued a byte has NOT serialized that byte
// against the attempt's closure, and the queue may hold it long enough for the
// addressee to go. The byte must be discarded, never written into whatever
// holds the terminal next.
//
// The window is built, not raced for: the channel parks inside its first write,
// so the second item sits in the queue while the test decides what happened to
// its addressee.
func TestEnqueueInputIf_DiscardsWhenTheConditionGoneAtTheWrite(t *testing.T) {
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
	if !sess.EnqueueInputIf([]byte{0x03}, func() bool {
		holdsMu.Lock()
		defer holdsMu.Unlock()
		return holds
	}, func(written bool, err error) { got <- outcome{written: written, err: err} }) {
		t.Fatal("the queue refused the conditioned byte with room to spare")
	}

	// Its addressee leaves BEFORE the write. Nothing about the caller's
	// earlier resolution can see this; only the write-time check can.
	holdsMu.Lock()
	holds = false
	holdsMu.Unlock()
	ch.release()

	select {
	case out := <-got:
		if out.err != nil {
			t.Fatalf("EnqueueInputIf settled with %v", out.err)
		}
		if out.written {
			t.Fatal("the byte was reported written after its addressee had gone")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EnqueueInputIf never settled")
	}
	if ch.wrote("\x03") {
		t.Fatal("an interrupt was written into a terminal whose command had gone")
	}
}

// The paired positive: while the condition holds, the byte is written and the
// caller is told it was.
func TestEnqueueInputIf_WritesWhileTheConditionHolds(t *testing.T) {
	ch := newGatedChannel()
	ch.release()
	reg, sess := openWith(t, ch)
	defer func() { _ = reg.Close(sess.ID()) }()

	queued, written, err := enqueueAndWait(t, sess, []byte{0x03}, func() bool { return true })
	if !queued || err != nil {
		t.Fatalf("EnqueueInputIf: queued=%v err=%v", queued, err)
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
// below is taken — answers "not queued" and never settles, rather than
// settling "discarded". The two outcomes mean different things to the caller
// that must report them.
func TestEnqueueInputIf_RefusesARefusedQueue(t *testing.T) {
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

	settled := make(chan struct{}, 1)
	if sess.EnqueueInputIf([]byte{0x03}, func() bool { return true }, func(bool, error) { settled <- struct{}{} }) {
		t.Fatal("a full queue accepted the conditioned byte")
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
	// Drained, and the refused byte never settled: nothing was queued for it.
	select {
	case <-settled:
		t.Fatal("a refused byte was settled; a refusal has no verdict")
	default:
	}
	if ch.wrote("\x03") {
		t.Fatal("a refused byte reached the channel")
	}
}

// TestEnqueueInputIf_KeepsItsPlaceInTheQueue pins the ordering guarantee, and it
// is constructed so that nothing about the ordering is raced for: the condition
// below runs INSIDE the drain, immediately before the write, and enqueues the
// "later input" there — so the marker enters the queue after the conditioned
// byte has been dequeued, and FIFO makes the recorded order the assertion.
//
// The property matters because this queue carries the USER's input: a command
// line typed after an interrupt must reach the terminal after it, never instead
// of it, and a discarded interrupt must not take the place of anything behind
// it — or the queue would lose input the person typed.
func TestEnqueueInputIf_KeepsItsPlaceInTheQueue(t *testing.T) {
	for _, tc := range []struct {
		name      string
		holds     bool
		wantBytes []string
	}{
		{
			name:      "a written byte is written before the input queued behind it",
			holds:     true,
			wantBytes: []string{"MARK-AFTER"},
		},
		{
			name:      "a discarded byte is dropped in place and the input behind it still lands",
			holds:     false,
			wantBytes: []string{"MARK-AFTER"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := newRecordingChannel()
			reg, sess := openWith(t, ch)
			defer func() { _ = reg.Close(sess.ID()) }()

			queued, written, err := enqueueAndWait(t, sess, []byte("CONDITIONED"), func() bool {
				// Enqueued at the one moment that is provably after this item
				// has left the queue and before its verdict.
				if !sess.EnqueueWrite([]byte("MARK-AFTER")) {
					t.Error("the marker was refused with a queue that had room")
				}
				return tc.holds
			})
			if !queued || err != nil {
				t.Fatalf("EnqueueInputIf: queued=%v err=%v", queued, err)
			}
			if written != tc.holds {
				t.Fatalf("written = %v, want %v", written, tc.holds)
			}
			waitForFrames(t, ch, 1+boolToInt(tc.holds))

			ch.mu.Lock()
			order := append([]string(nil), ch.got...)
			ch.mu.Unlock()
			want := append([]string(nil), tc.wantBytes...)
			if tc.holds {
				want = append([]string{"CONDITIONED"}, want...)
			}
			if strings.Join(order, "|") != strings.Join(want, "|") {
				t.Fatalf("the channel saw %v, want %v", order, want)
			}
		})
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
