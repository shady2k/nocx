package shellintegration

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
)

// FS is the filesystem-shaped seam a carrier implements (AD-8: one owner of
// the publish behaviour, variation lives in adapters). The publisher holds
// no SSH, SFTP or launcher knowledge: the SFTP carrier (P8) and the
// self-installing launcher (P6) implement this interface over their own
// transports, and the fault-injection tests inject failures through the same
// interface real carriers implement.
//
// Every method is a publish boundary: mkdir, each file write, each fsync,
// each rename, lock acquire (Mkdir of the lock dir) and lock release
// (Remove of it) are separately injectable failures. Implementations must
// never follow a symlink and must not leave modes subject to umask.
type FS interface {
	// Lstat reports info about path without following a final symlink.
	// Absence is reported as an error wrapping fs.ErrNotExist.
	Lstat(path string) (fs.FileInfo, error)

	// Mkdir creates a single directory at path with mode, failing with an
	// error wrapping fs.ErrExist when it already exists. The mode must be
	// applied exactly, never left to umask.
	Mkdir(path string, mode os.FileMode) error

	// Create opens path for writing, creating it if absent and truncating
	// it if present, with the mode applied exactly (not left to umask).
	// The returned File is the write boundary: Write, Sync and Close are
	// separate fault-injectable steps.
	Create(path string, mode os.FileMode) (File, error)

	// SyncDir fsyncs a directory so its entries survive a crash.
	// Transports without directory fsync (SFTP) may no-op; durability
	// scope is stated, not assumed (design §4).
	SyncDir(path string) error

	// Rename atomically moves src to dst within the same filesystem.
	Rename(src, dst string) error

	// Remove deletes a single file or empty directory; it is not
	// recursive. Absence is tolerated via fs.ErrNotExist.
	Remove(path string) error

	// ReadDir lists the entries of dir using lstat semantics (a symlink
	// entry is reported as a symlink, never followed).
	ReadDir(path string) ([]fs.FileInfo, error)

	// ReadFile returns the whole content of path.
	ReadFile(path string) ([]byte, error)
}

// File is a handle returned by FS.Create.
type File interface {
	io.Writer
	Sync() error
	Close() error
}

// osFS is the production FS over the local filesystem. The self-installing
// launcher runs on the remote host with this implementation; the SFTP
// carrier provides its own.
type osFS struct{}

// NewOSFS returns the production FS implementation over os.*.
func NewOSFS() FS { return osFS{} }

func (osFS) Lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }

// Mkdir creates the directory and then chmods it: os.Mkdir applies the
// process umask, which would silently widen 0700 into 0755 on a permissive
// machine ("modes are set at creation, never left to umask").
func (osFS) Mkdir(path string, mode os.FileMode) error {
	if err := os.Mkdir(path, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// Create opens the file and then chmods it for the same umask reason.
func (osFS) Create(path string, mode os.FileMode) (File, error) {
	// #nosec G304 — the publisher hands osFS only root-joined paths; tests hand it fixtures; no external input.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (osFS) SyncDir(path string) error {
	d, err := os.Open(path) // #nosec G304 — osFS opens only publisher-created directories or test fixtures.
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		// Some filesystems (and macOS) do not support fsync on a
		// directory. Durability scope is stated, not assumed (design §4):
		// where the filesystem cannot honour a directory fsync we say so
		// by degrading to the file-level fsyncs, not by failing forever.
		if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
			return nil
		}
		return err
	}
	return nil
}

func (osFS) Rename(src, dst string) error { return os.Rename(src, dst) }

func (osFS) Remove(path string) error { return os.Remove(path) }

func (osFS) ReadDir(path string) ([]fs.FileInfo, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	return dirInfos(entries)
}

// dirInfos resolves the listed entries to lstat results. A name that another
// publisher removed between the listing and its lstat is skipped, not an
// error: two invocations share this root, and one finishing its publish
// removes its lock and staging entries while the other is still reading the
// directory (nocx-1ebrs.66.1.34). An entry that no longer exists is not
// foreign data and not an unreadable directory, so the read of the directory
// succeeded. Every other failure is real and returned.
func dirInfos(entries []fs.DirEntry) ([]fs.FileInfo, error) {
	infos := make([]fs.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info() // lstat semantics: a symlink entry reports the link
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// #nosec G304 — osFS reads only manifest-named files under the validated root, or test fixtures.
func (osFS) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
