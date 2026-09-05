package coordinator

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

// PrepareRuntimeDir makes the runtime directory, forces 0700 on it and
// refuses it if it is not ours.
//
// The chmod is not redundant with the MkdirAll mode: MkdirAll applies the
// umask, and a directory left over from an earlier version — or from
// somebody's tar — carries whatever mode it was created with. The mode is
// asserted rather than assumed on every start.
func PrepareRuntimeDir(dir string, owner PathOwner, selfUID uint32) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("coordinator: create runtime dir %s: %w", dir, err)
	}
	//nolint:gosec // 0700 IS the mode this directory must carry: a directory
	// needs its execute bit to be entered at all, and 0600 would make the
	// socket inside unreachable to its own owner.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("coordinator: set mode on runtime dir %s: %w", dir, err)
	}
	uid, err := owner.OwnerUID(dir)
	if err != nil {
		return err
	}
	if uid != selfUID {
		return fmt.Errorf("%w: %s is owned by uid %d, we are uid %d", ErrForeignOwner, dir, uid, selfUID)
	}
	return nil
}

// BindSocket creates a Unix listener on name in dir. It checks the final path,
// binds through a temporary same-directory name and publishes atomically.
func BindSocket(dir, name string) (*net.UnixListener, error) {
	socket := filepath.Join(dir, name)
	if len(socket) > maxSocketPath {
		return nil, fmt.Errorf("%w: %d bytes at %s", ErrPathTooLong, len(socket), socket)
	}
	if err := checkSocketPath(socket); err != nil {
		return nil, err
	}

	tmp := filepath.Join(dir, "."+name+"."+strconv.Itoa(os.Getpid()))
	if len(tmp) > maxSocketPath {
		return nil, fmt.Errorf("%w: %d bytes at %s", ErrPathTooLong, len(tmp), tmp)
	}
	// A previous crash may have left this exact name behind; it is ours by
	// construction (our pid), so removing it cannot take anybody else's.
	if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("coordinator: clear stale bind name %s: %w", tmp, err)
	}

	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: tmp, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("coordinator: bind %s: %w", tmp, err)
	}
	// The listener knows the temporary name, not the final one, so it must
	// not unlink on close — that would delete a name it no longer owns
	// while leaving the real socket behind. Close does the unlink.
	listener.SetUnlinkOnClose(false)
	// Bind applies the umask, so the mode is set explicitly. The window
	// between the two is not reachable by another user: the parent
	// directory is already 0700 and ours.
	if err := os.Chmod(tmp, 0o600); err != nil {
		return nil, abandonBind(listener, tmp, fmt.Errorf("coordinator: set mode on socket: %w", err))
	}
	if err := os.Rename(tmp, socket); err != nil {
		return nil, abandonBind(listener, tmp, fmt.Errorf("coordinator: publish socket at %s: %w", socket, err))
	}
	return listener, nil
}

// checkSocketPath decides whether the path may be bound over.
func checkSocketPath(socket string) error {
	fi, err := os.Lstat(socket)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("coordinator: inspect socket path %s: %w", socket, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlinkPath, socket)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: %s is %s", ErrOccupiedPath, socket, fi.Mode())
	}
	return nil
}

// abandonBind closes a listener that will never serve and removes the name
// it was bound to, so a failed start leaves the directory as it found it.
func abandonBind(listener *net.UnixListener, tmp string, cause error) error {
	_ = listener.Close()
	_ = os.Remove(tmp)
	return cause
}
