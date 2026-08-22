package transfer

import (
	"errors"
	"fmt"
)

// ErrPosixRenameUnsupported is what the fallback keys on. A RemoteFS whose
// server does not advertise posix-rename@openssh.com returns an error
// satisfying errors.Is against THIS sentinel — internal/ssh wraps it — so
// neither package imports the other's errors.
var ErrPosixRenameUnsupported = errors.New("transfer: server does not support posix-rename@openssh.com")

// ErrInvalidUpload marks an Upload the sink cannot express. Path policy is
// the transport's and the provider's (§5.3); this is the sink refusing to
// join a Name it would have to interpret — a separator, "." or ".." would
// put the file somewhere other than where the caller named.
var ErrInvalidUpload = errors.New("transfer: invalid upload")

// ErrInvalidDownload marks a Download the source cannot express. Path
// policy is the transport's and the provider's (§5.3, which the download
// half applies unchanged); this is the read half refusing what it could
// only guess at — an empty path, a length no framing can carry, or a
// Download that names no open handle because it was never opened here.
var ErrInvalidDownload = errors.New("transfer: invalid download")

// SizeMismatchError is a reader that did not deliver what the caller
// declared, in either direction. Both are failures: the declared size is
// what the person and the progress bar were told, and a file of a different
// length is not the file they chose.
type SizeMismatchError struct {
	Declared int64
	Got      int64
	// AtLeast says Got is a lower bound rather than the total. The sink
	// stops reading as soon as the reader passes the declared size, so it
	// never learns how much more there was — and does not write the excess,
	// because a reader that lies must not be able to fill the server's disk.
	AtLeast bool
}

func (e *SizeMismatchError) Error() string {
	if e.AtLeast {
		return fmt.Sprintf("transfer: source declared %d bytes and delivered at least %d", e.Declared, e.Got)
	}
	return fmt.Sprintf("transfer: source declared %d bytes and delivered %d", e.Declared, e.Got)
}

// NameExhaustedError is a KeepBoth that could not find a free name. The
// bound is deliberate (D5): a directory holding 32 collisions is a person
// who needs to be told, not a loop that keeps trying.
//
// It is returned ONLY when every one of those attempts was refused with an
// error the sink could not classify — the ambiguous SSH_FX_FAILURE a v3
// server answers EEXIST with. A refusal that names itself (a permission, a
// missing directory, a gone lease; see RemoteFS.Create) stops the search on
// the spot and is returned as itself, because 31 more names cannot make a
// read-only directory writable and reporting that as "no free name" would
// be false rather than merely vague.
//
// Err is the last refusal seen, carried so the reason survives even though
// it could not be classified.
type NameExhaustedError struct {
	Name     string
	Attempts int
	Err      error
}

func (e *NameExhaustedError) Error() string {
	return fmt.Sprintf("transfer: no free name for %q after %d attempts: %v", e.Name, e.Attempts, e.Err)
}

func (e *NameExhaustedError) Unwrap() error { return e.Err }
