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
	"sync/atomic"

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
	// STAGE REPORTING ENDS WITH THE DIAL (review finding 3). The library calls
	// the host-key callback again on every REKEY, long after Dial returned;
	// reporting then would take the supervisor's mutex from x/crypto's
	// key-exchange loop, which is exactly the loop a close waits for — a
	// deadlock. A report outside the dial is dropped.
	stages := &dialStages{sink: sink}
	stages.begin()
	defer stages.finish()
	reportStage := stages.report
	cfg.HostKeyCallback = func(hostname string, remote net.Addr, key gossh.PublicKey) error {
		reportStage(hostconn.StageKeyExchange)
		reportStage(hostconn.StageSignIn)
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
		reportStage(hostconn.StageTCP)
		return &versionExchange{Conn: conn, report: reportStage}
	}

	// THE RELEASE BARRIER'S ONE PROBER (design §5.3; review finding 1): the pool
	// waits for the far side to have answered before re-asking a refused
	// session, and the answer comes from the supervisor's own prober — the
	// probe already parked is the evidence, and no second keepalive is sent.
	sup := c.conns.Supervisor(plan.Key)
	spec.Barrier = sup.Probe
	// EVERY HOP IS A CONNECTION THE SUPERVISOR OWNS (review finding 4): it gets
	// its own link, watcher, prober and wake probe, and the dialler riding it
	// holds a lease rather than a bare pool entry nothing watches. The hops are
	// collected in dial order, so the first one is what a look-only dial goes
	// through.
	var hops []*ssh.PooledConn
	spec.HopAcquire = func(ctx context.Context, hop ssh.PooledHop, prefix []ssh.PooledHop) (*ssh.PooledConn, error) {
		pooled, err := c.acquireHop(ctx, hop, prefix)
		if err == nil {
			hops = append(hops, pooled)
		}
		return pooled, err
	}

	pooled, err := c.client.AcquirePooled(ctx, spec)
	if err != nil {
		sink(dialFailure(err))
		return nil, err
	}
	return newLink(pooled, lookFor(spec, hops)), nil
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

// dialStages is one dial's stage reporter: it answers while that dial is in
// flight and nothing after it. The library's host-key callback outlives the
// dial (a rekey calls it again), and a report then would take the supervisor's
// mutex from the key-exchange loop a close waits for.
type dialStages struct {
	sink    hostconn.EventSink
	dialing atomic.Bool
}

func (d *dialStages) begin() { d.dialing.Store(true) }

func (d *dialStages) finish() { d.dialing.Store(false) }

func (d *dialStages) report(stage hostconn.Stage) {
	if d.sink == nil || !d.dialing.Load() {
		return
	}
	d.sink(hostconn.Event{Kind: hostconn.StageReached, Stage: stage})
}

// versionExchange reports the SSH version exchange as the stage it is: the
// server's version line is the first bytes this connection ever reads, and
// nothing above the socket can see it. It reports once and forwards every byte
// untouched — it reads no content at all, which is what keeps AD-6's "the
// backend never sniffs the stream" true of this seam too.
type versionExchange struct {
	net.Conn
	once   sync.Once
	report func(hostconn.Stage)
}

func (c *versionExchange) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.once.Do(func() { c.report(hostconn.StageSSHVersion) })
	}
	return n, err
}

// acquireHop is the carrier's HopAcquire: it dials one hop of a route through
// the supervisor, exactly as a destination is dialled, and answers a reference
// the dialler releases by giving its lease back.
//
// The hop's own identity is the same five components (its endpoint, account,
// credential and the route before it), which is why the hop's supervisor and
// the pool entry the same facts describe are one connection: PlanFor derives
// the key from the spec and the pool derives the same key from the same spec.
func (c *Carrier) acquireHop(ctx context.Context, hop ssh.PooledHop, prefix []ssh.PooledHop) (*ssh.PooledConn, error) {
	spec := ssh.PooledSpec{
		Host:     hop.Host,
		Port:     hop.Port,
		User:     hop.User,
		Identity: hop.Identity,
		Config:   hop.Config,
		Route:    prefix,
	}
	lease, err := c.conns.Acquire(ctx, PlanFor(spec))
	if err != nil {
		return nil, err
	}
	pooled, ok := Pooled(lease.Link())
	if !ok {
		lease.Release()
		return nil, fmt.Errorf("sshconn: the hop %s@%s:%d has a carrier with no channels", hop.User, hop.Host, hop.Port)
	}
	return ssh.Borrow(pooled.Client(), lease.Release), nil
}
