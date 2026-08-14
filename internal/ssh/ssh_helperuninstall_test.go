package ssh

// The helper-uninstall capability (remote-helper design D25) tested against
// the REAL SFTP subsystem: the fixture serves a real local directory as the
// remote root, so the tree a test seeds is a real tree and removal is
// observed on the local disk. The D25 ORDER — close the exec channel before
// removing the directory — is the CALLER's contract (the transport handler
// closes the composition root's live helper channels before invoking this
// capability); what this file proves is the dial-and-remove half: the
// capability owns the pooled lease, discovers the remote home SFTP-native,
// and removes the whole ~/.nocx/helper tree and nothing else.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUninstallHelper_RemovesTheWholeTreeAndNothingElse: a seeded helper
// tree (a complete install AND a markerless directory an interrupted
// install left) is removed wholesale, while the shell bundle's files beside
// it survive — the capability's remit is the helper tree alone (D25).
func TestUninstallHelper_RemovesTheWholeTreeAndNothingElse(t *testing.T) {
	srv := startFSTestServer(t, fsModeReal)
	rc := fsTestClient(t, srv)

	root := filepath.Join(srv.rootDir, ".nocx", "helper")
	complete := filepath.Join(root, "1-linux-amd64-"+strings.Repeat("a", 64))
	if err := os.MkdirAll(complete, 0o700); err != nil {
		t.Fatalf("seed complete install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(complete, "nocx-helper"), []byte("binary"), 0o600); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(complete, ".install-complete"), nil, 0o600); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	// The interrupted-install directory: no .install-complete marker —
	// exactly the one a user cannot otherwise get rid of.
	incomplete := filepath.Join(root, "1-linux-amd64-"+strings.Repeat("b", 64))
	if err := os.MkdirAll(incomplete, 0o700); err != nil {
		t.Fatalf("seed incomplete install: %v", err)
	}
	if err := os.WriteFile(filepath.Join(incomplete, "nocx-helper"), []byte("partial"), 0o600); err != nil {
		t.Fatalf("seed partial binary: %v", err)
	}
	// The shell bundle's own files live beside the helper tree and must
	// survive an uninstall.
	if err := os.MkdirAll(filepath.Join(srv.rootDir, ".nocx"), 0o700); err != nil {
		t.Fatalf("seed .nocx: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srv.rootDir, ".nocx", "manifest.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("seed manifest: %v", err)
	}

	removed, err := rc.UninstallHelper(context.Background(), srv.addr, fsConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("UninstallHelper: %v", err)
	}
	if !removed {
		t.Fatal("removed = false, want true (a tree existed)")
	}
	if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("~/.nocx/helper still exists after uninstall: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(srv.rootDir, ".nocx", "manifest.json")); statErr != nil {
		t.Fatalf("uninstall removed a shell-bundle file it does not own: %v", statErr)
	}
	waitPoolEmpty(t, rc)

	// A host with nothing installed uninstalls cleanly: a second click is
	// a no-op that succeeds, reported as such.
	removed, err = rc.UninstallHelper(context.Background(), srv.addr, fsConnectOpts(srv)...)
	if err != nil {
		t.Fatalf("second UninstallHelper on a bare host: %v", err)
	}
	if removed {
		t.Fatal("removed = true on a bare host, want false (nothing was there)")
	}
	waitPoolEmpty(t, rc)
}
