package app

import (
	"context"
	"sync"
	"syscall"
	"testing"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/procwatch"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/shellintegration"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// The composition-root half of nocx-cgzc. The observer itself is proven in
// internal/procwatch and the notification in internal/transport; what is left
// — and what AGENTS.md check 5 is about — is that the two are actually joined
// in the product, on a process the product actually started.

type recordedWatch struct {
	pid      int
	expected string
	sink     procwatch.Sink
}

type fakeWatcher struct {
	mu      sync.Mutex
	watches []recordedWatch
	stopped int
	closed  bool
	err     error
}

func (w *fakeWatcher) Started(pid int, expected string, sink procwatch.Sink) (func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return func() {}, w.err
	}
	w.watches = append(w.watches, recordedWatch{pid: pid, expected: expected, sink: sink})
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.stopped++
	}, nil
}

func (w *fakeWatcher) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func (w *fakeWatcher) only(t *testing.T) recordedWatch {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.watches) != 1 {
		t.Fatalf("watches = %+v, want exactly one", w.watches)
	}
	return w.watches[0]
}

func (w *fakeWatcher) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.watches)
}

func (w *fakeWatcher) stops() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopped
}

// An enhanced local session is watched, and it is watched with the two facts
// the observer needs to answer at all: the pid of the process THIS factory
// forked (the only thing that knows it), and the executable it was started
// as, so "something else is running there" is a comparison and not a guess.
func TestLocalEnhancedSessionIsWatchedForReplacement(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	bash := requireShellBinary(t, "bash")
	w := &fakeWatcher{}
	var (
		mu     sync.Mutex
		seen   []string
		called int
	)
	ptf := &localPTYFactory{
		log:    logger,
		shint:  shellintegration.New(logger),
		kernel: newIntegrationKernel(),
		shells: fixedShell{path: bash},
		procs:  w,
		reportShellReplaced: func(sid, observed string) {
			mu.Lock()
			defer mu.Unlock()
			called++
			seen = append(seen, sid+"/"+observed)
		},
	}
	p, err := ptf.NewPTY(context.Background(), pty.Config{
		SessionID: "0123456789abcdef0123456789abcdef",
		Enhanced:  true, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("NewPTY: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })

	got := w.only(t)
	// Against the INJECTED path, which is what makes this an assertion rather
	// than a tautology: the factory is told to start this bash and must
	// register that same absolute path as the executable it expects.
	if got.expected != bash {
		t.Errorf("expected executable = %q, want %q — the binary the factory exec'd", got.expected, bash)
	}
	// The pid must be a live process, not a zero the observer would silently
	// refuse: signal 0 is the portable "does this process exist".
	if got.pid <= 0 {
		t.Fatalf("pid = %d, want the shell this factory started", got.pid)
	}
	if err := syscall.Kill(got.pid, 0); err != nil {
		t.Errorf("pid %d is not a live process: %v", got.pid, err)
	}

	// The sink is the product's, not a nil the factory forgot to fill: an
	// observation has to arrive at the session's integration axis, which is
	// the only place the user can read it.
	got.sink(procwatch.Observation{PID: got.pid, Name: "kiro-cli-term"})
	mu.Lock()
	defer mu.Unlock()
	if called != 1 || seen[0] != "0123456789abcdef0123456789abcdef/kiro-cli-term" {
		t.Errorf("reported %v (%d times), want the session id and the observed name", seen, called)
	}
}

// The watch ends with the session. A tab that closes must not leave a
// registration behind for a pid the OS is free to reuse.
func TestClosingAnEnhancedSessionEndsItsWatch(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	bash := requireShellBinary(t, "bash")
	w := &fakeWatcher{}
	ptf := &localPTYFactory{
		log:                 logger,
		shint:               shellintegration.New(logger),
		kernel:              newIntegrationKernel(),
		shells:              fixedShell{path: bash},
		procs:               w,
		reportShellReplaced: func(string, string) {},
	}
	p, err := ptf.NewPTY(context.Background(), pty.Config{
		SessionID: "0123456789abcdef0123456789abcdef",
		Enhanced:  true, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("NewPTY: %v", err)
	}
	if w.stops() != 0 {
		t.Fatalf("the watch was stopped before the session ended")
	}
	_ = p.Close()
	if w.stops() != 1 {
		t.Errorf("stops = %d, want the watch released with the session", w.stops())
	}
}

// A session with no handshake to shorten is not watched at all. There is
// nothing for an observation to bring forward — the product has already said
// what this session is — and a watch that can only produce noise is noise.
func TestASessionWithoutAHandshakeIsNotWatched(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	bash := requireShellBinary(t, "bash")
	w := &fakeWatcher{}
	ptf := &localPTYFactory{
		log:                 logger,
		shint:               shellintegration.New(logger),
		kernel:              newIntegrationKernel(),
		shells:              fixedShell{path: bash},
		procs:               w,
		reportShellReplaced: func(string, string) {},
	}
	p, err := ptf.NewPTY(context.Background(), pty.Config{
		SessionID: "0123456789abcdef0123456789abcdef", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("NewPTY: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	if w.count() != 0 {
		t.Errorf("watches = %d, want none for a session that requested no integration", w.count())
	}
}

// A watcher that refuses the watch — the ordinary answer on a platform with
// no NOTE_EXEC — must not break the session. The handshake bound is still
// the detector it always was, so the tab opens exactly as before.
func TestAPlatformThatCannotObserveStillOpensTheSession(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	bash := requireShellBinary(t, "bash")
	w := &fakeWatcher{err: procwatch.ErrUnsupported}
	ptf := &localPTYFactory{
		log:                 logger,
		shint:               shellintegration.New(logger),
		kernel:              newIntegrationKernel(),
		shells:              fixedShell{path: bash},
		procs:               w,
		reportShellReplaced: func(string, string) {},
	}
	p, err := ptf.NewPTY(context.Background(), pty.Config{
		SessionID: "0123456789abcdef0123456789abcdef",
		Enhanced:  true, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("NewPTY on a platform without process observation: %v", err)
	}
	if _, ok := p.(*lifecyclePTY); !ok {
		t.Errorf("enhanced pty is %T, want a lifecycle session regardless of the observer", p)
	}
	_ = p.Close()
}

// The reachability check AGENTS.md names: the observer is constructed and
// injected by the composition root, not merely written. Before this, the
// whole detector would have been reachable from its own tests and nowhere
// else.
func TestProcessObservationIsWiredAtTheCompositionRoot(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	defer a.Shutdown(context.Background())
	f, ok := a.Pty.(*localPTYFactory)
	if !ok {
		t.Fatalf("Pty factory is %T, want *localPTYFactory", a.Pty)
	}
	if f.procs == nil {
		t.Error("no process observer was constructed at the composition root")
	}
	if f.reportShellReplaced == nil {
		t.Error("an observation has nowhere to go: the transport seam was not wired")
	}
}
