//go:build nocx_local_ssh

package sshsvc

// The NAMED-PROBE half of the `ssh` service: the lease that keeps a pooled
// connection, and the five fixed shell commands nocx runs on it (nocx-50w7p.9).
//
// # Why the commands run HERE and not where they were built
//
// The owner's invariant moves every ssh dial into this process, and D3 refuses
// a free-form command on this wire: a caller-supplied argv reaching somebody
// else's machine is the capability the whole level exists to not hand out. So a
// command cannot cross, and what does cross is a NAME — which probe, which
// enumeration phase — plus typed arguments. The text itself lives in
// internal/remoteprobe, which this file and the coordinator both link, so
// "what nocx runs on a host" has one spelling rather than one per process.
//
// # What this file does NOT do
//
// It does not interpret an answer. It runs the command, captures the two
// streams up to a bound, reports the remote exit status, and stops. Which exit
// status means "the tool is absent" (discovery: 127), which means "nothing
// matched and that is a valid empty answer" (lsof: 1), which reply is polluted
// (completion: unframed) and whether an empty enumeration may be published are
// all judgements of the caller, and moving any of them here would leave the
// caller unable to disagree with this process about its own question.
//
// # The lease, and what it is for
//
// A probe asked with no reference held would acquire and release inside the one
// request — closing the connection for the next probe to redial it, which on a
// password-auth host is a second authentication per sample and on a host
// running fail2ban is the afternoon's work of a ban. So `lease` takes a pooled
// reference (AD-4's pool, acquired from the coordinator's own ssh package), the
// probes run on it, and `unlease` drops it. It is one more holder beside the
// tabs: the connection still closes when the last of them lets go.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"

	gossh "golang.org/x/crypto/ssh"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/remoteprobe"
	"github.com/shady2k/nocx/internal/ssh"
)

// errBadLeaseParams is a probe this helper will not run: a request that names
// no lease, or a probe/phase outside the closed set. It is refused before
// anything is dialed, for the reason errBadProbeParams is.
var errBadLeaseParams = errors.New("probe lease params are incomplete")

// probeLease is one pooled reference this helper holds for a coordinator, plus
// the sessions currently running on it.
//
// The session set exists for the same reason discoveryConn's does, one process
// out: releasing a lease while a probe is in flight must tear the remote
// command down rather than leave it running on somebody's host. Closing the
// auxiliary session is the only thing that stops a remote exec.
type probeLease struct {
	id          proto.LeaseID
	pool        *ssh.PooledConn
	fingerprint string

	mu       sync.Mutex
	sessions map[*gossh.Session]struct{}
	closed   bool
}

// end releases this lease's pooled reference and stops whatever is still
// running on it. Idempotent: an unlease and a shutting-down helper must not
// release the same reference twice.
func (l *probeLease) end() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	for sess := range l.sessions {
		_ = sess.Close()
	}
	l.sessions = nil
	l.mu.Unlock()
	_ = l.pool.Close()
}

// claim registers one in-flight session, or reports that the lease has already
// ended — the race the session set exists to close: a lease released between a
// probe's lookup and its session open must not leave that session running.
func (l *probeLease) claim(sess *gossh.Session) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	if l.sessions == nil {
		l.sessions = make(map[*gossh.Session]struct{})
	}
	l.sessions[sess] = struct{}{}
	return true
}

func (l *probeLease) release(sess *gossh.Session) {
	l.mu.Lock()
	delete(l.sessions, sess)
	l.mu.Unlock()
}

// probeOps adds the lease and the five named probes to the service's own.
func (s *Service) probeOps() []string {
	return []string{
		proto.OpLease,
		proto.OpUnlease,
		proto.OpUname,
		proto.OpHome,
		proto.OpSamplePorts,
		proto.OpCompletion,
		proto.OpCommandNames,
	}
}

// lease acquires one pooled reference for a destination and answers its id.
//
// The dial goes through the same acquirePooled the channel plane uses — the
// same client configuration, the same host-key questions asked of the
// coordinator over the connection that asked for this, the same pool keyed by
// host+port+user+identity — because a second way to dial would be a second
// answer to "how does this helper authenticate".
func (s *Service) lease(ctx context.Context, p proto.LeaseParams) (proto.LeaseResult, error) {
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		return proto.LeaseResult{}, errNoAuthChannel
	}
	if err := validateDestinationAddress(p.Destination); err != nil {
		return proto.LeaseResult{}, err
	}
	pool, err := s.acquirePooled(ctx, conn, p.Destination, p.AcceptOnTrust, "")
	if err != nil {
		return proto.LeaseResult{}, err
	}
	fingerprint := pool.Fingerprint()
	if fingerprint == "" {
		// A connection whose host key was never established could not have been
		// authenticated, and the fingerprint is what the install path keys a
		// consent decision by (ADR-0023). Rather than answer a lease whose
		// identity nobody can state, release it and refuse.
		_ = pool.Close()
		return proto.LeaseResult{}, internalRefusal("the connection reported no host-key fingerprint")
	}
	id, err := mintLeaseID()
	if err != nil {
		_ = pool.Close()
		return proto.LeaseResult{}, err
	}
	s.mu.Lock()
	if s.leases == nil {
		s.leases = make(map[proto.LeaseID]*probeLease)
	}
	s.leases[id] = &probeLease{id: id, pool: pool, fingerprint: fingerprint}
	s.mu.Unlock()
	return proto.LeaseResult{Lease: id, HostKeyFingerprint: fingerprint}, nil
}

// unlease releases one reference. An id this helper does not hold is answered
// as released rather than refused: the ordinary caller is a consumer shutting
// down, and a second release is not a disagreement about state.
func (s *Service) unlease(id proto.LeaseID) (proto.UnleaseResult, error) {
	if id.IsZero() {
		return proto.UnleaseResult{}, fmt.Errorf("%w: no lease id", errBadLeaseParams)
	}
	s.mu.Lock()
	l := s.leases[id]
	delete(s.leases, id)
	s.mu.Unlock()
	if l == nil {
		s.log.Debug("ssh: unlease for an unknown lease", "lease", id.String())
		return proto.UnleaseResult{}, nil
	}
	l.end()
	return proto.UnleaseResult{}, nil
}

// leaseByID finds one held lease, or refuses. A lease this helper does not
// hold — a helper that restarted holds nothing — is `exec_lost` rather than a
// bad request: the connection that lease named is gone, which is the fact the
// caller acts on.
func (s *Service) leaseByID(id proto.LeaseID) (*probeLease, error) {
	if id.IsZero() {
		return nil, fmt.Errorf("%w: no lease id", errBadLeaseParams)
	}
	s.mu.Lock()
	l := s.leases[id]
	s.mu.Unlock()
	if l == nil {
		return nil, &proto.Refusal{
			Code:    proto.ErrCodeExecLost,
			Message: "this helper holds no such probe lease",
		}
	}
	return l, nil
}

// uname runs the platform probe (D20).
func (s *Service) uname(ctx context.Context, p proto.UnameParams) (proto.ProbeExecResult, error) {
	return s.runProbe(ctx, p.Lease, remoteprobe.UnameCommand)
}

// home answers where the account's home directory is.
//
// It runs the probe's commands IN ORDER and answers the first non-empty one,
// which is why its result is a value rather than a stream: the fallback is part
// of what this probe IS (remoteprobe.HomeCommands), and leaving it to each
// caller would be two copies of one rule, one process apart.
func (s *Service) home(ctx context.Context, p proto.HomeParams) (proto.HomeResult, error) {
	lease, err := s.leaseByID(p.Lease)
	if err != nil {
		return proto.HomeResult{}, err
	}
	for _, command := range remoteprobe.HomeCommands {
		res, err := s.runProbeOn(ctx, lease, command)
		if err != nil {
			return proto.HomeResult{}, err
		}
		if home := strings.TrimSpace(string(res.Stdout)); home != "" {
			return proto.HomeResult{Home: home}, nil
		}
	}
	return proto.HomeResult{}, nil
}

// samplePorts runs one rung of port discovery's ladder, named by the caller.
func (s *Service) samplePorts(ctx context.Context, p proto.SamplePortsParams) (proto.ProbeExecResult, error) {
	command, ok := remoteprobe.PortCommand(remoteprobe.PortProbe(p.Probe))
	if !ok {
		return proto.ProbeExecResult{}, fmt.Errorf("%w: unknown port probe %q", errBadLeaseParams, p.Probe)
	}
	return s.runProbe(ctx, p.Lease, command)
}

// completion runs the completion probe for one line at one caret.
func (s *Service) completion(ctx context.Context, p proto.CompletionParams) (proto.ProbeExecResult, error) {
	if p.Nonce == "" {
		// The nonce is not decoration: the script frames its answer with it and
		// the caller accepts nothing else, so a probe asked without one could
		// only produce an answer nobody could trust.
		return proto.ProbeExecResult{}, fmt.Errorf("%w: completion probe with no nonce", errBadLeaseParams)
	}
	return s.runProbe(ctx, p.Lease, remoteprobe.CompletionCommand(p.Cwd, p.Line, p.Pos, p.Limit, p.Nonce))
}

// commandNames runs one half of the PATH enumeration.
func (s *Service) commandNames(ctx context.Context, p proto.CommandNamesParams) (proto.ProbeExecResult, error) {
	if p.Nonce == "" {
		return proto.ProbeExecResult{}, fmt.Errorf("%w: enumeration with no nonce", errBadLeaseParams)
	}
	command, ok := remoteprobe.CommandNamesCommand(remoteprobe.CommandNamesPhase(p.Phase), p.Nonce)
	if !ok {
		return proto.ProbeExecResult{}, fmt.Errorf("%w: unknown enumeration phase %q", errBadLeaseParams, p.Phase)
	}
	return s.runProbe(ctx, p.Lease, command)
}

// runProbe runs ONE composed command on a lease's pooled connection and answers
// what it wrote, how it exited, and whether the capture bound was hit.
//
// The command is composed by the caller of this function from
// internal/remoteprobe's own text — never from anything a remote caller sent —
// and it is bounded HERE rather than trusted: a command that would die in the
// remote execve at MAX_ARG_STRLEN is refused by name instead, which is a
// sentence a person can act on.
func (s *Service) runProbe(ctx context.Context, id proto.LeaseID, command string) (proto.ProbeExecResult, error) {
	lease, err := s.leaseByID(id)
	if err != nil {
		return proto.ProbeExecResult{}, err
	}
	return s.runProbeOn(ctx, lease, command)
}

// runProbeOn is runProbe on a lease the caller already holds, for the one probe
// that asks twice (home).
func (s *Service) runProbeOn(ctx context.Context, lease *probeLease, command string) (proto.ProbeExecResult, error) {
	if len(command) >= remoteprobe.MaxCommandLen {
		return proto.ProbeExecResult{}, &proto.Refusal{
			Code:    proto.ErrCodeExecTooLong,
			Message: fmt.Sprintf("the probe command is %d bytes, over the %d-byte bound", len(command), remoteprobe.MaxCommandLen),
		}
	}
	sess, err := lease.pool.Client().NewSession()
	if err != nil {
		return proto.ProbeExecResult{}, classifySessionOpenError(err)
	}
	if !lease.claim(sess) {
		// The lease ended between the lookup and here. Closing the session is
		// the whole answer: it must not run on a connection nothing owns.
		_ = sess.Close()
		return proto.ProbeExecResult{}, &proto.Refusal{
			Code:    proto.ErrCodeExecLost,
			Message: "the probe lease was released while the probe was starting",
		}
	}
	defer func() {
		_ = sess.Close()
		lease.release(sess)
	}()

	capped := make(chan struct{}, 1)
	stdout := newCappedBuffer(remoteprobe.MaxOutputBytes, capped)
	stderr := newCappedBuffer(remoteprobe.MaxOutputBytes, capped)
	sess.Stdout = stdout
	sess.Stderr = stderr

	errCh := make(chan error, 1)
	go func() { errCh <- sess.Run(command) }()

	var runErr error
	var capFired bool
	select {
	case runErr = <-errCh:
	case <-capped:
		// The capture bound fired: stop the remote rather than let it block for
		// ever on a full channel buffer, then wait for Run to observe it.
		capFired = true
		_ = sess.Close()
		runErr = <-errCh
	case <-ctx.Done():
		_ = sess.Close()
		<-errCh // Run has observed the close; no goroutine outlives this op
		return proto.ProbeExecResult{}, ctx.Err()
	}

	result := proto.ProbeExecResult{
		Stdout:    stdout.Bytes(),
		Stderr:    stderr.Bytes(),
		Truncated: stdout.over || stderr.over,
	}
	// Both streams are STRINGS on this wire and the frozen schema requires
	// them, so empty is spelled "" rather than null: a nil byte slice marshals
	// as null, which the shape refuses. Nothing is lost by it — "the command
	// wrote nothing" and "the stream was empty" are the same answer, and the
	// distinction a caller acts on is Truncated.
	if result.Stdout == nil {
		result.Stdout = []byte{}
	}
	if result.Stderr == nil {
		result.Stderr = []byte{}
	}
	// The exit status is an ANSWER, not a failure: 127 means the tool is
	// missing, 1 from lsof means nothing matched, and a policy wrapper may exit
	// anything at all. Only a failure to run AT ALL is a refusal.
	var exitErr *gossh.ExitError
	switch {
	case runErr == nil:
		return result, nil
	case errors.As(runErr, &exitErr):
		result.ExitStatus = exitErr.ExitStatus()
		return result, nil
	case errors.Is(runErr, errProbeOutputCapped), capFired:
		// We stopped it ourselves, so whatever Run reports afterwards is a
		// consequence of that and not a fact about the host. The caller learns
		// what happened from Truncated.
		return result, nil
	}
	return proto.ProbeExecResult{}, refuseProbeFailure(runErr, command)
}

// classifySessionOpenError maps a refused session channel to the wire's own
// code for it. OpenSSH reports "resource shortage" for MaxSessions and
// "administratively prohibited" for a policy refusal, and they are different
// facts about the host: one is a shell holding the only session, the other is a
// server that will never allow it.
func classifySessionOpenError(err error) error {
	var ocErr *gossh.OpenChannelError
	if errors.As(err, &ocErr) {
		switch ocErr.Reason {
		case gossh.ResourceShortage:
			return &proto.Refusal{Code: proto.ErrCodeExecSessionRefused, Message: err.Error()}
		case gossh.Prohibited:
			return &proto.Refusal{Code: proto.ErrCodeExecProhibited, Message: err.Error()}
		}
	}
	return &proto.Refusal{Code: proto.ErrCodeExecLost, Message: err.Error()}
}

// refuseProbeFailure types a run error that is not an exit status.
//
// The "ssh: command <cmd> failed" shape is pinned by x/crypto/ssh v0.54.0: the
// library answers the exec request's false reply with exactly that sentence and
// no typed sentinel, so it is recognised by shape — here, where the command is
// ours and the string is composed by the library rather than by anybody's
// script.
func refuseProbeFailure(err error, command string) error {
	if err.Error() == "ssh: command "+command+" failed" {
		return &proto.Refusal{Code: proto.ErrCodeExecProhibited, Message: "the host refused the exec request"}
	}
	return &proto.Refusal{Code: proto.ErrCodeExecLost, Message: err.Error()}
}

// errProbeOutputCapped is what a capped capture reports so Run stops reading.
var errProbeOutputCapped = errors.New("ssh: probe output exceeded the capture bound")

// cappedBuffer captures up to a bound and reports, once, that it was hit.
//
// It exists here rather than being borrowed from internal/ssh because the
// captured bytes never leave this process in that shape: what crosses is a
// proto.ProbeExecResult, and the only thing the two have to agree about is the
// BOUND — which is remoteprobe's, since the commands are.
type cappedBuffer struct {
	buf     []byte
	cap     int
	over    bool
	onBound chan struct{}
}

func newCappedBuffer(bound int, onBound chan struct{}) *cappedBuffer {
	return &cappedBuffer{cap: bound, onBound: onBound}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.cap - len(b.buf)
	if room <= 0 {
		b.mark()
		return 0, errProbeOutputCapped
	}
	if len(p) > room {
		b.buf = append(b.buf, p[:room]...)
		b.mark()
		return room, errProbeOutputCapped
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *cappedBuffer) mark() {
	if b.over {
		return
	}
	b.over = true
	if b.onBound != nil {
		select {
		case b.onBound <- struct{}{}:
		default:
		}
	}
}

func (b *cappedBuffer) Bytes() []byte { return b.buf }

// mintLeaseID mints one lease's identity.
//
// It lives HERE and not in proto for the reason mintChannelID does: the minting
// is the HELPER's act — the helper is the end that owns the connection, and the
// coordinator only echoes what it was told.
func mintLeaseID() (proto.LeaseID, error) {
	var id proto.LeaseID
	if _, err := rand.Read(id[:]); err != nil {
		return proto.LeaseID{}, fmt.Errorf("mint a lease id: %w", err)
	}
	return id, nil
}
