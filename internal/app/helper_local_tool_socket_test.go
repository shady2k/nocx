package app

// The composition-root half of nocx-2tesu: SetLocalToolSocketPath is what
// cmd/nocx-server calls, after Start, once it knows whether it is running a
// tool endpoint at all. These tests prove the SEAM — that the call reaches
// the local helper opener's own field, which is what internal/app/helper_local.go's
// connect() reads to build the freshly-spawned daemon's extra environment —
// rather than proving (again) that toolSocketEnv renders one entry or none;
// that half is internal/helper/session and internal/helper/endpoint's, and is
// covered there.

import (
	"testing"

	"github.com/shady2k/nocx/internal/storage/storagetest"
)

// TestSetLocalToolSocketPathReachesTheLocalHelperOpener is criterion 1's
// composition-root half: a path handed to the App reaches the opener a local
// pane is actually started through.
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

	a.localHelper.mu.Lock()
	got := a.localHelper.toolSocketPath
	a.localHelper.mu.Unlock()
	if got != sock {
		t.Fatalf("localHelper.toolSocketPath = %q, want %q", got, sock)
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
// composition-root half: a backend that never calls SetLocalToolSocketPath
// at all (cmd/nocx-server's startToolEndpoint answered nil, nil — no
// authorizer/dispatcher) leaves the opener with the zero value, which is
// what makes connect() add no NOCX_TOOL_SOCKET entry — the soft degrade
// stays soft.
func TestSetLocalToolSocketPathWithNoEndpointLeavesItEmpty(t *testing.T) {
	storagetest.Isolate(t)
	a, err := newTestApp(t)
	if err != nil {
		t.Fatalf("newTestApp: %v", err)
	}
	if a.localHelper == nil {
		t.Fatal("newTestApp built no local helper opener to prove the seam against")
	}

	a.localHelper.mu.Lock()
	got := a.localHelper.toolSocketPath
	a.localHelper.mu.Unlock()
	if got != "" {
		t.Fatalf("localHelper.toolSocketPath = %q before anybody set it, want empty", got)
	}
}
