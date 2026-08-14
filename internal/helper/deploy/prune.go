package deploy

// Pruning (D25): pruning bounds the footprint to the version in use — it
// never touches the directory named as keep. Neither pruning nor anything
// else in this package ever follows a symlink or touches anything else
// under the home: ~/.nocx's other contents belong to the shell bundle, and
// the shell bundle's own uninstall is the publisher's. (Uninstall — the
// removal of the whole ~/.nocx/helper tree — is deliberately not here yet:
// nothing in the product offers a user a way to revoke a helper from a
// host, and a function with no caller does not land. The implementation
// and its D25 tests are parked at park/nocx-aggw-uninstall and land with
// the consent surface that calls them, nocx-aggw.)

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"path"
	"regexp"
)

// installDirName matches the D7 directory pattern
// <version>-<goos>-<goarch>-<hash> for any numeric version: a directory we
// own and may prune. The hash is pinned to SHA-256 hex so a foreign
// directory that merely resembles ours is left alone.
var installDirName = regexp.MustCompile(`^[0-9]+-[^-]+-[^-]+-[0-9a-f]{64}$`)

// Prune bounds the install footprint: it removes every sibling install
// directory under ~/.nocx/helper EXCEPT the one named keep — the version
// currently in use is never touched (D25). Only directories matching our
// naming pattern are candidates; anything else in the directory is left
// strictly alone.
func Prune(ctx context.Context, fs RemoteFS, home string, keep string) error {
	root := path.Join(home, ".nocx", helperRootName)
	entries, err := fs.ReadDir(root)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return nil // nothing installed, nothing to prune
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == keep {
			continue
		}
		if !installDirName.MatchString(name) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := removeTree(fs, path.Join(root, name)); err != nil {
			return fmt.Errorf("deploy: prune %s: %w", name, err)
		}
	}
	return nil
}

// removeTree removes dir and everything under it without ever following a
// symlink: a symlink entry is removed as the link itself, never traversed.
// Absence is not an error.
func removeTree(fs RemoteFS, dir string) error {
	entries, err := fs.ReadDir(dir)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		p := path.Join(dir, e.Name())
		if e.IsDir() {
			// ReadDir reports a symlink as a symlink (lstat semantics), so
			// IsDir is the truth: a directory recurses, a symlink is
			// removed as the link.
			if err := removeTree(fs, p); err != nil {
				return err
			}
			continue
		}
		if err := fs.Remove(p); err != nil && !errors.Is(err, iofs.ErrNotExist) {
			return err
		}
	}
	return fs.Remove(dir)
}
