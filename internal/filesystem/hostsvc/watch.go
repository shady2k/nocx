package hostsvc

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	filesystem "github.com/shady2k/nocx/internal/filesystem"
	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
)

// The watch half of the fs service (nocx-oalo1.12.10): a coordinator asks the
// HOST to follow a directory, and the host announces when its digest moves.
//
// # Why the host polls and the coordinator does not
//
// Every open folder used to cost the coordinator a round trip per rung of the
// transport's ladder — one listing over the wire, forever, multiplied by the
// folders a person had expanded. The host is the party standing next to the
// disk: a listing there is a readdir, not a network round trip, and the digest
// it compares is the same `filesystem.ComputeRev` the wire's own `list` answers
// (`FSListResult.Rev`). So the ladder still runs, it just runs on the machine
// that can afford it, and a quiet folder costs the coordinator nothing at all.
//
// # The cadence is one ladder, not two
//
// The wait between polls is `filesystem.WatchPollWait` — the same function the
// transport calls when a binding has no provider-side watch. A second copy
// here is exactly the defect AD-8 names: the two agree everywhere anybody
// looks and disagree on the host that is slow, or old, or simply wrong for one
// of them. The host adds no jitter: spreading simultaneous polls is what a
// shared network is for, and a host polling its own disk collides with nobody.
//
// # What a watch is bounded by
//
// A watch is installed BY a connection and lives no longer than it: the
// coordinator's subscription is a fact about a socket, and `ConnectionClosed`
// stops every watch that socket installed. It is removed from the same map in
// the same breath, so a notification can never be written down a connection
// that has ended — the ordering `sshsvc.ConnectionClosed` uses for the
// observations it sends, for the same reason.

// Clock is the watch's timer seam: the one thing a test needs to control so
// that "the second rung is due two seconds later" is asserted by advancing a
// clock rather than by sleeping for it. RealClock is the default; a fake one
// lives in this package's tests and reaches the shipped service through
// WithWatchClock, which is how the helperfs fixture drives the real host.
type Clock interface {
	// After starts a timer and returns its channel and cleanup function.
	After(d time.Duration) (<-chan time.Time, func())
}

// RealClock is the process's timer: Go's runtime timer, whose channel carries
// the instant it fired.
type RealClock struct{}

func (RealClock) After(d time.Duration) (<-chan time.Time, func()) {
	timer := time.NewTimer(d)
	return timer.C, func() { timer.Stop() }
}

// WithWatchClock gives the service the clock its watches wait on. A test
// advances it; a shipped helper leaves it alone and gets RealClock.
func WithWatchClock(c Clock) Option {
	return func(s *Service) {
		if c != nil {
			s.watchClock = c
		}
	}
}

// hostWatch is one installed subscription.
//
// mu is held ACROSS the notification write, and that is the point rather than
// an oversight: unwatch takes the same mutex, so once it has returned no
// notification of this watch is on the wire or on its way to it. The cost is
// that an unwatch waits for one write to a connection that has stopped
// reading — a connection which the host's own write path fails out of when it
// dies, and which stops every watch the moment it does.
type hostWatch struct {
	id   string
	path string
	// conn is the connection that asked, and the only one a notification for
	// this watch may be written down.
	conn *host.Host
	// rev is the digest this watch is comparing against. Only the poll
	// goroutine touches it after construction.
	rev  string
	stop chan struct{}
	done chan struct{}

	mu    sync.Mutex
	ended bool
}

func (w *hostWatch) end() {
	w.mu.Lock()
	if !w.ended {
		w.ended = true
		close(w.stop)
	}
	w.mu.Unlock()
}

// notify writes one change and answers whether the watch may go on. A failed
// write means the connection is gone: the coordinator that asked will never
// hear this watch, and the host stops instead of polling for nobody.
func (w *hostWatch) notify(rev string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended {
		return false
	}
	err := w.conn.SendNotification(proto.Notification{
		Service: proto.ServiceFS,
		Event:   proto.EventFSChanged,
		Params:  proto.FSChanged{ID: w.id, Rev: rev},
	})
	if err != nil {
		// Marking it ended here would close(stop) under this lock while the
		// loop's own select is watching it — which is fine — but the write
		// already said what the loop needs to know, so the answer carries it.
		return false
	}
	return true
}

// watch installs one subscription (proto.OpWatch).
//
// The listing that takes the baseline is the same listing the first poll would
// do, and doing it here rather than in the goroutine is what makes the answer's
// `rev` true: when this call returns, anything that changes afterwards is
// announced, and anything that changed before it is folded into the baseline
// the caller is told about. That is the inotify semantics the transport's own
// watch baseline documents.
func (s *Service) watch(ctx context.Context, raw json.RawMessage) (any, error) {
	var p proto.FSWatchParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	if p.ID == "" {
		return nil, &badParams{reason: "watch needs an id"}
	}
	// A watch is about the connection that asked for it. Outside a request
	// there is nobody to notify, and installing one anyway would be a poll
	// goroutine writing down a nil connection.
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		return nil, errors.New("hostsvc: a watch needs the connection it was asked on")
	}
	listing, err := s.provider.List(ctx, p.Path, filesystem.Page{Offset: 0, Limit: 1})
	if err != nil {
		return nil, err
	}
	w := &hostWatch{
		id:   p.ID,
		path: p.Path,
		conn: conn,
		rev:  listing.Rev,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	s.watchMu.Lock()
	if previous := s.watches[p.ID]; previous != nil {
		// One id names one watch. A second install under the same id is a
		// caller re-subscribing after a loss it could not distinguish from a
		// loss of the host's own watch, and the honest reading is the new
		// subscription: the old poll would otherwise go on announcing changes
		// for a watch the caller believes it replaced.
		delete(s.watches, p.ID)
		s.watchMu.Unlock()
		previous.end()
		s.watchMu.Lock()
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.watchMu.Unlock()
		return nil, &DrainingRefusal{}
	}
	s.watches[p.ID] = w
	s.watchMu.Unlock()
	go s.watchLoop(w)
	return proto.FSWatchResult{Rev: listing.Rev}, nil
}

// unwatch ends one subscription (proto.OpUnwatch). An id this daemon does not
// hold is answered as success, and the answer is the same empty object: the
// caller asked that no watch of that id remain, and after this call none does.
func (s *Service) unwatch(_ context.Context, raw json.RawMessage) (any, error) {
	var p proto.FSUnwatchParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s.watchMu.Lock()
	w := s.watches[p.ID]
	delete(s.watches, p.ID)
	s.watchMu.Unlock()
	if w != nil {
		w.end()
	}
	return proto.FSUnwatchResult{}, nil
}

// watchLoop is one watch's poll: the shared ladder, a local listing per rung,
// and a notification exactly when the digest moved.
//
// A failed listing stretches the failure ladder and announces nothing. That is
// not a gap in the promise but the promise's edge: a notification NAMES a
// digest (FSChanged.Rev is the value a list answers), and a directory that
// cannot be read has no digest to name. A directory that comes back announces
// the digest it came back with, and the caller compares it against what it
// last listed — the same comparison it makes on every other announcement.
func (s *Service) watchLoop(w *hostWatch) {
	defer close(w.done)
	idleStep, failureStep := 0, 0
	for {
		wait := filesystem.WatchPollWait(filesystem.WatchPollBase, idleStep, failureStep)
		tick, stopTimer := s.watchClock.After(wait)
		select {
		case <-w.stop:
			stopTimer()
			return
		case <-tick:
		}
		stopTimer()
		if s.watchPolled != nil {
			s.watchPolled(w.id)
		}
		listing, err := s.provider.List(context.Background(), w.path, filesystem.Page{Offset: 0, Limit: 1})
		if err != nil {
			failureStep++
			if failureStep > filesystem.WatchPollMaxFailureStep {
				failureStep = filesystem.WatchPollMaxFailureStep
			}
			continue
		}
		failureStep = 0
		if listing.Rev == w.rev {
			idleStep++
			if idleStep > filesystem.WatchPollMaxIdleStep {
				idleStep = filesystem.WatchPollMaxIdleStep
			}
			continue
		}
		w.rev = listing.Rev
		idleStep = 0
		if !w.notify(listing.Rev) {
			return
		}
	}
}

// stopConnectionWatches ends every watch one connection installed, and removes
// them from the table in the same critical section — so a notification written
// after this returns is impossible rather than unlikely.
func (s *Service) stopConnectionWatches(conn *host.Host) {
	if conn == nil {
		return
	}
	s.watchMu.Lock()
	stopped := make([]*hostWatch, 0, len(s.watches))
	for id, w := range s.watches {
		if w.conn == conn {
			delete(s.watches, id)
			stopped = append(stopped, w)
		}
	}
	s.watchMu.Unlock()
	for _, w := range stopped {
		w.end()
	}
}

// stopAllWatches ends every subscription. It is the service's own shutdown:
// the daemon is going, and a poll goroutine that outlived it would write down a
// connection somebody else now owns.
func (s *Service) stopAllWatches() {
	s.watchMu.Lock()
	stopped := make([]*hostWatch, 0, len(s.watches))
	for id, w := range s.watches {
		delete(s.watches, id)
		stopped = append(stopped, w)
	}
	s.watchMu.Unlock()
	for _, w := range stopped {
		w.end()
	}
}
