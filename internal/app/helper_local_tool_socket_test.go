package app

// The composition-root half of nocx-2tesu: SetLocalToolSocketPath is what
// cmd/nocx-server calls, after Start, once it knows whether it is running a
// tool endpoint at all. These tests prove the SEAM — that the call reaches the
// value the local helper opener names on every pane it opens
// (localHelperOpener.toolEndpoint, read per spawn) — rather than proving that
// the endpoint reaches a pane's launch script; that half is
// internal/helper/session's, where a real daemon opens a real pane and the
// shell in it prints the variable, and it is covered there.

import (
	"testing"

	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// TestSetLocalToolSocketPathReachesTheLocalHelperOpener is criterion 1's
// composition-root half: a path handed to the App reaches the opener a local
// pane is actually started through, at the value that opener reads WHEN it
// opens one — the endpoint is per pane (nocx-50w7p.18), so the seam under test
// is the accessor the spawn path calls and not a stored argument.
func TestSetLocalToolSocketPathReachesTheLocalHelperOpener(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	if a.localHelper == nil {
		t.Fatal("newTestApp built no local helper opener to prove the seam against")
	}

	const sock = "/run/nocx/tool.sock"
	a.SetLocalToolSocketPath(sock)

	if got := a.localHelper.toolEndpoint(); got != sock {
		t.Fatalf("the opener names %q as the pane's tool endpoint, want %q", got, sock)
	}
}

// TestSetLocalToolSocketPathOnAnAppWithNoLocalHelperDoesNotPanic is
// criterion 2's edge: a build or a test that never wired a local helper
// opener (a.localHelper == nil) is a legitimate wiring — see
// localHelperOpener's own file header — and telling it a tool socket path
// must be a no-op, not a crash.
func TestSetLocalToolSocketPathOnAnAppWithNoLocalHelperDoesNotPanic(t *testing.T) {
	a := &App{}
	a.SetLocalToolSocketPath("/run/nocx/tool.sock")
}

// TestSetLocalToolSocketPathWithNoEndpointLeavesItEmpty is criterion 2's
// composition-root half: a backend that never calls SetLocalToolSocketPath at
// all (cmd/nocx-server's startToolEndpoint answered nil, nil — no
// authorizer/dispatcher) leaves the opener naming no endpoint, which is what
// makes a pane it opens carry no NOCX_TOOL_SOCKET — the soft degrade stays
// soft.
func TestSetLocalToolSocketPathWithNoEndpointLeavesItEmpty(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	if a.localHelper == nil {
		t.Fatal("newTestApp built no local helper opener to prove the seam against")
	}

	if got := a.localHelper.toolEndpoint(); got != "" {
		t.Fatalf("the opener names %q before anybody set one, want empty", got)
	}
}
