package serverbin

// Pruning bounds the footprint to the copy in use. It is the other half of
// the versioned name: names that never collide are only affordable if the
// superseded ones eventually go.

import (
	"context"
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"regexp"
)

// copyFileName matches a name this package owns:
// nocx-server-<version>-<64 hex>. The hash is pinned to sha256's exact
// length and alphabet so a file that merely resembles ours — a user's own
// nocx-server-backup, a half-downloaded something — is never a candidate
// for deletion. Same discipline as deploy's installDirName, and for the
// same reason: this function deletes files in a directory under someone's
// home.
var copyFileName = regexp.MustCompile(`^` + BinaryName + `-.+-[0-9a-f]{64}$`)

// Prune removes every versioned copy in binDir EXCEPT keep, which is the
// name of the copy currently in use — [Install.Name].
//
// keep is required. Called with an empty name it would delete the copy the
// running daemon is executing from, and on Linux that daemon has no other
// path to its own executable; refusing is the only safe reading of "prune
// everything, keep nothing".
//
// A missing directory is not an error: nothing installed is nothing to
// prune.
func (i *Installer) Prune(ctx context.Context, binDir, keep string) error {
	if keep == "" {
		return errors.New("serverbin: prune: no copy named to keep — refusing to delete every installed coordinator")
	}

	entries, err := i.fs.ReadDir(binDir)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("serverbin: read %s: %w", binDir, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == keep || !copyFileName.MatchString(name) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := i.fs.Remove(filepath.Join(binDir, name)); err != nil && !errors.Is(err, iofs.ErrNotExist) {
			return fmt.Errorf("serverbin: prune %s: %w", name, err)
		}
		i.log.Info("serverbin: pruned a superseded coordinator copy", "name", name, "keeping", keep)
	}
	return nil
}
