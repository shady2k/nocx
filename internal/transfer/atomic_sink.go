package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
)

// AtomicFS opens a staged write. keepBoth opens in the requested path's
// parent directory without following its final component, because the new
// file will use a sibling name. If OpenWrite fails, it owns cleanup of any
// session it may have started.
type AtomicFS interface {
	OpenWrite(ctx context.Context, path string, keepBoth bool) (AtomicWrite, error)
}

// AtomicWrite is a session owned by the caller from OpenWrite until Commit
// installs it or Abort discards it. The adapter owns the session identity,
// per-call bounds, and determining the outcome after an interrupted reply.
type AtomicWrite interface {
	io.Writer
	// Commit installs the staged bytes at path without an intermediate
	// visible file. If expectAbsent is true, an occupied name returns
	// fs.ErrExist and leaves the staged bytes available for another Commit.
	// Only a confirmed collision may be reported as fs.ErrExist.
	Commit(ctx context.Context, path string, size int64, expectAbsent bool) error
	// Abort discards a staged write. It must not undo a completed Commit,
	// even if the caller could not confirm that commit's outcome.
	Abort(ctx context.Context) error
}

type atomicSink struct {
	fs    AtomicFS
	chunk int
}

// NewAtomicSink returns a Sink that stages bytes once and installs them
// through the filesystem's atomic commit. A nonpositive chunk uses DefaultChunk.
func NewAtomicSink(remote AtomicFS, chunk int) Sink {
	return &atomicSink{fs: remote, chunk: chunkOr(chunk)}
}

func (s *atomicSink) Put(ctx context.Context, u Upload, r io.Reader, progress func(int64)) (Outcome, error) {
	if err := u.validate(); err != nil {
		return Outcome{}, err
	}
	if u.OnExists == Skip {
		return Outcome{State: StateSkipped}, nil
	}
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if progress == nil {
		progress = func(int64) {}
	}

	target := path.Join(u.DestDir, u.Name)
	w, err := s.fs.OpenWrite(ctx, target, u.OnExists == KeepBoth)
	if err != nil {
		return Outcome{}, fmt.Errorf("transfer: open write %s: %w", target, err)
	}
	err = copyChunks(ctx, w, fullChunkReader{ctx, r}, u.Size, s.chunk, progress, copyLabels{read: "read source", write: "write"})
	if err == nil {
		var name string
		name, err = commitAtomicWrite(ctx, w, u)
		if err == nil {
			return Outcome{State: StateWritten, FinalName: name}, nil
		}
	}
	if abortErr := w.Abort(context.WithoutCancel(ctx)); abortErr != nil {
		err = errors.Join(err, fmt.Errorf("transfer: abort write %s: %w", target, abortErr))
	}
	return Outcome{}, err
}

func commitAtomicWrite(ctx context.Context, w AtomicWrite, u Upload) (string, error) {
	keepBoth := u.OnExists == KeepBoth
	attempts := 1
	if keepBoth {
		attempts = keepBothAttempts
	}
	// Observe cancellation before each attempt, but never interrupt a
	// commit. The adapter still bounds each mutation by its lease.
	mutation := context.WithoutCancel(ctx)
	var collision error
	for i := 1; i <= attempts; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := u.Name
		if keepBoth {
			name = suffixName(name, i)
		}
		target := path.Join(u.DestDir, name)
		err := w.Commit(mutation, target, u.Size, keepBoth)
		if err == nil {
			return name, nil
		}
		if !keepBoth || !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("transfer: commit %s: %w", target, err)
		}
		collision = err
	}
	return "", &NameExhaustedError{Name: u.Name, Attempts: attempts, Err: collision}
}

// fullChunkReader absorbs short source reads so each write RPC carries a
// full chunk, except the last one. Unlike io.ReadFull, it preserves a
// non-EOF error returned with the final bytes of a full chunk.
type fullChunkReader struct {
	ctx context.Context
	r   io.Reader
}

func (r fullChunkReader) Read(p []byte) (int, error) {
	var total int
	for total < len(p) {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		n, err := r.r.Read(p[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
