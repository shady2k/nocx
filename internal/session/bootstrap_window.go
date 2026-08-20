package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// The bootstrap window on a local session: the one interval in which the
// backend reads this session's output bytes and writes to its input (design
// §5.5, the bounded exception written into AD-6 and ADR-0024 rather than
// assumed here).
//
// # Why a local session needs one at all
//
// A typed `ssh` runs inside the user's own shell, and the shell's terminal is
// this session's pty — which this backend owns both ends of. So the loader on
// the far host reads ITS stdin, which is the ssh client's stdin, which is
// this pty; and the tokens it emits come back the same way. There is no other
// route: the frames must reach the process that becomes the shell, and only
// the pty reaches it (design §5.1 — a separate channel fails on ancestry).
//
// # What the window is and is not
//
// It is a COPY, never a diversion. Every byte still reaches the renderer, in
// order, unchanged: the tap is fed from the same read the output handler is
// fed from, and it takes nothing. Nothing here parses VT, OSC or DCS; the
// caller matches whole framed lines and nothing else.
//
// # Two intervals, not one
//
// Reading and quarantining OPEN AT DIFFERENT EVENTS, and conflating them
// would break the thing §4.3 promises to keep working. The reader opens
// first, before the line is even submitted, because the loader's readiness
// token must not be missed. The QUARANTINE opens later, at mux ownership
// proven — which is after authentication — because on the typed path nocx
// does not send the command, the user's own `ssh` does, and the host-key,
// password and 2FA prompts are the user talking to their own client before
// nocx has interposed at all. A quarantine that opened with the reader would
// refuse the user's password.
//
// While the quarantine is open the USER's keystrokes are REFUSED, not
// buffered — a buffered keystroke is a command the user did not knowingly
// run, executed later, at a prompt they were not looking at. The window's own
// writes are not the user's and go through. Resize and other control events
// are not user bytes and keep working: they never travel through the input
// queue.
//
// Both close together, at the caller's one terminal outcome, and input is
// re-enabled there — never on a readiness token, which says only that the far
// side is listening.

// ErrBootstrapDeadline is what ReadLine returns when the deadline passed
// before a line completed. Everything read so far stays where the next read
// will find it: a deadline may never consume the far side's bytes.
var ErrBootstrapDeadline = errors.New("session: bootstrap deadline")

// maxWindowBuffer bounds what the tap holds. The window is short and its
// vocabulary is a handful of short lines; a tap with no bound would
// accumulate a far side's whole output looking for a token. On overflow the
// OLDEST bytes go, because the token being waited for is always the newest.
const maxWindowBuffer = 64 * 1024

// maxWindowLine bounds one line, for the same reason internal/ssh bounds one:
// a binary stream has no newline in it and a reader looking for one would
// grow without end.
const maxWindowLine = 4096

// BootstrapWindow is the session's streams as a bootstrap driver sees them.
// It is the same shape internal/ssh declares for a remote session, because it
// is the same conversation over a different transport.
type BootstrapWindow interface {
	// ReadLine returns the next line of the session's output with its line
	// ending removed, or ErrBootstrapDeadline. A deadline consumes nothing.
	ReadLine(ctx context.Context, timeout time.Duration) (string, error)
	// Write writes to the session's input, bypassing the quarantine — which
	// exists to refuse the USER's bytes, not ours.
	Write(p []byte) (int, error)
	// QuarantineInput opens the input side of the interval (design §5.3).
	// It is called at ownership proof and not before: everything the user
	// types up to that point is theirs, addressed to their own ssh client.
	QuarantineInput()
	// Close ends the interval: the tap is removed and input is re-enabled.
	// Idempotent — the interval closes once, whatever runs it.
	Close() error
}

// outputTap is the read half: a bounded buffer the read pump copies into,
// plus a signal so a reader waits on an arrival rather than polling.
type outputTap struct {
	mu   sync.Mutex
	buf  []byte
	sig  chan struct{}
	over bool
}

func newOutputTap() *outputTap {
	return &outputTap{sig: make(chan struct{}, 1)}
}

// feed copies bytes into the tap. It never blocks the read pump and never
// takes a byte away from it: the renderer is fed from the same call.
func (t *outputTap) feed(p []byte) {
	t.mu.Lock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > maxWindowBuffer {
		t.buf = t.buf[len(t.buf)-maxWindowBuffer:]
	}
	t.mu.Unlock()
	select {
	case t.sig <- struct{}{}:
	default:
	}
}

// close ends the tap. A reader waiting on it wakes and reports the end.
func (t *outputTap) close() {
	t.mu.Lock()
	t.over = true
	t.mu.Unlock()
	select {
	case t.sig <- struct{}{}:
	default:
	}
}

// takeLine returns the next complete line, or false when there is not one
// yet. Everything short of a newline stays in the buffer.
func (t *outputTap) takeLine() (string, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i := bytes.IndexByte(t.buf, '\n'); i >= 0 {
		line := string(bytes.TrimRight(t.buf[:i], "\r"))
		t.buf = t.buf[i+1:]
		return line, true, nil
	}
	if len(t.buf) > maxWindowLine {
		// Not our protocol. The bytes are the renderer's and it already
		// has them; what this drops is only the tap's copy.
		t.buf = t.buf[:0]
		return "", false, errors.New("session: bootstrap line exceeds the bound")
	}
	if t.over {
		return "", false, io.EOF
	}
	return "", false, nil
}

// bootstrapWindow is one open interval.
type bootstrapWindow struct {
	sess *realSession
	tap  *outputTap
	once sync.Once
}

// OpenBootstrapWindow opens the interval. It fails if one is already open:
// two owners of one stream is the defect, whichever wins by evaluation order.
func (s *realSession) OpenBootstrapWindow() (BootstrapWindow, error) {
	s.windowMu.Lock()
	defer s.windowMu.Unlock()
	if s.window != nil {
		return nil, fmt.Errorf("session %s already has an open bootstrap window", s.id)
	}
	tap := newOutputTap()
	s.window = tap
	return &bootstrapWindow{sess: s, tap: tap}, nil
}

func (w *bootstrapWindow) ReadLine(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.After(timeout)
	for {
		line, ok, err := w.tap.takeLine()
		if err != nil {
			return "", err
		}
		if ok {
			return line, nil
		}
		select {
		case <-w.tap.sig:
		case <-deadline:
			return "", ErrBootstrapDeadline
		case <-ctx.Done():
			return "", ctx.Err()
		case <-w.sess.ch.Done():
			return "", io.EOF
		}
	}
}

// QuarantineInput closes the user's input path for the rest of the interval.
func (w *bootstrapWindow) QuarantineInput() {
	w.sess.windowMu.Lock()
	defer w.sess.windowMu.Unlock()
	if w.sess.window == w.tap {
		w.sess.inputQuarantined = true
	}
}

// Write goes to the pty directly rather than through the input queue: the
// queue is the USER's path and is exactly what the quarantine closes.
func (w *bootstrapWindow) Write(p []byte) (int, error) { return w.sess.Write(p) }

func (w *bootstrapWindow) Close() error {
	w.once.Do(func() {
		w.sess.windowMu.Lock()
		if w.sess.window == w.tap {
			w.sess.window = nil
			w.sess.inputQuarantined = false
		}
		w.sess.windowMu.Unlock()
		w.tap.close()
	})
	return nil
}

// tapOutput hands a copy of the session's output to an open window. Called
// from the read pump, before the renderer's handler, and it takes nothing.
func (s *realSession) tapOutput(p []byte) {
	s.windowMu.Lock()
	tap := s.window
	s.windowMu.Unlock()
	if tap != nil {
		tap.feed(p)
	}
}

// inputRefused reports whether the user's bytes are currently quarantined.
func (s *realSession) inputRefused() bool {
	s.windowMu.Lock()
	defer s.windowMu.Unlock()
	return s.inputQuarantined
}
