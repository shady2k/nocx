package serverbin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/shady2k/nocx/internal/assistant/runtimebundle"
	"github.com/shady2k/nocx/internal/log"
)

// Prune removes only owned, complete, hash-named versions that have no live
// shared lease. The exclusive lock covers validation THROUGH deletion, not a
// PID check followed by a racy unlink. The expected current version is kept
// unconditionally; busy, corrupt, foreign and uncertain entries are retained.
// Persistent files in .leases are never removed.
func (i *TreeInstaller) Prune(ctx context.Context, parent, keepHash, target string) error {
	if i.fs == nil || !filepath.IsAbs(parent) || !runtimebundle.ValidDigest(keepHash) {
		return runtimebundle.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := i.fs.Lstat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != dirMode || !runtimebundle.OwnedByCurrentUser(info) {
		return runtimebundle.ErrInvalid
	}
	entries, err := i.fs.ReadDir(parent)
	if err != nil {
		return err
	}
	var uncertain error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return errors.Join(uncertain, err)
		}
		name := entry.Name()
		if name == keepHash || !runtimebundle.ValidDigest(name) || !entry.IsDir() {
			continue
		}
		root := filepath.Join(parent, name)
		info, err := i.fs.Lstat(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			uncertain = errors.Join(uncertain, err)
			continue
		}
		if !info.IsDir() || info.Mode().Perm() != dirMode || !runtimebundle.OwnedByCurrentUser(info) {
			continue
		}
		if err = i.pruneTree(ctx, parent, name, target); err != nil {
			uncertain = errors.Join(uncertain, fmt.Errorf("retain uncertain runtime %s: %w", name, err))
		}
	}
	return uncertain
}

func (i *TreeInstaller) pruneTree(ctx context.Context, parent, digest, target string) (err error) {
	lease, err := i.fs.TryLockExclusive(parent, digest)
	if errors.Is(err, runtimebundle.ErrLeaseBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	root := filepath.Join(parent, digest)
	manifest, err := runtimebundle.ReadManifest(ctx, i.fs, root, digest, target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	dirs, err := runtimebundle.VerifyTree(ctx, i.fs, root, manifest)
	if err != nil {
		return err
	}
	if err = i.privateTree(root, dirs, manifest); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = i.fs.RemoveAll(root); err != nil {
		return err
	}
	if err = i.fs.SyncDir(parent); err != nil {
		return err
	}
	log.From(ctx).Info("pruned unused immutable assistant runtime", "manifest", digest)
	return nil
}

// PrepareRuntime is the one startup installation policy for desktop launchers
// AND standalone coordinators. Darwin also copies into immutable profile trees:
// replacing an app bundle must not remove modules a live worker can import.
// The returned shared lease must be passed to the coordinator or adopted by
// its locator before the caller relinquishes ownership.
func PrepareRuntime(ctx context.Context, executable, dataDir, goos, goarch string) (TreeInstall, error) {
	source, err := runtimebundle.PackagedRoot(executable, goos, goarch)
	if err != nil {
		return TreeInstall{}, err
	}
	parent := filepath.Join(dataDir, "agent-runtime")
	installer := NewTreeInstaller(NewOSTreeFS())
	installed, err := installer.Ensure(ctx, source, parent, goos+"/"+goarch)
	if err != nil {
		return TreeInstall{}, err
	}
	if err = installer.Prune(ctx, parent, installed.Hash, goos+"/"+goarch); err != nil {
		if ctx.Err() != nil {
			return TreeInstall{}, errors.Join(ctx.Err(), installed.Close())
		}
		log.From(ctx).Warn("retaining uncertain older assistant runtime versions", "error", err)
	}
	return installed, nil
}
