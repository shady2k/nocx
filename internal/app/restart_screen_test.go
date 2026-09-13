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
	"github.com/shady2k/nocx/internal/sessionruntime"
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

	// THE PROCESS THE DAEMON STARTED, read here because a shutdown is what
	// releases a session's own record of it: read after the kill it would be
	// unknown for a reason that has nothing to do with the re-attachment. What
	// the replacing coordinator records is compared against THIS number, and
	// it is the fact two decisions read (worker admission's root-pid check and
	// agent approval's "is this pane ours") — a re-attached pane that lost it
	// would silently refuse both, which is a feature going away across a
	// restart with nothing on screen to say so.
	beforePID, beforeKnown := first.Session.OwnedProcessPID(sid)
	if !beforeKnown || beforePID <= 0 {
		t.Fatalf("the first coordinator recorded no launch pid for %s (%d, known=%v): the pane was not "+
			"really opened by the daemon", sid, beforePID, beforeKnown)
	}

	// THE COORDINATOR DIES. Its sessions, its store and its watches go with it;
	// the daemon holding the PTY does not, and neither does the runtime beside
	// it.
	first.Shutdown(context.Background())

	second := bootLocalAppOn(t, src)

	// ── THE SESSION COMES BACK, AND THE PANE IS ITS PIPE AGAIN ─────────────
	//
	// Everything above is asserted unconditionally: the first coordinator
	// really opened a pane and really read its screen through the product's
	// own store. What follows is the criterion that used to be a known gap
	// (nocx-ie23r.5): the replacing coordinator takes the LIVE local session
	// back — attaches to it, adopts it under the id the daemon minted, and
	// registers the pane it was the pipe of.
	//
	// The two failure outcomes are kept apart, because they are two different
	// defects and only one of them is this test's: a daemon that no longer
	// holds the session means something ENDED it, and a daemon that holds it
	// while no pane claims it means nobody took it back. Calling the second
	// the first would report a lost session where there is an unclaimed one.
	if !coordinatorHolds(second, sid, reattachWindow) {
		if !helperHolds(t, second, sid) {
			t.Fatalf("the daemon no longer holds session %s: this is not an unclaimed session, it is a "+
				"LOST one — the daemon is the same one and it is still running, so something ended the "+
				"session rather than leaving it for the replacing coordinator to take back.", sid)
		}
		t.Fatalf("the replacing coordinator did not take session %s back, although this machine's daemon "+
			"still holds it: the shell is running, the verdict says live, and no pane is its pipe — "+
			"which is exactly the state nocx-ie23r.5 exists to end.", sid)
	}

	// THE SAME PROCESS the daemon started is what the replacing coordinator
	// holds now.
	afterPID, afterKnown := second.Session.OwnedProcessPID(sid)
	if !afterKnown || afterPID != beforePID {
		t.Fatalf("the re-attached pane records launch pid %d (known=%v), want the %d the daemon "+
			"started for the FIRST coordinator — the pane is not attached to the process it was",
			afterPID, afterKnown, beforePID)
	}

	// The same watch is opened over the same session, in the NEW process.
	if err := second.paneViews.Enrol(string(sid)); err != nil {
		t.Fatalf("watching the pane from the replacing coordinator: %v", err)
	}
	after := waitForMarker(t, second, sid, marker)

	if got := after.Text(2); !strings.Contains(got, marker) {
		t.Fatalf("the replaced coordinator reads row 3 as %q, want the marker the FIRST one's program drew", got)
	}
	// WHAT THE FRAME CAN HONESTLY CLAIM ABOUT ITSELF, asserted rather than
	// left to the marker: a screen that came back through a re-attachment
	// whose ingest lost bytes would still show this marker and would be a
	// different, quieter defect. `Complete` is the daemon's own statement that
	// every byte of the interval reached the emulator, and the runtime only
	// says it when that is true.
	if after.Completeness != sessionruntime.CompletenessComplete {
		t.Fatalf("the re-attached pane's frame claims completeness %v, want Complete: the screen the "+
			"pane is drawn from is not the whole stream the daemon holds", after.Completeness)
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
