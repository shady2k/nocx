package transport

// The assistant's file binding, on its own (stage 10, ADR-anntnxdj §5).
//
// Two properties are asserted here, and they are the ones that make "the tools
// act on the pane's host" true by construction rather than by review:
//
//   - one binding per CALL, from the same factory and registry the Files panel
//     uses — including when the panel was never opened;
//   - nothing left behind: after N calls, after a cancelled call and after the
//     pane closes mid-call, every provider this route created has been closed.
//
// The last one is observed through the provider's own Close rather than by
// reaching into the registry: a binding whose provider was closed is a binding
// the registry no longer holds (Registry.Close removes it first), and a counter
// of zero names a leak instead of a state somebody has to interpret.

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/filesystem/local"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/waittest"
)

// countedProvider is the local provider plus a close counter. It embeds the
// concrete provider so every optional seam it contributes — the conditional
// write this stage depends on — is still promoted, which a wrapper over the
// Provider interface would hide.
type countedProvider struct {
	*local.Provider
	mu     *sync.Mutex
	closed *int
}

func (p countedProvider) Close() error {
	p.mu.Lock()
	(*p.closed)++
	p.mu.Unlock()
	return p.Provider.Close()
}

// assistantFilesFixture is the real session registry (its PTY is the transport
// tests' fake, so no shell is spawned) plus a real filesystem registry and a
// factory that counts and reports what it was asked for.
type assistantFilesFixture struct {
	files     assistantFiles
	sessions  session.Registry
	sessionID session.ID
	reg       *filesystem.Registry

	mu           sync.Mutex
	factoryCalls int
	closed       int
	rootPaths    []string
}

func newAssistantFilesFixture(t *testing.T) *assistantFilesFixture {
	t.Helper()
	sessions := session.New(log.NewSlogAdapter(nil), &exitFakePTYFactory{fake: newExitFakePTY()})
	sess, err := sessions.Open(context.Background(), session.Config{Kind: session.KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { _ = sessions.Close(sess.ID()) })
	f := &assistantFilesFixture{
		sessions:  sessions,
		sessionID: sess.ID(),
		reg:       filesystem.New(),
	}
	f.files = assistantFiles{
		reg: f.reg,
		factory: func(got session.Session, rootPath string) (filesystem.Provider, error) {
			if got.ID() != sess.ID() {
				t.Errorf("factory asked for session %q, want the run's %q", got.ID(), sess.ID())
			}
			f.mu.Lock()
			f.factoryCalls++
			f.rootPaths = append(f.rootPaths, rootPath)
			f.mu.Unlock()
			return countedProvider{Provider: local.New(), mu: &f.mu, closed: &f.closed}, nil
		},
		sessions:  sessions,
		sessionID: sess.ID(),
	}
	return f
}

func (f *assistantFilesFixture) counts() (factory, closed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.factoryCalls, f.closed
}

// TestAssistantFiles_OneBindingPerCallAndNoneLeftBehind is the property the
// Files panel's registry has to keep for a run: the factory runs once per call,
// with the run's session and no root override, and every provider it built is
// closed by the time the call returns.
func TestAssistantFiles_OneBindingPerCallAndNoneLeftBehind(t *testing.T) {
	f := newAssistantFilesFixture(t)
	const calls = 3
	for i := 0; i < calls; i++ {
		handle, release, err := f.files.Acquire(context.Background())
		if err != nil {
			t.Fatalf("call %d: Acquire: %v", i, err)
		}
		if _, err := handle.Root(context.Background()); err != nil {
			release()
			t.Fatalf("call %d: the handle does not work: %v", i, err)
		}
		release()
	}
	factory, closed := f.counts()
	if factory != calls {
		t.Fatalf("factory calls = %d, want one per call (%d)", factory, calls)
	}
	if closed != calls {
		t.Fatalf("providers closed = %d, want one per call (%d) — a binding outlived its call", closed, calls)
	}
	for i, root := range f.rootPaths {
		if root != "" {
			t.Fatalf("factory rootPath for call %d = %q, want empty: the binding is the run's, not a panel's", i, root)
		}
	}
}

// TestAssistantFiles_CancelledCallLeavesNothing: a call that is already
// cancelled builds no provider at all, so a cancelled tool call cannot leave a
// connection behind.
func TestAssistantFiles_CancelledCallLeavesNothing(t *testing.T) {
	f := newAssistantFilesFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, release, err := f.files.Acquire(ctx); err == nil {
		release()
		t.Fatal("Acquire on a cancelled context succeeded")
	}
	factory, closed := f.counts()
	if factory != 0 || closed != 0 {
		t.Fatalf("factory calls = %d, closed = %d; a cancelled call must build nothing", factory, closed)
	}
}

// TestAssistantFiles_PaneClosingMidCallStillClosesTheProvider: the pane's
// session closes while the call holds its handle. CloseSession marks the binding
// closed, waits for the use-guard, the call releases it, and the provider is
// closed exactly once — whichever of the two paths closes the binding.
func TestAssistantFiles_PaneClosingMidCallStillClosesTheProvider(t *testing.T) {
	f := newAssistantFilesFixture(t)
	handle, release, err := f.files.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := handle.Root(context.Background()); err != nil {
		release()
		t.Fatalf("the handle does not work: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- f.reg.CloseSession(f.sessionID) }()
	// CloseSession marks the binding closed BEFORE it drains the guard, so a call
	// on this handle that refuses with ErrHandleReleased IS that marking having
	// happened — the handle itself was never released, so only the binding's own
	// state can produce it. Waiting for it is what puts the release under a
	// closing binding instead of racing the close for which comes first.
	waittest.WaitFor(t, "CloseSession to mark the binding closed", func() bool {
		_, err := handle.Stat(context.Background(), "/")
		var released *filesystem.ErrHandleReleased
		return errors.As(err, &released)
	})
	// The pane has closed; the call finishes and releases its guard.
	release()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("CloseSession: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CloseSession did not return after the call released its guard")
	}
	_, providers := f.counts()
	if providers != 1 {
		t.Fatalf("providers closed = %d, want exactly 1", providers)
	}
}

// TestAssistantFiles_CancelledWhileTheProviderIsBeingBuiltLeavesNothing: building
// the provider is the expensive half — dialing a host — and a call abandoned
// during it must not leave the binding it already registered for a call that
// will never use it. The factory cancels the turn's own context, so the check
// that follows registration is the one that answers.
func TestAssistantFiles_CancelledWhileTheProviderIsBeingBuiltLeavesNothing(t *testing.T) {
	f := newAssistantFilesFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	files := f.files
	files.factory = func(got session.Session, rootPath string) (filesystem.Provider, error) {
		f.mu.Lock()
		f.factoryCalls++
		f.rootPaths = append(f.rootPaths, rootPath)
		f.mu.Unlock()
		cancel() // the call was abandoned while this provider was being built
		return countedProvider{Provider: local.New(), mu: &f.mu, closed: &f.closed}, nil
	}
	if _, release, err := files.Acquire(ctx); err == nil {
		release()
		t.Fatal("Acquire succeeded on a context its own factory cancelled")
	}
	if factory, closed := f.counts(); factory != 1 || closed != 1 {
		t.Fatalf("factory calls = %d, closed = %d; the abandoned provider must be closed exactly once", factory, closed)
	}
	// And the registry holds nothing for the run: closing the session closes no
	// provider, so the counter stays where the call left it.
	if err := f.reg.CloseSession(f.sessionID); err != nil {
		t.Fatalf("CloseSession after the abandoned call: %v", err)
	}
	if factory, closed := f.counts(); factory != 1 || closed != 1 {
		t.Fatalf("after closing the session: factory calls = %d, closed = %d; a binding was left in the registry", factory, closed)
	}
}

// TestAssistantFiles_ACapabilityReadsThroughTheCallBinding is the path's own
// proof: a files.read capability built over this source reads a real file, so
// the factory, the registry, the acquisition and the conditional read are wired
// to each other rather than merely present.
func TestAssistantFiles_ACapabilityReadsThroughTheCallBinding(t *testing.T) {
	f := newAssistantFilesFixture(t)
	dir := t.TempDir()
	path := dir + "/note.txt"
	if err := os.WriteFile(path, []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	reader := filesystem.NewScopedReaderWithExactFiles(f.files, []string{"/"}, []string{path})
	snapshot, err := reader.ReadSnapshot(context.Background(), path, 64<<10)
	if err != nil {
		t.Fatalf("ReadSnapshot through the assistant's binding: %v", err)
	}
	if snapshot.Text != "1:hello\n" {
		t.Fatalf("snapshot text = %q, want the file's content", snapshot.Text)
	}
	if factory, closed := f.counts(); factory != 1 || closed != 1 {
		t.Fatalf("factory calls = %d, closed = %d; one read is one call and one closed binding", factory, closed)
	}
}
