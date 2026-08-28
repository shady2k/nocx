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
	dir := filepath.Join(t.TempDir(), "absent")
	s := newBindServer(t, dir)

	if _, err := s.bind(); err == nil {
		t.Fatal("bind succeeded with no directory to bind in")
	}
}

func TestBindReportsARenameFailureAndLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
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
	dir := t.TempDir()
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
