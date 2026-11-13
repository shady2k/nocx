//go:build nocx_local_ssh

// Package sshconn is the x/crypto carrier: the one implementation of
// hostconn.Carrier, and the only package besides internal/ssh that speaks
// golang.org/x/crypto/ssh about a connection a person owns.
//
// # What it is, and what it is not
//
// The supervisor above it knows a connection by its key and its state, and
// nothing about how it was dialled. Everything the library knows — the
// handshake's boundaries, the errors a refused sign-in arrives as, the
// client whose Wait is the transport's end — is translated HERE into the
// neutral vocabulary hostconn defines, so that the next transport is a package
// and one line at the composition root (design §5.1.2).
//
// The pool stays the connection table (design §5.1): this carrier acquires a
// pooled reference exactly as the helper did before it existed, and it adds no
// second table. What it adds is that the reference is the SUPERVISOR's, and
// that its transport's end is noticed at the moment it happens rather than when
// the last holder happens to unwind.
package sshconn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/shady2k/nocx/internal/hostconn"
	"github.com/shady2k/nocx/internal/log"
	"github.com/shady2k/nocx/internal/ssh"
	"github.com/shady2k/nocx/internal/wakesource"
	gossh "golang.org/x/crypto/ssh"
)

// CarrierName is the name this carrier answers to. It is the value a plan
// carries and the value a state's log line prints, so a person reading a
// connection's history can see which transport it was.
const CarrierName = "xcrypto"

// Carrier dials through x/crypto into the helper's own pool.
type Carrier struct {
	client *ssh.RealClient
	log    log.Logger
	conns  *hostconn.Manager
}

// Options is what a caller decides about the carrier's connection manager.
type Options struct {
	// Clock is the supervisor's time source; nil is the process's.
	Clock hostconn.Clock
	// Wake is the machine's wake source; nil is ONE WITH NO SIGNAL, which is
	// what a test driving its own signal wants. The production constructor
	// (New) supplies internal/wakesource's.
	Wake hostconn.WakeSource
}

// New builds the carrier over this machine's ssh client, on the process's clock
// and the machine's wake source: the same client the helper already holds,
// because the pool in it is the connection table and a second client would be a
// second pool (AD-4), and the two clocks the helper itself cannot read any
// other way (design §5.3, "On a wake signal").
func New(client *ssh.RealClient, logger log.Logger) *Carrier {
	return NewWithOptions(client, logger, Options{Wake: wakesource.New(logger)})
}

// NewWithOptions is New with the clock and the wake source decided by the
// caller. The fault harness states "within 30 s", "within five seconds of the
// signal" and "after ten minutes" by advancing a clock and by pulling a wake
// source, not by waiting (design §10), and the policy being asserted is the
// supervisor's.
func NewWithOptions(client *ssh.RealClient, logger log.Logger, opts Options) *Carrier {
	c := &Carrier{client: client, log: logger}
	c.conns = hostconn.NewManager(c, logger,
		hostconn.WithClock(opts.Clock), hostconn.WithWakeSource(opts.Wake))
	return c
}

// Name is this carrier's name in the neutral vocabulary.
func (c *Carrier) Name() string { return CarrierName }

// Capabilities states what this transport can do: x/crypto reports stages (a
// connection wrapper, the host-key callback and the sign-in), answers probes
// and multiplexes channels over one connection.
func (c *Carrier) Capabilities() hostconn.Capabilities {
	return hostconn.Capabilities{Stages: true, Liveness: true, Multiplex: true}
}

// DialAuth dials one resolved destination WITHOUT pooling it, which is what the
// helper's probe rides. It is here rather than on the carrier interface because
// it leaves no connection for a supervisor to own.
func (c *Carrier) DialAuth(ctx context.Context, spec ssh.PooledSpec) (*ssh.PooledConn, error) {
	return c.client.DialAuth(ctx, spec)
}

// Acquire answers a held reference to the connection for a resolved
// destination, through the supervisor: the helper's pooled acquire, which is
// the one door every channel, shell, forward and lease goes through.
func (c *Carrier) Acquire(ctx context.Context, spec ssh.PooledSpec) (*hostconn.Lease, error) {
	return c.conns.Acquire(ctx, PlanFor(spec))
}

// PlanFor renders a pooled spec into the plan the supervisor keys a connection
// by. The identity components are the pool's own — host, port, user, identity
// and route — because a supervisor keyed by anything else would either split
// one connection into two states or merge two principals into one.
func PlanFor(spec ssh.PooledSpec) hostconn.Plan {
	plan := hostconn.Plan{
		Carrier:       CarrierName,
		Key:           spec.Key(),
		Material:      spec,
		ProbeInterval: spec.ProbeInterval,
	}
	if spec.HoldsPlainShell {
		plan.Holds = hostconn.HoldsPlainShell
	}
	// A HOP CARRIES WHAT RIDES IT (design §5.1.1's second trap): a plain
	// shell on this connection is a plain shell on every hop it is dialled
	// through, and the hop's own closing policy may no more end under it than
	// this connection's may.
	plan.Route = ssh.RouteKeys(spec.Route)
	return plan
}

// Dial opens one connection and reports its stages. It is hostconn.Carrier's
// one method, called by the supervisor.
func (c *Carrier) Dial(ctx context.Context, plan hostconn.Plan, sink hostconn.EventSink) (hostconn.Link, error) {
	spec, ok := plan.Material.(ssh.PooledSpec)
	if !ok {
		return nil, fmt.Errorf("sshconn: a plan for %s carries %T, not a pooled spec", plan.Key, plan.Material)
	}
	if spec.Config == nil {
		return nil, fmt.Errorf("sshconn: a plan for %s carries no client configuration", plan.Key)
	}

	// THE HANDSHAKE'S TWO BOUNDARIES, in the order the library crosses them.
	// x/crypto has no stage callback, so the only honest boundaries are the
	// ones it calls out to: the host-key decision (after key exchange, before
	// sign-in) and the TCP connection itself, whose first bytes are the
	// version exchange.
	cfg := *spec.Config
	inner := cfg.HostKeyCallback
	cfg.HostKeyCallback = func(hostname string, remote net.Addr, key gossh.PublicKey) error {
		sink(hostconn.Event{Kind: hostconn.StageReached, Stage: hostconn.StageKeyExchange})
		sink(hostconn.Event{Kind: hostconn.StageReached, Stage: hostconn.StageSignIn})
		if inner == nil {
			// A plan with no verdict is not a connection this helper can
			// verify: the coordinator always supplies one, and guessing here
			// would be trusting a key nobody decided about (ADR-0034).
			return errors.New("sshconn: the connection plan carries no host-key verdict")
		}
		return inner(hostname, remote, key)
	}
	spec.Config = &cfg
	spec.WrapConn = func(conn net.Conn) net.Conn {
		sink(hostconn.Event{Kind: hostconn.StageReached, Stage: hostconn.StageTCP})
		return &versionExchange{Conn: conn, sink: sink}
	}

	// THE CLOSING POLICY'S ONE INPUT FROM THIS SIDE: may a connection that has
	// stopped answering be closed? Not while a plain shell rides it — that is
	// the person's choice (design §5.3). The pool's release barrier asks this
	// before it closes anything for silence, and the answer comes from the
	// supervisor, which is the party that counts holders.
	kept := c.conns.Supervisor(plan.Key)
	spec.AllowSilentClose = func() bool { return !kept.HoldsPlainShell() }

	pooled, err := c.client.AcquirePooled(ctx, spec)
	if err != nil {
		sink(dialFailure(err))
		return nil, err
	}
	return newLink(pooled, lookAddress(plan, spec)), nil
}

// dialFailure is what the machine is told about a dial that failed. It is the
// carrier's job rather than the supervisor's: the library's error types are the
// evidence, and this is the only package that reads them about a connection.
//
// A REFUSAL A RETRY CANNOT CHANGE reads failed, and nothing retries from there.
// Everything else reads lost: the transport did not come up and the next
// attempt may. That split is the design's own (§5.3: "Only network-class
// failures retry").
func dialFailure(err error) hostconn.Event {
	var changed *ssh.ErrHostKeyMismatch
	if errors.As(err, &changed) {
		return hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseHostKeyChanged}
	}
	var unknown *ssh.ErrUnknownHostKey
	if errors.As(err, &unknown) {
		// First contact is not a failure to report: it is a question waiting
		// for the person who decides it (ADR-0034), and the state says so.
		return hostconn.Event{Kind: hostconn.NeedsPerson, Wait: hostconn.WaitHostKey}
	}
	var noMethod *ssh.ErrNoAuthMethod
	if errors.As(err, &noMethod) {
		return hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseNoAuthMethod}
	}
	var auth *ssh.ErrAuthFailed
	if errors.As(err, &auth) {
		return hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseAuthRejected}
	}
	var rejected *ssh.ErrAuthRejected
	if errors.As(err, &rejected) {
		return hostconn.Event{Kind: hostconn.Refused, Cause: hostconn.CauseAuthRejected}
	}
	return hostconn.Event{Kind: hostconn.Lost, Cause: hostconn.CauseLostLink}
}

// versionExchange reports the SSH version exchange as the stage it is: the
// server's version line is the first bytes this connection ever reads, and
// nothing above the socket can see it. It reports once and forwards every byte
// untouched — it reads no content at all, which is what keeps AD-6's "the
// backend never sniffs the stream" true of this seam too.
type versionExchange struct {
	net.Conn
	once sync.Once
	sink hostconn.EventSink
}

func (c *versionExchange) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.once.Do(func() { c.sink(hostconn.Event{Kind: hostconn.StageReached, Stage: hostconn.StageSSHVersion}) })
	}
	return n, err
}
