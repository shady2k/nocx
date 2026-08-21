package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/log"
)

// The master's ownership interval, and losing it (design §6.2, ADR-0035's
// first consequence).
//
// A typed `ssh` leaves a control socket and a master process behind it, and
// they outlive the typed command. They are not a leak ONLY because they have
// a closing event: the interval ends when the last nocx-owned session and
// auxiliary channel have finished, and the socket is removed after the
// master's exit is CONFIRMED, under a bounded cleanup. Without that event a
// socket and a process are a footprint with no end, which is a shape this
// repository has paid for before.
//
// The other half is losing it. Three events, detected separately, because
// they mean three different things to the user:
//
//	the SOCKET FILE going    — the master is alive and we can no longer reach it;
//	the MASTER PROCESS going — there is nothing to reach;
//	the TRANSPORT going      — the connection under everything is gone.
//
// The first two end integration and return the session to a native
// presentation. The third ENDS THE SESSION: there is no prompt left to keep,
// and claiming otherwise would promise an outcome we cannot deliver.
//
// And the same event means different things in different intervals, which is
// why Outcome takes the phase rather than reading one off a field somewhere:
// before ownership is proven nothing has happened at all — the user's line is
// running as plain SSH, nothing was published and nothing was minted — so
// there is nothing to name.

// MasterCleanupBound is how long the closing event may take: 5 s (design
// §6.2). It is spent on CONFIRMING the master's exit, and it is spent against
// an injected clock, never a wall-clock sleep.
const MasterCleanupBound = 5 * time.Second

// masterCleanupPoll is how often the exit is re-checked inside that bound.
const masterCleanupPoll = 100 * time.Millisecond

// Loss reasons the typed path adds to the product's vocabulary. They are
// distinct from ReasonChannelLost, which already means "this session WAS
// integrated and is not any more" — these two say something that one cannot.
const (
	// ReasonMasterLost: the master or its socket went between the ownership
	// proof and integration being live. The user is at a native login shell
	// on the connection they already authenticated.
	ReasonMasterLost RefusalReason = "master-lost"
	// ReasonTransportLost: the SSH transport under the master is gone. This
	// is the one loss that ends the session — there is no prompt to keep.
	ReasonTransportLost RefusalReason = "transport-lost"
)

// OwnershipPhase is where in the interval a loss arrived.
type OwnershipPhase int

const (
	// PhaseUnproven: the handshake has not succeeded. Nothing has been
	// published, nothing minted, no remote state touched.
	PhaseUnproven OwnershipPhase = iota
	// PhaseProven: the mux handshake succeeded against that specific
	// socket. This is the moment §5.3's input quarantine opens.
	PhaseProven
	// PhaseIntegrated: the bootstrap reached its accepted outcome.
	PhaseIntegrated
	// PhaseClosed: the interval is over.
	PhaseClosed
)

func (p OwnershipPhase) String() string {
	switch p {
	case PhaseUnproven:
		return "unproven"
	case PhaseProven:
		return "proven"
	case PhaseIntegrated:
		return "integrated"
	case PhaseClosed:
		return "closed"
	default:
		return fmt.Sprintf("phase(%d)", int(p))
	}
}

// LossEvent is one of §6.2's three, named separately because they are
// detected separately.
type LossEvent string

const (
	LossNone          LossEvent = ""
	LossSocketFile    LossEvent = "socket-file"
	LossMasterProcess LossEvent = "master-process"
	LossTransport     LossEvent = "transport"
)

// LossOutcome is what one loss, in one interval, means for the product.
type LossOutcome struct {
	Event LossEvent
	// Reason is what the session reports. Empty means nothing is named,
	// which is only ever true before ownership was proven — the user's line
	// was plain SSH and nothing of ours had happened.
	Reason RefusalReason
	// EndsSession is true only for the transport: there is no prompt left.
	EndsSession bool
}

// MasterProbes are the three observations the watcher makes. They are
// injected because two of them are process- and filesystem-shaped and the
// third belongs to whoever holds an auxiliary channel.
type MasterProbes struct {
	// SocketPresent reports whether the control socket still exists.
	SocketPresent func(path string) bool
	// ProcessAlive reports whether the master process still exists.
	ProcessAlive func(pid int) bool
	// Terminate asks the master to exit (`ssh -O exit`).
	Terminate func() error
}

// OwnershipClock is the injected time the cleanup bound is spent against.
type OwnershipClock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

// SystemClock is the production OwnershipClock. It is exported and passed in
// rather than defaulted for the same reason the control root is: the cleanup
// bound is spent against whatever clock the caller supplies, and a caller
// that did not choose one has not decided anything.
type SystemClock struct{}

func (SystemClock) Now() time.Time                         { return time.Now() }
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Ownership is one typed session's master: the interval, its losses and its
// closing event.
type Ownership struct {
	log    log.Logger
	socket string
	pid    int
	probes MasterProbes
	clock  OwnershipClock

	mu            sync.Mutex
	phase         OwnershipPhase
	owned         []io.Closer
	transportLoss error
	closeOnce     sync.Once
	closeResult   CleanupResult
}

// NewOwnership returns the interval for a master proven at socket, whose
// process the handshake reported as pid. clock is what MasterCleanupBound is
// spent against; production passes SystemClock and a test passes one it
// drives, so "the five seconds passed" is a statement rather than a wait.
func NewOwnership(lg log.Logger, socket string, pid int, probes MasterProbes, clock OwnershipClock) *Ownership {
	return &Ownership{log: lg, socket: socket, pid: pid, probes: probes, clock: clock}
}

// Phase reports where the interval is.
func (o *Ownership) Phase() OwnershipPhase {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.phase
}

// MarkProven opens the interval. It is called at exactly one event: the
// successful mux handshake against that specific socket.
func (o *Ownership) MarkProven() { o.setPhase(PhaseProven) }

// MarkIntegrated records that the bootstrap reached its accepted outcome.
func (o *Ownership) MarkIntegrated() { o.setPhase(PhaseIntegrated) }

func (o *Ownership) setPhase(p OwnershipPhase) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.phase == PhaseClosed {
		return
	}
	o.phase = p
}

// Own registers something whose end the interval waits for: an auxiliary
// channel, a mux session, an SFTP client. The interval closes when the last
// of them has finished.
func (o *Ownership) Own(c io.Closer) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.owned = append(o.owned, c)
}

// ReportTransportLoss is how the transport's death becomes observable at all.
// The master is the user's own process and the socket is a file; neither says
// anything about the SSH transport underneath. What does is a channel we were
// using on it ending, and only its holder sees that.
func (o *Ownership) ReportTransportLoss(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.transportLoss == nil {
		o.transportLoss = err
	}
}

// Detect makes one observation and reports the loss it found, if any. The
// order is the discrimination: a dead transport is reported by its channel's
// holder and outranks everything, then the process, then the socket — so a
// missing socket is never mistaken for a dead master, which is exactly the
// conflation §6.2 forbids.
func (o *Ownership) Detect() (LossOutcome, bool) {
	o.mu.Lock()
	transport := o.transportLoss
	o.mu.Unlock()

	switch {
	case transport != nil:
		return o.Outcome(LossTransport), true
	case o.probes.ProcessAlive != nil && !o.probes.ProcessAlive(o.pid):
		return o.Outcome(LossMasterProcess), true
	case o.probes.SocketPresent != nil && !o.probes.SocketPresent(o.socket):
		return o.Outcome(LossSocketFile), true
	}
	return LossOutcome{}, false
}

// Outcome is what one event means in the interval as it stands now. It is the
// single place the phase decides, which is why Detect goes through it rather
// than mapping the event itself.
func (o *Ownership) Outcome(ev LossEvent) LossOutcome {
	return o.outcomeFor(o.Phase(), ev)
}

func (o *Ownership) outcomeFor(phase OwnershipPhase, ev LossEvent) LossOutcome {
	// The transport is the same answer in every interval, and it is the one
	// that ends the session.
	if ev == LossTransport {
		return LossOutcome{Event: ev, Reason: ReasonTransportLost, EndsSession: true}
	}
	switch phase {
	case PhaseUnproven:
		// Nothing of ours had happened: the user's line was running as
		// plain SSH. There is no refusal to name because there was no
		// attempt to refuse.
		return LossOutcome{Event: ev}
	case PhaseIntegrated:
		return LossOutcome{Event: ev, Reason: ReasonChannelLost}
	default:
		return LossOutcome{Event: ev, Reason: ReasonMasterLost}
	}
}

// CleanupResult is what the closing event actually achieved. Every field is
// an observation, not an intention: MasterExited is true only when the exit
// was CONFIRMED.
type CleanupResult struct {
	MasterExited  bool
	SocketRemoved bool
	Err           error
}

// Close is the closing event. It ends the owned sessions, asks the master to
// go, confirms it went inside MasterCleanupBound of injected time, and then
// removes the socket.
//
// The socket is removed even when the exit could not be confirmed, and that
// is deliberate: a socket left beside a master we have lost track of is the
// footprint with no end, and a stale socket is the one failure the spike
// measured as harmless — `ControlMaster=auto` removes a dead socket and takes
// over.
func (o *Ownership) Close(ctx context.Context) CleanupResult {
	o.closeOnce.Do(func() { o.closeResult = o.doClose(ctx) })
	return o.closeResult
}

func (o *Ownership) doClose(ctx context.Context) CleanupResult {
	o.mu.Lock()
	owned := o.owned
	o.owned = nil
	o.phase = PhaseClosed
	o.mu.Unlock()

	var errs []error
	// The last owned session ending is what opens the closing event, so
	// they go first and all of them go, whatever any one of them says.
	for i := len(owned) - 1; i >= 0; i-- {
		if err := owned[i].Close(); err != nil {
			errs = append(errs, err)
		}
	}

	res := CleanupResult{}
	if o.probes.Terminate != nil {
		if err := o.probes.Terminate(); err != nil {
			errs = append(errs, fmt.Errorf("asking the master to exit: %w", err))
		}
	}
	res.MasterExited = o.awaitExit(ctx)
	if !res.MasterExited {
		errs = append(errs, fmt.Errorf("the master did not exit inside %s", MasterCleanupBound))
	}

	if err := os.Remove(o.socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("removing the control socket: %w", err))
	}
	res.SocketRemoved = o.probes.SocketPresent == nil || !o.probes.SocketPresent(o.socket)
	if !res.SocketRemoved {
		errs = append(errs, fmt.Errorf("the control socket %s outlived the bounded cleanup", o.socket))
	}
	res.Err = errors.Join(errs...)
	if o.log != nil {
		o.log.Info("typed ssh: the master's ownership interval closed",
			"socket", o.socket, "pid", o.pid,
			"master_exited", res.MasterExited, "socket_removed", res.SocketRemoved,
			"error", res.Err)
	}
	return res
}

// awaitExit confirms the master is gone, inside the bound. The bound is spent
// against the injected clock: a test states that the five seconds passed
// instead of waiting for them.
func (o *Ownership) awaitExit(ctx context.Context) bool {
	if o.probes.ProcessAlive == nil {
		// Nothing to confirm against. Say so by reporting no confirmation
		// rather than assuming one — MasterExited is an observation.
		return false
	}
	deadline := o.clock.Now().Add(MasterCleanupBound)
	for {
		if !o.probes.ProcessAlive(o.pid) {
			return true
		}
		if !o.clock.Now().Before(deadline) {
			return false
		}
		wait := masterCleanupPoll
		if remaining := deadline.Sub(o.clock.Now()); remaining < wait {
			wait = remaining
		}
		select {
		case <-o.clock.After(wait):
		case <-ctx.Done():
			return !o.probes.ProcessAlive(o.pid)
		}
	}
}
