// The conditional-write half of the local view: read a file whole with the
// version of exactly the bytes that were read, then install new bytes only if
// the file still matches that version.
//
// # Why this lives here and not in hashline
//
// It used to live in hashline, which meant the assistant's edit path could only
// ever act on this machine's disk: the patch engine also owned EvalSymlinks,
// os.ReadFile, the temporary file and the rename. hashline is now pure by bytes
// and this is the provider seam the Files panel already had — VersionedProvider
// (nocx-oalo1.12.7) — implemented for the local provider, so a local pane and an
// SSH pane go through ONE edit path and the capability cannot tell them apart.
// No new logic: this is the same I/O hashline performed, moved behind the seam.
//
// # What the check buys
//
// The size, mtime and sha256 comparison and the rename happen under one
// process-wide path lock, so nothing this process writes can slip between them.
// That is the same guarantee hashline.Apply gave. Nothing here closes the window
// against ANOTHER process rewriting the file between the read and the rename;
// that window is what the helper's rename-time check exists for on a host, and
// on this machine it is bounded by the same assumption the panel has always
// made — the person and the assistant are the only writers.
package local

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
)

var (
	_ filesystem.VersionedProvider = (*Provider)(nil)
	_ filesystem.VersionedFiles    = (*Provider)(nil)
)

// pathMu serializes every install this process performs, and it is deliberately
// one lock rather than one per path. The interval it protects is short — a
// stat, a digest, a temporary write and a rename — and a per-path table would
// be a map that grows for the life of the process to buy parallelism this
// product does not need: two assistants editing two files at once is not a load
// this lock has ever been measured under.
var pathMu sync.Mutex

// Versioned returns the conditional-write seam. It is never nil: a local
// provider can always reach this machine's disk, and whether a particular path
// can be written is the call's answer, not a capability's.
func (p *Provider) Versioned() filesystem.VersionedFiles { return p }

// ReadVersion reads the whole file and answers the version of exactly those
// bytes. A file larger than maxBytes is refused with ErrByteLimit rather than
// cut, because a version of a prefix is not the file's version.
func (p *Provider) ReadVersion(ctx context.Context, path string, maxBytes int64) ([]byte, filesystem.Version, error) {
	if err := ctx.Err(); err != nil {
		return nil, filesystem.Version{}, err
	}
	if err := checkPath(path); err != nil {
		return nil, filesystem.Version{}, err
	}
	limit := filesystem.ReadLimit(maxBytes)
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, filesystem.Version{}, wrapPathErr("read", path, err)
	}
	if p.beforeOpen != nil {
		p.beforeOpen()
	}
	// O_NONBLOCK is a no-op for a regular file and makes a swapped-in FIFO's
	// open return instead of blocking forever; the authoritative kind then
	// comes from the descriptor that was opened.
	f, err := os.OpenFile(canonical, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, filesystem.Version{}, wrapPathErr("read", path, err)
	}
	defer func() { _ = f.Close() }()
	before, err := f.Stat()
	if err != nil {
		return nil, filesystem.Version{}, wrapPathErr("read", path, err)
	}
	if !before.Mode().IsRegular() {
		return nil, filesystem.Version{}, &filesystem.ErrNotRegular{Path: path, Kind: filesystem.KindOf(before.Mode())}
	}
	// The buffer is one byte past the file's OWN size — enough to see EOF and no
	// more — rather than one byte past the bound, which allocated the whole
	// 2 MiB default for every read of a small file. The file is larger than the
	// bound exactly when the read could fill limit+1 bytes, so the two sizes
	// agree about which case this is; a file that outgrew the smaller buffer is
	// caught by the size comparison below, before its bytes are trusted.
	bufSize := before.Size() + 1
	if max := limit + 1; bufSize > max {
		bufSize = max
	}
	buf := make([]byte, bufSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, filesystem.Version{}, wrapPathErr("read", path, err)
	}
	if p.afterRead != nil {
		p.afterRead()
	}
	after, err := f.Stat()
	if err != nil {
		return nil, filesystem.Version{}, wrapPathErr("read", path, err)
	}
	if int64(n) == limit+1 {
		return nil, filesystem.Version{}, &filesystem.ErrByteLimit{Path: path, Observed: before.Size(), Limit: limit}
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, filesystem.Version{}, &filesystem.ErrChangedDuringRead{Path: path}
	}
	data := buf[:n]
	return data, filesystem.Version{
		Size:    int64(len(data)),
		MtimeNs: after.ModTime().UnixNano(),
		SHA256:  filesystem.ContentDigest(data),
	}, nil
}

// Commit installs data only if expect still holds at the rename.
//
// Expect{Version} compares size, mtime and sha256 under the path lock and
// refuses ErrChangedSinceRead when any of them differ; Expect{Absent} creates
// the file with O_EXCL and refuses ErrExists when the name is taken. Either way
// the original bytes are untouched on every failure: the new content is written
// to a temporary beside the target and renamed over it only after every check
// passed, so a failed rename leaves the original file exactly as it was.
func (p *Provider) Commit(ctx context.Context, path string, data []byte, expect filesystem.Expect) (filesystem.Version, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.Version{}, err
	}
	if err := checkPath(path); err != nil {
		return filesystem.Version{}, err
	}
	pathMu.Lock()
	defer pathMu.Unlock()
	if expect.Absent {
		return p.create(path, data)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return filesystem.Version{}, wrapPathErr("commit", path, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return filesystem.Version{}, wrapPathErr("commit", path, err)
	}
	if !info.Mode().IsRegular() {
		return filesystem.Version{}, &filesystem.ErrNotRegular{Path: path, Kind: filesystem.KindOf(info.Mode())}
	}
	if expect.Version != nil {
		if reason, err := p.versionMismatch(ctx, canonical, info, *expect.Version); err != nil {
			return filesystem.Version{}, err
		} else if reason != "" {
			return filesystem.Version{}, &filesystem.ErrChangedSinceRead{Path: path, Reason: reason}
		}
	}
	if err := ctx.Err(); err != nil {
		return filesystem.Version{}, err
	}
	if err := p.install(canonical, data); err != nil {
		return filesystem.Version{}, err
	}
	return versionOfInstalled(canonical, path, data)
}

// versionMismatch answers why the file no longer matches the version the caller
// read, or "" when it does.
//
// The stat the commit already performed is compared FIRST, so a file whose size
// or timestamp moved is refused without being read at all. What remains is the
// rewrite of the same length inside one filesystem timestamp granule, and that
// is answered by the digest — read through ReadVersion, the same bounded,
// change-detecting read this package already has, with the caller's own size as
// the bound. It used to be an unbounded os.ReadFile of whatever was at the path,
// under the path lock, which is a read nothing bounded and nobody expected.
func (p *Provider) versionMismatch(ctx context.Context, canonical string, info fs.FileInfo, want filesystem.Version) (string, error) {
	if want.Size != info.Size() || want.MtimeNs != info.ModTime().UnixNano() {
		return "size or modification time differs", nil
	}
	// A file that grew past the expected size is refused as too large by this
	// read instead of being read to the end, and one that was being rewritten
	// while it was read is refused as changed: both answer the same question the
	// commit is asking, which is whether these are still the bytes that were
	// read.
	_, version, err := p.ReadVersion(ctx, canonical, want.Size)
	var limit *filesystem.ErrByteLimit
	var changed *filesystem.ErrChangedDuringRead
	switch {
	case errors.As(err, &limit):
		return "the file is larger than the version it was read with", nil
	case errors.As(err, &changed):
		return "the file changed while it was being checked", nil
	case err != nil:
		return "", err
	case version == want:
		return "", nil
	case version.SHA256 != want.SHA256:
		return "sha256 differs", nil
	}
	return "size or modification time differs", nil
}

// create writes a new file and refuses an occupied name with ErrExists. The
// parent is resolved through its symlinks and the base name is not: asking
// whether the caller's NAME is free must not follow a final symlink to the
// target's directory, where a free name beside the target would satisfy the
// check and land bytes the caller never asked to write.
func (p *Provider) create(path string, data []byte) (filesystem.Version, error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return filesystem.Version{}, wrapPathErr("create", path, err)
	}
	target := filepath.Join(parent, filepath.Base(path))
	// #nosec G304 -- target is the canonical parent plus the caller's base
	// name; the create with O_EXCL is the operation, and every path reaching
	// here has been through checkPath.
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return filesystem.Version{}, &filesystem.ErrExists{Path: path, Err: err}
		}
		return filesystem.Version{}, wrapPathErr("create", path, err)
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.Remove(target)
		}
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return filesystem.Version{}, err
	}
	if err := f.Close(); err != nil {
		return filesystem.Version{}, err
	}
	installed = true
	return versionOfInstalled(target, path, data)
}

// install writes data to a temporary beside the canonical target and renames it
// over the target, keeping the original file's permission bits.
func (p *Provider) install(canonical string, data []byte) error {
	info, err := os.Stat(canonical)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(canonical), ".nocx-edit-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if p.rename != nil {
		return p.rename(tmpName, canonical)
	}
	return os.Rename(tmpName, canonical)
}

// versionOfInstalled answers the version of the bytes just installed: the
// digest of the bytes that were written, and the modification time the
// filesystem stamped on them.
func versionOfInstalled(canonical, path string, data []byte) (filesystem.Version, error) {
	info, err := os.Stat(canonical)
	if err != nil {
		return filesystem.Version{}, wrapPathErr("stat", path, err)
	}
	return filesystem.Version{
		Size:    int64(len(data)),
		MtimeNs: info.ModTime().UnixNano(),
		SHA256:  filesystem.ContentDigest(data),
	}, nil
}
