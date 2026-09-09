package deploy

// The versioned install (D7): an immutable directory keyed by
// version-goos-goarch-contenthash under ~/.nocx/helper, written to a
// temporary name and renamed atomically, complete only when it carries an
// .install-complete marker. A directory without the marker is removed and
// reinstalled, never used; an already-complete directory uploads nothing;
// a hash mismatch triggers exactly one automatic reinstall (D6).

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path"
	"strings"

	"github.com/shady2k/nocx/internal/helper/proto"
)

// version is the helper protocol version; it names the install directory
// and separates installs of incompatible helpers (D7, D6).
const version = proto.Version

// Fixed names inside ~/.nocx/helper. Nothing else under the home is ever
// created or modified (N4's discipline, applied to the helper's own tree).
const (
	// HelperRootName is the D7 install root under the remote home — the
	// directory the deploy package owns and may prune or uninstall.
	HelperRootName = "helper"
	// markerName makes a directory complete: its presence is the claim
	// "every byte of the binary is durable". A directory without it is
	// removed and reinstalled, never used.
	markerName = ".install-complete"
	// binaryName is the installed helper's file name.
	binaryName = "nocx-helper"
	// dirMode is the install directory's mode: private to the account that
	// installed it, on a host that may be shared.
	dirMode = 0o700
	// binaryMode is the helper's mode: executable by its owner and nothing
	// else.
	binaryMode = 0o700
	// markerMode is the marker's mode; it is a data file, not executable.
	markerMode = 0o600
)

// RemoteFS is the filesystem seam the installer writes through: the same
// shape as the shell-integration publisher's seam, so one SFTP adapter
// serves both, and the seam keeps deploy testable against a fake with no
// transport at all. Modes are set at creation and never left to the
// server's umask, and no path is followed through a symlink — lstat
// semantics throughout.
type RemoteFS interface {
	Lstat(path string) (iofs.FileInfo, error)
	Mkdir(path string, mode os.FileMode) error
	Create(path string, mode os.FileMode) (File, error)
	SyncDir(path string) error
	Rename(src, dst string) error
	Remove(path string) error
	ReadDir(path string) ([]iofs.FileInfo, error)
	ReadFile(path string) ([]byte, error)
}

// File is a handle returned by RemoteFS.Create: Write, Sync and Close are
// separate fault-injectable steps.
type File interface {
	io.Writer
	Sync() error
	Close() error
}

// ErrHashMismatch is returned when a reinstall still leaves a directory
// whose binary does not hash to its name (D6): exactly one automatic
// reinstall, and a second mismatch is terminal — the caller maps it to
// helperVersionMismatch rather than looping.
var ErrHashMismatch = errors.New("deploy: installed helper does not match its content hash after reinstall")

// installDir returns the D7 install directory for p and contentHash:
// ~/.nocx/helper/<version>-<goos>-<goarch>-<hash>. Two platforms never
// collide on one name — one account on an arm64 and an amd64 machine (NFS,
// or the same login on both) resolves to two directories, each holding the
// binary for its own platform.
func installDir(home string, p Platform, contentHash string) string {
	return path.Join(home, ".nocx", HelperRootName, version+"-"+p.GOOS+"-"+p.GOARCH+"-"+contentHash)
}

// Ensure installs the artifact for p if it is not already complete, and
// returns the absolute path of the installed binary and its content hash
// (D7, D20, D21). The artifact bytes come from src — the seam the caller
// gives it; the composition root passes the embedded source from the sibling
// artifacts package, tests pass synthetic bytes, and the install semantics
// are identical either way.
//
// A directory is complete only when it carries .install-complete AND its
// binary hashes to the directory's key; anything else is removed and
// reinstalled, never used. An already-complete directory uploads nothing.
// A mismatch on a complete directory triggers exactly ONE automatic
// reinstall; a second mismatch on the same call returns ErrHashMismatch
// and does not loop (D6). The context is honoured between phases — a
// cancelled install stops rather than ploughing on.
//
// # What every failure boundary leaves, as an interval with both ends
//
// THE INTERVAL: an install directory is USABLE from the moment its marker
// exists over a binary that hashes to the directory's key, until something
// removes it. Outside that interval it is a tree the next attempt must REMOVE
// before it may install, and there is no third state — which is why the
// recovery is the same one at every boundary below rather than a table of
// special cases.
//
// The dozen boundaries fall into exactly three groups:
//
//  1. NOTHING WAS TOUCHED, so whatever was on disk stays as usable as it was:
//     artifact selection, both context checks, and the two reads isComplete
//     makes (the marker Lstat and the binary ReadFile) when they fail for any
//     reason other than absence. Note what those two do NOT do: an unreadable
//     marker is refused rather than treated as absent, because "I could not
//     look" is not "it is not there" — treating it as absent would delete a
//     live install on a transient read error.
//
//  2. A MARKERLESS DIRECTORY, which is precisely the state isComplete answers
//     `false` for, so the next attempt removes it and reinstalls: Mkdir,
//     decompression, the temp create, the write, the sync, the close, the
//     rename, and the directory sync. The write and rename paths remove their
//     own temporary first, and if that removal itself fails the orphan is
//     still harmless — the temporary name is per-attempt, so it can never be
//     mistaken for the binary, and the directory around it is markerless
//     anyway. THE DIRECTORY SYNC IS THE INTERESTING ONE: the binary is fully
//     in place by then and is still not usable, because the marker is what
//     makes a tree complete and it has not been written. That is the whole
//     reason the marker is written last, after every byte is durable.
//
//  3. A COMPLETE-LOOKING TREE THAT IS NOT COMPLETE, which exactly one
//     boundary can leave: the verification read after the install, when the
//     bytes on disk do not hash to the key. It is left in place rather than
//     removed, and it is still not reusable — the next Ensure runs the same
//     isComplete, gets `false` from the hash, removes the tree and reinstalls
//     once (D6). Completeness is the marker AND the hash, never the marker
//     alone, and that pairing is what makes this state self-healing instead of
//     permanently poisoned.
//
// The removal itself (removeTree, when it fails partway) cannot produce a
// fourth state: whichever of the two files it has removed, what is left is
// missing either the marker or the binary, which is group 2.
//
// Nothing in the layout is a claim about a PROCESS. There is no pid file and
// no lock: a complete install says the bytes are there, never that a helper is
// running from them, so an install followed by a start that fails is still
// exactly as reusable as one followed by a start that succeeds. Liveness has
// one authority and it is the endpoint socket.
func Ensure(ctx context.Context, fs RemoteFS, src ArtifactSource, home string, p Platform) (binaryPath, contentHash string, err error) {
	data, contentHash, err := src.Artifact(p)
	if err != nil {
		return "", "", err
	}
	if err = ctx.Err(); err != nil {
		return "", "", err
	}
	dir := installDir(home, p, contentHash)
	binary := path.Join(dir, binaryName)

	complete, err := isComplete(fs, dir, binary, contentHash)
	if err != nil {
		return "", "", err
	}
	if complete {
		return binary, contentHash, nil
	}
	if err = ctx.Err(); err != nil {
		return "", "", err
	}

	// Incomplete: absent, partial (no marker), or complete-looking but
	// corrupt. Remove whatever is there and reinstall once — the D6
	// reinstall.
	if err = removeTree(fs, dir); err != nil {
		return "", "", fmt.Errorf("deploy: remove incomplete install %s: %w", dir, err)
	}
	if err = ctx.Err(); err != nil {
		return "", "", err
	}
	if err = install(fs, dir, binary, data); err != nil {
		return "", "", err
	}

	// Verify exactly once. The bytes we wrote hash to contentHash by
	// construction; a mismatch here means the transport corrupted the
	// write, and looping would only repeat the corruption (D6).
	complete, err = isComplete(fs, dir, binary, contentHash)
	if err != nil {
		return "", "", err
	}
	if !complete {
		return "", "", fmt.Errorf("%w: %s", ErrHashMismatch, binary)
	}
	return binary, contentHash, nil
}

// isComplete reports whether dir is a complete D7 install for contentHash:
// the marker exists AND the binary hashes to the directory's key. A
// directory without the marker is never used; a marker with a mismatched
// binary is a corrupt install the caller removes and reinstalls (D6).
func isComplete(fs RemoteFS, dir, binary, contentHash string) (bool, error) {
	if _, err := fs.Lstat(path.Join(dir, markerName)); err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	data, err := fs.ReadFile(binary)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == contentHash, nil
}

// install writes the decompressed artifact into dir under a temporary
// name, renames it into place, and only then writes the .install-complete
// marker — the directory is complete only when the marker exists (D7). An
// interrupted install leaves a markerless directory, which a later Ensure
// removes and reinstalls.
func install(fs RemoteFS, dir, binary string, compressed []byte) error {
	if err := mkdirAll(fs, dir, dirMode); err != nil {
		return fmt.Errorf("deploy: install directory: %w", err)
	}
	payload, err := gunzip(compressed)
	if err != nil {
		return fmt.Errorf("deploy: decompress helper: %w", err)
	}

	// The temporary name is per-attempt: two concurrent installs of the
	// same artifact on one host never collide on one temp file, and the
	// final rename is last-writer-wins over byte-identical content.
	tmp := path.Join(dir, ".install-"+nonce())
	f, err := fs.Create(tmp, binaryMode)
	if err != nil {
		return fmt.Errorf("deploy: create temp: %w", err)
	}
	written, werr := f.Write(payload)
	if werr == nil && written != len(payload) {
		werr = io.ErrShortWrite
	}
	if werr == nil {
		werr = f.Sync()
	}
	closeErr := f.Close()
	if werr == nil {
		werr = closeErr
	}
	if werr != nil {
		// The half-written temp must not linger: an interrupted upload is
		// ordinary, and the markerless directory it leaves behind is
		// precisely the state a later Ensure must find clean.
		_ = fs.Remove(tmp)
		return fmt.Errorf("deploy: write helper: %w", werr)
	}
	if rerr := fs.Rename(tmp, binary); rerr != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("deploy: rename helper into place: %w", rerr)
	}
	if serr := fs.SyncDir(dir); serr != nil {
		return fmt.Errorf("deploy: sync install directory: %w", serr)
	}
	// The marker is written last, after every byte of the binary is
	// durable: its presence is the claim "this directory is complete".
	m, err := fs.Create(path.Join(dir, markerName), markerMode)
	if err != nil {
		return fmt.Errorf("deploy: write install marker: %w", err)
	}
	if cerr := m.Close(); cerr != nil {
		return fmt.Errorf("deploy: close install marker: %w", cerr)
	}
	return nil
}

// gunzip decompresses one embedded artifact — the payload is uploaded
// decompressed (D20).
func gunzip(data []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}

// mkdirAll creates dir and its parents with mode, tolerating an existing
// directory at every level — the SFTP carrier reports a concurrent create
// as fs.ErrExist, and two installs racing on one host are ordinary. A
// non-directory in the way is an error, never a write-through.
func mkdirAll(fs RemoteFS, dir string, mode os.FileMode) error {
	cur := "/"
	if !strings.HasPrefix(dir, "/") {
		cur = ""
	}
	for _, part := range strings.Split(strings.TrimPrefix(dir, "/"), "/") {
		if part == "" {
			continue
		}
		cur = path.Join(cur, part)
		if err := fs.Mkdir(cur, mode); err != nil {
			if errors.Is(err, iofs.ErrExist) {
				continue
			}
			return err
		}
	}
	return nil
}

// nonce returns a fresh random hex string naming a temporary install file.
// Entropy failure is unrecoverable in practice; a fixed fallback would make
// concurrent installs collide.
func nonce() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("deploy: no entropy for install nonce")
	}
	return hex.EncodeToString(b[:])
}
