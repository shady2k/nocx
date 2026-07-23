// Package update implements the cross-platform auto-update mechanism.
//
// The package is structured around a thin [Platform] seam (per
// ADR-0006): one platform-agnostic core (manifest fetch, ed25519
// verification, semver comparison, artefact matching, and the
// crash-consistency transaction) plus per-OS implementations of the
// Platform interface.
//
// This file defines the Platform interface and the ArtifactID type
// used for manifest matching. The contract is stable — new platforms
// (Linux, Windows) add implementations behind the same interface
// without touching the core.
package update

import "context"

// ArtifactID identifies a platform-specific artefact in the release
// manifest. The triple {OS, Arch, Format} is matched against the
// manifest's artifacts[] entries (see [MatchArtifact]).
type ArtifactID struct {
	OS     string // "darwin", "linux", "windows"
	Arch   string // "amd64", "arm64", "universal"
	Format string // "zip", "AppImage"
}

// Platform is the OS-specific seam for auto-update operations.
//
// Every method on this interface may be called from the
// platform-agnostic transaction core; none of them need to be
// goroutine-safe because the core serialises operations under a
// single advisory flock (§7.6 of the design).
//
// Methods that return an error must return a descriptive, legible
// message that the frontend can surface directly to the user.
type Platform interface {
	// Preflight checks whether the running installation can be
	// updated. It returns nil if updating is permitted, or a
	// descriptive error if the installation must be refused.
	//
	// On darwin this implements the §7.7 refusals: dev builds and
	// translocated (quarantined) bundles are refused with remediation
	// hints. The installPath is the absolute path to the running
	// .app bundle (or AppImage file on linux).
	Preflight(ctx context.Context, installPath string) error

	// Extract unpacks the downloaded archive at archivePath into
	// destDir. destDir already exists and is empty; archivePath is a
	// regular file whose sha256 and size have already been verified.
	//
	// On darwin this runs ditto(1) because archive/zip cannot restore
	// executable bits or symlinks inside a .app bundle.
	Extract(ctx context.Context, archivePath, destDir string) error

	// VerifyExtracted checks the integrity and platform compliance of
	// the extracted bundle at bundlePath. This is called immediately
	// after [Extract] and before the atomic exchange.
	//
	// On darwin this runs codesign(1) --verify --deep --strict and
	// checks that lipo -archs reports both arm64 and x86_64 slices.
	VerifyExtracted(ctx context.Context, bundlePath string) error

	// Exchange atomically swaps the installed bundle at installPath
	// with the staged replacement at replacementPath. After a
	// successful exchange, installPath holds the new bundle and
	// replacementPath holds the previous one.
	//
	// On darwin this uses renameatx_np(RENAME_SWAP), which is
	// atomic on APFS. Both paths must exist and be on the same
	// filesystem.
	Exchange(ctx context.Context, installPath, replacementPath string) error

	// ArtifactID returns the platform identifier used to select an
	// artefact from the release manifest. The returned value is
	// passed to [MatchArtifact].
	ArtifactID() ArtifactID
}
