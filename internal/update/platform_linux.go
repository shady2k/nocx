//go:build linux

package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

// appImageMagic is the type-2 AppImage magic at offset 8 of the ELF
// header: 'A', 'I', 0x02 (type 2).
var appImageMagic = []byte{0x41, 0x49, 0x02}

// elfMagic is the standard ELF magic: 0x7f, 'E', 'L', 'F'.
var elfMagic = []byte{0x7f, 0x45, 0x4c, 0x46}

// linuxPlatform implements [Platform] for Linux.
//
// The implementation follows ADR-0007: the updater ships an AppImage,
// a single self-contained executable file. Extract is a plain file
// copy + chmod, VerifyExtracted checks the executable bit and
// AppImage magic, and Exchange is an atomic renameat2(RENAME_EXCHANGE)
// of the running AppImage file.
type linuxPlatform struct{}

// NewPlatform returns the linux [Platform] implementation.
func NewPlatform() Platform {
	return &linuxPlatform{}
}

// Preflight implements [Platform.Preflight] with the APPIMAGE refusal.
//
// Self-update is only permitted when the app is running as a
// distributed AppImage (the APPIMAGE environment variable is set by
// the AppImage runtime). Without it the running process is a dev
// build, an unpacked binary, or a deb/rpm install — none of which
// can be self-replaced. This mirrors the darwin §7.7 refusals and
// matches electron-updater's behaviour.
func (p *linuxPlatform) Preflight(_ context.Context, installPath string) error {
	appImagePath := os.Getenv("APPIMAGE")
	if appImagePath == "" {
		return errors.New(
			"this is not a distributed AppImage build — the APPIMAGE environment variable is unset. " +
				"Self-update only works when nocx is launched from an AppImage. " +
				"If you installed nocx from a package manager (deb/rpm), updates are handled by your system. " +
				"Otherwise download the AppImage from https://github.com/shady2k/nocx/releases/latest",
		)
	}

	// The containing directory must be writable so the staged
	// AppImage can be placed and the atomic exchange can proceed.
	dir := filepath.Dir(installPath)
	if err := unix.Access(dir, unix.W_OK); err != nil {
		return fmt.Errorf("install directory %s is not writable — cannot stage or apply an update: %w", dir, err)
	}

	return nil
}

// Extract implements [Platform.Extract] for the single-file AppImage
// case.
//
// The archive IS the AppImage — already downloaded and
// sha256/size-verified by the core. This method copies it into
// destDir as the staged replacement and sets the executable bit.
// There is no bundle tree, no symlinks, and no zip extraction.
func (p *linuxPlatform) Extract(_ context.Context, archivePath, destDir string) error {
	staged := filepath.Join(destDir, filepath.Base(archivePath))

	src, err := os.Open(archivePath) //nolint:gosec // archivePath is from manifest-download, integrity-verified
	if err != nil {
		return fmt.Errorf("open archive %s: %w", archivePath, err)
	}
	defer func() { _ = src.Close() }()

	dst, err := os.Create(staged) //nolint:gosec // staged path is constructed in destDir
	if err != nil {
		return fmt.Errorf("create staged file %s: %w", staged, err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(staged)
		return fmt.Errorf("copy AppImage into staging directory: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("close staged file: %w", err)
	}

	if err := os.Chmod(staged, 0o755); err != nil { //nolint:gosec // AppImage must be executable
		_ = os.Remove(staged)
		return fmt.Errorf("set executable bit on staged AppImage %s: %w", staged, err)
	}

	return nil
}

// VerifyExtracted implements [Platform.VerifyExtracted] for AppImage.
//
// AppImage carries no OS-level signature (ADR-0007 Consequences —
// integrity already rests on the signed manifest plus the artefact
// sha256 verified at download). The checks here confirm the staged
// file is a plausible AppImage: it has the executable bit and starts
// with an ELF header followed by the AppImage type-2 magic at offset
// 8.
func (p *linuxPlatform) VerifyExtracted(_ context.Context, bundlePath string) error {
	info, err := os.Stat(bundlePath)
	if err != nil {
		return fmt.Errorf("stat staged bundle %s: %w", bundlePath, err)
	}

	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("staged AppImage %s is not executable (missing +x bit)", bundlePath)
	}

	// Read the first 11 bytes to check ELF + AppImage magic.
	f, err := os.Open(bundlePath) //nolint:gosec // bundlePath is caller-controlled, integrity already verified
	if err != nil {
		return fmt.Errorf("open staged bundle %s for magic check: %w", bundlePath, err)
	}
	defer func() { _ = f.Close() }()

	header := make([]byte, 11)
	if _, err := io.ReadFull(f, header); err != nil {
		return fmt.Errorf("read AppImage header from %s: %w", bundlePath, err)
	}

	if !bytes.HasPrefix(header, elfMagic) {
		return fmt.Errorf("staged file %s does not start with ELF magic — not a valid AppImage", bundlePath)
	}
	if !bytes.Equal(header[8:11], appImageMagic) {
		return fmt.Errorf("staged file %s is missing AppImage type-2 magic at offset 8 — not a valid AppImage", bundlePath)
	}

	return nil
}

// Exchange implements [Platform.Exchange] via renameat2(RENAME_EXCHANGE).
//
// This atomically swaps two single-file entries on the same
// filesystem. After a successful exchange installPath holds the new
// AppImage and replacementPath holds the previous one. This is the
// single-file analogue of the darwin RENAME_SWAP directory swap.
func (p *linuxPlatform) Exchange(_ context.Context, installPath, replacementPath string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, installPath, unix.AT_FDCWD, replacementPath, unix.RENAME_EXCHANGE); err != nil {
		return fmt.Errorf("atomic exchange of %s ↔ %s failed: %w", installPath, replacementPath, err)
	}
	return nil
}

// ArtifactID implements [Platform.ArtifactID].
//
// The linux runtime reports amd64 or arm64 (runtime.GOARCH); the
// manifest carries the same architecture string. No universal fat
// binary exists on Linux — matching is straightforward.
func (p *linuxPlatform) ArtifactID() ArtifactID {
	return ArtifactID{
		OS:     "linux",
		Arch:   runtime.GOARCH,
		Format: "AppImage",
	}
}
