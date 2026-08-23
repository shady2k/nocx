package transfer

// The read half: one file's bytes, taken OFF a host and streamed out.
//
// # Why this is in the sink's package and is not the sink inverted
//
// Download is upload's mirror, and the mirror is exact in the part that
// carries the bytes and empty in the part that carries the risk. What
// sink.go actually owns is a temp name, an O_EXCL reservation, a KeepBoth
// suffix search, two promote strategies and an account of what was left
// behind when one of them failed. NONE of that has a counterpart here: a
// download creates nothing on the source host, renames nothing, replaces
// nothing and can strand nothing. It opens a file for reading and copies.
//
// So the two share this package, its vocabulary (SizeMismatchError, the
// progress signature, the chunk bound) and — literally — the chunk loop,
// which is copyChunks and is the one place either direction decides how
// many bytes cross per call, when cancellation is observed and when
// progress is reported. They do not share Put, because two thirds of Put
// is about a destination this direction does not have.
//
// # The asymmetry that matters most, said once
//
// An upload can be undone. Its bytes land in a temp file, and a failure
// before the promote leaves the destination exactly as it was — which is
// what makes §6's whole "dest" column meaningful.
//
// A download cannot be undone. Bytes handed to the client are gone: there
// is no temp, no promote and no rollback, and a failure after the first
// byte leaves the far end holding a partial file. That is why the size is
// pinned at OPEN and enforced on the way past — a body that silently ran
// short of the length its own framing declared is a corrupt file the client
// may still keep — and it is why nothing in this file has a Stranded list
// to report.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
)

// ErrNotRegular is the refusal for a source that is not a regular file: a
// directory has no byte stream, a device or a procfs pseudo-file has no
// length to declare, and a fifo blocks. It is this package's own sentinel
// rather than internal/filesystem's because filesystem imports transfer and
// not the other way round; the adapters map their platform's answer onto
// it, which is the same shape ErrPosixRenameUnsupported already has.
var ErrNotRegular = errors.New("transfer: not a regular file")

// RemoteReadFS is the read surface this package needs of a host's
// filesystem — the mirror of RemoteFS, and as narrow.
//
// Three contracts the implementation owes, none of them visible in the
// signature and all three load-bearing:
//
//   - Open must not BLOCK on a path that is not a regular file. Opening a
//     fifo with no writer waits for one, and a download must never be able
//     to wedge the handler that took it. The answer both adapters use is
//     the one internal/transport's source-ticket mint already uses: ask
//     the kind by name first, cheaply, then open, then ask the OPEN object
//     again. A path that becomes a fifo between the two is the one shape
//     the ordering does not cover.
//   - The size is fstat'd on the OPEN object and never on the name. A name
//     is not an identity: between the answer to files.download and the
//     fetch that redeems it the name can be renamed, replaced, or be a
//     symlink whose target moved, and a size measured on the name would
//     then describe different bytes from the ones being sent. The open
//     handle pins the inode, so the size and the bytes are the same object
//     by construction.
//   - A refusal the adapter CAN classify is reported so errors.Is holds
//     against the matching sentinel: fs.ErrNotExist, fs.ErrPermission,
//     ErrNotRegular, fs.ErrClosed for a dead lease. The transport turns
//     exactly these into a request-shaped refusal (-32602) and everything
//     else into a server fault, and a permission denial reported as a
//     server fault tells the person the wrong thing to do about it.
type RemoteReadFS interface {
	// Open opens path for reading and reports the size of the object it
	// actually opened. It takes no context for the same reason
	// RemoteFS.Create takes none: it is one bounded round trip, and on the
	// SFTP lease it is one lane call with the lease's own watchdog under
	// it.
	Open(path string) (RemoteReader, int64, error)
}

// RemoteReader is an open read handle on the source host. Read is one
// round trip per call, so the engine's chunk bound is what keeps any single
// call inside the lease's lane timeout (D2, in the mirror direction).
//
// OWNERSHIP passes to whoever received it: Source.Open hands it out inside
// a Download, and every path after that closes it exactly once.
type RemoteReader interface {
	Read(p []byte) (int, error)
	Close() error
}

// Download is one file's bytes, PINNED — an open handle on the source host
// plus the little that anybody else needs to be told about it.
//
// The handle is unexported, so a Download is only ever the thing this
// package opened. That is the same structural guarantee Binding.provider
// has: the transport can hold one, hand it back to Get and close it, and
// cannot fabricate one naming bytes nobody opened.
type Download struct {
	// Path is the absolute path, in the source host's syntax, that was
	// opened. It is what a log or a failure names.
	Path string
	// Name is the base name — what the file is called when it lands.
	Name string
	// Size is the length of the OPEN object, in bytes, and it is
	// authoritative: it is what the fetch declares as its Content-Length
	// and what Get refuses to disagree with in either direction.
	Size int64

	r RemoteReader
}

// Close lets go of the pinned handle. It is idempotent, so a path that
// closes defensively and a deferred close cannot double-close the lease's
// handle — idempotent for one owner, which is what Source.Open's contract
// gives it, and not a substitute for that ownership: two goroutines racing
// Close on one Download is a defect in the caller, not a case this closes.
func (d *Download) Close() error {
	if d == nil || d.r == nil {
		return nil
	}
	r := d.r
	d.r = nil
	return r.Close()
}

// Source reads files off one host. It is the read counterpart of Sink, and
// the pair is what a provider contributes: a binding whose provider has no
// Source holds nil and refuses, which is R1 in this direction — reading a
// file off the wrong host is as wrong as writing to it.
type Source interface {
	// Open pins the bytes a download will send and measures them. The
	// returned Download owns an open handle on the source host: the CALLER
	// closes it, on every path, including the ones that never reach Get.
	//
	// It is a separate call from Get, and the split is what makes the
	// length honest. The size has to be known before a byte is sent —
	// there is no temp file to size afterwards and no way to revise a
	// Content-Length already on the wire — and a size taken from a
	// separate stat could describe a different object by the time the
	// bytes move. Open answers both questions of the same open handle.
	Open(path string) (*Download, error)

	// Get streams d into w, calling progress with the running byte total
	// after each chunk. It returns the number of bytes delivered, which is
	// the only number that says how much of the file the far end actually
	// got.
	//
	// It does NOT close d — Open's caller owns that, on every path — and
	// it does not touch the source host except to read.
	//
	// Cancellation, and what the CALLER owes, which is the mirror of
	// Sink.Put's bargain and owed at both ends here. Get observes ctx
	// between chunks, so cancellation is bounded by one remote read and
	// one write. Neither can be abandoned mid-flight: the remote read is
	// bounded by the lease's own watchdog (or is a local file read, which
	// does not block), and the WRITE is the one the caller must be able to
	// unblock — an http.ResponseWriter blocks for as long as the client
	// refuses to read. Whoever supplies w must trip its deadline when the
	// transfer is cancelled; Get's half of the bargain is that once either
	// end reports a failure it unwinds at once and reports how far it got.
	Get(ctx context.Context, d *Download, w io.Writer, progress func(total int64)) (int64, error)
}

type source struct {
	fs    RemoteReadFS
	chunk int
}

// NewSource returns a Source reading through fs, moving chunk bytes per lane
// call. Pass DefaultChunk unless you have a reason not to; see NewSink for
// why the bound is a parameter rather than an option.
func NewSource(remote RemoteReadFS, chunk int) Source {
	return &source{fs: remote, chunk: chunkOr(chunk)}
}

// Open pins and measures. See Source.Open.
func (s *source) Open(p string) (*Download, error) {
	if p == "" {
		return nil, fmt.Errorf("%w: path is empty", ErrInvalidDownload)
	}
	r, size, err := s.fs.Open(p)
	if err != nil {
		return nil, fmt.Errorf("transfer: open %s: %w", p, err)
	}
	if size < 0 {
		// A negative length is not a file we can frame. Refused rather
		// than clamped: a Content-Length is a promise, and a promise
		// derived from a number nobody understands is worse than no
		// download.
		_ = r.Close()
		return nil, fmt.Errorf("%w: %s reports a negative size %d", ErrInvalidDownload, p, size)
	}
	return &Download{Path: p, Name: path.Base(p), Size: size, r: r}, nil
}

// Get streams the pinned handle into w. See Source.Get.
//
// Invariant, both ends named. From the moment Get writes its first byte
// until it returns, w has received a prefix of the file that was open when
// Open ran, and never a byte of anything else — the handle is pinned, so
// nothing that happens to the NAME in between can redirect it. The interval
// closes when Get returns: either exactly Size bytes were delivered and the
// error is nil, or fewer were and the error says why. There is no third
// outcome and no way back: what has been written cannot be recalled, which
// is why the caller frames the response at Size and lets a short body be
// visible as one.
func (s *source) Get(ctx context.Context, d *Download, w io.Writer, progress func(int64)) (int64, error) {
	if d == nil || d.r == nil {
		return 0, fmt.Errorf("%w: no open source", ErrInvalidDownload)
	}
	if progress == nil {
		progress = func(int64) {}
	}
	var sent int64
	err := copyChunks(ctx, w, d.r, d.Size, s.chunk, func(total int64) {
		sent = total
		progress(total)
	}, copyLabels{read: "read remote", write: "send"})
	return sent, err
}
