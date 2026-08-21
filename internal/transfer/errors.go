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
// Err is the last refusal seen. It is carried because the SFTP wire cannot
// distinguish EEXIST from a generic failure on a v3 server, so a permission
// error and a lost race look alike here and only the reason tells them
// apart.
type NameExhaustedError struct {
	Name     string
	Attempts int
	Err      error
}

func (e *NameExhaustedError) Error() string {
	return fmt.Sprintf("transfer: no free name for %q after %d attempts: %v", e.Name, e.Attempts, e.Err)
}

func (e *NameExhaustedError) Unwrap() error { return e.Err }
