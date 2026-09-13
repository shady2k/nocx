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
	// The FIRST coordinator's read is a frame of the session's own size, and it
	// is asserted here rather than beside the resolution below: everything
	// above the gap is unconditional, so a frame that came back at some other
	// geometry fails the test instead of being described as the known gap.
	if before.Cols != 100 || before.Rows != 30 {
		t.Fatalf("the first coordinator read a %dx%d frame, want the 100x30 the session was opened at",
			before.Cols, before.Rows)
	}

	// THE COORDINATOR DIES. Its sessions, its store and its watches go with it;
	// the daemon holding the PTY does not, and neither does the runtime beside
	// it.
	first.Shutdown(context.Background())

	second := bootLocalAppOn(t, src)

	// ── THE KNOWN GAP, AND ITS THREE OUTCOMES ──────────────────────────────
	//
	// Everything above is asserted unconditionally: the first coordinator
	// really opened a pane and really read its screen through the product's
	// own store. What follows is where the gap lives, and it is written so the
	// gap cannot outlive itself.
	//
	//   * The session comes BACK → FAIL. The gap has closed, and a test that
	//     kept skipping would be a test nobody deletes.
	//   * The session is GONE from the helper too → FAIL. That is a lost
	//     session, not an unclaimed one, and calling it the known gap would
	//     hide exactly the defect this test exists to find.
	//   * The helper still holds it and the coordinator did not take it back →
	//     SKIP, naming the bead that owns the route (nocx-ie23r.5).
	//
	// The distinction is the whole value: a bare skip would pass on all three.
	if tookBack := coordinatorHolds(second, sid, reattachWindow); tookBack {
		t.Fatalf("stale: the replacing coordinator took session %s back, so nocx-ie23r.5 has landed — "+
			"delete this gap and keep the assertions below", sid)
	}
	if !helperHolds(t, second, sid) {
		t.Fatalf("the helper no longer holds session %s: this is not the known gap, it is a LOST "+
			"session — the daemon is the same one and it is still running, so something ended the "+
			"session rather than leaving it unclaimed. That is a different defect from the one this "+
			"gap names, and it must not be skipped over.", sid)
	}
	// WHAT CHANGED, AND WHAT DID NOT (nocx-ie23r.2 has since landed). The
	// session is no longer unjudged: the replacing coordinator now ASKS this
	// machine's daemon — over the endpoint, by dialling the generation the
	// binding names — and records the verdict `live` for it, which is the
	// claim the ledger and the notice are built on. What it still does not do
	// is ATTACH, and that is the whole of what this gap is now: a judged
	// session with no pane, rather than an unjudged one. Taking it back needs
	// a second attach against a live local session and the pane it was the
	// pipe of, which is nocx-ie23r.5's.
	t.Skip("known gap (nocx-ie23r.5): a local pane's session is re-adopted into the registry — and so " +
		"into a restored pane — by nobody yet. readoptPass.readoptLocal asks the generation the " +
		"binding names and judges it (session_readopt.go), and it deliberately stops there: " +
		"attaching to a live local session is the next bead. So the daemon still holds the PTY, the " +
		"verdict says `live`, and no pane owns it. The first coordinator's read of the marker is " +
		"asserted above; the resolution below is the criterion that is waiting on that bead.")

	// The same watch is opened over the same session, in the NEW process.
	if err := second.paneViews.Enrol(string(sid)); err != nil {
		t.Fatalf("watching the pane from the replacing coordinator: %v", err)
	}
	after := waitForMarker(t, second, sid, marker)

	if got := after.Text(2); !strings.Contains(got, marker) {
		t.Fatalf("the replaced coordinator reads row 3 as %q, want the marker the FIRST one's program drew", got)
	}
	if after.Cols != 100 || after.Rows != 30 {
		t.Errorf("the screen came back at %dx%d, want the 100x30 the session was opened at", after.Cols, after.Rows)
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

// reattachWindow bounds the wait for a re-adoption. It is generous on purpose:
// the pass is synchronous before the server listens, so a session that is
// coming back has already come back by the time Start returns — the window is
// here for a slow machine and not because the answer is expected to change.
const reattachWindow = 30 * time.Second

// coordinatorHolds reports whether the coordinator took the session back.
func coordinatorHolds(a *App, sid session.ID, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for {
		if _, err := a.Session.Get(sid); err == nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// helperHolds asks the DAEMON whether it still holds the session, through the
// seated coordinator's own client — the same route a frame read takes.
//
// It is what separates the known gap from a lost session: the gap is "the PTY
// is still there and nobody claimed it", and a helper that no longer holds it
// would be the other thing entirely.
func helperHolds(t *testing.T, a *App, sid session.ID) bool {
	t.Helper()
	// CONNECTED FIRST, and that is the whole correction: a coordinator opens
	// its connection to the daemon LAZILY, so looking at the opener's client
	// right after Start asks nothing and answers "the daemon holds nothing".
	// The question is what the DAEMON holds, and reaching it is the act the
	// epic is about — a replacing coordinator asks the daemon what it kept.
	c, _, err := a.localHelper.connect(context.Background())
	if err != nil {
		t.Fatalf("reaching this machine's helper: %v", err)
	}
	entries, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatalf("asking the helper what it holds: %v", err)
	}
	for _, entry := range entries {
		if entry.HostSessionID.Session == string(sid) {
			return true
		}
	}
	return false
}
