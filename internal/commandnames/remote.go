package commandnames

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shady2k/nocx/internal/remoteprobe"
)

// ExecResult is one enumeration half's answer, in the shared probe vocabulary:
// the captured stdout, the remote exit status, and whether a capture bound was
// hit (Truncated — the output is a PREFIX, and a prefix of an enumeration is
// exactly the partial answer that may not be published).
type ExecResult = remoteprobe.Result

// Phase names which half of the enumeration to run. The spellings are
// remoteprobe's, because "which phase is this" is one fact on both sides of the
// helper wire (AD-8) — the coordinator picks it and the helper owns what it is.
type Phase = remoteprobe.CommandNamesPhase

const (
	// PhaseProbe is the cheap per-session invalidation probe.
	PhaseProbe = remoteprobe.CommandNamesProbe
	// PhaseScan is the full enumeration of executable names on PATH.
	PhaseScan = remoteprobe.CommandNamesScan
)

// ExecConn is a lease on a remote connection that can run one HALF of the
// enumeration.
//
// It names a phase and never a command: the two shell programs live in
// internal/remoteprobe, the helper links the same package and runs them on the
// pooled connection, and what crosses this seam is a phase and a nonce (D3 —
// no free-form exec crosses the helper wire).
type ExecConn interface {
	Enumerate(ctx context.Context, phase Phase, nonce string) (*ExecResult, error)
	Close() error
}

// ExecConnProvider acquires a lease for one call. The composition root wires
// this machine's helper — the same pooled lane completion and port discovery
// use, so a jump route reuses one connection instead of dialing the target
// directly.
type ExecConnProvider func(ctx context.Context) (ExecConn, error)

// RemoteSource enumerates one remote route's PATH over the discovery lane.
//
// There is no process group to own on the far side, so the deadline is
// enforced the only way a client can enforce one: the context bounds the
// exec, and a run that did not close its frame publishes nothing. That is
// the honest claim — "nocx bounds its own remote work by explicit numbers",
// never "the remote host cannot fall over", which no client can prove (D5).
type RemoteSource struct {
	route      string
	generation string
	provider   ExecConnProvider
}

// NewRemoteSource builds the source for one resolved route. route must be
// the RESOLVED identity — the user@host:port the connection actually reached
// — so two aliases for one host share one scan rather than scanning twice.
func NewRemoteSource(route, generation string, provider ExecConnProvider) *RemoteSource {
	return &RemoteSource{route: route, generation: generation, provider: provider}
}

func (s *RemoteSource) Identity() Identity {
	return Identity{Route: s.route, Generation: s.generation}
}

func (s *RemoteSource) Probe(ctx context.Context) (Probe, error) {
	out, nonce, err := s.run(ctx, PhaseProbe, ProbeDeadline)
	if err != nil {
		return Probe{}, err
	}
	return parseProbe(out, nonce)
}

func (s *RemoteSource) Scan(ctx context.Context, _ Probe) (Scan, error) {
	out, nonce, err := s.run(ctx, PhaseScan, ScanDeadline)
	if err != nil {
		return Scan{}, err
	}
	return parseScan(out, nonce)
}

func (s *RemoteSource) run(ctx context.Context, phase Phase, deadline time.Duration) ([]byte, string, error) {
	nonce, err := newNonce()
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	conn, err := s.provider(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("commandnames: lease: %w", err)
	}
	defer func() { _ = conn.Close() }()

	res, err := conn.Enumerate(ctx, phase, nonce)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, "", fmt.Errorf("%w: %v", ErrScanDeadline, err)
		}
		return nil, "", fmt.Errorf("commandnames: remote exec: %w", err)
	}
	if res.Truncated {
		// A truncated answer is a prefix. It is reported as the deadline's
		// state rather than a failure because the cause is the same — a
		// bound stopped the work — and the user's next move is the same.
		return nil, "", fmt.Errorf("%w: remote output was truncated", ErrScanDeadline)
	}
	if res.ExitStatus != 0 {
		return nil, "", fmt.Errorf("commandnames: remote sh exited %d", res.ExitStatus)
	}
	return res.Stdout, nonce, nil
}
