// Package transfer writes one file onto a remote host: a temp file, a
// promote, progress, cancellation, and an honest account of what it left
// behind when something failed.
//
// It is the sink of the upload design
// (.internal/specs/2026-08-21-upload-to-the-active-tab-design.md §5.2). Put
// is given an io.Reader and does not know which source produced it — a file
// on the backend's disk or a stream off an HTTP body — which is what makes
// D1 one implementation rather than two.
//
// The package deliberately does NOT import internal/ssh. It declares the
// narrow write surface it needs and the lease satisfies it, the direction
// internal/filesystem already established where filesystem declares Caller
// and transport satisfies it (internal/filesystem/binding.go:62). The sink
// is therefore buildable and testable against a fake with no live server
// anywhere in its tests.
package transfer

import (
	"context"
	"io"
)

// RemoteFS is the write surface this package needs of a remote filesystem.
// Every method is one round trip.
//
// Two contracts the implementation owes, because the sink's correctness
// rests on them and neither is visible in a signature:
//
//   - Create opens with O_WRONLY|O_CREATE|O_EXCL and fails when the path
//     exists. It is not sftp.Client.Create, which is O_RDWR|O_CREATE|O_TRUNC
//     and would silently destroy a concurrent transfer's temp file (D5).
//   - A call that failed because the path does not exist reports it so that
//     errors.Is(err, fs.ErrNotExist) holds. pkg/sftp normalises
//     SSH_FX_NO_SUCH_FILE to os.ErrNotExist for exactly this
//     (client.go:2237), and the fallback needs it to tell "there was no
//     destination to back up" from "the backup failed".
type RemoteFS interface {
	Create(path string) (RemoteFile, error)
	// PosixRename replaces the destination atomically
	// (posix-rename@openssh.com). On a server without the extension it must
	// fail with an error satisfying errors.Is against
	// ErrPosixRenameUnsupported, distinguishably from every other failure,
	// because the fallback keys on exactly that.
	PosixRename(old, new string) error
	// Rename is plain SFTP v3 rename, which refuses an existing
	// destination (nocx-340t).
	Rename(old, new string) error
	Remove(path string) error
}

// RemoteFile is an open handle on the remote host. Write and Close are each
// one round trip; the sink issues one Write per chunk so no call outlives
// the lease's bounded lane (D2).
type RemoteFile interface {
	Write(p []byte) (int, error)
	Close() error
}

// Decision is what to do when the destination name is already taken. It is
// answered by a person before a byte moves (D5); the sink is never the one
// that decides.
type Decision string

const (
	Overwrite Decision = "overwrite"
	KeepBoth  Decision = "keepBoth"
	Skip      Decision = "skip"
)

// The states an Outcome can carry. Cancellation and failure are errors, not
// states: they arrive as the second return value of Put.
const (
	StateWritten = "written"
	StateSkipped = "skipped"
)

// Upload is one file's worth of instruction.
type Upload struct {
	// DestDir is an absolute directory in the provider's syntax. Path
	// policy belongs to the transport and the provider (§5.3); the sink
	// only refuses what it cannot express.
	DestDir string
	// Name is exactly ONE path component.
	Name string
	// Size is the declared length. The sink refuses a reader that
	// disagrees, in either direction.
	Size int64
	// OnExists is the person's collision decision.
	OnExists Decision
}

// Outcome is what Put leaves behind, said out loud.
//
// FinalName is the name actually written, which KeepBoth may have changed.
// Stranded is a list — never a single field — of paths the transfer created
// or moved and did not manage to clean up. It can carry two paths at once:
// a fallback that loses its second rename leaves both the backup holding
// the old content and the temp holding the new. An Outcome may be a success
// AND carry a stranded path (§6: unlink of the backup failing after the
// promote landed).
type Outcome struct {
	State     string
	FinalName string
	Stranded  []string
}

// Sink writes files onto one remote host.
type Sink interface {
	// Put writes r to u.DestDir/u.Name, calling progress with the running
	// byte total after each chunk that reached the server. It returns what
	// it left behind whether or not it succeeded: an error and a non-empty
	// Outcome.Stranded are not alternatives.
	Put(ctx context.Context, u Upload, r io.Reader, progress func(total int64)) (Outcome, error)
}
