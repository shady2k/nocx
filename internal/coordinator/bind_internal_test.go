package coordinator

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// The two external calls inside bind — ListenUnix and Rename — are not
// reachable in a failing state through Start, because Start has already
// made the directory and forced it to 0700 and ours by the time bind runs.
// That is the design working, and it is also why these two failure paths
// are exercised here, against the unexported function, rather than through
// a production hook that would exist only for a test.

// shortTempDir is a temporary directory whose NAME is short.
//
// t.TempDir() embeds the test's own name, and macOS puts the whole thing
// under /var/folders/<two random components>/T/ — so a test called
// TestBindPublishesTheSocketOnAnOrdinaryDirectory produced a 126-byte socket
// path against a 104-byte sun_path, and bind refused it with ErrPathTooLong
// on the runner while passing on Linux, whose limit is 108 and whose /tmp is
// short (nocx-lvdj3). The name of the test may not decide whether the socket
// can be bound, so the directory does not carry it.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nocxbind")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newBindServer(t *testing.T, dir string) *Server {
	t.Helper()
	s, err := NewServer(Config{
		Dir:     dir,
		Build:   Build{Version: "0", Commit: "0"},
		Backend: stubBackend{},
		Peers:   SystemPeerCredentials{},
		Owner:   SystemPathOwner{},
		SelfUID: SelfUID(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s
}

type stubBackend struct{}

func (stubBackend) WSAddress() string { return "127.0.0.1:1" }
func (stubBackend) WSToken() string   { return "unused" }

func TestBindReportsAListenFailure(t *testing.T) {
	// A runtime directory that does not exist: ListenUnix answers ENOENT.
	dir := filepath.Join(shortTempDir(t), "absent")
	s := newBindServer(t, dir)

	if _, err := s.bind(); err == nil {
		t.Fatal("bind succeeded with no directory to bind in")
	}
}

func TestBindReportsARenameFailureAndLeavesNothingBehind(t *testing.T) {
	dir := shortTempDir(t)
	s := newBindServer(t, dir)
	// A non-empty directory at the socket path: rename(2) refuses to
	// replace it (ENOTEMPTY / EISDIR).
	if err := os.MkdirAll(filepath.Join(s.SocketPath(), "child"), 0o700); err != nil {
		t.Fatalf("stage blocker: %v", err)
	}

	_, err := s.bind()

	if err == nil {
		t.Fatal("bind succeeded over a non-empty directory")
	}
	// The temporary bind name must not survive a failed publish.
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("readdir: %v", readErr)
	}
	for _, e := range entries {
		if e.Name() != socketName {
			t.Errorf("a failed bind left %s behind", e.Name())
		}
	}
}

// And the same function on an ordinary directory succeeds, with the socket
// published at its final name and nothing else in the directory.
func TestBindPublishesTheSocketOnAnOrdinaryDirectory(t *testing.T) {
	dir := shortTempDir(t)
	s := newBindServer(t, dir)

	l, err := s.bind()
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer func() { _ = l.Close() }()

	fi, err := os.Lstat(s.SocketPath())
	if err != nil {
		t.Fatalf("lstat socket: %v", err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Errorf("published path is not a socket: %v", fi.Mode())
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("socket mode = %o, want 600", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != socketName {
		t.Errorf("directory holds %v, want only %s", entries, socketName)
	}
}

// A connection accepted after Close has begun is REFUSED rather than counted.
//
// This is the deterministic half of nocx-dy2mn. The race needed an accept to
// land inside Close's Wait, which is why CI found it on two runners and three
// runs on the dev host did not; the rule that removes it is assertable every
// run. trackConn takes the mutex that Close uses to set closed, so an Add
// either happened before the Wait or does not happen at all — a WaitGroup
// going from zero to one underneath a waiter is the misuse the detector was
// reporting.
func TestTrackConnRefusesOnceTheServerIsClosing(t *testing.T) {
	s := newBindServer(t, shortTempDir(t))

	if !s.trackConn() {
		t.Fatal("trackConn refused on an open server")
	}
	s.conns.Done()

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if s.trackConn() {
		// Counted here, the WaitGroup would be raced by the Wait that Close
		// has already returned from — and the connection would be served by
		// a daemon that has unlinked its own socket.
		s.conns.Done()
		t.Fatal("trackConn joined the wait group after Close; the connection must be dropped instead")
	}
}
