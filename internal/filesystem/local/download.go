package local

// The read-stream half of the local view — the mirror of upload.go.
//
// # What this adds, and what it does not
//
// The backend already reads its own files at the renderer's request:
// files.read has done exactly that since the file manager shipped. What is
// new here is only that the read is UNBOUNDED and streamed rather than
// capped at 8 MiB and returned as text in a JSON result. The authority is
// the same authority — a binding whose session the connection owns, over a
// path that binding's provider validates — and the transport applies the
// same path validation to a download's path that it applies to an upload's
// destination.
//
// It is also why upload's rule R2 has no counterpart here, and the
// asymmetry is worth stating rather than leaving for somebody to notice.
// R2 forbids the renderer NAMING a source on the BACKEND's disk, because
// such a path is scoped by nothing: a renderer that could spell one could
// have the backend read ~/.ssh/id_ed25519 and send it to a host of the
// renderer's choosing. A download's path is scoped by the binding it is
// addressed through — the caller can already list it and read it with
// files.read — so naming it is not new authority. What would be new
// authority is a download addressed at a binding the caller does not own,
// and that is R1, which Registry.Acquire enforces on this call as on every
// other.

import (
	"fmt"
	"os"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/transfer"
)

// Source returns the read-stream half of this provider's view — the
// optional filesystem.Downloader seam.
//
// It is never nil: a provider that implements Downloader can stream. The
// source is built per call because it holds no state of its own; `os` is
// the whole of its dependency, exactly as it is for Sink.
//
// internal/transfer is reused rather than paralleled, for Sink's reason and
// one more: the chunk bound, the cancellation point and the size rule are
// one implementation for both providers AND for both directions, so a
// download off a local tab and a download off a remote one differ in the
// adapter underneath and in nothing a person can observe.
func (p *Provider) Source() transfer.Source {
	return transfer.NewSource(osReadFS{}, transfer.DefaultChunk)
}

// osReadFS presents this machine's filesystem as the read surface
// internal/transfer declares. It is the local counterpart of the
// composition root's fsTransferLease.
type osReadFS struct{}

// Open opens path for reading and reports the size of the object it
// actually opened.
//
// The kind is asked TWICE, by name and then of the open handle, and the
// order is load-bearing rather than defensive. Opening is not a harmless
// question to ask of every path: os.Open on a fifo with no writer BLOCKS,
// and a download must never be able to wedge the handler that took it. So
// the cheap question by name comes first and refuses everything that is not
// a regular file; the open then happens; and the authoritative question is
// asked of the thing actually opened, which is the identity the transfer
// will read. A path that becomes a fifo between the two is the one shape
// this ordering does not cover, and it is a race a person would have to run
// against their own click.
//
// The size comes from the second question for the same reason: it measures
// the bytes being held, not the bytes some name resolved to a moment ago.
// That is what makes it safe to declare as the response's length.
func (osReadFS) Open(path string) (transfer.RemoteReader, int64, error) {
	if err := checkPath(path); err != nil {
		return nil, 0, err
	}
	byName, err := os.Stat(path)
	if err != nil {
		return nil, 0, wrapPathErr("stat", path, err)
	}
	if !byName.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%w: %s", transfer.ErrNotRegular, path)
	}
	// #nosec G304 — a caller-supplied path is the feature. It reached here
	// through a binding whose session the connection owns and through the
	// transport's own absolute-and-clean validation, and it is the same
	// path files.read would already have read on the same authority. The
	// only difference is the bound.
	f, err := os.Open(path) //nolint:gosec // see above
	if err != nil {
		// The concrete type must not be returned as a nil interface: a
		// non-nil transfer.RemoteReader holding a nil *os.File would pass
		// the engine's error check and panic on the first Read.
		return nil, 0, wrapPathErr("open", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, wrapPathErr("stat", path, err)
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, 0, fmt.Errorf("%w: %s", transfer.ErrNotRegular, path)
	}
	return f, info.Size(), nil
}

// Compile-time proof that the read half is what the design says it is. The
// composition root asserts the same thing at the wiring, and both are
// wanted for the reason upload.go already gives.
var (
	_ filesystem.Downloader = (*Provider)(nil)
	_ transfer.RemoteReadFS = osReadFS{}
)
