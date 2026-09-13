// Package tunnelchan is the coordinator's ssh TUNNEL transport: a forward, a
// remote listener and every connection they carry ride a channel on THIS
// MACHINE'S HELPER.
//
// # What moved, and what did not
//
// The owner's invariant (2026-09-13) is that there is no ssh connection
// without a helper, and the coordinator holds no ssh client (epic nocx-50w7p).
// Plan §3 names the tenants and this package is their transport: the forward
// model's three strategies, the remote lifecycle channel (ADR-0024), and an
// API request routed through a connection. What moved is the DIAL; what stayed
// is every decision around it — the destination is resolved here, by the party
// that reads ~/.ssh/config and holds the credential binding, and the lease's
// semantics are the ones the tenants already switch on.
//
// # The lease is a GROUP, not a connection
//
// ssh.TunnelConn was a reference to one pooled connection (AD-4). It is not one
// any more, and that is not a redefinition for convenience: the connection
// lives in the helper's pool now, keyed by host+port+user+identity, and the
// coordinator holds no reference to it. What the coordinator still has is the
// thing its callers actually use — a group of streams to one destination, with
// one Close and one loss signal — so that is what this type is.
//
// Three consequences follow, and they are the whole of the semantic
// difference:
//
//   - Acquiring a lease resolves and AUTHORIZES the destination and opens
//     nothing. A routed dial (a jump route) is still refused by name at that
//     moment — ssh.ErrRoutedDial, owned by nocx-50w7p.11 — and so is a
//     credential the profile does not authorize for the endpoint. The
//     CONNECTION is established when the first channel is opened on it, which
//     is AD-4's pool doing what it was built to do.
//   - Done closes on the HELPER connection's loss (nothing this lease holds
//     can work any more) and on a listener ending for a reason this lease did
//     not ask for (the far side's connection died under it — the case a remote
//     forward exists to report). A lease with no listener outlives a lost ssh
//     connection, because the next channel re-establishes it; the streams that
//     were open end with their own cause, on their own reads.
//   - Close ends this lease's streams and listeners and nothing else. The
//     connection stays pooled for the next lease, which is the property AD-4's
//     pool exists for.
//
// # Failure paths are the tenants', and they are not weakened
//
// A dial refused by the far side, a forward refused by policy, a channel lost
// mid-stream: each arrives as the helper classified it (internal/helper/sshsvc
// runs the SAME classifiers the coordinator's own dial path used, because a
// second vocabulary for one fact is what AD-8 forbids), and each is wrapped
// with the act that failed. The typed errors this path does NOT rebuild are the
// host-key ones: no consumer here switches on the type — the accept sheet that
// does (transport's hostKeyInfoFromError) belongs to the pane-open path — and
// the helper's sentence carries the fingerprint and the address, which is what
// a person reads.
package tunnelchan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	helperclient "github.com/shady2k/nocx/internal/helper/client"
	"github.com/shady2k/nocx/internal/ssh"
)

// Resolver answers the one thing a helper cannot decide: which host, port and
// account this is, and what may authenticate for it. *ssh.RealClient is the
// implementation, and the seam is the interface rather than the type so this
// package can be driven without a config file, a vault or a known_hosts.
type Resolver interface {
	ResolveTarget(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.DialTarget, error)
}

// Connector acquires one lease per forward. It is what
// transport.WithTunnelConnector takes and what internal/tunnel's own
// Connector seam is satisfied by — structurally, so this package needs no
// import of the domain model it serves.
type Connector struct {
	// Local is this machine's daemon connection, the same one every local
	// pane rides (helper_local.go's "one connection, every pane").
	//
	// It is a func rather than an interface because its owner returns more
	// than this package uses (the generation the connection was opened for)
	// and because the composition root is where "which daemon" is decided: a
	// wrapper type with one method would be a type for one line, and an
	// interface here could not be satisfied by the opener's own unexported
	// accessor anyway.
	Local func(ctx context.Context) (*helperclient.Client, error)
	// Resolve resolves the destination, and it runs BEFORE Local: a
	// destination this process refuses — a jump route, an unauthorized
	// credential — must not start a daemon to be told so.
	Resolve Resolver
	Log     *slog.Logger
}

// TunnelConn acquires a lease on every stream this caller will open to one
// destination.
//
// The resolution is eager and the dial is not, which is the split the package
// doc explains: a refusal the coordinator owns (ssh.ErrRoutedDial, an
// unauthorized credential) is raised HERE, where a caller's start path can
// report it, and the connection itself is established by the helper on the
// first channel.
func (c *Connector) TunnelConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.TunnelConn, error) {
	if c.Resolve == nil {
		return nil, errors.New("tunnelchan: no destination resolver is wired")
	}
	if c.Local == nil {
		return nil, errors.New("tunnelchan: no connection to this machine's helper is wired")
	}
	target, err := c.Resolve.ResolveTarget(ctx, host, opts...)
	if err != nil {
		return nil, err
	}
	client, err := c.Local(ctx)
	if err != nil {
		return nil, fmt.Errorf("tunnel to %s: %w", host, err)
	}
	return newLease(client, host, target, c.Log), nil
}

// The lease is the contract every tenant takes by interface, asserted here
// rather than left to a wiring line: the fingerprints are structural
// (ssh.TunnelConn is an interface), so a drift would surface as a compile
// error in whichever caller happened to be built next.
var _ ssh.TunnelConn = (*lease)(nil)
