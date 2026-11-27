// Package transfer streams files through filesystem providers, accounting
// for progress, cancellation, and paths left behind after a failed upload.
package transfer

import (
	"context"
	"io"
)

// RemoteFS is the write surface the staged upload needs.
type RemoteFS interface {
	// Create opens a new writable file exclusively. A name collision
	// must satisfy errors.Is(err, fs.ErrExist).
	Create(path string) (RemoteFile, error)
	// Rename moves the temp into place, atomically replacing the destination.
	Rename(old, new string) error
	// Remove deletes a file. A missing path must satisfy errors.Is(err, fs.ErrNotExist).
	Remove(path string) error
}

// RemoteFile is an open upload handle.
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
// Stranded lists the paths the transfer created and could not clean up,
// such as an upload temp and its KeepBoth reservation.
type Outcome struct {
	State     string
	FinalName string
	Stranded  []string
}

// Sink writes files to one filesystem.
type Sink interface {
	// Put writes r to u.DestDir/u.Name, calling progress with the running
	// byte total after each chunk written. It returns what
	// it left behind whether or not it succeeded: an error and a non-empty
	// Outcome.Stranded are not alternatives.
	//
	// Cancellation, and what the CALLER owes. Put observes ctx between
	// chunks, so cancellation waits for one filesystem call and one source
	// read — and a source read is where the bound can be lost, because an
	// io.Reader has no cancellation and Put cannot abandon a Read already
	// in flight. Cancelling the context while the reader is blocked
	// therefore does NOT return: Put waits for that Read, and a stalled
	// HTTP body waits forever.
	//
	// So the caller owns unblocking the reader. Whoever supplies a streamed
	// body must close it when the request is cancelled; the sink's half of
	// the bargain is that once the reader reports a failure, Put unwinds at
	// once — the reason reaches the caller, the temp is cleaned up, and
	// anything that could not be cleaned up is named in Outcome.Stranded.
	//
	// A reader that cannot be unblocked at all — one with no Close and no
	// deadline — must not be handed to Put.
	Put(ctx context.Context, u Upload, r io.Reader, progress func(total int64)) (Outcome, error)
}
