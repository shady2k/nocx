// Package tunnel implements the port-forwarding domain model (spec §7): one
// record covering local (-L), remote (-R) and dynamic (-D) forwards, each
// behind a strategy interface (AD-8). The direction is chosen at
// construction — never a flag threaded into one forwarding loop — because
// -R brings remote-listener policy and -D brings SOCKS semantics that
// collapse under a shared loop.
package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/shady2k/nocx/internal/ssh"
)

// Direction names the three forwarding strategies the model covers.
type Direction string

const (
	// DirectionLocal forwards a local listener to a destination via one
	// direct-tcpip channel per accepted connection (-L). The destination
	// resolves on the server's network.
	DirectionLocal Direction = "local"
	// DirectionRemote creates a listener on the remote host (-R): governed
	// by the server's AllowTcpForwarding / PermitListen, and its own threat
	// warning — the bind appears on somebody else's host. The destination
	// resolves on the client's network (OpenSSH -R semantics).
	DirectionRemote Direction = "remote"
	// DirectionDynamic serves a local SOCKS5 proxy (-D). Each CONNECT target
	// is dialed as a direct-tcpip channel over the SSH connection, so the
	// domain-name form resolves at the far end. No fixed destination.
	DirectionDynamic Direction = "dynamic"
)

// Bind is an address:port pair — requested or actual.
type Bind struct {
	Host string
	Port int
}

// DefaultLocalHost is the default local bind address: IPv4 loopback.
// `localhost` is ambiguous across systems, and all-interfaces is an explicit
// advanced choice carrying a warning — never the default (spec §7.1).
const DefaultLocalHost = "127.0.0.1"

// Provenance says where a tunnel definition came from (spec §7): a human
// typed it, a detected row created it, or a profile established it.
type Provenance string

const (
	ProvenanceManual   Provenance = "manual"
	ProvenanceDetected Provenance = "detected"
	ProvenanceProfile  Provenance = "profile"
)

// Spec is the static definition of a forward: what was asked for, before any
// lifecycle state. One definition covers all three directions (spec D4).
type Spec struct {
	Direction   Direction
	Bind        Bind   // requested bind; Port 0 allocates (resolved at start)
	Destination string // "host:port"; empty for dynamic
	Scope       string // owning tab id or profile id (spec §7 scope/owner)
	Provenance  Provenance
}

// State is the lifecycle state of a tunnel.
type State string

const (
	// StateStarting: created, not yet bound.
	StateStarting State = "starting"
	// StateRunning: bound and forwarding.
	StateRunning State = "running"
	// StateStopped: no longer forwarding, for a named reason.
	StateStopped State = "stopped"
)

// StopReason explains why a stopped tunnel stopped.
type StopReason string

const (
	// StopReasonUser: stopped by the owner.
	StopReasonUser StopReason = "user"
	// StopReasonConnectionLost: the SSH connection died. The tunnel never
	// silently rebinds and never claims to still be running; restoration is
	// nocx-9le.7's boundary.
	StopReasonConnectionLost StopReason = "connection lost"
	// StopReasonError: a bind or acquire failure at start.
	StopReasonError StopReason = "error"
)

// Outcome explains why a strategy stopped. Valid once the strategy's done
// channel has closed.
type Outcome struct {
	StopReason StopReason
	Err        error
}

// Connector acquires an owned lease on the pooled SSH connection for a
// forward. The strategies take their OWN lease (spec §7.3) — never the tab's
// reference — so closing the creating tab releases the tab's reference, not
// the forward's, and one tab's teardown never kills another tab's forward on
// the same connection.
type Connector interface {
	TunnelConn(ctx context.Context, host string, opts ...ssh.ConnectOption) (ssh.TunnelConn, error)
}

// strategy implements one forwarding direction behind an interface (AD-8):
// local, remote and dynamic each live in their own file. There is never a
// direction switch inside an implementation.
type strategy interface {
	// start acquires the connection lease, binds the listener, and begins
	// forwarding. The bind happens BEFORE it reports (spec §7.1): EADDRINUSE,
	// an invalid address and a permission error are synchronous,
	// user-visible failures, never discoveries by a later goroutine. On
	// success it returns the ACTUAL bind — a requested port 0 is resolved
	// by the OS and reported, never left as 0. The port is never pre-checked
	// (TOCTOU); the listen itself is the check.
	start(ctx context.Context, host string, opts []ssh.ConnectOption) (Bind, error)
	// caveat reports a success-time caution about the bind, empty when none
	// applies. Only the remote strategy sets one: for a requested
	// non-loopback bind the tcpip-forward protocol cannot verify what the
	// server actually bound, so a URL built from the reported actual bind
	// may only work on the server. Valid after a successful start.
	caveat() string
	// stop tears the listener and in-flight streams down (user stop).
	stop()
	// done closes when the strategy stops for any reason: user, error,
	// connection loss.
	done() <-chan struct{}
	// outcome is valid once done has closed.
	outcome() Outcome
}

// Tunnel is one forward. It carries the full record the spec §7 names —
// direction, requested and actual bind, destination, scope/owner, lifecycle
// state, error reason, provenance — plus the strategy behind it (AD-8).
// Static fields are immutable after New; lifecycle state is read through the
// accessors.
type Tunnel struct {
	ID          string
	Direction   Direction
	Bind        Bind // requested bind
	Destination string
	Scope       string
	Provenance  Provenance

	conn Connector

	mu         sync.Mutex
	impl       strategy
	ActualBind Bind
	caveat     string
	state      State
	stopReason StopReason
	err        error

	done     chan struct{}
	doneOnce sync.Once
}

// New creates a Tunnel record for spec. It does not start anything. The bind
// host defaults to DefaultLocalHost when empty; the requested port is left
// untouched — the OS answers at start (trap 2: no pre-checking).
func New(spec Spec, conn Connector) (*Tunnel, error) {
	switch spec.Direction {
	case DirectionLocal, DirectionRemote:
		if spec.Destination == "" {
			return nil, fmt.Errorf("tunnel: %s destination is required", spec.Direction)
		}
		if _, _, err := net.SplitHostPort(spec.Destination); err != nil {
			return nil, fmt.Errorf("tunnel: invalid %s destination %q: %w", spec.Direction, spec.Destination, err)
		}
	case DirectionDynamic:
		// No destination: the SOCKS CONNECT target arrives per connection,
		// not from the spec. A destination on a dynamic forward would be
		// silently ignored — reject it instead.
		if spec.Destination != "" {
			return nil, errors.New("tunnel: dynamic destination must be empty")
		}
	case "":
		return nil, errors.New("tunnel: direction is required")
	default:
		return nil, fmt.Errorf("tunnel: unknown direction %q", spec.Direction)
	}
	if conn == nil {
		return nil, errors.New("tunnel: nil connector")
	}
	if spec.Bind.Host == "" {
		spec.Bind.Host = DefaultLocalHost
	}
	return &Tunnel{
		ID:          newID(),
		Direction:   spec.Direction,
		Bind:        spec.Bind,
		Destination: spec.Destination,
		Scope:       spec.Scope,
		Provenance:  spec.Provenance,
		conn:        conn,
		state:       StateStarting,
		done:        make(chan struct{}),
	}, nil
}

// localBindError marks the failure of the LOCAL listener — the net.Listen
// this process performs, as opposed to the SSH connection acquired before it
// or a listen the far end performs. It unwraps to the OS error underneath
// (EADDRINUSE, EACCES, an invalid address), so errors.Is keeps answering
// truthfully, while the marker is what lets a caller tell "the local port is
// held" from a dial failure that carries the same syscall: an SSH connector
// can fail with EADDRINUSE before the bind was even attempted, and only the
// bind has the remedy of another port.
type localBindError struct{ err error }

func (e *localBindError) Error() string { return e.err.Error() }

func (e *localBindError) Unwrap() error { return e.err }

// IsLocalBind reports whether err is the local listener's own bind failure.
func IsLocalBind(err error) bool {
	var e *localBindError
	return errors.As(err, &e)
}

// newID mints the tunnel's backend id (spec §7.3: every forward has its own
// backend id). Same shape as session.NewID: crypto/rand failure is treated
// as unreachable, and a deterministic fallback would mint colliding ids.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Start binds the listener and begins forwarding. The bind happens before
// this returns (spec §7.1): EADDRINUSE, an invalid address and a permission
// error are synchronous, user-visible failures. On success ActualBind
// carries the real bound address — a requested port 0 is resolved to the
// OS-assigned port. On failure the tunnel is stopped with StopReasonError.
func (t *Tunnel) Start(ctx context.Context, host string, opts ...ssh.ConnectOption) error {
	t.mu.Lock()
	if t.state != StateStarting {
		t.mu.Unlock()
		return fmt.Errorf("tunnel %s: already started (state %s)", t.ID, t.state)
	}
	impl, err := strategyFor(t.Direction, t.Bind, t.Destination, t.conn)
	if err != nil {
		t.state = StateStopped
		t.stopReason = StopReasonError
		t.err = err
		t.mu.Unlock()
		t.doneOnce.Do(func() { close(t.done) })
		return err
	}
	t.impl = impl
	t.mu.Unlock()

	actual, err := impl.start(ctx, host, opts)
	if err != nil {
		t.mu.Lock()
		t.state = StateStopped
		t.stopReason = StopReasonError
		t.err = err
		t.mu.Unlock()
		t.doneOnce.Do(func() { close(t.done) })
		return err
	}

	t.mu.Lock()
	t.ActualBind = actual
	t.caveat = impl.caveat()
	t.state = StateRunning
	t.mu.Unlock()

	// Watch the strategy: when it stops — user, error, connection loss —
	// move the record to stopped and signal Done. If Stop already moved it,
	// the state guard below keeps the earlier reason.
	go func() {
		<-impl.done()
		out := impl.outcome()
		t.mu.Lock()
		if t.state == StateRunning {
			t.state = StateStopped
			t.stopReason = out.StopReason
			t.err = out.Err
		}
		t.mu.Unlock()
		t.doneOnce.Do(func() { close(t.done) })
	}()
	return nil
}

// Stop tears the tunnel down: the listener closes, in-flight streams are
// closed, and the connection lease is released. The record moves to
// stopped:user. Idempotent.
func (t *Tunnel) Stop() {
	t.mu.Lock()
	impl := t.impl
	t.mu.Unlock()
	if impl != nil {
		impl.stop()
	}
	t.stopRecord(StopReasonUser, nil)
}

// stopRecord moves the record to stopped unless it already stopped for an
// earlier reason — a user Stop never overwrites a connection loss, and a
// late loss never overwrites a user stop.
func (t *Tunnel) stopRecord(reason StopReason, cause error) {
	t.mu.Lock()
	// A never-started tunnel (Stop before Start) also lands here; a stopped
	// tunnel keeps its earlier reason — a user Stop never overwrites a
	// connection loss, and a late loss never overwrites a user stop.
	if t.state == StateRunning || t.state == StateStarting {
		t.state = StateStopped
		t.stopReason = reason
		t.err = cause
	}
	t.mu.Unlock()
	t.doneOnce.Do(func() { close(t.done) })
}

// Done closes when the tunnel stops for any reason: user stop, bind error,
// connection loss.
func (t *Tunnel) Done() <-chan struct{} { return t.done }

// State returns the lifecycle state.
func (t *Tunnel) State() State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// StopReason explains why a stopped tunnel stopped. Valid once State is
// StateStopped.
func (t *Tunnel) StopReason() StopReason {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopReason
}

// Err is the error behind a stopped tunnel: the bind or acquire failure, or
// the transport error behind a connection loss. Nil for a clean user stop.
func (t *Tunnel) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// Actual returns the actual bind — the address the listener really holds.
// Only meaningful after Start succeeded: a requested port 0 is resolved to
// the OS-assigned port here, never reported as 0.
func (t *Tunnel) Actual() Bind {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ActualBind
}

// Caveat reports a success-time caution about the bind, empty for a clean
// bind. Only remote (-R) forwards set it: the tcpip-forward protocol
// confirms the port but never the bound host, so a requested non-loopback
// bind may have been silently rebound by the server (GatewayPorts=no), and a
// URL built from Actual() may only work on the server. Meaningful once Start
// succeeded; never an error — nothing failed.
func (t *Tunnel) Caveat() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.caveat
}

// strategyFor picks the strategy behind the AD-8 interface. This is the only
// place a direction maps to an implementation; the strategies themselves
// never switch on direction.
func strategyFor(dir Direction, bind Bind, dest string, conn Connector) (strategy, error) {
	switch dir {
	case DirectionLocal:
		return newLocal(bind, dest, conn), nil
	case DirectionRemote:
		return newRemote(bind, dest, conn), nil
	case DirectionDynamic:
		return newDynamic(bind, conn), nil
	default:
		return nil, fmt.Errorf("tunnel: unknown direction %q", dir)
	}
}
