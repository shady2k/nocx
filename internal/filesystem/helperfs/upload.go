package helperfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"path"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/transfer"
)

var _ filesystem.Uploader = (*Provider)(nil)

// Lost commits can wait for the registry's normal redial, but not forever.
const uploadMutationTimeout = 20 * time.Second

func (p *Provider) Sink() transfer.Sink {
	return transfer.NewAtomicSink(uploadFS{p}, transfer.DefaultChunk)
}

type uploadFS struct{ p *Provider }

func (f uploadFS) OpenWrite(ctx context.Context, name string, keepBoth bool) (transfer.AtomicWrite, error) {
	lease, err := f.p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		return nil, err
	}
	w := &uploadWrite{p: f.p, ctx: ctx, id: hex.EncodeToString(id[:]), opening: lease, digest: sha256.New()}
	target := name
	if keepBoth {
		// Only the directory is selected at open. The caller's target (including
		// a directory or symlink with that name) must not redirect a KeepBoth.
		target = path.Join(path.Dir(name), ".nocx-keep-both-"+w.id)
	}
	params := proto.FSWriteOpenParams{ID: w.id, Path: target}
	var out proto.FSWriteOpenResult
	// Do not race a successful open with caller cancellation: until Open has
	// heard the result the transfer has no handle it can clean up.
	openCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadMutationTimeout)
	defer cancel()
	if lease.Ctx.Err() != nil {
		return nil, f.p.unavailable()
	}
	callErr := lease.Client().Call(openCtx, proto.ServiceFS, proto.OpWriteOpen, params, &out)
	err = callErr
	switch {
	case lease.Ctx.Err() != nil || errors.Is(err, client.ErrLost):
		err = f.p.unavailable()
	case err != nil:
		err = classifyRefusal(name, params, err)
	default:
		err = ctx.Err()
	}
	if err != nil {
		// A served refusal never returns an open session. If the result was lost,
		// or if the caller cancelled after the open ran, reclaim the caller's id.
		var refused *client.RefusalError
		if !errors.As(callErr, &refused) {
			if abortErr := w.Abort(ctx); abortErr != nil {
				err = errors.Join(err, abortErr)
			}
		}
		return nil, err
	}
	w.resolved = out.Resolved
	return w, nil
}

type uploadWrite struct {
	p        *Provider
	ctx      context.Context
	id       string
	resolved string
	opening  *farhelper.Lease
	digest   hash.Hash
	offset   int64
}

func (w *uploadWrite) Write(data []byte) (int, error) {
	lease, err := w.p.openingLease(w.ctx, w.opening.Client())
	if err != nil {
		return 0, err
	}
	params := proto.FSWriteParams{ID: w.id, Offset: w.offset, Data: data}
	var out proto.FSWriteResult
	if err := w.p.callLease(w.ctx, lease, proto.OpWrite, w.resolved, params, &out); err != nil {
		return 0, err
	}
	if size := w.offset + int64(len(data)); out.Size != size {
		return 0, &transfer.SizeMismatchError{Declared: size, Got: out.Size}
	}
	w.offset = out.Size
	_, _ = w.digest.Write(data)
	return len(data), nil
}

func (w *uploadWrite) Commit(ctx context.Context, name string, size int64, expectAbsent bool) error {
	target := w.resolved
	params := proto.FSWriteCommitParams{ID: w.id, Path: target, Size: size}
	if expectAbsent {
		// The candidate is lexical within the directory selected at open. Do not
		// look the original pane or the original directory alias up again.
		params.Path = path.Join(path.Dir(w.resolved), path.Base(name))
		params.Expect = &proto.FSWriteExpect{Absent: &expectAbsent}
	}
	// A cancelled caller must not interrupt the commit and make its outcome
	// appear determinate. The same budget covers a lost result and its replay.
	mutationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadMutationTimeout)
	defer cancel()
	lease, err := w.p.openingLease(mutationCtx, w.opening.Client())
	if err != nil {
		// Nothing attempted the commit yet, so a peer that did not open the
		// session cannot change what this upload has to answer.
		return err
	}
	var result proto.FSWriteCommitResult
	err = lease.Client().Call(mutationCtx, proto.ServiceFS, proto.OpWriteCommit, params, &result)
	if errors.Is(err, client.ErrLost) {
		lease, err = w.p.replacementLease(mutationCtx, lease.Client())
		if err != nil {
			return &filesystem.ErrUnknownWriteOutcome{Path: name, Err: err}
		}
		// Replay the outcome query with the same id, not the upload. The old
		// helper either remembers the first answer, or the new one reports the
		// current target so the digest can establish whether our bytes landed.
		result = proto.FSWriteCommitResult{}
		err = lease.Client().Call(mutationCtx, proto.ServiceFS, proto.OpWriteCommit, params, &result)
	}
	if errors.Is(err, client.ErrLost) || errors.Is(err, context.DeadlineExceeded) {
		return &filesystem.ErrUnknownWriteOutcome{Path: name, Err: err}
	}
	if err != nil {
		return classifyRefusal(name, params, err)
	}
	switch result.Outcome {
	case proto.FSOutcomeCommitted:
		return nil
	case proto.FSOutcomeAborted:
		return &filesystem.ErrWriteAborted{Path: name}
	case proto.FSOutcomeUnknown:
		if result.Current != nil && result.Current.Size == size && result.Current.SHA256 == hex.EncodeToString(w.digest.Sum(nil)) {
			return nil
		}
		return &filesystem.ErrUnknownWriteOutcome{Path: name}
	default:
		return &filesystem.ErrUnknownWriteOutcome{Path: name, Err: fmt.Errorf("helper: unrecognized write outcome %q", result.Outcome)}
	}
}

// recoverable reports whether the axis can still hand out a peer that answers
// for the same write session: a paused transport that reconnects, a decision in
// progress, or a helper the registry will redial. Every other unavailable axis
// is a final answer, and waiting for it would only spend the mutation budget.
func recoverable(status farhelper.Status) bool {
	switch status.State {
	case farhelper.StateServing, farhelper.StateStarting, farhelper.StatePaused:
		return true
	case farhelper.StateUnavailable:
		return status.Reason == farhelper.ReasonHelperLost
	}
	return false
}

func (p *Provider) replacementLease(ctx context.Context, old *client.Client) (*farhelper.Lease, error) {
	changed := make(chan struct{}, 1)
	stop := p.reg.Watch(func(id string, _ farhelper.Status) {
		if id == p.connectionID {
			select {
			case changed <- struct{}{}:
			default:
			}
		}
	})
	defer stop()
	for {
		lease, err := p.acquire(ctx)
		if err == nil {
			if lease.Client() != old {
				return lease, nil
			}
		} else {
			var unavailable *filesystem.ErrUnavailable
			if !errors.As(err, &unavailable) {
				// The caller's own context ended: no axis answer improves it.
				return nil, err
			}
			status, known := p.reg.Axis(p.connectionID)
			if !known || !recoverable(status) {
				return nil, err
			}
			if status.State == farhelper.StateServing {
				// The axis started serving between the refusal and this reading,
				// so ask again now instead of waiting for a change already made.
				continue
			}
		}
		// Watch was registered before acquiring, so neither retirement of the
		// old peer nor publication of a replacement can be missed.
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (w *uploadWrite) Abort(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), uploadMutationTimeout)
	defer cancel()
	// A pane rebind prevents new writes but must not abandon the temporary
	// on an originating peer that is still alive. Abort only names that id.
	lease := w.opening
	if lease.Ctx.Err() != nil {
		// Retiring the originating connection discards its open write sessions.
		// The registry owns the retirement, not this provider.
		return nil
	}
	params := proto.FSWriteAbortParams{ID: w.id}
	var result proto.FSWriteAbortResult
	err := lease.Client().Call(ctx, proto.ServiceFS, proto.OpWriteAbort, params, &result)
	if errors.Is(err, client.ErrLost) {
		return nil
	} // the dead connection owns cleanup
	return classifyRefusal(w.resolved, params, err)
}
