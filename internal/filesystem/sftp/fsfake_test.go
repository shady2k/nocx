package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/shady2k/nocx/internal/transfer"
)

// errLeaseClosed is the fake's post-Close error, standing in for the lease's
// ErrFSClosed: it is not fs-shaped, so the provider's wrapPathErr passes it
// through untouched.
var errLeaseClosed = errors.New("sftp: lease closed")

// fakeFS is the transport double for this package's tests: a stand-in for the
// SFTP lease (the fsConn seam), backed by the real local filesystem so the
// provider is exercised over real file semantics — symlinks, permissions,
// FIFOs, mtimes — without a live SSH connection. It models the two halves of
// the lease contract: ReadDir is context-cancellable (ReadDirContext
// semantics: it checks the context before the first packet and between
// packets), and everything else is not.
//
// ReadFile mirrors the committed lease's implementation byte for byte,
// including its error handling: an empty file surfaces as io.EOF (the seam's
// defect, escalated; the provider passes it through).
type fakeFS struct {
	t    *testing.T
	root string // the served SFTP root; RealPath(".") resolves here

	// blockedReadDir makes ReadDir wait for ctx and return ctx.Err() — the
	// never-replying server shape: a listing against this fake can only be
	// released by its context, exactly the D14 cap's native cancellation.
	blockedReadDir bool
	// readFileErr is an injected transport-level ReadFile failure (not
	// fs-shaped), for the pass-through test.
	readFileErr error
	// realPathErr is an injected failure of the home resolution — the
	// paired failure of Root's external call.
	realPathErr error

	// posixRenameErr injects a promote failure, including the
	// capability answer the sink's fallback keys on.
	posixRenameErr error

	closed bool
}

func newFakeFS(t *testing.T) *fakeFS {
	t.Helper()
	return &fakeFS{t: t, root: tempDir(t)}
}

func (f *fakeFS) ReadDir(ctx context.Context, p string) ([]os.FileInfo, error) {
	if f.closed {
		return nil, errLeaseClosed
	}
	if f.blockedReadDir {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	des, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	infos := make([]os.FileInfo, 0, len(des))
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			return nil, err
		}
		infos = append(infos, fi)
	}
	return infos, nil
}

func (f *fakeFS) Stat(p string) (os.FileInfo, error) {
	if f.closed {
		return nil, errLeaseClosed
	}
	return os.Stat(p)
}

func (f *fakeFS) Lstat(p string) (os.FileInfo, error) {
	if f.closed {
		return nil, errLeaseClosed
	}
	return os.Lstat(p)
}

func (f *fakeFS) RealPath(p string) (string, error) {
	if f.closed {
		return "", errLeaseClosed
	}
	if p == "." {
		if f.realPathErr != nil {
			return "", f.realPathErr
		}
		return f.root, nil
	}
	return filepath.EvalSymlinks(p)
}

func (f *fakeFS) ReadLink(p string) (string, error) {
	if f.closed {
		return "", errLeaseClosed
	}
	return os.Readlink(p)
}

// ReadFile mirrors the lease's ReadFile contract: reads at most maxBytes,
// probing one byte past the bound so truncated says whether more data
// remains; a file that ends exactly at the bound is not truncated.
func (f *fakeFS) ReadFile(ctx context.Context, p string, maxBytes int64) ([]byte, bool, error) {
	if f.closed {
		return nil, false, errLeaseClosed
	}
	if f.readFileErr != nil {
		return nil, false, f.readFileErr
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	fh, err := os.Open(p) // #nosec G304 — the fake opens exactly the fixture path the test under it built from tempDir(t)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = fh.Close() }()
	buf := make([]byte, maxBytes+1)
	n, err := io.ReadFull(fh, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, false, err
	}
	truncated := int64(n) > maxBytes
	if truncated {
		n = int(maxBytes)
	}
	return buf[:n], truncated, nil
}

// --- the read-stream half (transfer.RemoteReadFS) ---------------------------

// Open mirrors the lease's Open contract: it refuses anything that is not a
// regular file BEFORE opening it (a fifo with no writer blocks on open), and
// it measures the size on the OPEN handle rather than on the name.
func (f *fakeFS) Open(p string) (transfer.RemoteReader, int64, error) {
	if f.closed {
		return nil, 0, errLeaseClosed
	}
	byName, err := os.Stat(p)
	if err != nil {
		return nil, 0, err
	}
	if !byName.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%w: %s", transfer.ErrNotRegular, p)
	}
	fh, err := os.Open(p) // #nosec G304 — the fake opens exactly the fixture path the test under it built from tempDir(t)
	if err != nil {
		return nil, 0, err
	}
	info, err := fh.Stat()
	if err != nil {
		_ = fh.Close()
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		_ = fh.Close()
		return nil, 0, fmt.Errorf("%w: %s", transfer.ErrNotRegular, p)
	}
	return fh, info.Size(), nil
}

// --- the write half (transfer.RemoteFS) -------------------------------------
//
// Backed by the real local filesystem like the read half, so the provider's
// sink is exercised over real file semantics — O_EXCL refusing an existing
// path, a rename that replaces, a missing path answering fs.ErrNotExist —
// with no live server anywhere.

// Create is O_WRONLY|O_CREATE|O_EXCL, the lease's contract and NOT
// sftp.Client.Create: it refuses an existing path rather than truncating it
// (design D5).
func (f *fakeFS) Create(p string) (transfer.RemoteFile, error) {
	if f.closed {
		return nil, errLeaseClosed
	}
	fh, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 — the fake writes exactly the fixture path the test under it built from tempDir(t)
	if err != nil {
		return nil, err
	}
	return fh, nil
}

// PosixRename replaces the destination atomically, which os.Rename does on
// every platform this suite runs on.
func (f *fakeFS) PosixRename(old, new string) error {
	if f.closed {
		return errLeaseClosed
	}
	if f.posixRenameErr != nil {
		return f.posixRenameErr
	}
	return os.Rename(old, new)
}

// Rename is plain SFTP v3 rename: it refuses an existing destination, which
// os.Rename does not, so the refusal is modelled here. A missing source
// answers fs.ErrNotExist, the contract the sink's "nothing to back up"
// branch keys on.
func (f *fakeFS) Rename(old, new string) error {
	if f.closed {
		return errLeaseClosed
	}
	if _, err := os.Lstat(old); err != nil {
		return err
	}
	if _, err := os.Lstat(new); err == nil {
		return fmt.Errorf("rename %s: %w", new, fs.ErrExist)
	}
	return os.Rename(old, new)
}

func (f *fakeFS) Remove(p string) error {
	if f.closed {
		return errLeaseClosed
	}
	return os.Remove(p)
}

func (f *fakeFS) Close() error {
	f.closed = true
	return nil
}
