package assistant

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/filesystem"
)

// FileVersion is the identity of the bytes a person was shown, recorded with an
// approval and re-read when the answer comes back.
//
// It is the PANE's own vocabulary — filesystem.Version, the size, modification
// time and sha256 the pane's provider reports for exactly those bytes — and it
// is read through the pane's Handle (RunContext.Files). Before stage 10 the
// evidence was an os.Stat and an os.ReadFile of the named path, which on an SSH
// pane described this machine's same-named file: the question read a local file
// that had nothing to do with the call, and an approval could bind bytes the
// person was never shown (nocx-a62cl.11.2, found while planning the stage).
type FileVersion struct {
	// Path is the path the model named, kept so a refusal names the file the
	// person has to reconsider.
	Path string
	// Canonical is the pane's own identity for Path at capture time — symlinks
	// followed, the provider's rules applied. Verification resolves the path
	// again and compares, so a path that now names another object is a
	// different file and is refused as one.
	Canonical string
	// Version is what the pane reported for the bytes at Canonical.
	Version filesystem.Version
}

// FileVersionMismatchError means the approved path no longer names the same
// version. Path is deliberately part of the error so a refusal identifies the
// file the person must reconsider.
type FileVersionMismatchError struct {
	Path   string
	Reason string
}

func (e *FileVersionMismatchError) Error() string {
	return fmt.Sprintf("file %q changed since approval: %s", e.Path, e.Reason)
}

// CaptureFileVersion records the version an approval is about to bind: the
// canonical identity of path and the exact bytes the pane holds there.
//
// Every failure is the caller's to treat as "nothing was captured" — a nil
// source, a source that cannot be acquired, a path that does not exist, a file
// that is not regular, a file over filesystem.MaxReadBytes — because an
// approval that cannot say WHAT it covers must not be able to authorise it.
// The only caller that reads a failure as an ordinary answer is files.create,
// whose success precondition is a path that does not exist yet.
//
// The handle lives for this one read and is released on every path, so a tool
// never holds a binding between calls.
func CaptureFileVersion(ctx context.Context, source filesystem.HandleSource, path string) (FileVersion, error) {
	if source == nil {
		return FileVersion{}, fmt.Errorf("file version %q: no file binding", path)
	}
	handle, release, err := source.Acquire(ctx)
	if err != nil {
		return FileVersion{}, fmt.Errorf("file version %q: acquire: %w", path, err)
	}
	defer release()
	canonical, err := handle.Canonical(ctx, path)
	if err != nil {
		return FileVersion{}, fmt.Errorf("file version %q: canonical: %w", path, err)
	}
	versioned, err := handle.Versioned()
	if err != nil {
		return FileVersion{}, fmt.Errorf("file version %q: versioned: %w", path, err)
	}
	_, version, err := versioned.ReadVersion(ctx, canonical, filesystem.MaxReadBytes)
	if err != nil {
		return FileVersion{}, fmt.Errorf("file version %q: read version: %w", path, err)
	}
	return FileVersion{Path: path, Canonical: canonical, Version: version}, nil
}

// VerifyFileVersion checks the approved path again, immediately before
// dispatch and through the same pane binding the capture used. It answers nil
// only when the path still resolves to the object the person was shown and that
// object still has the same size, modification time and sha256.
//
// The interval this proves is between the capture and this read. A change that
// lands after it is caught by the conditional write itself (filesystem.Expect
// at the host's rename), which is the last and strongest check; what this one
// buys is the honest answer — "changed since the question" — before a
// capability is even constructed.
func VerifyFileVersion(ctx context.Context, source filesystem.HandleSource, version FileVersion) error {
	if source == nil {
		return fmt.Errorf("file version %q: no file binding", version.Path)
	}
	handle, release, err := source.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("file version %q: acquire for verification: %w", version.Path, err)
	}
	defer release()
	canonical, err := handle.Canonical(ctx, version.Path)
	if err != nil {
		return fmt.Errorf("file version %q: canonical for verification: %w", version.Path, err)
	}
	if canonical != version.Canonical {
		return &FileVersionMismatchError{Path: version.Path, Reason: "the path now names a different object"}
	}
	versioned, err := handle.Versioned()
	if err != nil {
		return fmt.Errorf("file version %q: versioned for verification: %w", version.Path, err)
	}
	_, current, err := versioned.ReadVersion(ctx, canonical, filesystem.MaxReadBytes)
	if err != nil {
		return fmt.Errorf("file version %q: read for verification: %w", version.Path, err)
	}
	if reason := versionDifference(version.Version, current); reason != "" {
		return &FileVersionMismatchError{Path: version.Path, Reason: reason}
	}
	return nil
}

// versionDifference names the field that moved, or "" when the two versions
// describe the same bytes. The order is the one a reader would check them in:
// a different length is not the same file, a different mtime means it was
// written, and the digest is what catches a rewrite of the same length inside
// one timestamp granule.
func versionDifference(want, got filesystem.Version) string {
	switch {
	case got.Size != want.Size:
		return "size differs"
	case got.MtimeNs != want.MtimeNs:
		return "modification time differs"
	case got.SHA256 != want.SHA256:
		return "content hash differs"
	default:
		return ""
	}
}

// isNotFound reports whether err is the pane saying the path does not exist.
// A file tool's own answer depends on it — files.create's success precondition
// is a missing path — and it is asked here so the one spelling of "missing" the
// filesystem seam defines is spelled once.
func isNotFound(err error) bool {
	var notFound *filesystem.ErrNotFound
	return errors.As(err, &notFound)
}

func cloneFileVersions(versions []FileVersion) []FileVersion {
	if versions == nil {
		return nil
	}
	return append([]FileVersion(nil), versions...)
}
