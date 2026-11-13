//go:build nocx_local_ssh

// This file performs the network dial for a pooled connection: the TCP dial,
// the client handshake over a connection an earlier hop already carried, and
// the pool-key rendering a routed destination is identified by. It is
// nocx_local_ssh-gated because the coordinator makes no ssh connection at all
// — cmd/nocx-server is never built with the tag, and the local helper is
// (plan 2026-09-13 §1, §3). What is left untagged in this package is
// resolution and the host-key decision, which a build with no client still
// owns.

package ssh

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/shady2k/nocx/internal/operation/catalog"

	gossh "golang.org/x/crypto/ssh"
)

// dialer encapsulates SSH connection logic. It builds the per-target
// gossh.ClientConfig and performs the real network dial (direct or via a
// jump host). The pool owns the resulting connection; dialer only produces it.
type dialer struct {
	client *RealClient
}

// dialDirect establishes a direct SSH connection with context support.
//
// The TCP dial uses net.Dialer.DialContext, which respects ctx cancellation.
// gossh.NewClientConn has no context-aware form (v0.54.0), so it runs in a
// goroutine with a watchdog on ctx.Done(): closing the underlying net.Conn
// unblocks the handshake. The goroutine is drain-safe because the buffered
// channel (size 1) ensures the send always succeeds. The caller sees
// ctx.Err(), not an incidental "use of closed network connection" error.
func (d *dialer) dialDirect(ctx context.Context, addr string, cfg *gossh.ClientConfig, host, user string, wrap func(net.Conn) net.Conn) (*gossh.Client, error) {
	d.client.log.Info("Dialing directly", "addr", addr, "user", cfg.User)

	netConn, err := d.dialWithCtx(ctx, "tcp", addr, cfg.Timeout)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	// The wrapper goes on HERE and not earlier: it reports that the TCP dial
	// succeeded, which is the first thing it knows that the caller does not.
	if wrap != nil {
		netConn = wrap(netConn)
	}

	type hsResult struct {
		conn  gossh.Conn
		chans <-chan gossh.NewChannel
		reqs  <-chan *gossh.Request
		err   error
	}
	ch := make(chan hsResult, 1)
	go func() {
		conn, chans, reqs, err := gossh.NewClientConn(netConn, addr, cfg)
		ch <- hsResult{conn, chans, reqs, err}
	}()

	select {
	case <-ctx.Done():
		_ = netConn.Close() // unblocks NewClientConn
		<-ch                // drain goroutine
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			_ = netConn.Close()
			if isAuthError(r.err) {
				return nil, &ErrAuthFailed{User: user, Host: host, Err: r.err}
			}
			return nil, r.err
		}
		return gossh.NewClient(r.conn, r.chans, r.reqs), nil
	}
}

// dialWithCtx dials a network address with context cancellation support.
func (d *dialer) dialWithCtx(ctx context.Context, network, addr string, timeout time.Duration) (net.Conn, error) {
	scope := catalog.BeginInternal(ctx, catalog.OpInternalSshDial)
	defer scope.Complete()
	// KeepAlive is OFF, and that is the one closing policy's requirement
	// (design §5.3). Go's default is 15 s idle + 15 s between probes + 9
	// unanswered, about 150 s and then the socket errors — which would end a
	// plain shell at two and a half minutes on a link where only the path is
	// down, while the design says it waits until the person chooses. The
	// supervisor's prober already covers detection, and the kernel's own
	// keepalive would be a second prober with a second policy.
	netDialer := &net.Dialer{Timeout: timeout, KeepAlive: -1}
	return netDialer.DialContext(ctx, network, addr)
}

// isAuthError returns true if the error likely comes from a failed SSH
// authentication.
//
// It reads the library's message because x/crypto/ssh 0.54.0 offers no typed
// error for a rejected sign-in on a handshake. fmt.Sprint produces the same
// text as the error's own method, which the log-context ratchet would read as
// a log call site — and this is not one.
func isAuthError(err error) bool {
	msg := fmt.Sprint(err)
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain") ||
		strings.Contains(msg, "ssh: handshake failed") ||
		strings.Contains(msg, "no common algorithms")
}

// handshakeOver runs the client handshake on an ALREADY-OPEN connection: the
// step a route takes once the hop before it has dialed the next address, and
// the one x/crypto/ssh has no context-aware form for. The watchdog is what
// makes cancellation real — closing the connection unblocks NewClientConn —
// and the goroutine is drain-safe because the buffered channel (size 1) means
// its send always succeeds.
//
// It has exactly one caller on purpose — the routed dial — because a second
// copy would be a second chance to leave a watchdog without its drain, and
// the failure mode of that is a leaked goroutine per cancelled connection.
func (d *dialer) handshakeOver(ctx context.Context, conn net.Conn, addr string, cfg *gossh.ClientConfig) (*gossh.Client, error) {
	type hsResult struct {
		clientConn gossh.Conn
		chans      <-chan gossh.NewChannel
		reqs       <-chan *gossh.Request
		err        error
	}
	ch := make(chan hsResult, 1)
	go func() {
		cc, chans, reqs, err := gossh.NewClientConn(conn, addr, cfg)
		ch <- hsResult{cc, chans, reqs, err}
	}()

	select {
	case <-ctx.Done():
		_ = conn.Close() // unblocks NewClientConn
		<-ch             // drain goroutine
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			_ = conn.Close()
			return nil, r.err
		}
		return gossh.NewClient(r.clientConn, r.chans, r.reqs), nil
	}
}
