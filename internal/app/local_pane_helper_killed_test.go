package app

// THE HELPER IS KILLED UNDER A RUNNING SERVER, AND COPY DIAGNOSTICS SAYS SO
// (nocx-3an1s.1.8, owner's evidence 2026-11-05).
//
// The owner's experiment: kill the helper while the server keeps running, and
// the panes answered "Control plane busy" forever with no new helper started.
// The record must answer the first question of that state — is the helper
// connected, and since when — from the SAME server that is refusing work.
//
// This is the real composition root: the real installer, the real daemon, the
// real transport. Only the kill is the test's.

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/shady2k/nocx/internal/transport"
)

func TestCopyDiagnosticsSaysTheHelperIsGoneAfterItIsKilled(t *testing.T) {
	a := newLocalPaneApp(t)
	if a.Transport == nil {
		t.Fatal("the app has no transport")
	}
	// ONE PANE FIRST: a helper connection is only opened by work that needs
	// one, and "was connected, then lost" is the owner's state — not "never
	// connected".
	opened, err := a.Transport.OpenSession(t.Context(), transport.OpenSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("open a local pane so the helper connection exists: %v", err)
	}
	t.Cleanup(func() { _ = opened.Session.Close() })

	// THE BINARY THIS APP ACTUALLY INSTALLED, read from the opener that
	// installed it: a second isolated home in this test would name a path no
	// process is running.
	a.localHelper.mu.Lock()
	binary := a.localHelper.installed.Binary
	a.localHelper.mu.Unlock()
	if binary == "" {
		t.Fatal("the app installed no helper binary")
	}

	conn := dialTraceWSServer(t, a.Transport)
	before := callAppDiagnostics(t, conn)
	if !strings.Contains(before, "helper: connected") {
		t.Fatalf("a healthy helper did not read as connected:\n%s", before)
	}

	// KILLED, not disconnected by us: the process goes away exactly as the
	// owner's did, and the server stays up.
	// #nosec G204 — the path is the binary this test's own app installed.
	if out, err := exec.Command("pkill", "-TERM", "-f", binary).CombinedOutput(); err != nil {
		t.Fatalf("kill the helper %q: %v (%s)", binary, err, out)
	}
	waittestWaitGone(t, binary)

	after := callAppDiagnostics(t, conn)
	if !strings.Contains(after, "helper: not connected") {
		t.Fatalf("after the helper was killed the record still claims a helper:\n%s", after)
	}
	// AND THE SERVER ITSELF IS STILL ANSWERING: the method read a stat and the
	// record while the process that opens panes was gone.
	if !strings.Contains(after, "server: up") {
		t.Fatalf("the record lost the server's own uptime:\n%s", after)
	}
}

// waittestWaitGone waits until no process matches the helper's path. It is a
// condition wait on the OS, not a sleep.
func waittestWaitGone(t *testing.T, binary string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("pgrep", "-f", binary).Output()
		if err != nil && len(strings.TrimSpace(string(out))) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the helper %q is still running after SIGTERM", binary)
}
