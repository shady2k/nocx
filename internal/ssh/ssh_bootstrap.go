package ssh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// The session side of the bootstrap: the byte streams it runs on, and the
// input quarantine that owns the terminal while it does.
//
// internal/ssh knows nothing about frames. It owns the transport — a started
// session, its stdin and its stdout — and hands both to whoever does know,
// through the interfaces below. The frame protocol, the deadlines and every
// outcome name live in internal/shellintegration, and the composition root
// adapts the two declarations exactly as it does for the launcher.

// ErrBootstrapDeadline is what ReadLine returns when the deadline passed
// before a line completed. It is declared here as well as in
// internal/shellintegration because neither package may import the other; the
// values are compared by the CALLER's Is, so each side matches its own.
var ErrBootstrapDeadline = errors.New("ssh: bootstrap deadline")

// ErrInputQuarantined is returned by Write while the session is
// bootstrapping. A keystroke in that window is REFUSED, not buffered: a
// buffered keystroke is a command the user did not knowingly run, executed
// later, at a prompt they were not looking at (design §5.3).
//
// It is a distinct error rather than a silent short write because the session
// layer logs a failed input write as "the user typed into nothing", and
// telling that apart from a dead channel is the difference between a warning
// worth acting on and one that is expected.
type ErrInputQuarantined struct{}

func (e *ErrInputQuarantined) Error() string {
	return "ssh channel is bootstrapping: input refused"
}

// BootstrapStream is what the bootstrap driver sees of the session: a line
// reader with a deadline, and a writer that bypasses the quarantine.
//
// The deadline is enforced HERE, on this side of the seam, because an
// io.Reader cannot be interrupted — a blocked Read would otherwise hold the
// stream after the driver had given up, and the bytes it eventually consumed
// would be the user's.
type BootstrapStream interface {
	ReadLine(ctx context.Context, timeout time.Duration) (string, error)
	Write(p []byte) (int, error)
}

// BootstrapRun drives one session's bootstrap to a terminal outcome and
// returns why integration did not happen, or ReasonNone when it did.
type BootstrapRun func(ctx context.Context, s BootstrapStream) RefusalReason

// BootstrapGate is the ssh side of design §6.1's ordering: the two facts that
// must both be in before the far side is handed a bearer.
//
// It is declared here, and driven from here, because this package is the one
// that knows both of them — it opens the lifecycle transport and it runs the
// publish. It is CONSUMED on the other side of the seam, where the frame is
// built; the composition root adapts the two declarations exactly as it does
// for BootstrapStream and the launcher.
//
// Why the publish outcome does not cross this interface. §6.1 names four
// terminal outcomes — committed, unchanged, failed and contended — and every
// one of them opens the gate, because "after a failed publish the far side may
// still accept a generation installed earlier, so a failed publish is not a
// refusal". The far side is the owner of "is this installation valid" and
// re-proves it after the frame arrives. So what the gate needs is that the
// attempt SETTLED, and the error is carried only so the failure can be named
// in a diagnosis.
type BootstrapGate interface {
	// ReceiverReady records §6.1 step 4: the lifecycle transport and its
	// receiver are fully ready.
	ReceiverReady()
	// ReceiverUnavailable records that step 4 will never be true, so
	// nothing is minted and the far side is handed a non-secret refusal
	// rather than a bearer it has no channel to use.
	ReceiverUnavailable(err error)
	// PublishSettled records §6.1 step 5: the publish attempt reached a
	// terminal outcome. err is nil when it committed and non-nil otherwise,
	// and either way the gate opens.
	PublishSettled(err error)
}

// ---------------------------------------------------------------------------
// The stream
// ---------------------------------------------------------------------------

// maxBootstrapLine bounds one line of far-side output. The bootstrap
// vocabulary is short; a reader with no bound would accumulate a binary
// stream into memory looking for a newline that is not coming.
const maxBootstrapLine = 4096

// sessionFeed is the session's output, read once and handed to two consumers
// in sequence: the bootstrap driver first, the user's terminal afterwards.
//
// A single goroutine owns the underlying Read, and that is the point. The
// obvious shape — the driver reads directly and the channel takes over
// afterwards — loses bytes on exactly the path that matters: when a deadline
// fires, the driver's Read is still blocked inside the transport, and the line
// it eventually consumes is dropped on the floor. Here the pump keeps reading
// into a channel whatever the driver does, so a deadline abandons a WAIT and
// never a byte; whatever arrived meanwhile is still queued for the terminal.
//
// The two consumers never run at the same time: the channel's Read blocks
// until the bootstrap is finished, and the close of that signal is what
// publishes the pending buffer to it.
type sessionFeed struct {
	chunks chan []byte
	// pending is what a consumer read but did not use. Only one consumer
	// touches it at a time; the handover is ordered by the bootstrap-done
	// channel the RealChannel waits on.
	pending []byte
	// err holds why the pump stopped. Written before chunks is closed, read
	// only after, so the close orders it.
	err error
	// ended is closed by the pump when the session's output stream has
	// ended, and is the race-free way to ask "is the far side gone".
	// Design §6.4's substituted-exec row needs exactly that question
	// answered at the moment the bootstrap gives up, and asking the
	// session's own done channel would be a race between two goroutines
	// with no ordering between them.
	ended chan struct{}
	// after is the deadline source, injected so a test states "the deadline
	// fires" instead of waiting for it (AGENTS.md: no test may depend on
	// timing). Production is time.After.
	after func(time.Duration) <-chan time.Time
}

func newSessionFeed(r io.Reader) *sessionFeed {
	f := &sessionFeed{chunks: make(chan []byte, 8), ended: make(chan struct{}), after: time.After}
	go func() {
		// `ended` closes BEFORE `chunks`, so a consumer unblocked by the
		// end of `chunks` is ordered after it and cannot read a stale
		// "not ended" — the question farSideEnded asks on exactly that
		// wakeup. Closed the other way round the two are concurrent and
		// the answer would be the scheduler's.
		defer close(f.chunks)
		defer close(f.ended)
		for {
			buf := make([]byte, 32*1024)
			n, err := r.Read(buf)
			if n > 0 {
				f.chunks <- buf[:n]
			}
			if err != nil {
				f.err = err
				return
			}
		}
	}()
	return f
}

// Ended reports whether the session's output stream has ended. The pump
// closes `ended` after it has recorded why it stopped and before it closes
// `chunks`, so a true answer is ordered after everything the pump wrote, and
// every consumer the stream's end woke sees it.
func (f *sessionFeed) Ended() bool {
	select {
	case <-f.ended:
		return true
	default:
		return false
	}
}

// farSideEnded answers "is the far side's session over" at the moment the
// bootstrap gave up, and answers it so that the answer does not depend on
// which of two goroutines was scheduled first.
//
// ONE far-side event — the exit status and the channel close that follow a
// substituted `exec` — reaches the bootstrap down two independent chains, and
// the bootstrap gives up on whichever arrives first:
//
//	the pump    the channel read returns io.EOF, the feed's `ended` closes,
//	            and ReadLine returns that error;
//	the watcher session.Wait returns, RealChannel.recordWait records it,
//	            Close closes `done`, the bootstrap context is cancelled, and
//	            ReadLine returns context.Canceled.
//
// Nothing orders those two against each other. Session.Wait does not wait for
// the pump — the session's output is a StdoutPipe, for which x/crypto/ssh
// registers no copy goroutine, so Wait returns on the exit status alone while
// the pump is still blocked in Read. So asking only the feed answered "no"
// whenever the watcher's chain won a race the pump had not already finished,
// and §6.4's sixth row — accepted-and-substituted, which must never be
// collapsed into the recoverable refused row — was reported as ReasonUnknown
// instead.
//
// Each chain leaves its OWN fact behind, ordered before the wakeup it caused:
// the pump closes `ended` before `chunks` (newSessionFeed), and the watcher
// records the wait before Close closes `done` (RealChannel.recordWait, the
// same ordering nocx-ictcq bought for the exit monitor). Whichever chain woke
// the bootstrap, its fact is already visible, so the disjunction is definite
// and both orders give the same answer.
//
// remoteSessionEnded is the watcher's fact — the `ok` of RealChannel.WaitErr —
// and deliberately not the `done` channel: `done` also closes when the tab is
// closed locally, which is not the far side ending and is not this row.
//
// A bootstrap that gave up on its own DEADLINE has neither fact, and false is
// the right answer there: the session is still live, and that is a timeout
// rather than a substitution.
func farSideEnded(feed *sessionFeed, remoteSessionEnded bool) bool {
	return feed.Ended() || remoteSessionEnded
}

// Read implements io.Reader for the terminal side.
func (f *sessionFeed) Read(p []byte) (int, error) {
	if len(f.pending) == 0 {
		chunk, ok := <-f.chunks
		if !ok {
			if f.err != nil {
				return 0, f.err
			}
			return 0, io.EOF
		}
		f.pending = chunk
	}
	n := copy(p, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

// ReadLine returns the next line with its ending removed.
//
// A timeout leaves everything read so far in pending, so the next reader —
// which is the user's terminal — sees it. That is what "a deadline may never
// consume the user's bytes" means in code.
func (f *sessionFeed) ReadLine(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := f.after(timeout)
	var line []byte
	for {
		if i := bytes.IndexByte(f.pending, '\n'); i >= 0 {
			line = append(line, f.pending[:i]...)
			f.pending = f.pending[i+1:]
			return string(bytes.TrimRight(line, "\r")), nil
		}
		line = append(line, f.pending...)
		f.pending = nil
		if len(line) > maxBootstrapLine {
			// Not our protocol. Give the bytes back rather than
			// dropping them: this session is about to become an
			// ordinary terminal and they are its output.
			f.pending = line
			return "", errors.New("ssh: bootstrap line exceeds the bound")
		}
		select {
		case chunk, ok := <-f.chunks:
			if !ok {
				f.pending = line
				if f.err != nil {
					return "", f.err
				}
				return "", io.EOF
			}
			f.pending = chunk
		case <-deadline:
			f.pending = line
			return "", ErrBootstrapDeadline
		case <-ctx.Done():
			f.pending = line
			return "", ctx.Err()
		}
	}
}

// bootstrapStream pairs the feed with the session's stdin. The write side is
// deliberately NOT the channel's Write: the quarantine refuses the user's
// bytes, and these are not the user's.
type bootstrapStream struct {
	*sessionFeed
	w io.Writer
}

func (b bootstrapStream) Write(p []byte) (int, error) { return b.w.Write(p) }

// ---------------------------------------------------------------------------
// The input quarantine (design §5.3)
// ---------------------------------------------------------------------------

// inputGate decides, for each write, whether the user's bytes reach the
// terminal. It opens BEFORE the command is sent and closes at exactly one
// terminal outcome — never at READY, which says only that the far side is
// listening, not that it is finished with the terminal.
//
// The linearisation assertion (design §11 assertion 15) is the whole design
// of this type: one write is one decision taken under the mutex, so a
// keystroke arriving as the outcome lands is either refused or delivered
// exactly once, never both and never neither. The decision is taken under the
// lock and the WRITE happens outside it, deliberately: a channel write can
// block indefinitely (nocx-o2le), and holding the gate across it would let one
// stuck keystroke stop the bootstrap from ever releasing.
type inputGate struct {
	mu   sync.Mutex
	open bool
}

func newInputGate(open bool) *inputGate { return &inputGate{open: open} }

// admit reports whether this write may proceed.
func (g *inputGate) admit() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.open
}

// release opens the gate. Idempotent: the bootstrap has exactly one terminal
// outcome, and a second release would mean two.
func (g *inputGate) release() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open = true
}
