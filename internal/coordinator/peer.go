package coordinator

import (
	"fmt"
	"net"
	"os"
)

// PeerCredentials answers who is on the other end of an accepted
// connection. It is an interface for one reason: an unprivileged test
// cannot arrange for a second account to connect, so the refusal path —
// the security control this file exists for — would otherwise have no test
// at all (AD-8, and AGENTS.md testing rule 3).
//
// [SystemPeerCredentials] is the real answer and is what the composition
// root wires; its own "and on an ordinary machine it succeeds" test asserts
// the uid it reports is the one we are running as.
type PeerCredentials interface {
	// PeerUID returns the uid of the process that opened conn.
	PeerUID(conn *net.UnixConn) (uint32, error)
}

// SystemPeerCredentials asks the kernel. The question has a different
// spelling on each platform — SO_PEERCRED on Linux, getpeereid(3) on
// darwin — so the answer lives in a per-OS file pair, the shape
// internal/contentkey uses for the same kind of problem.
type SystemPeerCredentials struct{}

// PeerUID reports the uid the kernel recorded when the peer connected.
//
// The kernel records it at connect time and it cannot be restated by the
// peer afterwards, which is the whole reason the trust boundary is here
// rather than on the TCP side.
func (SystemPeerCredentials) PeerUID(conn *net.UnixConn) (uint32, error) {
	if conn == nil {
		return 0, fmt.Errorf("coordinator: peer uid: no connection")
	}
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("coordinator: peer uid: %w", err)
	}
	var uid uint32
	var opErr error
	if ctrlErr := raw.Control(func(fd uintptr) {
		uid, opErr = peerUID(fd)
	}); ctrlErr != nil {
		return 0, fmt.Errorf("coordinator: peer uid: %w", ctrlErr)
	}
	if opErr != nil {
		return 0, fmt.Errorf("coordinator: peer uid: %w", opErr)
	}
	return uid, nil
}

// PathOwner reports which uid owns a filesystem path.
//
// Separate from PeerCredentials, and an interface for the same reason: the
// refusal it guards — a runtime directory somebody else owns — is not
// something an unprivileged test can build out of real files.
type PathOwner interface {
	OwnerUID(path string) (uint32, error)
}

// SystemPathOwner stats the path. It does not follow symlinks: a symlink
// the attacker owns pointing at a directory we own would otherwise report
// our uid and pass the check the caller is making.
type SystemPathOwner struct{}

// OwnerUID returns the uid owning path, without following a final symlink.
func (SystemPathOwner) OwnerUID(path string) (uint32, error) {
	uid, err := ownerUID(path)
	if err != nil {
		return 0, fmt.Errorf("coordinator: owner of %s: %w", path, err)
	}
	return uid, nil
}

// SelfUID is the uid this process runs as — the value every peer uid and
// every directory owner is compared against.
//
// It exists so that the conversion happens once. os.Getuid returns an int
// and everything that compares uids uses uint32, and a cast repeated at
// each call site is a cast that has to be argued about at each call site.
func SelfUID() uint32 {
	return uint32(os.Getuid()) //nolint:gosec // a uid is a uint32 on every platform this builds for
}
