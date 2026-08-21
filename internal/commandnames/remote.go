package commandnames

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ExecResult is one remote command's answer, mirroring what the SSH
// discovery lease returns. The package deliberately does not import
// internal/ssh: the seam is one method, and keeping it here is what lets the
// service be tested without a network.
type ExecResult struct {
	Stdout     []byte
	ExitStatus int
	Truncated  bool
}

// ExecConn is a lease on a remote connection that can run one command.
type ExecConn interface {
	Exec(ctx context.Context, cmd string) (*ExecResult, error)
	Close() error
}

// ExecConnProvider acquires a lease for one call. The composition root wires
// the SSH client's DiscoveryConn — the same pooled lane completion and port
// discovery use, so a jump route reuses one connection instead of dialing
// the target directly.
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
	out, nonce, err := s.run(ctx, probeScript, ProbeDeadline)
	if err != nil {
		return Probe{}, err
	}
	return parseProbe(out, nonce)
}

func (s *RemoteSource) Scan(ctx context.Context, _ Probe) (Scan, error) {
	out, nonce, err := s.run(ctx, scanScript, ScanDeadline)
	if err != nil {
		return Scan{}, err
	}
	return parseScan(out, nonce)
}

func (s *RemoteSource) run(ctx context.Context, script string, deadline time.Duration) ([]byte, string, error) {
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

	res, err := conn.Exec(ctx, remoteCommand(script, nonce))
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

// remoteCommand wraps the script in a quoted heredoc, the same shape
// internal/completion uses for the same reason: no temp file on the far
// side, no printf escaping, and a delimiter carrying the nonce so it cannot
// collide with the script's own text. The quoted delimiter suppresses
// expansion, so the body arrives verbatim.
func remoteCommand(script, nonce string) string {
	delim := "NOCXCN_" + nonce
	var b strings.Builder
	b.WriteString("sh -s ")
	b.WriteString(nonce)
	b.WriteString(" << '")
	b.WriteString(delim)
	b.WriteString("'\n")
	b.WriteString(script)
	b.WriteString("\n")
	b.WriteString(delim)
	b.WriteString("\n")
	return b.String()
}
