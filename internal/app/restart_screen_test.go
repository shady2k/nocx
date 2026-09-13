package app

// A session's screen survives the coordinator dying and being replaced
// (nocx-ygxjv.3, ADR-0066). This is the bead's first acceptance criterion and
// the whole reason the runtime moved beside the PTY.
//
// # What is real here, and what that costs
//
// The helper is the SHIPPED daemon — built from cmd/nocx-helper, installed into
// an isolated home and reached over its own endpoint socket, exactly as
// local_pane_test.go's harness does it — and the sessions are real shells under
// real PTYs. The coordinator is the shipped App: two instances over ONE home,
// the first shut down before the second starts. Nothing here is a fake, which
// is why the test is slow and why it is worth it: the failure it exists to
// catch (state that lived only in the coordinator's memory) cannot be produced
// by any in-process double.
//
// # The kill, and why it is a Shutdown rather than a SIGKILL
//
// The property is that the screen is NOT the coordinator's. A second App over
// the same home has none of the first one's memory, reaches the same daemon
// through the same endpoint, and asks that daemon's runtime for the frame — so
// a marker that appears now can only have come from the helper. What a SIGKILL
// would add is proof that the first instance released nothing politely; that is
// a different property (the runtime's own lifetime) and it is asserted where it
// belongs, in internal/helper/session, where the runtime is created and
// destroyed.
//
// # No timing
//
// Every wait is on an observable: the marker in a frame read, or the reattach
// having published (polled through the same product API the surface uses).

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
)

// TestASessionsScreenOutlivesTheCoordinatorThatOpenedIt is the acceptance
// criterion: kill the coordinator, start another, read the screen.
func TestASessionsScreenOutlivesTheCoordinatorThatOpenedIt(t *testing.T) {
	// The helper is built BEFORE the home moves: `go build` resolves GOPATH
	// from HOME, and a build under the disposable one fills it with a module
	// cache the test then cannot remove (local_pane_test.go's own note).
	src := realHelperArtifacts(t)
	home := storagetest.IsolateWithHome(t)
	t.Cleanup(func() { endTheDaemon(t, filepath.Join(helperRoot(home, src.hash()), "nocx-helper")) })

	first := bootLocalAppOn(t, src)
	opened, err := first.Transport.OpenSession(context.Background(), transport.OpenSpec{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("opening a local pane through the shipped opener: %v", err)
	}
	sid := opened.Session.ID()

	// The program draws, and the marker is waited for THROUGH THE PRODUCT'S
	// OWN READ — the store the observation, the emitting view and the typing
	// gate all read — so what is asserted afterwards is a screen the product
	// really produced and not a byte string the test wrote.
	const marker = "SCREEN-SURVIVES"
	if _, err := opened.Session.Write([]byte("printf '\\033[2J\\033[3;5H" + marker + "\\n'\n")); err != nil {
		t.Fatalf("running a marker-drawing program: %v", err)
	}
	if err := first.paneViews.Enrol(string(sid)); err != nil {
		t.Fatalf("watching the pane: %v", err)
	}
	before := waitForMarker(t, first, sid, marker)

	// THE COORDINATOR DIES. Its sessions, its store and its watches go with it;
	// the daemon holding the PTY does not, and neither does the runtime beside
	// it.
	first.Shutdown(context.Background())

	second := bootLocalAppOn(t, src)
	// The pane exists again in the coordinator's registry — through the shipped
	// re-adoption pass, which attaches to the EXISTING host session rather than
	// spawning a second shell (session_readopt.go).
	waitForFreshCoordinator(t, second, sid)

	// The same watch is opened over the same session, in the NEW process.
	if err := second.paneViews.Enrol(string(sid)); err != nil {
		t.Fatalf("watching the pane from the replacing coordinator: %v", err)
	}
	after := waitForMarker(t, second, sid, marker)

	if got := after.Text(2); !strings.Contains(got, marker) {
		t.Fatalf("the replaced coordinator reads row 3 as %q, want the marker the FIRST one's program drew", got)
	}
	if after.Cols != before.Cols || after.Rows != before.Rows {
		t.Errorf("the screen came back at %dx%d, want the %dx%d the session was opened at",
			after.Cols, after.Rows, before.Cols, before.Rows)
	}
}

// bootLocalAppOn boots the shipped composition root and stops it when the test
// ends.
//
// It takes no home, and that is not an omission: the disposable home is set on
// the PROCESS by storagetest.IsolateWithHome, so every App this test starts
// resolves the same HOME, the same app directory and the same helper endpoint —
// which is exactly what "the coordinator was replaced" means here.
func bootLocalAppOn(t *testing.T, src fakeArtifacts) *App {
	t.Helper()
	a, err := newTestApp(t, withLocalHelperArtifacts(src))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		a.Shutdown(context.Background())
		cancel()
	})
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return a
}

// waitForMarker reads the pane's frame until the marker is on it. It goes
// through the app's own store, so the read that succeeds is the product's.
func waitForMarker(t *testing.T, a *App, sid session.ID, marker string) paneview.Frame {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for {
		f, err := a.paneViews.Frame(string(sid))
		if err == nil {
			if line := f.Text(2); strings.Contains(line, marker) {
				return f
			}
		} else {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the marker %q never reached the screen read (last error: %v)", marker, lastErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForFreshCoordinator waits until the replacing coordinator holds the
// session again, which is what the re-adoption pass establishes.
func waitForFreshCoordinator(t *testing.T, a *App, sid session.ID) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := a.Session.Get(sid); err == nil {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the replacing coordinator never took session %s back", sid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
