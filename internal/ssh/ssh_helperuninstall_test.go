package ssh

// The helper-uninstall capability (remote-helper design D25) tested against
// the REAL SFTP subsystem: the fixture serves a real local directory as the
// remote root, so the tree a test seeds is a real tree and removal is
// observed on the local disk.
//
// # What this file proves since nocx-50w7p.3, and what it no longer does
//
// The lease no longer DIALS — the coordinator holds no ssh client on this path
// and the stream arrives from this machine's helper (ssh_helperinstall.go) — so
// what is proven here is the half that stayed: the write-capable lease over a
// stream, SFTP-native home discovery, and the removal of the whole
// ~/.nocx/helper tree and nothing else. The dial-and-pool half is
// internal/helper/sshsvc's, over the same real fixture, and its assertions
// moved with it rather than being deleted.
//
// The stream is taken from the fixture by opening an sftp subsystem on a real
// connection here in the test. That is the shape a helper hands over — an
// ordered, bidirectional byte stream to a subsystem — without a daemon on this
// machine, and the wrapping code under test cannot tell the difference because
// it does not look at where the bytes came from.
//
// The D25 ORDER — close the exec channel before removing the directory — is the
// CALLER's contract (the transport handler closes the composition root's live
// helper channels before invoking this capability).

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// subsystemStream is one subsystem's bytes as a single ReadWriteCloser: the
// session's stdout reads and stdin writes, and Close ends the session.
//
// It is the test's stand-in for the helper's proxied channel, and it is
// deliberately the dumbest possible one: no buffering, no goroutine, no
// policy — so a failure here is the capability's and not the stand-in's.
type subsystemStream struct {
	r      io.Reader
	w      io.WriteCloser
	closer io.Closer
}

func (s *subsystemStream) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *subsystemStream) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *subsystemStream) Close() error                { return s.closer.Close() }

// helperStreamTo opens a real sftp subsystem to the test server and answers it
// as a stream.
func helperStreamTo(t *testing.T, rc *RealClient, srv *fsTestServer) io.ReadWriteCloser {
	t.Helper()
	gcfg := &gossh.ClientConfig{
		User:            "test",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(srv.userSigner)},
		HostKeyCallback: gossh.FixedHostKey(srv.hostSigner.PublicKey()),
	}
	gclient, err := (&dialer{client: rc}).dialDirect(context.Background(), srv.addr, gcfg, srv.addr, "test")
	if err != nil {
		t.Fatalf("dial the fixture: %v", err)
	}
	sess, err := gclient.NewSession()
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if subErr := sess.RequestSubsystem("sftp"); subErr != nil {
		t.Fatalf("request sftp subsystem: %v", subErr)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	return &subsystemStream{r: stdout, w: stdin, closer: sess}
}

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

	conn, err := NewHelperInstallConn(context.Background(), helperStreamTo(t, rc, srv))
	if err != nil {
		t.Fatalf("NewHelperInstallConn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	removed, err := UninstallHelperTree(context.Background(), conn)
	if err != nil {
		t.Fatalf("UninstallHelperTree: %v", err)
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

	// A host with nothing installed uninstalls cleanly: a second click is
	// a no-op that succeeds, reported as such.
	removed, err = UninstallHelperTree(context.Background(), conn)
	if err != nil {
		t.Fatalf("second UninstallHelperTree on a bare host: %v", err)
	}
	if removed {
		t.Fatal("removed = true on a bare host, want false (nothing was there)")
	}
}

// TestAnInstallLeaseOverAStreamEndsWhenItsStreamEnds is the loss half of the
// lease, which used to be a *gossh.Client.Wait and is now the stream's own
// end.
//
// It matters because the lease's interface DECLARES Done and LostErr, and a
// declaration nothing can trigger is a promise the product reads as "this
// connection is alive" for as long as it holds the lease. The stream is ended
// by closing the session underneath it — the way a helper ending a channel ends
// it — and the lease must report the loss rather than going quiet.
func TestAnInstallLeaseOverAStreamEndsWhenItsStreamEnds(t *testing.T) {
	srv := startFSTestServer(t, fsModeReal)
	rc := fsTestClient(t, srv)
	stream := helperStreamTo(t, rc, srv)

	conn, err := NewHelperInstallConn(context.Background(), stream)
	if err != nil {
		t.Fatalf("NewHelperInstallConn: %v", err)
	}
	defer func() { _ = conn.Close() }()

	select {
	case <-conn.Done():
		t.Fatal("the lease reported loss before anything ended")
	default:
	}

	// End the underlying session the way a helper's channel ends: the far side
	// goes away, and the next read or write observes it.
	if err := stream.Close(); err != nil {
		t.Fatalf("close the stream: %v", err)
	}
	// A CALL is what observes the end — the lease's signal is the stream's own
	// error, and the stream is only touched by the sftp client under a call.
	if _, err := conn.Home(); err != nil {
		t.Logf("home after the stream ended: %v", err)
	}
	select {
	case <-conn.Done():
	default:
		t.Fatal("the lease did not report the end of its stream: Done is still open, so a caller would treat a dead lease as alive")
	}
	if conn.LostErr() == nil {
		t.Fatal("LostErr is nil after the stream ended, so the loss has no cause to report")
	}
}

// TestAnInstallLeaseOverANilStreamIsRefused: the constructor's own gate. A nil
// stream is a wiring bug, and a lease that accepted one would hold a nil
// interface whose first method call panics in whichever goroutine gets there
// first — which for an install is the middle of writing somebody's home.
func TestAnInstallLeaseOverANilStreamIsRefused(t *testing.T) {
	if _, err := NewHelperInstallConn(context.Background(), nil); err == nil {
		t.Fatal("NewHelperInstallConn(nil) was accepted")
	}
}
