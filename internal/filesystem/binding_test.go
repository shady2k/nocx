package filesystem

import (
	"context"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/session"
)

// stubProvider records how the binding drives its provider. watchErrs makes
// individual paths fail to establish, which is what the atomic-swap test
// needs; gates block establishment for one path until the test releases it.
type stubProvider struct {
	mu            sync.Mutex
	watchErrs     map[string]error
	watchMode     WatchMode             // mode given to every watch this provider creates
	watchCloseErr error                 // close error given to every watch it creates
	watches       map[string]*stubWatch // path → the live watch object
	watchOrder    []string
	gates         map[string]*watchGate // path → establishment gate, when set
	closed        atomic.Bool
}

func newStubProvider() *stubProvider {
	return &stubProvider{watches: make(map[string]*stubWatch)}
}

func (s *stubProvider) Root(ctx context.Context) (Root, error) { return Root{Path: "/"}, nil }
func (s *stubProvider) List(ctx context.Context, path string, page Page) (Listing, error) {
	return Listing{Path: path, Entries: []Entry{}}, nil
}

func (s *stubProvider) Read(ctx context.Context, path string, maxBytes int64) (Content, error) {
	return Content{Path: path}, nil
}

func (s *stubProvider) Canonical(ctx context.Context, path string) (string, error) { return path, nil }

func (s *stubProvider) Watch(ctx context.Context, path string) (Watch, error) {
	s.mu.Lock()
	if err := s.watchErrs[path]; err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if g := s.gates[path]; g != nil {
		s.mu.Unlock()
		close(g.entered)
		<-g.release
		s.mu.Lock()
	}
	w := &stubWatch{mode: s.watchMode, closeErr: s.watchCloseErr}
	s.watches[path] = w
	s.watchOrder = append(s.watchOrder, path)
	s.mu.Unlock()
	return w, nil
}
func (s *stubProvider) Close() error { s.closed.Store(true); return nil }

// watchOf returns the watch object last created for a path.
func (s *stubProvider) watchOf(path string) *stubWatch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watches[path]
}

// blockWatch makes the next Watch for path p block until the returned gate's
// release channel is closed — the test's way of holding a Watch call in
// flight mid-establishment.
func (s *stubProvider) blockWatch(p string) *watchGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gates == nil {
		s.gates = make(map[string]*watchGate)
	}
	g := &watchGate{entered: make(chan struct{}), release: make(chan struct{})}
	s.gates[p] = g
	return g
}

type watchGate struct {
	entered chan struct{} // closed when Watch reaches the gate
	release chan struct{} // closed by the test to let Watch through
}

type stubWatch struct {
	mode     WatchMode
	closeErr error // returned by Close; nil in the ordinary case
	closed   atomic.Bool
}

func (w *stubWatch) Events() <-chan struct{} { return nil }
func (w *stubWatch) Mode() WatchMode         { return w.mode }
func (w *stubWatch) Close() error {
	w.closed.Store(true)
	return w.closeErr
}

// fakeCaller is a Caller that owns exactly the sessions it is told about.
type fakeCaller struct {
	owns map[session.ID]bool
}

func (f fakeCaller) Owns(sid session.ID) bool { return f.owns[sid] }

func owner(sids ...session.ID) fakeCaller {
	m := make(map[session.ID]bool, len(sids))
	for _, s := range sids {
		m[s] = true
	}
	return fakeCaller{owns: m}
}

func TestRegisterMintsUnguessableIDs(t *testing.T) {
	reg := New()
	id1, err := reg.Register(newStubProvider(), "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := reg.Register(newStubProvider(), "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatal("two registers minted the same id")
	}
	if len(id1) != 32 {
		t.Fatalf("id %q not 32 hex chars", id1)
	}
	if _, err := hex.DecodeString(id1); err != nil {
		t.Fatalf("id %q not hex: %v", id1, err)
	}
	if _, err := reg.Register(nil, "s1", "", Capabilities{}); err == nil {
		t.Fatal("Register accepted a nil provider")
	}
}

func TestAcquireUnknownBinding(t *testing.T) {
	reg := New()
	var ub *ErrUnknownBinding
	if _, _, err := reg.Acquire("no-such-id", owner("s1")); !errors.As(err, &ub) {
		t.Fatalf("Acquire on unknown id = %v, want ErrUnknownBinding", err)
	}
}

func TestAcquireRequiresOwnership(t *testing.T) {
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	// The binding exists and the session is real; the caller just does not
	// own it — the D15 shape: B knows A's valid session id, and must not be
	// able to use A's filesystem.
	var no *ErrNotOwned
	if _, _, acqErr := reg.Acquire(id, owner("s2")); !errors.As(acqErr, &no) {
		t.Fatalf("Acquire by non-owner = %v, want ErrNotOwned", acqErr)
	}
	// Nil caller owns nothing.
	if _, _, acqErr := reg.Acquire(id, nil); !errors.As(acqErr, &no) {
		t.Fatalf("Acquire with nil caller = %v, want ErrNotOwned", acqErr)
	}
	// The owner succeeds.
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := h.Root(context.Background())
	if err != nil || root.Path != "/" {
		t.Fatalf("owner's Root() = %+v, %v", root, err)
	}
	release()
}

func TestBindingIdentityAccessors(t *testing.T) {
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "v1:abc", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	hh, ok := h.(*handle)
	if !ok {
		t.Fatalf("Acquire returned %T, want *handle", h)
	}
	b := hh.b // the test is in-package; accessors are the public surface
	if b.ID() != id {
		t.Errorf("ID() = %q, want %q", b.ID(), id)
	}
	if b.EndpointID() != "v1:abc" {
		t.Errorf("EndpointID() = %q, want v1:abc", b.EndpointID())
	}
}

// TestHandleValidUntilReleaseInvalidAfter pins the interval with both ends:
// from Acquire until release every method works; after release every method
// errors with ErrHandleReleased.
func TestHandleValidUntilReleaseInvalidAfter(t *testing.T) {
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// First end of the interval: valid.
	if _, err := h.Root(ctx); err != nil {
		t.Fatalf("Root before release: %v", err)
	}
	if _, err := h.List(ctx, "/", Page{Offset: 0, Limit: 10}); err != nil {
		t.Fatalf("List before release: %v", err)
	}
	if _, err := h.Read(ctx, "/f", 0); err != nil {
		t.Fatalf("Read before release: %v", err)
	}
	if _, err := h.Watch(ctx, []string{"/"}); err != nil {
		t.Fatalf("Watch before release: %v", err)
	}
	release()
	// Second end of the interval: every method errors.
	var hr *ErrHandleReleased
	if _, err := h.Root(ctx); !errors.As(err, &hr) {
		t.Errorf("Root after release = %v, want ErrHandleReleased", err)
	}
	if _, err := h.List(ctx, "/", Page{Offset: 0, Limit: 10}); !errors.As(err, &hr) {
		t.Errorf("List after release = %v, want ErrHandleReleased", err)
	}
	if _, err := h.Read(ctx, "/f", 0); !errors.As(err, &hr) {
		t.Errorf("Read after release = %v, want ErrHandleReleased", err)
	}
	if _, err := h.Watch(ctx, []string{"/"}); !errors.As(err, &hr) {
		t.Errorf("Watch after release = %v, want ErrHandleReleased", err)
	}
	// The provider was never reached after release: the stub is still open.
	// (Nothing further to assert — the errors above are the proof.)
}

func TestReleaseIsIdempotent(t *testing.T) {
	reg := New()
	id, err := reg.Register(newStubProvider(), "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	release()
	release() // must not panic, double-decrement, or resurrect the handle
	var hr *ErrHandleReleased
	if _, err := h.Root(context.Background()); !errors.As(err, &hr) {
		t.Fatalf("Root after double release = %v, want ErrHandleReleased", err)
	}
}

// TestCloseWaitsForTheUseGuard pins "close waits on it": a Close issued while
// a guard is held does not return until release, and returns promptly after.
func TestCloseWaitsForTheUseGuard(t *testing.T) {
	reg := New()
	p := newStubProvider()
	id, err := reg.Register(p, "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	_, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- reg.Close(id) }()
	select {
	case err := <-done:
		t.Fatalf("Close returned while a guard was held: %v", err)
	case <-time.After(100 * time.Millisecond):
		// still waiting — correct
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close after release: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the guard was released")
	}
	if !p.closed.Load() {
		t.Error("provider not closed after binding Close")
	}
	// The binding is gone: a second Close and a new Acquire both refuse.
	var ub *ErrUnknownBinding
	if err := reg.Close(id); !errors.As(err, &ub) {
		t.Errorf("second Close = %v, want ErrUnknownBinding", err)
	}
	if _, _, err := reg.Acquire(id, owner("s1")); !errors.As(err, &ub) {
		t.Errorf("Acquire after Close = %v, want ErrUnknownBinding", err)
	}
}

func TestCloseSessionClosesOnlyThatSessionsBindings(t *testing.T) {
	reg := New()
	pA := newStubProvider()
	pB := newStubProvider()
	pC := newStubProvider()
	idA, err := reg.Register(pA, "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	idB, err := reg.Register(pB, "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	idC, err := reg.Register(pC, "s2", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	reg.CloseSession("s1")
	var ub *ErrUnknownBinding
	if _, _, err := reg.Acquire(idA, owner("s1")); !errors.As(err, &ub) {
		t.Errorf("Acquire on closed session's binding A = %v, want ErrUnknownBinding", err)
	}
	if _, _, err := reg.Acquire(idB, owner("s1")); !errors.As(err, &ub) {
		t.Errorf("Acquire on closed session's binding B = %v, want ErrUnknownBinding", err)
	}
	if !pA.closed.Load() || !pB.closed.Load() {
		t.Error("session's providers not closed")
	}
	if pC.closed.Load() {
		t.Error("other session's provider was closed")
	}
	if h, release, err := reg.Acquire(idC, owner("s2")); err != nil {
		t.Errorf("Acquire on other session's binding = %v, want success", err)
	} else {
		release()
		_ = h
	}
}

// TestWatchSwapIsAtomicAndReplaces pins spec §5.2: Watch replaces the set,
// closes the replaced watches, and a failure establishing any new path leaves
// the existing set healthy.
func TestWatchSwapIsAtomicAndReplaces(t *testing.T) {
	reg := New()
	p := newStubProvider()
	id, err := reg.Register(p, "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx := context.Background()

	mode, err := h.Watch(ctx, []string{"/a", "/a", "/b"}) // duplicates collapse
	if err != nil {
		t.Fatal(err)
	}
	if mode.Kind != WatchLive {
		t.Fatalf("mode = %+v, want live", mode)
	}
	if got := p.watchOrder; len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Fatalf("watchOrder = %v, want [/a /b] (deduplicated)", got)
	}
	wa := p.watchOf("/a")

	// Replacement: the old set is closed, the new one is live.
	mode, err = h.Watch(ctx, []string{"/c"})
	if err != nil {
		t.Fatal(err)
	}
	if mode.Kind != WatchLive {
		t.Fatalf("mode = %+v, want live", mode)
	}
	if !wa.closed.Load() {
		t.Error("replaced watch /a was not closed")
	}
	if wc := p.watchOf("/c"); wc.closed.Load() {
		t.Error("new watch /c was closed immediately")
	}

	// Failure: establishing /d fails; the existing set stays healthy, the
	// partially established /c (a second establishment) is closed, and the
	// error is the provider's own.
	boom := errors.New("watch refused")
	p.mu.Lock()
	p.watchErrs = map[string]error{"/d": boom}
	p.mu.Unlock()
	oldC := p.watchOf("/c")
	_, err = h.Watch(ctx, []string{"/c", "/d"})
	if !errors.Is(err, boom) {
		t.Fatalf("Watch failure = %v, want the provider's error", err)
	}
	if oldC.closed.Load() {
		t.Error("existing watch was taken down by a failed swap")
	}
	if wc2 := p.watchOf("/c"); !wc2.closed.Load() {
		t.Error("partially established watch was leaked")
	}

	// An empty set closes everything.
	mode, err = h.Watch(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !oldC.closed.Load() {
		t.Error("watch /c not closed by empty replacement")
	}
}

func TestWatchModeAggregation(t *testing.T) {
	reg := New()
	p := newStubProvider()
	id, err := reg.Register(p, "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// A degraded watch reports the persistent "Polling" state.
	p.mu.Lock()
	p.watchErrs = nil
	p.watchMode = WatchMode{Kind: WatchPolling, DegradedReason: "fsnotify unavailable"}
	p.mu.Unlock()
	mode, err := h.Watch(context.Background(), []string{"/x"})
	if err != nil {
		t.Fatal(err)
	}
	if mode.Kind != WatchPolling || mode.DegradedReason != "fsnotify unavailable" {
		t.Fatalf("mode = %+v, want polling with the degradation reason", mode)
	}
	// Designed polling (SFTP) warns about nothing.
	p.mu.Lock()
	p.watchMode = WatchMode{Kind: WatchPolling}
	p.mu.Unlock()
	mode, err = h.Watch(context.Background(), []string{"/x"})
	if err != nil {
		t.Fatal(err)
	}
	if mode.Kind != WatchPolling || mode.DegradedReason != "" {
		t.Fatalf("mode = %+v, want polling with no reason", mode)
	}
}

// TestRegistryConcurrentUse is the race-detector exercise: acquires,
// releases and a close all at once must not corrupt the registry.
func TestRegistryConcurrentUse(t *testing.T) {
	reg := New()
	ids := make([]string, 8)
	for i := range ids {
		id, err := reg.Register(newStubProvider(), session.ID("s1"), "", Capabilities{})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = id
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h, release, err := reg.Acquire(ids[i%len(ids)], owner("s1"))
			if err != nil {
				return // closed underneath us is fine
			}
			defer release()
			var hr *ErrHandleReleased
			if _, err := h.Root(ctx); err != nil && !errors.As(err, &hr) {
				t.Errorf("Root: %v", err)
			}
		}(i)
	}
	time.Sleep(2 * time.Millisecond) // let some acquires land before the close
	reg.CloseSession("s1")
	wg.Wait()
}

// blockingProvider's Root blocks until the test releases it, and records
// whether the provider was closed while it was blocked. closed is a plain
// bool on purpose: with the old code Close ran while Root was still blocked,
// which is a data race the detector must also see.
type blockingProvider struct {
	stubProvider
	entered chan struct{} // closed when Root is blocked inside the provider
	unblock chan struct{} // closed by the test to let Root return
	closed  bool
}

func newBlockingProvider() *blockingProvider {
	return &blockingProvider{entered: make(chan struct{}), unblock: make(chan struct{})}
}

func (p *blockingProvider) Root(ctx context.Context) (Root, error) {
	close(p.entered)
	<-p.unblock
	if p.closed {
		return Root{}, errors.New("provider closed while Root was blocked")
	}
	return Root{Path: "/"}, nil
}

func (p *blockingProvider) Close() error {
	p.closed = true
	return nil
}

// TestUseGuardHoldsForTheCallsDuration opens the window the old code left
// between a handle's validity check and its provider call: a provider method
// blocked mid-call, release called while it is blocked, close racing both.
// Close must not reach the provider until the method returned — the guard is
// held for the call's duration, not just for the handle's lifetime.
func TestUseGuardHoldsForTheCallsDuration(t *testing.T) {
	reg := New()
	p := newBlockingProvider()
	id, err := reg.Register(p, "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	rootDone := make(chan struct{})
	go func() {
		defer close(rootDone)
		if _, err := h.Root(context.Background()); err != nil {
			t.Errorf("Root: %v", err)
		}
	}()
	<-p.entered // the provider call is in flight
	closeDone := make(chan error, 1)
	go func() { closeDone <- reg.Close(id) }()
	release() // the handle's own guard is gone; the call's guard must survive
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while Root was in flight: %v", err)
	case <-time.After(100 * time.Millisecond):
		// still waiting — correct: the in-flight call holds the guard
	}
	close(p.unblock)
	<-rootDone
	if err := <-closeDone; err != nil {
		t.Fatalf("Close after Root returned: %v", err)
	}
}

// TestCloseWaitsForTheGuardBeforeTearingDownWatches pins the close ordering:
// the guard drains before watches are closed. A Watch call in flight when
// Close begins completes its swap (installing the fresh set) before any
// teardown, the teardown then closes that set, and a new Watch call once
// close has begun refuses with ErrHandleReleased — nothing is closed
// underneath a live handle and nothing is installed after the cleanup and
// leaked.
func TestCloseWaitsForTheGuardBeforeTearingDownWatches(t *testing.T) {
	reg := New()
	p := newStubProvider()
	id, err := reg.Register(p, "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := h.Watch(ctx, []string{"/a"}); err != nil {
		t.Fatal(err)
	}
	wa := p.watchOf("/a")
	gate := p.blockWatch("/b")
	swapDone := make(chan struct{})
	go func() {
		defer close(swapDone)
		if _, err := h.Watch(ctx, []string{"/b"}); err != nil {
			t.Errorf("in-flight Watch: %v", err)
		}
	}()
	<-gate.entered // the swap is blocked mid-establishment
	closeDone := make(chan error, 1)
	go func() { closeDone <- reg.Close(id) }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while a guard was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if wa.closed.Load() {
		t.Fatal("watch closed while Close was waiting on the guard")
	}
	// A new call after close began refuses — nothing can install a watch
	// after the teardown. (The in-flight swap holds watchMu, so on the old
	// code this call would block there and then install a leaked watch.)
	late := make(chan error, 1)
	go func() {
		_, err := h.Watch(ctx, []string{"/c"})
		late <- err
	}()
	select {
	case err := <-late:
		var hr *ErrHandleReleased
		if !errors.As(err, &hr) {
			t.Fatalf("Watch after Close began = %v, want ErrHandleReleased", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch after Close began hung")
	}
	close(gate.release) // the in-flight swap completes and installs /b
	<-swapDone
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before the guard drained: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	wb := p.watchOf("/b")
	if wb.closed.Load() {
		t.Fatal("fresh watch closed before Close drained the guard")
	}
	release()
	if err := <-closeDone; err != nil {
		t.Fatalf("Close after release: %v", err)
	}
	if !wa.closed.Load() || !wb.closed.Load() {
		t.Error("watches not closed by the teardown")
	}
}

// failingCloseProvider is a provider whose Close fails, plus watches that
// fail to close, for the close-error propagation test.
type failingCloseProvider struct {
	*stubProvider
	err error
}

func newFailingCloseProvider(err error) *failingCloseProvider {
	return &failingCloseProvider{stubProvider: newStubProvider(), err: err}
}

func (p *failingCloseProvider) Close() error { return p.err }

// deliberately, so Registry.Close must surface it — and the watch-close
// errors collected along the way. Every external call has a test where it
// fails; this is the failing-close one.
func TestCloseReportsProviderAndWatchErrors(t *testing.T) {
	reg := New()
	perr := errors.New("provider close boom")
	werr := errors.New("watch close boom")
	p := newFailingCloseProvider(perr)
	id, err := reg.Register(p, "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	h, release, err := reg.Acquire(id, owner("s1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, watchErr := h.Watch(context.Background(), []string{"/w"}); watchErr != nil {
		t.Fatal(watchErr)
	}
	p.watchOf("/w").closeErr = werr
	release()
	err = reg.Close(id)
	if !errors.Is(err, perr) {
		t.Fatalf("Close = %v, want the provider's close error", err)
	}
	if !errors.Is(err, werr) {
		t.Fatalf("Close = %v, want the watch's close error", err)
	}
	// The clean path still reports success.
	reg2 := New()
	id2, err := reg2.Register(newStubProvider(), "s1", "", Capabilities{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg2.Close(id2); err != nil {
		t.Fatalf("clean Close = %v, want nil", err)
	}
}

// TestRegisterRejectsTypedNilProvider: `p == nil` catches a nil interface
// but not an interface holding a typed-nil pointer, which registers fine and
// panics on first use or close.
func TestRegisterRejectsTypedNilProvider(t *testing.T) {
	reg := New()
	var p *stubProvider // typed nil: non-nil interface, nil pointer
	if _, err := reg.Register(p, "s1", "", Capabilities{}); err == nil {
		t.Fatal("Register accepted a typed-nil provider")
	}
}
