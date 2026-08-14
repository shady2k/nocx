package ssh

// The write-capable SFTP filesystem adapter: mkdir+chmod, create+chmod,
// rename, removal, fsync tolerance and SFTP status translation, moved out
// of the shell-integration package so ONE implementation serves both the
// shell bundle's publisher and the helper-install lease (remote-helper
// design D7). Modes are set at creation and never left to the server's
// umask, and no path is followed through a symlink — lstat semantics
// throughout (the publisher's design §4.1).

import (
	"errors"
	"io"
	iofs "io/fs"
	"os"

	"github.com/pkg/sftp"
)

// File is the write boundary SFTPFS.Create returns: Write, Sync and Close
// are separate fault-injectable steps.
type File interface {
	io.Writer
	Sync() error
	Close() error
}

// SFTPFS is the FS seam adapter over an *sftp.Client. The shell-integration
// publisher and the helper-install lease both consume it; neither holds any
// SFTP knowledge of its own (AD-8: one owner of the publish behaviour,
// variation lives in adapters).
type SFTPFS struct {
	client *sftp.Client
}

// NewSFTPFS wraps an *sftp.Client with the FS seam.
func NewSFTPFS(client *sftp.Client) *SFTPFS { return &SFTPFS{client: client} }

func (f *SFTPFS) Lstat(path string) (iofs.FileInfo, error) { return f.client.Lstat(path) }

// Mkdir creates the directory and then chmods it: the SFTP server applies
// its own umask to the requested mode, which would silently widen 0700 into
// 0755 on a permissive host ("modes are set at creation, never left to
// umask"). The SFTP protocol answers EEXIST as a generic failure, so a path
// that already exists as a directory is translated to fs.ErrExist — the
// caller's concurrent-create race tolerance reads it through that error.
func (f *SFTPFS) Mkdir(path string, mode os.FileMode) error {
	if err := f.client.Mkdir(path); err != nil {
		if isSFTPStatus(err, uint32(sftp.ErrSSHFxFailure)) {
			if info, lerr := f.client.Lstat(path); lerr == nil && info.IsDir() {
				return iofs.ErrExist
			}
		}
		return err
	}
	return f.client.Chmod(path, mode)
}

// Create opens the file for writing (truncating if present) and then chmods
// it for the same umask reason. The returned File is the write boundary:
// Write, Sync and Close are separate fault-injectable steps.
func (f *SFTPFS) Create(path string, mode os.FileMode) (File, error) {
	fh, err := f.client.Create(path)
	if err != nil {
		return nil, err
	}
	if err := fh.Chmod(mode); err != nil {
		_ = fh.Close()
		return nil, err
	}
	return &sftpFile{File: fh}, nil
}

// SyncDir no-ops: SFTP has no directory fsync, and the seam's own carve-out
// says transports without it may no-op. Durability scope is stated, not
// assumed (design §4).
func (f *SFTPFS) SyncDir(string) error { return nil }

func (f *SFTPFS) Rename(src, dst string) error { return f.client.Rename(src, dst) }

// Remove deletes a single file, retrying as a directory removal when
// SSH_FXP_REMOVE refuses (the publisher removes the empty lock directory
// this way). Absence is reported through fs.ErrNotExist, which the caller
// tolerates.
func (f *SFTPFS) Remove(path string) error {
	err := f.client.Remove(path)
	if err == nil || errors.Is(err, iofs.ErrNotExist) {
		return err
	}
	if rerr := f.client.RemoveDirectory(path); rerr != nil {
		return err
	}
	return nil
}

// ReadDir lists the entries of dir with lstat semantics: the SFTP server
// fills attrs from readdir(3), so a symlink entry is reported as a symlink,
// never followed.
func (f *SFTPFS) ReadDir(path string) ([]iofs.FileInfo, error) { return f.client.ReadDir(path) }

func (f *SFTPFS) ReadFile(path string) ([]byte, error) {
	fh, err := f.client.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = fh.Close() }()
	return io.ReadAll(fh)
}

// RealPath resolves path to the server's canonical absolute form — the
// helper-install lease's Home() uses it for "." (the SFTP session's
// starting directory is the remote account's home).
func (f *SFTPFS) RealPath(path string) (string, error) { return f.client.RealPath(path) }

// sftpFile adapts *sftp.File to the File boundary.
type sftpFile struct {
	*sftp.File
}

// Sync requests a flush to stable storage when the server advertises the
// fsync@openssh.com extension. The SFTP protocol has no mandatory fsync: a
// server without the extension answers OP_UNSUPPORTED, and durability is
// then the server's promise — stated, not assumed (design §4). Every other
// failure is a real publish boundary.
func (f *sftpFile) Sync() error {
	err := f.File.Sync()
	if err == nil || isSFTPStatus(err, uint32(sftp.ErrSSHFxOpUnsupported)) {
		return nil
	}
	return err
}

// isSFTPStatus reports whether err is an SFTP status reply carrying the
// given protocol code. The SFTP protocol answers EEXIST and directory
// removal refusals as generic failures, so the adapter maps those to the fs
// semantics its callers expect.
func isSFTPStatus(err error, code uint32) bool {
	var se *sftp.StatusError
	return errors.As(err, &se) && se.Code == code
}
