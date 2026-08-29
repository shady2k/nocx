package coordinator

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// fileLock is an exclusive advisory flock held on an open descriptor. The
// kernel releases it when the process dies however it dies, which an O_EXCL
// sentinel file does not — a SIGKILLed daemon would otherwise leave a
// pidfile nobody can prove is stale.
//
// internal/update/lock.go holds the same shape for the updater and is where
// the "never unlink the lock file" reasoning below was first written down.
// It is not reused because every one of its symbols is unexported and that
// package is another task's; a second small copy beside the socket it
// guards is the cheaper of the two wrongs. If a third caller appears, the
// answer is to lift one of these into a package both import, not to write a
// third.
type fileLock struct {
	f *os.File
}

// acquireLock takes the exclusive lock without blocking. It returns
// [ErrAlreadyRunning] when somebody else holds it, which is the whole point:
// a second nocx-server against one app directory must refuse to start, not
// queue behind the incumbent and become a second daemon when it exits.
func acquireLock(path string) (*fileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // app-owned runtime path, never caller input
	if err != nil {
		return nil, fmt.Errorf("coordinator: open lock %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("coordinator: lock %s: %w", path, err)
	}
	return &fileLock{f: f}, nil
}

// release drops the lock and closes the descriptor.
//
// The lock file is deliberately NOT unlinked. Unlinking breaks flock's
// mutual exclusion: a waiter can hold the lock on an inode that has been
// unlinked while a new process O_CREATes a fresh file at the same path and
// locks that one — two daemons, which is exactly what the lock exists to
// prevent. The file is a persistent, stable-inode mutex and holds nothing
// but its own existence.
func (l *fileLock) release() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	closeErr := l.f.Close()
	l.f = nil
	if unlockErr != nil {
		return fmt.Errorf("coordinator: unlock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("coordinator: close lock: %w", closeErr)
	}
	return nil
}
