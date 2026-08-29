package serverbin

// The real filesystem behind [FS]. Nothing here decides anything — every
// method is one syscall — so the package's behaviour is exercised against
// the fake and this file is what the round-trip test runs against.

import (
	"io"
	iofs "io/fs"
	"os"
)

type osFS struct{}

// NewOSFS returns the [FS] backed by the local filesystem.
func NewOSFS() FS { return osFS{} }

func (osFS) Stat(path string) (iofs.FileInfo, error) { return os.Stat(path) }

func (osFS) MkdirAll(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }

func (osFS) Open(path string) (io.ReadCloser, error) {
	return os.Open(path) //nolint:gosec // the caller names the path; this seam has no policy of its own
}

func (osFS) Create(path string, mode os.FileMode) (File, error) {
	// O_EXCL: the temporary name carries a nonce, so an existing file
	// under it is not a race we should write through.
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // ditto
}

func (osFS) Rename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }

func (osFS) Remove(path string) error { return os.Remove(path) }

func (osFS) ReadDir(path string) ([]iofs.DirEntry, error) { return os.ReadDir(path) }

// SyncDir fsyncs a directory so a rename into it is durable across a power
// cut — the same step writeJournal takes for the same reason.
func (osFS) SyncDir(path string) error {
	d, err := os.Open(path) //nolint:gosec // caller-named directory
	if err != nil {
		return err
	}
	serr := d.Sync()
	cerr := d.Close()
	if serr != nil {
		return serr
	}
	return cerr
}
