package update

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
)

// flock is an advisory file lock held on an open file descriptor.
// The kernel releases it when the process dies, however it dies —
// unlike an O_EXCL sentinel file that survives SIGKILL (§7.6).
type flock struct {
	f    *os.File
	path string
}

// acquireLock acquires an exclusive advisory flock, blocking until
// the lock is available.
func acquireLock(path string) (*flock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o644) //nolint:gosec // path is caller-controlled, permission is read-only
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock LOCK_EX %s: %w", path, err)
	}
	return &flock{f: f, path: path}, nil
}

// tryLock attempts to acquire an exclusive advisory flock with a
// bounded timeout. It returns nil, nil if the lock cannot be
// acquired within the timeout (another process is mid-update).
func tryLock(ctx context.Context, path string) (*flock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o644) //nolint:gosec // path is caller-controlled, permission is read-only
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}

	// Use LOCK_NB and poll with a short sleep.
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &flock{f: f, path: path}, nil
		}
		if !isWouldBlock(err) {
			_ = f.Close()
			return nil, fmt.Errorf("flock LOCK_EX|LOCK_NB %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, nil // timeout — not an error
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, nil // context cancelled — treat as timeout
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// release releases the flock and closes the underlying file.
//
// The lock file is deliberately NOT unlinked. Unlinking breaks
// flock mutual exclusion: a blocked waiter can hold the lock on an
// inode that has been unlinked while a new process O_CREATes a fresh
// lock file at the same path and acquires a second lock — two
// concurrent updaters, exactly the failure the lock exists to
// prevent. The lock file is a persistent, stable-inode mutex.
func (l *flock) release() error {
	if l == nil {
		return nil
	}
	err1 := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err2 := l.f.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func isWouldBlock(err error) bool {
	if errno, ok := err.(syscall.Errno); ok {
		return errno == syscall.EAGAIN || errno == syscall.EWOULDBLOCK
	}
	return false
}
