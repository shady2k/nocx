package local

// The filesystem as a transport (L2). deploy.RemoteFS was named for the case
// it was built for and is not remote in anything but its name: it is a narrow
// write surface with lstat semantics, explicit modes and a separately
// fault-injectable write, sync and close. That is exactly what os gives, so
// this file is an adapter and nothing more — no local install semantics live
// here, because a second set of them is a second answer to "which build is
// serving".
//
// Two properties the seam states and cannot check, honoured here rather than
// assumed:
//
//   - MODES ARE SET AT CREATION and never left to the umask. os applies the
//     process umask to Mkdir and OpenFile, which is inherited from whoever
//     started us, so each is followed by an explicit chmod. Only on a path we
//     ourselves created: chmod on a directory that was already there would
//     narrow somebody's home to 0700 on the way past it.
//   - NO PATH IS FOLLOWED THROUGH A SYMLINK. Lstat and the ReadDir entries are
//     lstat throughout, so a symlink under the install root is removed as the
//     link rather than traversed.

import (
	"io/fs"
	"os"

	"github.com/shady2k/nocx/internal/helper/deploy"
)

// FS is deploy.RemoteFS over the real filesystem of this machine.
type FS struct{}

// Compile-time proof that the local carrier gives the installer exactly what
// the sftp carrier gives it. If this stops holding, the two transports have
// stopped being two transports for one installer.
var _ deploy.RemoteFS = FS{}

func (FS) Lstat(p string) (fs.FileInfo, error) { return os.Lstat(p) }

// Mkdir creates one directory with the mode the installer asked for. An
// existing directory comes back as fs.ErrExist, which is what mkdirAll walks
// past — two installs racing on one machine is ordinary.
func (FS) Mkdir(p string, mode fs.FileMode) error {
	if err := os.Mkdir(p, mode); err != nil {
		return err
	}
	return os.Chmod(p, mode)
}

// Create makes a file at mode, truncating whatever was there. It is not
// exclusive, deliberately: the installer's temporary name is per-attempt and
// its final rename is last-writer-wins over byte-identical content, so an
// exclusive create would refuse a race that has no bad outcome.
func (FS) Create(p string, mode fs.FileMode) (deploy.File, error) {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode) // #nosec G304 — p is the installer's own content-addressed path
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// SyncDir makes a directory's own entries durable — the step that turns "the
// bytes are written" into "the name that finds them survives a power cut", and
// the reason the marker is written after it.
func (FS) SyncDir(p string) error {
	d, err := os.Open(p) // #nosec G304 — p is the installer's own content-addressed path
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

func (FS) Rename(src, dst string) error { return os.Rename(src, dst) }

func (FS) Remove(p string) error { return os.Remove(p) }

// ReadDir reports entries with lstat semantics: a symlink is reported as a
// symlink, so the installer's removal takes the link and never what it points
// at.
func (FS) ReadDir(p string) ([]fs.FileInfo, error) {
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := make([]fs.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			// The entry vanished between the listing and the stat, or it
			// cannot be stat'ed. Either way it is not something to remove or
			// keep on a guess: the caller gets the failure.
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

func (FS) ReadFile(p string) ([]byte, error) {
	return os.ReadFile(p) // #nosec G304 — p is the installer's own content-addressed path
}
