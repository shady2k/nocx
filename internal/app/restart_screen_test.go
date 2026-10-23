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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/paneview"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/sessionruntime"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/transport"
	"golang.org/x/sys/unix"
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
	opened, openErr := first.Transport.OpenSession(context.Background(), transport.OpenSpec{Cols: 100, Rows: 30})
	if openErr != nil {
		t.Fatalf("opening a local pane through the shipped opener: %v", openErr)
	}
	sid := opened.Session.ID()
	awaitLocalPaneIntegration(t, first, sid)
	if err := first.paneViews.Enrol(string(sid)); err != nil {
		t.Fatalf("watching the pane: %v", err)
	}
	// Keep one POSIX foreground probe alive across the restart. The daemon's
	// child remains the native account shell; the probe owns the trap state
	// and receives the PTY's foreground SIGWINCH regardless of that shell's
	// language. Its split prompt marker cannot match the echoed launch line.
	const probeReady = "RESTART-PROBE-READY> "
	probe := posixPaneCommand("PS1='RESTART-''PROBE-READY> '; export PS1; exec /bin/sh -i")
	if _, err := opened.Session.Write([]byte(probe + "\n")); err != nil {
		t.Fatalf("starting the POSIX foreground probe: %v", err)
	}
	waitForMarkerAnywhere(t, first, sid, probeReady)

	// The program draws, and the marker is waited for THROUGH THE PRODUCT'S
	// OWN READ — the store the observation, the emitting view and the typing
	// gate all read — so what is asserted afterwards is a screen the product
	// really produced and not a byte string the test wrote.
	const marker = "SCREEN-SURVIVES"
	if _, err := opened.Session.Write([]byte("printf '\\033[2J\\033[3;5H" + marker + "\\n'\n")); err != nil {
		t.Fatalf("running a marker-drawing program: %v", err)
	}
	before := waitForMarker(t, first, sid, marker)
	// Keep the foreground probe's trap running across the coordinator restart.
	// Its trap records SIGWINCH from the post-restart resize.
	const winchMarker = "RESTART-RESIZE-WINCH"
	if _, err := opened.Session.Write([]byte("trap 'WINCH_SEEN=1' WINCH; echo WINCH-TRAP-''READY\n")); err != nil {
		t.Fatalf("starting the SIGWINCH probe in the helper PTY: %v", err)
	}
	waitForMarkerAnywhere(t, first, sid, "WINCH-TRAP-READY")
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
	// unknown for a reason that has nothing to do with the re-attachment.
	beforePID, beforeKnown := first.Session.OwnedProcessPID(sid)
	if !beforeKnown || beforePID <= 0 {
		t.Fatalf("the first coordinator recorded no launch pid for %s (%d, known=%v): the pane was not "+
			"really opened by the daemon", sid, beforePID, beforeKnown)
	}
	// AND THE OPERATING SYSTEM IS ASKED THE SAME QUESTION, because a record
	// nocx wrote about itself is exactly what the criterion says not to read:
	// the pid has to be a live process, and its parent has to be the daemon
	// that is holding the PTY. A pane attached to the wrong thing would pass
	// the registry's account of it and fail this.
	if !alive(beforePID) {
		t.Fatalf("the pid the first coordinator records for %s (%d) is not a running process", sid, beforePID)
	}
	if parent := commandOf(t, parentOf(t, beforePID)); !strings.Contains(parent, "nocx-helper") {
		t.Fatalf("pid %d's parent is running %q, want this machine's nocx-helper daemon", beforePID, parent)
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
	// THE ANSWER IS ASSERTED, NOT POLLED FOR, and that is not a shortcut: the
	// pass is synchronously before the server listens (readoptAttemptTimeout's
	// own comment says why it must be), so by the time Start returned this
	// question already has its final answer. A poll here would be a slower way
	// of asking it, and a deadline used as a success mechanism is the shape
	// the repo's "no timing" rule exists to refuse.
	//
	// The two failure outcomes are kept apart, because they are two different
	// defects and only one of them is this test's: a daemon that no longer
	// holds the session means something ENDED it, and a daemon that holds it
	// while no pane claims it means nobody took it back. Calling the second
	// the first would report a lost session where there is an unclaimed one.
	if _, err := second.Session.Get(sid); err != nil {
		if !helperHolds(t, second, sid) {
			t.Fatalf("the daemon no longer holds session %s: this is not an unclaimed session, it is a "+
				"LOST one — the daemon is the same one and it is still running, so something ended the "+
				"session rather than leaving it for the replacing coordinator to take back.", sid)
		}
		t.Fatalf("the replacing coordinator did not take session %s back, although this machine's daemon "+
			"still holds it: %v — the shell is running, the verdict says live, and no pane is its "+
			"pipe, which is exactly the state nocx-ie23r.5 exists to end.", sid, err)
	}

	// THE SAME PROCESS the daemon started is what the replacing coordinator
	// holds now — recorded for the decisions that read it, and running, which
	// the kernel is asked directly.
	afterPID, afterKnown := second.Session.OwnedProcessPID(sid)
	if !afterKnown || afterPID != beforePID {
		t.Fatalf("the re-attached pane records launch pid %d (known=%v), want the %d the daemon "+
			"started for the FIRST coordinator — the pane is not attached to the process it was",
			afterPID, afterKnown, beforePID)
	}
	if !alive(afterPID) {
		t.Fatalf("pid %d is not running after the re-attachment, so the pane holds a shell the "+
			"kernel no longer has", afterPID)
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

	restored, err := second.Session.Get(sid)
	if err != nil {
		t.Fatalf("getting the re-adopted session to resize: %v", err)
	}
	// Resize through the adopted session channel, then query TIOCGWINSZ on
	// the helper-owned PTY slave. The helper session entry carries the shell
	// pid, and /proc/<pid>/fd/0 is the slave the shell reads from — on Linux,
	// which is the only platform that reaches it: the PTY MASTER belongs to
	// the helper daemon, because this test drives the shipped App rather than
	// holding a PTY of its own, so the slave is reachable by pid through
	// /proc and by nothing else. On darwin the read is skipped and said out
	// loud; the SAME winsize is still asserted there through the program's
	// own view of the same ioctl, two assertions below — the shell's `stty
	// size` and the SIGWINCH trap that ioctl raises.
	if err := restored.Resize(context.Background(), session.Size{Cols: 132, Rows: 43}); err != nil {
		t.Fatalf("resizing the re-adopted helper session: %v", err)
	}
	if err := waitForPTYWinsize(t, afterPID, 132, 43); err != nil {
		if !errors.Is(err, errNoProcessFD) {
			t.Fatal(err)
		}
		t.Logf("the out-of-band TIOCGWINSZ read is skipped here: %v", err)
	}
	// The PTY ioctl also raises SIGWINCH to the foreground process group. The
	// foreground probe remains in its POSIX prompt loop; ask it to report the
	// trap state and the winsize directly from its tty after the ioctl.
	if _, err := restored.Write([]byte("echo " + winchMarker + "-$WINCH_SEEN; stty size\n")); err != nil {
		t.Fatalf("asking the re-adopted shell to report SIGWINCH: %v", err)
	}
	// THE SIZE IS WAITED FOR, NOT THE MARKER (nocx-xn63t.6.49). The line just
	// typed CARRIES the marker as text the shell echoes, so a wait on the
	// marker alone returns while `stty size` has not printed yet: measured on
	// 2026-10-18 under a loaded machine, the frame the wait returned held
	// `echo RESTART-RESIZE-WINCH-$WINCH_SEEN; stty size` and
	// `RESTART-RESIZE-WINCH-1` and no size at all, and this test failed an
	// assertion about a number it had never waited for. Waiting on both is
	// waiting on the observable; widening the bound would not be, because the
	// wait was never short of time — it was short of a reason to keep reading.
	resized := waitForScreenTexts(t, second, sid, winchMarker, "43 132")
	var screen strings.Builder
	for row := 0; row < resized.Rows; row++ {
		screen.WriteString(resized.Text(row))
		screen.WriteByte('\n')
	}
	if got := screen.String(); !strings.Contains(got, "43 132") {
		t.Fatalf("the shell's stty size output %q does not show 43 132", got)
	}
	if got := screen.String(); !strings.Contains(got, winchMarker+"-1") {
		t.Fatalf("the shell output %q does not show the SIGWINCH trap ran", got)
	}
	if resized.Cols != 132 || resized.Rows != 43 {
		t.Fatalf("the re-adopted screen is %dx%d after resize, want 132x43", resized.Cols, resized.Rows)
	}
}

// errNoProcessFD reports that this platform gives a test no way to reach a
// process's own tty by pid. It is a SENTINEL rather than a skip woven into the
// caller so the one platform-dependent step stays one function: the caller
// tolerates exactly this refusal and nothing else.
var errNoProcessFD = errors.New("this platform keeps no /proc/<pid>/fd")

// waitForPTYWinsize reads the helper-owned PTY slave directly; no shell or
// coordinator size report can substitute for the kernel's TIOCGWINSZ value.
//
// LINUX ONLY, and the reason is the PTY's SHAPE rather than a preference: the
// master of this session belongs to the helper daemon — this test drives the
// shipped App, so it never holds a PTY fd of its own — which leaves
// /proc/<pid>/fd/0, the slave the shell reads from, as the one door to the
// kernel's answer. darwin keeps no such tree, and the honest consequence is
// the sentence on `errNoProcessFD`: the CALLER says it skipped this read
// rather than the read pretending it passed. What darwin does still assert is
// the same winsize through the program's own view of the same ioctl, which is
// why the skip costs this test its out-of-band half and not its criterion.
func waitForPTYWinsize(t *testing.T, pid, cols, rows int) error {
	t.Helper()
	if runtime.GOOS != "linux" {
		return fmt.Errorf("%w (%s): the PTY master is the helper daemon's, so its slave "+
			"is reachable by pid only through /proc", errNoProcessFD, runtime.GOOS)
	}
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		fd, err := os.Open(fmt.Sprintf("/proc/%d/fd/0", pid))
		if err != nil {
			return fmt.Errorf("open helper shell tty for pid %d: %w", pid, err)
		}
		ws, ioctlErr := unix.IoctlGetWinsize(int(fd.Fd()), unix.TIOCGWINSZ)
		_ = fd.Close()
		if ioctlErr != nil {
			return fmt.Errorf("TIOCGWINSZ for helper shell pid %d: %w", pid, ioctlErr)
		}
		last = fmt.Sprintf("%dx%d", ws.Col, ws.Row)
		if int(ws.Col) == cols && int(ws.Row) == rows {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("helper PTY winsize is %s, want %dx%d", last, cols, rows)
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

// waitForMarkerAnywhere is for asynchronous terminal replies such as a shell
// trap. Unlike a command's normal output, its line depends on the cursor where
// the signal arrived.
func waitForMarkerAnywhere(t *testing.T, a *App, sid session.ID, marker string) paneview.Frame {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for {
		f, err := a.paneViews.Frame(string(sid))
		if err == nil {
			for row := 0; row < f.Rows; row++ {
				if strings.Contains(f.Text(row), marker) {
					return f
				}
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

// waitForScreenTexts waits until ONE frame carries every text given, and
// returns it.
//
// It is waitForMarkerAnywhere for the case that one function cannot express: a
// marker the shell ECHOES before it produces the answer the caller asserts on.
// A wait for the marker alone can be satisfied by the echo — the same bytes the
// caller typed — so the caller then reads a frame whose next line has not been
// written yet. Waiting for the marker AND the answer is waiting on an
// observable; the alternative, a longer bound, waits on a duration for a fact
// that may already have arrived. (nocx-xn63t.6.49, measured under load.)
func waitForScreenTexts(t *testing.T, a *App, sid session.ID, texts ...string) paneview.Frame {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	var last string
	for {
		f, err := a.paneViews.Frame(string(sid))
		if err == nil {
			var screen strings.Builder
			for row := 0; row < f.Rows; row++ {
				screen.WriteString(f.Text(row))
				screen.WriteByte('\n')
			}
			last = screen.String()
			carries := true
			for _, want := range texts {
				if !strings.Contains(last, want) {
					carries = false
					break
				}
			}
			if carries {
				return f
			}
		} else {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the screen never carried %q (last error: %v); the screen held:\n%s",
				strings.Join(texts, " and "), lastErr, last)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// helperHolds asks the DAEMON whether it still holds the session, through the
// seated coordinator's own client — the same route a frame read takes.
//
// It is what separates an UNCLAIMED session from a LOST one: the first is "the
// PTY is still there and nobody took it back", and a helper that no longer
// holds it would be the other thing entirely.
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
