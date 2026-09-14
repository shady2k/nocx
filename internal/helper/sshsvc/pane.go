//go:build nocx_local_ssh

package sshsvc

// The PANE half of the `ssh` service: the far-side listeners a remote pane
// needs before its shell is started, on the same pooled connection the shell
// channel is opened on (nocx-50w7p.14).
//
// # What a pane needs, and why it is two listeners rather than one
//
// An integrated remote pane carries two things between the far host and the
// coordinator, and neither can be a channel the coordinator asked for by name:
//
//   - the LIFECYCLE CHANNEL (ADR-0024 decision 2). The shell on the far host
//     dials 127.0.0.1:<port> when its rcfile loads, so that port must exist on
//     that host BEFORE the shell runs. It is a remote forward (-R), and the
//     connection it produces is the session's lifecycle carrier — the raw
//     stream the session service moves and nothing else (plan §6).
//   - the AGENT TOOL SOCKET. An agent process on the far host dials a local
//     path to reach the coordinator's tool endpoint (the owner's decision,
//     nocx-e2bws). The far host's sshd creates that path for the login
//     account, and what arrives on it is FOR the coordinator's endpoint — so
//     this helper pipes it there, because it is the party on the endpoint's
//     own machine.
//
// Both are created HERE, before the shell channel is opened, because the
// launcher the caller renders names them: the lifecycle port travels to the
// far shell in frame 2 (carrier.go's note — the port is allocated by the
// listening side and is deliberately not in the command), and the tool socket
// path is rendered into the shell's own environment. A caller that built its
// command first would have nothing to name.
//
// # The pooled connection is the pane's own
//
// This file acquires ONE pooled reference per listener set, and OpenShell
// acquires its own for the channel. AD-4's pool is ref-counted and keyed by
// the resolved destination, so the second acquisition is the SAME connection
// the listeners are on — that is what the key means, and the pane's own test
// asserts the far host saw one connection for a pane that has a listener and a
// shell.
//
// # Who owns what
//
// The caller owns the returned object and MUST close it when the session ends:
// closing cancels the far-side listeners (their port and path stop existing
// there) and releases this service's pooled reference. Closing the bridge's
// far end is also what tells the session service no more lifecycle bytes will
// arrive — the window's closing event.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"

	"github.com/shady2k/nocx/internal/helper/host"
	"github.com/shady2k/nocx/internal/helper/proto"
	"github.com/shady2k/nocx/internal/helper/sshdial"
	"github.com/shady2k/nocx/internal/lifecyclechannel"
	"github.com/shady2k/nocx/internal/ssh"
)

// lifecycleBindHost is the far-side address the lifecycle listener is bound
// on. It is loopback and it is not a choice: the process that dials it is the
// login account on that machine, the address is never disclosed to anybody,
// and a listener on any other interface would be a port on somebody else's
// machine offered to their whole network.
const lifecycleBindHost = "127.0.0.1"

var (
	// errPaneToolEndpointUnreachable is what a far-side tool connection is
	// refused with when the pane's OWN coordinator endpoint cannot be reached
	// — the coordinator has exited, or its socket is gone.
	//
	// It is named, and it is what keeps two situations apart that a bare dial
	// error merges: "the endpoint that owns this pane is not there" and "the
	// request was wrong". Neither is repairable by forwarding somewhere else,
	// and there is deliberately nowhere else to forward to: the target is the
	// one the REQUEST named (nocx-50w7p.18), so a daemon- or sibling-held
	// endpoint — another coordinator of the same account — can no longer be
	// reached even by accident.
	errPaneToolEndpointUnreachable = errors.New("this pane's coordinator endpoint is not reachable, so the tool connection is refused rather than forwarded to another coordinator")
	// errNoPaneListeners is a listener set asked for with nothing to listen
	// for: neither the lifecycle channel nor a tool socket. It is a caller
	// that believes it is getting something (channel.go's own rule for a
	// target on an sftp open), not a no-op.
	errNoPaneListeners = errors.New("no far-side listener was asked for")
	// errBadPaneSpec is a pane's listeners asked for with an incomplete
	// destination. It is refused before anything is dialed, for the reason
	// validateShellSpec's errors are.
	errBadPaneSpec = errors.New("pane spec is incomplete")
)

// PaneSpec is one pane's far-side listeners, as the caller resolves them.
//
// Every field is either a resolved value or a decision the caller owns. The
// two paths are the caller's for the reason the tool socket is: this helper
// cannot read a far host's filesystem, so a path it invented would be a guess
// about somebody else's machine.
type PaneSpec struct {
	// Destination, AcceptOnTrust and HostKeyFingerprint are the same three a
	// ShellSpec carries, and they must agree with it: the listeners and the
	// shell channel share one pooled connection because they name one
	// destination.
	Destination        proto.SSHDestination
	AcceptOnTrust      bool
	HostKeyFingerprint string
	// Lifecycle asks for the loopback listener the integrated shell dials
	// back to.
	Lifecycle bool
}

// PaneListeners is one pane's far-side listeners and the lifecycle carrier
// they produce.
//
// It is a value the caller holds for the session's life; everything below is
// set before OpenPaneListeners returns except what the far side produces
// later.
type PaneListeners struct {
	pool *ssh.PooledConn
	log  *slog.Logger

	lifecycleLn net.Listener
	port        int
	// carrier is the session's end of the lifecycle stream: what the session
	// service reads (the shell's frames, into its window) and writes (the
	// coordinator's frames, out to the shell). sink is the far side's end,
	// bridged to the connection the shell dials.
	carrier *os.File
	sink    *os.File

	mu       sync.Mutex
	claimed  bool
	closeOne sync.Once
}

// OpenPaneListeners creates the far-side listeners a pane's launcher is
// rendered against.
//
// Its order is the contract's: validate, decide whether this helper can serve
// what was asked, acquire the pooled connection, create the listeners, answer.
// Nothing is dialed before validation, and the refusal for a capability this
// helper does not have (a far tool socket with no endpoint behind it) is raised
// BEFORE the ssh connection, so a request it cannot honour costs the far host
// nothing.
func (s *Service) OpenPaneListeners(ctx context.Context, spec PaneSpec) (*PaneListeners, error) {
	// VALIDATION FIRST, before the connection is resolved: the refusals this
	// function owns are about the REQUEST (a far tool path with nothing behind
	// it, an incomplete destination), and a request is refused the same way
	// whether or not a coordinator is currently attached.
	if err := validatePaneSpec(spec); err != nil {
		return nil, err
	}
	conn, _ := host.ConnectionFrom(ctx).(*host.Host)
	if conn == nil {
		return nil, errNoAuthChannel
	}

	p := &PaneListeners{
		log: s.log,
	}
	pool, err := s.acquirePooled(ctx, conn, spec.Destination, spec.AcceptOnTrust, spec.HostKeyFingerprint)
	if err != nil {
		return nil, err
	}
	p.pool = pool

	if spec.Lifecycle {
		if err := p.openLifecycle(pool); err != nil {
			_ = p.Close()
			return nil, err
		}
	}
	s.log.Info("ssh: pane listeners opened",
		"host", spec.Destination.Host, "port", spec.Destination.Port,
		"lifecycle", spec.Lifecycle, "lifecycle_port", p.port)
	return p, nil
}

// openLifecycle asks the far side for the loopback listener and starts
// accepting on it.
func (p *PaneListeners) openLifecycle(pool *ssh.PooledConn) error {
	ln, err := sshdial.ListenRemoteTCP(pool.Client(), net.JoinHostPort(lifecycleBindHost, "0"))
	if err != nil {
		// The far side refused a listener, and there is no lifecycle channel
		// without one. It is NOT degraded to a conventional session: the
		// caller asked for an authenticated channel, and a launch carrying a
		// port nobody listens on is a shell that blocks until its hello
		// budget runs out.
		return fmt.Errorf("the far side refused the lifecycle listener: %w", classifyChannelError(err))
	}
	port, err := listenerPort(ln)
	if err != nil {
		_ = ln.Close()
		return err
	}
	carrier, sink, err := lifecyclechannel.NewSocketPair()
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("lifecycle carrier: %w", err)
	}
	p.lifecycleLn, p.port, p.carrier, p.sink = ln, port, carrier, sink
	go p.acceptLifecycle()
	return nil
}

// acceptLifecycle gives the first connection the far shell makes to the
// carrier and closes any later one.
//
// ONE connection is what this channel is: the shell dials once and the stream
// it gets is the session's carrier for the session's whole life — the same
// single stream a local pane's socketpair is. A later connection is not a
// reconnect the session could adopt (a re-established domain rides the same
// carrier, and the carrier is a value the session service already holds), so
// it is refused rather than silently accepted.
func (p *PaneListeners) acceptLifecycle() {
	for {
		conn, err := p.lifecycleLn.Accept()
		if err != nil {
			// The listener is over: the session ended, or the connection
			// under it did. Closing the far end of the carrier is what tells
			// the session service no more lifecycle bytes will arrive — the
			// window's own closing event.
			_ = p.sink.Close()
			return
		}
		if !p.claimCarrier() {
			p.log.Warn("ssh: a second connection arrived on the lifecycle listener; the session's carrier is the first one",
				"peer", conn.RemoteAddr().String())
			_ = conn.Close()
			continue
		}
		go p.bridge(conn)
	}
}

// claimCarrier answers whether this connection is the first one.
func (p *PaneListeners) claimCarrier() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.claimed {
		return false
	}
	p.claimed = true
	return true
}

// bridge moves bytes between the far shell's connection and the carrier's far
// end, and ends both when either end does.
func (p *PaneListeners) bridge(conn net.Conn) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = conn.Close()
			_ = p.sink.Close()
		})
	}
	go func() {
		_, _ = io.Copy(p.sink, conn)
		closeBoth()
	}()
	go func() {
		_, _ = io.Copy(conn, p.sink)
		closeBoth()
	}()
}

// Lifecycle is the session's end of the lifecycle stream, or nil when this
// pane has no channel. It is the `session.LifecycleProcess` seam: what the
// session service reads into its window and writes the coordinator's frames
// into.
func (p *PaneListeners) Lifecycle() io.ReadWriteCloser {
	if p.carrier == nil {
		return nil
	}
	return p.carrier
}

// LifecyclePort is the port the far side bound. It is the value the launcher
// renders and the shell dials, and it is zero only when no lifecycle was asked
// for.
func (p *PaneListeners) LifecyclePort() int { return p.port }

// Close cancels the far-side listeners and releases this service's pooled
// reference. It is idempotent and safe to call from the session's own
// teardown, which is where it is called from.
func (p *PaneListeners) Close() error {
	p.closeOne.Do(func() {
		if p.lifecycleLn != nil {
			_ = p.lifecycleLn.Close()
		}
		if p.carrier != nil {
			_ = p.carrier.Close()
		}
		if p.sink != nil {
			_ = p.sink.Close()
		}
		// THE POOL IS LAST: it is the pane's own reference, and it is released
		// once, after everything riding it has been closed.
		if p.pool != nil {
			_ = p.pool.Close()
		}
	})
	return nil
}

// listenerPort reads the port the server allocated out of a remote listener.
//
// It is the transport's answer and never a promise — the same value
// ForwardResult.Bind carries for the coordinator's own forwards, read from the
// same place — and a listener that cannot report one is closed rather than
// answered: a port nobody can be told is a shell that dials nothing.
func listenerPort(ln net.Listener) (int, error) {
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 || addr.Port > 65535 {
		return 0, fmt.Errorf("the far side's listener reported %v, which is not a port", ln.Addr())
	}
	return addr.Port, nil
}

// validatePaneSpec refuses a listener set this helper will not create, before
// anything is dialed.
func validatePaneSpec(spec PaneSpec) error {
	if !spec.Lifecycle {
		// The only listener a PANE asks for now is the lifecycle channel: its
		// tool socket is the `ssh.tool-socket` op's (nocx-e2bws), which serves
		// a pane whose shell runs on a host whose own helper hosts it.
		return errNoPaneListeners
	}
	switch {
	case spec.Destination.Host == "":
		return fmt.Errorf("%w: no host", errBadPaneSpec)
	case spec.Destination.Port <= 0 || spec.Destination.Port > 65535:
		return fmt.Errorf("%w: port %d", errBadPaneSpec, spec.Destination.Port)
	case spec.Destination.User == "":
		return fmt.Errorf("%w: no user", errBadPaneSpec)
	}
	return validateIdentity(spec.Destination.Identity)
}
