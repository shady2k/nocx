//go:build darwin

package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/shady2k/nocx/internal/desktopidentity"
	"github.com/shady2k/nocx/internal/log"
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

	// The containing directory must be writable so the update
	// transaction can create its staging directory and perform the
	// atomic exchange.
	dir := filepath.Dir(installPath)
	if err := unix.Access(dir, unix.W_OK); err != nil {
		log.From(ctx).Warn("update install directory is not writable", "path", dir, "error", err)
		return errors.New("the Applications folder is not writable — allow writes to Applications and try again")
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
// Two checks: codesign --verify --deep --strict confirms the bundle's
// signature has not been damaged, and lipo -archs confirms the
// universal binary still carries both slices. Either failure means the
// bundle must not be swapped in.
func (p *darwinPlatform) VerifyExtracted(ctx context.Context, bundlePath string) error {
	// codesign integrity check. The shipped bundle carries the project
	// code-signing certificate (ADR-0052); it used to carry the ad-hoc
	// signature Wails applied, and this check long predates the change
	// of signer. What it verifies is unchanged either way: that the
	// extracted bundle's seal is intact, not who signed it — who signed
	// it proves nothing to a stranger, and the download's integrity is
	// the ed25519-signed manifest. Packaging must not damage the seal.
	{
		cmd := exec.CommandContext(ctx, "codesign", "--verify", "--deep", "--strict", bundlePath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("codesign verification of %s failed (the bundle's signature may have been damaged in transit or extraction): %w\n%s", bundlePath, err, string(out))
		}
	}

	// Universal slice check. The release pipeline builds with
	// -platform darwin/universal; both slices must survive the
	// pack/extract round-trip.
	{
		binary := filepath.Join(bundlePath, "Contents", "MacOS", "nocx")
		cmd := exec.CommandContext(ctx, "lipo", "-archs", binary) //nolint:gosec // fixed argv; binary is the verified extracted bundle path
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

// InspectBundle implements [Platform.InspectBundle] for a macOS .app bundle.
//
// Ownership is the bundle's own identifier, read from the Info.plist inside
// it: the release passes -productidentifier dev.nocx (ADR-0103) and a release
// reads CFBundleIdentifier back as that value, so a bundle declaring anything
// else is not one of ours however it is named. The version is
// CFBundleShortVersionString, which is what lets reconcile keep a copy NEWER
// than the running install instead of deleting the only copy of that release.
//
// An unreadable plist is not proof of ownership: it answers Nocx false and the
// caller refuses with the path, which is the safe direction for a directory
// this code is considering deleting.
func (p *darwinPlatform) InspectBundle(ctx context.Context, path string) (BundleReport, error) {
	plist := filepath.Join(path, "Contents", "Info.plist")
	out, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", plist).Output()
	if err != nil {
		return BundleReport{}, nil
	}
	var info struct {
		Identifier string `json:"CFBundleIdentifier"`
		Version    string `json:"CFBundleShortVersionString"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return BundleReport{}, nil
	}
	return BundleReport{
		Nocx:    info.Identifier == desktopidentity.ID,
		Version: strings.TrimSpace(info.Version),
	}, nil
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
