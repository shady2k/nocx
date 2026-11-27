package helperfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/farhelper"
	"github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// The far side's watch (nocx-oalo1.12.10): one directory, one subscription on
// the host, and the events the transport turns into files.changed.
//
// # What a watch here actually is
//
// `filesystem.Watch` is a change HINT — Events carries struct{}, never a diff —
// and this is the hint the HOST can produce: it polls the directory on its own
// disk at the shared ladder and announces the digest when it moves. So the
// transport stops re-listing over the wire on a ladder of its own, and a quiet
// folder costs the connection nothing.
//
// # What it does when the connection goes
//
// A subscription is a fact about one helper client, and the client is replaced
// when the axis leaves serving and comes back. The watch therefore lives longer
// than any one subscription: it keeps its id and its path, re-subscribes on the
// next serving lease, and announces ONCE on re-subscription when the digest the
// new subscription starts from differs from the last one it knew. That single
// synthetic event is what catches a change made during the cut — the helper
// will never announce it, because its baseline is already after it.
//
// # Why the call timeouts
//
// Subscribe and release are one request each on a socket that may have stopped
// answering. They are bounded the way the upload's mutations are, because a
// registry that has not yet noticed a dead transport is exactly the state these
// calls run in, and neither Close nor a visibility edge may wait on the read of
// a socket nobody is writing.

// watchCallTimeout bounds one fs.watch or fs.unwatch request. It is the same
// budget uploadMutationTimeout states and for the same reason: the transport's
// loss is discovered asynchronously, so a call made in that window would
// otherwise wait for the peer's answer for as long as the peer takes.
const watchCallTimeout = 20 * time.Second

// watchRetryClock starts a retry timer whose cleanup releases any pending wait.
type watchRetryClock interface {
	After(time.Duration) (<-chan time.Time, func())
}

type watchRetryTimer struct{}

func (watchRetryTimer) After(delay time.Duration) (<-chan time.Time, func()) {
	timer := time.NewTimer(delay)
	return timer.C, func() { timer.Stop() }
}

// watch is one directory subscription that survives the clients it runs on.
type watch struct {
	p      *Provider
	path   string
	id     string
	mode   filesystem.WatchMode
	events chan struct{}
	done   chan struct{}

	// signal wakes the supervisor: the axis published a status, or something
	// asked it to re-check. Buffered and written non-blocking, because the
	// registry's callback runs on a goroutine that must not wait.
	signal chan struct{}

	// stopRegistry ends the subscription to the axis's status stream.
	stopRegistry func()

	// runCtx is the supervisor's own lifetime: it ends with Close, so a
	// re-subscription waiting on a decision that never finishes is released
	// rather than left parked for a helper that may never answer.
	runCtx    context.Context
	runCancel context.CancelFunc

	// mu guards the subscription state the supervisor mutates and Close reads.
	mu       sync.Mutex
	closed   bool
	lease    *farhelper.Lease
	unnotify func()
	lastRev  string

	closeOnce sync.Once
}

var _ filesystem.Watch = (*watch)(nil)

func newWatchID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

// Watch installs a subscription on the host for one directory.
//
// It creates the subscription HERE rather than lazily in a goroutine, because
// the transport's decision depends on its outcome: a provider that cannot watch
// makes the transport poll, and a provider that says it can must already be
// able to. A refused subscription is returned as the refusal the transport
// knows how to classify (*ErrUnavailable for a helper that is not serving).
func (p *Provider) Watch(ctx context.Context, path string) (filesystem.Watch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, err := newWatchID()
	if err != nil {
		return nil, err
	}
	lease, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	w := &watch{
		p:         p,
		path:      path,
		id:        id,
		runCtx:    runCtx,
		runCancel: runCancel,
		// The host polls its own disk; the transport therefore has a change
		// signal that costs the wire nothing, which is a designed mechanism
		// rather than a degraded one — no DegradedReason, no warning badge.
		mode:   filesystem.WatchMode{Kind: filesystem.WatchPolling},
		events: make(chan struct{}, 1),
		done:   make(chan struct{}),
		signal: make(chan struct{}, 1),
	}
	if err := w.subscribe(ctx, lease); err != nil {
		runCancel()
		return nil, err
	}
	w.stopRegistry = p.reg.Watch(func(connectionID string, status farhelper.Status) {
		if connectionID != p.connectionID || status.State != farhelper.StateServing {
			return
		}
		w.poke()
	})
	go w.supervise()
	return w, nil
}

// subscribe installs the host-side watch on one lease and registers the
// delivery for its id. The registration happens BEFORE the request so a
// `changed` that overtakes the answer is delivered rather than matched to
// nothing; a failed request unregisters it again.
func (w *watch) subscribe(ctx context.Context, lease *farhelper.Lease) error {
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), watchCallTimeout)
	defer cancel()
	notified := false // guarded by w.mu
	unnotify := lease.Client().OnFSChanged(w.id, func(changed proto.FSChanged) {
		w.mu.Lock()
		defer w.mu.Unlock()
		if lease.Ctx.Err() != nil {
			return
		}
		notified = true
		w.observeRevLocked(changed.Rev, true)
	})
	params := proto.FSWatchParams{ID: w.id, Path: w.path}
	var out proto.FSWatchResult
	err := lease.Client().Call(callCtx, proto.ServiceFS, proto.OpWatch, params, &out)
	if err != nil {
		unnotify()
		if lease.Ctx.Err() != nil || errors.Is(err, client.ErrLost) {
			return w.p.unavailable()
		}
		return classifyRefusal(w.path, params, err)
	}
	w.mu.Lock()
	w.lease = lease
	w.unnotify = unnotify
	// An early notification already carries a newer revision than the reply.
	// On reconnect, a different starting revision also earns a change hint.
	if !notified {
		w.observeRevLocked(out.Rev, w.lastRev != "")
	}
	w.mu.Unlock()
	return nil
}

// observeRevLocked stores the latest revision and coalesces pending hints.
func (w *watch) observeRevLocked(rev string, announce bool) {
	if w.closed || rev == w.lastRev {
		return
	}
	w.lastRev = rev
	if !announce {
		return
	}
	select {
	case w.events <- struct{}{}:
	default:
	}
}

// poke asks the supervisor to look at the axis now.
func (w *watch) poke() {
	select {
	case w.signal <- struct{}{}:
	default:
	}
}

// supervise keeps the subscription alive for as long as the watch exists: on
// every serving lease it re-subscribes, and it announces once when the digest
// it starts from is not the digest it last knew.
func (w *watch) supervise() {
	defer close(w.done)
	for w.runCtx.Err() == nil {
		w.mu.Lock()
		lease := w.lease
		w.mu.Unlock()
		if lease != nil {
			select {
			case <-w.runCtx.Done():
				return
			case <-lease.Ctx.Done():
			}
			w.forget()
		}
		lease, err := w.p.acquire(w.runCtx)
		if err != nil {
			select {
			case <-w.runCtx.Done():
				return
			case <-w.signal:
				continue
			}
		}
		w.retrySubscription(lease)
	}
}

// retrySubscription backs off while the serving lease stays usable. A new
// lease cancels the old one and the supervisor resumes from the registry.
// Same-lease status notices leave the retry timer in place.
func (w *watch) retrySubscription(lease *farhelper.Lease) {
	for step := 1; w.runCtx.Err() == nil && lease.Ctx.Err() == nil; step = min(step+1, filesystem.WatchPollMaxFailureStep) {
		if err := w.subscribe(w.runCtx, lease); err == nil {
			return
		}
		if w.runCtx.Err() != nil || lease.Ctx.Err() != nil {
			return
		}
		delay := filesystem.WatchPollWait(filesystem.WatchPollBase, 0, step)
		w.waitForRetry(lease, delay)
	}
}

func (w *watch) waitForRetry(lease *farhelper.Lease, delay time.Duration) {
	tick, stop := w.p.watchRetryClock.After(delay)
	defer stop()
	for lease.Ctx.Err() == nil {
		select {
		case <-w.runCtx.Done():
			return
		case <-lease.Ctx.Done():
			return
		case <-tick:
			return
		case <-w.signal:
			// Recheck the lease. A serving notice for the same lease
			// leaves the timer running.
		}
	}
}

// forget drops the state of a subscription that ended with its transport.
func (w *watch) forget() {
	w.mu.Lock()
	unnotify := w.unnotify
	w.unnotify = nil
	w.lease = nil
	w.mu.Unlock()
	if unnotify != nil {
		unnotify()
	}
}

// Events is the transport's change hint.
func (w *watch) Events() <-chan struct{} { return w.events }

// Mode is designed host-side polling: the host re-lists its own disk, and no
// network ladder runs. There is no degraded reason, because nothing degraded.
func (w *watch) Mode() filesystem.WatchMode { return w.mode }

// Close ends the subscription and the supervisor. It releases the host's watch
// when the lease it lives on is still alive; a lease that has gone needs no
// release, because the host ended every watch of that connection itself
// (hostsvc.ConnectionClosed).
//
// The release is SYNCHRONOUS and bounded (watchCallTimeout) rather than fired
// into a goroutine, and that is what makes the promise exact: when Close
// returns, no notification of this id can reach this process, and the host has
// been told to stop polling. A binding that drops a collapsed folder pays one
// round trip, which is the price of the host not polling a directory nobody is
// looking at.
func (w *watch) Close() error {
	var err error
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.mu.Unlock()
		w.runCancel()
		if w.stopRegistry != nil {
			w.stopRegistry()
		}
		// The supervisor may be waiting for an in-flight watch reply. Join it
		// before selecting the lease to release.
		<-w.done
		w.mu.Lock()
		lease := w.lease
		unnotify := w.unnotify
		w.unnotify = nil
		w.lease = nil
		w.mu.Unlock()
		// Delivery ends here in every case: a notification that a dead host
		// somehow still sends must not reach a closed watch.
		if unnotify != nil {
			unnotify()
		}
		if lease == nil || lease.Ctx.Err() != nil {
			return
		}
		err = w.release(lease)
	})
	return err
}

// release tells the host to stop one subscription. A refusal is not an error:
// a host that does not hold this id is a host where nothing is polling for it,
// which is what the caller asked for.
func (w *watch) release(lease *farhelper.Lease) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(lease.Ctx), watchCallTimeout)
	defer cancel()
	err := lease.Client().Call(ctx, proto.ServiceFS, proto.OpUnwatch, proto.FSUnwatchParams{ID: w.id}, &proto.FSUnwatchResult{})
	if err == nil || lease.Ctx.Err() != nil || errors.Is(err, client.ErrLost) {
		return nil
	}
	var refusal *client.RefusalError
	if errors.As(err, &refusal) {
		return nil
	}
	return err
}
