// Package mux is a client of an OpenSSH multiplex master's control socket.
//
// It exists because of one measurement and one decision. The measurement is
// the multiplex spike's: with the server's MaxSessions at 1 the mux session
// request is refused, and `sftp -o ControlMaster=auto` — told in as many words
// to use the master — quietly opens its OWN connection and authenticates a
// second time. The spike's table records that as "no-second-auth promise
// broken". A second authentication can be a second password or a second 2FA
// prompt: a credential use the user did not ask for, arriving as a side effect
// of a feature they did not request.
//
// The decision is ADR-0035's: THE ADAPTER IS MUX-ONLY, WITH NO FALLBACK. A
// refused session request refuses the delivery; it never opens a connection.
// OpenSSH's own clients cannot promise that — there is no option that says
// "use the master or fail" — so the client is here instead, and the property
// is structural rather than configured: this package speaks to a unix socket
// and has no code that dials a network address at all.
//
// Two further properties follow from the same place:
//
//   - OWNERSHIP IS PROVEN, NEVER ASSUMED (design D3). Open completes the
//     protocol's hello exchange and an alive check against THAT SPECIFIC
//     socket before it returns. A socket that is absent, that answers another
//     protocol version, or that answers nothing is not ownership, and until
//     Open returns nothing may be published, minted or written remotely.
//
//   - LIVENESS IS NOT IDENTITY. A master answers an alive check regardless of
//     which destination it was created for — our own spike measured a push
//     aimed at a different port landing on the master's server, as a second
//     subsystem session, with no new authentication. The control socket IS the
//     trust boundary, so this package is only ever pointed at a socket nocx
//     created for one destination (a %C-derived path under a directory we
//     own). Reusing a master the user runs is rejected in ADR-0035, not
//     deferred, and there is deliberately no API here for it.
//
// The wire is OpenSSH's PROTOCOL.mux. Every message is a big-endian uint32
// length followed by that many bytes, and the body opens with a uint32 type.
package mux

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// protocolVersion is SSHMUX_VER: the only version this client speaks. A
// master answering anything else is not a master we own — refuse rather than
// guess at a format.
const protocolVersion = 4

// Message types, PROTOCOL.mux. Only the ones this client sends or expects are
// named; an unnamed type arriving is an error, never a silent skip.
const (
	msgHello    = 0x00000001
	cNewSession = 0x10000002
	cAliveCheck = 0x10000004
	cTerminate  = 0x10000005

	sOK               = 0x80000001
	sPermissionDenied = 0x80000002
	sFailure          = 0x80000003
	sAlive            = 0x80000005
	sSessionOpened    = 0x80000006
	sTTYAllocFail     = 0x80000008
)

// maxPacket bounds one control-socket message. The vocabulary is short and
// the socket is ours; a reader with no bound would accumulate whatever a
// broken peer sent looking for an end.
const maxPacket = 256 * 1024

// MaxCommandLen is the bound on SessionRequest.Command, enforced HERE —
// at the seam that hands the command to the master — and not only where a
// command is built (nocx-e4ir3).
//
// The distinction is the whole point. Before nocx-m8jwn the remote command
// was ~92 KiB carrying the integration bundle and two bearers, and the cap
// that was supposed to stop it (120 KiB) sat beside the builder that made
// it: one producer checked itself, and the measured command sat at 75% of
// the cap with nobody watching. A bound a producer applies to itself is a
// convention. A bound the transport applies to everything it is handed is
// a bound, and it holds for the producer that does not exist yet.
//
// The number is 1 KiB: the size a consumer of an exec request has to be
// able to carry whole as one field of one record. It is deliberately far
// below every mechanical ceiling in the path — Linux's MAX_ARG_STRLEN is
// 131072 bytes for the far side's execve, and maxPacket above is 256 KiB —
// because those ceilings are what a caller hits when nobody declared a
// contract, and hitting them produces somebody else's opaque error rather
// than our named refusal.
//
// It is one number declared in three packages, because AD-8 forbids the
// imports that would make it one symbol: internal/shellintegration owns
// MaxCarrierLen (the producer's contract), internal/ssh owns
// MaxRemoteCommandLen (the gossh seams), and this package owns the control
// socket. TestTheBoundIsOneNumber in internal/app pins all three equal, so
// raising any one of them alone goes red.
const MaxCommandLen = 1024

// dialTimeout bounds the connect to the control socket. It is a local unix
// socket, so this is a liveness bound on a file, not a network wait.
const dialTimeout = 5 * time.Second

var (
	// ErrSessionRefused is the master telling us the session request failed.
	// It is the whole of D3's fallback question: a caller that sees this
	// refuses the delivery, and there is nothing else it may do.
	ErrSessionRefused = errors.New("mux: the master refused the session request")

	// ErrCommandTooLong is a SessionRequest whose Command is at or above
	// MaxCommandLen. Nothing is dialled, nothing is encoded and nothing is
	// sent: a refusal that has already written the packet is not a bound.
	// The command is never truncated — a shortened command runs something
	// the caller did not ask for, on somebody else's machine.
	ErrCommandTooLong = errors.New("mux: the remote command is longer than the bound")

	// ErrHandshake is a socket that did not complete the mux handshake.
	// Ownership is not proven, so nothing may be published or minted.
	ErrHandshake = errors.New("mux: the control socket did not complete the handshake")
)

// SessionRequest is one auxiliary channel on the master's connection.
//
// There is no WantTTY: the user's own process carries the interactive session
// and there is never a second one. A tty here would be exactly that.
type SessionRequest struct {
	// Subsystem selects a subsystem request rather than an exec — this is
	// how the SFTP publish rides the master.
	Subsystem bool
	// Command is the remote command, or the subsystem name.
	Command string
	// Env are `NAME=value` strings passed with the request.
	Env []string
}

// Master is a proven-owned multiplex master.
//
// Open holds one control connection for the lifetime of the value, which is
// what keeps the alive check and the exit request answerable; each Session
// opens its own control connection, because PROTOCOL.mux gives a session
// request the connection it arrived on.
type Master struct {
	path string
	pid  int

	mu     sync.Mutex
	ctl    *net.UnixConn
	rid    uint32
	done   bool
	closed bool
}

// Open proves ownership of the control socket at path: it connects,
// completes the hello exchange and takes the master's alive answer. A
// successful return is the event design §6.2 calls "the successful mux
// handshake", and it is the earliest moment at which anything may be
// published, minted or written on the far host.
func Open(path string) (*Master, error) {
	c, err := dialControl(path)
	if err != nil {
		return nil, err
	}
	m := &Master{path: path, ctl: c}
	if hErr := helloExchange(c); hErr != nil {
		_ = c.Close()
		return nil, hErr
	}
	pid, err := m.alive()
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	m.pid = pid
	return m, nil
}

// Path is the control socket this master was proven against.
func (m *Master) Path() string { return m.path }

// PID is the master process's id, as the master itself reported it. It is
// what distinguishes "the master process died" from "the socket file went"
// (design §6.2, three distinct events).
func (m *Master) PID() int { return m.pid }

// Alive re-checks the master on a fresh control connection and reports its
// pid. An error means the master is no longer answering — one of §6.2's
// three loss events, and the only one this package can observe.
func (m *Master) Alive() (int, error) {
	c, err := dialControl(m.path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = c.Close() }()
	if err := helloExchange(c); err != nil {
		return 0, err
	}
	return aliveOn(c, 1)
}

func (m *Master) alive() (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rid++
	return aliveOn(m.ctl, m.rid)
}

// Exit asks the master to terminate — `ssh -O exit`. It is the closing half
// of the ownership interval: the socket is removed only after the master's
// exit is confirmed, and confirming it is the caller's job because only the
// caller knows what "the last owned session" was.
//
// ON A FRESH CONTROL CONNECTION, exactly like Alive and for the same reason:
// this must keep working after the caller has released the connection Open
// took, and releasing it early is what lets the master exit at all (see
// Close).
func (m *Master) Exit() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.done {
		return nil
	}
	c, err := dialControl(m.path)
	if err != nil {
		// No socket to ask on is not a refusal: a master that cannot be
		// reached on its own socket is not running, which is the outcome
		// this asks for. The caller confirms the exit either way.
		m.done = true
		return nil
	}
	defer func() { _ = c.Close() }()
	if hErr := helloExchange(c); hErr != nil {
		return hErr
	}
	e := &encoder{}
	e.u32(cTerminate)
	e.u32(1)
	if wErr := writePacket(c, e.b); wErr != nil {
		return wErr
	}
	body, err := readPacket(c)
	if err != nil {
		// A master that exits without answering has still exited. That
		// is the outcome we asked for, so it is not an error.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			m.done = true
			return nil
		}
		return err
	}
	d := &decoder{b: body}
	switch typ := d.u32(); typ {
	case sOK:
		m.done = true
		return nil
	case sPermissionDenied, sFailure:
		_ = d.u32()
		return fmt.Errorf("mux: the master refused to exit: %s", d.str())
	default:
		return fmt.Errorf("mux: unexpected reply 0x%08x to the exit request", typ)
	}
}

// Close drops this client's control connection. It does NOT stop the master:
// the master is the user's own ssh process and its lifetime is theirs until
// the ownership interval closes.
//
// IT DOES, HOWEVER, DECIDE WHEN THAT LIFETIME CAN END, which is the opposite
// of what the paragraph above used to imply. An attached mux client is an
// open channel on the master's connection, and `ssh` does not exit while one
// is open — so holding this connection for the whole delivery kept the user's
// own `ssh` alive after their remote shell had exited, and their local prompt
// with it. Measured on 2026-08-21 in e2e/nocxify-journey.spec.ts: the far
// shell ended and the master was still there 20.1 s later; released at the
// terminal outcome instead, the same two events are 65 ms apart.
//
// Nothing else on this type needs it: Session and Alive each dial their own,
// and Exit does now too. So a caller may close this as soon as it stops
// needing the proof, and everything that follows still works.
//
// Idempotent: the release and the deferred cleanup are two different callers
// and neither knows about the other.
func (m *Master) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	return m.ctl.Close()
}

// Session opens one auxiliary channel on the master's connection.
//
// A refusal returns ErrSessionRefused and NOTHING ELSE HAPPENS: no retry, no
// second socket, and above all no connection of our own. That is the whole of
// D3, and it is why this package exists rather than an `sftp -o
// ControlMaster=auto`, which answers a refusal by authenticating again.
func (m *Master) Session(req SessionRequest) (*Session, error) {
	// Before the dial, so a refused command costs no socket and leaves no
	// trace on the master (nocx-e4ir3).
	if len(req.Command) >= MaxCommandLen {
		return nil, fmt.Errorf("%w: %d bytes, bound %d", ErrCommandTooLong, len(req.Command), MaxCommandLen)
	}
	c, err := dialControl(m.path)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = c.Close()
		}
	}()
	if hErr := helloExchange(c); hErr != nil {
		return nil, hErr
	}

	// The three descriptors the master will treat as the session's stdin,
	// stdout and stderr. Socketpairs rather than pipes: one end is handed
	// over and the other is ours, and a socketpair gives each side a real
	// descriptor the master can poll exactly as it polls a terminal's.
	stdinTheirs, stdinOurs, err := socketPair()
	if err != nil {
		return nil, err
	}
	stdoutTheirs, stdoutOurs, err := socketPair()
	if err != nil {
		closeAll(stdinTheirs, stdinOurs)
		return nil, err
	}
	stderrTheirs, stderrOurs, err := socketPair()
	if err != nil {
		closeAll(stdinTheirs, stdinOurs, stdoutTheirs, stdoutOurs)
		return nil, err
	}
	// The far ends belong to the master from the moment they are sent; our
	// copies are closed either way, so a refused request leaves no
	// descriptor behind.
	defer closeAll(stdinTheirs, stdoutTheirs, stderrTheirs)

	m.mu.Lock()
	m.rid++
	rid := m.rid
	m.mu.Unlock()

	e := &encoder{}
	e.u32(cNewSession)
	e.u32(rid)
	e.str("") // reserved
	// The four flags are documented as booleans and are UINT32 on the wire:
	// OpenSSH's master reads them with sshbuf_get_u32, and a one-byte
	// encoding is rejected as a malformed message (measured against
	// OpenSSH 10.5 — "mux_master_process_new_session: malformed message").
	e.flag(false) // want tty
	e.flag(false) // want X11 forwarding
	e.flag(false) // want agent forwarding
	e.flag(req.Subsystem)
	e.u32(0xffffffff) // no escape character: this channel carries no user input
	e.str("")         // no terminal type; there is no tty on an auxiliary channel
	e.str(req.Command)
	for _, kv := range req.Env {
		e.str(kv)
	}
	if wErr := writePacket(c, e.b); wErr != nil {
		closeAll(stdinOurs, stdoutOurs, stderrOurs)
		return nil, wErr
	}
	for _, f := range []*os.File{stdinTheirs, stdoutTheirs, stderrTheirs} {
		if fdErr := sendFD(c, f); fdErr != nil {
			closeAll(stdinOurs, stdoutOurs, stderrOurs)
			return nil, fmt.Errorf("mux: passing a session descriptor: %w", fdErr)
		}
	}

	body, err := readPacket(c)
	if err != nil {
		closeAll(stdinOurs, stdoutOurs, stderrOurs)
		return nil, err
	}
	d := &decoder{b: body}
	switch typ := d.u32(); typ {
	case sSessionOpened:
		_ = d.u32() // request id
		_ = d.u32() // session id
	case sPermissionDenied, sFailure:
		_ = d.u32()
		reason := d.str()
		closeAll(stdinOurs, stdoutOurs, stderrOurs)
		return nil, fmt.Errorf("%w: %s", ErrSessionRefused, reason)
	case sTTYAllocFail:
		closeAll(stdinOurs, stdoutOurs, stderrOurs)
		return nil, fmt.Errorf("%w: the master could not allocate a tty", ErrSessionRefused)
	default:
		closeAll(stdinOurs, stdoutOurs, stderrOurs)
		return nil, fmt.Errorf("mux: unexpected reply 0x%08x to the session request", typ)
	}

	ok = true
	return &Session{ctl: c, in: stdinOurs, out: stdoutOurs, errOut: stderrOurs}, nil
}

// Session is one auxiliary channel. Write reaches the far command's stdin and
// Read is its stdout, so the pair is the pipe an SFTP client speaks over.
type Session struct {
	ctl    *net.UnixConn
	in     *os.File
	out    *os.File
	errOut *os.File
	once   sync.Once
}

func (s *Session) Write(p []byte) (int, error) { return s.in.Write(p) }
func (s *Session) Read(p []byte) (int, error)  { return s.out.Read(p) }

// Stderr is the channel's error stream, for a caller that wants to report
// what the far side said when it failed.
func (s *Session) Stderr() io.Reader { return s.errOut }

// Close ends the auxiliary channel and its control connection. It is
// idempotent: the ownership interval closes once, whatever runs it.
func (s *Session) Close() error {
	var err error
	s.once.Do(func() {
		err = errors.Join(s.in.Close(), s.out.Close(), s.errOut.Close(), s.ctl.Close())
	})
	return err
}

// ---------------------------------------------------------------------------
// The wire

func dialControl(path string) (*net.UnixConn, error) {
	d := net.Dialer{Timeout: dialTimeout}
	c, err := d.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHandshake, err)
	}
	uc, ok := c.(*net.UnixConn)
	if !ok {
		_ = c.Close()
		return nil, fmt.Errorf("%w: %s is not a unix socket", ErrHandshake, path)
	}
	return uc, nil
}

func helloExchange(c *net.UnixConn) error {
	e := &encoder{}
	e.u32(msgHello)
	e.u32(protocolVersion)
	if err := writePacket(c, e.b); err != nil {
		return fmt.Errorf("%w: %w", ErrHandshake, err)
	}
	body, err := readPacket(c)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHandshake, err)
	}
	d := &decoder{b: body}
	if typ := d.u32(); typ != msgHello {
		return fmt.Errorf("%w: first message was 0x%08x, not a hello", ErrHandshake, typ)
	}
	if ver := d.u32(); ver != protocolVersion {
		return fmt.Errorf("%w: the master speaks version %d, this client speaks %d", ErrHandshake, ver, protocolVersion)
	}
	return nil
}

func aliveOn(c *net.UnixConn, rid uint32) (int, error) {
	e := &encoder{}
	e.u32(cAliveCheck)
	e.u32(rid)
	if err := writePacket(c, e.b); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrHandshake, err)
	}
	body, err := readPacket(c)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrHandshake, err)
	}
	d := &decoder{b: body}
	if typ := d.u32(); typ != sAlive {
		return 0, fmt.Errorf("%w: the alive check answered 0x%08x", ErrHandshake, typ)
	}
	_ = d.u32() // request id
	pid := d.u32()
	if d.err != nil {
		return 0, fmt.Errorf("%w: %w", ErrHandshake, d.err)
	}
	return int(pid), nil
}

func readPacket(c net.Conn) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(c, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n == 0 || n > maxPacket {
		return nil, fmt.Errorf("mux: packet length %d is outside 1..%d", n, maxPacket)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(c, body); err != nil {
		return nil, err
	}
	return body, nil
}

// writePacket is the one place a message length is decided, so it is the one
// place the bound is enforced: every string this package encodes ends up in a
// body that passes through here, and an over-long one is refused rather than
// truncated into a message that means something else.
func writePacket(c net.Conn, body []byte) error {
	if len(body) == 0 || len(body) > maxPacket {
		return fmt.Errorf("mux: message body is %d bytes, outside 1..%d", len(body), maxPacket)
	}
	buf := make([]byte, 0, 4+len(body))
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(body))) //nolint:gosec // bounded to maxPacket immediately above
	buf = append(buf, body...)
	_, err := c.Write(buf)
	return err
}

type encoder struct{ b []byte }

func (e *encoder) u32(v uint32) { e.b = binary.BigEndian.AppendUint32(e.b, v) }

// str writes an SSH string. The length is not bounded here because the body
// it lands in is: writePacket refuses anything past maxPacket, so an
// over-long string cannot leave this package.
func (e *encoder) str(s string) {
	e.u32(uint32(len(s))) //nolint:gosec // the enclosing body is bounded by writePacket
	e.b = append(e.b, s...)
}

// flag writes one of PROTOCOL.mux's "bool" fields, which are uint32 values.
func (e *encoder) flag(v bool) {
	if v {
		e.u32(1)
		return
	}
	e.u32(0)
}

type decoder struct {
	b   []byte
	err error
}

func (d *decoder) u32() uint32 {
	if d.err != nil || len(d.b) < 4 {
		d.err = errors.New("mux: message ended mid-value")
		return 0
	}
	v := binary.BigEndian.Uint32(d.b)
	d.b = d.b[4:]
	return v
}

func (d *decoder) str() string {
	n := d.u32()
	//nolint:gosec // d.b came from readPacket, which bounds it to maxPacket
	if d.err != nil || uint32(len(d.b)) < n {
		d.err = errors.New("mux: message ended mid-string")
		return ""
	}
	s := string(d.b[:n])
	d.b = d.b[n:]
	return s
}

func closeAll(fs ...*os.File) {
	for _, f := range fs {
		if f != nil {
			_ = f.Close()
		}
	}
}
