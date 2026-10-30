package serverbin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/shady2k/nocx/internal/assistant/runtimebundle"
	"github.com/shady2k/nocx/internal/log"
)

// TreeFS extends the existing binary install seam with directory ownership and
// no-replace promotion. RenameNew must atomically refuse ANY existing target.
type TreeFS interface {
	FS
	Lstat(string) (fs.FileInfo, error)
	Readlink(string) (string, error)
	Symlink(target, name string) error
	Mkdir(string, os.FileMode) error
	RemoveAll(string) error
	RenameNew(oldPath, newPath string) error
	LockShared(context.Context, string, string) (*runtimebundle.Lease, error)
	TryLockExclusive(string, string) (*runtimebundle.Lease, error)
}

type TreeInstall struct {
	Path  string
	Hash  string
	Fresh bool
	// Lease must remain held until a locator or spawned coordinator owns it.
	Lease *runtimebundle.Lease
}

func (i TreeInstall) Close() error { return i.Lease.Close() }

type TreeInstaller struct{ fs TreeFS }

func NewTreeInstaller(view TreeFS) *TreeInstaller { return &TreeInstaller{fs: view} }

// Ensure verifies and copies a runtime to parent/<manifest hash>. It acquires
// a shared lease before reading or publishing the target and transfers that
// lease with the successful result. Only Prune may remove unused versions.
func (i *TreeInstaller) Ensure(ctx context.Context, source, parent, target string) (result TreeInstall, err error) {
	if i.fs == nil || !filepath.IsAbs(parent) {
		return result, errors.New("serverbin: runtime tree needs an absolute private parent")
	}
	manifest, err := runtimebundle.ReadManifest(ctx, i.fs, source, "", target)
	if err != nil {
		return result, fmt.Errorf("serverbin: runtime manifest: %w", err)
	}
	dirs, err := runtimebundle.VerifyTree(ctx, i.fs, source, manifest)
	if err != nil {
		return result, fmt.Errorf("serverbin: verify runtime source: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	result = TreeInstall{Path: filepath.Join(parent, manifest.Digest), Hash: manifest.Digest}
	if err = i.fs.MkdirAll(parent, dirMode); err != nil {
		return TreeInstall{}, err
	}
	parentInfo, err := i.fs.Lstat(parent)
	if err != nil {
		return TreeInstall{}, err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm() != dirMode || !runtimebundle.OwnedByCurrentUser(parentInfo) {
		return TreeInstall{}, errors.New("serverbin: runtime parent is not a private directory")
	}
	lease, err := i.fs.LockShared(ctx, parent, manifest.Digest)
	if err != nil {
		return TreeInstall{}, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, lease.Close())
			result = TreeInstall{}
		}
	}()
	result.Lease = lease
	present, err := i.existingTree(ctx, result.Path, manifest)
	if err != nil {
		return TreeInstall{}, err
	}
	if present {
		if err = i.fs.SyncDir(parent); err != nil {
			return TreeInstall{}, err
		}
		return result, nil
	}
	stage := filepath.Join(parent, ".runtime-"+nonce())
	if err = i.fs.Mkdir(stage, dirMode); err != nil {
		return TreeInstall{}, err
	}
	defer func() {
		if cleanupErr := i.fs.RemoveAll(stage); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("serverbin: remove owned runtime staging: %w", cleanupErr))
		}
	}()
	for _, dir := range dirs {
		if dir == "." {
			continue
		}
		if err = ctx.Err(); err != nil {
			return TreeInstall{}, err
		}
		if err = i.fs.MkdirAll(filepath.Join(stage, filepath.FromSlash(dir)), dirMode); err != nil {
			return TreeInstall{}, err
		}
	}
	buffer := make([]byte, 64<<10)
	for _, entry := range manifest.Files {
		if err = ctx.Err(); err != nil {
			return TreeInstall{}, err
		}
		destination := filepath.Join(stage, filepath.FromSlash(entry.Path))
		if entry.Link != "" {
			if err = i.fs.Symlink(entry.Link, destination); err != nil {
				return TreeInstall{}, err
			}
			continue
		}
		src, openErr := i.fs.Open(filepath.Join(source, filepath.FromSlash(entry.Path)))
		if openErr != nil {
			return TreeInstall{}, openErr
		}
		mode := os.FileMode(0o600)
		if entry.Executable {
			mode = binaryMode
		}
		err = i.writeTreeFile(ctx, destination, mode, io.LimitReader(src, entry.Size+1), buffer)
		closeErr := src.Close()
		if err != nil {
			return TreeInstall{}, err
		}
		if closeErr != nil {
			return TreeInstall{}, closeErr
		}
	}
	if err = i.writeTreeFile(ctx, filepath.Join(stage, runtimebundle.ManifestName), 0o600, bytes.NewReader(manifest.Bytes), buffer); err != nil {
		return TreeInstall{}, err
	}
	if _, err = runtimebundle.VerifyTree(ctx, i.fs, stage, manifest); err != nil {
		return TreeInstall{}, fmt.Errorf("serverbin: verify runtime copy: %w", err)
	}
	if err = i.privateTree(stage, dirs, manifest); err != nil {
		return TreeInstall{}, err
	}
	// Deepest first: directory entries become durable before their parents.
	for n := len(dirs) - 1; n >= 0; n-- {
		if err = ctx.Err(); err != nil {
			return TreeInstall{}, err
		}
		if err = i.fs.SyncDir(filepath.Join(stage, filepath.FromSlash(dirs[n]))); err != nil {
			return TreeInstall{}, err
		}
	}
	if err = i.fs.SyncDir(parent); err != nil {
		return TreeInstall{}, err
	}
	if err = ctx.Err(); err != nil {
		return TreeInstall{}, err
	}
	if err = i.fs.RenameNew(stage, result.Path); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return TreeInstall{}, fmt.Errorf("serverbin: promote runtime: %w", err)
		}
		present, verifyErr := i.existingTree(ctx, result.Path, manifest)
		if verifyErr != nil {
			return TreeInstall{}, verifyErr
		}
		if !present {
			return TreeInstall{}, err
		}
		if err = i.fs.SyncDir(parent); err != nil {
			return TreeInstall{}, err
		}
		return result, nil
	}
	// A post-rename fsync failure is reported, but the complete immutable
	// winner is never removed: another launcher may already be using it.
	if err = i.fs.SyncDir(parent); err != nil {
		return TreeInstall{}, fmt.Errorf("serverbin: runtime promotion durability uncertain: %w", err)
	}
	result.Fresh = true
	log.From(ctx).Info("installed immutable assistant runtime", "path", result.Path, "manifest", result.Hash)
	return result, nil
}

func (i *TreeInstaller) existingTree(ctx context.Context, root string, manifest *runtimebundle.Manifest) (bool, error) {
	_, err := i.fs.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	installed, err := runtimebundle.ReadManifest(ctx, i.fs, root, manifest.Digest, manifest.Metadata.Target)
	if err != nil {
		return false, fmt.Errorf("serverbin: existing runtime is corrupt: %w", err)
	}
	dirs, err := runtimebundle.VerifyTree(ctx, i.fs, root, installed)
	if err != nil {
		return false, fmt.Errorf("serverbin: existing runtime is corrupt: %w", err)
	}
	if err = i.privateTree(root, dirs, installed); err != nil {
		return false, err
	}
	return true, nil
}

func (i *TreeInstaller) privateTree(root string, dirs []string, manifest *runtimebundle.Manifest) error {
	for _, dir := range dirs {
		info, err := i.fs.Lstat(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != dirMode || !runtimebundle.OwnedByCurrentUser(info) {
			return errors.New("serverbin: runtime directory is not private and owned")
		}
	}
	for _, entry := range manifest.Files {
		info, err := i.fs.Lstat(filepath.Join(root, filepath.FromSlash(entry.Path)))
		if err != nil {
			return err
		}
		if !runtimebundle.OwnedByCurrentUser(info) {
			return errors.New("serverbin: runtime file is not owned")
		}
		if entry.Link != "" {
			continue
		}
		want := os.FileMode(0o600)
		if entry.Executable {
			want = binaryMode
		}
		if info.Mode().Perm() != want {
			return errors.New("serverbin: runtime file permissions are not private")
		}
	}
	info, err := i.fs.Lstat(filepath.Join(root, runtimebundle.ManifestName))
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o600 || !runtimebundle.OwnedByCurrentUser(info) {
		return errors.New("serverbin: runtime manifest is not private and owned")
	}
	return nil
}

func (i *TreeInstaller) writeTreeFile(ctx context.Context, name string, mode os.FileMode, reader io.Reader, buffer []byte) error {
	file, err := i.fs.Create(name, mode)
	if err != nil {
		return err
	}
	_, writeErr := io.CopyBuffer(file, treeReader{ctx, reader}, buffer)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

type treeReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r treeReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
