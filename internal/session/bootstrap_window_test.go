package session

// The bootstrap window on a LOCAL session (design §5.5, §5.3).
//
// A typed `ssh` runs inside the user's own shell, on the pty this backend
// owns, so the frames the far-side loader reads travel through this session's
// input and its tokens come back through this session's output. That is the
// same bounded exception to AD-6 the managed path already has, applied to the
// one place the typed path can reach: the window opens at a named event,
// closes at a named event, parses no VT, and takes nothing away from the
// renderer — every byte still reaches it, in order.

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

func TestBootstrapWindow_TheRendererStillSeesEveryByte(t *testing.T) {
	w, feed, rendered := newWindowFixture(t)
	defer func() { _ = w.Close() }()

	feed("NOCX1 LOADER_READY\r\nsome output\r\n")
	line, err := w.ReadLine(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if line != "NOCX1 LOADER_READY" {
		t.Fatalf("read %q, want the loader's token", line)
	}
	if got := rendered(); got != "NOCX1 LOADER_READY\r\nsome output\r\n" {
		t.Fatalf("the renderer saw %q; the window may add, remove or reorder no byte on its way there", got)
	}
}

func TestBootstrapWindow_ReadsLinesInOrderAndSurvivesSplitChunks(t *testing.T) {
	w, feed, _ := newWindowFixture(t)
	defer func() { _ = w.Close() }()

	feed("NOCX1 LOA")
	feed("DER_READY\nNOCX1 STAGE_READY\n")
	for _, want := range []string{"NOCX1 LOADER_READY", "NOCX1 STAGE_READY"} {
		got, err := w.ReadLine(context.Background(), time.Second)
		if err != nil {
			t.Fatalf("ReadLine: %v", err)
		}
		if got != want {
			t.Fatalf("read %q, want %q", got, want)
		}
	}
}

// The deadline is the writer's and it may never consume the far side's bytes:
// a line that had not completed when the deadline fired is still there for
// the next read.
func TestBootstrapWindow_ADeadlineConsumesNothing(t *testing.T) {
	w, feed, _ := newWindowFixture(t)
	defer func() { _ = w.Close() }()

	feed("half a li")
	if _, err := w.ReadLine(context.Background(), time.Millisecond); !errors.Is(err, ErrBootstrapDeadline) {
		t.Fatalf("ReadLine returned %v, want ErrBootstrapDeadline", err)
	}
	feed("ne\n")
	got, err := w.ReadLine(context.Background(), time.Second)
	if err != nil {
		t.Fatalf("ReadLine after the deadline: %v", err)
	}
	if got != "half a line" {
		t.Fatalf("read %q; the deadline ate the bytes that had already arrived", got)
	}
}

// §5.3: while the window is open the USER's keystrokes are refused, not
// buffered — a buffered keystroke is a command they did not knowingly run,
// executed later at a prompt they were not looking at. Ours are not the
// user's, so the window's own writes go through.
func TestBootstrapWindow_QuarantinesTheUsersInputAndNotItsOwn(t *testing.T) {
	w, _, _ := newWindowFixture(t)

	s := windowSession(t, w)
	// Before ownership is proven the user is still talking to their own ssh
	// client — a host-key prompt, a password, a second factor — and their
	// keystrokes must reach it.
	if !s.EnqueueWrite([]byte("their password")) {
		t.Fatal("the user's own authentication input was refused before nocx had interposed at all")
	}
	w.QuarantineInput()
	if s.EnqueueWrite([]byte("typed")) {
		t.Fatal("a keystroke reached the terminal while the bootstrap owned it")
	}
	if _, err := w.Write([]byte("NOCX1 1        4\nabcd")); err != nil {
		t.Fatalf("the window could not write its own frame: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !s.EnqueueWrite([]byte("typed")) {
		t.Fatal("input was not re-enabled at the terminal outcome")
	}
	ch, ok := s.ch.(*fakeWindowChannel)
	if !ok {
		t.Fatalf("channel is %T, want *fakeWindowChannel", s.ch)
	}
	if got := ch.written(); strings.Contains(got, "typed") {
		t.Fatalf("a refused keystroke was delivered later; the terminal received %q", got)
	}
}

// One window, one interval: a second open is refused rather than silently
// handing two owners the same stream.
func TestBootstrapWindow_OnlyOneAtATime(t *testing.T) {
	w, _, _ := newWindowFixture(t)
	defer func() { _ = w.Close() }()
	s := windowSession(t, w)
	if _, err := s.OpenBootstrapWindow(); err == nil {
		t.Fatal("a second bootstrap window opened on the same session")
	}
}

// Close is idempotent: the interval closes once, whatever runs it.
func TestBootstrapWindow_ClosesOnce(t *testing.T) {
	w, _, _ := newWindowFixture(t)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	s := windowSession(t, w)
	if _, err := s.OpenBootstrapWindow(); err != nil {
		t.Fatalf("the window could not be reopened after a closed one: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Fixture: a realSession over a channel whose reads the test drives.

type fakeWindowChannel struct {
	mu   sync.Mutex
	out  chan []byte
	done chan struct{}
	wrot []byte
}

func (c *fakeWindowChannel) Read(p []byte) (int, error) {
	chunk, ok := <-c.out
	if !ok {
		return 0, io.EOF
	}
	return copy(p, chunk), nil
}

func (c *fakeWindowChannel) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wrot = append(c.wrot, p...)
	return len(p), nil
}

func (c *fakeWindowChannel) written() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.wrot)
}

func (c *fakeWindowChannel) Close() error { return nil }
func (c *fakeWindowChannel) Resize(context.Context, uint16, uint16, uint16, uint16) error {
	return nil
}
func (c *fakeWindowChannel) Done() <-chan struct{} { return c.done }

// newWindowFixture returns an open window, a way to make the far side speak,
// and a way to read back what the renderer was given.
func newWindowFixture(t *testing.T) (BootstrapWindow, func(string), func() string) {
	t.Helper()
	ch := &fakeWindowChannel{out: make(chan []byte, 16), done: make(chan struct{})}
	s := &realSession{
		id:        NewID(),
		ch:        ch,
		log:       log.NewSlogAdapter(nil),
		writeCh:   make(chan writeJob, 8),
		writeDone: make(chan struct{}),
	}
	s.startWriteLoop()
	t.Cleanup(func() { close(s.writeDone) })

	var renderedMu sync.Mutex
	var rendered []byte
	if err := s.StartOutput(context.Background(), func(b []byte) error {
		renderedMu.Lock()
		rendered = append(rendered, b...)
		renderedMu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("StartOutput: %v", err)
	}

	w, err := s.OpenBootstrapWindow()
	if err != nil {
		t.Fatalf("OpenBootstrapWindow: %v", err)
	}
	feed := func(text string) {
		ch.out <- []byte(text)
	}
	readRendered := func() string {
		// The renderer's copy is written by the read pump; a reader has to
		// wait for the pump to have handled everything the test fed, which
		// it observes by the byte count settling on the last value rather
		// than by waiting a while.
		deadline := time.Now().Add(5 * time.Second)
		var last int
		for time.Now().Before(deadline) {
			renderedMu.Lock()
			n := len(rendered)
			renderedMu.Unlock()
			if n > 0 && n == last {
				break
			}
			last = n
			time.Sleep(time.Millisecond)
		}
		renderedMu.Lock()
		defer renderedMu.Unlock()
		return string(rendered)
	}
	return w, feed, readRendered
}

// windowSession reaches the session behind a window, which only a test in
// this package can do and only a test would want to.
func windowSession(t *testing.T, w BootstrapWindow) *realSession {
	t.Helper()
	bw, ok := w.(*bootstrapWindow)
	if !ok {
		t.Fatalf("window is %T, want *bootstrapWindow", w)
	}
	return bw.sess
}
