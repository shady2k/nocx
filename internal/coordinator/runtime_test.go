package coordinator_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shady2k/nocx/internal/coordinator"
)

func TestPrepareRuntimeDirRefusesForeignOwner(t *testing.T) {
	dir := t.TempDir()
	foreignUID := coordinator.SelfUID() + 1
	if err := coordinator.PrepareRuntimeDir(dir, fixedOwner{uid: foreignUID}, coordinator.SelfUID()); !errors.Is(err, coordinator.ErrForeignOwner) {
		t.Fatalf("PrepareRuntimeDir error = %v, want ErrForeignOwner", err)
	}
}

func TestBindSocketRefusesSymlinkPath(t *testing.T) {
	dir := t.TempDir()
	if err := coordinator.PrepareRuntimeDir(dir, coordinator.SystemPathOwner{}, coordinator.SelfUID()); err != nil {
		t.Fatalf("PrepareRuntimeDir: %v", err)
	}
	path := filepath.Join(dir, "wave.sock")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := coordinator.BindSocket(dir, "wave.sock"); !errors.Is(err, coordinator.ErrSymlinkPath) {
		t.Fatalf("BindSocket error = %v, want ErrSymlinkPath", err)
	}
}

func TestBindSocketRefusesNonSocketOccupant(t *testing.T) {
	dir := t.TempDir()
	if err := coordinator.PrepareRuntimeDir(dir, coordinator.SystemPathOwner{}, coordinator.SelfUID()); err != nil {
		t.Fatalf("PrepareRuntimeDir: %v", err)
	}
	path := filepath.Join(dir, "wave.sock")
	if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("write occupant: %v", err)
	}

	if _, err := coordinator.BindSocket(dir, "wave.sock"); !errors.Is(err, coordinator.ErrOccupiedPath) {
		t.Fatalf("BindSocket error = %v, want ErrOccupiedPath", err)
	}
}

func TestBindSocketRefusesOverlongPath(t *testing.T) {
	dir := filepath.Join(string(os.PathSeparator), strings.Repeat("x", 110))
	if _, err := coordinator.BindSocket(dir, "wave.sock"); !errors.Is(err, coordinator.ErrPathTooLong) {
		t.Fatalf("BindSocket error = %v, want ErrPathTooLong", err)
	}
}

func TestSystemPeerCredentialsReportsOwnPID(t *testing.T) {
	dir := shortDir(t)
	if prepareErr := coordinator.PrepareRuntimeDir(dir, coordinator.SystemPathOwner{}, coordinator.SelfUID()); prepareErr != nil {
		t.Fatalf("PrepareRuntimeDir: %v", prepareErr)
	}

	listener, err := coordinator.BindSocket(dir, "peer.sock")
	if err != nil {
		t.Fatalf("BindSocket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close(); _ = os.Remove(filepath.Join(dir, "peer.sock")) })

	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Join(dir, "peer.sock"), Net: "unix"})
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	defer func() { _ = conn.Close() }()

	pid, err := (coordinator.SystemPeerCredentials{}).PeerPID(conn)
	if err != nil {
		t.Fatalf("PeerPID: %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("PeerPID = %d, want %d", pid, os.Getpid())
	}
}
