//go:build darwin

package update

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shady2k/nocx/internal/version"
	"golang.org/x/sys/unix"
)

// darwinPlatform implements [Platform] for macOS.
//
// The implementation follows §7 of the distribution-and-updates
// design: ditto for extraction (archive/zip cannot restore executable
// bits or symlinks), codesign + lipo for verification, and
// renameatx_np(RENAME_SWAP) for the atomic bundle exchange.
type darwinPlatform struct{}

// NewPlatform returns the darwin [Platform] implementation.
func NewPlatform() Platform {
	return &darwinPlatform{}
}

// Preflight implements [Platform.Preflight] with the §7.7 refusals.
func (p *darwinPlatform) Preflight(ctx context.Context, installPath string) error {
	// Dev builds must never attempt self-update.
	if version.Version == "dev" {
		return errors.New("this is a development build; self-update is not available — use a release build or run wails dev for local development")
	}

	// A translocated bundle runs from a read-only randomised path and
	// cannot replace itself. The user must move it to /Applications
	// (or any stable writable location) first.
	if strings.Contains(installPath, "AppTranslocation") {
		return fmt.Errorf(
			"nocx is running from a translocated path (%s). "+
				"This happens when the app is launched directly from a DMG or download "+
				"without being moved to a stable location first. "+
				"Move nocx to /Applications (or any writable directory) and try again.",
			installPath,
		)
	}

	// The containing directory must be writable so the update
	// transaction can create its staging directory and perform the
	// atomic exchange.
	dir := filepath.Dir(installPath)
	if err := unix.Access(dir, unix.W_OK); err != nil {
		return fmt.Errorf("install directory %s is not writable — cannot stage or apply an update: %w", dir, err)
	}

	return nil
}

// Extract implements [Platform.Extract] via ditto(1).
//
// ditto -x -k preserves symlinks, extended attributes, and executable
// bits that archive/zip would lose. --noqtn prevents any quarantine
// attribute from riding along in the extracted tree.
func (p *darwinPlatform) Extract(ctx context.Context, archivePath, destDir string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/ditto", "-x", "-k", "--noqtn", archivePath, destDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ditto extract of %s into %s failed: %w\n%s", archivePath, destDir, err, string(out))
	}
	return nil
}

// VerifyExtracted implements [Platform.VerifyExtracted].
//
// Two checks: codesign --verify --deep --strict confirms the ad-hoc
// signature Wails applied at build time has not been damaged, and
// lipo -archs confirms the universal binary still carries both
// slices. Either failure means the bundle must not be swapped in.
func (p *darwinPlatform) VerifyExtracted(ctx context.Context, bundlePath string) error {
	// codesign integrity check. Wails v2.13 unconditionally runs
	// codesign --force --deep --sign - on a production build, so the
	// bundle carries a real (ad-hoc) signature even without a
	// Developer ID. Packaging must not damage it.
	{
		cmd := exec.CommandContext(ctx, "codesign", "--verify", "--deep", "--strict", bundlePath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("codesign verification of %s failed (the ad-hoc signature Wails applied may have been damaged): %w\n%s", bundlePath, err, string(out))
		}
	}

	// Universal slice check. The release pipeline builds with
	// -platform darwin/universal; both slices must survive the
	// pack/extract round-trip.
	{
		binary := filepath.Join(bundlePath, "Contents", "MacOS", "nocx")
		cmd := exec.CommandContext(ctx, "lipo", "-archs", binary)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("lipo -archs %s failed: %w\n%s", binary, err, string(out))
		}
		archs := strings.TrimSpace(string(out))
		hasArm64 := strings.Contains(archs, "arm64")
		hasX8664 := strings.Contains(archs, "x86_64")
		if !hasArm64 || !hasX8664 {
			return fmt.Errorf("universal binary at %s is missing slices: got %q, want both arm64 and x86_64", binary, archs)
		}
	}

	return nil
}

// Exchange implements [Platform.Exchange] via renameatx_np(RENAME_SWAP).
//
// This is an atomic same-filesystem swap of two directory entries
// (both .app bundles are directories). APFS supports this; a crash
// between the call and the return either happened or didn't — no
// observer sees a missing install path.
func (p *darwinPlatform) Exchange(ctx context.Context, installPath, replacementPath string) error {
	if err := unix.RenameatxNp(unix.AT_FDCWD, installPath, unix.AT_FDCWD, replacementPath, unix.RENAME_SWAP); err != nil {
		return fmt.Errorf("atomic exchange of %s ↔ %s failed: %w", installPath, replacementPath, err)
	}
	return nil
}

// ArtifactID implements [Platform.ArtifactID].
//
// The darwin runtime may report arm64 or amd64, but the release
// manifest declares "universal" because the build is a fat binary.
// [MatchArtifact] handles this: it tries an exact architecture match
// first, then falls back to "universal" for darwin.
func (p *darwinPlatform) ArtifactID() ArtifactID {
	return ArtifactID{
		OS:     "darwin",
		Arch:   "universal",
		Format: "zip",
	}
}

// Ensure runtime is not flagged as unused.
var _ = runtime.GOOS
