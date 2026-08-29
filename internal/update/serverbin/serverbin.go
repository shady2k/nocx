// Package serverbin installs the nocx coordinator binary at a versioned
// path and hands back the path to spawn it from.
//
// # Why a copy exists at all
//
// An AppImage is a squashfs image mounted through FUSE for exactly as long
// as the process that started it lives. When that process exits the mount
// goes, and every path inside it stops resolving. A coordinator whose whole
// purpose is to outlive the window (design §1) would therefore lose its own
// executable at the moment it must survive — the window it was spawned from
// is the thing that unmounts the filesystem the daemon is running out of.
//
// So on Linux the binary is copied out of the image on first run and the
// daemon is always spawned from the copy. macOS needs none of this: the
// .app is an ordinary directory and Contents/MacOS/nocx-server stays where
// it is, so the launcher spawns it in place (design §4, "Where the binary
// lives").
//
// # Why the path carries a version and a hash
//
// A stable mutable name — ~/.local/share/nocx/bin/nocx-server — is
// forbidden, and not as a matter of taste. Overwriting it is unsafe while a
// daemon is executing from it, so the copy would have to be skipped when
// the file already exists; and then an installation that has ever run once
// goes on spawning that first binary forever, through every update, with
// nothing anywhere reporting it. The failure is silent, permanent, and
// indistinguishable from the update having worked.
//
// A versioned, content-addressed name has no such state: a new build cannot
// collide with an old one, the old copy stays readable for the daemon still
// running out of it, and superseded copies are pruned when nothing needs
// them. The hash is what makes the name a claim about CONTENT rather than
// about a number somebody stamped — two builds of one version differ in
// their file names, and a truncated copy is not mistaken for the whole one.
//
// # Why this is not internal/helper/deploy
//
// That package owns the same shape and the resemblance is not accidental:
// versioned immutable installs, a content-hash key, install-then-promote,
// prune-everything-except-the-one-in-use. It was read first and this
// package follows its discipline deliberately, down to the pruning regex
// pinning the hash to 64 hex characters so a foreign file is never a
// candidate.
//
// It is not extended, for three reasons that are about what deploy IS
// rather than about how it is written:
//
//   - It is the REMOTE footprint. ~/.nocx/helper is what nocx puts on
//     somebody else's machine; it is what the footprint panel lists, what
//     the consent model governs and what deploy.Uninstall exists to remove
//     (internal/helper/consent, internal/ssh/ssh_helperuninstall.go). The
//     coordinator's own binary on the user's own machine is none of those
//     things, and filing it under that tree would put a local daemon's
//     lifecycle behind a remote-consent decision.
//   - Its completeness marker answers a question that does not arise here.
//     deploy writes over SFTP, where a directory can be left half-populated
//     and nothing local can see it, so completeness is a separate
//     .install-complete file. Locally, rename(2) within one directory is
//     atomic: a file that exists under the final name is complete by
//     construction, and adding a marker would be a second answer to a
//     question the filesystem has already answered.
//   - Its key is the helper PROTOCOL version and its artifacts are the
//     three cross-compiled binaries embedded by //go:embed all:artifacts.
//     This installs one binary, for this machine, keyed by the release
//     version, out of the running image. Bending deploy to take all three
//     as parameters would change the signature every caller in
//     internal/app and internal/ssh uses in order to serve a caller with
//     different semantics.
//
// What IS shared is the seam: the filesystem is an interface here as it is
// there, so every external call has a test in which it fails.
package serverbin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"

	"github.com/shady2k/nocx/internal/log"
)

const (
	// BinaryName is the coordinator's executable name — the name it has
	// inside the macOS bundle and inside the AppImage, and the stem every
	// versioned copy is built from.
	BinaryName = "nocx-server"

	// DirName is the subdirectory of the profile's data directory that
	// holds the versioned copies: ~/.local/share/nocx/bin on Linux. The
	// profile is the build's own (nocx vs nocx-dev), so a development
	// build never spawns a released daemon's copy or vice versa.
	DirName = "bin"

	// dirMode: private to the user who installed it.
	dirMode = 0o700
	// binaryMode: executable by its owner and by nobody else.
	binaryMode = 0o700
)

// ErrCorruptCopy reports that the bytes written did not hash to the bytes
// read. The copy is removed rather than promoted: a truncated daemon that
// starts is worse than one that never started, because it looks like an
// update that worked.
var ErrCorruptCopy = errors.New("serverbin: the installed copy does not match the source's content hash")

// Install describes one versioned copy.
type Install struct {
	// Path is the absolute path to spawn. This is the ONLY thing a
	// launcher should exec — never the source inside the image.
	Path string
	// Name is Path's base name, and the value to pass to [Installer.Prune]
	// as the one copy that must survive.
	Name string
	// Hash is the sha256 of the binary's content, hex.
	Hash string
	// Version is the release version the copy was installed for.
	Version string
	// Fresh reports whether this call wrote the copy. False means an
	// identical copy was already installed and nothing was written.
	Fresh bool
}

// File is a handle returned by [FS.Create]: Write, Sync and Close are
// separate steps so each can fail on its own in a test.
type File interface {
	io.Writer
	Sync() error
	Close() error
}

// FS is the filesystem seam. It exists so that every external call this
// package makes has a test in which it fails — a real os.* call cannot be
// made to fail on demand without depending on the identity of the user
// running the suite, which in a container is root and in CI is not.
//
// Modes are passed at creation and never left to the umask: a
// world-readable copy of the daemon under a user's home is a footprint
// nobody asked for.
type FS interface {
	Stat(path string) (iofs.FileInfo, error)
	MkdirAll(path string, mode os.FileMode) error
	Open(path string) (io.ReadCloser, error)
	Create(path string, mode os.FileMode) (File, error)
	Rename(oldPath, newPath string) error
	Remove(path string) error
	ReadDir(path string) ([]iofs.DirEntry, error)
	SyncDir(path string) error
}

// Installer installs and prunes versioned coordinator copies.
type Installer struct {
	fs  FS
	log log.Logger
}

// New constructs an Installer over fs. A nil logger gets the slog default,
// matching NewUpdater — a package that logged nothing when the caller
// passed nothing would go silent exactly where the interesting events are.
func New(fs FS, logger log.Logger) *Installer {
	if logger == nil {
		logger = log.NewSlogAdapter(nil)
	}
	return &Installer{fs: fs, log: logger}
}

// SiblingPath returns the coordinator binary that ships beside the
// application executable at exePath.
//
// One expression covers both layouts because both put the two binaries in
// one directory: nocx.app/Contents/MacOS/{nocx,nocx-server} on macOS, and
// $APPDIR/usr/bin/{nocx,nocx-server} inside the AppImage. That is a
// packaging fact, asserted by the release workflow and by
// scripts/appimage/package-appimage.sh, and it is stated here so a launcher
// does not have to know the shape of either bundle.
func SiblingPath(exePath string) string {
	return filepath.Join(filepath.Dir(exePath), BinaryName)
}

// Ensure makes a versioned copy of the coordinator at srcPath available
// under binDir and returns the path to spawn.
//
// It hashes the source, and if binDir already holds a copy under that
// exact version-and-hash name whose CONTENT still hashes the same, it
// writes nothing and returns that path — the ordinary case on every launch
// after the first. Otherwise it writes to a temporary name in the same
// directory, verifies the written bytes by reading them back, and only then
// renames into place. A copy that fails that check is removed and
// [ErrCorruptCopy] returned; it is never promoted.
//
// version is the release version (internal/version.Version). It is part of
// the name so a person reading the directory can tell what is there; the
// hash is what makes the name unique.
func (i *Installer) Ensure(ctx context.Context, srcPath, binDir, version string) (Install, error) {
	if version == "" {
		return Install{}, errors.New("serverbin: ensure: no version given — a copy that cannot be named cannot be superseded")
	}

	hash, err := i.hashFile(srcPath)
	if err != nil {
		return Install{}, fmt.Errorf("serverbin: hash source %s: %w", srcPath, err)
	}
	if err = ctx.Err(); err != nil {
		return Install{}, err
	}

	name := copyName(version, hash)
	target := filepath.Join(binDir, name)
	result := Install{Path: target, Name: name, Hash: hash, Version: version}

	installed, err := i.installedMatches(target, hash)
	if err != nil {
		return Install{}, err
	}
	if installed {
		i.log.Debug("serverbin: already installed", "path", target, "version", version)
		return result, nil
	}
	if err = ctx.Err(); err != nil {
		return Install{}, err
	}

	// Anything at the target that did not verify is debris — a copy
	// interrupted before it was renamed cannot exist under this name, so
	// a file here that does not hash is one somebody or something else
	// wrote. Remove it and install once.
	if err = i.removeIfPresent(target); err != nil {
		return Install{}, err
	}
	if err = i.install(ctx, srcPath, binDir, target, hash); err != nil {
		return Install{}, err
	}

	result.Fresh = true
	i.log.Info("serverbin: installed versioned coordinator", "path", target, "version", version)
	return result, nil
}

// install writes the source into binDir under a per-attempt temporary name,
// verifies the written bytes, and renames into place.
//
// The verification is a read-back rather than a hash of what was written:
// a short write, a full disk or a truncating filesystem all produce a file
// whose bytes are not the source's, and hashing the buffer we sent would
// agree with itself every time.
func (i *Installer) install(ctx context.Context, srcPath, binDir, target, wantHash string) error {
	if err := i.fs.MkdirAll(binDir, dirMode); err != nil {
		return fmt.Errorf("serverbin: create %s: %w", binDir, err)
	}

	// Per-attempt, so two launches racing to install the same version
	// never write one another's temporary file. The final rename is
	// last-writer-wins over byte-identical content.
	tmp := filepath.Join(binDir, "."+BinaryName+"-"+nonce())

	if err := i.copyFile(srcPath, tmp); err != nil {
		_ = i.fs.Remove(tmp)
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = i.fs.Remove(tmp)
		return err
	}

	got, err := i.hashFile(tmp)
	if err != nil {
		_ = i.fs.Remove(tmp)
		return fmt.Errorf("serverbin: verify written copy: %w", err)
	}
	if got != wantHash {
		_ = i.fs.Remove(tmp)
		i.log.Error("serverbin: refusing to promote a copy that does not match its source",
			"target", target, "want", wantHash, "got", got)
		return fmt.Errorf("%w: %s (want %s, got %s)", ErrCorruptCopy, target, wantHash, got)
	}

	if err := i.fs.Rename(tmp, target); err != nil {
		_ = i.fs.Remove(tmp)
		return fmt.Errorf("serverbin: promote %s: %w", target, err)
	}
	if err := i.fs.SyncDir(binDir); err != nil {
		return fmt.Errorf("serverbin: sync %s: %w", binDir, err)
	}
	return nil
}

// copyFile streams srcPath into dst, created with binaryMode.
func (i *Installer) copyFile(srcPath, dst string) error {
	src, err := i.fs.Open(srcPath)
	if err != nil {
		return fmt.Errorf("serverbin: open source %s: %w", srcPath, err)
	}
	defer func() { _ = src.Close() }()

	f, err := i.fs.Create(dst, binaryMode)
	if err != nil {
		return fmt.Errorf("serverbin: create %s: %w", dst, err)
	}
	_, werr := io.Copy(f, src)
	if werr == nil {
		werr = f.Sync()
	}
	closeErr := f.Close()
	if werr == nil {
		werr = closeErr
	}
	if werr != nil {
		return fmt.Errorf("serverbin: write %s: %w", dst, werr)
	}
	return nil
}

// installedMatches reports whether target exists AND its content hashes to
// hash. A file that exists but does not hash is not an install.
func (i *Installer) installedMatches(target, hash string) (bool, error) {
	if _, err := i.fs.Stat(target); err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("serverbin: stat %s: %w", target, err)
	}
	got, err := i.hashFile(target)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("serverbin: hash installed copy %s: %w", target, err)
	}
	if got != hash {
		i.log.Warn("serverbin: an installed copy does not match its own name — reinstalling",
			"path", target, "named", hash, "actual", got)
		return false, nil
	}
	return true, nil
}

func (i *Installer) removeIfPresent(path string) error {
	if err := i.fs.Remove(path); err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return fmt.Errorf("serverbin: remove %s: %w", path, err)
	}
	return nil
}

// hashFile streams path through sha256. Streamed rather than read whole
// because the coordinator is a ~60 MB binary and this runs on every launch.
func (i *Installer) hashFile(path string) (string, error) {
	f, err := i.fs.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyName builds the versioned file name.
func copyName(version, hash string) string {
	return BinaryName + "-" + version + "-" + hash
}

// nonce names one install attempt. Entropy failure is unrecoverable in
// practice and a fixed fallback would make two concurrent launches collide
// on one temporary file — the same call deploy makes, for the same reason.
func nonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("serverbin: no entropy for an install nonce")
	}
	return hex.EncodeToString(b[:])
}
