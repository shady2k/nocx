package helperfs

import (
	"context"
	"errors"
	"fmt"

	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/transfer"
)

var (
	_ filesystem.VersionedProvider = (*Provider)(nil)
	_ filesystem.VersionedFiles    = (*Provider)(nil)
)

// Versioned is the conditional-write seam (C27): the host checks the caller's
// expectation at the rename, so the window between reading a file and
// replacing it is closed on the machine that owns the file rather than in the
// coordinator.
//
// It is never nil. A helper-backed provider can always reach the `fs` service,
// and whether that reach succeeds at a given moment is the axis's answer, not a
// capability's — which is why an unavailable connection gets a live seam whose
// calls refuse with *ErrUnavailable, and not a missing one.
func (p *Provider) Versioned() filesystem.VersionedFiles {
	return p
}

// ReadVersion reads the whole file and answers the version of exactly those
// bytes. A file bigger than maxBytes is refused rather than cut: a version of a
// prefix is not the file's version, and a caller holding one would fence its
// commit on bytes it never saw.
func (p *Provider) ReadVersion(ctx context.Context, name string, maxBytes int64) ([]byte, filesystem.Version, error) {
	out, err := p.readWhole(ctx, name, filesystem.ReadLimit(maxBytes))
	if err != nil {
		return nil, filesystem.Version{}, err
	}
	if out.Version == nil {
		// The host answers a version with every whole-file read; a missing one
		// is a peer this code cannot read, and inventing a version here would
		// hand the caller a fence that matches nothing.
		return nil, filesystem.Version{}, fmt.Errorf("helper: whole-file read of %s answered no version", name)
	}
	return out.Data, versionOf(*out.Version), nil
}

// Commit stages the bytes in the target's directory and installs them only if
// expect still holds at the rename. It reuses the upload session rather than
// re-implementing one: the same write-open, the same chunk framing, the same
// mutation budget and the same lost-reply replay, because a second copy of that
// protocol would agree with the first everywhere anyone looked and disagree at
// the one point that matters.
//
// The session is aborted on any failure. A caller of this seam holds no
// session handle, so a temporary left behind would be one nobody could ever
// finish; and an abort of a session that had already installed its bytes does
// not undo them, so aborting after a lost reply is safe as well as necessary.
func (p *Provider) Commit(ctx context.Context, name string, data []byte, expect filesystem.Expect) (filesystem.Version, error) {
	// `absent` asks whether the caller's NAME is free, so the session opens a
	// name in the enclosing directory instead of resolving the path the way a
	// plain write does. Following a final symlink would select the target's
	// directory, where a free name beside the target would pass the check and
	// land bytes the caller asked never to be written.
	w, err := uploadFS{p}.start(ctx, name, expect.Absent)
	if err != nil {
		return filesystem.Version{}, err
	}
	size := int64(len(data))
	err = w.writeAll(data)
	if err != nil {
		return filesystem.Version{}, w.abortAfter(ctx, err)
	}
	result, err := w.commit(ctx, name, size, expectOf(expect))
	if err != nil {
		return filesystem.Version{}, w.abortAfter(ctx, err)
	}
	version, err := w.installed(name, size, result)
	if err != nil {
		return filesystem.Version{}, w.abortAfter(ctx, err)
	}
	return version, nil
}

// writeAll streams the bytes in the same chunk size the upload sink uses. One
// chunk size for the whole product, because the bound belongs to the wire and
// not to which caller is sending.
func (w *uploadWrite) writeAll(data []byte) error {
	for offset := 0; offset < len(data); offset += transfer.DefaultChunk {
		if _, err := w.Write(data[offset:min(offset+transfer.DefaultChunk, len(data))]); err != nil {
			return err
		}
	}
	return nil
}

// abortAfter discards the session's temporary and keeps the original refusal
// as the error the caller sees; a failed abort is joined rather than
// substituted, because both facts matter to whoever reads the log.
func (w *uploadWrite) abortAfter(ctx context.Context, err error) error {
	if abortErr := w.Abort(ctx); abortErr != nil {
		return errors.Join(err, abortErr)
	}
	return err
}

// expectOf translates the neutral condition into the wire's. The two members
// are alternatives there as they are here, and the host is the one that refuses
// a request carrying both.
func expectOf(expect filesystem.Expect) *proto.FSWriteExpect {
	if expect.Version == nil && !expect.Absent {
		return nil
	}
	wire := &proto.FSWriteExpect{}
	if expect.Absent {
		absent := true
		wire.Absent = &absent
	}
	if expect.Version != nil {
		wire.Version = &proto.FSVersion{Size: expect.Version.Size, MtimeNs: expect.Version.MtimeNs, SHA256: expect.Version.SHA256}
	}
	return wire
}
