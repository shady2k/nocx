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
	"sync"

	"github.com/shady2k/nocx/internal/operation/catalog"

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
	// writeChunk is how much of the payload one Write carries. It is small
	// enough for the INSTALL to be watchable and stoppable: a progress
	// reporter sees a byte count that moves while somebody waits on a slow
	// link, and a cancelled install is noticed within one chunk instead of
	// after the whole binary. A 15.3 MB helper is 60 writes of it.
	writeChunk = 256 << 10
)

// Stage names what the install is doing at this moment, in the vocabulary a
// surface shows a person. There is no stage for "removing an unfinished
// directory" or "decompressing": both are local work measured in
// milliseconds, and a row that flickered through three sentences to say so
// would be noise rather than news.
type Stage string

const (
	// StageCopying is the payload moving to the host. Progress carries bytes.
	StageCopying Stage = "copying"
	// StageChecking is the verification read that proves what arrived hashes
	// to the install directory's name. It moves no counted bytes: the read
	// is one ReadFile, so a row says what it is doing and shows no bar
	// rather than a bar at 100% that stops moving.
	StageChecking Stage = "checking"
)

// Progress is the install's own account of what it is doing, for a surface
// that shows the work while it happens (ADR-8eej3e66 §8: a first helper
// install is work a person is watching). It is called on the installing
// goroutine, never concurrently, and total is zero where the stage counts
// nothing. A nil Progress is the ordinary case for every caller that is not
// a person's open.
type Progress func(stage Stage, written, total int64)

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

// InstalledPath is the absolute path of the binary inside a complete install
// for home, p and generation: <installDir>/nocx-helper.
//
// It is exported because it has a READER that is not this package — the
// helper's ssh service, which must run the binary this installer wrote
// (internal/helper/sshsvc's lane op, nocx-50w7p.10) — and a second derivation
// of "where did we install it" is exactly the regression AD-8 names: the two
// would agree everywhere anybody looked and disagree the day the directory
// layout moved. Ensure returns it, so the installer and the lane answer with
// one expression.
//
// It reads no filesystem and proves nothing: it says WHERE a complete install
// of that generation is, which is why the caller that matters (the lane)
// reaches a helper that exists or a `bridge` that answers "no helper is
// serving that generation".
func InstalledPath(home string, p Platform, generation string) string {
	return InstalledBinary(installDir(home, p, generation))
}

// InstalledBinary is the binary inside an install directory.
//
// It is the same one derivation as InstalledPath, reached from the other end:
// a caller that holds the DIRECTORY — the coordinator's own record of an
// install (consent.Install.Path), or a session a coordinator re-adopted — must
// name the same file the installer wrote without re-deriving the directory
// name, and the helper's lane op is exactly that caller
// (internal/helper/sshsvc, nocx-50w7p.10).
func InstalledBinary(dir string) string {
	return path.Join(dir, binaryName)
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
// exists over a binary this installer hashed and accepted, until something
// removes it. Outside that interval it is a tree the next attempt must REMOVE
// before it may install, and there is no third state — which is why the
// recovery is the same one at every boundary below rather than a table of
// special cases.
//
// The boundaries fall into exactly two groups:
//
//  1. NOTHING WAS TOUCHED, so whatever was on disk stays as usable as it was:
//     artifact selection, both context checks, and the marker Lstat when it
//     fails for any reason other than absence. Note what that does NOT do: an
//     unreadable marker is refused rather than treated as absent, because "I
//     could not look" is not "it is not there" — treating it as absent would
//     delete a live install on a transient read error.
//
//  2. A MARKERLESS DIRECTORY, which is precisely the state isComplete answers
//     `false` for, so the next attempt removes it and reinstalls: Mkdir,
//     decompression, the temp create, the write, the sync, the close, the
//     rename, the directory sync, and the verification read. The write and
//     rename paths remove their own temporary first, and if that removal
//     itself fails the orphan is still harmless — the temporary name is
//     per-attempt, so it can never be mistaken for the binary, and the
//     directory around it is markerless anyway. THE MARKER IS WRITTEN LAST,
//     after the bytes are both durable AND verified: its presence is the
//     claim "this directory is complete", and a write the transport corrupted
//     leaves no claim behind, so the next attempt removes the tree and
//     reinstalls it once (D6).
//
// The removal itself (removeTree, when it fails partway) cannot produce a
// third state: whichever of the two files it has removed, what is left is
// missing either the marker or the binary, which is group 2.
//
// # Why a COMPLETE install is never re-read (nocx-b3j8r)
//
// The marker used to be checked together with a full ReadFile of the binary,
// on EVERY Ensure — which is every SSH open, because the open path installs
// before it connects. The installed helper is 15.3 MB, and an SFTP read of it
// costs the person's link, not the disk: measured 2026-11-05 on a 25 ms
// loopback, a repeat open spent 2.3 s of its 3.4 s there, and the owner's
// 22.15 s silent gap on a real WAN is the same 15.3 MB at about 0.7 MB/s.
//
// The read is not what makes an install safe, and D21 says so: integrity is
// proven by what RAN, not by what was written. Every connection to the helper
// presents the content hash the binary computes of itself, and the helper
// client compares it with the generation the lane asked for. The read only
// added a second opinion before the process
// started, and it was the expensive one. What it could catch that the
// handshake cannot — a same-size corruption of an already-complete tree, not
// reachable by an interrupted upload — is reported as helperVersionMismatch
// by the handshake instead of being repaired silently.
//
// The verification read itself stays, exactly once, where the bytes are
// unproven: immediately after this call wrote them and BEFORE the marker.
//
// Nothing in the layout is a claim about a PROCESS. There is no pid file and
// no lock: a complete install says the bytes are there, never that a helper is
// running from them, so an install followed by a start that fails is still
// exactly as reusable as one followed by a start that succeeds. Liveness has
// one authority and it is the endpoint socket.
func Ensure(ctx context.Context, fs RemoteFS, src ArtifactSource, home string, p Platform, progress Progress) (binaryPath, contentHash string, err error) {
	scope := catalog.BeginInternal(ctx, catalog.OpInternalHelperInstall)
	defer scope.Complete()
	data, contentHash, err := src.Artifact(p)
	if err != nil {
		return "", "", err
	}
	if err = ctx.Err(); err != nil {
		return "", "", err
	}
	dir := installDir(home, p, contentHash)
	binary := InstalledPath(home, p, contentHash)

	complete, err := isComplete(fs, dir)
	if err != nil {
		return "", "", err
	}
	if complete {
		// NOTHING TO REPORT, and that is a fact a surface relies on: a
		// repeat open installs nothing, so progress is never called and no
		// row is ever made for it.
		return binary, contentHash, nil
	}
	if err = ctx.Err(); err != nil {
		return "", "", err
	}

	// Incomplete: absent or partial (no marker). Remove whatever is there and
	// reinstall once — the D6 reinstall.
	if err = removeTree(fs, dir); err != nil {
		return "", "", fmt.Errorf("deploy: remove incomplete install %s: %w", dir, err)
	}
	if err = ctx.Err(); err != nil {
		return "", "", err
	}
	if err = install(ctx, fs, dir, binary, contentHash, data, progress); err != nil {
		return "", "", err
	}
	return binary, contentHash, nil
}

// isComplete reports whether dir carries the marker that makes a D7 install
// complete. A directory without it is never used.
//
// THE MARKER IS THE WHOLE CHECK, and that is a cost decision as much as a
// design one: reading the binary here made every SSH open transfer the
// installed helper over the person's link (install.go's own note,
// nocx-b3j8r). The bytes are verified once, by install, before the marker is
// written; what runs afterwards is verified by the helper's own handshake.
func isComplete(fs RemoteFS, dir string) (bool, error) {
	if _, err := fs.Lstat(path.Join(dir, markerName)); err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// hashesTo reports whether the file at binary hashes to contentHash.
func hashesTo(fs RemoteFS, binary, contentHash string) (bool, error) {
	data, err := fs.ReadFile(binary)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == contentHash, nil
}

// install writes the decompressed artifact into dir under a temporary
// name, renames it into place, verifies the bytes it wrote, and only then
// writes the .install-complete marker — the directory is complete only when
// the marker exists (D7). An interrupted or corrupt install leaves a
// markerless directory, which a later Ensure removes and reinstalls.
//
// THE WRITE IS CHUNKED, and it is the payload's two readers that decide it.
// A person watching a slow link needs a byte count that moves (progress), and
// a person who cancels needs the install to stop rather than to plough
// through the rest of the binary — the context is checked between chunks,
// and the write, sync and close all happen AFTER the last chunk was accepted,
// so a cancelled install removes its temporary and leaves a markerless tree.
// Neither reader can exist if the whole payload goes out in one Write, which
// is what this used to do.
func install(ctx context.Context, fs RemoteFS, dir, binary, contentHash string, compressed []byte, progress Progress) error {
	if err := mkdirAll(fs, dir, dirMode); err != nil {
		return fmt.Errorf("deploy: install directory: %w", err)
	}
	payload, err := decompressedPayload(compressed)
	if err != nil {
		return fmt.Errorf("deploy: decompress helper: %w", err)
	}
	report := func(stage Stage, written, total int64) {
		if progress != nil {
			progress(stage, written, total)
		}
	}

	// The temporary name is per-attempt: two concurrent installs of the
	// same artifact on one host never collide on one temp file, and the
	// final rename is last-writer-wins over byte-identical content.
	tmp := path.Join(dir, ".install-"+nonce())
	f, err := fs.Create(tmp, binaryMode)
	if err != nil {
		return fmt.Errorf("deploy: create temp: %w", err)
	}
	total := int64(len(payload))
	report(StageCopying, 0, total)
	written := 0
	werr := ctx.Err()
	for werr == nil && written < len(payload) {
		size := min(writeChunk, len(payload)-written)
		n, writeErr := f.Write(payload[written : written+size])
		if writeErr != nil {
			werr = writeErr
			break
		}
		if n != size {
			werr = io.ErrShortWrite
			break
		}
		written += n
		report(StageCopying, int64(written), total)
	}
	// AND ONCE MORE BEFORE ANYTHING IS COMMITTED. A cancellation that
	// arrives while the LAST chunk is on the wire otherwise slips past the
	// loop, and the install would commit an upload the person had already
	// stopped. Nothing is durable yet at this point — the rename and the
	// marker come after — so stopping here costs the person nothing and
	// leaves the markerless tree every later attempt knows how to replace.
	if werr == nil {
		werr = ctx.Err()
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
	// Verify exactly once, BEFORE the marker. The bytes we wrote hash to
	// contentHash by construction; a mismatch here means the transport
	// corrupted the write, and looping would only repeat the corruption
	// (D6). Leaving the tree markerless is what makes the next attempt
	// reinstall it: the marker is the claim, and a corrupt write may not
	// carry one.
	//
	// NO BAR HERE, and the stage says so: the read is one ReadFile of the
	// whole binary, so there is no byte count between its ends to report
	// (total zero). A row that showed 100% for the several seconds this
	// takes on a slow link would read as stalled at the moment it is
	// working hardest.
	report(StageChecking, 0, 0)
	ok, err := hashesTo(fs, binary, contentHash)
	if err != nil {
		return fmt.Errorf("deploy: verify helper: %w", err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrHashMismatch, binary)
	}
	// The marker is written last, after every byte of the binary is durable
	// and verified: its presence is the claim "this directory is complete".
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
// decompressedArtifacts remembers every artifact this process has decompressed.
//
// The same artifact is decompressed once per install otherwise: an App that
// opens a pane installs this machine's helper, every stand in a test process
// installs its own copy, and the package's own profile measured 12.18 s of the
// internal/app suite inside gunzip alone (nocx-atpw5.1.17). A process installs
// at most the build's own artifact matrix — four targets plus this machine's
// local variant — so the entries are bounded by the build rather than by how
// many installs it performs.
//
// THE LOCK IS HELD ACROSS THE DECOMPRESSION, which is what makes "once" true
// rather than likely: two panes opening at once install the same artifact at
// once, and the second must read what the first produced rather than decompress
// a second copy. The work it serialises is a cold start's, once per artifact,
// and an install of a different artifact waits for it rather than repeating it.
type decompressedArtifacts struct {
	mu sync.Mutex
	// byDigest is keyed by the COMPRESSED artifact's own digest — the input of
	// the decompression — so two different artifacts can never answer for one
	// another, and a corrupt or unexpected artifact is still decompressed (and
	// still fails) rather than served a payload cached for its generation.
	//
	// THE PAYLOADS ARE SHARED: every caller writes one out and none modifies
	// it, so what the map holds is the artifact's bytes rather than a copy per
	// install.
	byDigest map[[sha256.Size]byte][]byte
}

var decompressed = &decompressedArtifacts{byDigest: map[[sha256.Size]byte][]byte{}}

// decompressedPayload answers the payload of one compressed artifact, reading
// it from this process's memory when it has been decompressed before.
func decompressedPayload(compressed []byte) ([]byte, error) {
	digest := sha256.Sum256(compressed)
	decompressed.mu.Lock()
	defer decompressed.mu.Unlock()
	if p, ok := decompressed.byDigest[digest]; ok {
		return p, nil
	}
	p, err := gunzip(compressed)
	if err != nil {
		return nil, err
	}
	decompressed.byDigest[digest] = p
	return p, nil
}

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
