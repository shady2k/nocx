package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const LeaseDirectory = ".leases"

var (
	ErrLeaseBusy   = errors.New("runtime tree is leased")
	ErrLeaseClosed = errors.New("runtime lease is closed")
)

// Lease owns one descriptor of a stable-inode flock. The files live OUTSIDE
// immutable runtime trees and are never unlinked, including after pruning.
// Close deliberately never calls LOCK_UN: exec-inherited descriptors share
// this open-file description and must keep the lock after their parent dies.
type Lease struct {
	mu        sync.Mutex
	file      *os.File
	digest    string
	exclusive bool
}

func OwnedByCurrentUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Geteuid())
}

func privateDirectory(name string) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !OwnedByCurrentUser(info) {
		return fmt.Errorf("%w: runtime directory is not private and owned", ErrInvalid)
	}
	return nil
}

func leasePath(parent, digest string, create bool) (string, error) {
	if !filepath.IsAbs(parent) || !ValidDigest(digest) {
		return "", ErrInvalid
	}
	if err := privateDirectory(parent); err != nil {
		return "", err
	}
	dir := filepath.Join(parent, LeaseDirectory)
	if create {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	if err := privateDirectory(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, digest+".lock"), nil
}

func privateLock(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1 && info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && info.Size() == 0 && OwnedByCurrentUser(info)
}

func openLease(parent, digest string, exclusive bool) (*Lease, error) {
	name, err := leasePath(parent, digest, true)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600) //nolint:gosec // leasePath binds a hex digest beneath private owned directories; O_NOFOLLOW and inode checks reject substitution
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	named, nameErr := os.Lstat(name)
	if statErr != nil || nameErr != nil || !privateLock(info) || !privateLock(named) || !os.SameFile(info, named) {
		_ = file.Close()
		return nil, fmt.Errorf("%w: unsafe runtime lease file", ErrInvalid)
	}
	return &Lease{file: file, digest: digest, exclusive: exclusive}, nil
}

func busy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

// AcquireShared waits only for an in-progress prune. Cancellation never hands
// a caller an unleased tree. Uncontended acquisition allocates no timer.
func AcquireShared(ctx context.Context, parent, digest string) (*Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lease, err := openLease(parent, digest, false)
	if err != nil {
		return nil, err
	}
	attempt := func() error { return syscall.Flock(int(lease.file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) }
	if err = attempt(); err == nil {
		return lease, nil
	} else if !busy(err) {
		_ = lease.Close()
		return nil, err
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = lease.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
		if err = attempt(); err == nil {
			return lease, nil
		} else if !busy(err) {
			_ = lease.Close()
			return nil, err
		}
	}
}

// TryExclusive is intentionally nonblocking. Busy and uncertain versions are
// retained rather than guessed unused from a PID or a stale timestamp.
func TryExclusive(parent, digest string) (*Lease, error) {
	lease, err := openLease(parent, digest, true)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lease.file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lease.Close()
		if busy(err) {
			return nil, ErrLeaseBusy
		}
		return nil, err
	}
	return lease, nil
}

func (l *Lease) File() *os.File {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file
}

func (l *Lease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// validateShared does not take ownership on failure. The inode comparison
// also permits ordinary parent-directory aliases (for example macOS /var).
func (l *Lease) validateShared(root, digest string) error {
	if l == nil || !ValidDigest(digest) || filepath.Base(root) != digest || !filepath.IsAbs(root) {
		return ErrInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return ErrLeaseClosed
	}
	if l.exclusive || l.digest != digest {
		return ErrInvalid
	}
	if err := privateDirectory(root); err != nil {
		return err
	}
	name, err := leasePath(filepath.Dir(root), digest, false)
	if err != nil {
		return err
	}
	info, err := l.file.Stat()
	if err != nil {
		return err
	}
	named, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !privateLock(info) || !privateLock(named) || !os.SameFile(info, named) {
		return ErrInvalid
	}
	if err = syscall.Flock(int(l.file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if busy(err) {
			return ErrLeaseBusy
		}
		return err
	}
	return nil
}

// inheritedLease adopts only a validated FD. A malformed environment must not
// close an unrelated descriptor already owned by the coordinator. Once bound,
// CLOEXEC keeps it out of ordinary tool subprocesses; Python receives it only
// through its explicitly configured ExtraFiles entry.
func inheritedLease(fd int, root, digest string) (*Lease, error) {
	if fd < 3 || !ValidDigest(digest) || filepath.Base(root) != digest {
		return nil, ErrInvalid
	}
	if err := privateDirectory(root); err != nil {
		return nil, err
	}
	name, err := leasePath(filepath.Dir(root), digest, false)
	if err != nil {
		return nil, err
	}
	named, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !privateLock(named) {
		return nil, ErrInvalid
	}
	var actual syscall.Stat_t
	if err = syscall.Fstat(fd, &actual); err != nil {
		return nil, err
	}
	wanted, ok := named.Sys().(*syscall.Stat_t)
	if !ok || actual.Dev != wanted.Dev || actual.Ino != wanted.Ino {
		return nil, ErrInvalid
	}
	if _, err = unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC); err != nil {
		return nil, err
	}
	if err = syscall.Flock(fd, syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if busy(err) {
			return nil, ErrLeaseBusy
		}
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		return nil, ErrInvalid
	}
	return &Lease{file: file, digest: digest}, nil
}
