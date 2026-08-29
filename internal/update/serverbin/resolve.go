package serverbin

// Resolving the spawn path: the one decision a launcher must not make for
// itself, in the one place that knows why the two platforms differ.

import (
	"context"
	"path/filepath"
)

// Target is what the composition root knows and this package must not
// guess: which machine this is, where the running application is, and
// which release it claims to be.
type Target struct {
	// GOOS is the operating system, as runtime.GOOS reports it. A
	// parameter rather than a build tag so BOTH answers are compiled,
	// exercised and reachable on every platform: a build-tagged split
	// would leave this whole package unreferenced on darwin, which is
	// how a copy that never runs on the developer's machine stops being
	// checked at all.
	GOOS string
	// ExePath is the running application's own executable, from
	// os.Executable. "" resolves to the bare binary name, which is what
	// the exec failure can then name.
	ExePath string
	// DataDir is the profile's data directory (storage.Paths.DataDir) —
	// ~/.local/share/nocx, or the dev profile's, so a development build
	// never spawns a released daemon's copy.
	DataDir string
	// Version is the release version (internal/version.Version), which
	// names the copy.
	Version string
}

// Resolve returns the path the launcher must spawn nocx-server from.
//
// TWO answers, and each is wrong for the other platform.
//
// On darwin the binary ships inside the bundle beside the application
// (nocx.app/Contents/MacOS/nocx-server) and the bundle is an ordinary
// directory that stays mounted, so it is spawned where it lies. Copying it
// out would buy nothing and would put a second, older copy of the daemon
// under the user's home for the updater to keep in step.
//
// Everywhere else — Linux, where the shipped artefact is an AppImage — the
// image is a squashfs mounted through FUSE for exactly as long as the
// process that started it lives. The daemon exists to outlive the window,
// so spawning it from inside the image hands it an executable that
// disappears at the moment it must survive (design §4). It is installed to
// a versioned copy under DataDir/bin and spawned from there, always: a
// plain Linux build takes the same path, because deciding per-packaging
// would mean inferring the packaging, and the copy costs one hash of an
// already-warm file on every launch after the first.
//
// A failed install is returned as an error rather than degraded into the
// sibling path. The sibling would start, serve, and then die with the
// first window that closes — a backend that silently stops outliving the
// window is indistinguishable from the product working until somebody's
// session is gone.
func (i *Installer) Resolve(ctx context.Context, t Target) (string, error) {
	sibling := SiblingPath(t.ExePath)
	if t.GOOS == "darwin" {
		return sibling, nil
	}

	binDir := filepath.Join(t.DataDir, DirName)
	install, err := i.Ensure(ctx, sibling, binDir, t.Version)
	if err != nil {
		return "", err
	}

	// Pruning is housekeeping and must not stop a launch: the copy this
	// window needs is already installed, and a directory holding one
	// superseded binary too many is a footprint, not a failure. Unlinking
	// a copy some older daemon is still executing from is safe — the
	// inode outlives the name — so this runs before the spawn rather
	// than being deferred to a moment nobody reaches.
	if pruneErr := i.Prune(ctx, binDir, install.Name); pruneErr != nil {
		i.log.Warn("serverbin: could not prune superseded coordinator copies",
			"dir", binDir, "keeping", install.Name, "error", pruneErr)
	}
	return install.Path, nil
}
