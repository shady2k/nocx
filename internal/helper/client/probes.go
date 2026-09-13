package client

// The coordinator's end of a PROBE LEASE and the named probes that run on it.
//
// # What a lease is here
//
// A reference, held by the helper, on one pooled ssh connection: `ssh.lease`
// takes it and answers its id, every probe op runs a fixed command on the
// connection it keeps alive, and `unlease` drops it. A caller that holds one
// therefore holds a destination rather than a connection — the connection is
// still the helper's, comes and goes with the pool, and closes when the last
// holder lets go (AD-4).
//
// # Why the lease carries the loss signal
//
// The consumer that holds a lease across many probes — port discovery's
// detector — watches for the transport dying, because a sampler that keeps
// asking a dead connection records a dead host where a tab merely died. Two
// things can die here and both are reported:
//
//   - the COORDINATOR's own connection to the helper (Client.Done), which
//     takes every lease with it and is reported at the moment it happens;
//   - the helper's POOLED connection to the far host, which nobody can watch
//     from here and which the helper reports as `exec_lost` on the next probe
//     that uses it. Loss is therefore DISCOVERED BY USE on that path, and the
//     lease closes Done at the moment it learns — which is the honest
//     difference from the coordinator's own former lease, whose watcher saw
//     the far transport die while it was idle. The consumer is not worse off
//     for it: a fresh acquire re-dials, because the helper's pool evicts a
//     corpse rather than handing it out.
//
// # Why the results are remoteprobe's
//
// One place converts the wire's refusal codes into the failure vocabulary the
// callers switch on (and one place is what AD-8 asks for): a second conversion
// in each feature package would be a second opinion about which code means
// "the host allows one session" and which means "the host refuses exec".

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/remoteprobe"
)

// leaseReleaseTimeout bounds the ssh.unlease REQUEST.
//
// It exists for the reason channelCloseTimeout does: Close has a promise to
// keep that the request does not — the caller must be released even when the
// helper has gone quiet — and a Close usually runs in a deferred cleanup, where
// a request that waits for ever is a shutdown that never finishes. The bound is
// a courtesy's bound, not a retry budget: the local release already happened.
const leaseReleaseTimeout = 2 * time.Second

// ErrProbeLeaseClosed is returned by a probe on a lease this process has
// closed. It is not ErrLost: the connection may be perfectly healthy, and the
// distinction is what lets a consumer tell "I gave up on this" from "this
// died".
var ErrProbeLeaseClosed = errors.New("helper: the probe lease is closed")

// ProbeLease is one reference this coordinator holds on a pooled connection,
// with the named probes that run on it.
type ProbeLease struct {
	client      *Client
	id          proto.LeaseID
	fingerprint string

	mu     sync.Mutex
	lost   error
	closed bool

	done     chan struct{}
	doneOnce sync.Once
}

// AcquireProbeLease asks the helper for one reference to a destination and
// returns it.
//
// The params are the coordinator's own resolution carried across: the
// destination is resolved HERE (an alias through ~/.ssh/config, the
// credential's authorization against the endpoint it names), because the helper
// reads no config and holds no binding, and it decides nothing about what the
// address means.
func (c *Client) AcquireProbeLease(ctx context.Context, params proto.LeaseParams) (*ProbeLease, error) {
	var result proto.LeaseResult
	if err := c.Call(ctx, proto.ServiceSSH, proto.OpLease, params, &result); err != nil {
		return nil, err
	}
	if result.Lease.IsZero() {
		return nil, errors.New("helper: lease: the helper answered no lease id")
	}
	l := &ProbeLease{
		client:      c,
		id:          result.Lease,
		fingerprint: result.HostKeyFingerprint,
		done:        make(chan struct{}),
	}
	c.mu.Lock()
	if c.probeLeases == nil {
		c.probeLeases = make(map[proto.LeaseID]*ProbeLease)
	}
	if _, exists := c.probeLeases[result.Lease]; exists {
		c.mu.Unlock()
		// The helper minted an id this client already holds. Releasing it is
		// the only honest answer: it is a reference nobody can address, and
		// leaving it would be a pooled connection held for a caller that has
		// forgotten it.
		_ = c.releaseLease(context.Background(), result.Lease)
		return nil, fmt.Errorf("helper: the helper reused lease id %s", result.Lease)
	}
	c.probeLeases[result.Lease] = l
	c.mu.Unlock()
	return l, nil
}

// ID is the wire identity of this lease, for a log line or a test.
func (l *ProbeLease) ID() proto.LeaseID { return l.id }

// Fingerprint is the TARGET host's host-key fingerprint as observed at dial
// time — the fact the install path keys a consent decision by (ADR-0023).
func (l *ProbeLease) Fingerprint() string { return l.fingerprint }

// Done closes when the connection under this lease is gone: the coordinator's
// connection to the helper died, or a probe came back telling us the far
// transport had. It does NOT close on an explicit Close — a deliberate release
// is not a fact about the host, and a consumer that watches Done to mark a host
// unreachable must not see our own shutdown as one.
func (l *ProbeLease) Done() <-chan struct{} { return l.done }

// LostErr reports why the transport under this lease ended. Meaningful once
// Done has closed; nil while the lease is live and nil for an explicit Close.
func (l *ProbeLease) LostErr() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lost
}

// Close releases this lease: the helper drops its pooled reference and this
// process forgets the id. It does not close Done (see its doc), and a second
// Close is harmless — the client's release forgets an id it no longer holds.
func (l *ProbeLease) Close() error {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	return l.client.releaseLease(context.Background(), l.id)
}

// releaseLease tells the helper to drop a reference and forgets it here.
func (c *Client) releaseLease(ctx context.Context, id proto.LeaseID) error {
	c.mu.Lock()
	delete(c.probeLeases, id)
	c.mu.Unlock()
	reqCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseReleaseTimeout)
	defer cancel()
	return c.Call(reqCtx, proto.ServiceSSH, proto.OpUnlease, proto.UnleaseParams{Lease: id}, nil)
}

// finishLoss records a loss and releases every watcher. Called with the helper
// connection's loss as its cause, and by a probe that discovered the far
// transport dead.
func (l *ProbeLease) finishLoss(cause error) {
	l.mu.Lock()
	if l.lost == nil {
		l.lost = cause
	}
	l.mu.Unlock()
	l.doneOnce.Do(func() { close(l.done) })
}

// live reports whether a probe may run, in the order the two facts must be read:
// an explicit close outranks a loss, because a release of the last reference
// closes the transport as a CONSEQUENCE and reporting that as a fact about the
// host would describe our own action (the ordering internal/ssh's lease
// documents, one process out).
func (l *ProbeLease) live() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrProbeLeaseClosed
	}
	if l.lost != nil {
		return l.lost
	}
	return nil
}

// Uname runs the platform probe (D20) on this lease.
func (l *ProbeLease) Uname(ctx context.Context) (*remoteprobe.Result, error) {
	return l.run(ctx, proto.OpUname, proto.UnameParams{ProbeRunParams: proto.ProbeRunParams{Lease: l.id}})
}

// Home answers where the far account's home directory is.
//
// The answer is a VALUE rather than a captured stream: the probe asks twice (the
// shell's $HOME, then the tilde for a shell that answered nothing) and the
// helper applies that rule, so what comes back here is the answer to the
// QUESTION. Empty means the host could not say.
func (l *ProbeLease) Home(ctx context.Context) (string, error) {
	if err := l.live(); err != nil {
		return "", probeFailure(err)
	}
	var out proto.HomeResult
	err := l.client.Call(ctx, proto.ServiceSSH, proto.OpHome,
		proto.HomeParams{ProbeRunParams: proto.ProbeRunParams{Lease: l.id}}, &out)
	if err != nil {
		var refusal *RefusalError
		if errors.As(err, &refusal) && refusal.Code == proto.ErrCodeExecLost {
			l.finishLoss(fmt.Errorf("%w: %s", ErrLost, refusal.Message))
		}
		return "", probeFailure(err)
	}
	return out.Home, nil
}

// SamplePorts runs one rung of the port-discovery ladder on this lease.
func (l *ProbeLease) SamplePorts(ctx context.Context, probe remoteprobe.PortProbe) (*remoteprobe.Result, error) {
	return l.run(ctx, proto.OpSamplePorts, proto.SamplePortsParams{
		ProbeRunParams: proto.ProbeRunParams{Lease: l.id},
		Probe:          proto.PortProbe(probe),
	})
}

// Completion runs the completion probe for one line at one caret.
func (l *ProbeLease) Completion(ctx context.Context, cwd, line string, pos, limit int, nonce string) (*remoteprobe.Result, error) {
	return l.run(ctx, proto.OpCompletion, proto.CompletionParams{
		ProbeRunParams: proto.ProbeRunParams{Lease: l.id},
		Cwd:            cwd, Line: line, Pos: pos, Limit: limit, Nonce: nonce,
	})
}

// CommandNames runs one half of the PATH enumeration.
func (l *ProbeLease) CommandNames(ctx context.Context, phase remoteprobe.CommandNamesPhase, nonce string) (*remoteprobe.Result, error) {
	return l.run(ctx, proto.OpCommandNames, proto.CommandNamesParams{
		ProbeRunParams: proto.ProbeRunParams{Lease: l.id},
		Phase:          proto.CommandNamesPhase(phase),
		Nonce:          nonce,
	})
}

// run sends one named probe and converts its answer.
//
// Every failure that is not a probe's own exit status becomes a
// *remoteprobe.Error, because that is the vocabulary the three consumers switch
// on and this is the one place the wire's codes are known. A `lost` refusal also
// ends the lease: the connection it named is gone, and a consumer watching Done
// learns it here rather than at its next probe.
func (l *ProbeLease) run(ctx context.Context, op string, params any) (*remoteprobe.Result, error) {
	if err := l.live(); err != nil {
		return nil, probeFailure(err)
	}
	var out proto.ProbeExecResult
	err := l.client.Call(ctx, proto.ServiceSSH, op, params, &out)
	if err == nil {
		return &remoteprobe.Result{
			Stdout:     out.Stdout,
			Stderr:     out.Stderr,
			ExitStatus: out.ExitStatus,
			Truncated:  out.Truncated,
		}, nil
	}
	var refusal *RefusalError
	if errors.As(err, &refusal) && refusal.Code == proto.ErrCodeExecLost {
		l.finishLoss(fmt.Errorf("%w: %s", ErrLost, refusal.Message))
	}
	return nil, probeFailure(err)
}

// probeFailure converts a lease-level or wire-level failure into the shared
// probe vocabulary. A refusal the wire does not name keeps its own cause: an
// unclassifiable failure is reported as a lost transport rather than folded
// into a sentence about the host, which is the direction the probe op's own
// docs take about unclassifiable failures.
func probeFailure(err error) *remoteprobe.Error {
	var refusal *RefusalError
	if errors.As(err, &refusal) {
		switch refusal.Code {
		case proto.ErrCodeExecSessionRefused:
			return &remoteprobe.Error{Kind: remoteprobe.KindSessionRefused, Err: refusal}
		case proto.ErrCodeExecProhibited:
			return &remoteprobe.Error{Kind: remoteprobe.KindExecProhibited, Err: refusal}
		case proto.ErrCodeExecTooLong:
			return &remoteprobe.Error{Kind: remoteprobe.KindCommandTooLong, Err: refusal}
		case proto.ErrCodeExecLost:
			return &remoteprobe.Error{Kind: remoteprobe.KindConnectionLost, Err: refusal}
		}
		return &remoteprobe.Error{Kind: remoteprobe.KindConnectionLost, Err: refusal}
	}
	if errors.Is(err, ErrProbeLeaseClosed) {
		return &remoteprobe.Error{Kind: remoteprobe.KindLeaseClosed, Err: err}
	}
	if errors.Is(err, ErrLost) {
		return &remoteprobe.Error{Kind: remoteprobe.KindConnectionLost, Err: err}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &remoteprobe.Error{Kind: remoteprobe.KindConnectionLost, Err: err}
	}
	return &remoteprobe.Error{Kind: remoteprobe.KindConnectionLost, Err: err}
}

// endLeases ends every lease this client holds, with the cause the transport
// died for. Called by Client.lose, under no lock of its own.
func (c *Client) endLeases(cause error) {
	c.mu.Lock()
	leases := make([]*ProbeLease, 0, len(c.probeLeases))
	for _, l := range c.probeLeases {
		leases = append(leases, l)
	}
	c.probeLeases = make(map[proto.LeaseID]*ProbeLease)
	c.mu.Unlock()
	for _, l := range leases {
		l.finishLoss(fmt.Errorf("%w: %v", ErrLost, cause))
	}
}
