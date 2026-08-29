package app

// A signal addressed to an integrated local session must reach the execution
// (nocx-7l4ex.13).
//
// THE SAME DEFECT AS nocx-o3amz, IN THE SAME PLACE, ONE METHOD ALONG.
// lifecyclePTY embeds the pty.Pty INTERFACE, and a concrete type's method is
// not promoted through an embedded interface — so *LocalPty.SignalForeground,
// the only thing that can reach the foreground process group, was invisible to
// the optional-method assertion realSession.SignalForeground makes. Every
// ENHANCED local session therefore answered pty.ErrNoForeground for every
// signal, and session.signal reported "nothing-running" over a plainly running
// command. That is the incident nocx-92gfl.4 was filed as, and it was diagnosed
// as a shared process group instead: the shell's group was never consulted,
// because nothing ever asked the pty.
//
// The WaitErr forward carries the warning this test is the second instance of:
// "Anything else optional a pty grows needs the same forward, for the same
// reason." So this drives the production composition root — the real
// localPTYFactory, a real bash under the real lifecycle bootstrap, a real
// session registry — and asks the question through the seam the transport uses.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/pty"
	"github.com/shady2k/nocx/internal/session"
	"github.com/shady2k/nocx/internal/storage/storagetest"
	"github.com/shady2k/nocx/internal/waittest"
)

// openLocalEnhancedWatched is openLocalEnhanced with the session's output kept,
// so readiness is an observable fact rather than a duration.
func openLocalEnhancedWatched(t *testing.T) (session.Session, func() string) {
	t.Helper()
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash is not installed: %v — the integrated tier this test signals must be present", err)
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
	var mu sync.Mutex
	var seen strings.Builder
	if err := sess.StartOutput(context.Background(), func(b []byte) error {
		mu.Lock()
		seen.Write(b)
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatalf("StartOutput: %v", err)
	}
	return sess, func() string {
		mu.Lock()
		defer mu.Unlock()
		return seen.String()
	}
}

func TestLocalEnhancedSession_SignalReachesTheForegroundExecution(t *testing.T) {
	sess, output := openLocalEnhancedWatched(t)
	sg, ok := sess.(interface {
		SignalForeground(sig syscall.Signal) error
	})
	if !ok {
		t.Fatal("the session does not offer SignalForeground at all")
	}

	// The marker is a file's CONTENT echoed back by the running program, so
	// observing it means that program has completed its execve and holds the
	// terminal — it cannot be satisfied by the shell's echo of the command
	// line, which names only the path.
	dir := t.TempDir()
	marker := filepath.Join(dir, "enhanced-foreground-ready")
	if err := os.WriteFile(marker, []byte("ENHANCED-FOREGROUND-READY\n"), 0o600); err != nil {
		t.Fatalf("write the readiness marker: %v", err)
	}
	if _, err := sess.Write([]byte("tail -f '" + marker + "'\n")); err != nil {
		t.Fatalf("write the command: %v", err)
	}
	waittest.WaitForTimeoutDetail(t, "the foreground program to announce itself", 30*time.Second,
		output, func() bool { return strings.Contains(output(), "ENHANCED-FOREGROUND-READY") })

	// The existence check first, because it is the exact question the defect
	// answered wrongly: is there a foreground execution this session can
	// reach? ErrNoForeground here means the signal never left the session.
	if err := sg.SignalForeground(0); err != nil {
		t.Fatalf("SignalForeground(0) over a running foreground job = %v, want nil — "+
			"the signal is not reaching the pty (errors.Is ErrNoForeground: %v)",
			err, errors.Is(err, pty.ErrNoForeground))
	}

	// And a real SIGINT ends it: the shell gets the terminal back and reads a
	// line again, which it cannot do while a foreground job holds it.
	if err := sg.SignalForeground(syscall.SIGINT); err != nil {
		t.Fatalf("SignalForeground(SIGINT): %v", err)
	}
	if _, err := sess.Write([]byte("printf %s%s INTERRUPTED -OK\n")); err != nil {
		t.Fatalf("write the follow-up command: %v", err)
	}
	waittest.WaitForTimeoutDetail(t, "the shell to answer after the interrupt", 30*time.Second,
		output, func() bool { return strings.Contains(output(), "INTERRUPTED-OK") })
}

// TestLocalEnhancedSession_TheWholeSignalSeamIsForwarded is the THIRD instance
// of one defect, and it is written to be the last (nocx-92gfl.4, nocx-o3amz,
// nocx-7l4ex.13, and now the reconciliation of nocx-uvac6.11 with it).
//
// Every time, lifecyclePTY forwarded SOME of the pty's optional signal methods
// and not others, and every time the missing one made realSession's
// optional-method assertion answer pty.ErrNoForeground on an ENHANCED local
// session — which session.signal reports to a person as "nothing is running in
// this pane" over a command that plainly is. Nothing fails to compile, and the
// wrong answer is a plausible one, so only an end-to-end run has ever caught
// it.
//
// nocx-uvac6.11 then split a stop into "name the addressee once"
// (ForegroundJob) and "signal that exact group" (SignalProcessGroup). Those
// two grew on a branch where this wrapper did not exist, so they did not
// travel with it, and the merge reopened the incident through a door that was
// not there when it was closed.
//
// So this test asks for the SEAM, not for a method: every question the stop
// policy puts to a session must be answerable by an enhanced local session.
// A method added to that seam later and not forwarded here fails this test
// rather than a person's Stop.
func TestLocalEnhancedSession_TheWholeSignalSeamIsForwarded(t *testing.T) {
	sess, output := openLocalEnhancedWatched(t)
	seam, ok := sess.(interface {
		SignalForeground(sig syscall.Signal) error
		ForegroundJob() (int, error)
		SignalProcessGroup(pgid int, sig syscall.Signal) error
	})
	if !ok {
		t.Fatal("an enhanced local session does not offer the whole signal seam — " +
			"a stop asks ForegroundJob before it signals anything, so a session " +
			"missing any of these answers ErrNoForeground over a running command")
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "seam-foreground-ready")
	if err := os.WriteFile(marker, []byte("SEAM-FOREGROUND-READY\n"), 0o600); err != nil {
		t.Fatalf("write the readiness marker: %v", err)
	}
	if _, err := sess.Write([]byte("tail -f '" + marker + "'\n")); err != nil {
		t.Fatalf("write the command: %v", err)
	}
	waittest.WaitForTimeoutDetail(t, "the foreground program to announce itself", 30*time.Second,
		output, func() bool { return strings.Contains(output(), "SEAM-FOREGROUND-READY") })

	// NAMING the addressee is the question the merge broke: it answered
	// ErrNoForeground, the policy read that as "the group is gone", and Stop
	// said nothing-running about this very program.
	pgid, err := seam.ForegroundJob()
	if err != nil {
		t.Fatalf("ForegroundJob over a running foreground job = %v, want a pgid — "+
			"the naming half of the seam is not reaching the pty (errors.Is ErrNoForeground: %v)",
			err, errors.Is(err, pty.ErrNoForeground))
	}
	if pgid <= 0 {
		t.Fatalf("ForegroundJob named pgid %d, want a real process group", pgid)
	}

	// And the named group is reachable: the existence check must succeed
	// against the pgid just named, or the ladder's every rung would miss.
	if err := seam.SignalProcessGroup(pgid, 0); err != nil {
		t.Fatalf("SignalProcessGroup(%d, 0) right after naming it = %v, want nil — "+
			"the signalling half of the seam is not reaching the pty", pgid, err)
	}

	// The whole point, end to end: the group that was NAMED is the group that
	// gets the signal, and the shell reads a line again afterwards.
	if err := seam.SignalProcessGroup(pgid, syscall.SIGINT); err != nil {
		t.Fatalf("SignalProcessGroup(%d, SIGINT): %v", pgid, err)
	}
	if _, err := sess.Write([]byte("printf %s%s SEAM -OK\n")); err != nil {
		t.Fatalf("write the follow-up command: %v", err)
	}
	waittest.WaitForTimeoutDetail(t, "the shell to answer after the interrupt", 30*time.Second,
		output, func() bool { return strings.Contains(output(), "SEAM-OK") })
}
