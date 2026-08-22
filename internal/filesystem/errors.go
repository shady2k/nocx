package filesystem

import (
	"fmt"
	"time"

	"github.com/shady2k/nocx/internal/session"
)

// Domain error markers for the filesystem package. Transport switches on
// these to surface the right user-facing state; each wraps a distinguishable
// type the UI layer can map to an action, the way internal/ssh/errors.go does
// for connection failures.

// ErrNotFound — the path does not exist (ENOENT), including a broken symlink.
type ErrNotFound struct {
	Path string
	Err  error
}

func (e *ErrNotFound) Error() string {
	return fmt.Sprintf("filesystem: %s: no such file or directory", e.Path)
}

func (e *ErrNotFound) Unwrap() error { return e.Err }

// ErrPermission — an explicit refusal (EACCES), never a silently empty
// listing: permission denied is a node state the product surfaces, not an
// empty directory the user reads as "there is nothing here".
type ErrPermission struct {
	Path string
	Err  error
}

func (e *ErrPermission) Error() string {
	return fmt.Sprintf("filesystem: %s: permission denied", e.Path)
}

func (e *ErrPermission) Unwrap() error { return e.Err }

// ErrNotDir — the path names a non-directory (ENOTDIR) where a directory was
// required, i.e. List on a file.
type ErrNotDir struct {
	Path string
	Err  error
}

func (e *ErrNotDir) Error() string {
	return fmt.Sprintf("filesystem: %s: not a directory", e.Path)
}

func (e *ErrNotDir) Unwrap() error { return e.Err }

// ErrNotRegular — reading was refused by the §5.1 openability table. Kind is
// what the path actually is at call time: a directory, a FIFO, a device.
type ErrNotRegular struct {
	Path string
	Kind Kind
	Err  error
}

func (e *ErrNotRegular) Error() string {
	return fmt.Sprintf("filesystem: %s: not a regular file (kind %s)", e.Path, e.Kind)
}

func (e *ErrNotRegular) Unwrap() error { return e.Err }

// ErrInvalidPath — the path violates the provider's syntax rules: it must be
// absolute and already clean. The provider owns path syntax and rejects
// rather than silently rewriting a caller's path.
type ErrInvalidPath struct {
	Path   string
	Reason string
}

func (e *ErrInvalidPath) Error() string {
	return fmt.Sprintf("filesystem: invalid path %q: %s", e.Path, e.Reason)
}

// ErrInvalidPage — offset or limit violate the Page contract: offset >= 0 and
// limit >= 1. A zero limit is never produced by the wire and means a caller
// bug, so it errors rather than silently defaulting.
type ErrInvalidPage struct {
	Offset, Limit int
}

func (e *ErrInvalidPage) Error() string {
	return fmt.Sprintf("filesystem: invalid page (offset %d, limit %d)", e.Offset, e.Limit)
}

// ErrTooLarge — the directory exceeds the entry cap (D14): "this directory
// has more than N entries; nocx does not display directories this large".
// ObservedCount is exact only when a complete enumeration was actually paid
// for; the local provider always pays for one, so it is exact here. No
// pagination is offered and polling is disabled for a capped directory.
type ErrTooLarge struct {
	ObservedCount int
	Limit         int
}

func (e *ErrTooLarge) Error() string {
	return fmt.Sprintf("filesystem: directory has %d entries, exceeding the cap of %d", e.ObservedCount, e.Limit)
}

// ErrTimedOut — the enumeration exceeded the elapsed-time cap (D14). It is
// deliberately not the user-facing explanation: the same directory would pass
// on one network and fail on another. Partial results are discarded, never
// returned as if complete.
type ErrTimedOut struct {
	Timeout time.Duration
}

func (e *ErrTimedOut) Error() string {
	return fmt.Sprintf("filesystem: directory enumeration exceeded the %s time cap", e.Timeout)
}

// ErrTooLargeSize — the listing exceeded the response-size ceiling (spec
// §5.1, the third bound). The entry cap bounds the count; this bounds what
// equal counts can cost, because 5,000 entries with 4 KB symlink targets are
// a different object from 5,000 short names. ObservedBytes is exact: the
// local provider enumerates completely before refusing. Partial results are
// discarded, like tooLarge.
type ErrTooLargeSize struct {
	ObservedBytes int64
	Limit         int64
}

func (e *ErrTooLargeSize) Error() string {
	return fmt.Sprintf("filesystem: listing is about %d bytes, exceeding the %d-byte cap", e.ObservedBytes, e.Limit)
}

// ErrUnknownBinding — Acquire or Close named a binding id that does not exist
// or is already closed. A binding id is not a bearer token; it is also
// unguessable (minted from crypto/rand), so reaching this error through
// guessing is not possible.
type ErrUnknownBinding struct {
	ID string
}

func (e *ErrUnknownBinding) Error() string {
	return fmt.Sprintf("filesystem: unknown binding %q", e.ID)
}

// ErrNotOwned — the caller does not Own the binding's session (D15). The
// binding exists; the caller may not use it. This is the authorisation check
// that lives inside Acquire so no handler can forget it.
type ErrNotOwned struct {
	ID        string
	SessionID session.ID
}

func (e *ErrNotOwned) Error() string {
	return fmt.Sprintf("filesystem: binding %q belongs to session %q, which the caller does not own", e.ID, e.SessionID)
}

// ErrHandleReleased — a method was called on a Handle after its release func
// ran. The handle is valid from Acquire until release and invalid after;
// this error is the second end of that interval.
type ErrHandleReleased struct{}

func (e *ErrHandleReleased) Error() string { return "filesystem: handle released" }

// ErrWatchUnavailable — Watch is declared by the Provider contract (spec
// §5.1) but the watching wave — fsnotify locally, polling over SFTP — is a
// later step of the design's sequence (§6 step 5). Until then the local
// provider refuses with this error rather than returning a watch that would
// never fire, which would be a silent lie the product could not surface.
type ErrWatchUnavailable struct{}

func (e *ErrWatchUnavailable) Error() string { return "filesystem: watching is not available yet" }

// ErrUploadUnsupported — Uploader was called on a binding with no write seam,
// which is a binding whose provider did not implement Uploader. That is the
// upload design's rule R1 ("a file can only be uploaded to the machine the
// tab is actually on") arriving as a typed refusal rather than as a check
// somebody performs: a provider that cannot write contributes no sink, and
// the binding then has nothing to write through.
//
// Both shipped providers CAN write, so no binding the composition root mints
// takes this refusal today (D7, as corrected: a browser drop on a local tab
// has bytes and no path, so it uploads onto the backend's own machine, which
// is the machine that tab's shell is on). The refusal is the seam's shape,
// not a case that has gone away: the next provider that cannot write inherits
// it without anybody adding a check.
//
// It names the binding rather than the path: the refusal is a property of the
// binding, not of what was being written.
type ErrUploadUnsupported struct {
	BindingID string
}

func (e *ErrUploadUnsupported) Error() string {
	return fmt.Sprintf("filesystem: binding %q has no write seam; files cannot be uploaded to this tab", e.BindingID)
}

// ErrDownloadUnsupported — Downloader was called on a binding with no
// read-stream seam, which is a binding whose provider did not implement
// Downloader. It is rule R1 in the read direction, arriving as a typed
// refusal rather than as a check somebody performs: a provider that cannot
// stream contributes no source, and the binding then has nothing to read
// through.
//
// Both shipped providers CAN stream, so no binding the composition root
// mints takes this refusal today. The refusal is the seam's shape, not a
// case that has gone away: the next provider that cannot stream inherits it
// without anybody adding a check.
//
// It names the binding rather than the path, because the refusal is a
// property of the binding and not of what was being read.
type ErrDownloadUnsupported struct {
	BindingID string
}

func (e *ErrDownloadUnsupported) Error() string {
	return fmt.Sprintf("filesystem: binding %q has no read-stream seam; files cannot be downloaded from this tab", e.BindingID)
}
