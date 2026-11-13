package session

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/ssh"
)

// stuckChannel is the channel this whole mechanism exists for: a dead SSH
// channel behind a NAT or firewall that drops silently. Write never returns,
// because there is no RST to return an error from. Close releases it, which
// is what a real gossh channel does too.
type stuckChannel struct {
	writeEntered chan struct{} // one token per Write that has begun blocking
	done         chan struct{}
	closeOnce    sync.Once
}

func newStuckChannel() *stuckChannel {
	return &stuckChannel{
		writeEntered: make(chan struct{}, 1024),
		done:         make(chan struct{}),
	}
}

func (c *stuckChannel) Read(p []byte) (int, error) { return 0, io.EOF }

func (c *stuckChannel) Write(p []byte) (int, error) {
	c.writeEntered <- struct{}{}
	<-c.done
	return 0, io.ErrClosedPipe
}

func (c *stuckChannel) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return nil
}
func (c *stuckChannel) Done() <-chan struct{}                             { return c.done }
func (c *stuckChannel) Resize(_ context.Context, _, _, _, _ uint16) error { return nil }
func (c *stuckChannel) ShellIntegrationReason() ssh.RefusalReason         { return ssh.ReasonNone }

// recordingChannel captures what actually reached the wire, in order.
type recordingChannel struct {
	mu   sync.Mutex
	got  []string
	done chan struct{}
}

func newRecordingChannel() *recordingChannel {
	return &recordingChannel{done: make(chan struct{})}
}

func (c *recordingChannel) Read(p []byte) (int, error) { return 0, io.EOF }
func (c *recordingChannel) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, string(p))
	return len(p), nil
}
func (c *recordingChannel) Close() error                                      { return nil }
func (c *recordingChannel) Done() <-chan struct{}                             { return c.done }
func (c *recordingChannel) Resize(_ context.Context, _, _, _, _ uint16) error { return nil }
func (c *recordingChannel) ShellIntegrationReason() ssh.RefusalReason         { return ssh.ReasonNone }

// waitForFrames blocks until the channel has recorded want frames.
func waitForFrames(t *testing.T, ch *recordingChannel, want int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		ch.mu.Lock()
		got := len(ch.got)
		ch.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("only %d of %d frames reached the channel", got, want)
		case <-time.After(time.Millisecond):
		}
	}
}

func openWith(t *testing.T, ch Channel) (*Reg, Session) {
	t.Helper()
	reg := launcherReg()
	sess, err := reg.Adopt(context.Background(), Config{
		Kind:   KindRemote,
		Host:   "example.com",
		Remote: &ssh.ConnectConfig{},
	}, NewID(), ch)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	return reg, sess
}

// TestEnqueueWrite_ConcurrentWithClose_DoesNotPanic pins the failure mode that
// makes this worth a queue at all. The readLoop enqueues frames; monitorExit
// closes the session from its own goroutine the moment the channel dies —
// which is exactly when a dead channel is being typed at. If Close tears the
// queue down underneath a sender, the send panics and takes the whole backend
// with it, not one tab.
func TestEnqueueWrite_ConcurrentWithClose_DoesNotPanic(t *testing.T) {
	for i := 0; i < 300; i++ {
		reg, sess := openWith(t, newRecordingChannel())

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				sess.EnqueueWrite([]byte("x"))
			}
		}()
		go func() {
			defer wg.Done()
			_ = reg.Close(sess.ID())
		}()
		wg.Wait()
	}
}

// TestEnqueueWrite_PreservesOrder is the second half of the bug: the first fix
// for the freeze dispatched each write in its own goroutine, and the Go
// scheduler makes no FIFO promise — "ls\n" then "hostname\n" arrived as
// "lshostname". The queue has one sender and one drainer, which is what makes
// the order the user's.
func TestEnqueueWrite_PreservesOrder(t *testing.T) {
	ch := newRecordingChannel()
	reg, sess := openWith(t, ch)
	defer func() { _ = reg.Close(sess.ID()) }()

	// Sent in batches that fit the queue: a producer outrunning the drain
	// is entitled to be refused, and this test is about order, not depth.
	const batches, perBatch = 10, writeQueueDepth / 4
	n := batches * perBatch
	want := make([]string, 0, n)
	for b := 0; b < batches; b++ {
		for i := 0; i < perBatch; i++ {
			p := string(rune('a'+(b*perBatch+i)%26)) + string(rune('0'+i%10))
			want = append(want, p)
			if !sess.EnqueueWrite([]byte(p)) {
				t.Fatalf("frame %d refused with a queue that had room", len(want)-1)
			}
		}
		waitForFrames(t, ch, len(want))
	}

	ch.mu.Lock()
	defer ch.mu.Unlock()
	for i := range want {
		if ch.got[i] != want[i] {
			t.Fatalf("frame %d is %q, want %q — the queue reordered the user's input", i, ch.got[i], want[i])
		}
	}
}

// TestStuckChannel_RefusesRatherThanBlocking is the freeze itself, stated at
// the seam the transport uses. A channel that never accepts a byte must make
// EnqueueWrite return false — not block — because the caller is the one
// goroutine feeding every other session on the connection.
func TestStuckChannel_RefusesRatherThanBlocking(t *testing.T) {
	ch := newStuckChannel()
	reg, sess := openWith(t, ch)
	defer func() { _ = reg.Close(sess.ID()) }()

	// The write loop takes one frame and blocks in it forever; the queue
	// then fills. Every call has to return, whatever it answers.
	done := make(chan bool, 1)
	go func() {
		var refused bool
		for i := 0; i < writeQueueDepth*4; i++ {
			if !sess.EnqueueWrite([]byte("k")) {
				refused = true
			}
		}
		done <- refused
	}()

	select {
	case refused := <-done:
		if !refused {
			t.Fatal("a channel that accepts nothing never refused a frame: the queue is unbounded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EnqueueWrite blocked on a stuck channel — this is the freeze it exists to prevent")
	}
}

// TestClose_DoesNotWaitForAStuckWrite is the freeze arriving through the other
// door. `close` is handled on the readLoop, so if Close waited for the write
// loop to finish, a channel blocked in Write would stall every tab exactly as
// the original bug did — while looking like cleanup.
func TestClose_DoesNotWaitForAStuckWrite(t *testing.T) {
	ch := newStuckChannel()
	reg, sess := openWith(t, ch)

	if !sess.EnqueueWrite([]byte("k")) {
		t.Fatal("first frame refused by an idle session")
	}
	select {
	case <-ch.writeEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the write loop never reached the channel")
	}

	closed := make(chan struct{})
	go func() {
		_ = reg.Close(sess.ID())
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked behind a stuck write; on the readLoop that is the freeze again")
	}

	if sess.EnqueueWrite([]byte("k")) {
		t.Fatal("a closed session accepted a frame it can never write")
	}
	if _, err := sess.Write([]byte("k")); err == nil {
		t.Fatal("Write on a closed session returned no error")
	}
}
