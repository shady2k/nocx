package app

// A local shell that exits on its own is an EXIT, not a lost connection
// (nocx-o3amz).
//
// lifecyclePTY embeds the pty.Pty INTERFACE, and a concrete type's method is
// not promoted through an embedded interface — so *LocalPty.WaitErr, the only
// thing that knows what cmd.Wait returned, was invisible to the optional-method
// assertion realSession.ExitOutcome makes. Every enhanced local session
// therefore classified as ExitInterrupted: the tab hung about marked
// "Connection lost" and the shell's exit status was thrown away.
//
// These drive the production composition root: the real localPTYFactory, a real
// bash under the real lifecycle bootstrap, a real session registry over it, and
// the exit the shell itself reports. The mapping from a wait result to a cause
// is owned by internal/session and tested there; what is proven here is that an
// enhanced local session REACHES that mapping with the shell's own report, and
// that a pty which genuinely has nothing to report still classifies as a loss
// rather than panicking.

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// openLocalEnhanced opens a real enhanced local session through the production
// factory, with bash pinned so the assertion is about nocx's classification
// rather than about which login shell this machine happens to have.
func openLocalEnhanced(t *testing.T) session.Session {
	t.Helper()
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is not installed: %v — the integrated tier this test classifies must be present", err)
	}
	storagetest.IsolateWithHome(t)
	f := localFactory(t)
	f.shells = fixedShell{path: shell}

	reg := session.New(f.log, f)
	sess, err := reg.Open(context.Background(), session.Config{
		Kind: session.KindLocal, Cols: 80, Rows: 24, Enhanced: true,
	})
	if err != nil {
		t.Fatalf("Open(local enhanced): %v", err)
	}
	// The shell must not block on a full pty buffer while the test waits for
	// it to exit, so its output is drained for as long as it lives — through
	// the same seam the transport uses.
	if err := sess.StartOutput(context.Background(), func([]byte) error { return nil }); err != nil {
		t.Fatalf("StartOutput: %v", err)
	}
	return sess
}

// exitShell types `exit <code>` at the shell and waits for the session to end.
func exitShell(t *testing.T, sess session.Session, line string) {
	t.Helper()
	if _, err := sess.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write %q to the session: %v", line, err)
	}
	select {
	case <-sess.Done():
	case <-time.After(30 * time.Second):
		t.Fatalf("the shell did not exit within 30s after %q", line)
	}
}

// A shell the user exited cleanly is an authoritative exit with status 0 —
// the tab closes, and nothing tells the user the connection was lost.
func TestLocalEnhancedSession_CleanExitIsExited(t *testing.T) {
	sess := openLocalEnhanced(t)
	exitShell(t, sess, "exit 0")

	cause, status := sess.ExitOutcome()
	if cause != session.ExitExited {
		t.Errorf("cause = %q, want %q — a clean local exit is not a lost connection", cause, session.ExitExited)
	}
	if status != 0 {
		t.Errorf("status = %d, want 0", status)
	}
}

// And the status the shell reported is the status that reaches the product:
// a nonzero exit is still an exit, carrying its own code.
func TestLocalEnhancedSession_NonzeroExitCarriesItsStatus(t *testing.T) {
	sess := openLocalEnhanced(t)
	exitShell(t, sess, "exit 17")

	cause, status := sess.ExitOutcome()
	if cause != session.ExitExited {
		t.Errorf("cause = %q, want %q", cause, session.ExitExited)
	}
	if status != 17 {
		t.Errorf("status = %d, want 17 — the shell's own report", status)
	}
}

// silentPTY is a lifecyclePTY over a pty that reports nothing about how its
// process ended — the paired failure path for the forward: an absent report
// must stay absent, never be invented, and never panic.
type silentPTYFactory struct {
	log  log.Logger
	stub *pty.Stub
}

func (f *silentPTYFactory) NewPTY(context.Context, pty.Config) (pty.Pty, error) {
	f.stub = pty.NewStub(f.log)
	return &lifecyclePTY{Pty: f.stub}, nil
}

func TestLocalSession_PtyWithNoShellReportIsInterrupted(t *testing.T) {
	logger := log.NewSlogAdapter(nil)
	f := &silentPTYFactory{log: logger}
	reg := session.New(logger, f)
	sess, err := reg.Open(context.Background(), session.Config{Kind: session.KindLocal, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// The pty ends on its own — the session is not torn down — so the only
	// reason there is no status is that this pty never had one.
	if cerr := f.stub.Close(); cerr != nil {
		t.Fatalf("close the stub pty: %v", cerr)
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end when its pty did")
	}

	cause, status := sess.ExitOutcome()
	if cause != session.ExitInterrupted {
		t.Errorf("cause = %q, want %q — a pty with no shell report must not be dressed up as an exit", cause, session.ExitInterrupted)
	}
	if status != 0 {
		t.Errorf("status = %d, want 0 — a loss never carries a fabricated status", status)
	}
}
