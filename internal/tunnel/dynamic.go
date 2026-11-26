package tunnel

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shady2k/nocx/internal/ssh"
)

// SOCKS5 protocol constants (RFC 1928).
const (
	socksVersion                 = 0x05
	socksCmdConnect              = 0x01
	socksCmdBind                 = 0x02
	socksCmdUDPAssociate         = 0x03
	socksATYPIPv4                = 0x01
	socksATYPDomain              = 0x03
	socksATYPIPv6                = 0x04
	socksMethodNoAuth            = 0x00
	socksMethodNoAcceptable      = 0xFF
	socksRepSuccess              = 0x00
	socksRepGeneralFailure       = 0x01
	socksRepNetworkUnreachable   = 0x03
	socksRepConnectionRefused    = 0x05
	socksRepCmdNotSupported      = 0x07
	socksRepAddrTypeNotSupported = 0x08
	// socksHandshakeTimeout bounds the greeting and request reads so a
	// client that connects and stalls cannot pin a stream forever.
	socksHandshakeTimeout = 10 * time.Second
)

// dynamic implements the dynamic (-D) strategy: a local SOCKS5 server
// (spec §7.2). Each CONNECT target is dialed as one direct-tcpip channel
// over the pooled connection — the domain-name form is forwarded verbatim,
// so name resolution happens at the far end, which is the point of -D. The
// local bind follows -L's rules: default loopback, port 0 allocates and is
// reported, no pre-checking.
type dynamic struct {
	conn Connector
	bind Bind
	fl   *forwardLifecycle
}

func newDynamic(bind Bind, conn Connector) *dynamic {
	return &dynamic{
		conn: conn,
		bind: bind,
		fl:   newForwardLifecycle(),
	}
}

// start implements strategy.start for -D: acquire the lease, bind the LOCAL
// SOCKS listener, publish, and serve. Same trap order as -L (bind before
// reporting, no pre-check, port 0 reported from the listener).
func (d *dynamic) start(ctx context.Context, host string, opts []ssh.ConnectOption) (Bind, error) {
	lease, err := d.conn.TunnelConn(ctx, host, opts...)
	if err != nil {
		return Bind{}, fmt.Errorf("tunnel: acquire connection: %w", err)
	}

	addr := net.JoinHostPort(d.bind.Host, strconv.Itoa(d.bind.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		_ = lease.Close()
		return Bind{}, &localBindError{err: err}
	}
	if !d.fl.publish(lease, ln) {
		_ = lease.Close()
		_ = ln.Close()
		return Bind{}, errors.New("tunnel: connection lost before bind completed")
	}

	go d.fl.watchLoss(lease)

	hostStr, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		d.fl.shutdown(StopReasonError, err)
		return Bind{}, fmt.Errorf("tunnel: parse actual bind %q: %w", ln.Addr().String(), err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		d.fl.shutdown(StopReasonError, err)
		return Bind{}, fmt.Errorf("tunnel: parse actual port %q: %w", portStr, err)
	}

	go d.fl.serve(ln, d.handleSocks)
	return Bind{Host: hostStr, Port: port}, nil
}

// handleSocks serves one accepted SOCKS5 connection. The negotiation happens
// first (bounded by a deadline), then one direct-tcpip channel per CONNECT.
// A refused target replies 0x05 and closes only this stream — the proxy
// keeps serving (spec §7.2, same rule as trap 4).
func (d *dynamic) handleSocks(c net.Conn) {
	defer func() {
		d.fl.untrack(c)
		_ = c.Close()
	}()

	_ = c.SetDeadline(time.Now().Add(socksHandshakeTimeout))
	target, err := negotiateSocks(c)
	if err != nil {
		// negotiateSocks wrote the failure reply, or the client vanished.
		return
	}
	_ = c.SetDeadline(time.Time{})

	d.fl.mu.Lock()
	lease := d.fl.lease
	d.fl.mu.Unlock()
	if lease == nil {
		// shutdown already ran — the listener closed between Accept and
		// here, orphaning this stream. Do not dial on a dead forward.
		return
	}

	remote, err := lease.Dial(target)
	if err != nil {
		_ = writeSocksReply(c, socksReplyCode(err))
		return
	}
	defer func() { _ = remote.Close() }()

	if err := writeSocksReply(c, socksRepSuccess); err != nil {
		return
	}
	d.fl.relay(c, remote)
}

// negotiateSocks performs the SOCKS5 greeting and CONNECT request and
// returns the target as "host:port", verbatim — the domain-name form is NOT
// resolved locally, so name resolution happens at the far end of the SSH
// connection. On a protocol failure it writes the correct failure reply
// (0xFF for no acceptable method, 0x07 for BIND/UDP ASSOCIATE, 0x08 for an
// unsupported address type) and returns an error; the caller just closes
// the connection. A client that gets the reply knows the truth instead of
// an EOF that reads as "the proxy is broken".
func negotiateSocks(c net.Conn) (string, error) {
	// Greeting: VER, NMETHODS, METHODS.
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return "", fmt.Errorf("tunnel: socks greeting: %w", err)
	}
	if hdr[0] != socksVersion {
		return "", errors.New("tunnel: socks greeting: unsupported version")
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", fmt.Errorf("tunnel: socks greeting: %w", err)
	}
	if !bytes.Contains(methods, []byte{socksMethodNoAuth}) {
		_ = writeSocksMethodReply(c, socksMethodNoAcceptable)
		return "", errors.New("tunnel: socks greeting: no acceptable auth method")
	}
	if err := writeSocksMethodReply(c, socksMethodNoAuth); err != nil {
		return "", fmt.Errorf("tunnel: socks greeting reply: %w", err)
	}

	// Request: VER, CMD, RSV, ATYP, DST.ADDR, DST.PORT.
	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return "", fmt.Errorf("tunnel: socks request: %w", err)
	}
	if req[0] != socksVersion {
		return "", errors.New("tunnel: socks request: unsupported version")
	}
	switch req[1] {
	case socksCmdConnect:
		// the only command this proxy serves
	case socksCmdBind, socksCmdUDPAssociate:
		_ = writeSocksReply(c, socksRepCmdNotSupported)
		return "", errors.New("tunnel: socks request: command not supported")
	default:
		_ = writeSocksReply(c, socksRepCmdNotSupported)
		return "", fmt.Errorf("tunnel: socks request: unknown command 0x%02x", req[1])
	}

	var host string
	switch req[3] {
	case socksATYPIPv4:
		b := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", fmt.Errorf("tunnel: socks request: %w", err)
		}
		host = net.IP(b).String()
	case socksATYPDomain:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return "", fmt.Errorf("tunnel: socks request: %w", err)
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(c, b); err != nil {
			return "", fmt.Errorf("tunnel: socks request: %w", err)
		}
		host = string(b)
	case socksATYPIPv6:
		b := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", fmt.Errorf("tunnel: socks request: %w", err)
		}
		host = net.IP(b).String()
	default:
		_ = writeSocksReply(c, socksRepAddrTypeNotSupported)
		return "", fmt.Errorf("tunnel: socks request: unsupported address type 0x%02x", req[3])
	}
	var portB [2]byte
	if _, err := io.ReadFull(c, portB[:]); err != nil {
		return "", fmt.Errorf("tunnel: socks request: %w", err)
	}
	if host == "" {
		// An empty domain name is not a connectable target.
		_ = writeSocksReply(c, socksRepGeneralFailure)
		return "", errors.New("tunnel: socks request: empty destination")
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portB[:])))), nil
}

// socksReplyCode maps a dial failure to the SOCKS5 reply code the client can
// act on: refused is 0x05, unreachable is 0x03, everything else is a
// generic 0x01. A direct-tcpip open that the SSH server refuses surfaces as
// gossh's rejection message ("connect failed: Connection refused"), which is
// not a syscall error — match its text as well as the errno chain.
func socksReplyCode(err error) byte {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return socksRepConnectionRefused
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return socksRepNetworkUnreachable
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "refused"):
		return socksRepConnectionRefused
	case strings.Contains(msg, "unreachable"):
		return socksRepNetworkUnreachable
	}
	return socksRepGeneralFailure
}

// writeSocksReply sends the fixed SOCKS5 reply envelope: VER=5, REP, RSV=0,
// ATYP=1 (IPv4), BND.ADDR=0.0.0.0, BND.PORT=0. The bound address of a relay
// over SSH is unknown to the client side, and 0.0.0.0:0 is the standard
// "unknown" answer every SOCKS5 client accepts.
func writeSocksReply(c net.Conn, rep byte) error {
	_, err := c.Write([]byte{
		socksVersion, rep, 0x00, socksATYPIPv4,
		0x00, 0x00, 0x00, 0x00, // BND.ADDR 0.0.0.0
		0x00, 0x00, // BND.PORT 0
	})
	return err
}

// writeSocksMethodReply sends the SOCKS5 method-selection reply: exactly
// VER and the chosen METHOD — two bytes. It is a different message from the
// 10-byte request reply envelope (writeSocksReply): a 0xFF method refusal
// must not carry BND fields, and a client that negotiated no-auth expects
// only the two bytes back.
func writeSocksMethodReply(c net.Conn, method byte) error {
	_, err := c.Write([]byte{socksVersion, method})
	return err
}

// stop implements strategy.stop: the user stopped the forward.
func (d *dynamic) stop() {
	d.fl.shutdown(StopReasonUser, nil)
}

func (d *dynamic) done() <-chan struct{} { return d.fl.done() }

func (d *dynamic) outcome() Outcome { return d.fl.outcome() }

// caveat implements strategy.caveat: -D has no bind caveat — the local
// SOCKS listener's address is the OS's own answer, verified by the bind.
func (d *dynamic) caveat() string { return "" }
